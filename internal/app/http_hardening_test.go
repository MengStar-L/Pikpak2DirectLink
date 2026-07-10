package app

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthSetupRejectsRemoteAndForwardedRequests(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	remote := jsonRequest(http.MethodPost, "/api/auth/setup", `{"password":"admin-secret"}`)
	remote.RemoteAddr = "203.0.113.10:4321"
	remote.Host = "localhost"
	remote.Header.Set(setupBootstrapHeader, srv.setupBootstrapToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, remote)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote setup status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	forwardingHeaders := map[string]string{
		"Forwarded":         "for=203.0.113.10",
		"Via":               "1.1 public-proxy.example",
		"X-Forwarded-For":   "203.0.113.10",
		"X-Forwarded-Host":  "public.example",
		"X-Forwarded-Port":  "443",
		"X-Forwarded-Proto": "https",
		"X-Real-IP":         "203.0.113.10",
	}
	for header, value := range forwardingHeaders {
		t.Run(header, func(t *testing.T) {
			forwarded := jsonRequest(http.MethodPost, "/api/auth/setup", `{"password":"admin-secret"}`)
			forwarded.RemoteAddr = "127.0.0.1:4321"
			forwarded.Host = "localhost"
			forwarded.Header.Set(header, value)
			forwarded.Header.Set(setupBootstrapHeader, srv.setupBootstrapToken)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, forwarded)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("forwarded setup status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAuthSetupRequiresBootstrapTokenForLoopbackPeer(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	// This is indistinguishable from a reverse proxy that stripped forwarding
	// headers and rewrote Host, so loopback properties alone cannot authorize it.
	req := jsonRequest(http.MethodPost, "/api/auth/setup", `{"password":"admin-secret"}`)
	req.RemoteAddr = "127.0.0.1:4321"
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("setup without bootstrap token status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	req = localSetupRequest(srv, `{"password":"admin-secret"}`)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup with bootstrap token status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthSetupRejectsExpiredBootstrapToken(t *testing.T) {
	srv := newTestServer(t)
	srv.setupBootstrapUntil = time.Now().Add(-time.Second)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, localSetupRequest(srv, `{"password":"admin-secret"}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expired bootstrap token status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestInitialSetupURLKeepsTokenInFragment(t *testing.T) {
	srv := newTestServer(t)
	srv.config.Addr = ":51873"

	setupURL := srv.InitialSetupURL()
	parsed, err := url.Parse(setupURL)
	if err != nil {
		t.Fatalf("parse setup URL: %v", err)
	}
	if parsed.Host != "127.0.0.1:51873" || parsed.RawQuery != "" {
		t.Fatalf("setup URL origin/query = %q/%q", parsed.Host, parsed.RawQuery)
	}
	fragment, err := url.ParseQuery(parsed.Fragment)
	if err != nil {
		t.Fatalf("parse setup URL fragment: %v", err)
	}
	if got := fragment.Get("setup_token"); got == "" || got != srv.setupBootstrapToken {
		t.Fatalf("setup token fragment = %q", got)
	}
}

func TestInitialSetupURLRejectsConcreteNonLoopbackBind(t *testing.T) {
	srv := newTestServer(t)
	srv.config.Addr = "192.168.1.2:51873"

	if setupURL := srv.InitialSetupURL(); setupURL != "" {
		t.Fatalf("non-loopback bind produced unusable setup URL %q", setupURL)
	}
	if !srv.InitialSetupRequired() {
		t.Fatal("unconfigured server did not report that initial setup is required")
	}
}

func TestDecodeJSONRequiresJSONMediaTypeAndSingleValue(t *testing.T) {
	var payload struct {
		Name string `json:"name"`
	}

	plain := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"value"}`))
	plain.Header.Set("Content-Type", "text/plain")
	if err := decodeJSON(plain, &payload); err == nil {
		t.Fatal("text/plain JSON body was accepted")
	}

	trailing := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"value"}=`))
	trailing.Header.Set("Content-Type", "application/json")
	if err := decodeJSON(trailing, &payload); err == nil {
		t.Fatal("trailing non-JSON content was accepted")
	}

	valid := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"value"}`))
	valid.Header.Set("Content-Type", "application/problem+json; charset=utf-8")
	if err := decodeJSON(valid, &payload); err != nil {
		t.Fatalf("valid structured JSON media type rejected: %v", err)
	}
	if payload.Name != "value" {
		t.Fatalf("decoded name = %q", payload.Name)
	}
}
