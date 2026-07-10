package app

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteDatabaseDSNUsesSafeFileURI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database ?# %.sqlite")
	dsn, err := sqliteDatabaseDSN(path)
	if err != nil {
		t.Fatalf("sqliteDatabaseDSN: %v", err)
	}

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	wantPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("absolute database path: %v", err)
	}
	wantURIPath := filepath.ToSlash(wantPath)
	if filepath.VolumeName(wantPath) != "" && wantURIPath[0] != '/' {
		wantURIPath = "/" + wantURIPath
	}
	if u.Path != wantURIPath {
		t.Fatalf("DSN path = %q, want %q", u.Path, wantURIPath)
	}

	query := u.Query()
	if got := query.Get("_txlock"); got != "immediate" {
		t.Fatalf("_txlock = %q, want immediate", got)
	}
	wantPragmas := map[string]bool{
		"busy_timeout(5000)": false,
		"foreign_keys(1)":    false,
		"journal_mode(WAL)":  false,
		"synchronous(FULL)":  false,
	}
	for _, pragma := range query["_pragma"] {
		if _, ok := wantPragmas[pragma]; ok {
			wantPragmas[pragma] = true
		}
	}
	for pragma, found := range wantPragmas {
		if !found {
			t.Errorf("DSN missing _pragma=%s", pragma)
		}
	}
}

func TestOpenDatabaseConfiguresEveryDiskConnection(t *testing.T) {
	// '?' is not a legal Windows filename character; '#' and '%' still catch
	// accidental URI-fragment and percent-escape handling on every platform.
	path := filepath.Join(t.TempDir(), "nested dir", "database # %.sqlite")
	db, err := openDatabase(path)
	if err != nil {
		dsn, _ := sqliteDatabaseDSN(path)
		t.Fatalf("openDatabase with DSN %q: %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database was not created at the requested path: %v", err)
	}
	if got := db.Stats().MaxOpenConnections; got != diskDatabaseMaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, diskDatabaseMaxOpenConns)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connections := make([]*sql.Conn, 0, diskDatabaseMaxOpenConns)
	for range diskDatabaseMaxOpenConns {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("acquire connection %d: %v", len(connections), err)
		}
		connections = append(connections, conn)
	}
	t.Cleanup(func() {
		for _, conn := range connections {
			conn.Close()
		}
	})

	for i, conn := range connections {
		assertSQLitePragmaInt(t, conn, i, "foreign_keys", 1)
		assertSQLitePragmaInt(t, conn, i, "busy_timeout", 5000)
		assertSQLitePragmaInt(t, conn, i, "synchronous", 2)

		var journalMode string
		if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
			t.Fatalf("connection %d journal_mode: %v", i, err)
		}
		if journalMode != "wal" {
			t.Errorf("connection %d journal_mode = %q, want wal", i, journalMode)
		}
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatalf("release connection: %v", err)
		}
	}
	connections = nil
	if got := db.Stats().Idle; got != diskDatabaseMaxIdleConns {
		t.Fatalf("idle connections = %d, want %d", got, diskDatabaseMaxIdleConns)
	}
}

func TestOpenDatabaseKeepsMemoryDatabaseOnOneConnection(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	if has, err := tableExists(db, "resolve_jobs"); err != nil || !has {
		t.Fatalf("resolve_jobs exists = %v, err = %v; want true, nil", has, err)
	}
}

func TestStorageSchemaMigrationIsCompleteAndIdempotent(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	wantTables := []string{
		"schema_migrations",
		"pikpak_accounts",
		"pikpak_account_sessions",
		"admin_credentials",
		"admin_sessions",
		"app_secrets",
		"resolve_jobs",
		"storage_migration_state",
		"backup_runs",
	}
	for _, table := range wantTables {
		has, err := tableExists(db, table)
		if err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if !has {
			t.Errorf("missing table %s", table)
		}
	}
	if has, err := columnExists(db, "user_sessions", "token_hash"); err != nil || !has {
		t.Fatalf("fresh user_sessions token_hash exists = %v, err = %v; want true, nil", has, err)
	}
	if has, err := columnExists(db, "user_sessions", "token"); err != nil || has {
		t.Fatalf("fresh user_sessions legacy token exists = %v, err = %v; want false, nil", has, err)
	}

	var migrationName string
	if err := db.QueryRow(
		`SELECT name FROM schema_migrations WHERE version = ?`, storageSchemaVersion,
	).Scan(&migrationName); err != nil {
		t.Fatalf("read storage migration: %v", err)
	}
	if migrationName != storageSchemaMigrationName {
		t.Fatalf("migration name = %q, want %q", migrationName, storageSchemaMigrationName)
	}

	if _, err := db.Exec(`INSERT INTO app_secrets(key, ciphertext, updated_at) VALUES('test', 'v1.payload', 1)`); err != nil {
		t.Fatalf("seed app secret: %v", err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var ciphertext string
	if err := db.QueryRow(`SELECT ciphertext FROM app_secrets WHERE key = 'test'`).Scan(&ciphertext); err != nil {
		t.Fatalf("read app secret after second migration: %v", err)
	}
	if ciphertext != "v1.payload" {
		t.Fatalf("second migration changed app secret to %q", ciphertext)
	}

	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, storageSchemaVersion,
	).Scan(&count); err != nil {
		t.Fatalf("count storage migration records: %v", err)
	}
	if count != 1 {
		t.Fatalf("storage migration record count = %d, want 1", count)
	}
}

func TestPikPakSessionCascadesWithAccount(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`
		INSERT INTO pikpak_accounts(
			id, sort_order, username, password_encrypted, created_at, updated_at
		) VALUES('account-1', 0, 'person@example.com', 'v1.password', 1, 1);
		INSERT INTO pikpak_account_sessions(account_id, session_encrypted, updated_at)
		VALUES('account-1', 'v1.session', 1);
	`); err != nil {
		t.Fatalf("seed account and session: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM pikpak_accounts WHERE id = 'account-1'`); err != nil {
		t.Fatalf("delete account: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pikpak_account_sessions WHERE account_id = 'account-1'`).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 0 {
		t.Fatalf("session count after account deletion = %d, want 0", count)
	}
}

type pragmaQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func assertSQLitePragmaInt(t *testing.T, conn pragmaQuerier, connectionIndex int, name string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRowContext(context.Background(), "PRAGMA "+name).Scan(&got); err != nil {
		t.Fatalf("connection %d %s: %v", connectionIndex, name, err)
	}
	if got != want {
		t.Errorf("connection %d %s = %d, want %d", connectionIndex, name, got, want)
	}
}

func tableExists(db *sql.DB, table string) (bool, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
	).Scan(&count)
	return count == 1, err
}
