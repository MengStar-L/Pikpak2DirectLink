package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestAuthLimiterAdmissionIsAtomicDuringConcurrentBurst(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAuthLimiter()
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "203.0.113.8:12345"
	attempt := authAttemptForRequest(req, "email:user@example.com")

	const callers = 64
	start := make(chan struct{})
	admissions := make(chan *authAdmission, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			admission, _, admitted := limiter.admit(attempt, now)
			if admitted {
				admissions <- admission
			}
		}()
	}
	close(start)
	wg.Wait()
	close(admissions)

	if got := len(admissions); got != authIdentityFailureLimit {
		t.Fatalf("concurrent admissions = %d, want %d", got, authIdentityFailureLimit)
	}
	for admission := range admissions {
		admission.cancel(now)
	}
	if admission, _, admitted := limiter.admit(attempt, now); !admitted {
		t.Fatal("capacity was not released after cancelling in-flight admissions")
	} else {
		admission.cancel(now)
	}
}

func TestAuthAttemptTrustsForwardedForOnlyFromLoopbackPeer(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		forwarded  []string
		wantIPKey  string
	}{
		{
			name:       "public peer cannot spoof source",
			remoteAddr: "198.51.100.20:443",
			forwarded:  []string{"203.0.113.9"},
			wantIPKey:  "ip:198.51.100.20",
		},
		{
			name:       "loopback proxy selects rightmost non-loopback hop",
			remoteAddr: "127.0.0.1:51873",
			forwarded:  []string{"203.0.113.9, 198.51.100.12, 127.0.0.1"},
			wantIPKey:  "ip:198.51.100.12",
		},
		{
			name:       "loopback-only chain falls back to direct peer",
			remoteAddr: "[::1]:51873",
			forwarded:  []string{"127.0.0.1, ::1"},
			wantIPKey:  "ip:::1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/login", nil)
			req.RemoteAddr = tc.remoteAddr
			for _, value := range tc.forwarded {
				req.Header.Add("X-Forwarded-For", value)
			}
			if got := authAttemptForRequest(req, "admin").ipKey; got != tc.wantIPKey {
				t.Fatalf("ip key = %q, want %q", got, tc.wantIPKey)
			}
		})
	}
}

func TestTryAcquirePasswordHashSlotReturnsImmediatelyWhenFull(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	started := time.Now()
	_, err := tryAcquirePasswordHashSlot(context.Background(), slots)
	if !errors.Is(err, errPasswordHashBusy) {
		t.Fatalf("error = %v, want errPasswordHashBusy", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("full slot acquisition blocked for %s", elapsed)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = tryAcquirePasswordHashSlot(cancelled, make(chan struct{}, 1))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquisition error = %v, want context.Canceled", err)
	}
}

func TestAuthLimiterIdentityWindowAndReset(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAuthLimiter()
	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "203.0.113.8:12345"
	attempt := authAttemptForRequest(req, "email:user@example.com")

	for range authIdentityFailureLimit {
		if retry, limited := limiter.retryAfter(attempt, now); limited {
			t.Fatalf("limited before threshold: retry=%s", retry)
		}
		limiter.recordFailure(attempt, now)
	}
	if retry, limited := limiter.retryAfter(attempt, now); !limited || retry != authIdentityFailureWindow {
		t.Fatalf("retry = %s, limited = %v; want %s, true", retry, limited, authIdentityFailureWindow)
	}

	limiter.clearIdentity(attempt)
	if retry, limited := limiter.retryAfter(attempt, now); limited {
		t.Fatalf("successful identity reset remained limited for %s", retry)
	}
}

func TestAuthLimiterIPWindowIsSharedAndExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAuthLimiter()
	var last authAttempt
	for i := range authIPFailureLimit {
		req := httptest.NewRequest("POST", "/login", nil)
		req.RemoteAddr = "198.51.100.4:4321"
		req.Header.Set("X-Forwarded-For", "192.0.2."+strconv.Itoa(i+1))
		last = authAttemptForRequest(req, "email:user"+strconv.Itoa(i)+"@example.com")
		limiter.recordFailure(last, now)
	}
	if retry, limited := limiter.retryAfter(last, now); !limited || retry != authIPFailureWindow {
		t.Fatalf("shared IP retry = %s, limited = %v; want %s, true", retry, limited, authIPFailureWindow)
	}
	if retry, limited := limiter.retryAfter(last, now.Add(authIPFailureWindow)); limited {
		t.Fatalf("IP window did not expire: retry=%s", retry)
	}
}

func TestAuthLimiterBoundsAndSweepsEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	limiter := newAuthLimiterWithMaxEntries(4)
	for i := range 20 {
		req := httptest.NewRequest("POST", "/login", nil)
		req.RemoteAddr = "203.0.113." + strconv.Itoa(i+1) + ":80"
		limiter.recordFailure(authAttemptForRequest(req, "identity:"+strconv.Itoa(i)), now.Add(time.Duration(i)*time.Second))
		if got := len(limiter.windows); got > limiter.maxEntries {
			t.Fatalf("entries = %d, max = %d", got, limiter.maxEntries)
		}
	}

	limiter.retryAfter(authAttempt{}, now.Add(authIdentityFailureWindow+time.Hour))
	if got := len(limiter.windows); got != 0 {
		t.Fatalf("expired entries = %d, want 0", got)
	}

	req := httptest.NewRequest("POST", "/login", nil)
	req.RemoteAddr = "203.0.113.1:80"
	attempt := authAttemptForRequest(req, "same-identity")
	for range 100 {
		limiter.recordFailure(attempt, now)
	}
	if got := len(limiter.windows[attempt.ipKey].failures); got != authIPFailureLimit {
		t.Fatalf("stored IP failures = %d, want bounded at %d", got, authIPFailureLimit)
	}
	if got := len(limiter.windows[attempt.identityKey].failures); got != authIdentityFailureLimit {
		t.Fatalf("stored identity failures = %d, want bounded at %d", got, authIdentityFailureLimit)
	}
}

func TestPasswordHashConcurrencyBounds(t *testing.T) {
	for _, tc := range []struct {
		procs int
		want  int
	}{{1, 2}, {2, 2}, {4, 4}, {8, 8}, {32, 8}} {
		if got := passwordHashConcurrency(tc.procs); got != tc.want {
			t.Errorf("passwordHashConcurrency(%d) = %d, want %d", tc.procs, got, tc.want)
		}
	}
}

func TestWriteAuthRateLimitRoundsRetryAfterUp(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAuthRateLimit(rec, 1500*time.Millisecond)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("status/header = %d/%q, want 429/2", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestEmailLoginUsesSharedLimiter(t *testing.T) {
	srv := newTestServer(t)
	now := time.Unix(1_700_000_000, 0)
	srv.nowFunc = func() time.Time { return now }
	if _, err := srv.users.createEmailUser("limited@example.com", "correct-password", now); err != nil {
		t.Fatalf("create email user: %v", err)
	}

	for i := range authIdentityFailureLimit {
		rec := httptest.NewRecorder()
		req := jsonRequest(http.MethodPost, "/api/u/auth/email/login", `{"email":"limited@example.com","password":"wrong-password"}`)
		req.RemoteAddr = "203.0.113.90:12345"
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("failed login %d status = %d, want 401 (%s)", i+1, rec.Code, rec.Body.String())
		}
	}

	rec := httptest.NewRecorder()
	req := jsonRequest(http.MethodPost, "/api/u/auth/email/login", `{"email":"limited@example.com","password":"correct-password"}`)
	req.RemoteAddr = "203.0.113.90:12345"
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "900" {
		t.Fatalf("limited login = %d retry=%q, want 429/900 (%s)", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}

	now = now.Add(authIdentityFailureWindow)
	rec = httptest.NewRecorder()
	req = jsonRequest(http.MethodPost, "/api/u/auth/email/login", `{"email":"limited@example.com","password":"correct-password"}`)
	req.RemoteAddr = "203.0.113.90:12345"
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login after expiry = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func TestEmailLoginReturnsServiceUnavailableWhenPasswordHashSlotsAreFull(t *testing.T) {
	srv := newTestServer(t)

	srv.users.hashSlots = make(chan struct{}, 1)
	srv.users.hashSlots <- struct{}{}

	rec := httptest.NewRecorder()
	req := jsonRequest(http.MethodPost, "/api/u/auth/email/login", `{"email":"missing@example.com","password":"candidate-password"}`)
	req.RemoteAddr = "203.0.113.91:12345"
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("full hash slots = %d retry=%q, want 503 with Retry-After (%s)", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}

	if retry, limited := srv.authLimiter.retryAfter(authAttemptForRequest(req, "email:missing@example.com"), srv.now()); limited {
		t.Fatalf("hash capacity rejection consumed auth failure budget: retry=%s", retry)
	}
}

func TestAdminLoginReturnsServiceUnavailableWhenPasswordHashSlotsAreFull(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.creds.Set("correct-password"); err != nil {
		t.Fatalf("set admin password: %v", err)
	}

	store, ok := srv.creds.(*databaseCredentialStore)
	if !ok {
		t.Fatalf("credential store type = %T, want *databaseCredentialStore", srv.creds)
	}
	store.hashSlots = make(chan struct{}, 1)
	store.hashSlots <- struct{}{}

	rec := httptest.NewRecorder()
	req := jsonRequest(http.MethodPost, "/api/auth/login", `{"password":"correct-password"}`)
	req.RemoteAddr = "203.0.113.92:12345"
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("full hash slots = %d retry=%q, want 503 with Retry-After (%s)", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

func TestEmailRegistrationReturnsServiceUnavailableWhenPasswordHashSlotsAreFull(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.settings.setBool(settingKeyEmailRegistrationEnabled, true); err != nil {
		t.Fatalf("enable email registration: %v", err)
	}

	srv.users.hashSlots = make(chan struct{}, 1)
	srv.users.hashSlots <- struct{}{}

	rec := httptest.NewRecorder()
	req := jsonRequest(http.MethodPost, "/api/u/auth/email/register", `{"email":"busy@example.com","password":"correct-password"}`)
	req.RemoteAddr = "203.0.113.93:12345"
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("full hash slots = %d retry=%q, want 503 with Retry-After (%s)", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
}

func TestEmailRegistrationConsumesAdmissionBudgetBeforeHashing(t *testing.T) {
	srv := newTestServer(t)
	if err := srv.settings.setBool(settingKeyEmailRegistrationEnabled, true); err != nil {
		t.Fatalf("enable email registration: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	srv.nowFunc = func() time.Time { return now }

	for i := range authIPFailureLimit - 1 {
		req := httptest.NewRequest(http.MethodPost, "/api/u/auth/email/register", nil)
		req.RemoteAddr = "127.0.0.1:51873"
		req.Header.Set("X-Forwarded-For", "198.51.100.77")
		srv.authLimiter.recordFailure(authAttemptForRequest(req, "seed:"+strconv.Itoa(i)), now)
	}

	first := httptest.NewRecorder()
	firstReq := jsonRequest(http.MethodPost, "/api/u/auth/email/register", `{"email":"first@example.com","password":"correct-password"}`)
	firstReq.RemoteAddr = "127.0.0.1:51873"
	firstReq.Header.Set("X-Forwarded-For", "198.51.100.77")
	srv.Handler().ServeHTTP(first, firstReq)
	if first.Code != http.StatusCreated {
		t.Fatalf("first registration = %d, want 201 (%s)", first.Code, first.Body.String())
	}

	limited := httptest.NewRecorder()
	limitedReq := jsonRequest(http.MethodPost, "/api/u/auth/email/register", `{"email":"second@example.com","password":"correct-password"}`)
	limitedReq.RemoteAddr = "127.0.0.1:51873"
	limitedReq.Header.Set("X-Forwarded-For", "198.51.100.77")
	srv.Handler().ServeHTTP(limited, limitedReq)
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("limited registration = %d retry=%q, want 429 with Retry-After (%s)", limited.Code, limited.Header().Get("Retry-After"), limited.Body.String())
	}
}
