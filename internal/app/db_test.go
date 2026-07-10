package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestMigrateLegacyCDKToCredential verifies that each unused legacy credit
// becomes 2 GiB of grant snapshot without carrying the old used counter over.
func TestMigrateLegacyCDKToCredential(t *testing.T) {
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

	// Columns should now be the credential-only schema.
	hasLegacy, _ := columnExists(db, "cdks", "remaining")
	hasGrant, _ := columnExists(db, "cdks", "grant_bytes")
	if hasLegacy || !hasGrant {
		t.Fatalf("schema not rebuilt: legacy=%v grant=%v", hasLegacy, hasGrant)
	}

	var grant int64
	if err := db.QueryRow(`SELECT grant_bytes FROM cdks WHERE code='OLD1'`).Scan(&grant); err != nil {
		t.Fatalf("scan migrated row: %v", err)
	}
	if grant != 5*legacyCDKBytesPerCredit {
		t.Fatalf("converted grant=%d, want %d", grant, 5*legacyCDKBytesPerCredit)
	}

	// Idempotent: a second pass leaves the data untouched.
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := db.QueryRow(`SELECT grant_bytes FROM cdks WHERE code='OLD1'`).Scan(&grant); err != nil {
		t.Fatalf("scan after second migrate: %v", err)
	}
	if grant != 5*legacyCDKBytesPerCredit {
		t.Fatalf("second migrate changed data: grant=%d", grant)
	}
}

// TestMigrateFreshDBUsesNewSchema confirms a brand-new database is created
// directly on the credential schema (no live quota columns).
func TestMigrateFreshDBUsesNewSchema(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if has, _ := columnExists(db, "cdks", "grant_bytes"); !has {
		t.Fatal("fresh db missing grant_bytes column")
	}
	if has, _ := columnExists(db, "cdks", "remaining"); has {
		t.Fatal("fresh db should not have legacy remaining column")
	}
	if has, _ := columnExists(db, "cdks", "remaining_bytes"); has {
		t.Fatal("fresh db should not store subscription remaining_bytes")
	}
	assertPureCDKColumns(t, db)
	if has, _ := columnExists(db, "user_subscriptions", "quota_generation"); !has {
		t.Fatal("fresh db missing user_subscriptions.quota_generation")
	}
	if has, _ := columnExists(db, "user_subscriptions", "revision"); !has {
		t.Fatal("fresh db missing user_subscriptions.revision")
	}
	if has, _ := columnExists(db, "user_subscriptions", "terminated_at"); !has {
		t.Fatal("fresh db missing user_subscriptions.terminated_at")
	}
	if has, _ := columnExists(db, "user_quota_reservations", "quota_generation"); !has {
		t.Fatal("fresh db missing user_quota_reservations.quota_generation")
	}
	var quotaIndexes int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master
		 WHERE type='index'
		   AND name IN (
		       'idx_user_subscriptions_source_cdk',
		       'idx_user_quota_reservations_subscription',
		       'idx_users_created_id',
		       'idx_user_subscriptions_user_timeline'
		   )`,
	).Scan(&quotaIndexes); err != nil {
		t.Fatalf("count quota indexes: %v", err)
	}
	if quotaIndexes != 4 {
		t.Fatalf("quota indexes = %d, want 4", quotaIndexes)
	}
}

func TestMigrateQuotaReservationGenerationsIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE user_subscriptions (
			id TEXT PRIMARY KEY,
			remaining_bytes INTEGER NOT NULL
		);
		CREATE TABLE user_quota_reservations (
			job_id TEXT NOT NULL,
			subscription_id TEXT NOT NULL,
			reserved_bytes INTEGER NOT NULL,
			PRIMARY KEY(job_id, subscription_id)
		);
		INSERT INTO user_subscriptions(id, remaining_bytes) VALUES('subscription', 90);
		INSERT INTO user_quota_reservations(job_id, subscription_id, reserved_bytes)
		VALUES('job', 'subscription', 10);
	`); err != nil {
		t.Fatalf("create legacy quota schema: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := migrateQuotaReservationGenerations(db); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt, err)
		}
	}
	var subscriptionGeneration, reservationGeneration int64
	if err := db.QueryRow(`SELECT quota_generation FROM user_subscriptions WHERE id='subscription'`).Scan(&subscriptionGeneration); err != nil {
		t.Fatalf("read migrated subscription: %v", err)
	}
	if err := db.QueryRow(`SELECT quota_generation FROM user_quota_reservations WHERE job_id='job'`).Scan(&reservationGeneration); err != nil {
		t.Fatalf("read migrated reservation: %v", err)
	}
	if subscriptionGeneration != 0 || reservationGeneration != 0 {
		t.Fatalf("migrated generations = subscription:%d reservation:%d, want 0/0", subscriptionGeneration, reservationGeneration)
	}
}

func TestOpenDatabaseCreatesPrivateDirectoryAndDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	root := t.TempDir()
	directory := filepath.Join(root, "private", "nested")
	path := filepath.Join(directory, "app.db")
	db, err := openDatabase(path)
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat database directory: %v", err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("database directory mode = %o, want 700", got)
	}
	databaseInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat database file: %v", err)
	}
	if got := databaseInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("database file mode = %o, want 600", got)
	}
}

func TestHardenSQLiteFileModesSecuresExistingSidecars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	root := t.TempDir()
	path := filepath.Join(root, "app.db")
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.WriteFile(candidate, []byte("test"), 0o666); err != nil {
			t.Fatalf("write %s: %v", candidate, err)
		}
		if err := os.Chmod(candidate, 0o666); err != nil {
			t.Fatalf("chmod %s: %v", candidate, err)
		}
	}
	if err := hardenSQLiteFileModes(path); err != nil {
		t.Fatalf("hardenSQLiteFileModes: %v", err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %o, want 600", candidate, got)
		}
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

func TestMigrateUserSessionsHashesLegacyTokensWithoutLoggingUsersOut(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)

	_, err = db.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY, email TEXT, display_name TEXT NOT NULL DEFAULT '',
			avatar_url TEXT NOT NULL DEFAULT '', disabled INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
		);
		CREATE TABLE user_sessions (
			token TEXT PRIMARY KEY, user_id TEXT NOT NULL, expires_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX idx_user_sessions_user ON user_sessions(user_id);
		INSERT INTO users(id, email, created_at, updated_at) VALUES('usr_1','u@example.com',1,1);
		INSERT INTO user_sessions(token,user_id,expires_at,created_at)
		VALUES('raw-cookie-token','usr_1',4102444800,1);
	`)
	if err != nil {
		t.Fatalf("seed legacy sessions: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if has, _ := columnExists(db, "user_sessions", "token"); has {
		t.Fatal("legacy token column remains")
	}
	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM user_sessions`).Scan(&stored); err != nil {
		t.Fatalf("read token hash: %v", err)
	}
	if stored != sessionTokenDigest("raw-cookie-token") {
		t.Fatalf("token hash = %q", stored)
	}
	user, ok, err := newUserStore(db).userForSession("raw-cookie-token", time.Unix(2, 0))
	if err != nil || !ok || user.ID != "usr_1" {
		t.Fatalf("legacy cookie lookup: user=%+v ok=%v err=%v", user, ok, err)
	}
}
