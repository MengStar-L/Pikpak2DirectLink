package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// openDatabase opens (creating if needed) the SQLite database used for CDK
// storage and applies the schema. modernc.org/sqlite is a pure-Go driver, so the
// binary still cross-compiles with CGO disabled.
func openDatabase(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection serializes all access, which sidesteps SQLITE_BUSY
	// under the polling UI without needing WAL juggling.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS cdks (
    code            TEXT PRIMARY KEY,
    remaining_bytes INTEGER NOT NULL,
    used_bytes      INTEGER NOT NULL DEFAULT 0,
    expires_at      INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    allow_proxy     INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cdk_resolve_history (
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
CREATE INDEX IF NOT EXISTS idx_cdk_resolve_history_cdk_completed
ON cdk_resolve_history(cdk_code, completed_at DESC);
CREATE INDEX IF NOT EXISTS idx_cdk_resolve_history_expires
ON cdk_resolve_history(expires_at);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := migrateCDKToTraffic(db); err != nil {
		return fmt.Errorf("migrate cdks to traffic: %w", err)
	}
	if err := migrateCDKAddAllowProxy(db); err != nil {
		return fmt.Errorf("migrate cdks add allow_proxy: %w", err)
	}
	return nil
}

// migrateCDKAddAllowProxy adds the allow_proxy column to a pre-existing cdks
// table. Existing CDKs default to 1 (proxy allowed) so prior behavior is
// preserved. Fresh databases already have the column from the schema above, so
// this is a no-op for them and idempotent thereafter.
func migrateCDKAddAllowProxy(db *sql.DB) error {
	has, err := columnExists(db, "cdks", "allow_proxy")
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE cdks ADD COLUMN allow_proxy INTEGER NOT NULL DEFAULT 1`)
	return err
}

// legacyCDKBytesPerCredit converts an existing count-based CDK credit into a
// downstream-traffic allowance: one parse credit becomes 2 GiB.
const legacyCDKBytesPerCredit = int64(2) << 30

// migrateCDKToTraffic upgrades a pre-existing count-based cdks table (columns
// remaining/used) to the traffic-based schema (remaining_bytes/used_bytes),
// converting each credit to 2 GiB. The presence of the remaining_bytes column is
// itself the version marker, so this is idempotent: once migrated, the legacy
// `remaining` column is gone and this is a no-op. Fresh databases already have
// the new schema and skip the rebuild.
func migrateCDKToTraffic(db *sql.DB) error {
	hasLegacy, err := columnExists(db, "cdks", "remaining")
	if err != nil {
		return err
	}
	hasBytes, err := columnExists(db, "cdks", "remaining_bytes")
	if err != nil {
		return err
	}
	if !hasLegacy || hasBytes {
		return nil // already on the new schema (or a fresh install)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`CREATE TABLE cdks_new (
            code            TEXT PRIMARY KEY,
            remaining_bytes INTEGER NOT NULL,
            used_bytes      INTEGER NOT NULL DEFAULT 0,
            expires_at      INTEGER NOT NULL,
            created_at      INTEGER NOT NULL
        )`,
		fmt.Sprintf(`INSERT INTO cdks_new (code, remaining_bytes, used_bytes, expires_at, created_at)
            SELECT code, remaining * %d, used * %d, expires_at, created_at FROM cdks`,
			legacyCDKBytesPerCredit, legacyCDKBytesPerCredit),
		`DROP TABLE cdks`,
		`ALTER TABLE cdks_new RENAME TO cdks`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// columnExists reports whether a table has a column of the given name.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
