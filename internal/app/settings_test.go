package app

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestSettingsServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	minTimeout := minResolveTaskTimeout(cfg)
	serialTimeout, parallelTimeout := normalizeResolveTimeouts(60*time.Second, 2*time.Minute, minTimeout)
	return &Server{
		config:   cfg,
		resolver: newResolveQueue(serialTimeout, parallelTimeout, 1, nil),
		settings: newSettingsStore(db),
		logs:     newLogStore(10),
	}
}

func TestMinResolveTaskTimeoutDefault(t *testing.T) {
	t.Parallel()

	got := minResolveTaskTimeout(Config{})
	want := 4 * time.Minute
	if got != want {
		t.Fatalf("minResolveTaskTimeout = %s, want %s", got, want)
	}

	serial, parallel := normalizeResolveTimeouts(45*time.Second, 2*time.Minute, got)
	if serial != want {
		t.Fatalf("normalized serial timeout = %s, want %s", serial, want)
	}
	if parallel != want {
		t.Fatalf("normalized parallel timeout = %s, want %s", parallel, want)
	}
}

func TestUpdateSettingsRejectsBelowDynamicMinimum(t *testing.T) {
	t.Parallel()

	s := newTestSettingsServer(t, Config{})
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"concurrency":1,"task_timeout_seconds":60}`))
	rec := httptest.NewRecorder()

	s.handleUpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "至少为 4 分钟") {
		t.Fatalf("error body did not include dynamic minimum: %s", rec.Body.String())
	}
}

func TestUpdateSettingsAcceptsDynamicMinimum(t *testing.T) {
	t.Parallel()

	s := newTestSettingsServer(t, Config{})
	minTimeout := minResolveTaskTimeout(s.config)
	body := `{"concurrency":1,"task_timeout_seconds":` + strconv.Itoa(int(minTimeout.Seconds())) + `}`
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleUpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := s.resolver.currentTimeout(); got != minTimeout {
		t.Fatalf("resolver timeout = %s, want %s", got, minTimeout)
	}
	if payload := s.settingsPayload(); payload.MinTaskTimeoutS != int(minTimeout.Seconds()) {
		t.Fatalf("min timeout payload = %d, want %d", payload.MinTaskTimeoutS, int(minTimeout.Seconds()))
	}
}
