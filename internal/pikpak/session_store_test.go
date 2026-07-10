package pikpak

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type memorySessionStore struct {
	data        []byte
	loadErr     error
	saveErr     error
	deleteErr   error
	loadCalls   int
	saveCalls   int
	deleteCalls int
}

func (s *memorySessionStore) Load() ([]byte, error) {
	s.loadCalls++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return append([]byte(nil), s.data...), nil
}

func (s *memorySessionStore) Save(data []byte) error {
	s.saveCalls++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.data = append([]byte(nil), data...)
	return nil
}

func (s *memorySessionStore) Delete() error {
	s.deleteCalls++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.data = nil
	return nil
}

func TestStatusLoadsSessionStoreBeforeFile(t *testing.T) {
	t.Parallel()

	sessionFile := filepath.Join(t.TempDir(), "session.json")
	writeSessionFile(t, sessionFile, sessionState{
		Username:     "file@example.com",
		AccessToken:  "file-access",
		RefreshToken: "file-refresh",
		DeviceID:     "file-device",
		ExpiresAt:    time.Now().Add(time.Hour),
	})

	store := &memorySessionStore{data: marshalSession(t, sessionState{
		Username:     "store@example.com",
		AccessToken:  "store-access",
		RefreshToken: "store-refresh",
		DeviceID:     "store-device",
		ExpiresAt:    time.Now().Add(time.Hour),
	})}
	client := NewClient(Config{
		SessionFile:  sessionFile,
		SessionStore: store,
	})

	status := client.Status()
	if !status.Ready || !status.LoggedIn || !status.Persisted {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.Username != "store@example.com" {
		t.Fatalf("expected store username, got %q", status.Username)
	}
	if client.DeviceID() != "store-device" {
		t.Fatalf("expected store device id, got %q", client.DeviceID())
	}
	if store.loadCalls != 1 {
		t.Fatalf("expected one store load, got %d", store.loadCalls)
	}
}

func TestSaveSessionUsesStoreBeforeFile(t *testing.T) {
	t.Parallel()

	sessionFile := filepath.Join(t.TempDir(), "session.json")
	store := &memorySessionStore{}
	client := NewClient(Config{
		SessionFile:  sessionFile,
		SessionStore: store,
	})

	client.authMu.Lock()
	client.config.Username = "demo@example.com"
	client.deviceID = "device-123"
	client.accessToken = "access-token"
	client.refreshToken = "refresh-token"
	client.userID = "user-123"
	client.expiresAt = time.Now().Add(time.Hour)
	client.sessionLoaded = true
	err := client.saveSessionLocked()
	client.authMu.Unlock()
	if err != nil {
		t.Fatalf("save session: %v", err)
	}

	if store.saveCalls != 1 {
		t.Fatalf("expected one store save, got %d", store.saveCalls)
	}
	var saved sessionState
	if err := json.Unmarshal(store.data, &saved); err != nil {
		t.Fatalf("decode saved session: %v", err)
	}
	if saved.Username != "demo@example.com" || saved.RefreshToken != "refresh-token" {
		t.Fatalf("unexpected saved session: %+v", saved)
	}
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Fatalf("expected legacy session file to remain unused, stat error: %v", err)
	}
	if status := client.Status(); !status.Persisted {
		t.Fatalf("expected store-backed session to be persisted: %+v", status)
	}
}

func TestSessionStoreLoadErrorDoesNotFallBackToFile(t *testing.T) {
	t.Parallel()

	sessionFile := filepath.Join(t.TempDir(), "session.json")
	writeSessionFile(t, sessionFile, sessionState{
		Username:     "file@example.com",
		RefreshToken: "file-refresh",
	})
	store := &memorySessionStore{loadErr: errors.New("load failed")}
	client := NewClient(Config{
		SessionFile:  sessionFile,
		SessionStore: store,
	})

	status := client.Status()
	if status.Ready || status.LoggedIn || status.Persisted {
		t.Fatalf("expected failed store load to leave session empty: %+v", status)
	}
	if store.loadCalls != 1 {
		t.Fatalf("expected one store load, got %d", store.loadCalls)
	}
}

func TestLogoutDeletesSessionStoreBeforeFile(t *testing.T) {
	t.Parallel()

	sessionFile := filepath.Join(t.TempDir(), "session.json")
	const legacyContents = "legacy-session"
	if err := os.WriteFile(sessionFile, []byte(legacyContents), 0o600); err != nil {
		t.Fatalf("write legacy session file: %v", err)
	}
	store := &memorySessionStore{data: []byte("stored-session")}
	client := NewClient(Config{
		SessionFile:  sessionFile,
		SessionStore: store,
	})
	client.accessToken = "access-token"
	client.sessionLoaded = true

	if err := client.Logout(); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if store.deleteCalls != 1 {
		t.Fatalf("expected one store delete, got %d", store.deleteCalls)
	}
	if store.data != nil {
		t.Fatalf("expected store data to be deleted")
	}
	contents, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("read legacy session file: %v", err)
	}
	if string(contents) != legacyContents {
		t.Fatalf("legacy session file changed: %q", contents)
	}
}

func marshalSession(t *testing.T, session sessionState) []byte {
	t.Helper()
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	return data
}

func writeSessionFile(t *testing.T, path string, session sessionState) {
	t.Helper()
	if err := os.WriteFile(path, marshalSession(t, session), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}
}
