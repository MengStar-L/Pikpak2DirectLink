package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const backupScheduleCheckMaximum = time.Hour

type storageBackupRunResponse struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Path        string     `json:"path"`
	SizeBytes   int64      `json:"size_bytes"`
	SHA256      string     `json:"sha256"`
	Error       string     `json:"error"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type storageMigrationResponse struct {
	Status      string     `json:"status"`
	BackupID    string     `json:"backup_id"`
	BackupPath  string     `json:"backup_path"`
	SizeBytes   int64      `json:"size_bytes,omitempty"`
	Files       int        `json:"files,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type storageStatusResponse struct {
	BackupDir             string                    `json:"backup_dir"`
	BackupIntervalSeconds int64                     `json:"backup_interval_seconds"`
	BackupRetention       int                       `json:"backup_retention"`
	Running               bool                      `json:"running"`
	LastRun               *storageBackupRunResponse `json:"last_run,omitempty"`
	LastSuccess           *storageBackupRunResponse `json:"last_success,omitempty"`
	NextRunAt             time.Time                 `json:"next_run_at"`
	Migration             *storageMigrationResponse `json:"migration,omitempty"`
}

type deleteMigrationBackupRequest struct {
	BackupID string `json:"backup_id"`
}

func (s *Server) startBackupMonitor() {
	if s == nil || s.backups == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.backupCancel = cancel
	s.backupDone = make(chan struct{})
	go func() {
		defer close(s.backupDone)
		s.runBackupMonitor(ctx)
	}()
}

func (s *Server) runBackupMonitor(ctx context.Context) {
	s.runScheduledBackup(ctx)
	interval := s.backups.interval
	if interval <= 0 || interval > backupScheduleCheckMaximum {
		interval = backupScheduleCheckMaximum
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runScheduledBackup(ctx)
		}
	}
}

func (s *Server) runScheduledBackup(ctx context.Context) {
	if _, ran, err := s.backups.RunIfDue(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.logJob(LogWarn, "", "scheduled SQLite backup failed", err.Error())
	} else if ran && err == nil {
		s.logJob(LogSuccess, "", "scheduled SQLite backup completed")
	}
}

func (s *Server) handleGetStorageSettings(w http.ResponseWriter, r *http.Request) {
	payload, err := s.storageStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleCreateStorageBackup(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeError(w, http.StatusServiceUnavailable, "database backups are not configured")
		return
	}
	run, err := s.backups.RunNow(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logJob(LogSuccess, "", "manual SQLite backup completed", run.Path)
	writeJSON(w, http.StatusOK, map[string]any{"backup": storageBackupRunPayload(&run)})
}

func (s *Server) handleDeleteMigrationBackup(w http.ResponseWriter, r *http.Request) {
	var request deleteMigrationBackupRequest
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	request.BackupID = strings.TrimSpace(request.BackupID)
	if request.BackupID == "" {
		writeError(w, http.StatusBadRequest, "backup_id is required")
		return
	}
	if err := s.deleteMigrationBackup(request.BackupID, s.now()); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "migration backup not found")
		case errors.Is(err, errMigrationBackupIDMismatch):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	s.legacyBackup = nil
	s.logJob(LogSuccess, "", "legacy plaintext migration backup deleted")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) storageStatus(ctx context.Context) (storageStatusResponse, error) {
	payload := storageStatusResponse{
		BackupDir:             s.config.BackupDir,
		BackupIntervalSeconds: int64(s.config.BackupInterval / time.Second),
		BackupRetention:       s.config.BackupRetention,
	}
	if s.backups != nil {
		status, err := s.backups.Status(ctx)
		if err != nil {
			return storageStatusResponse{}, err
		}
		payload.BackupDir = s.backups.dir
		payload.BackupIntervalSeconds = int64(s.backups.interval / time.Second)
		payload.BackupRetention = s.backups.retention
		payload.Running = status.Running
		payload.LastRun = storageBackupRunPayload(status.LastRun)
		payload.LastSuccess = storageBackupRunPayload(status.LastSuccess)
		payload.NextRunAt = status.NextRunAt
	}
	migration, err := s.storageMigrationStatus()
	if err != nil {
		return storageStatusResponse{}, err
	}
	payload.Migration = migration
	return payload, nil
}

func storageBackupRunPayload(run *backupRun) *storageBackupRunResponse {
	if run == nil {
		return nil
	}
	return &storageBackupRunResponse{
		ID: run.ID, Kind: run.Kind, Status: run.Status, Path: run.Path,
		SizeBytes: run.SizeBytes, SHA256: run.SHA256, Error: run.Error,
		StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
	}
}

func (s *Server) storageMigrationStatus() (*storageMigrationResponse, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var (
		payload                storageMigrationResponse
		startedAt, completedAt sql.NullInt64
	)
	err := s.db.QueryRow(`SELECT status,backup_id,backup_path,started_at,completed_at
		FROM storage_migration_state WHERE id=1`).Scan(
		&payload.Status, &payload.BackupID, &payload.BackupPath, &startedAt, &completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if startedAt.Valid {
		value := time.Unix(startedAt.Int64, 0).UTC()
		payload.CreatedAt = &value
	}
	if completedAt.Valid {
		value := time.Unix(completedAt.Int64, 0).UTC()
		payload.CompletedAt = &value
	}
	if payload.Status == "backup_pending" && payload.BackupPath != "" {
		backup, err := loadMigrationBackup(payload.BackupPath)
		if err != nil {
			return nil, fmt.Errorf("load migration backup status: %w", err)
		}
		if err := verifyMigrationBackup(backup); err != nil {
			return nil, fmt.Errorf("verify migration backup status: %w", err)
		}
		payload.Files = len(backup.Files)
		for _, file := range backup.Files {
			payload.SizeBytes += file.Size
		}
		created := backup.CreatedAt.UTC()
		payload.CreatedAt = &created
	}
	return &payload, nil
}

var errMigrationBackupIDMismatch = errors.New("migration backup id does not match the current backup")

func (s *Server) deleteMigrationBackup(backupID string, now time.Time) error {
	if s == nil || s.db == nil {
		return sql.ErrNoRows
	}
	var status, storedID, backupPath string
	err := s.db.QueryRow(`SELECT status,backup_id,backup_path
		FROM storage_migration_state WHERE id=1`).Scan(&status, &storedID, &backupPath)
	if err != nil {
		return err
	}
	if status != "backup_pending" && status != "delete_pending" {
		return sql.ErrNoRows
	}
	if storedID != backupID {
		return errMigrationBackupIDMismatch
	}
	if err := validateMigrationBackupDeletePath(s.config, backupPath); err != nil {
		return err
	}
	if status == "backup_pending" {
		backup, err := loadMigrationBackup(backupPath)
		if err != nil {
			return err
		}
		if backup.ID != backupID {
			return errMigrationBackupIDMismatch
		}
		if err := verifyMigrationBackup(backup); err != nil {
			return err
		}
		result, err := s.db.Exec(`UPDATE storage_migration_state
			SET status='delete_pending',updated_at=?,last_error=''
			WHERE id=1 AND status='backup_pending' AND backup_id=? AND backup_path=?`,
			now.Unix(), backupID, backupPath)
		if err != nil {
			return err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err != nil {
				return err
			}
			return errMigrationBackupIDMismatch
		}
	}
	return finishMigrationBackupDeletion(s.db, s.config, backupPath, now)
}

func finishMigrationBackupDeletion(db *sql.DB, cfg Config, backupPath string, now time.Time) error {
	if err := validateMigrationBackupDeletePath(cfg, backupPath); err != nil {
		return err
	}
	if err := os.RemoveAll(backupPath); err != nil {
		_, _ = db.Exec(`UPDATE storage_migration_state SET last_error=?,updated_at=? WHERE id=1`, err.Error(), now.Unix())
		return err
	}
	_, err := db.Exec(`UPDATE storage_migration_state
		SET status='complete',backup_id='',backup_path='',last_error='',updated_at=?
		WHERE id=1 AND status='delete_pending'`, now.Unix())
	return err
}

func validateMigrationBackupDeletePath(cfg Config, backupPath string) error {
	backupPath = strings.TrimSpace(backupPath)
	if backupPath == "" {
		return errors.New("migration backup path is empty")
	}
	expected, err := filepath.Abs(filepath.Join(filepath.Dir(cfg.DBFile), "migration-backups", "pending"))
	if err != nil {
		return err
	}
	actual, err := filepath.Abs(backupPath)
	if err != nil {
		return err
	}
	if !samePath(expected, actual) {
		return fmt.Errorf("refusing to delete migration backup outside %s", expected)
	}
	return nil
}
