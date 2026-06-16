package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		RootFolderName:    "TestRoot",
		AccountsFile:      filepath.Join(dir, "accounts.json"),
		AccountSessionDir: filepath.Join(dir, "sessions"),
		AuthFile:          filepath.Join(dir, "auth.json"),
		DBFile:            filepath.Join(dir, "pikpak.db"),
		RequestTimeout:    0,
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func TestCredentialStoreSetVerify(t *testing.T) {
	t.Parallel()

	store, err := newCredentialStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatalf("newCredentialStore: %v", err)
	}
	if store.HasPassword() {
		t.Fatal("expected no password initially")
	}
	if err := store.Set("hunter2pass"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !store.HasPassword() {
		t.Fatal("expected password after Set")
	}
	if !store.Verify("hunter2pass") {
		t.Fatal("Verify should accept the correct password")
	}
	if store.Verify("wrong") {
		t.Fatal("Verify should reject an incorrect password")
	}
}

func TestCredentialStorePersistsAndNeverStoresPlaintext(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "auth.json")
	store, err := newCredentialStore(path)
	if err != nil {
		t.Fatalf("newCredentialStore: %v", err)
	}
	if err := store.Set("s3cret-on-disk"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	data := mustReadFile(t, path)
	if strings.Contains(string(data), "s3cret-on-disk") {
		t.Fatal("plaintext password must never be written to disk")
	}

	reopened, err := newCredentialStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened.Verify("s3cret-on-disk") {
		t.Fatal("password should survive reload from disk")
	}
}

// The gate must be served (not the app) to unauthenticated browsers, and the
// app shell / scripts must never be returned without a valid session.
func TestGateBlocksAppForUnauthenticated(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t)
	handler := srv.Handler()

	// The app shell and its script must never reach an unauthenticated client.
	// (/styles.css is intentionally public so the CDK user portal can render.)
	for _, path := range []string{"/", "/app.js", "/anything"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		if strings.Contains(body, "id=\"resolveForm\"") || strings.Contains(body, "navButtons") {
			t.Fatalf("path %q leaked application content to an unauthenticated client", path)
		}
		if !strings.Contains(body, "访问验证") && !strings.Contains(body, "gateForm") {
			t.Fatalf("path %q did not return the gate page; got: %.80s", path, body)
		}
	}
}

func TestProtectedAPIReturns401(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t)
	handler := srv.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated API call, got %d", rec.Code)
	}
}

func TestSetupThenLoginFlow(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t)
	handler := srv.Handler()

	// status before setup: not configured, not authenticated.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if !strings.Contains(rec.Body.String(), "\"configured\":false") {
		t.Fatalf("expected configured=false before setup, got %s", rec.Body.String())
	}

	// setup with a too-short password is rejected.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/setup", `{"password":"123"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short password, got %d", rec.Code)
	}

	// valid setup issues a session cookie and grants access.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/setup", `{"password":"admin-secret"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for setup, got %d (%s)", rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(t, rec.Result().Cookies())

	// the session cookie now unlocks the app shell.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "resolveForm") {
		t.Fatal("authenticated request should receive the app shell")
	}

	// setup is closed once a password exists.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/setup", `{"password":"another"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on second setup, got %d", rec.Code)
	}

	// login with the wrong password fails.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/login", `{"password":"nope"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", rec.Code)
	}

	// login with the correct password succeeds.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/login", `{"password":"admin-secret"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for correct login, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestFixedPasswordDisablesSetup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := Config{
		AccessPassword:    "env-pinned-pass",
		RootFolderName:    "TestRoot",
		AccountsFile:      filepath.Join(dir, "accounts.json"),
		AccountSessionDir: filepath.Join(dir, "sessions"),
		AuthFile:          filepath.Join(dir, "auth.json"),
		DBFile:            filepath.Join(dir, "pikpak.db"),
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	handler := srv.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if !strings.Contains(rec.Body.String(), "\"configured\":true") {
		t.Fatalf("expected configured=true when ACCESS_PASSWORD is set, got %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/login", `{"password":"env-pinned-pass"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected login to succeed with the pinned password, got %d", rec.Code)
	}
}

func TestChangePasswordFlow(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t)
	handler := srv.Handler()

	// Establish a password and capture the session.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/setup", `{"password":"admin-secret"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: expected 200, got %d", rec.Code)
	}
	oldCookie := sessionCookie(t, rec.Result().Cookies())

	// Unauthenticated change is rejected by the middleware.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/password", `{"current_password":"admin-secret","new_password":"brand-new-pass"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated change: expected 401, got %d", rec.Code)
	}

	// Wrong current password is rejected.
	rec = httptest.NewRecorder()
	req := jsonRequest(http.MethodPost, "/api/auth/password", `{"current_password":"wrong","new_password":"brand-new-pass"}`)
	req.AddCookie(oldCookie)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password: expected 401, got %d", rec.Code)
	}

	// Too-short new password is rejected.
	rec = httptest.NewRecorder()
	req = jsonRequest(http.MethodPost, "/api/auth/password", `{"current_password":"admin-secret","new_password":"123"}`)
	req.AddCookie(oldCookie)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short new password: expected 400, got %d", rec.Code)
	}

	// Reusing the same password is rejected.
	rec = httptest.NewRecorder()
	req = jsonRequest(http.MethodPost, "/api/auth/password", `{"current_password":"admin-secret","new_password":"admin-secret"}`)
	req.AddCookie(oldCookie)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same password: expected 400, got %d", rec.Code)
	}

	// A valid change succeeds and mints a fresh session.
	rec = httptest.NewRecorder()
	req = jsonRequest(http.MethodPost, "/api/auth/password", `{"current_password":"admin-secret","new_password":"brand-new-pass"}`)
	req.AddCookie(oldCookie)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid change: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	newCookie := sessionCookie(t, rec.Result().Cookies())

	// The previous session was invalidated; the freshly issued one still works.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.AddCookie(oldCookie)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old session should be invalidated after password change, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.AddCookie(newCookie)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new session should remain valid, got %d", rec.Code)
	}

	// Login reflects the new password, not the old one.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/login", `{"password":"admin-secret"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password should no longer log in, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/login", `{"password":"brand-new-pass"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("new password should log in, got %d", rec.Code)
	}
}

func TestChangePasswordBlockedWhenFixed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := Config{
		AccessPassword:    "env-pinned-pass",
		RootFolderName:    "TestRoot",
		AccountsFile:      filepath.Join(dir, "accounts.json"),
		AccountSessionDir: filepath.Join(dir, "sessions"),
		AuthFile:          filepath.Join(dir, "auth.json"),
		DBFile:            filepath.Join(dir, "pikpak.db"),
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	handler := srv.Handler()

	// Log in with the pinned password to get a session.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/auth/login", `{"password":"env-pinned-pass"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("login: expected 200, got %d", rec.Code)
	}
	cookie := sessionCookie(t, rec.Result().Cookies())

	rec = httptest.NewRecorder()
	req := jsonRequest(http.MethodPost, "/api/auth/password", `{"current_password":"env-pinned-pass","new_password":"something-else"}`)
	req.AddCookie(cookie)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("change with fixed password: expected 409, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestCDKAdminGatedUserPortalPublic(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t)
	handler := srv.Handler()

	// Admin CDK endpoints require a session.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cdks", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/cdks, got %d", rec.Code)
	}

	// The user portal page is reachable without the admin session.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/u", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "CDK") {
		t.Fatalf("expected the user portal page at /u, got %d", rec.Code)
	}

	// An unknown CDK is rejected at login.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/u/login", `{"code":"DOES-NOTE-XIST-0000"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown CDK, got %d", rec.Code)
	}
}

func jsonRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func sessionCookie(t *testing.T, cookies []*http.Cookie) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == "session" && c.Value != "" {
			return c
		}
	}
	t.Fatal("expected a session cookie to be set")
	return nil
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
