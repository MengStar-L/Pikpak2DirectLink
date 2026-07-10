package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	backupKindManual    = "database_manual"
	backupKindScheduled = "database_scheduled"
	backupStatusRunning = "running"
	backupStatusSuccess = "success"
	backupStatusFailed  = "failed"
	backupStatusPruned  = "pruned"

	defaultBackupInterval  = 24 * time.Hour
	defaultBackupRetention = 7
	backupRecordTimeout    = 5 * time.Second
	backupTimestampLayout  = "20060102T150405.000000000Z"
)

type backupRun struct {
	ID          string
	Kind        string
	Status      string
	Path        string
	SizeBytes   int64
	SHA256      string
	Error       string
	StartedAt   time.Time
	CompletedAt *time.Time
}

type backupStatus struct {
	Running     bool
	LastRun     *backupRun
	LastSuccess *backupRun
	NextRunAt   time.Time
}

type backupManager struct {
	db        *sql.DB
	dbPath    string
	dir       string
	interval  time.Duration
	retention int
	now       func() time.Time

	runMu sync.Mutex
}

func newBackupManager(db *sql.DB, dbPath, dir string, interval time.Duration, retention int) *backupManager {
	if interval <= 0 {
		interval = defaultBackupInterval
	}
	if retention <= 0 {
		retention = defaultBackupRetention
	}
	return &backupManager{
		db:        db,
		dbPath:    dbPath,
		dir:       dir,
		interval:  interval,
		retention: retention,
		now:       time.Now,
	}
}

// RunNow creates and verifies a database backup before publishing it.
func (m *backupManager) RunNow(ctx context.Context) (backupRun, error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	return m.runLocked(ctx, backupKindManual)
}

// RunIfDue creates a scheduled backup when no successful snapshot has
// completed within the configured interval. The lock makes the due check and
// the run atomic within the process, so concurrent scheduler ticks cannot start
// duplicate backups.
func (m *backupManager) RunIfDue(ctx context.Context) (backupRun, bool, error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	now := m.currentTime()
	lastCompleted, ok, err := m.lastSuccessfulAt(ctx)
	if err != nil {
		return backupRun{}, false, err
	}
	if ok && now.Before(lastCompleted.Add(m.interval)) {
		return backupRun{}, false, nil
	}
	run, err := m.runLocked(ctx, backupKindScheduled)
	return run, true, err
}

func (m *backupManager) Status(ctx context.Context) (backupStatus, error) {
	lastRun, err := m.latestRun(ctx, false)
	if err != nil {
		return backupStatus{}, err
	}
	lastSuccess, err := m.latestRun(ctx, true)
	if err != nil {
		return backupStatus{}, err
	}

	status := backupStatus{
		LastRun:     lastRun,
		LastSuccess: lastSuccess,
		NextRunAt:   m.currentTime(),
	}
	if lastRun != nil {
		status.Running = lastRun.Status == backupStatusRunning
	}
	if lastSuccess != nil {
		lastCompleted := lastSuccess.StartedAt
		if lastSuccess.CompletedAt != nil {
			lastCompleted = *lastSuccess.CompletedAt
		}
		status.NextRunAt = lastCompleted.Add(m.interval)
	}
	return status, nil
}

func (m *backupManager) ReconcileInterrupted(ctx context.Context) error {
	if m == nil || m.db == nil {
		return nil
	}
	_, err := m.db.ExecContext(ctx, `UPDATE backup_runs
		SET status=?,error='service restarted during backup',completed_at=?
		WHERE status=?`, backupStatusFailed, m.currentTime().Unix(), backupStatusRunning)
	return err
}

func (m *backupManager) runLocked(ctx context.Context, kind string) (backupRun, error) {
	if m.db == nil {
		return backupRun{}, errors.New("backup database is nil")
	}
	if strings.TrimSpace(m.dir) == "" {
		return backupRun{}, errors.New("backup directory is empty")
	}

	id, err := newBackupRunID()
	if err != nil {
		return backupRun{}, fmt.Errorf("generate backup id: %w", err)
	}
	startedAt := m.currentTime()
	run := backupRun{
		ID:        id,
		Kind:      kind,
		Status:    backupStatusRunning,
		StartedAt: startedAt,
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO backup_runs(id, kind, status, started_at)
		VALUES(?, ?, ?, ?)`, run.ID, run.Kind, run.Status, run.StartedAt.Unix()); err != nil {
		return run, fmt.Errorf("record backup start: %w", err)
	}

	fail := func(cause error) (backupRun, error) {
		run.Status = backupStatusFailed
		run.Error = cause.Error()
		completedAt := m.currentTime()
		run.CompletedAt = &completedAt
		if recordErr := m.recordFailure(run); recordErr != nil {
			return run, errors.Join(cause, fmt.Errorf("record backup failure: %w", recordErr))
		}
		return run, cause
	}

	dir, err := filepath.Abs(m.dir)
	if err != nil {
		return fail(fmt.Errorf("resolve backup directory: %w", err))
	}
	if err := durableMkdirAll(dir, 0o700); err != nil {
		return fail(fmt.Errorf("create backup directory: %w", err))
	}

	name := m.backupFilename(startedAt, id)
	finalPath := filepath.Join(dir, name)
	tempPath := filepath.Join(dir, "."+name+".tmp")
	defer os.Remove(tempPath)

	// SQLite treats the bound value as a filename expression. Keeping the path
	// out of SQL quoting is important for spaces and Windows drive letters.
	if _, err := m.db.ExecContext(ctx, `VACUUM INTO ?`, tempPath); err != nil {
		return fail(fmt.Errorf("create SQLite snapshot: %w", err))
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fail(fmt.Errorf("secure SQLite snapshot: %w", err))
	}
	if err := checkApplicationDatabaseBackup(ctx, tempPath); err != nil {
		return fail(fmt.Errorf("verify SQLite snapshot: %w", err))
	}
	if err := syncBackupFile(tempPath); err != nil {
		return fail(fmt.Errorf("sync SQLite snapshot: %w", err))
	}

	digest, size, err := hashBackupFile(tempPath)
	if err != nil {
		return fail(fmt.Errorf("hash SQLite snapshot: %w", err))
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fail(fmt.Errorf("publish SQLite snapshot: %w", err))
	}
	if err := syncDirectory(dir); err != nil {
		_ = os.Remove(finalPath)
		return fail(fmt.Errorf("sync SQLite snapshot directory: %w", err))
	}

	completedAt := m.currentTime()
	run.Status = backupStatusSuccess
	run.Path = finalPath
	run.SizeBytes = size
	run.SHA256 = digest
	run.CompletedAt = &completedAt
	if err := m.recordSuccess(run); err != nil {
		return run, fmt.Errorf("record backup success: %w", err)
	}
	if err := m.pruneSuccessfulBackups(ctx); err != nil {
		return run, fmt.Errorf("prune old backups: %w", err)
	}
	return run, nil
}

func (m *backupManager) recordFailure(run backupRun) error {
	ctx, cancel := context.WithTimeout(context.Background(), backupRecordTimeout)
	defer cancel()
	_, err := m.db.ExecContext(ctx, `
		UPDATE backup_runs
		SET status = ?, error = ?, completed_at = ?
		WHERE id = ?`, run.Status, run.Error, run.CompletedAt.Unix(), run.ID)
	return err
}

func (m *backupManager) recordSuccess(run backupRun) error {
	ctx, cancel := context.WithTimeout(context.Background(), backupRecordTimeout)
	defer cancel()
	_, err := m.db.ExecContext(ctx, `
		UPDATE backup_runs
		SET status = ?, path = ?, size_bytes = ?, sha256 = ?, error = '', completed_at = ?
		WHERE id = ?`, run.Status, run.Path, run.SizeBytes, run.SHA256, run.CompletedAt.Unix(), run.ID)
	return err
}

func (m *backupManager) lastSuccessfulAt(ctx context.Context) (time.Time, bool, error) {
	var unixSeconds int64
	err := m.db.QueryRowContext(ctx, `
		SELECT completed_at FROM backup_runs
		WHERE kind IN (?, ?) AND status = ? AND completed_at IS NOT NULL
		ORDER BY completed_at DESC, started_at DESC, rowid DESC
		LIMIT 1`, backupKindManual, backupKindScheduled, backupStatusSuccess).Scan(&unixSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read latest successful backup time: %w", err)
	}
	return time.Unix(unixSeconds, 0).UTC(), true, nil
}

func (m *backupManager) latestRun(ctx context.Context, successfulOnly bool) (*backupRun, error) {
	query := `
		SELECT id, kind, status, path, size_bytes, sha256, error, started_at, completed_at
		FROM backup_runs
		WHERE kind IN (?, ?)`
	if successfulOnly {
		query += ` AND status = 'success'`
	}
	query += ` ORDER BY started_at DESC, rowid DESC LIMIT 1`

	run, err := scanBackupRun(m.db.QueryRowContext(ctx, query, backupKindManual, backupKindScheduled))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read backup status: %w", err)
	}
	return &run, nil
}

func (m *backupManager) pruneSuccessfulBackups(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id,path,size_bytes,sha256 FROM backup_runs
		WHERE kind IN (?, ?) AND status = ? AND path <> ''
		ORDER BY completed_at DESC, started_at DESC, rowid DESC`,
		backupKindManual, backupKindScheduled, backupStatusSuccess)
	if err != nil {
		return err
	}
	defer rows.Close()

	type candidate struct {
		id, path, sha256 string
		size             int64
	}
	var managed []candidate
	var invalid []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.path, &item.size, &item.sha256); err != nil {
			return err
		}
		isManaged, err := m.isManagedBackupPath(item.path)
		if err != nil {
			return err
		}
		if !isManaged {
			continue
		}
		digest, size, err := hashBackupFile(item.path)
		if err != nil || size != item.size || digest != item.sha256 {
			invalid = append(invalid, item)
			continue
		}
		managed = append(managed, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range invalid {
		if _, err := m.db.ExecContext(ctx, `UPDATE backup_runs
			SET status=?,error='backup file is missing or failed verification'
			WHERE id=? AND status=?`, backupStatusFailed, item.id, backupStatusSuccess); err != nil {
			return err
		}
	}
	if len(managed) <= m.retention {
		return nil
	}

	for _, item := range managed[m.retention:] {
		if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := m.db.ExecContext(ctx, `UPDATE backup_runs
			SET status=?,path='',error='removed by retention policy'
			WHERE id=?`, backupStatusPruned, item.id); err != nil {
			return err
		}
	}
	return syncDirectory(m.dir)
}

func (m *backupManager) isManagedBackupPath(path string) (bool, error) {
	dir, err := filepath.Abs(m.dir)
	if err != nil {
		return false, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(dir, absPath)
	if err != nil || filepath.Dir(rel) != "." || filepath.Base(rel) != rel {
		return false, err
	}
	name := filepath.Base(absPath)
	prefix := m.backupFilePrefix()
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".db") {
		return false, nil
	}
	body := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".db")
	separator := strings.LastIndexByte(body, '-')
	if separator < 0 {
		return false, nil
	}
	if _, err := time.Parse(backupTimestampLayout, body[:separator]); err != nil {
		return false, nil
	}
	id := body[separator+1:]
	if len(id) != 24 {
		return false, nil
	}
	_, err = hex.DecodeString(id)
	return err == nil, nil
}

func (m *backupManager) backupFilename(at time.Time, id string) string {
	return fmt.Sprintf("%s%s-%s.db", m.backupFilePrefix(), at.UTC().Format(backupTimestampLayout), id)
}

func (m *backupManager) backupFilePrefix() string {
	base := filepath.Base(m.dbPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	var clean strings.Builder
	for _, r := range stem {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			clean.WriteRune(r)
		} else {
			clean.WriteByte('_')
		}
	}
	if clean.Len() == 0 {
		clean.WriteString("database")
	}
	return clean.String() + "-backup-"
}

func (m *backupManager) currentTime() time.Time {
	if m.now == nil {
		return time.Now().UTC()
	}
	return m.now().UTC()
}

type backupRowScanner interface {
	Scan(...any) error
}

func scanBackupRun(row backupRowScanner) (backupRun, error) {
	var (
		run         backupRun
		startedAt   int64
		completedAt sql.NullInt64
	)
	err := row.Scan(
		&run.ID, &run.Kind, &run.Status, &run.Path, &run.SizeBytes, &run.SHA256,
		&run.Error, &startedAt, &completedAt,
	)
	if err != nil {
		return backupRun{}, err
	}
	run.StartedAt = time.Unix(startedAt, 0).UTC()
	if completedAt.Valid {
		completed := time.Unix(completedAt.Int64, 0).UTC()
		run.CompletedAt = &completed
	}
	return run, nil
}

func newBackupRunID() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func checkSQLiteBackup(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	header := make([]byte, len("SQLite format 3\x00"))
	_, readErr := io.ReadFull(file, header)
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("read SQLite header: %w", readErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if string(header) != "SQLite format 3\x00" {
		return errors.New("file does not have a SQLite database header")
	}

	dsn, err := sqliteReadOnlyDSN(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("quick_check returned %q", result)
	}
	return nil
}

func checkApplicationDatabaseBackup(ctx context.Context, path string) error {
	if err := checkSQLiteBackup(ctx, path); err != nil {
		return err
	}
	dsn, err := sqliteReadOnlyDSN(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	var name string
	if err := db.QueryRowContext(ctx,
		`SELECT name FROM schema_migrations WHERE version=?`, storageSchemaVersion,
	).Scan(&name); err != nil {
		return fmt.Errorf("database is not a Pikpak2DirectLink application backup: %w", err)
	}
	if name != storageSchemaMigrationName {
		return fmt.Errorf("database has unknown storage schema %q", name)
	}
	return nil
}

func sqliteReadOnlyDSN(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(absPath)
	if filepath.VolumeName(absPath) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := make(url.Values)
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}
	return u.String(), nil
}

func syncBackupFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func hashBackupFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
