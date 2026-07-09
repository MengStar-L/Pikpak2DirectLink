package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	proxyTempCleanupFallbackTTL = 3 * time.Hour
	proxyTempCleanupGrace       = 10 * time.Minute
	proxyTempCleanupRetryDelay  = 10 * time.Minute
	proxyTempCleanupBatchLimit  = 20
)

type proxyTempCleanupStore struct {
	db *sql.DB
}

type proxyTempCleanupRecord struct {
	ID           string
	JobID        string
	AccountID    string
	FileIDs      []string
	CleanupAfter time.Time
	Attempts     int
	LastError    string
	CreatedAt    time.Time
}

func newProxyTempCleanupStore(db *sql.DB) *proxyTempCleanupStore {
	return &proxyTempCleanupStore{db: db}
}

func (s *proxyTempCleanupStore) record(jobID, accountID string, ids []string, cleanupAfter, createdAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	ids = uniqueStrings(ids)
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(accountID) == "" || len(ids) == 0 {
		return nil
	}
	if cleanupAfter.IsZero() {
		cleanupAfter = createdAt.Add(proxyTempCleanupFallbackTTL)
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO proxy_temp_cleanups
			(id, job_id, account_id, file_ids_json, cleanup_after, attempts, created_at)
			VALUES(?,?,?,?,?,?,?)`,
		newJobID(),
		jobID,
		accountID,
		string(data),
		cleanupAfter.Unix(),
		0,
		createdAt.Unix(),
	)
	return err
}

func (s *proxyTempCleanupStore) due(now time.Time, limit int) ([]proxyTempCleanupRecord, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = proxyTempCleanupBatchLimit
	}
	rows, err := s.db.Query(
		`SELECT id, job_id, account_id, file_ids_json, cleanup_after, attempts, COALESCE(last_error, ''), created_at
			FROM proxy_temp_cleanups
			WHERE cleanup_after<=?
			ORDER BY cleanup_after ASC
			LIMIT ?`,
		now.Unix(),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []proxyTempCleanupRecord
	for rows.Next() {
		var rawIDs string
		var cleanupUnix, createdUnix int64
		var rec proxyTempCleanupRecord
		if err := rows.Scan(&rec.ID, &rec.JobID, &rec.AccountID, &rawIDs, &cleanupUnix, &rec.Attempts, &rec.LastError, &createdUnix); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rawIDs), &rec.FileIDs); err != nil {
			return nil, err
		}
		rec.FileIDs = uniqueStrings(rec.FileIDs)
		rec.CleanupAfter = time.Unix(cleanupUnix, 0)
		rec.CreatedAt = time.Unix(createdUnix, 0)
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *proxyTempCleanupStore) markFailure(id string, now time.Time, err error) error {
	if s == nil || s.db == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	_, updateErr := s.db.Exec(
		`UPDATE proxy_temp_cleanups
			SET attempts=attempts+1, last_error=?, cleanup_after=?
			WHERE id=?`,
		message,
		now.Add(proxyTempCleanupRetryDelay).Unix(),
		id,
	)
	return updateErr
}

func (s *proxyTempCleanupStore) delete(id string) error {
	if s == nil || s.db == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM proxy_temp_cleanups WHERE id=?`, id)
	return err
}

func proxyDeferredCleanupAfter(results []JobResult, completedAt time.Time) time.Time {
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	cleanupAfter := completedAt.Add(proxyTempCleanupFallbackTTL)
	for _, result := range results {
		expiresAt, ok := parseProxyResultExpiry(result.ExpiresAt)
		if ok && expiresAt.After(cleanupAfter) {
			cleanupAfter = expiresAt
		}
	}
	return cleanupAfter.Add(proxyTempCleanupGrace)
}

func parseProxyResultExpiry(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func (s *Server) deferJobTempCleanup(jobID string, account AccountRuntime, cleanupAfter time.Time, fallbackIDs ...string) error {
	if s.tempCleanups == nil {
		return nil
	}
	job, ok := s.jobs.get(jobID)
	if !ok {
		return errors.New("job not found")
	}
	if job.TempAccountID != "" && job.TempAccountID != account.ID {
		return nil
	}
	ids := append([]string(nil), job.TempIDs...)
	if len(ids) == 0 && strings.TrimSpace(job.FolderID) != "" {
		ids = append(ids, job.FolderID)
	}
	ids = append(ids, fallbackIDs...)
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return nil
	}
	if err := s.tempCleanups.record(jobID, account.ID, ids, cleanupAfter, s.now()); err != nil {
		return err
	}
	s.clearTempResources(jobID)
	return nil
}

func (s *Server) cleanupProxyTempResources(now time.Time) {
	if s.tempCleanups == nil {
		return
	}
	records, err := s.tempCleanups.due(now, proxyTempCleanupBatchLimit)
	if err != nil {
		s.logJob(LogWarn, "", "deferred PikPak cleanup failed", err.Error())
		return
	}
	for _, rec := range records {
		if len(rec.FileIDs) == 0 {
			if err := s.tempCleanups.delete(rec.ID); err != nil {
				s.logJob(LogWarn, rec.JobID, "deferred PikPak cleanup failed", err.Error())
			}
			continue
		}
		if s.accounts == nil {
			s.markProxyTempCleanupFailure(rec, now, errors.New("account pool is missing"))
			continue
		}
		account, ok := s.accounts.Get(rec.AccountID)
		if !ok || account.Client == nil {
			s.markProxyTempCleanupFailure(rec, now, errors.New("account is no longer available"))
			continue
		}
		timeout := s.config.RequestTimeout
		if timeout <= 0 {
			timeout = 20 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := account.Client.DeleteFiles(ctx, rec.FileIDs)
		cancel()
		if err != nil {
			s.markProxyTempCleanupFailure(rec, now, err)
			continue
		}
		if err := s.tempCleanups.delete(rec.ID); err != nil {
			s.logJob(LogWarn, rec.JobID, "deferred PikPak cleanup failed", err.Error())
			continue
		}
		s.clearTempResources(rec.JobID)
		s.logJob(LogSuccess, rec.JobID, "deferred PikPak cleanup succeeded", "files="+itoa(len(rec.FileIDs)))
	}
}

func (s *Server) markProxyTempCleanupFailure(rec proxyTempCleanupRecord, now time.Time, err error) {
	if markErr := s.tempCleanups.markFailure(rec.ID, now, err); markErr != nil {
		s.logJob(LogWarn, rec.JobID, "deferred PikPak cleanup failed", markErr.Error())
		return
	}
	if err != nil {
		s.logJob(LogWarn, rec.JobID, "deferred PikPak cleanup failed", err.Error())
	}
}
