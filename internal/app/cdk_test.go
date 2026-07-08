package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestCDKStore(t *testing.T) *cdkStore {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return newCDKStore(db)
}

func TestCDKCreateAndList(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, err := store.createBatch(3, 5*bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("createBatch: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("expected 3 CDKs, got %d", len(created))
	}
	seen := map[string]bool{}
	for _, c := range created {
		if seen[c.Code] {
			t.Fatalf("duplicate code generated: %s", c.Code)
		}
		seen[c.Code] = true
		if c.RemainingBytes != 5*bytesPerGB {
			t.Fatalf("expected remaining 5G, got %d", c.RemainingBytes)
		}
	}

	list, err := store.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected list of 3, got %d", len(list))
	}
}

func TestCDKAllowProxyPersistsAndToggles(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	off, err := store.createBatch(1, 5*bytesPerGB, 30, false, now)
	if err != nil {
		t.Fatalf("createBatch(false): %v", err)
	}
	on, err := store.createBatch(1, 5*bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("createBatch(true): %v", err)
	}
	if off[0].AllowProxy {
		t.Fatal("created CDK should have AllowProxy=false")
	}
	if !on[0].AllowProxy {
		t.Fatal("created CDK should have AllowProxy=true")
	}

	// Round-trips through get + view.
	gotOff, ok, _ := store.get(off[0].Code)
	if !ok || gotOff.AllowProxy {
		t.Fatalf("get(off): ok=%v allow=%v, want ok=true allow=false", ok, gotOff.AllowProxy)
	}
	if v := toCDKView(gotOff, now); v.AllowProxy {
		t.Fatal("cdkView should expose AllowProxy=false")
	}

	// And shows up in list.
	for _, c := range mustList(t, store) {
		if c.Code == on[0].Code && !c.AllowProxy {
			t.Fatal("list should report AllowProxy=true for the enabled CDK")
		}
	}

	// update flips the flag.
	updated, ok, err := store.update(off[0].Code, 5*bytesPerGB, 30, true, now)
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if !updated.AllowProxy {
		t.Fatal("update should have set AllowProxy=true")
	}
}

func mustList(t *testing.T, store *cdkStore) []CDK {
	t.Helper()
	list, err := store.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return list
}

// TestMigrateCDKAddAllowProxyOnExistingDB guards the startup migration that runs
// against real user databases: adding the column must succeed, be idempotent,
// and default existing CDKs to proxy-allowed so prior behavior is preserved.
func TestMigrateCDKAddAllowProxyOnExistingDB(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)

	// Simulate a pre-feature cdks table without the allow_proxy column.
	if _, err := db.Exec(`CREATE TABLE cdks (
		code TEXT PRIMARY KEY,
		remaining_bytes INTEGER NOT NULL,
		used_bytes INTEGER NOT NULL DEFAULT 0,
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cdks(code, remaining_bytes, used_bytes, expires_at, created_at) VALUES('OLDCODE-AAAA-BBBB-CCCC', 100, 0, 9999999999, 1700000000)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	if err := migrateCDKAddAllowProxy(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := migrateCDKAddAllowProxy(db); err != nil { // idempotent
		t.Fatalf("migrate (2nd run): %v", err)
	}

	c, ok, err := newCDKStore(db).get("OLDCODE-AAAA-BBBB-CCCC")
	if err != nil || !ok {
		t.Fatalf("get migrated row: ok=%v err=%v", ok, err)
	}
	if !c.AllowProxy {
		t.Fatal("existing CDK should default to AllowProxy=true after migration")
	}
}

func TestCDKHasTrafficGuards(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 2*bytesPerGB, 30, true, now)
	code := created[0].Code

	if _, err := store.hasTraffic(code, now); err != nil {
		t.Fatalf("hasTraffic on fresh CDK: %v", err)
	}

	// Drain it, then it must report exhausted.
	if err := store.charge(code, 2*bytesPerGB); err != nil {
		t.Fatalf("charge: %v", err)
	}
	if _, err := store.hasTraffic(code, now); err != errCDKExhausted {
		t.Fatalf("expected errCDKExhausted, got %v", err)
	}

	// Unknown code.
	if _, err := store.hasTraffic("NOPE-NOPE-NOPE-NOPE", now); err != errCDKNotFound {
		t.Fatalf("expected errCDKNotFound, got %v", err)
	}

	// Expired code.
	exp, _ := store.createBatch(1, 5*bytesPerGB, 1, true, now)
	later := now.Add(48 * time.Hour)
	if _, err := store.hasTraffic(exp[0].Code, later); err != errCDKExpired {
		t.Fatalf("expected errCDKExpired, got %v", err)
	}
}

func TestCDKCharge(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 5*bytesPerGB, 30, true, now)
	code := created[0].Code

	if err := store.charge(code, 2*bytesPerGB); err != nil {
		t.Fatalf("charge: %v", err)
	}
	c, _, _ := store.get(code)
	if c.RemainingBytes != 3*bytesPerGB || c.UsedBytes != 2*bytesPerGB {
		t.Fatalf("after charge 2G: remaining=%d used=%d", c.RemainingBytes, c.UsedBytes)
	}

	// Overdraw clamps remaining at zero but still accumulates used.
	if err := store.charge(code, 10*bytesPerGB); err != nil {
		t.Fatalf("overdraw charge: %v", err)
	}
	c, _, _ = store.get(code)
	if c.RemainingBytes != 0 {
		t.Fatalf("remaining should clamp at 0, got %d", c.RemainingBytes)
	}
	if c.UsedBytes != 12*bytesPerGB {
		t.Fatalf("used should accumulate to 12G, got %d", c.UsedBytes)
	}
}

func TestCDKChargeIfEnoughDoesNotOverdrawConcurrently(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)
	created, _ := store.createBatch(1, 1*bytesPerGB, 30, true, now)
	code := created[0].Code
	chargeSize := int64(700 * 1024 * 1024)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.chargeIfEnough(code, chargeSize, now)
		}()
	}
	wg.Wait()
	close(errs)

	successes := 0
	overdraws := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if _, ok := errAsOverdraw(err); ok {
			overdraws++
			continue
		}
		t.Fatalf("unexpected charge error: %v", err)
	}
	if successes != 1 || overdraws != 1 {
		t.Fatalf("successes=%d overdraws=%d, want 1 each", successes, overdraws)
	}

	c, _, _ := store.get(code)
	if c.RemainingBytes != bytesPerGB-chargeSize || c.UsedBytes != chargeSize {
		t.Fatalf("remaining=%d used=%d, want remaining=%d used=%d", c.RemainingBytes, c.UsedBytes, bytesPerGB-chargeSize, chargeSize)
	}
}

func TestCDKUpdateAndDelete(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 5*bytesPerGB, 10, true, now)
	code := created[0].Code

	updated, ok, err := store.update(code, 20*bytesPerGB, 60, true, now)
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if updated.RemainingBytes != 20*bytesPerGB {
		t.Fatalf("expected remaining 20G, got %d", updated.RemainingBytes)
	}
	wantExpiry := now.Add(60 * 24 * time.Hour).Unix()
	if updated.ExpiresAt != wantExpiry {
		t.Fatalf("expected expiry %d, got %d", wantExpiry, updated.ExpiresAt)
	}

	deleted, err := store.delete(code)
	if err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
	if _, ok, _ := store.get(code); ok {
		t.Fatalf("expected code to be gone after delete")
	}
}

func TestCDKMergeAddsTrafficKeepsPrimaryFieldsAndDeletesSecondary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	primary, err := store.createBatch(1, 5*bytesPerGB, 10, true, now)
	if err != nil {
		t.Fatalf("create primary: %v", err)
	}
	secondary, err := store.createBatch(1, 4*bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("create secondary: %v", err)
	}
	if err := store.charge(primary[0].Code, 2*bytesPerGB); err != nil {
		t.Fatalf("charge primary: %v", err)
	}
	if err := store.charge(secondary[0].Code, bytesPerGB); err != nil {
		t.Fatalf("charge secondary: %v", err)
	}

	merged, err := store.merge(primary[0].Code, secondary[0].Code, now)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Code != primary[0].Code {
		t.Fatalf("merged code = %q, want primary %q", merged.Code, primary[0].Code)
	}
	if merged.RemainingBytes != 6*bytesPerGB {
		t.Fatalf("remaining = %d, want 6 GiB", merged.RemainingBytes)
	}
	if merged.UsedBytes != 2*bytesPerGB {
		t.Fatalf("used = %d, want primary used unchanged at 2 GiB", merged.UsedBytes)
	}
	if merged.ExpiresAt != secondary[0].ExpiresAt {
		t.Fatalf("expires_at = %d, want later secondary expiry %d", merged.ExpiresAt, secondary[0].ExpiresAt)
	}
	if !merged.AllowProxy || merged.CreatedAt != primary[0].CreatedAt {
		t.Fatalf("primary fields changed unexpectedly: %+v", merged)
	}
	if _, ok, err := store.get(secondary[0].Code); err != nil || ok {
		t.Fatalf("secondary should be deleted, ok=%v err=%v", ok, err)
	}
}

func TestCDKMergeRejectsInvalidInputs(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	t.Run("same code", func(t *testing.T) {
		store := newTestCDKStore(t)
		created, _ := store.createBatch(1, bytesPerGB, 30, true, now)
		if _, err := store.merge(created[0].Code, created[0].Code, now); !errors.Is(err, errCDKSameMergeCode) {
			t.Fatalf("err = %v, want errCDKSameMergeCode", err)
		}
	})

	t.Run("missing primary", func(t *testing.T) {
		store := newTestCDKStore(t)
		secondary, _ := store.createBatch(1, bytesPerGB, 30, true, now)
		if _, err := store.merge("NOPE-NOPE-NOPE-NOPE", secondary[0].Code, now); !errors.Is(err, errCDKNotFound) {
			t.Fatalf("err = %v, want errCDKNotFound", err)
		}
	})

	t.Run("missing secondary", func(t *testing.T) {
		store := newTestCDKStore(t)
		primary, _ := store.createBatch(1, bytesPerGB, 30, true, now)
		if _, err := store.merge(primary[0].Code, "NOPE-NOPE-NOPE-NOPE", now); !errors.Is(err, errCDKNotFound) {
			t.Fatalf("err = %v, want errCDKNotFound", err)
		}
	})

	t.Run("expired primary", func(t *testing.T) {
		store := newTestCDKStore(t)
		expired, _ := store.createBatch(1, bytesPerGB, 1, true, now.Add(-48*time.Hour))
		secondary, _ := store.createBatch(1, bytesPerGB, 30, true, now)
		if _, err := store.merge(expired[0].Code, secondary[0].Code, now); !errors.Is(err, errCDKExpired) {
			t.Fatalf("err = %v, want errCDKExpired", err)
		}
	})

	t.Run("expired secondary", func(t *testing.T) {
		store := newTestCDKStore(t)
		primary, _ := store.createBatch(1, bytesPerGB, 30, true, now)
		expired, _ := store.createBatch(1, bytesPerGB, 1, true, now.Add(-48*time.Hour))
		if _, err := store.merge(primary[0].Code, expired[0].Code, now); !errors.Is(err, errCDKExpired) {
			t.Fatalf("err = %v, want errCDKExpired", err)
		}
	})

	t.Run("secondary exhausted", func(t *testing.T) {
		store := newTestCDKStore(t)
		primary, _ := store.createBatch(1, bytesPerGB, 30, true, now)
		secondary, _ := store.createBatch(1, bytesPerGB, 30, true, now)
		if err := store.charge(secondary[0].Code, bytesPerGB); err != nil {
			t.Fatalf("charge secondary: %v", err)
		}
		if _, err := store.merge(primary[0].Code, secondary[0].Code, now); !errors.Is(err, errCDKExhausted) {
			t.Fatalf("err = %v, want errCDKExhausted", err)
		}
	})

	t.Run("proxy mismatch", func(t *testing.T) {
		store := newTestCDKStore(t)
		primary, _ := store.createBatch(1, bytesPerGB, 30, true, now)
		secondary, _ := store.createBatch(1, bytesPerGB, 30, false, now)
		if _, err := store.merge(primary[0].Code, secondary[0].Code, now); !errors.Is(err, errCDKProxyMismatch) {
			t.Fatalf("err = %v, want errCDKProxyMismatch", err)
		}
	})
}

func TestCDKDeleteExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	// Two already-expired (created far in the past, short window) and one live.
	past := now.Add(-40 * 24 * time.Hour)
	if _, err := store.createBatch(2, 5*bytesPerGB, 10, true, past); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	live, err := store.createBatch(1, 5*bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("create live: %v", err)
	}

	deleted, err := store.deleteExpired(now)
	if err != nil {
		t.Fatalf("deleteExpired: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 expired deleted, got %d", deleted)
	}

	remaining, err := store.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Code != live[0].Code {
		t.Fatalf("expected only the live CDK to remain, got %+v", remaining)
	}

	// A second purge with nothing expired removes nothing.
	again, err := store.deleteExpired(now)
	if err != nil || again != 0 {
		t.Fatalf("second purge: deleted=%d err=%v", again, err)
	}
}

func TestHandleDeleteExpiredCDKs(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()

	past := time.Now().Add(-72 * time.Hour)
	if _, err := srv.cdk.createBatch(2, 5*bytesPerGB, 1, true, past); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	live, err := srv.cdk.createBatch(1, 5*bytesPerGB, 30, true, time.Now())
	if err != nil {
		t.Fatalf("create live: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/cdks/expired", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated purge: expected 401, got %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/cdks/expired", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: srv.authSessions.create()})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("purge: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	var payload struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Deleted != 2 {
		t.Fatalf("expected 2 expired CDKs deleted, got %d", payload.Deleted)
	}

	remaining, err := srv.cdk.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Code != live[0].Code {
		t.Fatalf("expected only the live CDK to remain, got %+v", remaining)
	}
}

func TestHandleUserMergeCDKWithCurrentPrimary(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()
	now := time.Now()
	created, err := srv.cdk.createBatch(2, bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("create cdks: %v", err)
	}
	primary, secondary := created[0].Code, created[1].Code

	req := jsonRequest(http.MethodPost, "/api/u/cdks/merge", `{"primary_code":"`+primary+`","secondary_code":"`+secondary+`"}`)
	req.AddCookie(&http.Cookie{Name: "cdk", Value: primary})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d body=%s", rec.Code, rec.Body.String())
	}

	var payload userStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != primary || payload.RemainingBytes != 2*bytesPerGB {
		t.Fatalf("payload code/remaining = %s/%d, want %s/%d", payload.Code, payload.RemainingBytes, primary, 2*bytesPerGB)
	}
	if got := cdkCookieValue(rec.Result().Cookies()); got != primary {
		t.Fatalf("set cdk cookie = %q, want primary %q", got, primary)
	}
	if _, ok, err := srv.cdk.get(secondary); err != nil || ok {
		t.Fatalf("secondary should be deleted, ok=%v err=%v", ok, err)
	}
}

func TestHandleUserMergeCDKWithCurrentSecondarySwitchesSession(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()
	now := time.Now()
	created, err := srv.cdk.createBatch(2, bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("create cdks: %v", err)
	}
	primary, secondary := created[0].Code, created[1].Code

	req := jsonRequest(http.MethodPost, "/api/u/cdks/merge", `{"primary_code":"`+primary+`","secondary_code":"`+secondary+`"}`)
	req.AddCookie(&http.Cookie{Name: "cdk", Value: secondary})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := cdkCookieValue(rec.Result().Cookies()); got != primary {
		t.Fatalf("set cdk cookie = %q, want primary %q", got, primary)
	}
}

func TestHandleUserMergeCDKRejectsCurrentCDKOutsideMerge(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()
	now := time.Now()
	created, err := srv.cdk.createBatch(3, bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("create cdks: %v", err)
	}
	current, primary, secondary := created[0].Code, created[1].Code, created[2].Code

	req := jsonRequest(http.MethodPost, "/api/u/cdks/merge", `{"primary_code":"`+primary+`","secondary_code":"`+secondary+`"}`)
	req.AddCookie(&http.Cookie{Name: "cdk", Value: current})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("merge status = %d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if _, ok, err := srv.cdk.get(secondary); err != nil || !ok {
		t.Fatalf("secondary should remain, ok=%v err=%v", ok, err)
	}
}

func TestHandleUserMergeCDKRejectsProxyMismatch(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()
	now := time.Now()
	primary, err := srv.cdk.createBatch(1, bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("create primary: %v", err)
	}
	secondary, err := srv.cdk.createBatch(1, bytesPerGB, 30, false, now)
	if err != nil {
		t.Fatalf("create secondary: %v", err)
	}

	req := jsonRequest(http.MethodPost, "/api/u/cdks/merge", `{"primary_code":"`+primary[0].Code+`","secondary_code":"`+secondary[0].Code+`"}`)
	req.AddCookie(&http.Cookie{Name: "cdk", Value: primary[0].Code})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("merge status = %d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "CDK 中转权限不同，不允许合并") {
		t.Fatalf("expected proxy mismatch message, got %s", rec.Body.String())
	}
}

func cdkCookieValue(cookies []*http.Cookie) string {
	for _, c := range cookies {
		if c.Name == "cdk" {
			return c.Value
		}
	}
	return ""
}

func TestCDKViewDaysLeft(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := CDK{
		Code:           "AAAA-BBBB-CCCC-DDDD",
		RemainingBytes: 5 * bytesPerGB,
		ExpiresAt:      now.Add(72 * time.Hour).Unix(),
		CreatedAt:      now.Unix(),
	}
	v := toCDKView(c, now)
	if v.Expired {
		t.Fatal("should not be expired")
	}
	if v.DaysLeft != 3 {
		t.Fatalf("expected 3 days left, got %d", v.DaysLeft)
	}
	if v.RemainingBytes != 5*bytesPerGB || v.RemainingLabel != "5 GB" {
		t.Fatalf("unexpected remaining view: bytes=%d label=%q", v.RemainingBytes, v.RemainingLabel)
	}

	expiredView := toCDKView(CDK{ExpiresAt: now.Add(-time.Hour).Unix()}, now)
	if !expiredView.Expired || expiredView.DaysLeft != 0 {
		t.Fatalf("expected expired view, got %+v", expiredView)
	}
}

// TestUserJobViewHidesAccountInfo locks in the security boundary: a CDK user
// must never receive the raw error/message, which embed PikPak account
// usernames (the "all accounts failed: <email>: ..." leak).
func TestUserJobViewHidesAccountInfo(t *testing.T) {
	leak := "all PikPak accounts failed: alice@passinbox.com: record not found; bob@passinbox.com: record not found"

	failed := toUserJobView(&Job{
		ID:      "job1",
		Status:  JobFailed,
		Stage:   StageFailed,
		Message: "starting with alice@passinbox.com",
		Error:   leak,
	})
	if failed.Error != genericUserJobError {
		t.Fatalf("failed job error should be generic, got %q", failed.Error)
	}
	if failed.Message != "" {
		t.Fatalf("failed job should expose no message, got %q", failed.Message)
	}
	if contains(failed.Error, "@") || contains(failed.Message, "@") {
		t.Fatalf("account email leaked: error=%q message=%q", failed.Error, failed.Message)
	}

	// A running job's internal "starting with <username>" message must not pass
	// through either.
	running := toUserJobView(&Job{
		ID:      "job2",
		Status:  JobRunning,
		Stage:   StageTransfer,
		Message: "starting with bob@passinbox.com",
	})
	if contains(running.Message, "@") || contains(running.Message, "bob") {
		t.Fatalf("running message leaked account info: %q", running.Message)
	}
	if running.Error != "" {
		t.Fatalf("running job should have no error, got %q", running.Error)
	}

	// Defense in depth: an error set without a failed status is still scrubbed.
	weird := toUserJobView(&Job{ID: "job3", Status: JobRunning, Error: leak})
	if weird.Error != genericUserJobError {
		t.Fatalf("stray error not scrubbed, got %q", weird.Error)
	}

	badResource := toUserJobView(&Job{ID: "job4", Status: JobFailed, Error: badResourceParseUserError})
	if badResource.Error != badResourceParseUserError {
		t.Fatalf("bad resource error should be user-visible, got %q", badResource.Error)
	}
	if contains(badResource.Error, "@") {
		t.Fatalf("bad resource error leaked account info: %q", badResource.Error)
	}

	overdraw := toUserJobView(&Job{ID: "job5", Status: JobFailed, Error: (errCDKOverdraw{size: 2 * bytesPerGB, remaining: bytesPerGB}).Error()})
	if overdraw.Error == genericUserJobError {
		t.Fatalf("CDK overdraw should be a safe user-visible error, got generic")
	}
	if contains(overdraw.Error, "@") {
		t.Fatalf("CDK overdraw leaked account info: %q", overdraw.Error)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// TestApplyItemsSelectionRejectsOverdraw locks in the traffic gate: a CDK user
// picking result files whose SUMMED size exceeds the CDK's remaining traffic
// must be refused at selection time with a 403, not silently absorbed at charge
// time. A selection that fits is accepted.
func TestApplyItemsSelectionRejectsOverdraw(t *testing.T) {
	now := time.Now()
	store := newTestCDKStore(t)
	created, _ := store.createBatch(1, 1*bytesPerGB, 30, true, now)
	code := created[0].Code

	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{cdk: store, jobs: newJobStore(10), resolver: noopResolver}

	items := []DownloadItem{
		{ID: "a", Name: "a.bin", Size: itoa64(512 * 1024 * 1024)}, // 0.5G
		{ID: "b", Name: "b.bin", Size: itoa64(512 * 1024 * 1024)}, // 0.5G
		{ID: "c", Name: "c.bin", Size: itoa64(512 * 1024 * 1024)}, // 0.5G
	}
	mkJob := func(id string) string {
		job := &Job{ID: id, Status: JobSelectionRequired, Stage: StageResultSelection, CDKCode: code, Items: items}
		s.jobs.create(job)
		return id
	}

	// 1.5G summed > 1G remaining → refused.
	if _, status, msg := s.applyItemsSelection(mkJob("job-over"), []string{"a", "b", "c"}); status != 403 {
		t.Fatalf("expected 403 for oversized batch, got status=%d msg=%q", status, msg)
	}

	// 1.0G summed == 1G remaining → allowed (not strictly greater).
	if _, status, msg := s.applyItemsSelection(mkJob("job-fit"), []string{"a", "b"}); status != 0 {
		t.Fatalf("expected fitting batch to succeed, got status=%d msg=%q", status, msg)
	}

	// Empty selection is a bad request.
	if _, status, _ := s.applyItemsSelection(mkJob("job-empty"), []string{"  "}); status != 400 {
		t.Fatalf("expected 400 for empty selection, got status=%d", status)
	}

	// Unknown item id is rejected.
	if _, status, _ := s.applyItemsSelection(mkJob("job-unknown"), []string{"a", "zzz"}); status != 400 {
		t.Fatalf("expected 400 for unknown item, got status=%d", status)
	}
}

func TestApplyItemSelectionRejectsUnknownSourceItem(t *testing.T) {
	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{jobs: newJobStore(10), resolver: noopResolver}
	s.jobs.create(&Job{
		ID:     "source-job",
		Status: JobSelectionRequired,
		Stage:  StageSourceSelection,
		Share:  &ShareState{ShareID: "share"},
		Items:  []DownloadItem{{ID: "known", Name: "known"}},
	})

	if _, status, msg := s.applyItemSelection("source-job", "unknown"); status != 400 {
		t.Fatalf("status=%d msg=%q, want 400", status, msg)
	}
}

func TestApplyItemSelectionDuplicateSubmitOnlyQueuesOnce(t *testing.T) {
	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{jobs: newJobStore(10), resolver: noopResolver}
	s.jobs.create(&Job{
		ID:     "source-job",
		Status: JobSelectionRequired,
		Stage:  StageSourceSelection,
		Share:  &ShareState{ShareID: "share"},
		Items:  []DownloadItem{{ID: "known", Name: "known", Path: "folder/known"}},
	})

	updated, status, msg := s.applyItemSelection("source-job", "known")
	if status != 0 {
		t.Fatalf("first selection status=%d msg=%q, want success", status, msg)
	}
	if updated.Share == nil || len(updated.Share.SelectedItems) != 1 || updated.Share.SelectedItems[0].Path != "folder/known" {
		t.Fatalf("selected source item = %+v, want path folder/known", updated.Share)
	}
	if _, status, msg := s.applyItemSelection("source-job", "known"); status != 409 {
		t.Fatalf("second selection status=%d msg=%q, want 409", status, msg)
	}
	if queued := noopResolver.queuedIDs(); len(queued) != 1 || queued[0] != "source-job" {
		t.Fatalf("queued IDs = %v, want [source-job]", queued)
	}
}

func TestApplyItemsSelectionAcceptsMultipleSourceItems(t *testing.T) {
	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{jobs: newJobStore(10), resolver: noopResolver}
	s.jobs.create(&Job{
		ID:     "source-job",
		Status: JobSelectionRequired,
		Stage:  StageSourceSelection,
		Share:  &ShareState{ShareID: "share"},
		Items: []DownloadItem{
			{ID: "a", Name: "a.bin", Path: "root/a.bin", Size: itoa64(256 * 1024 * 1024)},
			{ID: "b", Name: "b.bin", Path: "root/b.bin", Size: itoa64(256 * 1024 * 1024)},
			{ID: "c", Name: "c.bin", Path: "root/c.bin", Size: itoa64(256 * 1024 * 1024)},
		},
	})

	updated, status, msg := s.applyItemsSelection("source-job", []string{"b", "a", "b"})
	if status != 0 {
		t.Fatalf("source multi selection status=%d msg=%q, want success", status, msg)
	}
	if updated.Status != JobQueued || updated.Stage != StageTransfer {
		t.Fatalf("updated job status/stage = %s/%s, want queued/transfer", updated.Status, updated.Stage)
	}
	if updated.Share == nil || updated.Share.SelectedID != "b" || len(updated.Share.SelectedIDs) != 2 || updated.Share.SelectedIDs[0] != "b" || updated.Share.SelectedIDs[1] != "a" {
		t.Fatalf("selected share ids = %+v, want [b a]", updated.Share)
	}
	if len(updated.Share.SelectedItems) != 2 || updated.Share.SelectedItems[0].Path != "root/b.bin" || updated.Share.SelectedItems[1].Path != "root/a.bin" {
		t.Fatalf("selected source items = %+v, want paths [root/b.bin root/a.bin]", updated.Share.SelectedItems)
	}
	if !updated.ResolveSelected {
		t.Fatal("source multi selection should mark the job to resolve selected files directly")
	}
	if len(updated.Items) != 0 {
		t.Fatalf("selection items should be cleared, got %+v", updated.Items)
	}
	if queued := noopResolver.queuedIDs(); len(queued) != 1 || queued[0] != "source-job" {
		t.Fatalf("queued IDs = %v, want [source-job]", queued)
	}
}

func TestApplyItemsSelectionRejectsTooManyItems(t *testing.T) {
	ids := make([]string, maxSelectedFilesPerResolve+1)
	for i := range ids {
		ids[i] = "file-" + strconv.Itoa(i)
	}

	s := &Server{jobs: newJobStore(10)}
	if _, status, msg := s.applyItemsSelection("job-any", ids); status != http.StatusBadRequest {
		t.Fatalf("status=%d msg=%q, want 400", status, msg)
	}
}

func TestApplyItemsSelectionRejectsSourceOverdraw(t *testing.T) {
	now := time.Now()
	store := newTestCDKStore(t)
	created, _ := store.createBatch(1, 1*bytesPerGB, 30, true, now)
	code := created[0].Code

	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{cdk: store, jobs: newJobStore(10), resolver: noopResolver}
	s.jobs.create(&Job{
		ID:      "source-job",
		Status:  JobSelectionRequired,
		Stage:   StageSourceSelection,
		Share:   &ShareState{ShareID: "share"},
		CDKCode: code,
		Items: []DownloadItem{
			{ID: "a", Name: "a.bin", Size: itoa64(512 * 1024 * 1024)},
			{ID: "b", Name: "b.bin", Size: itoa64(512 * 1024 * 1024)},
			{ID: "c", Name: "c.bin", Size: itoa64(512 * 1024 * 1024)},
		},
	})

	if _, status, msg := s.applyItemsSelection("source-job", []string{"a", "b", "c"}); status != http.StatusForbidden {
		t.Fatalf("expected 403 for oversized source selection, got status=%d msg=%q", status, msg)
	}
}

// TestResultForToken locks in proxy-link routing for batch jobs: each resolved
// file's token must select its own result, across both the single Result and
// the multi-file Results slice.
func TestResultForToken(t *testing.T) {
	job := &Job{
		Result:  &JobResult{ProxyToken: "single-tok", File: DownloadItem{ID: "s"}},
		Results: []JobResult{{ProxyToken: "tok-a", File: DownloadItem{ID: "a"}}, {ProxyToken: "tok-b", File: DownloadItem{ID: "b"}}},
	}
	if r := job.resultForToken("tok-b"); r == nil || r.File.ID != "b" {
		t.Fatalf("expected result b, got %+v", r)
	}
	if r := job.resultForToken("single-tok"); r == nil || r.File.ID != "s" {
		t.Fatalf("expected single result, got %+v", r)
	}
	if r := job.resultForToken("nope"); r != nil {
		t.Fatalf("expected nil for unknown token, got %+v", r)
	}
	if r := job.resultForToken(""); r != nil {
		t.Fatalf("expected nil for empty token, got %+v", r)
	}
}

// TestCDKOverdrawError covers the single-file backstop used by finishWithItems:
// it returns a typed errCDKOverdraw only when a CDK job's file exceeds remaining
// traffic, and never blocks non-CDK jobs.
func TestCDKOverdrawError(t *testing.T) {
	now := time.Now()
	store := newTestCDKStore(t)
	created, _ := store.createBatch(1, 1*bytesPerGB, 30, true, now)
	code := created[0].Code

	s := &Server{cdk: store, jobs: newJobStore(10)}

	cdkJob := &Job{ID: "cdk-job", CDKCode: code}
	s.jobs.create(cdkJob)
	if err := s.cdkOverdrawError(cdkJob.ID, DownloadItem{Size: itoa64(2 * bytesPerGB)}); err == nil {
		t.Fatal("expected overdraw error for 2G file against 1G CDK")
	} else if _, ok := errAsOverdraw(err); !ok {
		t.Fatalf("expected errCDKOverdraw, got %T", err)
	}
	if err := s.cdkOverdrawError(cdkJob.ID, DownloadItem{Size: itoa64(512 * 1024 * 1024)}); err != nil {
		t.Fatalf("0.5G file should fit 1G CDK, got %v", err)
	}

	// A job with no CDK is never gated.
	plainJob := &Job{ID: "plain-job"}
	s.jobs.create(plainJob)
	if err := s.cdkOverdrawError(plainJob.ID, DownloadItem{Size: itoa64(99 * bytesPerGB)}); err != nil {
		t.Fatalf("non-CDK job must never be gated, got %v", err)
	}
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}

func errAsOverdraw(err error) (errCDKOverdraw, bool) {
	var o errCDKOverdraw
	ok := errors.As(err, &o)
	return o, ok
}
