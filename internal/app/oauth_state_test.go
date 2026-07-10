package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOAuthStateRequiresMatchingNonceAndIsSingleUse(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newOAuthStateStore()

	state, err := store.create("browser-a", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.consume(state, "browser-b", now) {
		t.Fatal("state accepted a nonce from another browser")
	}
	if store.consume(state, "browser-a", now) {
		t.Fatal("state remained usable after a failed consume")
	}

	state, err = store.create("browser-a", now)
	if err != nil {
		t.Fatalf("create second state: %v", err)
	}
	if !store.consume(state, "browser-a", now) {
		t.Fatal("state rejected its matching browser nonce")
	}
	if store.consume(state, "browser-a", now) {
		t.Fatal("state was reusable")
	}
}

func TestOAuthStateExpiresSweepsAndStaysBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newOAuthStateStoreWithConfig(time.Minute, 2)

	first, err := store.create("first", now)
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := store.create("second", now.Add(time.Second))
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	third, err := store.create("third", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("create third: %v", err)
	}
	if got := len(store.states); got != 2 {
		t.Fatalf("states = %d, want 2", got)
	}
	if store.consume(first, "first", now.Add(2*time.Second)) {
		t.Fatal("oldest state was not evicted at capacity")
	}
	if !store.consume(second, "second", now.Add(2*time.Second)) || !store.consume(third, "third", now.Add(2*time.Second)) {
		t.Fatal("capacity eviction removed a newer state")
	}

	expired, err := store.create("expired", now)
	if err != nil {
		t.Fatalf("create expired state: %v", err)
	}
	if store.consume(expired, "expired", now.Add(time.Minute)) {
		t.Fatal("state remained valid at its expiry boundary")
	}
	if got := len(store.states); got != 0 {
		t.Fatalf("expired states = %d, want 0", got)
	}
}

func TestLinuxDoOAuthNonceCookieBindsCallback(t *testing.T) {
	srv := newTestServer(t)
	now := time.Unix(1_700_000_000, 0)
	srv.setNowFunc(func() time.Time { return now })
	srv.config.PublicBaseURL = "https://public.example"
	if err := srv.settings.setString(settingKeyLinuxDoClientID, "client-id"); err != nil {
		t.Fatalf("set client id: %v", err)
	}
	if err := srv.appSecrets.set(linuxDoClientSecretName, "client-secret"); err != nil {
		t.Fatalf("set client secret: %v", err)
	}

	start := func(t *testing.T) (string, *http.Cookie) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/u/auth/linuxdo/start", nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("start status = %d, want 302 (%s)", rec.Code, rec.Body.String())
		}
		location, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatalf("parse redirect: %v", err)
		}
		state := location.Query().Get("state")
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == linuxDoOAuthNonceCookieName {
				if state == "" || cookie.Value == "" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
					t.Fatalf("invalid OAuth start state/cookie: state=%q cookie=%+v", state, cookie)
				}
				return state, cookie
			}
		}
		t.Fatal("OAuth start did not set browser nonce cookie")
		return "", nil
	}

	state, _ := start(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/u/auth/linuxdo/callback?state="+url.QueryEscape(state)+"&error=access_denied", nil)
	req.AddCookie(&http.Cookie{Name: linuxDoOAuthNonceCookieName, Value: "wrong-browser"})
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "error=invalid_state") {
		t.Fatalf("mismatched nonce redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	assertOAuthNonceCookieCleared(t, rec)

	state, nonceCookie := start(t)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/u/auth/linuxdo/callback?state="+url.QueryEscape(state)+"&error=access_denied", nil)
	req.AddCookie(nonceCookie)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "error=access_denied") {
		t.Fatalf("matching nonce redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	assertOAuthNonceCookieCleared(t, rec)
}

func assertOAuthNonceCookieCleared(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == linuxDoOAuthNonceCookieName {
			if cookie.MaxAge != -1 || cookie.Value != "" {
				t.Fatalf("OAuth nonce cookie was not cleared: %+v", cookie)
			}
			return
		}
	}
	t.Fatal("callback did not clear OAuth nonce cookie")
}
