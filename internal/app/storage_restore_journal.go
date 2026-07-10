package app

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	restoreJournalVersion       = 1
	restoreJournalFilename      = ".storage-restore-journal.json"
	restoreJournalModeDatabase  = "database"
	restoreJournalModeMigration = "migration"
)

type storageRestoreJournal struct {
	Version          int
	ID               string
	Mode             string
	DatabasePath     string
	SafetyBackupPath string
	Operations       []storageRestoreJournalOperation
}

type storageRestoreJournalOperation struct {
	Kind        string
	Target      string
	Staged      string
	Rollback    string
	HadOriginal bool
}

func prepareRestoreJournal(databasePath string, operations []restoreOperation, id, safetyPath, mode string) (string, error) {
	databasePath, err := absoluteDatabasePath(databasePath)
	if err != nil {
		return "", err
	}
	journalPath := filepath.Join(filepath.Dir(databasePath), restoreJournalFilename)
	journal := storageRestoreJournal{
		Version: restoreJournalVersion, ID: id, Mode: mode, DatabasePath: databasePath,
		SafetyBackupPath: safetyPath,
		Operations:       make([]storageRestoreJournalOperation, 0, len(operations)),
	}
	for index := range operations {
		operation := &operations[index]
		_, err := os.Lstat(operation.target)
		switch {
		case err == nil:
			operation.hadOriginal = true
		case errors.Is(err, os.ErrNotExist):
			operation.hadOriginal = false
		default:
			return "", err
		}
		operation.rollback = filepath.Join(
			filepath.Dir(operation.target),
			"."+filepath.Base(operation.target)+".restore-old-"+id,
		)
		if _, err := os.Lstat(operation.rollback); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return "", fmt.Errorf("restore rollback path already exists: %s", operation.rollback)
			}
			return "", err
		}
		journal.Operations = append(journal.Operations, storageRestoreJournalOperation{
			Kind: operation.kind, Target: operation.target, Staged: operation.staged, Rollback: operation.rollback,
			HadOriginal: operation.hadOriginal,
		})
	}
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return "", err
	}
	tempPath := journalPath + ".tmp-" + id
	if err := writeSyncedExclusiveFile(tempPath, data); err != nil {
		return "", fmt.Errorf("write restore journal: %w", err)
	}
	defer os.Remove(tempPath)
	if err := os.Rename(tempPath, journalPath); err != nil {
		return "", fmt.Errorf("publish restore journal: %w", err)
	}
	if err := syncDirectory(filepath.Dir(journalPath)); err != nil {
		return "", err
	}
	return journalPath, nil
}

func markRestoreJournalCommitted(journalPath, id string) error {
	marker := restoreJournalCommitPath(journalPath)
	if err := writeSyncedExclusiveFile(marker, []byte(id+"\n")); err != nil {
		return fmt.Errorf("commit restore journal: %w", err)
	}
	return syncDirectory(filepath.Dir(journalPath))
}

func recoverInterruptedStorageRestore(cfg Config) error {
	databasePath, err := absoluteDatabasePath(cfg.DBFile)
	if err != nil {
		return err
	}
	journalPath := filepath.Join(filepath.Dir(databasePath), restoreJournalFilename)
	data, err := os.ReadFile(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		if removeErr := os.Remove(restoreJournalCommitPath(journalPath)); removeErr == nil {
			_ = syncDirectory(filepath.Dir(journalPath))
		}
		return nil
	}
	if err != nil {
		return err
	}
	var journal storageRestoreJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return fmt.Errorf("read restore journal %s: %w", journalPath, err)
	}
	if err := validateRestoreJournal(journal, cfg, databasePath); err != nil {
		return err
	}
	markerData, markerErr := os.ReadFile(restoreJournalCommitPath(journalPath))
	committed := markerErr == nil && strings.TrimSpace(string(markerData)) == journal.ID
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return markerErr
	}

	operations := make([]restoreOperation, 0, len(journal.Operations))
	for _, operation := range journal.Operations {
		operations = append(operations, restoreOperation{target: operation.Target})
	}
	for index := len(journal.Operations) - 1; index >= 0; index-- {
		operation := journal.Operations[index]
		if !committed {
			if operation.HadOriginal {
				if _, err := os.Lstat(operation.Rollback); err == nil {
					if err := removeRestoreArtifact(operation.Target); err != nil {
						return restoreRecoveryError(journal, err)
					}
					if err := os.Rename(operation.Rollback, operation.Target); err != nil {
						return restoreRecoveryError(journal, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					return restoreRecoveryError(journal, err)
				} else if _, targetErr := os.Lstat(operation.Target); errors.Is(targetErr, os.ErrNotExist) {
					return restoreRecoveryError(journal, fmt.Errorf("both target and rollback are missing: %s", operation.Target))
				}
			} else if err := removeRestoreArtifact(operation.Target); err != nil {
				return restoreRecoveryError(journal, err)
			}
		} else if err := removeRestoreArtifact(operation.Rollback); err != nil {
			return restoreRecoveryError(journal, err)
		}
		if err := removeRestoreArtifact(operation.Staged); err != nil {
			return restoreRecoveryError(journal, err)
		}
	}
	if err := syncRestoreOperationDirectories(operations); err != nil {
		return restoreRecoveryError(journal, err)
	}
	return removeRestoreJournal(journalPath)
}

func validateRestoreJournal(journal storageRestoreJournal, cfg Config, databasePath string) error {
	if journal.Version != restoreJournalVersion || strings.TrimSpace(journal.ID) == "" || len(journal.Operations) == 0 {
		return errors.New("restore journal is incomplete")
	}
	if len(journal.ID) != 24 {
		return errors.New("restore journal has an invalid id")
	}
	if _, err := hex.DecodeString(journal.ID); err != nil {
		return errors.New("restore journal has an invalid id")
	}
	if journal.Mode != restoreJournalModeDatabase && journal.Mode != restoreJournalModeMigration {
		return errors.New("restore journal has an invalid mode")
	}
	journalDB, err := absoluteDatabasePath(journal.DatabasePath)
	if err != nil || !samePath(journalDB, databasePath) {
		return errors.New("restore journal belongs to a different database")
	}
	seen := make(map[string]struct{}, len(journal.Operations))
	for _, operation := range journal.Operations {
		if !filepath.IsAbs(operation.Target) {
			return errors.New("restore journal contains a relative target")
		}
		if err := validateRestoreJournalTarget(journal.Mode, cfg, databasePath, operation); err != nil {
			return err
		}
		canonical, err := canonicalRestorePath(operation.Target)
		if err != nil {
			return err
		}
		key := comparablePath(canonical)
		if _, duplicate := seen[key]; duplicate {
			return errors.New("restore journal contains duplicate targets")
		}
		seen[key] = struct{}{}
		expectedRollback := filepath.Join(filepath.Dir(operation.Target), "."+filepath.Base(operation.Target)+".restore-old-"+journal.ID)
		if !samePath(expectedRollback, operation.Rollback) {
			return errors.New("restore journal contains an unexpected rollback path")
		}
		if operation.Staged != "" {
			expectedStaged := filepath.Join(filepath.Dir(operation.Target), "."+filepath.Base(operation.Target)+".restore-new-"+journal.ID)
			if !samePath(expectedStaged, operation.Staged) {
				return errors.New("restore journal contains an unexpected staged path")
			}
		}
	}
	return nil
}

func validateRestoreJournalTarget(mode string, cfg Config, databasePath string, operation storageRestoreJournalOperation) error {
	target, err := absoluteRestorePath(operation.Target)
	if err != nil {
		return err
	}
	databaseTargets := map[string]string{
		"database": databasePath, "database_wal": databasePath + "-wal", "database_shm": databasePath + "-shm",
	}
	if expected, ok := databaseTargets[operation.Kind]; ok {
		if !samePath(target, expected) {
			return errors.New("restore journal database target is not allowed")
		}
		return nil
	}
	if mode != restoreJournalModeMigration {
		return errors.New("restore journal contains a non-database target")
	}
	fixed := map[string]string{
		"admin_auth": cfg.AuthFile, "accounts": cfg.AccountsFile, "bootstrap_session": cfg.SessionFile,
	}
	if configured, ok := fixed[operation.Kind]; ok {
		expected, err := absoluteRestorePath(configured)
		if err != nil || !samePath(target, expected) {
			return errors.New("restore journal legacy target is not allowed")
		}
		return nil
	}
	if operation.Kind != "account_session" {
		return errors.New("restore journal contains an unknown target kind")
	}
	root, err := absoluteRestorePath(cfg.AccountSessionDir)
	if err != nil {
		return err
	}
	return validateRestoreContainment(root, target)
}

func removeRestoreArtifact(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func removeRestoreJournal(journalPath string) error {
	if err := removeRestoreArtifact(journalPath); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(journalPath)); err != nil {
		return err
	}
	if err := removeRestoreArtifact(restoreJournalCommitPath(journalPath)); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(journalPath))
}

func restoreJournalCommitPath(journalPath string) string {
	return journalPath + ".committed"
}

func restoreRecoveryError(journal storageRestoreJournal, err error) error {
	if journal.SafetyBackupPath != "" {
		return fmt.Errorf("recover interrupted restore; safety backup: %s: %w", journal.SafetyBackupPath, err)
	}
	return fmt.Errorf("recover interrupted restore: %w", err)
}

func writeSyncedExclusiveFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
