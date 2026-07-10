package app

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestProxyTempCleanupStoreEncryptsAndMigratesFileIDs(t *testing.T) {
	db, err := openDatabase(t.TempDir() + "/cleanup.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x39}, secretKeySize))
	store := newProxyTempCleanupStore(db, cipher)
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)

	if err := store.record("job-encrypted", "account-1", []string{"private-file-id"}, now, now); err != nil {
		t.Fatalf("record encrypted cleanup: %v", err)
	}
	assertCleanupPayloadEncrypted(t, db, "job-encrypted", "private-file-id")

	if _, err := db.Exec(`INSERT INTO proxy_temp_cleanups
		(id, job_id, account_id, file_ids_json, cleanup_after, attempts, created_at)
		VALUES(?,?,?,?,?,?,?)`, "legacy-cleanup", "job-legacy", "account-1",
		`["legacy-private-id"]`, now.Unix(), 0, now.Unix()); err != nil {
		t.Fatalf("insert legacy cleanup: %v", err)
	}
	if err := store.RotateSecrets(); err != nil {
		t.Fatalf("rotate cleanup secrets: %v", err)
	}
	assertCleanupPayloadEncrypted(t, db, "job-legacy", "legacy-private-id")

	records, err := store.due(now, 10)
	if err != nil {
		t.Fatalf("read due cleanups: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("due cleanup count = %d, want 2", len(records))
	}
}

func TestProxyTempCleanupStoreSeparatesAccountsForOneJob(t *testing.T) {
	db, err := openDatabase(t.TempDir() + "/cleanup.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x41}, secretKeySize))
	store := newProxyTempCleanupStore(db, cipher)
	now := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)

	if err := store.record("shared-job", "account-a", []string{"file-a"}, now, now); err != nil {
		t.Fatalf("record account-a: %v", err)
	}
	if err := store.record("shared-job", "account-b", []string{"file-b"}, now, now); err != nil {
		t.Fatalf("record account-b: %v", err)
	}
	records, err := store.due(now, 10)
	if err != nil {
		t.Fatalf("read due records: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	want := map[string]string{
		"account-a": cleanupRecordID("shared-job", "account-a"),
		"account-b": cleanupRecordID("shared-job", "account-b"),
	}
	for _, record := range records {
		if record.ID != want[record.AccountID] {
			t.Fatalf("record ID for %s = %q, want %q", record.AccountID, record.ID, want[record.AccountID])
		}
	}
}

func TestProxyTempCleanupRotateMergesLegacyRowsByJobAndAccount(t *testing.T) {
	db, err := openDatabase(t.TempDir() + "/cleanup.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x52}, secretKeySize))
	store := newProxyTempCleanupStore(db, cipher)
	now := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)

	encryptedDuplicate, err := cipher.Encrypt(proxyTempCleanupPurpose, "legacy-b", []byte(`["file-b","shared"]`))
	if err != nil {
		t.Fatalf("encrypt legacy duplicate: %v", err)
	}
	legacyRows := []struct {
		id, jobID, accountID, payload, lastError string
		cleanupAfter, createdAt                  time.Time
		attempts                                 int
	}{
		{"legacy-a", "merge-job", "account-a", `["file-a","shared"]`, "first", now.Add(time.Hour), now, 1},
		{"legacy-b", "merge-job", "account-a", encryptedDuplicate, "latest", now.Add(2 * time.Hour), now.Add(-time.Hour), 4},
		{"legacy-c", "merge-job", "account-b", `["file-c"]`, "", now.Add(30 * time.Minute), now.Add(-2 * time.Hour), 2},
	}
	for _, row := range legacyRows {
		if _, err := db.Exec(`INSERT INTO proxy_temp_cleanups
			(id,job_id,account_id,file_ids_json,cleanup_after,attempts,last_error,created_at)
			VALUES(?,?,?,?,?,?,?,?)`, row.id, row.jobID, row.accountID, row.payload,
			row.cleanupAfter.Unix(), row.attempts, row.lastError, row.createdAt.Unix()); err != nil {
			t.Fatalf("insert %s: %v", row.id, err)
		}
	}

	if err := store.RotateSecrets(); err != nil {
		t.Fatalf("rotate cleanup secrets: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM proxy_temp_cleanups`).Scan(&count); err != nil {
		t.Fatalf("count migrated rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("migrated row count = %d, want 2", count)
	}

	id := cleanupRecordID("merge-job", "account-a")
	var payload, lastError string
	var cleanupUnix, createdUnix int64
	var attempts int
	if err := db.QueryRow(`SELECT file_ids_json,cleanup_after,attempts,
		COALESCE(last_error,''),created_at FROM proxy_temp_cleanups WHERE id=?`, id).
		Scan(&payload, &cleanupUnix, &attempts, &lastError, &createdUnix); err != nil {
		t.Fatalf("read merged row: %v", err)
	}
	ids, err := store.decodeFileIDs(id, payload)
	if err != nil {
		t.Fatalf("decode merged IDs: %v", err)
	}
	if !sameCleanupIDs(ids, []string{"file-a", "file-b", "shared"}) {
		t.Fatalf("merged IDs = %v", ids)
	}
	if cleanupUnix != now.Add(2*time.Hour).Unix() || createdUnix != now.Add(-time.Hour).Unix() || attempts != 4 || lastError != "latest" {
		t.Fatalf("merged metadata = cleanup %d, created %d, attempts %d, error %q", cleanupUnix, createdUnix, attempts, lastError)
	}
	if !strings.HasPrefix(payload, secretEnvelopeVersion+".") || strings.Contains(payload, "file-a") {
		t.Fatalf("merged payload is not encrypted: %q", payload)
	}
	var legacyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM proxy_temp_cleanups WHERE id IN ('legacy-a','legacy-b','legacy-c')`).Scan(&legacyCount); err != nil {
		t.Fatalf("count legacy IDs: %v", err)
	}
	if legacyCount != 0 {
		t.Fatalf("legacy row count = %d, want 0", legacyCount)
	}
}

func TestProxyTempCleanupStorePartiallyRemovesFileIDs(t *testing.T) {
	db, err := openDatabase(t.TempDir() + "/cleanup.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x63}, secretKeySize))
	store := newProxyTempCleanupStore(db, cipher)
	now := time.Date(2026, 7, 10, 11, 0, 0, 0, time.UTC)
	if err := store.record("partial-job", "account-a", []string{"file-a", "file-b", "file-c"}, now, now.Add(-time.Hour)); err != nil {
		t.Fatalf("record cleanup: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin rollback transaction: %v", err)
	}
	if err := store.removeIDsByJobAccountTx(tx, "partial-job", "account-a", []string{"file-a"}); err != nil {
		t.Fatalf("remove before rollback: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback removal: %v", err)
	}
	assertCleanupIDs(t, store, now, []string{"file-a", "file-b", "file-c"})

	tx, err = db.Begin()
	if err != nil {
		t.Fatalf("begin removal transaction: %v", err)
	}
	if err := store.removeIDsByJobAccountTx(tx, "partial-job", "account-a", []string{"file-a", "missing"}); err != nil {
		t.Fatalf("remove by job/account: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit removal: %v", err)
	}
	assertCleanupIDs(t, store, now, []string{"file-b", "file-c"})

	id := cleanupRecordID("partial-job", "account-a")
	if err := store.removeRecordIDs(id, []string{"file-b"}); err != nil {
		t.Fatalf("remove by record: %v", err)
	}
	assertCleanupIDs(t, store, now, []string{"file-c"})
	if err := store.removeRecordIDs(id, []string{"file-c"}); err != nil {
		t.Fatalf("remove final ID: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM proxy_temp_cleanups WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatalf("count final row: %v", err)
	}
	if count != 0 {
		t.Fatalf("remaining row count = %d, want 0", count)
	}
}

func assertCleanupPayloadEncrypted(t *testing.T, db *sql.DB, jobID, plaintext string) {
	t.Helper()
	var stored string
	if err := db.QueryRow(`SELECT file_ids_json FROM proxy_temp_cleanups WHERE job_id=?`, jobID).Scan(&stored); err != nil {
		t.Fatalf("read cleanup payload: %v", err)
	}
	if !strings.HasPrefix(stored, secretEnvelopeVersion+".") || strings.Contains(stored, plaintext) {
		t.Fatalf("cleanup payload was not encrypted: %q", stored)
	}
}

func assertCleanupIDs(t *testing.T, store *proxyTempCleanupStore, now time.Time, want []string) {
	t.Helper()
	records, err := store.due(now, 10)
	if err != nil {
		t.Fatalf("read cleanup IDs: %v", err)
	}
	if len(records) != 1 || !sameCleanupIDs(records[0].FileIDs, want) {
		t.Fatalf("cleanup records = %#v, want IDs %v", records, want)
	}
	var payload string
	if err := store.db.QueryRow(`SELECT file_ids_json FROM proxy_temp_cleanups WHERE id=?`, records[0].ID).Scan(&payload); err != nil {
		t.Fatalf("read cleanup payload: %v", err)
	}
	if !strings.HasPrefix(payload, secretEnvelopeVersion+".") {
		t.Fatalf("cleanup payload is not encrypted: %q", payload)
	}
}

func sameCleanupIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]struct{}, len(got))
	for _, id := range got {
		seen[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := seen[id]; !ok {
			return false
		}
	}
	return true
}
