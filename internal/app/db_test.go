package app

import (
	"database/sql"
	"testing"
	"time"
)

// TestMigrateLegacyCDKToTraffic verifies the one-time rebuild of a count-based
// cdks table into the traffic-based schema: each credit becomes 2 GiB, and the
// migration is idempotent (running it again is a no-op).
func TestMigrateLegacyCDKToTraffic(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1) // keep the single in-memory connection alive
	t.Cleanup(func() { db.Close() })

	// Build the legacy schema and seed a couple of rows.
	_, err = db.Exec(`CREATE TABLE cdks (
        code        TEXT PRIMARY KEY,
        remaining   INTEGER NOT NULL,
        used        INTEGER NOT NULL DEFAULT 0,
        expires_at  INTEGER NOT NULL,
        created_at  INTEGER NOT NULL
    )`)
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cdks(code, remaining, used, expires_at, created_at) VALUES('OLD1', 5, 2, 9999999999, 1700000000)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Columns should now be the byte-based schema.
	hasLegacy, _ := columnExists(db, "cdks", "remaining")
	hasBytes, _ := columnExists(db, "cdks", "remaining_bytes")
	if hasLegacy || !hasBytes {
		t.Fatalf("schema not rebuilt: legacy=%v bytes=%v", hasLegacy, hasBytes)
	}

	var remaining, used int64
	if err := db.QueryRow(`SELECT remaining_bytes, used_bytes FROM cdks WHERE code='OLD1'`).Scan(&remaining, &used); err != nil {
		t.Fatalf("scan migrated row: %v", err)
	}
	if remaining != 5*legacyCDKBytesPerCredit || used != 2*legacyCDKBytesPerCredit {
		t.Fatalf("converted wrong: remaining=%d used=%d (want %d/%d)",
			remaining, used, 5*legacyCDKBytesPerCredit, 2*legacyCDKBytesPerCredit)
	}

	// Idempotent: a second pass leaves the data untouched.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := db.QueryRow(`SELECT remaining_bytes FROM cdks WHERE code='OLD1'`).Scan(&remaining); err != nil {
		t.Fatalf("scan after second migrate: %v", err)
	}
	if remaining != 5*legacyCDKBytesPerCredit {
		t.Fatalf("second migrate changed data: remaining=%d", remaining)
	}
}

// TestMigrateFreshDBUsesNewSchema confirms a brand-new database is created
// directly on the traffic schema (no legacy columns, no rebuild).
func TestMigrateFreshDBUsesNewSchema(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if has, _ := columnExists(db, "cdks", "remaining_bytes"); !has {
		t.Fatal("fresh db missing remaining_bytes column")
	}
	if has, _ := columnExists(db, "cdks", "remaining"); has {
		t.Fatal("fresh db should not have legacy remaining column")
	}
}

func TestMigrateAddsUserIDBeforeHistoryUserIndex(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`
CREATE TABLE cdks (
    code            TEXT PRIMARY KEY,
    remaining_bytes INTEGER NOT NULL,
    used_bytes      INTEGER NOT NULL DEFAULT 0,
    expires_at      INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    allow_proxy     INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE cdk_resolve_history (
    id           TEXT PRIMARY KEY,
    cdk_code     TEXT NOT NULL,
    job_id       TEXT NOT NULL,
    kind         TEXT NOT NULL,
    mode         TEXT NOT NULL,
    input        TEXT NOT NULL,
    results_json TEXT NOT NULL,
    batch_json   TEXT,
    created_at   INTEGER NOT NULL,
    completed_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);
CREATE INDEX idx_cdk_resolve_history_cdk_completed
ON cdk_resolve_history(cdk_code, completed_at DESC);
CREATE INDEX idx_cdk_resolve_history_expires
ON cdk_resolve_history(expires_at);
CREATE TABLE proxy_temp_cleanups (
    id             TEXT PRIMARY KEY,
    job_id         TEXT NOT NULL,
    account_id     TEXT NOT NULL,
    file_ids_json  TEXT NOT NULL,
    cleanup_after  INTEGER NOT NULL,
    attempts       INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT,
    created_at     INTEGER NOT NULL
);
CREATE INDEX idx_proxy_temp_cleanups_after
ON proxy_temp_cleanups(cleanup_after);`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	if _, err := db.Exec(
		`INSERT INTO cdk_resolve_history
		 (id, cdk_code, job_id, kind, mode, input, results_json, created_at, completed_at, expires_at)
		 VALUES('hist1', 'CDK-AAAA', 'job1', 'magnet', 'direct', 'magnet:?xt=urn:btih:test', '[]', ?, ?, ?)`,
		now.Unix(), now.Unix(), now.Add(time.Hour).Unix(),
	); err != nil {
		t.Fatalf("insert legacy history: %v", err)
	}

	if err := migrate(db); err != nil {
		t.Fatalf("migrate legacy db: %v", err)
	}
	if ok, err := columnExists(db, "cdk_resolve_history", "user_id"); err != nil || !ok {
		t.Fatalf("user_id column exists = %v err=%v, want true nil", ok, err)
	}

	var indexName string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_cdk_resolve_history_user_completed'`).Scan(&indexName); err != nil {
		t.Fatalf("history user index missing after migration: %v", err)
	}
}
