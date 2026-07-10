package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const restoreSafetyDirectory = "restore-safety"

// StorageRestoreResult describes the files changed by an offline restore and
// where the pre-restore safety copy was saved.
type StorageRestoreResult struct {
	SafetyBackupPath string
	RestoredPaths    []string
}

type restoreOperation struct {
	kind           string
	source         string
	target         string
	staged         string
	checkSQLite    bool
	checkAppDB     bool
	containedIn    string
	expectedSize   int64
	expectedSHA256 string
	rollback       string
	hadOriginal    bool
}

// RestoreDatabase replaces dbPath with a verified standalone SQLite backup.
// The caller must stop the server before invoking it.
func RestoreDatabase(ctx context.Context, dbPath, backupPath string) (StorageRestoreResult, error) {
	target, err := absoluteDatabasePath(dbPath)
	if err != nil {
		return StorageRestoreResult{}, err
	}
	lock, err := acquireStorageFileLock(target)
	if err != nil {
		return StorageRestoreResult{}, err
	}
	defer lock.Close()
	if err := recoverInterruptedStorageRestore(Config{DBFile: target}); err != nil {
		return StorageRestoreResult{}, err
	}
	source, err := regularRestoreSource(backupPath)
	if err != nil {
		return StorageRestoreResult{}, fmt.Errorf("validate database backup: %w", err)
	}
	if err := ensureStandaloneSQLiteBackup(source); err != nil {
		return StorageRestoreResult{}, err
	}
	if samePath(source, target) || sameFile(source, target) {
		return StorageRestoreResult{}, errors.New("database backup and target database must be different files")
	}
	if err := checkApplicationDatabaseBackup(ctx, source); err != nil {
		return StorageRestoreResult{}, fmt.Errorf("verify database backup: %w", err)
	}
	digest, size, err := hashBackupFile(source)
	if err != nil {
		return StorageRestoreResult{}, fmt.Errorf("hash database backup: %w", err)
	}

	operations := []restoreOperation{
		{kind: "database", source: source, target: target, checkAppDB: true, expectedSize: size, expectedSHA256: digest},
		{kind: "database_wal", target: target + "-wal"},
		{kind: "database_shm", target: target + "-shm"},
	}
	return executeStorageRestore(ctx, Config{DBFile: target}, operations, restoreJournalModeDatabase)
}

// RestoreMigration restores the legacy files captured before the secure
// SQLite migration. Manifest destinations must match the current configured
// legacy layout so a modified manifest cannot overwrite arbitrary files.
func RestoreMigration(ctx context.Context, cfg Config, backupPath string) (StorageRestoreResult, error) {
	databasePath, err := absoluteDatabasePath(cfg.DBFile)
	if err != nil {
		return StorageRestoreResult{}, err
	}
	lock, err := acquireStorageFileLock(databasePath)
	if err != nil {
		return StorageRestoreResult{}, err
	}
	defer lock.Close()
	if err := recoverInterruptedStorageRestore(cfg); err != nil {
		return StorageRestoreResult{}, err
	}
	backupRoot, err := filepath.Abs(strings.TrimSpace(backupPath))
	if err != nil || strings.TrimSpace(backupPath) == "" {
		if err == nil {
			err = errors.New("migration backup directory is empty")
		}
		return StorageRestoreResult{}, err
	}
	backup, err := loadMigrationBackup(backupRoot)
	if err != nil {
		return StorageRestoreResult{}, fmt.Errorf("load migration backup: %w", err)
	}
	if len(backup.Files) == 0 {
		return StorageRestoreResult{}, errors.New("migration backup manifest contains no files")
	}
	if err := verifyMigrationBackup(backup); err != nil {
		return StorageRestoreResult{}, fmt.Errorf("verify migration backup: %w", err)
	}

	operations, hasDatabase, err := migrationRestoreOperations(cfg, backupRoot, backup)
	if err != nil {
		return StorageRestoreResult{}, err
	}
	if !hasDatabase {
		operations = append(operations, restoreOperation{kind: "database", target: databasePath})
	}
	operations = append(operations,
		restoreOperation{kind: "database_wal", target: databasePath + "-wal"},
		restoreOperation{kind: "database_shm", target: databasePath + "-shm"},
	)
	return executeStorageRestore(ctx, cfg, operations, restoreJournalModeMigration)
}

func migrationRestoreOperations(cfg Config, backupRoot string, backup *migrationBackup) ([]restoreOperation, bool, error) {
	canonicalBackupRoot, err := canonicalRestorePath(backupRoot)
	if err != nil {
		return nil, false, err
	}
	exactTargets := map[string]string{
		"database":          cfg.DBFile,
		"admin_auth":        cfg.AuthFile,
		"accounts":          cfg.AccountsFile,
		"bootstrap_session": cfg.SessionFile,
	}
	for kind, configured := range exactTargets {
		absolute, err := absoluteRestorePath(configured)
		if err != nil {
			return nil, false, fmt.Errorf("resolve configured %s path: %w", kind, err)
		}
		exactTargets[kind] = absolute
	}
	sessionRoot, err := absoluteRestorePath(cfg.AccountSessionDir)
	if err != nil {
		return nil, false, fmt.Errorf("resolve configured account session directory: %w", err)
	}

	seenTargets := make(map[string]struct{}, len(backup.Files))
	seenSessionTargets := make(map[string]struct{}, len(backup.Files))
	seenExactKinds := make(map[string]struct{}, len(exactTargets))
	operations := make([]restoreOperation, 0, len(backup.Files))
	hasDatabase := false
	for _, file := range backup.Files {
		if !filepath.IsAbs(file.OriginalPath) {
			return nil, false, fmt.Errorf("migration backup target for %s is not absolute", file.Kind)
		}
		target, err := filepath.Abs(file.OriginalPath)
		if err != nil {
			return nil, false, err
		}
		target = filepath.Clean(target)
		switch file.Kind {
		case "database", "admin_auth", "accounts", "bootstrap_session":
			if !samePath(target, exactTargets[file.Kind]) {
				return nil, false, fmt.Errorf("migration backup %s target %q does not match configured path %q", file.Kind, target, exactTargets[file.Kind])
			}
		case "account_session":
			inside, err := pathInsideDirectory(sessionRoot, target)
			if err != nil || !inside {
				return nil, false, fmt.Errorf("migration account session target %q is outside configured directory %q", target, sessionRoot)
			}
		default:
			return nil, false, fmt.Errorf("migration backup contains unsupported file kind %q", file.Kind)
		}
		canonicalTarget, err := canonicalRestorePath(target)
		if err != nil {
			return nil, false, err
		}
		insideBackup, err := pathInsideDirectory(canonicalBackupRoot, canonicalTarget)
		if err != nil {
			return nil, false, err
		}
		if insideBackup || samePath(canonicalBackupRoot, canonicalTarget) {
			return nil, false, fmt.Errorf("migration restore target %q is inside the backup directory", target)
		}
		key := comparablePath(canonicalTarget)
		if _, duplicate := seenTargets[key]; duplicate {
			return nil, false, fmt.Errorf("migration backup contains duplicate target %q", target)
		}
		seenTargets[key] = struct{}{}
		source := filepath.Join(backupRoot, filepath.FromSlash(file.RelativePath))
		operations = append(operations, restoreOperation{
			kind:           file.Kind,
			source:         source,
			target:         target,
			checkSQLite:    file.Kind == "database",
			expectedSize:   file.Size,
			expectedSHA256: file.SHA256,
		})
		if file.Kind == "account_session" {
			operations[len(operations)-1].containedIn = sessionRoot
			canonical, err := canonicalRestorePath(target)
			if err != nil {
				return nil, false, err
			}
			seenSessionTargets[comparablePath(canonical)] = struct{}{}
		} else {
			seenExactKinds[file.Kind] = struct{}{}
		}
		if file.Kind == "database" {
			hasDatabase = true
		}
	}
	for _, kind := range []string{"admin_auth", "accounts", "bootstrap_session"} {
		if _, exists := seenExactKinds[kind]; !exists {
			operations = append(operations, restoreOperation{kind: kind, target: exactTargets[kind]})
		}
	}

	resolvedSessionRoot, err := canonicalRestorePath(sessionRoot)
	if err != nil {
		return nil, false, fmt.Errorf("resolve configured account session directory: %w", err)
	}
	err = filepath.WalkDir(resolvedSessionRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if path == resolvedSessionRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("account session path %q is a symbolic link", path)
		}
		if entry.IsDir() {
			return validateRestoreContainment(resolvedSessionRoot, path)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("account session path %q is not a regular file", path)
		}
		canonical, err := canonicalRestorePath(path)
		if err != nil {
			return err
		}
		if _, exists := seenSessionTargets[comparablePath(canonical)]; !exists {
			operations = append(operations, restoreOperation{
				kind: "account_session", target: path, containedIn: resolvedSessionRoot,
			})
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	return operations, hasDatabase, nil
}

func executeStorageRestore(ctx context.Context, cfg Config, operations []restoreOperation, mode string) (StorageRestoreResult, error) {
	databasePath, err := absoluteDatabasePath(cfg.DBFile)
	if err != nil {
		return StorageRestoreResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return StorageRestoreResult{}, err
	}
	if err := validateRestoreOperations(operations); err != nil {
		return StorageRestoreResult{}, err
	}
	if err := ensureSQLiteWriteUnlocked(ctx, databasePath); err != nil {
		return StorageRestoreResult{}, err
	}
	id, err := newBackupRunID()
	if err != nil {
		return StorageRestoreResult{}, fmt.Errorf("generate restore id: %w", err)
	}
	if err := stageRestoreOperations(ctx, operations, id); err != nil {
		removeStagedRestoreFiles(operations)
		return StorageRestoreResult{}, err
	}
	defer removeStagedRestoreFiles(operations)

	safetyPath, err := createRestoreSafetyBackup(databasePath, operations, id, time.Now().UTC())
	if err != nil {
		return StorageRestoreResult{}, fmt.Errorf("create pre-restore safety backup: %w", err)
	}
	result := StorageRestoreResult{SafetyBackupPath: safetyPath}
	journalPath, err := prepareRestoreJournal(databasePath, operations, id, safetyPath, mode)
	if err != nil {
		return result, err
	}
	if err := applyRestoreOperations(operations, id, journalPath); err != nil {
		recoveryErr := recoverInterruptedStorageRestore(cfg)
		if result.SafetyBackupPath != "" {
			return result, errors.Join(
				fmt.Errorf("apply storage restore; pre-restore safety backup: %s: %w", result.SafetyBackupPath, err),
				recoveryErr,
			)
		}
		return result, errors.Join(err, recoveryErr)
	}
	for _, operation := range operations {
		if operation.source != "" {
			result.RestoredPaths = append(result.RestoredPaths, operation.target)
		}
	}
	return result, nil
}

func validateRestoreOperations(operations []restoreOperation) error {
	seen := make(map[string]struct{}, len(operations))
	targets := make([]string, 0, len(operations))
	for _, operation := range operations {
		if strings.TrimSpace(operation.target) == "" {
			return errors.New("restore target is empty")
		}
		if err := rejectFinalRestoreSymlink(operation.target); err != nil {
			return err
		}
		key := comparablePath(operation.target)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("restore target %q is duplicated", operation.target)
		}
		seen[key] = struct{}{}
		targets = append(targets, operation.target)
		if operation.containedIn != "" {
			if err := validateRestoreContainment(operation.containedIn, operation.target); err != nil {
				return err
			}
		}
	}
	for _, operation := range operations {
		if operation.source == "" {
			continue
		}
		for _, target := range targets {
			if samePath(operation.source, target) || sameFile(operation.source, target) {
				return fmt.Errorf("restore source %q conflicts with restore target %q", operation.source, target)
			}
		}
	}
	return nil
}

func stageRestoreOperations(ctx context.Context, operations []restoreOperation, id string) error {
	for index := range operations {
		operation := &operations[index]
		if operation.source == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := regularRestoreSource(operation.source); err != nil {
			return fmt.Errorf("validate %s restore source: %w", operation.kind, err)
		}
		if operation.checkAppDB {
			if err := ensureStandaloneSQLiteBackup(operation.source); err != nil {
				return err
			}
		}
		if operation.containedIn != "" {
			if err := validateRestoreContainment(operation.containedIn, operation.target); err != nil {
				return err
			}
		}
		if err := durableMkdirAll(filepath.Dir(operation.target), 0o700); err != nil {
			return fmt.Errorf("create restore target directory: %w", err)
		}
		operation.staged = filepath.Join(filepath.Dir(operation.target), "."+filepath.Base(operation.target)+".restore-new-"+id)
		size, digest, err := copyFileWithSHA256(operation.source, operation.staged)
		if err != nil {
			return fmt.Errorf("stage %s restore: %w", operation.kind, err)
		}
		if operation.expectedSHA256 != "" && (size != operation.expectedSize || digest != operation.expectedSHA256) {
			return fmt.Errorf("stage %s restore: source changed after verification", operation.kind)
		}
		if operation.checkAppDB {
			if err := ensureStandaloneSQLiteBackup(operation.source); err != nil {
				return err
			}
		}
		if err := syncBackupFile(operation.staged); err != nil {
			return fmt.Errorf("sync staged %s restore: %w", operation.kind, err)
		}
		if operation.checkAppDB {
			if err := checkApplicationDatabaseBackup(ctx, operation.staged); err != nil {
				return fmt.Errorf("verify staged %s restore: %w", operation.kind, err)
			}
		} else if operation.checkSQLite {
			if err := checkSQLiteBackup(ctx, operation.staged); err != nil {
				return fmt.Errorf("verify staged %s restore: %w", operation.kind, err)
			}
		}
	}
	return nil
}

func removeStagedRestoreFiles(operations []restoreOperation) {
	for _, operation := range operations {
		if operation.staged != "" {
			_ = os.Remove(operation.staged)
		}
	}
}

func createRestoreSafetyBackup(databasePath string, operations []restoreOperation, id string, now time.Time) (string, error) {
	type existingTarget struct {
		kind string
		path string
	}
	var existing []existingTarget
	for _, operation := range operations {
		info, err := os.Stat(operation.target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("pre-restore target is not a regular file: %s", operation.target)
		}
		existing = append(existing, existingTarget{kind: operation.kind, path: operation.target})
	}
	if len(existing) == 0 {
		return "", nil
	}
	sort.Slice(existing, func(i, j int) bool { return existing[i].path < existing[j].path })

	root := filepath.Join(filepath.Dir(databasePath), restoreSafetyDirectory)
	if err := durableMkdirAll(root, 0o700); err != nil {
		return "", err
	}
	name := "restore-" + now.Format("20060102T150405.000000000Z") + "-" + id
	finalPath := filepath.Join(root, name)
	tempPath := finalPath + ".tmp"
	if err := os.Mkdir(tempPath, 0o700); err != nil {
		return "", err
	}
	if err := syncDirectory(root); err != nil {
		return "", err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tempPath)
		}
	}()

	manifest := migrationBackup{ID: name, CreatedAt: now}
	for index, target := range existing {
		relative := filepath.Join("files", fmt.Sprintf("%04d-%s", index, filepath.Base(target.path)))
		copiedPath := filepath.Join(tempPath, relative)
		size, checksum, err := copyFileWithSHA256(target.path, copiedPath)
		if err != nil {
			return "", err
		}
		if err := syncBackupFile(copiedPath); err != nil {
			return "", err
		}
		manifest.Files = append(manifest.Files, migrationBackupFile{
			Kind:         "pre_restore_" + target.kind,
			OriginalPath: target.path,
			RelativePath: filepath.ToSlash(relative),
			Size:         size,
			SHA256:       checksum,
		})
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", err
	}
	manifestPath := filepath.Join(tempPath, migrationManifestName)
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		return "", err
	}
	if err := syncBackupFile(manifestPath); err != nil {
		return "", err
	}
	if err := syncDirectory(filepath.Join(tempPath, "files")); err != nil {
		return "", err
	}
	if err := syncDirectory(tempPath); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return "", err
	}
	if err := syncDirectory(root); err != nil {
		return "", err
	}
	cleanup = false
	return finalPath, nil
}

func applyRestoreOperations(operations []restoreOperation, id, journalPath string) error {
	if err := validateRestoreOperations(operations); err != nil {
		return err
	}
	type movedTarget struct {
		original string
		rollback string
	}
	var moved []movedTarget
	rollbackMoved := func() error {
		var rollbackErr error
		for index := len(moved) - 1; index >= 0; index-- {
			if err := os.Rename(moved[index].rollback, moved[index].original); err != nil {
				rollbackErr = errors.Join(rollbackErr, err)
			}
		}
		rollbackErr = errors.Join(rollbackErr, syncRestoreOperationDirectories(operations))
		return rollbackErr
	}

	for _, operation := range operations {
		if _, err := os.Stat(operation.target); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			_ = rollbackMoved()
			return fmt.Errorf("inspect restore target %q: %w", operation.target, err)
		}
		rollbackPath := operation.rollback
		if rollbackPath == "" {
			rollbackPath = filepath.Join(filepath.Dir(operation.target), "."+filepath.Base(operation.target)+".restore-old-"+id)
		}
		if err := os.Rename(operation.target, rollbackPath); err != nil {
			rollbackErr := rollbackMoved()
			return errors.Join(fmt.Errorf("move current restore target %q: %w", operation.target, err), rollbackErr)
		}
		moved = append(moved, movedTarget{original: operation.target, rollback: rollbackPath})
	}
	if err := syncRestoreOperationDirectories(operations); err != nil {
		return errors.Join(fmt.Errorf("sync restore rollback publication: %w", err), rollbackMoved())
	}

	var published []string
	for _, operation := range operations {
		if operation.staged == "" {
			continue
		}
		if err := os.Rename(operation.staged, operation.target); err != nil {
			var rollbackErr error
			for index := len(published) - 1; index >= 0; index-- {
				if removeErr := os.Remove(published[index]); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					rollbackErr = errors.Join(rollbackErr, removeErr)
				}
			}
			rollbackErr = errors.Join(rollbackErr, rollbackMoved())
			return errors.Join(fmt.Errorf("publish restored target %q: %w", operation.target, err), rollbackErr)
		}
		published = append(published, operation.target)
	}
	if err := syncRestoreOperationDirectories(operations); err != nil {
		var rollbackErr error
		for index := len(published) - 1; index >= 0; index-- {
			if removeErr := os.Remove(published[index]); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				rollbackErr = errors.Join(rollbackErr, removeErr)
			}
		}
		rollbackErr = errors.Join(rollbackErr, rollbackMoved())
		return errors.Join(fmt.Errorf("sync restored targets: %w", err), rollbackErr)
	}
	if err := markRestoreJournalCommitted(journalPath, id); err != nil {
		return err
	}

	var cleanupErr error
	for _, target := range moved {
		if err := os.Remove(target.rollback); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if cleanupErr != nil {
		return fmt.Errorf("restore completed but temporary file cleanup failed: %w", cleanupErr)
	}
	if err := syncRestoreOperationDirectories(operations); err != nil {
		return fmt.Errorf("restore completed but directory sync failed: %w", err)
	}
	return removeRestoreJournal(journalPath)
}

func syncRestoreOperationDirectories(operations []restoreOperation) error {
	directories := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		directories[filepath.Dir(operation.target)] = struct{}{}
	}
	var syncErr error
	for directory := range directories {
		syncErr = errors.Join(syncErr, syncDirectory(directory))
	}
	return syncErr
}

func regularRestoreSource(path string) (string, error) {
	absPath, err := absoluteRestorePath(path)
	if err != nil {
		return "", err
	}
	if err := rejectFinalRestoreSymlink(absPath); err != nil {
		return "", err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("restore source is not a regular file: %s", absPath)
	}
	return absPath, nil
}

func ensureStandaloneSQLiteBackup(path string) error {
	walPath := path + "-wal"
	info, err := os.Stat(walPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		return fmt.Errorf("database backup is not standalone: remove or checkpoint %s", walPath)
	}
	return nil
}

func absoluteDatabasePath(path string) (string, error) {
	if strings.TrimSpace(path) == ":memory:" {
		return "", errors.New("offline restore requires a disk database")
	}
	path, err := absoluteRestorePath(path)
	if err != nil {
		return "", fmt.Errorf("resolve target database path: %w", err)
	}
	return path, nil
}

func absoluteRestorePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is empty")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absPath), nil
}

func pathInsideDirectory(root, path string) (bool, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, nil
	}
	return true, nil
}

func validateRestoreContainment(root, target string) error {
	inside, err := pathInsideDirectory(root, target)
	if err != nil {
		return err
	}
	if !inside {
		return fmt.Errorf("restore target %q is outside configured directory %q", target, root)
	}
	canonicalRoot, err := canonicalRestorePath(root)
	if err != nil {
		return err
	}
	canonicalTarget, err := canonicalRestorePath(target)
	if err != nil {
		return err
	}
	inside, err = pathInsideDirectory(canonicalRoot, canonicalTarget)
	if err != nil {
		return err
	}
	if !inside {
		return fmt.Errorf("unsafe restore target %q resolves outside %q", target, root)
	}
	if err := rejectFinalRestoreSymlink(target); err != nil {
		return err
	}
	return nil
}

func canonicalRestorePath(path string) (string, error) {
	current, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %q", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func rejectFinalRestoreSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("restore path %q is a symbolic link", path)
	}
	return nil
}

func comparablePath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func samePath(a, b string) bool {
	return comparablePath(a) == comparablePath(b)
}

func sameFile(a, b string) bool {
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	return aErr == nil && bErr == nil && os.SameFile(aInfo, bInfo)
}
