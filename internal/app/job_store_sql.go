package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	resolveJobDetailsTTL = 3 * time.Hour
	resolveJobRecordTTL  = 30 * 24 * time.Hour

	resolveJobPayloadPurpose = "resolve_jobs.payload"
	resolveJobOwnerAdmin     = "admin"
	resolveJobOwnerUser      = "user"
	resolveJobOwnerCDK       = "cdk"
)

const resolveJobRestartError = "service restarted before the job completed"

var errResolveJobDetailsExpired = errors.New("resolve job details have expired")

// sqlJobStore is the durable counterpart to the in-memory jobStore. Queryable
// metadata stays in clear columns; the complete Job is always stored inside an
// authenticated encrypted payload.
type sqlJobStore struct {
	db     *sql.DB
	cipher *SecretCipher
	now    func() time.Time
}

type resolveJobWriteMetadata struct {
	FailureCode   string
	ErrorCategory string
	ChargedBytes  int64
}

type resolveJobRecord struct {
	ID               string
	ParentID         string
	OwnerType        string
	OwnerID          string
	Kind             ResourceKind
	Mode             string
	Status           JobStatus
	Stage            JobStage
	FailureCode      string
	ErrorCategory    string
	ErrorMessage     string
	ResultCount      int
	ChargedBytes     int64
	DetailsAvailable bool
	DetailsExpiresAt time.Time
	RecordExpiresAt  time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CompletedAt      time.Time
	Job              *Job
}

func newSQLJobStore(db *sql.DB, cipher *SecretCipher) *sqlJobStore {
	return &sqlJobStore{db: db, cipher: cipher, now: time.Now}
}

// resolveJobPayload deliberately mirrors Job instead of marshaling Job
// directly. Several runtime fields use json:"-" and are required after a
// database read for ownership checks, proxy refreshes, cleanup, and restart
// handling.
type resolveJobPayload struct {
	ID              string                    `json:"id"`
	Kind            ResourceKind              `json:"kind"`
	Mode            string                    `json:"mode"`
	Input           string                    `json:"input"`
	OriginalInput   string                    `json:"original_input"`
	PassCode        string                    `json:"pass_code"`
	Status          JobStatus                 `json:"status"`
	Stage           JobStage                  `json:"stage"`
	Message         string                    `json:"message"`
	Error           string                    `json:"error"`
	BaseURL         string                    `json:"base_url"`
	FolderID        string                    `json:"folder_id"`
	CDKCode         string                    `json:"cdk_code"`
	UserID          string                    `json:"user_id"`
	ProxyAllowed    bool                      `json:"proxy_allowed"`
	AccountID       string                    `json:"account_id"`
	Share           *resolveJobSharePayload   `json:"share"`
	Items           []DownloadItem            `json:"items"`
	AccountAttempts []AccountAttempt          `json:"account_attempts"`
	Result          *resolveJobResultPayload  `json:"result"`
	Results         []resolveJobResultPayload `json:"results"`
	Warnings        []string                  `json:"warnings"`
	QueueAhead      int                       `json:"queue_ahead"`
	TempAccountID   string                    `json:"temp_account_id"`
	TempIDs         []string                  `json:"temp_ids"`
	ResolveAll      bool                      `json:"resolve_all"`
	ResolveSelected bool                      `json:"resolve_selected"`
	ParentID        string                    `json:"parent_id"`
	Batch           *BatchProgress            `json:"batch"`
	CreatedAt       time.Time                 `json:"created_at"`
	UpdatedAt       time.Time                 `json:"updated_at"`
}

type resolveJobSharePayload struct {
	ShareID       string         `json:"share_id"`
	TailID        string         `json:"tail_id"`
	PassCodeToken string         `json:"pass_code_token"`
	SelectedID    string         `json:"selected_id"`
	SelectedIDs   []string       `json:"selected_ids"`
	SelectedItems []DownloadItem `json:"selected_items"`
}

type resolveJobResultPayload struct {
	File       DownloadItem `json:"file"`
	URL        string       `json:"url"`
	DirectURL  string       `json:"direct_url"`
	ProxyURL   string       `json:"proxy_url"`
	ProxyToken string       `json:"proxy_token"`
	ExpiresAt  string       `json:"expires_at"`
	AccountID  string       `json:"account_id"`
}

func (s *sqlJobStore) create(job *Job, metadata ...resolveJobWriteMetadata) error {
	return s.write(job, true, metadata)
}

func (s *sqlJobStore) upsert(job *Job, metadata ...resolveJobWriteMetadata) error {
	return s.write(job, false, metadata)
}

func (s *sqlJobStore) write(job *Job, createOnly bool, metadata []resolveJobWriteMetadata) error {
	if s == nil || s.db == nil || s.cipher == nil {
		return errors.New("SQL job store is not initialized")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.writeTx(tx, job, createOnly, metadata); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlJobStore) writeTx(tx *sql.Tx, job *Job, createOnly bool, metadata []resolveJobWriteMetadata) error {
	if s == nil || s.cipher == nil || tx == nil {
		return errors.New("SQL job transaction is not initialized")
	}
	if job == nil || strings.TrimSpace(job.ID) == "" {
		return errors.New("job ID is required")
	}
	if len(metadata) > 1 {
		return errors.New("at most one job metadata value is allowed")
	}
	meta := resolveJobWriteMetadata{
		FailureCode:  job.FailureCode,
		ChargedBytes: job.ChargedBytes,
	}
	if len(metadata) == 1 {
		meta = metadata[0]
	}
	if meta.ChargedBytes < 0 {
		return errors.New("charged bytes cannot be negative")
	}

	now := s.currentTime()
	copyJob := cloneJob(job)
	copyJob.ID = strings.TrimSpace(copyJob.ID)
	copyJob.ParentID = strings.TrimSpace(copyJob.ParentID)
	ownerType, ownerID := resolveJobOwner(copyJob)

	existing, err := readExistingResolveJob(tx, copyJob.ID)
	if err != nil {
		return err
	}
	if createOnly && existing.found {
		return fmt.Errorf("resolve job %q already exists", copyJob.ID)
	}
	if existing.found && existing.parentID != copyJob.ParentID {
		return errors.New("resolve job parent cannot be changed")
	}

	if copyJob.CreatedAt.IsZero() {
		if existing.found {
			copyJob.CreatedAt = time.Unix(existing.createdAt, 0).UTC()
		} else {
			copyJob.CreatedAt = now
		}
	}
	if copyJob.UpdatedAt.IsZero() {
		copyJob.UpdatedAt = now
	}

	childIndex, err := resolveJobChildIndex(tx, copyJob.ID, copyJob.ParentID, existing)
	if err != nil {
		return err
	}

	completedAt, detailsExpiresAt := resolveJobCompletionTimes(copyJob, existing, now)
	recordExpiresAt := now.Add(resolveJobRecordTTL).Unix()
	payload, err := encryptResolveJob(s.cipher, copyJob)
	if err != nil {
		return err
	}

	if len(metadata) == 0 && existing.found {
		if meta.ChargedBytes == 0 {
			meta.ChargedBytes = existing.chargedBytes
		}
		if copyJob.Status == JobFailed {
			if meta.FailureCode == "" {
				meta.FailureCode = existing.failureCode
			}
			if meta.ErrorCategory == "" {
				meta.ErrorCategory = existing.errorCategory
			}
		}
	}
	copyJob.FailureCode = strings.TrimSpace(meta.FailureCode)
	copyJob.ChargedBytes = meta.ChargedBytes
	copyJob.DetailsAvailable = true

	parentValue := nullableString(copyJob.ParentID)
	childValue := nullableInt(childIndex)
	completedValue := nullableUnix(completedAt)
	detailsValue := nullableUnix(detailsExpiresAt)
	args := []any{
		copyJob.ID, parentValue, childValue, ownerType, ownerID,
		string(copyJob.Kind), copyJob.Mode, string(copyJob.Status), string(copyJob.Stage),
		strings.TrimSpace(meta.FailureCode), strings.TrimSpace(meta.ErrorCategory), resolveJobClearError(copyJob),
		resolveJobResultCount(copyJob), meta.ChargedBytes, payload, detailsValue,
		recordExpiresAt, copyJob.CreatedAt.Unix(), copyJob.UpdatedAt.Unix(), completedValue,
	}

	if createOnly {
		_, err = tx.Exec(`INSERT INTO resolve_jobs (
			id, parent_job_id, child_index, owner_type, owner_id, kind, mode,
			status, phase, failure_code, error_category, error_message,
			result_count, charged_bytes, payload_encrypted, details_expires_at,
			record_expires_at, created_at, updated_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	} else {
		_, err = tx.Exec(`INSERT INTO resolve_jobs (
			id, parent_job_id, child_index, owner_type, owner_id, kind, mode,
			status, phase, failure_code, error_category, error_message,
			result_count, charged_bytes, payload_encrypted, details_expires_at,
			record_expires_at, created_at, updated_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			owner_type=excluded.owner_type,
			owner_id=excluded.owner_id,
			kind=excluded.kind,
			mode=excluded.mode,
			status=excluded.status,
			phase=excluded.phase,
			failure_code=excluded.failure_code,
			error_category=excluded.error_category,
			error_message=excluded.error_message,
			result_count=excluded.result_count,
			charged_bytes=excluded.charged_bytes,
			payload_encrypted=excluded.payload_encrypted,
			details_expires_at=excluded.details_expires_at,
			record_expires_at=excluded.record_expires_at,
			updated_at=excluded.updated_at,
			completed_at=excluded.completed_at`, args...)
	}
	if err != nil {
		return fmt.Errorf("write resolve job: %w", err)
	}
	return nil
}

// get performs the ownership check in SQL before decrypting the payload.
func (s *sqlJobStore) get(id, ownerType, ownerID string, now time.Time) (*Job, bool, error) {
	record, ok, err := s.getRecord(id, ownerType, ownerID, now)
	if err != nil || !ok || !record.DetailsAvailable {
		return nil, false, err
	}
	return cloneJob(record.Job), true, nil
}

func (s *sqlJobStore) getAny(id string, now time.Time) (*Job, bool, error) {
	record, ok, err := s.loadRecord(id, "", "", now)
	if err != nil || !ok || !record.DetailsAvailable {
		return nil, false, err
	}
	return cloneJob(record.Job), true, nil
}

func (s *sqlJobStore) getRecord(id, ownerType, ownerID string, now time.Time) (resolveJobRecord, bool, error) {
	ownerType, ownerID, err := normalizeResolveJobOwner(ownerType, ownerID)
	if err != nil {
		return resolveJobRecord{}, false, err
	}
	return s.loadRecord(id, ownerType, ownerID, now)
}

func (s *sqlJobStore) loadRecord(id, ownerType, ownerID string, now time.Time) (resolveJobRecord, bool, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return resolveJobRecord{}, false, nil
	}
	query := `SELECT id, COALESCE(parent_job_id, ''), owner_type, owner_id,
		kind, mode, status, phase, failure_code, error_category, error_message,
		result_count, charged_bytes, payload_encrypted, details_expires_at,
		record_expires_at, created_at, updated_at, completed_at
		FROM resolve_jobs WHERE id=? AND record_expires_at>?`
	args := []any{strings.TrimSpace(id), now.Unix()}
	if ownerType != "" {
		query += ` AND owner_type=? AND owner_id=?`
		args = append(args, ownerType, ownerID)
	}
	record, err := s.scanRecord(s.db.QueryRow(query, args...), now)
	if errors.Is(err, sql.ErrNoRows) {
		return resolveJobRecord{}, false, nil
	}
	if err != nil {
		return resolveJobRecord{}, false, err
	}
	return record, true, nil
}

func (s *sqlJobStore) listByUser(userID string, now time.Time) ([]*Job, error) {
	return s.list(resolveJobOwnerUser, strings.TrimSpace(userID), now)
}

func (s *sqlJobStore) listByCDK(code string, now time.Time) ([]*Job, error) {
	return s.list(resolveJobOwnerCDK, normalizeCode(code), now)
}

func (s *sqlJobStore) list(ownerType, ownerID string, now time.Time) ([]*Job, error) {
	records, err := s.listRecords(ownerType, ownerID, now, false)
	if err != nil {
		return nil, err
	}
	var jobs []*Job
	for _, record := range records {
		if record.DetailsAvailable {
			jobs = append(jobs, cloneJob(record.Job))
		}
	}
	return jobs, nil
}

func (s *sqlJobStore) listHistoryByUser(userID string, now time.Time) ([]resolveJobRecord, error) {
	return s.listRecords(resolveJobOwnerUser, strings.TrimSpace(userID), now, true)
}

func (s *sqlJobStore) listRecords(ownerType, ownerID string, now time.Time, terminalRootsOnly bool) ([]resolveJobRecord, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return nil, nil
	}
	ownerType, ownerID, err := normalizeResolveJobOwner(ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	query := `SELECT id, COALESCE(parent_job_id, ''), owner_type, owner_id,
		kind, mode, status, phase, failure_code, error_category, error_message,
		result_count, charged_bytes, payload_encrypted, details_expires_at,
		record_expires_at, created_at, updated_at, completed_at
		FROM resolve_jobs
		WHERE owner_type=? AND owner_id=? AND record_expires_at>?`
	args := []any{ownerType, ownerID, now.Unix()}
	if terminalRootsOnly {
		query += ` AND parent_job_id IS NULL AND status IN (?, ?)`
		args = append(args, string(JobCompleted), string(JobFailed))
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []resolveJobRecord
	for rows.Next() {
		record, err := s.scanRecord(rows, now)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// foldCompletedChildren closes the crash window between a child reaching a
// terminal state and its in-memory batch continuation updating the parent.
// Parents with unfinished children remain nonterminal and are failed by the
// subsequent restart pass; no child is ever re-enqueued here.
func (s *sqlJobStore) foldCompletedChildren(now time.Time) (int64, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return 0, nil
	}
	rows, err := s.db.Query(`SELECT id FROM resolve_jobs
		WHERE kind=? AND status IN (?, ?, ?) AND record_expires_at>?`,
		string(ResourceBatch), string(JobQueued), string(JobRunning),
		string(JobSelectionRequired), now.Unix())
	if err != nil {
		return 0, err
	}
	var parentIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		parentIDs = append(parentIDs, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	var folded int64
	for _, parentID := range parentIDs {
		parentRecord, ok, err := s.loadRecord(parentID, "", "", now)
		if err != nil {
			return folded, err
		}
		if !ok || !parentRecord.DetailsAvailable {
			continue
		}

		childRows, err := s.db.Query(`SELECT id FROM resolve_jobs
			WHERE parent_job_id=? AND record_expires_at>?
			ORDER BY child_index, id`, parentID, now.Unix())
		if err != nil {
			return folded, err
		}
		var childIDs []string
		for childRows.Next() {
			var id string
			if err := childRows.Scan(&id); err != nil {
				childRows.Close()
				return folded, err
			}
			childIDs = append(childIDs, id)
		}
		if err := childRows.Close(); err != nil {
			return folded, err
		}
		if len(childIDs) == 0 {
			continue
		}

		parent := parentRecord.Job
		progress := &BatchProgress{Total: len(childIDs)}
		var results []JobResult
		allTerminal := true
		var chargedBytes int64
		for index, childID := range childIDs {
			childRecord, ok, err := s.loadRecord(childID, "", "", now)
			if err != nil {
				return folded, err
			}
			if !ok || !isTerminalJobStatus(childRecord.Status) {
				allTerminal = false
				continue
			}
			if childRecord.Status == JobCompleted && childRecord.DetailsAvailable {
				progress.Succeeded++
				chargedBytes += childRecord.ChargedBytes
				label := batchLinkLabel(index+1, childRecord.Job.Input, childRecord.Job.Kind)
				for _, result := range historyResultsFromJob(childRecord.Job) {
					merged := result
					merged.File.Path = label + "/" + childResultPath(result.File)
					if merged.ProxyToken != "" {
						merged.ProxyURL = proxyURLForParent(parent.BaseURL, parent.ID, merged.ProxyToken)
					}
					merged.applyPreferredURL(childRecord.Job.Mode)
					results = append(results, merged)
				}
				continue
			}
			progress.Failed++
			failure := "child job details are unavailable"
			label := fmt.Sprintf("link %d", index+1)
			if childRecord.DetailsAvailable {
				failure = safeBatchFailureError(childRecord.Job.Error)
				label = batchLinkLabel(index+1, childRecord.Job.Input, childRecord.Job.Kind)
			}
			progress.Failures = append(progress.Failures, BatchFailure{Label: label, Error: failure})
		}

		parent.Batch = progress
		parent.ChargedBytes = chargedBytes
		parent.UpdatedAt = now.UTC()
		if allTerminal {
			parent.Results = results
			parent.Items = nil
			if progress.Succeeded > 0 {
				parent.Status = JobCompleted
				parent.Stage = StageComplete
				parent.Message = fmt.Sprintf("resolved %d/%d links", progress.Succeeded, progress.Total)
				parent.Error = ""
			} else {
				parent.Status = JobFailed
				parent.Stage = StageFailed
				parent.Message = ""
				parent.Error = "all batch links failed"
				parent.FailureCode = "batch_failed"
			}
		} else {
			parent.Status = JobRunning
			parent.Message = fmt.Sprintf("resolving: %d/%d links complete", progress.Succeeded+progress.Failed, progress.Total)
		}
		if err := s.upsert(parent); err != nil {
			return folded, err
		}
		folded++
	}
	return folded, nil
}

type resolveJobRecordScanner interface {
	Scan(dest ...any) error
}

func (s *sqlJobStore) scanRecord(scanner resolveJobRecordScanner, now time.Time) (resolveJobRecord, error) {
	var (
		record         resolveJobRecord
		encrypted      sql.NullString
		detailsExpires sql.NullInt64
		completed      sql.NullInt64
		recordExpires  int64
		createdAt      int64
		updatedAt      int64
	)
	if err := scanner.Scan(
		&record.ID, &record.ParentID, &record.OwnerType, &record.OwnerID,
		&record.Kind, &record.Mode, &record.Status, &record.Stage,
		&record.FailureCode, &record.ErrorCategory, &record.ErrorMessage,
		&record.ResultCount, &record.ChargedBytes, &encrypted, &detailsExpires,
		&recordExpires, &createdAt, &updatedAt, &completed,
	); err != nil {
		return resolveJobRecord{}, err
	}
	record.RecordExpiresAt = time.Unix(recordExpires, 0).UTC()
	record.CreatedAt = time.Unix(createdAt, 0).UTC()
	record.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if detailsExpires.Valid {
		record.DetailsExpiresAt = time.Unix(detailsExpires.Int64, 0).UTC()
	}
	if completed.Valid {
		record.CompletedAt = time.Unix(completed.Int64, 0).UTC()
	}
	record.DetailsAvailable = encrypted.Valid && encrypted.String != "" &&
		(!detailsExpires.Valid || detailsExpires.Int64 > now.Unix())
	if record.DetailsAvailable {
		job, err := decryptResolveJob(s.cipher, record.ID, encrypted.String)
		if err != nil {
			return resolveJobRecord{}, err
		}
		job.FailureCode = record.FailureCode
		job.DetailsAvailable = true
		job.ChargedBytes = record.ChargedBytes
		record.Job = job
	}
	return record, nil
}

// markNonterminalInterrupted atomically converts every durable in-flight job
// into a terminal failure. Nothing is re-enqueued after a restart.
func (s *sqlJobStore) markNonterminalInterrupted(now time.Time) (int64, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return 0, nil
	}
	now = now.UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id, payload_encrypted
		FROM resolve_jobs
		WHERE status IN (?, ?, ?) AND record_expires_at>?`,
		string(JobQueued), string(JobRunning), string(JobSelectionRequired), now.Unix())
	if err != nil {
		return 0, err
	}
	type interruptedPayload struct {
		id        string
		encrypted sql.NullString
	}
	var records []interruptedPayload
	for rows.Next() {
		var record interruptedPayload
		if err := rows.Scan(&record.id, &record.encrypted); err != nil {
			rows.Close()
			return 0, err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	for _, record := range records {
		var encrypted any
		resultCount := 0
		if record.encrypted.Valid && record.encrypted.String != "" {
			job, err := decryptResolveJob(s.cipher, record.id, record.encrypted.String)
			if err != nil {
				return 0, err
			}
			job.Status = JobFailed
			job.Stage = StageFailed
			job.Message = resolveJobRestartError
			job.Error = resolveJobRestartError
			job.UpdatedAt = now
			encrypted, err = encryptResolveJob(s.cipher, job)
			if err != nil {
				return 0, err
			}
			resultCount = resolveJobResultCount(job)
		}
		if _, err := tx.Exec(`UPDATE resolve_jobs SET
			status=?, phase=?, failure_code=?, error_category=?, error_message=?,
			result_count=?, payload_encrypted=?, details_expires_at=?,
			record_expires_at=?, updated_at=?, completed_at=?
			WHERE id=?`, string(JobFailed), string(StageFailed), "service_restart", "service",
			resolveJobRestartError, resultCount, encrypted, now.Add(resolveJobDetailsTTL).Unix(),
			now.Add(resolveJobRecordTTL).Unix(), now.Unix(), now.Unix(), record.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(records)), nil
}

func (s *sqlJobStore) scrubExpired(now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	result, err := s.db.Exec(`UPDATE resolve_jobs
		SET payload_encrypted=NULL
		WHERE payload_encrypted IS NOT NULL
			AND details_expires_at IS NOT NULL AND details_expires_at<=?`, now.Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *sqlJobStore) deleteExpired(now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	result, err := s.db.Exec(`DELETE FROM resolve_jobs WHERE record_expires_at<=?`, now.Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// RotateSecrets verifies and atomically rewrites payloads that were encrypted
// with a configured previous key. Current-key envelopes are left untouched.
func (s *sqlJobStore) RotateSecrets() error {
	if s == nil || s.db == nil || s.cipher == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id, payload_encrypted FROM resolve_jobs WHERE payload_encrypted IS NOT NULL`)
	if err != nil {
		return err
	}
	type encryptedJob struct {
		id      string
		payload string
	}
	var records []encryptedJob
	for rows.Next() {
		var record encryptedJob
		if err := rows.Scan(&record.id, &record.payload); err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, record := range records {
		needsRotation, err := s.cipher.NeedsRotation(record.payload)
		if err != nil {
			return fmt.Errorf("inspect resolve job %q encryption: %w", record.id, err)
		}
		if !needsRotation {
			continue
		}
		plaintext, err := s.cipher.Decrypt(resolveJobPayloadPurpose, record.id, record.payload)
		if err != nil {
			return fmt.Errorf("verify resolve job %q before rotation: %w", record.id, err)
		}
		replacement, err := s.cipher.Encrypt(resolveJobPayloadPurpose, record.id, plaintext)
		if err != nil {
			return fmt.Errorf("rotate resolve job %q: %w", record.id, err)
		}
		if _, err := tx.Exec(`UPDATE resolve_jobs SET payload_encrypted=? WHERE id=? AND payload_encrypted=?`, replacement, record.id, record.payload); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE resolve_jobs SET error_message=''
		WHERE error_message<>'' AND failure_code<>'service_restart'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *sqlJobStore) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

type existingResolveJob struct {
	found         bool
	parentID      string
	childIndex    sql.NullInt64
	createdAt     int64
	completedAt   sql.NullInt64
	failureCode   string
	errorCategory string
	chargedBytes  int64
}

func readExistingResolveJob(tx *sql.Tx, id string) (existingResolveJob, error) {
	var record existingResolveJob
	var parent sql.NullString
	err := tx.QueryRow(`SELECT parent_job_id, child_index, created_at, completed_at,
		failure_code, error_category, charged_bytes
		FROM resolve_jobs WHERE id=?`, id).Scan(&parent, &record.childIndex,
		&record.createdAt, &record.completedAt, &record.failureCode,
		&record.errorCategory, &record.chargedBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	record.found = true
	if parent.Valid {
		record.parentID = parent.String
	}
	return record, nil
}

func resolveJobChildIndex(tx *sql.Tx, id, parentID string, existing existingResolveJob) (sql.NullInt64, error) {
	if parentID == "" {
		return sql.NullInt64{}, nil
	}
	if existing.found {
		if !existing.childIndex.Valid {
			return sql.NullInt64{}, errors.New("stored child job has no child index")
		}
		return existing.childIndex, nil
	}
	var index int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(child_index), -1) + 1
		FROM resolve_jobs WHERE parent_job_id=?`, parentID).Scan(&index); err != nil {
		return sql.NullInt64{}, err
	}
	return sql.NullInt64{Int64: index, Valid: true}, nil
}

func resolveJobCompletionTimes(job *Job, existing existingResolveJob, now time.Time) (sql.NullInt64, sql.NullInt64) {
	if !isTerminalJobStatus(job.Status) {
		return sql.NullInt64{}, sql.NullInt64{}
	}
	completed := now.Unix()
	if existing.completedAt.Valid {
		completed = existing.completedAt.Int64
	} else if !job.UpdatedAt.IsZero() {
		completed = job.UpdatedAt.Unix()
	}
	return sql.NullInt64{Int64: completed, Valid: true}, sql.NullInt64{
		Int64: completed + int64(resolveJobDetailsTTL/time.Second),
		Valid: true,
	}
}

func resolveJobOwner(job *Job) (string, string) {
	if userID := strings.TrimSpace(job.UserID); userID != "" {
		return resolveJobOwnerUser, userID
	}
	if code := normalizeCode(job.CDKCode); code != "" {
		return resolveJobOwnerCDK, code
	}
	return resolveJobOwnerAdmin, resolveJobOwnerAdmin
}

func normalizeResolveJobOwner(ownerType, ownerID string) (string, string, error) {
	ownerType = strings.ToLower(strings.TrimSpace(ownerType))
	switch ownerType {
	case resolveJobOwnerUser:
		ownerID = strings.TrimSpace(ownerID)
	case resolveJobOwnerCDK:
		ownerID = normalizeCode(ownerID)
	case resolveJobOwnerAdmin:
		ownerID = resolveJobOwnerAdmin
	default:
		return "", "", errors.New("invalid resolve job owner type")
	}
	if ownerID == "" {
		return "", "", errors.New("resolve job owner ID is required")
	}
	return ownerType, ownerID, nil
}

func resolveJobResultCount(job *Job) int {
	if job == nil {
		return 0
	}
	count := len(job.Results)
	if job.Result != nil {
		count++
	}
	return count
}

func resolveJobClearError(job *Job) string {
	if job != nil && job.FailureCode == "service_restart" {
		return resolveJobRestartError
	}
	return ""
}

func encryptResolveJob(cipher *SecretCipher, job *Job) (string, error) {
	payload, err := json.Marshal(resolveJobPayloadFromJob(job))
	if err != nil {
		return "", fmt.Errorf("marshal resolve job: %w", err)
	}
	encrypted, err := cipher.Encrypt(resolveJobPayloadPurpose, job.ID, payload)
	if err != nil {
		return "", fmt.Errorf("encrypt resolve job: %w", err)
	}
	return encrypted, nil
}

func decryptResolveJob(cipher *SecretCipher, id, encrypted string) (*Job, error) {
	plaintext, err := cipher.Decrypt(resolveJobPayloadPurpose, id, encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt resolve job %q: %w", id, err)
	}
	var payload resolveJobPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal resolve job %q: %w", id, err)
	}
	if payload.ID != id {
		return nil, fmt.Errorf("resolve job payload ID %q does not match row ID %q", payload.ID, id)
	}
	return payload.job(), nil
}

func resolveJobPayloadFromJob(job *Job) resolveJobPayload {
	payload := resolveJobPayload{
		ID: job.ID, Kind: job.Kind, Mode: job.Mode, Input: job.Input,
		OriginalInput: job.OriginalInput, PassCode: job.PassCode,
		Status: job.Status, Stage: job.Stage, Message: job.Message, Error: job.Error,
		BaseURL: job.BaseURL, FolderID: job.FolderID, CDKCode: job.CDKCode,
		UserID: job.UserID, ProxyAllowed: job.ProxyAllowed, AccountID: job.AccountID,
		Items:           append([]DownloadItem(nil), job.Items...),
		AccountAttempts: append([]AccountAttempt(nil), job.AccountAttempts...),
		Warnings:        append([]string(nil), job.Warnings...), QueueAhead: job.QueueAhead,
		TempAccountID: job.TempAccountID, TempIDs: append([]string(nil), job.TempIDs...),
		ResolveAll: job.ResolveAll, ResolveSelected: job.ResolveSelected,
		ParentID: job.ParentID, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}
	if job.Share != nil {
		payload.Share = &resolveJobSharePayload{
			ShareID: job.Share.ShareID, TailID: job.Share.TailID,
			PassCodeToken: job.Share.PassCodeToken, SelectedID: job.Share.SelectedID,
			SelectedIDs:   append([]string(nil), job.Share.SelectedIDs...),
			SelectedItems: append([]DownloadItem(nil), job.Share.SelectedItems...),
		}
	}
	if job.Result != nil {
		result := resolveJobResultPayloadFromResult(*job.Result)
		payload.Result = &result
	}
	if job.Results != nil {
		payload.Results = make([]resolveJobResultPayload, len(job.Results))
		for i := range job.Results {
			payload.Results[i] = resolveJobResultPayloadFromResult(job.Results[i])
		}
	}
	if job.Batch != nil {
		batch := *job.Batch
		batch.Failures = append([]BatchFailure(nil), job.Batch.Failures...)
		payload.Batch = &batch
	}
	return payload
}

func (p resolveJobPayload) job() *Job {
	job := &Job{
		ID: p.ID, Kind: p.Kind, Mode: p.Mode, Input: p.Input,
		OriginalInput: p.OriginalInput, PassCode: p.PassCode,
		Status: p.Status, Stage: p.Stage, Message: p.Message, Error: p.Error,
		BaseURL: p.BaseURL, FolderID: p.FolderID, CDKCode: p.CDKCode,
		UserID: p.UserID, ProxyAllowed: p.ProxyAllowed, AccountID: p.AccountID,
		Items:           append([]DownloadItem(nil), p.Items...),
		AccountAttempts: append([]AccountAttempt(nil), p.AccountAttempts...),
		Warnings:        append([]string(nil), p.Warnings...), QueueAhead: p.QueueAhead,
		TempAccountID: p.TempAccountID, TempIDs: append([]string(nil), p.TempIDs...),
		ResolveAll: p.ResolveAll, ResolveSelected: p.ResolveSelected,
		ParentID: p.ParentID, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
	if p.Share != nil {
		job.Share = &ShareState{
			ShareID: p.Share.ShareID, TailID: p.Share.TailID,
			PassCodeToken: p.Share.PassCodeToken, SelectedID: p.Share.SelectedID,
			SelectedIDs:   append([]string(nil), p.Share.SelectedIDs...),
			SelectedItems: append([]DownloadItem(nil), p.Share.SelectedItems...),
		}
	}
	if p.Result != nil {
		result := p.Result.result()
		job.Result = &result
	}
	if p.Results != nil {
		job.Results = make([]JobResult, len(p.Results))
		for i := range p.Results {
			job.Results[i] = p.Results[i].result()
		}
	}
	if p.Batch != nil {
		batch := *p.Batch
		batch.Failures = append([]BatchFailure(nil), p.Batch.Failures...)
		job.Batch = &batch
	}
	return job
}

func resolveJobResultPayloadFromResult(result JobResult) resolveJobResultPayload {
	return resolveJobResultPayload{
		File: result.File, URL: result.URL, DirectURL: result.DirectURL,
		ProxyURL: result.ProxyURL, ProxyToken: result.ProxyToken,
		ExpiresAt: result.ExpiresAt, AccountID: result.AccountID,
	}
}

func (p resolveJobResultPayload) result() JobResult {
	return JobResult{
		File: p.File, URL: p.URL, DirectURL: p.DirectURL, ProxyURL: p.ProxyURL,
		ProxyToken: p.ProxyToken, ExpiresAt: p.ExpiresAt, AccountID: p.AccountID,
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nullableUnix(value sql.NullInt64) any {
	return nullableInt(value)
}
