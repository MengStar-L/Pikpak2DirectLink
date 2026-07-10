package app

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCDKCredentialLifecycle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, err := store.createBatch(2, 5*bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("createBatch: %v", err)
	}
	if len(created) != 2 || created[0].GrantBytes != 5*bytesPerGB {
		t.Fatalf("created credentials = %+v", created)
	}

	updated, ok, err := store.update(created[0].Code, 8*bytesPerGB, 45, false, now.Add(time.Hour))
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if updated.GrantBytes != 8*bytesPerGB || updated.DurationDays != 45 || updated.AllowProxy {
		t.Fatalf("updated credential = %+v", updated)
	}

	revoked, ok, err := store.revoke(created[0].Code, now.Add(2*time.Hour))
	if err != nil || !ok || revoked.RevokedAt == 0 {
		t.Fatalf("revoke: credential=%+v ok=%v err=%v", revoked, ok, err)
	}
	again, ok, err := store.revoke(created[0].Code, now.Add(3*time.Hour))
	if err != nil || !ok || again.RevokedAt != revoked.RevokedAt {
		t.Fatalf("idempotent revoke: credential=%+v ok=%v err=%v", again, ok, err)
	}
	if _, ok, err := store.update(created[0].Code, bytesPerGB, 10, true, now); !ok || !errors.Is(err, errVoucherRevoked) {
		t.Fatalf("update revoked credential: ok=%v err=%v", ok, err)
	}
}

func TestCDKCredentialRedeemedIsReadOnlyAndListIsSnapshot(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)
	created, err := store.createBatch(1, 5*bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("createBatch: %v", err)
	}
	code := created[0].Code
	if _, err := store.db.Exec(
		`INSERT INTO users(id, email, created_at, updated_at) VALUES('usr_snapshot', 'snapshot@example.com', ?, ?)`,
		now.Unix(), now.Unix(),
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO user_subscriptions(id, user_id, source_cdk_code, remaining_bytes, used_bytes, expires_at, created_at, allow_proxy)
		 VALUES('sub_snapshot', 'usr_snapshot', ?, 123, 456, ?, ?, 0)`,
		code, now.Add(10*24*time.Hour).Unix(), now.Unix(),
	); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
	if _, err := store.db.Exec(
		`UPDATE cdks SET redeemed_by_user_id='usr_snapshot', redeemed_at=? WHERE code=?`,
		now.Add(time.Hour).Unix(), code,
	); err != nil {
		t.Fatalf("mark redeemed: %v", err)
	}

	listed, err := store.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].GrantBytes != 5*bytesPerGB || !listed[0].AllowProxy {
		t.Fatalf("credential list projected subscription state: %+v", listed)
	}
	if _, ok, err := store.update(code, bytesPerGB, 10, false, now); !ok || !errors.Is(err, errVoucherRedeemed) {
		t.Fatalf("update redeemed credential: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.revoke(code, now); !ok || !errors.Is(err, errVoucherRedeemed) {
		t.Fatalf("revoke redeemed credential: ok=%v err=%v", ok, err)
	}
}

func TestMigrateCDKsToCredentialsFromHistoricalSchemas(t *testing.T) {
	tests := []struct {
		name      string
		ddl       string
		insert    string
		wantGrant int64
		wantDays  int
		wantProxy bool
		wantUser  string
	}{
		{
			name: "count",
			ddl: `CREATE TABLE cdks (
				code TEXT PRIMARY KEY, remaining INTEGER NOT NULL, used INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL
			)`,
			insert:    `INSERT INTO cdks VALUES('COUNT-CODE', 5, 2, 1700864000, 1700000000)`,
			wantGrant: 5 * legacyCDKBytesPerCredit,
			wantDays:  10,
			wantProxy: true,
		},
		{
			name: "traffic",
			ddl: `CREATE TABLE cdks (
				code TEXT PRIMARY KEY, remaining_bytes INTEGER NOT NULL, used_bytes INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL
			)`,
			insert:    `INSERT INTO cdks VALUES('TRAFFIC-CODE', 321, 777, 1700864000, 1700000000)`,
			wantGrant: 321,
			wantDays:  10,
			wantProxy: true,
		},
		{
			name: "voucher",
			ddl: `CREATE TABLE cdks (
				code TEXT PRIMARY KEY, remaining_bytes INTEGER NOT NULL, used_bytes INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL, allow_proxy INTEGER NOT NULL DEFAULT 1,
				duration_days INTEGER NOT NULL DEFAULT 30, redeemed_by_user_id TEXT, redeemed_at INTEGER, revoked_at INTEGER
			)`,
			insert:    `INSERT INTO cdks VALUES('VOUCHER-CODE', 456, 888, 1700864000, 1700000000, 1, 30, NULL, NULL, 1700000200)`,
			wantGrant: 456,
			wantDays:  30,
			wantProxy: true,
		},
		{
			name: "v3.1.2",
			ddl: `CREATE TABLE cdks (
				code TEXT PRIMARY KEY, remaining_bytes INTEGER NOT NULL, used_bytes INTEGER NOT NULL DEFAULT 0,
				expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL, allow_proxy INTEGER NOT NULL DEFAULT 1,
				duration_days INTEGER NOT NULL DEFAULT 30, redeemed_by_user_id TEXT, redeemed_at INTEGER, revoked_at INTEGER
			)`,
			insert:    `INSERT INTO cdks VALUES('V312-CODE', 123, 999, 1700864000, 1700000000, 0, 45, 'usr_old', 1700000100, NULL)`,
			wantGrant: 123,
			wantDays:  45,
			wantProxy: false,
			wantUser:  "usr_old",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openRawSQLite(t)
			if _, err := db.Exec(tt.ddl); err != nil {
				t.Fatalf("create historical table: %v", err)
			}
			if _, err := db.Exec(tt.insert); err != nil {
				t.Fatalf("insert historical credential: %v", err)
			}

			if err := migrate(db); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			assertPureCDKColumns(t, db)

			var got CDK
			var allow int
			if err := db.QueryRow(
				`SELECT code, grant_bytes, duration_days, allow_proxy, created_at,
				        COALESCE(redeemed_by_user_id, ''), COALESCE(redeemed_at, 0), COALESCE(revoked_at, 0)
				 FROM cdks`,
			).Scan(&got.Code, &got.GrantBytes, &got.DurationDays, &allow, &got.CreatedAt, &got.RedeemedByUserID, &got.RedeemedAt, &got.RevokedAt); err != nil {
				t.Fatalf("scan migrated credential: %v", err)
			}
			got.AllowProxy = allow != 0
			if got.GrantBytes != tt.wantGrant || got.DurationDays != tt.wantDays || got.AllowProxy != tt.wantProxy || got.RedeemedByUserID != tt.wantUser {
				t.Fatalf("migrated credential = %+v", got)
			}
			if err := migrate(db); err != nil {
				t.Fatalf("idempotent migrate: %v", err)
			}
			assertPureCDKColumns(t, db)
		})
	}
}

func TestMigrateCDKsToCredentialsRollsBackOnCopyFailure(t *testing.T) {
	db := openRawSQLite(t)
	if _, err := db.Exec(`CREATE TABLE cdks (
		code TEXT, remaining_bytes INTEGER NOT NULL, used_bytes INTEGER NOT NULL,
		expires_at INTEGER NOT NULL, created_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create malformed historical table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cdks VALUES(NULL, 10, 0, 1700864000, 1700000000)`); err != nil {
		t.Fatalf("insert malformed historical credential: %v", err)
	}

	if err := migrateCDKsToCredentials(db); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	if has, err := columnExists(db, "cdks", "remaining_bytes"); err != nil || !has {
		t.Fatalf("historical table was not restored: has=%v err=%v", has, err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cdks WHERE code IS NULL`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("historical data changed after rollback: rows=%d err=%v", rows, err)
	}
}

func openRawSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func assertPureCDKColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(cdks)`)
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	defer rows.Close()
	type columnDefinition struct {
		name       string
		columnType string
		notNull    int
		defaultVal string
		primaryKey int
	}
	var columns []columnDefinition
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		columns = append(columns, columnDefinition{
			name:       name,
			columnType: columnType,
			notNull:    notNull,
			defaultVal: defaultValue.String,
			primaryKey: pk,
		})
	}
	want := []columnDefinition{
		{name: "code", columnType: "TEXT", notNull: 1, primaryKey: 1},
		{name: "grant_bytes", columnType: "INTEGER", notNull: 1},
		{name: "duration_days", columnType: "INTEGER", notNull: 1},
		{name: "allow_proxy", columnType: "INTEGER", notNull: 1, defaultVal: "1"},
		{name: "created_at", columnType: "INTEGER", notNull: 1},
		{name: "redeemed_by_user_id", columnType: "TEXT"},
		{name: "redeemed_at", columnType: "INTEGER"},
		{name: "revoked_at", columnType: "INTEGER"},
	}
	if !reflect.DeepEqual(columns, want) {
		t.Fatalf("cdks columns = %v, want %v", columns, want)
	}
}
