package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupManagerRunNowCreatesVerifiedSnapshotWithWALContent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "source # database.db")
	db, err := openDatabase(dbPath)
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES('snapshot-key', 'from-wal')`); err != nil {
		t.Fatalf("seed source database: %v", err)
	}
	backupDir := filepath.Join(t.TempDir(), "backups with spaces")
	manager := newBackupManager(db, dbPath, backupDir, time.Hour, 7)
	manager.now = func() time.Time { return time.Date(2026, 7, 10, 12, 34, 56, 0, time.UTC) }

	run, err := manager.RunNow(context.Background())
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if run.Status != backupStatusSuccess || run.Path == "" || run.SizeBytes == 0 {
		t.Fatalf("unexpected successful run: %+v", run)
	}
	if ok, err := manager.isManagedBackupPath(run.Path); err != nil || !ok {
		t.Fatalf("published path managed = %v, err = %v", ok, err)
	}
	data, err := os.ReadFile(run.Path)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != run.SHA256 {
		t.Fatalf("SHA256 = %q, want %q", run.SHA256, got)
	}
	if int64(len(data)) != run.SizeBytes {
		t.Fatalf("size = %d, want %d", run.SizeBytes, len(data))
	}

	backupDB, err := openReadOnlyTestDatabase(run.Path)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupDB.Close()
	var value string
	if err := backupDB.QueryRow(`SELECT value FROM settings WHERE key = 'snapshot-key'`).Scan(&value); err != nil {
		t.Fatalf("read snapshot content: %v", err)
	}
	if value != "from-wal" {
		t.Fatalf("snapshot value = %q, want from-wal", value)
	}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Running || status.LastRun == nil || status.LastSuccess == nil {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.LastRun.ID != run.ID || status.LastSuccess.ID != run.ID {
		t.Fatalf("status does not reference run %q: %+v", run.ID, status)
	}
}

func TestBackupManagerPrunesOnlyOldManagedBackupsAfterSuccess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "source.db")
	db, err := openDatabase(dbPath)
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	backupDir := t.TempDir()
	manager := newBackupManager(db, dbPath, backupDir, time.Hour, 2)
	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	var runs []backupRun
	for i := range 4 {
		if _, err := db.Exec(`INSERT OR REPLACE INTO settings(key, value) VALUES('version', ?)`, i); err != nil {
			t.Fatalf("seed version %d: %v", i, err)
		}
		run, err := manager.RunNow(context.Background())
		if err != nil {
			t.Fatalf("RunNow %d: %v", i, err)
		}
		runs = append(runs, run)
		now = now.Add(time.Minute)
	}

	for _, run := range runs[:2] {
		if _, err := os.Stat(run.Path); !os.IsNotExist(err) {
			t.Errorf("old backup %q still exists, err = %v", run.Path, err)
		}
	}
	for _, run := range runs[2:] {
		if _, err := os.Stat(run.Path); err != nil {
			t.Errorf("retained backup %q: %v", run.Path, err)
		}
	}

	outside := filepath.Join(t.TempDir(), "source-backup-20000101T000000.000000000Z-fake.db")
	if err := os.WriteFile(outside, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	insideUnexpected := filepath.Join(backupDir, "unrelated.db")
	if err := os.WriteFile(insideUnexpected, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}
	insideLookalike := filepath.Join(backupDir, "source-backup-not-a-real-run.db")
	if err := os.WriteFile(insideLookalike, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write lookalike file: %v", err)
	}
	for i, path := range []string{outside, insideUnexpected, insideLookalike} {
		if _, err := db.Exec(`
			INSERT INTO backup_runs(id, kind, status, path, started_at, completed_at)
			VALUES(?, ?, ?, ?, ?, ?)`,
			"unsafe-"+string(rune('a'+i)), backupKindManual, backupStatusSuccess, path,
			now.Add(time.Hour).Unix(), now.Add(time.Hour).Unix()); err != nil {
			t.Fatalf("seed unsafe path: %v", err)
		}
	}
	if err := manager.pruneSuccessfulBackups(context.Background()); err != nil {
		t.Fatalf("prune with unsafe paths: %v", err)
	}
	for _, path := range []string{outside, insideUnexpected, insideLookalike} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("unsafe path %q was touched: %v", path, err)
		}
	}
	for _, run := range runs[2:] {
		if _, err := os.Stat(run.Path); err != nil {
			t.Errorf("unsafe records displaced retained backup %q: %v", run.Path, err)
		}
	}
}

func TestBackupRetentionUsesInsertionOrderForSameTimestamp(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "source.db")
	db, err := openDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := newBackupManager(db, dbPath, filepath.Join(dir, "backups"), time.Hour, 1)
	fixed := time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC)
	manager.now = func() time.Time { return fixed }

	first, err := manager.RunNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.RunNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("older same-timestamp backup remains: %v", err)
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("newest same-timestamp backup was pruned: %v", err)
	}
}

func TestBackupRetentionDoesNotCountMissingSnapshots(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "source.db")
	db, err := openDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := newBackupManager(db, dbPath, filepath.Join(dir, "backups"), time.Hour, 2)
	now := time.Date(2026, 7, 10, 1, 2, 3, 0, time.UTC)
	manager.now = func() time.Time { return now }

	first, err := manager.RunNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	second, err := manager.RunNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(second.Path); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	third, err := manager.RunNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{first.Path, third.Path} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("valid retained snapshot %s: %v", path, err)
		}
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM backup_runs WHERE id=?`, second.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != backupStatusFailed {
		t.Fatalf("missing snapshot status = %q", status)
	}
}

func TestBackupManagerReconcilesInterruptedRuns(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := newBackupManager(db, "source.db", t.TempDir(), time.Hour, 2)
	if _, err := db.Exec(`INSERT INTO backup_runs(id,kind,status,started_at)
		VALUES('interrupted',?,?,?)`, backupKindScheduled, backupStatusRunning, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReconcileInterrupted(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || status.LastRun == nil || status.LastRun.Status != backupStatusFailed {
		t.Fatalf("reconciled status = %+v", status)
	}
}

func TestBackupManagerRunIfDueUsesLatestSuccess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "source.db")
	db, err := openDatabase(dbPath)
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	interval := 24 * time.Hour
	manager := newBackupManager(db, dbPath, t.TempDir(), interval, 7)
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	first, ran, err := manager.RunIfDue(context.Background())
	if err != nil || !ran {
		t.Fatalf("first RunIfDue: run=%+v ran=%v err=%v", first, ran, err)
	}
	now = now.Add(interval - time.Second)
	if run, ran, err := manager.RunIfDue(context.Background()); err != nil || ran {
		t.Fatalf("early RunIfDue: run=%+v ran=%v err=%v", run, ran, err)
	}
	now = now.Add(time.Second)
	second, ran, err := manager.RunIfDue(context.Background())
	if err != nil || !ran || second.ID == first.ID {
		t.Fatalf("due RunIfDue: run=%+v ran=%v err=%v", second, ran, err)
	}
}

func TestBackupManagerFailureIsRecordedAndKeepsPreviousSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "source.db")
	db, err := openDatabase(dbPath)
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	manager := newBackupManager(db, dbPath, t.TempDir(), time.Hour, 1)
	manager.now = func() time.Time { return time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC) }
	good, err := manager.RunNow(context.Background())
	if err != nil {
		t.Fatalf("create good backup: %v", err)
	}

	notDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("file"), 0o600); err != nil {
		t.Fatalf("create invalid backup directory: %v", err)
	}
	manager.dir = notDirectory
	manager.now = func() time.Time { return time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC) }
	failed, err := manager.RunNow(context.Background())
	if err == nil || failed.Status != backupStatusFailed || failed.Error == "" {
		t.Fatalf("failed RunNow = %+v, err = %v", failed, err)
	}
	if _, err := os.Stat(good.Path); err != nil {
		t.Fatalf("previous good snapshot was removed: %v", err)
	}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.LastRun == nil || status.LastRun.ID != failed.ID || status.LastRun.Status != backupStatusFailed {
		t.Fatalf("latest failure not reflected in status: %+v", status)
	}
	if status.LastSuccess == nil || status.LastSuccess.ID != good.ID {
		t.Fatalf("last success was lost: %+v", status)
	}
	if want := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC); !status.NextRunAt.Equal(want) {
		t.Fatalf("next run after failure = %v, want %v", status.NextRunAt, want)
	}
}

func TestCheckSQLiteBackupRejectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database"), 0o600); err != nil {
		t.Fatalf("write corrupt database: %v", err)
	}
	err := checkSQLiteBackup(context.Background(), path)
	if err == nil {
		t.Fatal("checkSQLiteBackup accepted corrupt input")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "database") {
		t.Fatalf("unexpected corruption error: %v", err)
	}
}

func openReadOnlyTestDatabase(path string) (*sql.DB, error) {
	dsn, err := sqliteReadOnlyDSN(path)
	if err != nil {
		return nil, err
	}
	return sql.Open("sqlite", dsn)
}
