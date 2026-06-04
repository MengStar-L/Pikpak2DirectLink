package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pikpak2directlink/internal/pikpak"
)

func TestAccountPoolListResetAndDelete(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pool, err := NewAccountPool(AccountPoolConfig{
		AccountsFile:   filepath.Join(dir, "accounts.json"),
		SessionDir:     filepath.Join(dir, "sessions"),
		RootFolderName: "Pikpak2DirectLink",
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}

	accountID := "acct_demo"
	sessionFile := filepath.Join(dir, "sessions", accountID+".json")
	pool.mu.Lock()
	pool.accounts[accountID] = &accountState{
		record: accountRecord{
			ID:          accountID,
			Username:    "demo@example.com",
			Password:    "secret",
			SessionFile: sessionFile,
			Status:      AccountAvailable,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
		client: pikpak.NewClient(pikpak.Config{
			Username:       "demo@example.com",
			Password:       "secret",
			SessionFile:    sessionFile,
			RootFolderName: "Pikpak2DirectLink",
			RequestTimeout: time.Second,
		}),
	}
	pool.order = append(pool.order, accountID)
	if err := pool.saveLocked(); err != nil {
		pool.mu.Unlock()
		t.Fatalf("save: %v", err)
	}
	pool.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	if err := os.WriteFile(sessionFile, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	pool.MarkFailed(accountID, errors.New("login failed"))
	summaries := pool.List()
	if len(summaries) != 1 {
		t.Fatalf("expected one account, got %d", len(summaries))
	}
	if summaries[0].Status != AccountFailed {
		t.Fatalf("expected failed account, got %q", summaries[0].Status)
	}
	if summaries[0].LastError != "login failed" {
		t.Fatalf("expected last error, got %q", summaries[0].LastError)
	}

	if err := pool.ResetFailure(accountID); err != nil {
		t.Fatalf("reset failure: %v", err)
	}
	summaries = pool.List()
	if summaries[0].Status != AccountAvailable {
		t.Fatalf("expected available account, got %q", summaries[0].Status)
	}
	if summaries[0].LastError != "" {
		t.Fatalf("expected empty last error, got %q", summaries[0].LastError)
	}

	reloaded, err := NewAccountPool(AccountPoolConfig{
		AccountsFile:   filepath.Join(dir, "accounts.json"),
		SessionDir:     filepath.Join(dir, "sessions"),
		RootFolderName: "Pikpak2DirectLink",
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("reload pool: %v", err)
	}
	if len(reloaded.List()) != 1 {
		t.Fatalf("expected persisted account")
	}

	if err := reloaded.Delete(accountID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(reloaded.List()) != 0 {
		t.Fatalf("expected empty pool after delete")
	}
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Fatalf("expected session file to be removed, got %v", err)
	}
}

func TestAccountPoolBootstrapUsesLegacySessionFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	legacySession := filepath.Join(dir, "pikpak-session.json")
	pool, err := NewAccountPool(AccountPoolConfig{
		AccountsFile:   filepath.Join(dir, "accounts.json"),
		SessionDir:     filepath.Join(dir, "sessions"),
		RootFolderName: "Pikpak2DirectLink",
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}

	if err := pool.AddBootstrap("demo@example.com", "secret", legacySession); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	runtime, ok := pool.Get(accountIDForUsername("demo@example.com"))
	if !ok {
		t.Fatalf("expected bootstrapped account")
	}
	if runtime.Client == nil {
		t.Fatalf("expected client")
	}

	pool.mu.RLock()
	defer pool.mu.RUnlock()
	state := pool.accounts[accountIDForUsername("demo@example.com")]
	if state.record.SessionFile != legacySession {
		t.Fatalf("expected legacy session file %q, got %q", legacySession, state.record.SessionFile)
	}
}
