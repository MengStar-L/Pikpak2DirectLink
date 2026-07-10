package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareLegacyMigrationBackupCapturesAndVerifiesSources(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		DBFile:            filepath.Join(dir, "pikpak.db"),
		AuthFile:          filepath.Join(dir, "auth.json"),
		AccountsFile:      filepath.Join(dir, "accounts.json"),
		SessionFile:       filepath.Join(sessions, "bootstrap.json"),
		AccountSessionDir: sessions,
	}
	fixtures := map[string]string{
		cfg.DBFile:                              "database-before-migration",
		cfg.AuthFile:                            "admin-hash",
		cfg.AccountsFile:                        "account-password",
		cfg.SessionFile:                         "bootstrap-refresh-token",
		filepath.Join(sessions, "account.json"): "account-refresh-token",
	}
	for path, content := range fixtures {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	backup, err := prepareLegacyMigrationBackup(cfg, now)
	if err != nil {
		t.Fatalf("prepare backup: %v", err)
	}
	if backup == nil || backup.CreatedAt != now {
		t.Fatalf("unexpected backup: %+v", backup)
	}
	if len(backup.Files) != len(fixtures) {
		t.Fatalf("backup file count=%d want=%d", len(backup.Files), len(fixtures))
	}
	if err := verifyMigrationBackup(backup); err != nil {
		t.Fatalf("verify backup: %v", err)
	}

	reused, err := prepareLegacyMigrationBackup(cfg, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("reuse backup: %v", err)
	}
	if reused.ID != backup.ID {
		t.Fatalf("created duplicate backup %q, want %q", reused.ID, backup.ID)
	}
}

func TestVerifyMigrationBackupDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DBFile: filepath.Join(dir, "pikpak.db")}
	if err := os.WriteFile(cfg.DBFile, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := prepareLegacyMigrationBackup(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(backup.Path, filepath.FromSlash(backup.Files[0].RelativePath))
	if err := os.WriteFile(target, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyMigrationBackup(backup); err == nil {
		t.Fatal("tampered migration backup unexpectedly verified")
	}
}

func TestPrepareLegacyMigrationBackupReturnsNilWithoutSources(t *testing.T) {
	dir := t.TempDir()
	backup, err := prepareLegacyMigrationBackup(Config{
		DBFile:            filepath.Join(dir, "missing.db"),
		AuthFile:          filepath.Join(dir, "missing-auth.json"),
		AccountsFile:      filepath.Join(dir, "missing-accounts.json"),
		SessionFile:       filepath.Join(dir, "missing-session.json"),
		AccountSessionDir: filepath.Join(dir, "missing-sessions"),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if backup != nil {
		t.Fatalf("unexpected backup: %+v", backup)
	}
}
