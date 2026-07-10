package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	proxyTempCleanupFallbackTTL = 3 * time.Hour
	proxyTempCleanupGrace       = 10 * time.Minute
	proxyTempCleanupRetryDelay  = 10 * time.Minute
	proxyTempCleanupBatchLimit  = 20
	proxyTempCleanupPurpose     = "proxy_temp_cleanups.file_ids"
)

type proxyTempCleanupStore struct {
	db     *sql.DB
	cipher *SecretCipher
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

func newProxyTempCleanupStore(db *sql.DB, cipher ...*SecretCipher) *proxyTempCleanupStore {
	store := &proxyTempCleanupStore{db: db}
	if len(cipher) > 0 {
		store.cipher = cipher[0]
	}
	return store
}

func (s *proxyTempCleanupStore) record(jobID, accountID string, ids []string, cleanupAfter, createdAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.recordTx(tx, jobID, accountID, ids, cleanupAfter, createdAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *proxyTempCleanupStore) recordTx(tx *sql.Tx, jobID, accountID string, ids []string, cleanupAfter, createdAt time.Time) error {
	if s == nil || tx == nil {
		return nil
	}
	jobID = strings.TrimSpace(jobID)
	accountID = strings.TrimSpace(accountID)
	ids = uniqueStrings(ids)
	if jobID == "" || accountID == "" || len(ids) == 0 {
		return nil
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	if cleanupAfter.IsZero() {
		cleanupAfter = createdAt.Add(proxyTempCleanupFallbackTTL)
	}
	id := cleanupRecordID(jobID, accountID)
	var existingJob, existingAccount, existingIDs string
	err := tx.QueryRow(`SELECT job_id,account_id,file_ids_json FROM proxy_temp_cleanups WHERE id=?`, id).Scan(&existingJob, &existingAccount, &existingIDs)
	if err == nil {
		if strings.TrimSpace(existingJob) != jobID || strings.TrimSpace(existingAccount) != accountID {
			return errors.New("cleanup record identity cannot be changed")
		}
		storedIDs, err := s.decodeFileIDs(id, existingIDs)
		if err != nil {
			return err
		}
		ids = uniqueStrings(append(storedIDs, ids...))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	encoded, err := s.encodeFileIDs(id, ids)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO proxy_temp_cleanups
			(id, job_id, account_id, file_ids_json, cleanup_after, attempts, created_at)
			VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
			account_id=excluded.account_id,file_ids_json=excluded.file_ids_json,
			cleanup_after=excluded.cleanup_after,attempts=0,last_error=NULL`,
		id,
		jobID,
		accountID,
		encoded,
		cleanupAfter.Unix(),
		0,
		createdAt.Unix(),
	)
	return err
}

func cleanupRecordID(jobID, accountID string) string {
	identity := strings.TrimSpace(jobID) + "\x00" + strings.TrimSpace(accountID)
	digest := sha256.Sum256([]byte(identity))
	return "job_account_" + hex.EncodeToString(digest[:12])
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
		decoded, err := s.decodeFileIDs(rec.ID, rawIDs)
		if err != nil {
			return nil, err
		}
		rec.FileIDs = decoded
		rec.FileIDs = uniqueStrings(rec.FileIDs)
		rec.CleanupAfter = time.Unix(cleanupUnix, 0)
		rec.CreatedAt = time.Unix(createdUnix, 0)
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *proxyTempCleanupStore) decodeFileIDs(id, stored string) ([]string, error) {
	data := []byte(stored)
	if strings.HasPrefix(stored, secretEnvelopeVersion+".") {
		if s.cipher == nil {
			return nil, errors.New("cleanup file IDs are encrypted but no cipher is configured")
		}
		plaintext, err := s.cipher.Decrypt(proxyTempCleanupPurpose, id, stored)
		if err != nil {
			return nil, fmt.Errorf("decrypt cleanup file IDs %q: %w", id, err)
		}
		data = plaintext
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		return nil, fmt.Errorf("decode cleanup file IDs %q: %w", id, err)
	}
	return uniqueStrings(ids), nil
}

func (s *proxyTempCleanupStore) encodeFileIDs(id string, ids []string) (string, error) {
	data, err := json.Marshal(uniqueStrings(ids))
	if err != nil {
		return "", err
	}
	if s.cipher == nil {
		return string(data), nil
	}
	encoded, err := s.cipher.Encrypt(proxyTempCleanupPurpose, id, data)
	if err != nil {
		return "", fmt.Errorf("encrypt cleanup file IDs: %w", err)
	}
	return encoded, nil
}

// RotateSecrets canonicalizes legacy/random records by job and account while
// rotating every payload to the current key and its new record-bound AAD.
func (s *proxyTempCleanupStore) RotateSecrets() error {
	if s == nil || s.db == nil || s.cipher == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id,job_id,account_id,file_ids_json,cleanup_after,
		attempts,COALESCE(last_error, ''),created_at
		FROM proxy_temp_cleanups ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	type cleanupStoredRecord struct {
		id           string
		jobID        string
		accountID    string
		stored       string
		cleanupAfter int64
		attempts     int
		lastError    string
		createdAt    int64
	}
	var records []cleanupStoredRecord
	for rows.Next() {
		var record cleanupStoredRecord
		if err := rows.Scan(
			&record.id, &record.jobID, &record.accountID, &record.stored,
			&record.cleanupAfter, &record.attempts, &record.lastError, &record.createdAt,
		); err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	type cleanupMergedRecord struct {
		id           string
		jobID        string
		accountID    string
		fileIDs      []string
		cleanupAfter int64
		attempts     int
		lastError    string
		createdAt    int64
	}
	groups := make(map[string]*cleanupMergedRecord, len(records))
	var groupOrder []string
	for _, record := range records {
		ids, err := s.decodeFileIDs(record.id, record.stored)
		if err != nil {
			return err
		}
		jobID := strings.TrimSpace(record.jobID)
		accountID := strings.TrimSpace(record.accountID)
		key := jobID + "\x00" + accountID
		merged := groups[key]
		if merged == nil {
			merged = &cleanupMergedRecord{
				id: cleanupRecordID(jobID, accountID), jobID: jobID, accountID: accountID,
				cleanupAfter: record.cleanupAfter, attempts: record.attempts,
				lastError: record.lastError, createdAt: record.createdAt,
			}
			groups[key] = merged
			groupOrder = append(groupOrder, key)
		} else {
			if record.cleanupAfter > merged.cleanupAfter {
				merged.cleanupAfter = record.cleanupAfter
			}
			if record.createdAt < merged.createdAt {
				merged.createdAt = record.createdAt
			}
			if record.attempts > merged.attempts {
				merged.attempts = record.attempts
				merged.lastError = record.lastError
			} else if record.attempts == merged.attempts && record.lastError != "" {
				merged.lastError = record.lastError
			}
		}
		merged.fileIDs = uniqueStrings(append(merged.fileIDs, ids...))
	}

	if _, err := tx.Exec(`DELETE FROM proxy_temp_cleanups`); err != nil {
		return err
	}
	for _, key := range groupOrder {
		merged := groups[key]
		encoded, err := s.encodeFileIDs(merged.id, merged.fileIDs)
		if err != nil {
			return err
		}
		var lastError any
		if merged.lastError != "" {
			lastError = merged.lastError
		}
		if _, err := tx.Exec(`INSERT INTO proxy_temp_cleanups
			(id,job_id,account_id,file_ids_json,cleanup_after,attempts,last_error,created_at)
			VALUES(?,?,?,?,?,?,?,?)`,
			merged.id, merged.jobID, merged.accountID, encoded, merged.cleanupAfter,
			merged.attempts, lastError, merged.createdAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// removeIDsByJobAccountTx removes only IDs confirmed deleted for one account.
// The caller owns the transaction so job state and cleanup state can be
// reconciled in the same commit.
func (s *proxyTempCleanupStore) removeIDsByJobAccountTx(tx *sql.Tx, jobID, accountID string, ids []string) error {
	jobID = strings.TrimSpace(jobID)
	accountID = strings.TrimSpace(accountID)
	if jobID == "" || accountID == "" {
		return nil
	}
	return s.removeRecordIDsTx(tx, cleanupRecordID(jobID, accountID), ids)
}

// removeRecordIDs is the standalone counterpart used when a cleanup worker has
// a concrete ledger record rather than a job/account pair.
func (s *proxyTempCleanupStore) removeRecordIDs(id string, ids []string) error {
	if s == nil || s.db == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.removeRecordIDsTx(tx, id, ids); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *proxyTempCleanupStore) removeRecordIDsTx(tx *sql.Tx, id string, ids []string) error {
	if s == nil || tx == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	removed := uniqueStrings(ids)
	if len(removed) == 0 {
		return nil
	}
	var stored string
	err := tx.QueryRow(`SELECT file_ids_json FROM proxy_temp_cleanups WHERE id=?`, strings.TrimSpace(id)).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	current, err := s.decodeFileIDs(strings.TrimSpace(id), stored)
	if err != nil {
		return err
	}
	removedSet := make(map[string]struct{}, len(removed))
	for _, fileID := range removed {
		removedSet[fileID] = struct{}{}
	}
	remaining := make([]string, 0, len(current))
	for _, fileID := range current {
		if _, ok := removedSet[fileID]; !ok {
			remaining = append(remaining, fileID)
		}
	}
	if len(remaining) == 0 {
		_, err := tx.Exec(`DELETE FROM proxy_temp_cleanups WHERE id=?`, strings.TrimSpace(id))
		return err
	}
	encoded, err := s.encodeFileIDs(strings.TrimSpace(id), remaining)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE proxy_temp_cleanups SET file_ids_json=? WHERE id=?`, encoded, strings.TrimSpace(id))
	return err
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
	return s.detachTempResources(jobID, account.ID)
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
		if err := s.tempCleanups.removeRecordIDs(rec.ID, rec.FileIDs); err != nil {
			s.logJob(LogWarn, rec.JobID, "deferred PikPak cleanup failed", err.Error())
			continue
		}
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
