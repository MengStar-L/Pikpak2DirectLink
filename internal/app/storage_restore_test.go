package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRestoreDatabaseRoundTripCreatesSafetyBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "current.db")
	backup := filepath.Join(dir, "verified-backup.db")
	writeRestoreTestDatabase(t, target, "current")
	writeRestoreTestDatabase(t, backup, "backup")

	result, err := RestoreDatabase(context.Background(), target, backup)
	if err != nil {
		t.Fatalf("RestoreDatabase: %v", err)
	}
	if got := readRestoreTestDatabase(t, target); got != "backup" {
		t.Fatalf("restored value = %q, want backup", got)
	}
	if got := readRestoreTestDatabase(t, backup); got != "backup" {
		t.Fatalf("source backup changed: %q", got)
	}
	if len(result.RestoredPaths) != 1 || !samePath(result.RestoredPaths[0], target) {
		t.Fatalf("restored paths = %#v", result.RestoredPaths)
	}

	safety, err := loadMigrationBackup(result.SafetyBackupPath)
	if err != nil {
		t.Fatalf("load safety backup: %v", err)
	}
	if err := verifyMigrationBackup(safety); err != nil {
		t.Fatalf("verify safety backup: %v", err)
	}
	if len(safety.Files) != 1 || safety.Files[0].Kind != "pre_restore_database" {
		t.Fatalf("safety backup files = %#v", safety.Files)
	}
	safetyDB := filepath.Join(safety.Path, filepath.FromSlash(safety.Files[0].RelativePath))
	if got := readRestoreTestDatabase(t, safetyDB); got != "current" {
		t.Fatalf("safety database value = %q, want current", got)
	}
}

func TestRestoreDatabaseRejectsCorruptBackupWithoutChangingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "current.db")
	backup := filepath.Join(dir, "corrupt.db")
	writeRestoreTestDatabase(t, target, "current")
	if err := os.WriteFile(backup, []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RestoreDatabase(context.Background(), target, backup); err == nil {
		t.Fatal("RestoreDatabase accepted corrupt backup")
	}
	if got := readRestoreTestDatabase(t, target); got != "current" {
		t.Fatalf("target changed after corrupt restore: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, restoreSafetyDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("safety directory created before backup validation: %v", err)
	}
}

func TestRestoreDatabaseRejectsBackupThatIsAnotherRestoreTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "current.db")
	backup := target + "-wal"
	writeRestoreTestDatabase(t, target, "current")
	writeRestoreTestDatabase(t, backup, "backup")

	if _, err := RestoreDatabase(context.Background(), target, backup); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("cross-operation source conflict error = %v", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup source was removed: %v", err)
	}
	if got := readRestoreTestDatabase(t, target); got != "current" {
		t.Fatalf("target changed after rejected restore: %q", got)
	}
}

func TestRestoreDatabaseRefusesLockedTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "current.db")
	backup := filepath.Join(dir, "backup.db")
	writeRestoreTestDatabase(t, target, "current")
	writeRestoreTestDatabase(t, backup, "backup")

	lock, err := acquireStorageFileLock(target)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	_, err = RestoreDatabase(context.Background(), target, backup)
	if err == nil || !errors.Is(err, errStorageInUse) {
		t.Fatalf("locked restore error = %v", err)
	}
	if got := readRestoreTestDatabase(t, target); got != "current" {
		t.Fatalf("locked target changed: %q", got)
	}
}

func TestRestoreDatabaseRefusesActiveSQLiteWriteTransaction(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "current.db")
	backup := filepath.Join(dir, "backup.db")
	writeRestoreTestDatabase(t, target, "current")
	writeRestoreTestDatabase(t, backup, "backup")

	db := openRestoreTestDatabase(t, target)
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")

	if _, err := RestoreDatabase(context.Background(), target, backup); !errors.Is(err, errStorageInUse) {
		t.Fatalf("active SQLite transaction error = %v", err)
	}
}

func TestRestoreDatabaseReplacesCorruptCurrentDatabase(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "current.db")
	backup := filepath.Join(dir, "backup.db")
	writeRestoreTestDatabase(t, backup, "backup")
	if err := os.WriteFile(target, []byte("corrupt current database"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RestoreDatabase(context.Background(), target, backup); err != nil {
		t.Fatalf("RestoreDatabase: %v", err)
	}
	if got := readRestoreTestDatabase(t, target); got != "backup" {
		t.Fatalf("restored value = %q", got)
	}
}

func TestRestoreDatabaseRejectsEmptyAndForeignSQLite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "current.db")
	writeRestoreTestDatabase(t, target, "current")

	empty := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreDatabase(context.Background(), target, empty); err == nil {
		t.Fatal("RestoreDatabase accepted an empty backup")
	}

	foreign := filepath.Join(dir, "foreign.db")
	db := openRestoreTestDatabase(t, foreign)
	if _, err := db.Exec(`CREATE TABLE foreign_marker(value TEXT)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreDatabase(context.Background(), target, foreign); err == nil || !strings.Contains(err.Error(), "Pikpak2DirectLink") {
		t.Fatalf("foreign database error = %v", err)
	}
	if got := readRestoreTestDatabase(t, target); got != "current" {
		t.Fatalf("target changed after rejected backups: %q", got)
	}
}

func TestRestoreDatabaseRejectsBackupWithSidecars(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "current.db")
	backup := filepath.Join(dir, "backup.db")
	writeRestoreTestDatabase(t, target, "current")
	writeRestoreTestDatabase(t, backup, "backup")
	if err := os.WriteFile(backup+"-wal", []byte("uncheckpointed data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreDatabase(context.Background(), target, backup); err == nil || !strings.Contains(err.Error(), "not standalone") {
		t.Fatalf("sidecar backup error = %v", err)
	}
	if got := readRestoreTestDatabase(t, target); got != "current" {
		t.Fatalf("target changed after sidecar rejection: %q", got)
	}
}

func TestCreateRestoreSafetyBackupCopiesDatabaseSidecars(t *testing.T) {
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "current.db")
	fixtures := map[string]string{
		databasePath:          "database bytes",
		databasePath + "-wal": "wal bytes",
		databasePath + "-shm": "shm bytes",
	}
	for path, content := range fixtures {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	operations := []restoreOperation{
		{kind: "database", target: databasePath},
		{kind: "database_wal", target: databasePath + "-wal"},
		{kind: "database_shm", target: databasePath + "-shm"},
	}
	path, err := createRestoreSafetyBackup(databasePath, operations, "0123456789abcdef01234567", time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	backup, err := loadMigrationBackup(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMigrationBackup(backup); err != nil {
		t.Fatal(err)
	}
	if len(backup.Files) != 3 {
		t.Fatalf("safety file count = %d, want 3", len(backup.Files))
	}
	for _, file := range backup.Files {
		data, err := os.ReadFile(filepath.Join(path, filepath.FromSlash(file.RelativePath)))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(data), fixtures[file.OriginalPath]; got != want {
			t.Fatalf("safety copy for %s = %q, want %q", file.OriginalPath, got, want)
		}
	}
}

func TestRestoreMigrationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := restoreTestConfig(dir)
	writeRestoreTestDatabase(t, cfg.DBFile, "legacy database")
	fixtures := map[string]string{
		cfg.AuthFile:     "legacy auth",
		cfg.AccountsFile: "legacy accounts",
		cfg.SessionFile:  "legacy bootstrap session",
		filepath.Join(cfg.AccountSessionDir, "a.json"): "legacy account session",
	}
	for path, content := range fixtures {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backup, err := prepareLegacyMigrationBackup(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(cfg.DBFile); err != nil {
		t.Fatal(err)
	}
	writeRestoreTestDatabase(t, cfg.DBFile, "current database")
	for path := range fixtures {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(cfg.AuthFile, []byte("current auth"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := RestoreMigration(context.Background(), cfg, backup.Path)
	if err != nil {
		t.Fatalf("RestoreMigration: %v", err)
	}
	if got := readRestoreTestDatabase(t, cfg.DBFile); got != "legacy database" {
		t.Fatalf("restored database = %q", got)
	}
	for path, want := range fixtures {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read restored %s: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("restored %s = %q, want %q", path, data, want)
		}
	}
	if result.SafetyBackupPath == "" {
		t.Fatal("migration restore did not retain current data safety backup")
	}
	safety, err := loadMigrationBackup(result.SafetyBackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyMigrationBackup(safety); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreMigrationWithoutLegacyDatabaseRemovesCurrentDatabase(t *testing.T) {
	dir := t.TempDir()
	cfg := restoreTestConfig(dir)
	if err := os.WriteFile(cfg.AuthFile, []byte("legacy auth"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := prepareLegacyMigrationBackup(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.AuthFile); err != nil {
		t.Fatal(err)
	}
	writeRestoreTestDatabase(t, cfg.DBFile, "current database")

	if _, err := RestoreMigration(context.Background(), cfg, backup.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.DBFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current database still exists after restoring a database-free migration: %v", err)
	}
	data, err := os.ReadFile(cfg.AuthFile)
	if err != nil || string(data) != "legacy auth" {
		t.Fatalf("restored auth = %q, err = %v", data, err)
	}
}

func TestRestoreMigrationRemovesFilesAbsentFromSnapshot(t *testing.T) {
	dir := t.TempDir()
	cfg := restoreTestConfig(dir)
	if err := os.WriteFile(cfg.AuthFile, []byte("legacy auth"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := prepareLegacyMigrationBackup(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{cfg.AccountsFile, cfg.SessionFile, filepath.Join(cfg.AccountSessionDir, "new.json")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("post-snapshot secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRestoreTestDatabase(t, cfg.DBFile, "post-snapshot database")

	if _, err := RestoreMigration(context.Background(), cfg, backup.Path); err != nil {
		t.Fatalf("RestoreMigration: %v", err)
	}
	for _, path := range []string{cfg.AccountsFile, cfg.SessionFile, filepath.Join(cfg.AccountSessionDir, "new.json"), cfg.DBFile} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("post-snapshot path still exists %s: %v", path, err)
		}
	}
}

func TestRestoreMigrationRejectsLinkedSessionTarget(t *testing.T) {
	dir := t.TempDir()
	cfg := restoreTestConfig(dir)
	legacySession := filepath.Join(cfg.AccountSessionDir, "a.json")
	if err := os.MkdirAll(cfg.AccountSessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacySession, []byte("legacy session"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := prepareLegacyMigrationBackup(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(dir, "outside")
	link := filepath.Join(cfg.AccountSessionDir, "linked")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	createRestoreTestDirectoryLink(t, link, outside)
	victim := filepath.Join(outside, "victim.json")
	if err := os.WriteFile(victim, []byte("outside-current"), 0o600); err != nil {
		t.Fatal(err)
	}
	for index := range backup.Files {
		if backup.Files[index].Kind == "account_session" {
			backup.Files[index].OriginalPath = filepath.Join(link, "victim.json")
		}
	}
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup.Path, migrationManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RestoreMigration(context.Background(), cfg, backup.Path); err == nil {
		t.Fatalf("linked target restore error = %v", err)
	}
	if data, err := os.ReadFile(victim); err != nil || string(data) != "outside-current" {
		t.Fatalf("outside victim changed to %q, err=%v", data, err)
	}
}

func TestStageRestoreRejectsSourceChangedAfterVerification(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	operations := []restoreOperation{{
		kind: "fixture", source: source, target: target,
		expectedSize: 3, expectedSHA256: strings.Repeat("0", 64),
	}}
	if err := stageRestoreOperations(context.Background(), operations, "restore-id"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed source error = %v", err)
	}
	removeStagedRestoreFiles(operations)
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target published after source change: %v", err)
	}
}

func TestRestoreContainmentAllowsLinkedConfiguredRoot(t *testing.T) {
	dir := t.TempDir()
	physicalRoot := filepath.Join(dir, "physical")
	configuredRoot := filepath.Join(dir, "configured")
	if err := os.MkdirAll(physicalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	createRestoreTestDirectoryLink(t, configuredRoot, physicalRoot)
	target := filepath.Join(configuredRoot, "session.json")
	if err := validateRestoreContainment(configuredRoot, target); err != nil {
		t.Fatalf("linked configured root was rejected: %v", err)
	}
}

func TestStorageFileLockExcludesConcurrentRestore(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "pikpak.db")
	first, err := acquireStorageFileLock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireStorageFileLock(databasePath); !errors.Is(err, errStorageInUse) {
		t.Fatalf("second lock error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireStorageFileLock(databasePath)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInterruptedRestoreJournalRollsBackOrFinishesCommit(t *testing.T) {
	const restoreID = "0123456789abcdef01234567"
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "commit"}[committed], func(t *testing.T) {
			dir := t.TempDir()
			databasePath := filepath.Join(dir, "pikpak.db")
			target := databasePath
			staged := filepath.Join(dir, ".pikpak.db.restore-new-"+restoreID)
			walTarget := databasePath + "-wal"
			walStaged := filepath.Join(dir, ".pikpak.db-wal.restore-new-"+restoreID)
			if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(staged, []byte("restored"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(walStaged, []byte("restored wal"), 0o600); err != nil {
				t.Fatal(err)
			}
			operations := []restoreOperation{
				{kind: "database", target: target, staged: staged},
				{kind: "database_wal", target: walTarget, staged: walStaged},
			}
			journalPath, err := prepareRestoreJournal(databasePath, operations, restoreID, "safety", restoreJournalModeDatabase)
			if err != nil {
				t.Fatal(err)
			}
			for _, operation := range operations {
				if operation.hadOriginal {
					if err := os.Rename(operation.target, operation.rollback); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Rename(operation.staged, operation.target); err != nil {
					t.Fatal(err)
				}
			}
			if committed {
				if err := markRestoreJournalCommitted(journalPath, restoreID); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverInterruptedStorageRestore(Config{DBFile: databasePath}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			want := "original"
			if committed {
				want = "restored"
			}
			if string(data) != want {
				t.Fatalf("recovered target = %q, want %q", data, want)
			}
			_, walErr := os.Stat(walTarget)
			if committed && walErr != nil {
				t.Fatalf("committed new WAL was removed: %v", walErr)
			}
			if !committed && !errors.Is(walErr, os.ErrNotExist) {
				t.Fatalf("uncommitted new WAL remains: %v", walErr)
			}
			if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journal remains after recovery: %v", err)
			}
		})
	}
}

func TestRestoreMigrationRejectsManifestTargetOutsideConfiguredLayout(t *testing.T) {
	dir := t.TempDir()
	cfg := restoreTestConfig(dir)
	if err := os.WriteFile(cfg.AuthFile, []byte("legacy auth"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := prepareLegacyMigrationBackup(cfg, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	backup.Files[0].OriginalPath = filepath.Join(dir, "outside", "auth.json")
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup.Path, migrationManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RestoreMigration(context.Background(), cfg, backup.Path); err == nil || !strings.Contains(err.Error(), "configured path") {
		t.Fatalf("unexpected manifest target error: %v", err)
	}
}

func restoreTestConfig(dir string) Config {
	return Config{
		DBFile:            filepath.Join(dir, "pikpak.db"),
		AuthFile:          filepath.Join(dir, "auth.json"),
		AccountsFile:      filepath.Join(dir, "accounts.json"),
		SessionFile:       filepath.Join(dir, "bootstrap-session.json"),
		AccountSessionDir: filepath.Join(dir, "accounts"),
	}
}

func createRestoreTestDirectoryLink(t *testing.T, link, target string) {
	t.Helper()
	var err error
	if runtime.GOOS == "windows" {
		err = exec.Command("cmd", "/c", "mklink", "/J", link, target).Run()
	} else {
		err = os.Symlink(target, link)
	}
	if err != nil {
		t.Skipf("directory links are unavailable: %v", err)
	}
}

func writeRestoreTestDatabase(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE restore_marker(value TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatalf("create restore marker: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO restore_marker(value) VALUES(?)`, value); err != nil {
		db.Close()
		t.Fatalf("insert restore marker: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func readRestoreTestDatabase(t *testing.T, path string) string {
	t.Helper()
	db := openRestoreTestDatabase(t, path)
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM restore_marker`).Scan(&value); err != nil {
		t.Fatalf("read restore marker from %s: %v", path, err)
	}
	return value
}

func openRestoreTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}
