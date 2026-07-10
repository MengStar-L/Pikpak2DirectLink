package app

import (
	"strings"
	"testing"
	"time"
)

func TestDatabaseCredentialStorePersists(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := newDatabaseCredentialStore(db)
	if err != nil {
		t.Fatalf("newDatabaseCredentialStore: %v", err)
	}
	if store.HasPassword() {
		t.Fatal("fresh database unexpectedly has an admin password")
	}
	if err := store.SetInitial("admin-secret"); err != nil {
		t.Fatalf("SetInitial: %v", err)
	}
	if err := store.SetInitial("other-secret"); err == nil {
		t.Fatal("second SetInitial unexpectedly succeeded")
	}

	reopened, err := newDatabaseCredentialStore(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened.Verify("admin-secret") || reopened.Verify("wrong") {
		t.Fatal("persisted credential verification mismatch")
	}
	var raw string
	if err := db.QueryRow(`SELECT password_hash FROM admin_credentials WHERE id=1`).Scan(&raw); err != nil {
		t.Fatalf("read password hash: %v", err)
	}
	if strings.Contains(raw, "admin-secret") {
		t.Fatal("admin password was stored in plaintext")
	}
}

func TestDatabaseAuthSessionsStoreOnlyDigestAndSurviveReopen(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	store := newDatabaseAuthSessionStore(db, func() time.Time { return now })
	token, err := store.create()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !store.validate(token) {
		t.Fatal("new session was not valid")
	}
	var digest string
	if err := db.QueryRow(`SELECT token_hash FROM admin_sessions`).Scan(&digest); err != nil {
		t.Fatalf("read token digest: %v", err)
	}
	if digest == token || strings.Contains(digest, token) {
		t.Fatal("raw bearer token was stored in the database")
	}
	if digest != sessionTokenDigest(token) {
		t.Fatalf("stored digest = %q, want %q", digest, sessionTokenDigest(token))
	}

	reopened := newDatabaseAuthSessionStore(db, func() time.Time { return now })
	if !reopened.validate(token) {
		t.Fatal("session did not survive store reconstruction")
	}
	reopened.delete(token)
	if reopened.validate(token) {
		t.Fatal("deleted session remained valid")
	}
}

func TestDatabaseAuthSessionExpiryAndInvalidateAll(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	store := newDatabaseAuthSessionStore(db, func() time.Time { return now })
	first, err := store.create()
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := store.create()
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	store.invalidateAll()
	if store.validate(first) || store.validate(second) {
		t.Fatal("invalidateAll left a session valid")
	}

	expiring, err := store.create()
	if err != nil {
		t.Fatalf("create expiring: %v", err)
	}
	now = now.Add(adminSessionMaxAge + time.Second)
	if store.validate(expiring) {
		t.Fatal("expired session remained valid")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_sessions`).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("expired session was not pruned; count=%d", count)
	}
}
