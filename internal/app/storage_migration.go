package app

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const migrationManifestName = "manifest.json"

type migrationBackup struct {
	ID        string                `json:"id"`
	Path      string                `json:"-"`
	CreatedAt time.Time             `json:"created_at"`
	Files     []migrationBackupFile `json:"files"`
}

type migrationBackupFile struct {
	Kind         string `json:"kind"`
	OriginalPath string `json:"original_path"`
	RelativePath string `json:"relative_path"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
}

func migrateLegacyStorage(db *sql.DB, cfg Config, cipher *SecretCipher, backup *migrationBackup, now time.Time) (*migrationBackup, error) {
	var status, backupPath string
	err := db.QueryRow(`SELECT status,backup_path FROM storage_migration_state WHERE id=1`).Scan(&status, &backupPath)
	if err == nil {
		if status == "delete_pending" {
			if err := finishMigrationBackupDeletion(db, cfg, backupPath, now); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return finishLegacyQuarantine(db, backupPath)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	records, sessions, err := loadLegacyAccounts(cfg, now)
	if err != nil {
		return nil, err
	}
	legacyCredential, err := loadLegacyAdminCredential(cfg.AuthFile)
	if err != nil {
		return nil, err
	}
	store := newAccountStore(db, cipher)

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := store.importLegacyTx(tx, records, sessions); err != nil {
		return nil, err
	}
	if legacyCredential != nil {
		var credentialCount int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM admin_credentials`).Scan(&credentialCount); err != nil {
			return nil, err
		}
		if credentialCount == 0 {
			raw, err := json.Marshal(legacyCredential)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(
				`INSERT INTO admin_credentials(id,password_hash,updated_at) VALUES(1,?,?)`,
				string(raw), legacyCredential.UpdatedAt.Unix(),
			); err != nil {
				return nil, err
			}
		}
	}
	status = "complete"
	backupID := ""
	backupPath = ""
	if backup != nil {
		status = "backup_pending"
		backupID = backup.ID
		backupPath = backup.Path
	}
	if _, err := tx.Exec(
		`INSERT INTO storage_migration_state(
			id,status,phase,backup_id,backup_path,last_error,started_at,completed_at,updated_at
		) VALUES(1,?,'complete',?,?, '',?,?,?)`,
		status, backupID, backupPath, now.Unix(), now.Unix(), now.Unix(),
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return finishLegacyQuarantine(db, backupPath)
}

func loadLegacyAdminCredential(path string) (*credentialRecord, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rec credentialRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	if rec.Hash == "" || rec.Salt == "" {
		return nil, errors.New("legacy admin credential is incomplete")
	}
	return &rec, nil
}

func loadLegacyAccounts(cfg Config, now time.Time) ([]accountRecord, map[string][]byte, error) {
	data, err := os.ReadFile(cfg.AccountsFile)
	if errors.Is(err, os.ErrNotExist) {
		return legacyBootstrapAccount(cfg, now)
	}
	if err != nil {
		return nil, nil, err
	}
	var records []accountRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, nil, err
	}
	if len(records) == 0 && cfg.IsConfigured() {
		return legacyBootstrapAccount(cfg, now)
	}
	seen := make(map[string]struct{}, len(records))
	sessions := make(map[string][]byte)
	for index := range records {
		record := &records[index]
		record.ID = strings.TrimSpace(record.ID)
		record.Username = strings.TrimSpace(record.Username)
		if record.ID == "" || record.Username == "" {
			return nil, nil, fmt.Errorf("legacy account %d has an empty id or username", index)
		}
		if _, duplicate := seen[record.ID]; duplicate {
			return nil, nil, fmt.Errorf("legacy accounts contain duplicate id %q", record.ID)
		}
		seen[record.ID] = struct{}{}
		if record.SessionFile == "" {
			record.SessionFile = filepath.Join(cfg.AccountSessionDir, record.ID+".json")
		}
		if record.Status == "" {
			record.Status = AccountAvailable
		}
		if record.TrafficLimit <= 0 {
			record.TrafficLimit = defaultAccountTraffic
		}
		if record.TrafficPeriod == "" {
			record.TrafficPeriod = monthKey(now)
		}
		if record.CreatedAt.IsZero() {
			record.CreatedAt = now
		}
		if record.UpdatedAt.IsZero() {
			record.UpdatedAt = record.CreatedAt
		}
		session, err := os.ReadFile(record.SessionFile)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		sessions[record.ID] = session
	}
	return records, sessions, nil
}

func legacyBootstrapAccount(cfg Config, now time.Time) ([]accountRecord, map[string][]byte, error) {
	if !cfg.IsConfigured() {
		return nil, nil, nil
	}
	id := accountIDForUsername(cfg.Username)
	record := accountRecord{
		ID:                    id,
		Username:              strings.TrimSpace(cfg.Username),
		Password:              cfg.Password,
		SessionFile:           cfg.SessionFile,
		Status:                AccountAvailable,
		TrafficLimit:          defaultAccountTraffic,
		TrafficPeriod:         monthKey(now),
		CredentialNextCheckAt: formatAccountTime(now),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	sessions := make(map[string][]byte)
	data, err := os.ReadFile(cfg.SessionFile)
	if err == nil {
		sessions[id] = data
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	return []accountRecord{record}, sessions, nil
}

func finishLegacyQuarantine(db *sql.DB, backupPath string) (*migrationBackup, error) {
	if strings.TrimSpace(backupPath) == "" {
		return nil, nil
	}
	backup, err := loadMigrationBackup(backupPath)
	if err != nil {
		return nil, err
	}
	if err := verifyMigrationBackup(backup); err != nil {
		return nil, err
	}
	for _, file := range backup.Files {
		if file.Kind == "database" {
			continue
		}
		data, err := os.ReadFile(file.OriginalPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		if int64(len(data)) != file.Size || hex.EncodeToString(digest[:]) != file.SHA256 {
			return nil, fmt.Errorf("legacy file changed after backup: %s", file.OriginalPath)
		}
		if err := os.Remove(file.OriginalPath); err != nil {
			return nil, err
		}
	}
	return backup, nil
}

func storageMigrationRecorded(dbPath string) (bool, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='storage_migration_state'`).Scan(&tableCount); err != nil {
		return false, err
	}
	if tableCount == 0 {
		return false, nil
	}
	var status string
	err = db.QueryRow(`SELECT status FROM storage_migration_state WHERE id=1`).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "backup_pending" || status == "delete_pending" || status == "complete", nil
}

func prepareLegacyMigrationBackup(cfg Config, now time.Time) (*migrationBackup, error) {
	sources, err := legacyMigrationSources(cfg)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, nil
	}

	root := filepath.Join(filepath.Dir(cfg.DBFile), "migration-backups")
	pending := filepath.Join(root, "pending")
	if existing, err := loadMigrationBackup(pending); err == nil {
		if err := verifyMigrationBackup(existing); err != nil {
			return nil, fmt.Errorf("verify pending migration backup: %w", err)
		}
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.RemoveAll(pending); err != nil {
		return nil, err
	}

	if err := durableMkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	randomID, err := generateSessionID()
	if err != nil {
		return nil, err
	}
	backup := &migrationBackup{
		ID:        "migration-" + now.UTC().Format("20060102T150405Z") + "-" + randomID[:12],
		CreatedAt: now.UTC(),
	}
	if err := os.Mkdir(pending, 0o700); err != nil {
		return nil, err
	}
	if err := syncDirectory(root); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(pending)
		}
	}()

	for index, source := range sources {
		relative := filepath.Join("files", fmt.Sprintf("%04d-%s", index, filepath.Base(source.path)))
		target := filepath.Join(pending, relative)
		size, checksum, err := copyFileWithSHA256(source.path, target)
		if err != nil {
			return nil, fmt.Errorf("backup %s: %w", source.path, err)
		}
		if err := syncBackupFile(target); err != nil {
			return nil, err
		}
		backup.Files = append(backup.Files, migrationBackupFile{
			Kind:         source.kind,
			OriginalPath: source.path,
			RelativePath: filepath.ToSlash(relative),
			Size:         size,
			SHA256:       checksum,
		})
	}
	manifest, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestTemp := filepath.Join(pending, ".manifest.tmp")
	if err := os.WriteFile(manifestTemp, manifest, 0o600); err != nil {
		return nil, err
	}
	if err := syncBackupFile(manifestTemp); err != nil {
		return nil, err
	}
	if err := syncDirectory(filepath.Join(pending, "files")); err != nil {
		return nil, err
	}
	if err := os.Rename(manifestTemp, filepath.Join(pending, migrationManifestName)); err != nil {
		return nil, err
	}
	if err := syncDirectory(pending); err != nil {
		return nil, err
	}
	cleanup = false
	backup.Path = pending
	if err := verifyMigrationBackup(backup); err != nil {
		return nil, err
	}
	return backup, nil
}

type migrationSource struct {
	kind string
	path string
}

func legacyMigrationSources(cfg Config) ([]migrationSource, error) {
	var candidates []migrationSource
	add := func(kind, path string) {
		path = strings.TrimSpace(path)
		if path != "" {
			candidates = append(candidates, migrationSource{kind: kind, path: path})
		}
	}
	add("database", cfg.DBFile)
	add("admin_auth", cfg.AuthFile)
	add("accounts", cfg.AccountsFile)
	add("bootstrap_session", cfg.SessionFile)
	if dir := strings.TrimSpace(cfg.AccountSessionDir); dir != "" {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				add("account_session", path)
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}

	seen := make(map[string]struct{})
	out := make([]migrationSource, 0, len(candidates))
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate.path)
		if err != nil {
			return nil, err
		}
		absolute = filepath.Clean(absolute)
		if _, ok := seen[strings.ToLower(absolute)]; ok {
			continue
		}
		info, err := os.Stat(absolute)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		seen[strings.ToLower(absolute)] = struct{}{}
		candidate.path = absolute
		out = append(out, candidate)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].kind != out[j].kind {
			return out[i].kind < out[j].kind
		}
		return out[i].path < out[j].path
	})
	return out, nil
}

func copyFileWithSHA256(source, target string) (int64, string, error) {
	input, err := os.Open(source)
	if err != nil {
		return 0, "", err
	}
	defer input.Close()
	if err := durableMkdirAll(filepath.Dir(target), 0o700); err != nil {
		return 0, "", err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	closeErr := output.Close()
	if copyErr != nil {
		return 0, "", copyErr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func loadMigrationBackup(path string) (*migrationBackup, error) {
	data, err := os.ReadFile(filepath.Join(path, migrationManifestName))
	if err != nil {
		return nil, err
	}
	var backup migrationBackup
	if err := json.Unmarshal(data, &backup); err != nil {
		return nil, err
	}
	backup.Path = path
	return &backup, nil
}

func verifyMigrationBackup(backup *migrationBackup) error {
	if backup == nil || strings.TrimSpace(backup.ID) == "" || strings.TrimSpace(backup.Path) == "" {
		return errors.New("migration backup manifest is incomplete")
	}
	root, err := filepath.Abs(backup.Path)
	if err != nil {
		return err
	}
	for _, file := range backup.Files {
		target := filepath.Join(root, filepath.FromSlash(file.RelativePath))
		absolute, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("migration backup contains an unsafe relative path")
		}
		if err := validateRestoreContainment(root, absolute); err != nil {
			return fmt.Errorf("migration backup contains an unsafe path: %w", err)
		}
		digest, size, err := hashBackupFile(absolute)
		if err != nil {
			return err
		}
		if size != file.Size {
			return fmt.Errorf("backup size mismatch for %s", file.RelativePath)
		}
		if digest != file.SHA256 {
			return fmt.Errorf("backup checksum mismatch for %s", file.RelativePath)
		}
	}
	return nil
}
