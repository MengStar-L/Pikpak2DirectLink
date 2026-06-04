package pikpak

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusLoadsPersistedSession(t *testing.T) {
	t.Parallel()

	sessionFile := filepath.Join(t.TempDir(), "session.json")

	client := NewClient(Config{
		SessionFile: sessionFile,
	})

	client.authMu.Lock()
	client.config.Username = "demo@example.com"
	client.deviceID = "device-123"
	client.accessToken = "access-token"
	client.refreshToken = "refresh-token"
	client.userID = "user-123"
	client.expiresAt = time.Now().Add(time.Hour)
	client.sessionLoaded = true
	if err := client.saveSessionLocked(); err != nil {
		client.authMu.Unlock()
		t.Fatalf("save session: %v", err)
	}
	client.authMu.Unlock()

	reloaded := NewClient(Config{
		SessionFile: sessionFile,
	})

	status := reloaded.Status()
	if !status.Ready {
		t.Fatalf("expected ready status")
	}
	if !status.LoggedIn {
		t.Fatalf("expected logged in status")
	}
	if !status.Persisted {
		t.Fatalf("expected persisted status")
	}
	if status.Username != "demo@example.com" {
		t.Fatalf("expected username to be restored, got %q", status.Username)
	}
	if reloaded.DeviceID() != "device-123" {
		t.Fatalf("expected device id to be restored, got %q", reloaded.DeviceID())
	}
}

func TestLogoutRemovesSessionFile(t *testing.T) {
	t.Parallel()

	sessionFile := filepath.Join(t.TempDir(), "session.json")

	client := NewClient(Config{
		SessionFile: sessionFile,
	})

	client.authMu.Lock()
	client.config.Username = "demo@example.com"
	client.deviceID = "device-123"
	client.accessToken = "access-token"
	client.refreshToken = "refresh-token"
	client.userID = "user-123"
	client.expiresAt = time.Now().Add(time.Hour)
	client.sessionLoaded = true
	if err := client.saveSessionLocked(); err != nil {
		client.authMu.Unlock()
		t.Fatalf("save session: %v", err)
	}
	client.authMu.Unlock()

	if err := client.Logout(); err != nil {
		t.Fatalf("logout: %v", err)
	}

	status := client.Status()
	if status.Ready {
		t.Fatalf("expected client to be not ready after logout")
	}
	if status.LoggedIn {
		t.Fatalf("expected client to be logged out after logout")
	}
}

func TestVIPInfoPremiumDetection(t *testing.T) {
	t.Parallel()

	vip := VIPInfo{
		Data: VIPInfoData{
			Status: "ok",
			Type:   "platinum",
			Expire: "2026-12-31T23:59:59Z",
		},
	}
	if !vip.IsPremium() {
		t.Fatalf("expected platinum account to be premium")
	}
	if vip.Expiration() != "2026-12-31T23:59:59Z" {
		t.Fatalf("unexpected expiration %q", vip.Expiration())
	}

	normal := VIPInfo{
		Data: VIPInfoData{
			Status: "ok",
			Type:   "novip",
		},
	}
	if normal.IsPremium() {
		t.Fatalf("expected novip account to be non-premium")
	}
}

func TestDeleteFilesIgnoresEmptyIDs(t *testing.T) {
	t.Parallel()

	client := NewClient(Config{})
	if err := client.DeleteFiles(context.Background(), []string{"", "   "}); err != nil {
		t.Fatalf("delete empty ids: %v", err)
	}
}
