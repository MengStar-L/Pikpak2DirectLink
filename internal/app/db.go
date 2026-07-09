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
    allow_proxy     INTEGER NOT NULL DEFAULT 1,
    duration_days   INTEGER NOT NULL DEFAULT 30,
    redeemed_by_user_id TEXT,
    redeemed_at     INTEGER,
    revoked_at      INTEGER
);
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
    id           TEXT PRIMARY KEY,
    email        TEXT,
    display_name TEXT NOT NULL DEFAULT '',
    avatar_url   TEXT NOT NULL DEFAULT '',
    disabled     INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS user_identities (
    provider         TEXT NOT NULL,
    provider_user_id TEXT NOT NULL,
    user_id          TEXT NOT NULL,
    email            TEXT,
    username         TEXT NOT NULL DEFAULT '',
    display_name     TEXT NOT NULL DEFAULT '',
    avatar_url       TEXT NOT NULL DEFAULT '',
    password_hash    TEXT,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    PRIMARY KEY(provider, provider_user_id),
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_identities_user
ON user_identities(user_id);
CREATE TABLE IF NOT EXISTS user_sessions (
    token      TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user
ON user_sessions(user_id);
CREATE TABLE IF NOT EXISTS user_subscriptions (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    source_cdk_code TEXT,
    remaining_bytes INTEGER NOT NULL,
    used_bytes      INTEGER NOT NULL DEFAULT 0,
    expires_at      INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    allow_proxy     INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_user_expiry
ON user_subscriptions(user_id, expires_at);
CREATE TABLE IF NOT EXISTS cdk_resolve_history (
    id           TEXT PRIMARY KEY,
    cdk_code     TEXT NOT NULL,
    user_id      TEXT NOT NULL DEFAULT '',
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
ON cdk_resolve_history(expires_at);
CREATE TABLE IF NOT EXISTS proxy_temp_cleanups (
    id             TEXT PRIMARY KEY,
    job_id         TEXT NOT NULL,
    account_id     TEXT NOT NULL,
    file_ids_json  TEXT NOT NULL,
    cleanup_after  INTEGER NOT NULL,
    attempts       INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT,
    created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_proxy_temp_cleanups_after
ON proxy_temp_cleanups(cleanup_after);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := migrateCDKToTraffic(db); err != nil {
		return fmt.Errorf("migrate cdks to traffic: %w", err)
	}
	if err := migrateCDKAddAllowProxy(db); err != nil {
		return fmt.Errorf("migrate cdks add allow_proxy: %w", err)
	}
	if err := migrateCDKToVoucher(db); err != nil {
		return fmt.Errorf("migrate cdks to voucher: %w", err)
	}
	if err := migrateHistoryAddUserID(db); err != nil {
		return fmt.Errorf("migrate history add user id: %w", err)
	}
	return nil
}

func migrateCDKToVoucher(db *sql.DB) error {
	hadDurationDays, err := columnExists(db, "cdks", "duration_days")
	if err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "cdks", "duration_days", `ALTER TABLE cdks ADD COLUMN duration_days INTEGER NOT NULL DEFAULT 30`); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "cdks", "redeemed_by_user_id", `ALTER TABLE cdks ADD COLUMN redeemed_by_user_id TEXT`); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "cdks", "redeemed_at", `ALTER TABLE cdks ADD COLUMN redeemed_at INTEGER`); err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "cdks", "revoked_at", `ALTER TABLE cdks ADD COLUMN revoked_at INTEGER`); err != nil {
		return err
	}
	whereClause := `WHERE duration_days IS NULL OR duration_days <= 0`
	if !hadDurationDays {
		whereClause = ``
	}
	_, err = db.Exec(`
		UPDATE cdks
		SET duration_days = max(1, CAST(((expires_at - created_at) + 86399) / 86400 AS INTEGER))
		` + whereClause)
	return err
}

func migrateHistoryAddUserID(db *sql.DB) error {
	if err := addColumnIfMissing(db, "cdk_resolve_history", "user_id", `ALTER TABLE cdk_resolve_history ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_cdk_resolve_history_user_completed ON cdk_resolve_history(user_id, completed_at DESC)`)
	return err
}

func addColumnIfMissing(db *sql.DB, table, column, stmt string) error {
	has, err := columnExists(db, table, column)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err = db.Exec(stmt)
	return err
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
