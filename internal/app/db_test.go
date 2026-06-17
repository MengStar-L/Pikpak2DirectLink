package app

import (
	"database/sql"
	"testing"
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
