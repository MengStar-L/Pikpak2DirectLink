package app

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	diskDatabaseMaxOpenConns = 8
	diskDatabaseMaxIdleConns = 4
)

// openDatabase opens (creating if needed) the application SQLite database and
// applies its schema. modernc.org/sqlite is a pure-Go driver, so the binary
// still cross-compiles with CGO disabled.
func openDatabase(path string) (*sql.DB, error) {
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "." && dir != "" {
			if err := durableMkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
		}
		if err := prepareDatabaseFile(path); err != nil {
			return nil, err
		}
	}

	dsn, err := sqliteDatabaseDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		// Each :memory: connection is a different database, so it must stay on
		// the single connection used to create the schema.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(diskDatabaseMaxOpenConns)
		db.SetMaxIdleConns(diskDatabaseMaxIdleConns)
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := hardenSQLiteFileModes(path); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func prepareDatabaseFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("prepare database file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure database file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close prepared database file: %w", err)
	}
	return nil
}

func hardenSQLiteFileModes(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("secure SQLite file %s: %w", candidate, err)
		}
	}
	return nil
}

// sqliteDatabaseDSN builds a URI instead of appending query parameters to the
// supplied filename. This keeps characters such as '?' and '#' in a database
// filename from being interpreted as part of the DSN. The modernc driver
// applies each _pragma whenever it opens a pooled connection.
func sqliteDatabaseDSN(path string) (string, error) {
	query := make(url.Values)
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Set("_txlock", "immediate")

	if path == ":memory:" {
		return path + "?" + query.Encode(), nil
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute database path: %w", err)
	}
	uriPath := filepath.ToSlash(absPath)
	if filepath.VolumeName(absPath) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query.Add("_pragma", "journal_mode(WAL)")
	u := url.URL{
		Scheme:   "file",
		Path:     uriPath,
		RawQuery: query.Encode(),
	}
	return u.String(), nil
}

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS cdks (
    code            TEXT PRIMARY KEY NOT NULL,
    grant_bytes     INTEGER NOT NULL,
    duration_days   INTEGER NOT NULL,
    allow_proxy     INTEGER NOT NULL DEFAULT 1,
    created_at      INTEGER NOT NULL,
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
CREATE INDEX IF NOT EXISTS idx_users_created_id
ON users(created_at DESC, id);
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
    token_hash TEXT PRIMARY KEY,
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
    quota_generation INTEGER NOT NULL DEFAULT 0,
    revision        INTEGER NOT NULL DEFAULT 1,
    terminated_at   INTEGER,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_user_expiry
ON user_subscriptions(user_id, expires_at);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_source_cdk
ON user_subscriptions(source_cdk_code, user_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_user_timeline
ON user_subscriptions(user_id, expires_at, created_at, id);
CREATE TABLE IF NOT EXISTS user_quota_reservations (
    job_id          TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    user_id         TEXT NOT NULL,
    reserved_bytes  INTEGER NOT NULL CHECK(reserved_bytes > 0),
    require_proxy   INTEGER NOT NULL DEFAULT 0 CHECK(require_proxy IN (0, 1)),
    created_at      INTEGER NOT NULL,
    quota_generation INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(job_id, subscription_id),
    FOREIGN KEY(subscription_id) REFERENCES user_subscriptions(id) ON DELETE CASCADE,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_quota_reservations_user
ON user_quota_reservations(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_user_quota_reservations_subscription
ON user_quota_reservations(subscription_id, require_proxy);
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
	if err := migrateQuotaReservationGenerations(db); err != nil {
		return fmt.Errorf("migrate quota reservation generations: %w", err)
	}
	if err := migrateUserSubscriptionAdminFields(db); err != nil {
		return fmt.Errorf("migrate user subscription admin fields: %w", err)
	}
	if err := migrateCDKsToCredentials(db); err != nil {
		return fmt.Errorf("migrate cdks to credentials: %w", err)
	}
	if err := migrateHistoryAddUserID(db); err != nil {
		return fmt.Errorf("migrate history add user id: %w", err)
	}
	if err := migrateUserSessionsToDigests(db); err != nil {
		return fmt.Errorf("migrate user sessions to digests: %w", err)
	}
	if err := migrateStorageSchema(db); err != nil {
		return fmt.Errorf("migrate storage schema: %w", err)
	}
	return nil
}

func migrateQuotaReservationGenerations(db *sql.DB) error {
	if err := addColumnIfMissing(
		db,
		"user_subscriptions",
		"quota_generation",
		`ALTER TABLE user_subscriptions ADD COLUMN quota_generation INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return err
	}
	return addColumnIfMissing(
		db,
		"user_quota_reservations",
		"quota_generation",
		`ALTER TABLE user_quota_reservations ADD COLUMN quota_generation INTEGER NOT NULL DEFAULT 0`,
	)
}

func migrateUserSubscriptionAdminFields(db *sql.DB) error {
	if err := addColumnIfMissing(
		db,
		"user_subscriptions",
		"revision",
		`ALTER TABLE user_subscriptions ADD COLUMN revision INTEGER NOT NULL DEFAULT 1`,
	); err != nil {
		return err
	}
	if err := addColumnIfMissing(
		db,
		"user_subscriptions",
		"terminated_at",
		`ALTER TABLE user_subscriptions ADD COLUMN terminated_at INTEGER`,
	); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_users_created_id ON users(created_at DESC, id)`); err != nil {
		return err
	}
	_, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_user_subscriptions_user_timeline ON user_subscriptions(user_id, expires_at, created_at, id)`)
	return err
}

func migrateUserSessionsToDigests(db *sql.DB) error {
	hasLegacyToken, err := columnExists(db, "user_sessions", "token")
	if err != nil || !hasLegacyToken {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT token, user_id, expires_at, created_at FROM user_sessions`)
	if err != nil {
		return err
	}
	type legacySession struct {
		token     string
		userID    string
		expiresAt int64
		createdAt int64
	}
	var sessions []legacySession
	for rows.Next() {
		var session legacySession
		if err := rows.Scan(&session.token, &session.userID, &session.expiresAt, &session.createdAt); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		CREATE TABLE user_sessions_new (
			token_hash TEXT PRIMARY KEY,
			user_id    TEXT NOT NULL,
			expires_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`); err != nil {
		return err
	}
	for _, session := range sessions {
		if _, err := tx.Exec(
			`INSERT INTO user_sessions_new(token_hash, user_id, expires_at, created_at) VALUES(?,?,?,?)`,
			sessionTokenDigest(session.token), session.userID, session.expiresAt, session.createdAt,
		); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`
		DROP TABLE user_sessions;
		ALTER TABLE user_sessions_new RENAME TO user_sessions;
		CREATE INDEX idx_user_sessions_user ON user_sessions(user_id);
	`); err != nil {
		return err
	}
	return tx.Commit()
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

// legacyCDKBytesPerCredit converts an existing count-based CDK credit into a
// downstream-traffic allowance: one parse credit becomes 2 GiB.
const legacyCDKBytesPerCredit = int64(2) << 30

var credentialCDKColumns = []string{
	"code",
	"grant_bytes",
	"duration_days",
	"allow_proxy",
	"created_at",
	"redeemed_by_user_id",
	"redeemed_at",
	"revoked_at",
}

// migrateCDKsToCredentials rebuilds every historical CDK representation into
// an issuance snapshot. In particular, remaining_bytes becomes grant_bytes
// without adding used_bytes back; live subscription state is never read or
// changed by this migration.
func migrateCDKsToCredentials(db *sql.DB) error {
	columns, err := tableColumnSet(db, "cdks")
	if err != nil {
		return err
	}
	if hasOnlyColumns(columns, credentialCDKColumns) {
		return nil
	}
	if !columns["code"] || !columns["created_at"] {
		return errors.New("unsupported cdks schema: code and created_at are required")
	}

	grantExpr := ""
	switch {
	case columns["grant_bytes"]:
		grantExpr = `max(grant_bytes, 0)`
	case columns["remaining_bytes"]:
		grantExpr = `max(remaining_bytes, 0)`
	case columns["remaining"]:
		grantExpr = fmt.Sprintf(`max(remaining, 0) * %d`, legacyCDKBytesPerCredit)
	default:
		return errors.New("unsupported cdks schema: no grant or remaining column")
	}

	durationExpr := "30"
	if columns["duration_days"] {
		durationExpr = `max(duration_days, 1)`
	} else if columns["expires_at"] {
		durationExpr = `max(1, CAST(((expires_at - created_at) + 86399) / 86400 AS INTEGER))`
	}
	allowProxyExpr := "1"
	if columns["allow_proxy"] {
		allowProxyExpr = "allow_proxy"
	}
	redeemedByExpr := "NULL"
	if columns["redeemed_by_user_id"] {
		redeemedByExpr = "redeemed_by_user_id"
	}
	redeemedAtExpr := "NULL"
	if columns["redeemed_at"] {
		redeemedAtExpr = "redeemed_at"
	}
	revokedAtExpr := "NULL"
	if columns["revoked_at"] {
		revokedAtExpr = "revoked_at"
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DROP TABLE IF EXISTS cdks_credentials_new`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE cdks_credentials_new (
		code                TEXT PRIMARY KEY NOT NULL,
		grant_bytes         INTEGER NOT NULL,
		duration_days       INTEGER NOT NULL,
		allow_proxy         INTEGER NOT NULL DEFAULT 1,
		created_at          INTEGER NOT NULL,
		redeemed_by_user_id TEXT,
		redeemed_at         INTEGER,
		revoked_at          INTEGER
	)`); err != nil {
		return err
	}
	copySQL := fmt.Sprintf(`INSERT INTO cdks_credentials_new (
		code, grant_bytes, duration_days, allow_proxy, created_at,
		redeemed_by_user_id, redeemed_at, revoked_at
	) SELECT code, %s, %s, %s, created_at, %s, %s, %s FROM cdks`,
		grantExpr, durationExpr, allowProxyExpr, redeemedByExpr, redeemedAtExpr, revokedAtExpr,
	)
	if _, err := tx.Exec(copySQL); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE cdks`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE cdks_credentials_new RENAME TO cdks`); err != nil {
		return err
	}
	return tx.Commit()
}

func tableColumnSet(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make(map[string]bool)
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
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func hasOnlyColumns(got map[string]bool, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, column := range want {
		if !got[column] {
			return false
		}
	}
	return true
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
