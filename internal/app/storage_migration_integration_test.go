package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewServerMigratesLegacyAuthAccountsAndSessions(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "accounts")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(dir, "auth.json")
	legacyCredential, err := newCredentialStore(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyCredential.Set("legacy-admin-password"); err != nil {
		t.Fatal(err)
	}

	accountID := accountIDForUsername("legacy@example.com")
	sessionPath := filepath.Join(sessionDir, accountID+".json")
	sessionJSON := `{"username":"legacy@example.com","access_token":"legacy-access-token","refresh_token":"legacy-refresh-token","user_id":"pikpak-user","device_id":"device","expires_at":"2099-01-01T00:00:00Z"}`
	if err := os.WriteFile(sessionPath, []byte(sessionJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	records := []accountRecord{{
		ID: accountID, Username: "legacy@example.com", Password: "legacy-account-password",
		SessionFile: sessionPath, Status: AccountAvailable, TrafficLimit: defaultAccountTraffic,
		TrafficPeriod: monthKey(now), CreatedAt: now, UpdatedAt: now,
	}}
	accountsJSON, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	accountsPath := filepath.Join(dir, "accounts.json")
	if err := os.WriteFile(accountsPath, accountsJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		RootFolderName:    "TestRoot",
		AccountsFile:      accountsPath,
		AccountSessionDir: sessionDir,
		SessionFile:       filepath.Join(dir, "bootstrap.json"),
		AuthFile:          authPath,
		DBFile:            filepath.Join(dir, "pikpak.db"),
		DataEncryptionKey: testDataEncryptionKey,
		RequestTimeout:    time.Second,
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("first NewServer: %v", err)
	}
	if !srv.creds.Verify("legacy-admin-password") {
		t.Fatal("legacy admin password was not imported")
	}
	accounts := srv.accounts.List()
	if len(accounts) != 1 || accounts[0].Username != "legacy@example.com" || !accounts[0].LoggedIn {
		t.Fatalf("migrated accounts = %+v", accounts)
	}
	for _, path := range []string{authPath, accountsPath, sessionPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy plaintext file remains active: %s (err=%v)", path, err)
		}
	}
	var encryptedPassword, encryptedSession string
	if err := srv.db.QueryRow(`SELECT password_encrypted FROM pikpak_accounts WHERE id=?`, accountID).Scan(&encryptedPassword); err != nil {
		t.Fatal(err)
	}
	if err := srv.db.QueryRow(`SELECT session_encrypted FROM pikpak_account_sessions WHERE account_id=?`, accountID).Scan(&encryptedSession); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encryptedPassword, "legacy-account-password") || strings.Contains(encryptedSession, "legacy-refresh-token") {
		t.Fatal("legacy secret remains plaintext in SQLite")
	}
	var migrationStatus, backupPath string
	if err := srv.db.QueryRow(`SELECT status,backup_path FROM storage_migration_state WHERE id=1`).Scan(&migrationStatus, &backupPath); err != nil {
		t.Fatal(err)
	}
	if migrationStatus != "backup_pending" || backupPath == "" {
		t.Fatalf("migration status=%q backup=%q", migrationStatus, backupPath)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("reopen NewServer: %v", err)
	}
	defer reopened.Close()
	if got := reopened.accounts.List(); len(got) != 1 || got[0].Username != "legacy@example.com" {
		t.Fatalf("accounts after restart = %+v", got)
	}
}

func TestNewServerRejectsWrongDataEncryptionKey(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		RootFolderName:    "TestRoot",
		AccountsFile:      filepath.Join(dir, "accounts.json"),
		AccountSessionDir: filepath.Join(dir, "accounts"),
		AuthFile:          filepath.Join(dir, "auth.json"),
		DBFile:            filepath.Join(dir, "pikpak.db"),
		DataEncryptionKey: testDataEncryptionKey,
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.DataEncryptionKey = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
	if _, err := NewServer(cfg); err == nil {
		t.Fatal("NewServer accepted the wrong data encryption key")
	}
}
