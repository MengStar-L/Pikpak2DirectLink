package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageSettingsBackupAndMigrationDeletion(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		DBFile:          filepath.Join(dir, "pikpak.db"),
		BackupDir:       filepath.Join(dir, "backups"),
		BackupInterval:  24 * time.Hour,
		BackupRetention: 7,
	}
	db, err := openDatabase(cfg.DBFile)
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	legacy := filepath.Join(dir, "legacy.json")
	if err := os.WriteFile(legacy, []byte(`{"password":"legacy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := prepareLegacyMigrationBackup(Config{DBFile: cfg.DBFile, AuthFile: legacy}, time.Now())
	if err != nil {
		t.Fatalf("prepareLegacyMigrationBackup: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO storage_migration_state(
		id,status,phase,backup_id,backup_path,started_at,completed_at,updated_at
	) VALUES(1,'backup_pending','complete',?,?,?,?,?)`, backup.ID, backup.Path, time.Now().Unix(), time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatalf("insert migration state: %v", err)
	}

	server := &Server{
		config:  cfg,
		db:      db,
		backups: newBackupManager(db, cfg.DBFile, cfg.BackupDir, cfg.BackupInterval, cfg.BackupRetention),
		nowFunc: time.Now,
	}

	recorder := httptest.NewRecorder()
	server.handleCreateStorageBackup(recorder, httptest.NewRequest(http.MethodPost, "/api/settings/storage/backups", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("backup status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created struct {
		Backup storageBackupRunResponse `json:"backup"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Backup.Status != backupStatusSuccess || created.Backup.SHA256 == "" {
		t.Fatalf("backup response = %+v", created.Backup)
	}

	recorder = httptest.NewRecorder()
	server.handleGetStorageSettings(recorder, httptest.NewRequest(http.MethodGet, "/api/settings/storage", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("storage status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var status storageStatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.LastSuccess == nil || status.Migration == nil || status.Migration.BackupID != backup.ID {
		t.Fatalf("storage status = %+v", status)
	}

	wrongBody := bytes.NewBufferString(`{"backup_id":"wrong"}`)
	recorder = httptest.NewRecorder()
	server.handleDeleteMigrationBackup(recorder, httptest.NewRequest(http.MethodDelete, "/api/settings/storage/migration-backup", wrongBody))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("wrong ID status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	body := bytes.NewBufferString(`{"backup_id":"` + backup.ID + `"}`)
	recorder = httptest.NewRecorder()
	server.handleDeleteMigrationBackup(recorder, httptest.NewRequest(http.MethodDelete, "/api/settings/storage/migration-backup", body))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(backup.Path); !os.IsNotExist(err) {
		t.Fatalf("migration backup still exists: %v", err)
	}
	var storedStatus, storedID string
	if err := db.QueryRow(`SELECT status,backup_id FROM storage_migration_state WHERE id=1`).Scan(&storedStatus, &storedID); err != nil {
		t.Fatal(err)
	}
	if storedStatus != "complete" || storedID != "" {
		t.Fatalf("migration state = %q %q", storedStatus, storedID)
	}
}

func TestMigrationBackupDeletePendingFinishesOnStartup(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DBFile: filepath.Join(dir, "pikpak.db")}
	db, err := openDatabase(cfg.DBFile)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(dir, "migration-backups", "pending")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "partial"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := db.Exec(`INSERT INTO storage_migration_state(
		id,status,phase,backup_id,backup_path,updated_at
	) VALUES(1,'delete_pending','complete','backup-id',?,?)`, path, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateLegacyStorage(db, cfg, newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef")), nil, now); err != nil {
		t.Fatalf("migrateLegacyStorage: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pending deletion still exists: %v", err)
	}
}
