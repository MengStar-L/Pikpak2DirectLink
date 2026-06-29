package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
	if state.record.CredentialNextCheckAt == "" {
		t.Fatalf("expected bootstrapped account to be scheduled for credential check")
	}
}

func TestAccountPoolEnsureCredentialScheduleBackfillsOldAccounts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	pool := newTrafficTestPool(t)
	pool.injectAccount(accountRecord{ID: "old", Username: "old@example.com", Status: AccountAvailable})

	if err := pool.EnsureCredentialSchedule(now, 6*time.Hour); err != nil {
		t.Fatalf("ensure schedule: %v", err)
	}

	got := pool.List()[0]
	wantNext := now.Add(6 * time.Hour).Format(time.RFC3339)
	if got.CredentialNextCheckAt != wantNext {
		t.Fatalf("next check = %q, want %q", got.CredentialNextCheckAt, wantNext)
	}
	if got.CredentialCheckedAt != "" {
		t.Fatalf("old account should not get a checked_at value, got %q", got.CredentialCheckedAt)
	}
}

func TestAccountPoolResolveOrder(t *testing.T) {
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

	ids := []string{"a", "b", "c"}
	pool.mu.Lock()
	for _, id := range ids {
		pool.accounts[id] = &accountState{
			record: accountRecord{ID: id, Username: id, Status: AccountAvailable},
			client: pikpak.NewClient(pikpak.Config{Username: id}),
		}
		pool.order = append(pool.order, id)
	}
	pool.mu.Unlock()

	// Serial mode preserves stored order on every call.
	for i := 0; i < 3; i++ {
		got := orderIDs(pool.ResolveOrder(false))
		if got[0] != "a" || got[1] != "b" || got[2] != "c" {
			t.Fatalf("serial ResolveOrder = %v, want [a b c]", got)
		}
	}

	// Parallel mode rotates the starting account across calls so consecutive jobs
	// fan out instead of all hitting "a".
	firsts := []string{
		orderIDs(pool.ResolveOrder(true))[0],
		orderIDs(pool.ResolveOrder(true))[0],
		orderIDs(pool.ResolveOrder(true))[0],
	}
	if firsts[0] == firsts[1] && firsts[1] == firsts[2] {
		t.Fatalf("expected rotating start accounts, all began with %q", firsts[0])
	}

	// A failed account sinks to the back of the parallel order.
	pool.MarkFailed("a", errors.New("boom"))
	got := orderIDs(pool.ResolveOrder(true))
	if got[len(got)-1] != "a" {
		t.Fatalf("failed account not last: %v", got)
	}
}

func TestAccountPoolRefreshLoginValidation(t *testing.T) {
	t.Parallel()

	pool := newTrafficTestPool(t)

	if _, err := pool.RefreshLogin(nil, "missing"); err == nil {
		t.Fatal("expected error for unknown account")
	}

	pool.injectAccount(accountRecord{
		ID:       "acct_missing_password",
		Username: "demo@example.com",
		Status:   AccountFailed,
	})

	if _, err := pool.RefreshLogin(nil, "acct_missing_password"); err == nil {
		t.Fatal("expected error for account without saved password")
	}
}

func orderIDs(rts []AccountRuntime) []string {
	out := make([]string, len(rts))
	for i, rt := range rts {
		out[i] = rt.ID
	}
	return out
}

func TestAccountTrafficHelpers(t *testing.T) {
	t.Parallel()
	now := time.Now()
	thisMonth := monthKey(now)

	within := accountRecord{TrafficLimit: 700 * bytesPerGB, TrafficUsed: 100 * bytesPerGB, TrafficPeriod: thisMonth}
	if got := effectiveTrafficUsed(within, now); got != 100*bytesPerGB {
		t.Fatalf("effectiveTrafficUsed within month = %d", got)
	}
	if accountTrafficLimited(within, now) {
		t.Fatal("account within budget should not be limited")
	}

	atLimit := accountRecord{TrafficLimit: 700 * bytesPerGB, TrafficUsed: 700 * bytesPerGB, TrafficPeriod: thisMonth}
	if !accountTrafficLimited(atLimit, now) {
		t.Fatal("account at budget should be limited")
	}

	// A counter stamped in a previous month is treated as already reset.
	stale := accountRecord{TrafficLimit: 700 * bytesPerGB, TrafficUsed: 700 * bytesPerGB, TrafficPeriod: "2000-01"}
	if got := effectiveTrafficUsed(stale, now); got != 0 {
		t.Fatalf("stale-period used should reset to 0, got %d", got)
	}
	if accountTrafficLimited(stale, now) {
		t.Fatal("stale-period account should not be limited (auto monthly reset)")
	}

	// Non-positive limit means unlimited.
	unlimited := accountRecord{TrafficLimit: 0, TrafficUsed: 9999 * bytesPerGB, TrafficPeriod: thisMonth}
	if accountTrafficLimited(unlimited, now) {
		t.Fatal("zero limit should be treated as unlimited")
	}
}

func newTrafficTestPool(t *testing.T) *AccountPool {
	t.Helper()
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
	return pool
}

func (p *AccountPool) injectAccount(rec accountRecord) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accounts[rec.ID] = &accountState{
		record: rec,
		client: pikpak.NewClient(pikpak.Config{Username: rec.Username}),
	}
	p.order = append(p.order, rec.ID)
}

func TestAccountAddTrafficAndMonthlyReset(t *testing.T) {
	t.Parallel()
	pool := newTrafficTestPool(t)
	pool.injectAccount(accountRecord{ID: "a", Username: "a", Status: AccountAvailable, TrafficLimit: 700 * bytesPerGB, TrafficPeriod: monthKey(time.Now())})

	pool.AddTraffic("a", 100*bytesPerGB)
	pool.mu.RLock()
	used := pool.accounts["a"].record.TrafficUsed
	pool.mu.RUnlock()
	if used != 100*bytesPerGB {
		t.Fatalf("after AddTraffic: used=%d, want 100G", used)
	}

	// Force a stale period, then add: the counter must roll over to the new month.
	pool.mu.Lock()
	pool.accounts["a"].record.TrafficPeriod = "2000-01"
	pool.accounts["a"].record.TrafficUsed = 700 * bytesPerGB
	pool.mu.Unlock()

	pool.AddTraffic("a", 50*bytesPerGB)
	pool.mu.RLock()
	rec := pool.accounts["a"].record
	pool.mu.RUnlock()
	if rec.TrafficUsed != 50*bytesPerGB {
		t.Fatalf("after monthly reset: used=%d, want 50G", rec.TrafficUsed)
	}
	if rec.TrafficPeriod != monthKey(time.Now()) {
		t.Fatalf("period not rolled to current month: %q", rec.TrafficPeriod)
	}
}

func TestAccountSetTrafficLimit(t *testing.T) {
	t.Parallel()
	pool := newTrafficTestPool(t)
	pool.injectAccount(accountRecord{ID: "a", Username: "a", Status: AccountAvailable, TrafficLimit: 700 * bytesPerGB, TrafficPeriod: monthKey(time.Now())})

	if err := pool.SetTrafficLimit("a", 100*bytesPerGB); err != nil {
		t.Fatalf("SetTrafficLimit: %v", err)
	}
	pool.mu.RLock()
	got := pool.accounts["a"].record.TrafficLimit
	pool.mu.RUnlock()
	if got != 100*bytesPerGB {
		t.Fatalf("limit = %d, want 100G", got)
	}
	if err := pool.SetTrafficLimit("missing", bytesPerGB); err == nil {
		t.Fatal("expected error for unknown account")
	}
}

func TestRecordParseErrorKeepsAccountAvailable(t *testing.T) {
	t.Parallel()
	pool := newTrafficTestPool(t)
	pool.injectAccount(accountRecord{ID: "a", Username: "a@example.com", Status: AccountAvailable, TrafficLimit: 700 * bytesPerGB, TrafficPeriod: monthKey(time.Now())})

	pool.RecordParseError("a", "job1", "record not found")

	summaries := pool.List()
	if len(summaries) != 1 {
		t.Fatalf("summaries len = %d, want 1", len(summaries))
	}
	got := summaries[0]
	if got.Status != AccountAvailable {
		t.Fatalf("status = %q, want available", got.Status)
	}
	if got.LastError != "" {
		t.Fatalf("last error = %q, want empty", got.LastError)
	}
	if got.ParseErrorCount != 1 || len(got.ParseErrors) != 1 {
		t.Fatalf("parse errors = count %d list %+v, want one", got.ParseErrorCount, got.ParseErrors)
	}
	if got.ParseErrors[0].JobID != "job1" || got.ParseErrors[0].Message != "record not found" {
		t.Fatalf("parse error entry = %+v", got.ParseErrors[0])
	}
}

func TestDeleteParseErrorRemovesSelectedEntry(t *testing.T) {
	t.Parallel()
	pool := newTrafficTestPool(t)
	pool.injectAccount(accountRecord{
		ID:            "a",
		Username:      "a@example.com",
		Status:        AccountAvailable,
		TrafficLimit:  700 * bytesPerGB,
		TrafficPeriod: monthKey(time.Now()),
		ParseErrors: []ParseError{
			{Time: "2026-06-29T10:00:00Z", JobID: "job1", Message: "first"},
			{Time: "2026-06-29T11:00:00Z", JobID: "job2", Message: "second"},
		},
	})

	if err := pool.DeleteParseError("a", 0); err != nil {
		t.Fatalf("DeleteParseError: %v", err)
	}
	got := pool.List()[0]
	if got.ParseErrorCount != 1 || len(got.ParseErrors) != 1 {
		t.Fatalf("parse errors = count %d list %+v, want one", got.ParseErrorCount, got.ParseErrors)
	}
	if got.ParseErrors[0].JobID != "job2" || got.ParseErrors[0].Message != "second" {
		t.Fatalf("remaining parse error = %+v, want job2", got.ParseErrors[0])
	}

	reloaded, err := NewAccountPool(pool.config)
	if err != nil {
		t.Fatalf("reload pool: %v", err)
	}
	reloadedGot := reloaded.List()[0]
	if reloadedGot.ParseErrorCount != 1 || reloadedGot.ParseErrors[0].JobID != "job2" {
		t.Fatalf("reloaded parse errors = count %d list %+v, want job2", reloadedGot.ParseErrorCount, reloadedGot.ParseErrors)
	}
	if err := pool.DeleteParseError("missing", 0); err == nil {
		t.Fatal("expected error for missing account")
	}
	if err := pool.DeleteParseError("a", 99); err == nil {
		t.Fatal("expected error for missing parse error index")
	}
}

func TestHandleDeleteAccountParseError(t *testing.T) {
	t.Parallel()
	pool := newTrafficTestPool(t)
	pool.injectAccount(accountRecord{
		ID:            "a",
		Username:      "a@example.com",
		Status:        AccountAvailable,
		TrafficLimit:  700 * bytesPerGB,
		TrafficPeriod: monthKey(time.Now()),
		ParseErrors:   []ParseError{{Time: "2026-06-29T10:00:00Z", JobID: "job1", Message: "record not found"}},
	})
	srv := &Server{accounts: pool}

	req := httptest.NewRequest(http.MethodDelete, "/api/accounts/a/parse-errors/0", nil)
	req.SetPathValue("id", "a")
	req.SetPathValue("index", "0")
	rec := httptest.NewRecorder()
	srv.handleDeleteAccountParseError(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s, want 200", rec.Code, rec.Body.String())
	}
	if got := pool.List()[0].ParseErrorCount; got != 0 {
		t.Fatalf("parse error count = %d, want 0", got)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/accounts/a/parse-errors/bad", nil)
	req.SetPathValue("id", "a")
	req.SetPathValue("index", "bad")
	rec = httptest.NewRecorder()
	srv.handleDeleteAccountParseError(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", rec.Code, rec.Body.String())
	}
}

func TestResolveOrderExcludesOverLimit(t *testing.T) {
	t.Parallel()
	pool := newTrafficTestPool(t)
	mk := monthKey(time.Now())
	// a: available within budget; b: over budget; c: failed (fallback).
	pool.injectAccount(accountRecord{ID: "a", Username: "a", Status: AccountAvailable, TrafficLimit: 700 * bytesPerGB, TrafficUsed: 10 * bytesPerGB, TrafficPeriod: mk})
	pool.injectAccount(accountRecord{ID: "b", Username: "b", Status: AccountAvailable, TrafficLimit: 700 * bytesPerGB, TrafficUsed: 700 * bytesPerGB, TrafficPeriod: mk})
	pool.injectAccount(accountRecord{ID: "c", Username: "c", Status: AccountFailed, TrafficLimit: 700 * bytesPerGB, TrafficPeriod: mk})

	for _, rotate := range []bool{false, true} {
		got := orderIDs(pool.ResolveOrder(rotate))
		for _, id := range got {
			if id == "b" {
				t.Fatalf("rotate=%v: over-limit account b must be excluded, got %v", rotate, got)
			}
		}
		hasA, hasC := false, false
		for _, id := range got {
			hasA = hasA || id == "a"
			hasC = hasC || id == "c"
		}
		if !hasA {
			t.Fatalf("rotate=%v: available account a missing: %v", rotate, got)
		}
		if !hasC {
			t.Fatalf("rotate=%v: failed account c should remain as fallback: %v", rotate, got)
		}
	}
}

func TestIsResourceUnavailableError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "copyright takedown", err: errors.New("Involving copyright or harmful content,no longer available"), want: true},
		{name: "no longer available", err: errors.New("File no longer available"), want: true},
		{name: "harmful content", err: errors.New("harmful content detected"), want: true},
		{name: "nil", err: nil, want: false},
		{name: "ordinary account failure", err: errors.New("login failed: bad password"), want: false},
		{name: "timeout", err: errors.New("context deadline exceeded"), want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isResourceUnavailableError(tc.err); got != tc.want {
				t.Fatalf("isResourceUnavailableError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsResourceParseError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "record not found", err: errors.New("record not found"), want: true},
		{name: "wrapped api response", err: errors.New("pikpak api failed: record not found"), want: true},
		{name: "nil", err: nil, want: false},
		{name: "ordinary auth failure", err: errors.New("login failed"), want: false},
		{name: "copyright takedown", err: errors.New("no longer available"), want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isResourceParseError(tc.err); got != tc.want {
				t.Fatalf("isResourceParseError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestFriendlyPikPakMessageResourceUnavailable(t *testing.T) {
	t.Parallel()

	msg := friendlyPikPakError(errors.New("Involving copyright or harmful content,no longer available"))
	if msg == "" || msg == "Involving copyright or harmful content,no longer available" {
		t.Fatalf("expected a friendly Chinese message, got %q", msg)
	}
}
