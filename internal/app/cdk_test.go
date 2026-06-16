package app

import (
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

	created, err := store.createBatch(3, 5, 30, now)
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
		if c.Remaining != 5 {
			t.Fatalf("expected remaining 5, got %d", c.Remaining)
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

func TestCDKReserveDecrementsAndGuards(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 2, 30, now)
	code := created[0].Code

	c, err := store.reserve(code, now)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if c.Remaining != 1 || c.Used != 1 {
		t.Fatalf("after first reserve: remaining=%d used=%d", c.Remaining, c.Used)
	}

	if _, err := store.reserve(code, now); err != nil {
		t.Fatalf("second reserve: %v", err)
	}

	// Third reserve must fail: quota exhausted.
	if _, err := store.reserve(code, now); err != errCDKExhausted {
		t.Fatalf("expected errCDKExhausted, got %v", err)
	}

	// Unknown code.
	if _, err := store.reserve("NOPE-NOPE-NOPE-NOPE", now); err != errCDKNotFound {
		t.Fatalf("expected errCDKNotFound, got %v", err)
	}
}

func TestCDKReserveRejectsExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 5, 1, now)
	code := created[0].Code

	later := now.Add(48 * time.Hour) // past the 1-day expiry
	if _, err := store.reserve(code, later); err != errCDKExpired {
		t.Fatalf("expected errCDKExpired, got %v", err)
	}
}

func TestCDKRefund(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 3, 30, now)
	code := created[0].Code

	if _, err := store.reserve(code, now); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := store.refund(code); err != nil {
		t.Fatalf("refund: %v", err)
	}
	c, _, _ := store.get(code)
	if c.Remaining != 3 || c.Used != 0 {
		t.Fatalf("after refund: remaining=%d used=%d, want 3/0", c.Remaining, c.Used)
	}
}

func TestCDKUpdateAndDelete(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 5, 10, now)
	code := created[0].Code

	updated, ok, err := store.update(code, 20, 60, now)
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if updated.Remaining != 20 {
		t.Fatalf("expected remaining 20, got %d", updated.Remaining)
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

func TestCDKViewDaysLeft(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := CDK{
		Code:      "AAAA-BBBB-CCCC-DDDD",
		Remaining: 5,
		ExpiresAt: now.Add(72 * time.Hour).Unix(),
		CreatedAt: now.Unix(),
	}
	v := toCDKView(c, now)
	if v.Expired {
		t.Fatal("should not be expired")
	}
	if v.DaysLeft != 3 {
		t.Fatalf("expected 3 days left, got %d", v.DaysLeft)
	}

	expiredView := toCDKView(CDK{ExpiresAt: now.Add(-time.Hour).Unix()}, now)
	if !expiredView.Expired || expiredView.DaysLeft != 0 {
		t.Fatalf("expected expired view, got %+v", expiredView)
	}
}
