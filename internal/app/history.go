package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const resolveHistoryTTL = 3 * time.Hour
const resolveHistoryCleanupInterval = time.Minute

type resolveHistoryStore struct {
	db      *sql.DB
	durable *sqlJobStore
}

func newResolveHistoryStore(db *sql.DB, durable ...*sqlJobStore) *resolveHistoryStore {
	store := &resolveHistoryStore{db: db}
	if len(durable) > 0 {
		store.durable = durable[0]
	}
	return store
}

func (s *Server) startResolveHistoryCleanup() {
	if s.history == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.historyCancel = cancel
	s.historyDone = make(chan struct{})
	go func() {
		defer close(s.historyDone)
		s.runResolveHistoryCleanup(ctx)
	}()
}

func (s *Server) runResolveHistoryCleanup(ctx context.Context) {
	s.cleanupResolveHistory(s.now())
	ticker := time.NewTicker(resolveHistoryCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupResolveHistory(s.now())
		}
	}
}

func (s *Server) cleanupResolveHistory(now time.Time) {
	s.expireSelectionJobs(now)
	if s.history == nil {
		s.cleanupProxyTempResources(now)
		return
	}
	if s.durableJobs != nil {
		if _, err := s.durableJobs.scrubExpired(now); err != nil {
			s.logJob(LogWarn, "", "resolve job detail cleanup failed", err.Error())
		}
	}
	if _, err := s.history.deleteExpired(now); err != nil {
		s.logJob(LogWarn, "", "CDK resolve history cleanup failed", err.Error())
	}
	s.cleanupProxyTempResources(now)
}

func (s *Server) expireSelectionJobs(now time.Time) {
	if s.jobs == nil {
		return
	}
	cutoff := now.Add(-selectionRequiredTimeout)
	for _, jobID := range s.jobs.expiredSelectionIDs(cutoff) {
		s.expireSelectionJob(jobID, cutoff)
	}
}

var errSelectionNoLongerExpired = errors.New("job is no longer waiting on an expired selection")

func (s *Server) expireSelectionJob(jobID string, cutoff time.Time) {
	updated, err := s.jobs.update(jobID, func(job *Job) error {
		if job.Status != JobSelectionRequired || job.UpdatedAt.After(cutoff) {
			return errSelectionNoLongerExpired
		}
		job.Status = JobFailed
		job.Stage = StageFailed
		job.Message = ""
		job.Error = "file selection timed out"
		job.FailureCode = "selection_timeout"
		return nil
	})
	if errors.Is(err, errSelectionNoLongerExpired) {
		return
	}
	if err != nil {
		s.logJob(LogError, jobID, "terminal job persistence failed", err.Error())
		s.requestRestart()
		return
	}
	if updated != nil && updated.UserID != "" && s.users != nil {
		if _, err := s.users.releaseQuotaReservation(jobID); err != nil {
			s.logJob(LogError, jobID, "quota reservation release failed", err.Error())
			s.requestRestart()
		}
	}
}

func (s *Server) saveCDKHistory(jobID string) {
	if s.history == nil {
		return
	}
	job, ok := s.jobs.get(jobID)
	if !ok || job.Status != JobCompleted {
		return
	}
	if err := s.history.saveJob(job); err != nil {
		s.logJob(LogWarn, jobID, "CDK resolve history save failed", err.Error())
	}
}

type resolveHistorySummary struct {
	ID               string         `json:"id"`
	JobID            string         `json:"job_id"`
	Kind             ResourceKind   `json:"kind"`
	Mode             string         `json:"mode"`
	Input            string         `json:"input"`
	ResultCount      int            `json:"result_count"`
	Batch            *BatchProgress `json:"batch,omitempty"`
	Status           JobStatus      `json:"status"`
	Stage            JobStage       `json:"stage"`
	FailureCode      string         `json:"failure_code,omitempty"`
	DetailsAvailable bool           `json:"details_available"`
	ChargedBytes     int64          `json:"charged_bytes"`
	CreatedAt        time.Time      `json:"created_at"`
	CompletedAt      time.Time      `json:"completed_at"`
	ExpiresAt        time.Time      `json:"expires_at"`
}

type resolveHistoryDetail struct {
	resolveHistorySummary
	Results []JobResult `json:"results"`
}

type storedResolveHistory struct {
	resolveHistoryDetail
	cdkCode string
	userID  string
}

func (s *resolveHistoryStore) saveJob(job *Job) error {
	if s == nil || s.db == nil || job == nil {
		return nil
	}
	if strings.TrimSpace(job.CDKCode) == "" && strings.TrimSpace(job.UserID) == "" || strings.TrimSpace(job.ParentID) != "" || job.Status != JobCompleted {
		return nil
	}
	if s.durable != nil {
		return nil
	}

	results := historyResultsFromJob(job)
	if len(results) == 0 {
		return nil
	}

	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return err
	}

	var batchJSON any
	if batch := cleanHistoryBatch(job.Batch); batch != nil {
		data, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		batchJSON = string(data)
	}

	completedAt := job.UpdatedAt
	if completedAt.IsZero() {
		completedAt = time.Now()
	}
	createdAt := job.CreatedAt
	if createdAt.IsZero() {
		createdAt = completedAt
	}

	input := strings.TrimSpace(firstNonEmpty(job.OriginalInput, job.Input))
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO cdk_resolve_history
			(id, cdk_code, user_id, job_id, kind, mode, input, results_json, batch_json, created_at, completed_at, expires_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		job.ID,
		normalizeCode(job.CDKCode),
		strings.TrimSpace(job.UserID),
		job.ID,
		string(job.Kind),
		job.Mode,
		input,
		string(resultsJSON),
		batchJSON,
		createdAt.Unix(),
		completedAt.Unix(),
		completedAt.Add(resolveHistoryTTL).Unix(),
	)
	return err
}

func (s *resolveHistoryStore) list(cdkCode string, now time.Time) ([]resolveHistorySummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, cdk_code, user_id, job_id, kind, mode, input, results_json, batch_json, created_at, completed_at, expires_at
			FROM cdk_resolve_history
			WHERE cdk_code=? AND expires_at>?
			ORDER BY completed_at DESC`,
		normalizeCode(cdkCode),
		now.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []resolveHistorySummary
	for rows.Next() {
		record, err := scanResolveHistory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record.resolveHistorySummary)
	}
	return out, rows.Err()
}

func (s *resolveHistoryStore) listByUser(userID string, now time.Time) ([]resolveHistorySummary, error) {
	var out []resolveHistorySummary
	seen := make(map[string]struct{})
	if s != nil && s.durable != nil {
		records, err := s.durable.listHistoryByUser(userID, now)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			summary := resolveHistorySummaryFromRecord(record)
			out = append(out, summary)
			seen[summary.ID] = struct{}{}
		}
	}
	legacy, err := s.listByUserLegacy(userID, now)
	if err != nil {
		return nil, err
	}
	for _, summary := range legacy {
		if _, exists := seen[summary.ID]; exists {
			continue
		}
		summary.Status = JobCompleted
		summary.Stage = StageComplete
		summary.DetailsAvailable = true
		out = append(out, summary)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CompletedAt.After(out[j].CompletedAt) })
	return out, nil
}

func (s *resolveHistoryStore) listByUserLegacy(userID string, now time.Time) ([]resolveHistorySummary, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, cdk_code, user_id, job_id, kind, mode, input, results_json, batch_json, created_at, completed_at, expires_at
			FROM cdk_resolve_history
			WHERE user_id=? AND expires_at>?
			ORDER BY completed_at DESC`,
		strings.TrimSpace(userID),
		now.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []resolveHistorySummary
	for rows.Next() {
		record, err := scanResolveHistory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record.resolveHistorySummary)
	}
	return out, rows.Err()
}

func (s *resolveHistoryStore) get(cdkCode, id string, now time.Time) (resolveHistoryDetail, bool, error) {
	if s == nil || s.db == nil {
		return resolveHistoryDetail{}, false, nil
	}
	row := s.db.QueryRow(
		`SELECT id, cdk_code, user_id, job_id, kind, mode, input, results_json, batch_json, created_at, completed_at, expires_at
			FROM cdk_resolve_history
			WHERE id=? AND cdk_code=? AND expires_at>?`,
		strings.TrimSpace(id),
		normalizeCode(cdkCode),
		now.Unix(),
	)
	record, err := scanResolveHistory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return resolveHistoryDetail{}, false, nil
	}
	if err != nil {
		return resolveHistoryDetail{}, false, err
	}
	return record.resolveHistoryDetail, true, nil
}

func (s *resolveHistoryStore) getByUser(userID, id string, now time.Time) (resolveHistoryDetail, bool, error) {
	if s != nil && s.durable != nil {
		record, ok, err := s.durable.getRecord(id, resolveJobOwnerUser, userID, now)
		if err != nil {
			return resolveHistoryDetail{}, false, err
		}
		if ok && record.ParentID == "" && isTerminalJobStatus(record.Status) {
			detail := resolveHistoryDetail{resolveHistorySummary: resolveHistorySummaryFromRecord(record)}
			if !record.DetailsAvailable {
				return detail, true, errResolveJobDetailsExpired
			}
			detail.Results = historyResultsFromJob(record.Job)
			return detail, true, nil
		}
	}
	return s.getByUserLegacy(userID, id, now)
}

func (s *resolveHistoryStore) getByUserLegacy(userID, id string, now time.Time) (resolveHistoryDetail, bool, error) {
	if s == nil || s.db == nil {
		return resolveHistoryDetail{}, false, nil
	}
	row := s.db.QueryRow(
		`SELECT id, cdk_code, user_id, job_id, kind, mode, input, results_json, batch_json, created_at, completed_at, expires_at
			FROM cdk_resolve_history
			WHERE id=? AND user_id=? AND expires_at>?`,
		strings.TrimSpace(id),
		strings.TrimSpace(userID),
		now.Unix(),
	)
	record, err := scanResolveHistory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return resolveHistoryDetail{}, false, nil
	}
	if err != nil {
		return resolveHistoryDetail{}, false, err
	}
	return record.resolveHistoryDetail, true, nil
}

func (s *resolveHistoryStore) deleteExpired(now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var deleted int64
	if s.durable != nil {
		count, err := s.durable.deleteExpired(now)
		if err != nil {
			return 0, err
		}
		deleted += count
	}
	res, err := s.db.Exec(`DELETE FROM cdk_resolve_history WHERE expires_at<=?`, now.Unix())
	if err != nil {
		return 0, err
	}
	legacyDeleted, err := res.RowsAffected()
	return deleted + legacyDeleted, err
}

func resolveHistorySummaryFromRecord(record resolveJobRecord) resolveHistorySummary {
	summary := resolveHistorySummary{
		ID:               record.ID,
		JobID:            record.ID,
		Kind:             record.Kind,
		Mode:             record.Mode,
		ResultCount:      record.ResultCount,
		Status:           record.Status,
		Stage:            record.Stage,
		FailureCode:      record.FailureCode,
		DetailsAvailable: record.DetailsAvailable,
		ChargedBytes:     record.ChargedBytes,
		CreatedAt:        record.CreatedAt,
		CompletedAt:      record.CompletedAt,
		ExpiresAt:        record.DetailsExpiresAt,
	}
	if record.DetailsAvailable && record.Job != nil {
		summary.Input = strings.TrimSpace(firstNonEmpty(record.Job.OriginalInput, record.Job.Input))
		summary.Batch = cleanHistoryBatch(record.Job.Batch)
	}
	return summary
}

type resolveHistoryScanner interface {
	Scan(dest ...any) error
}

func scanResolveHistory(scanner resolveHistoryScanner) (storedResolveHistory, error) {
	var (
		id          string
		cdkCode     string
		userID      string
		jobID       string
		kind        string
		mode        string
		input       string
		resultsRaw  string
		batchRaw    sql.NullString
		createdUnix int64
		doneUnix    int64
		expiresUnix int64
	)
	if err := scanner.Scan(&id, &cdkCode, &userID, &jobID, &kind, &mode, &input, &resultsRaw, &batchRaw, &createdUnix, &doneUnix, &expiresUnix); err != nil {
		return storedResolveHistory{}, err
	}

	var results []JobResult
	if err := json.Unmarshal([]byte(resultsRaw), &results); err != nil {
		return storedResolveHistory{}, err
	}

	var batch *BatchProgress
	if batchRaw.Valid && strings.TrimSpace(batchRaw.String) != "" {
		var decoded BatchProgress
		if err := json.Unmarshal([]byte(batchRaw.String), &decoded); err != nil {
			return storedResolveHistory{}, err
		}
		batch = cleanHistoryBatch(&decoded)
	}

	return storedResolveHistory{
		cdkCode: cdkCode,
		userID:  userID,
		resolveHistoryDetail: resolveHistoryDetail{
			resolveHistorySummary: resolveHistorySummary{
				ID:               id,
				JobID:            jobID,
				Kind:             ResourceKind(kind),
				Mode:             mode,
				Input:            input,
				ResultCount:      len(results),
				Batch:            batch,
				Status:           JobCompleted,
				Stage:            StageComplete,
				DetailsAvailable: true,
				CreatedAt:        time.Unix(createdUnix, 0),
				CompletedAt:      time.Unix(doneUnix, 0),
				ExpiresAt:        time.Unix(expiresUnix, 0),
			},
			Results: results,
		},
	}, nil
}

func historyResultsFromJob(job *Job) []JobResult {
	if job == nil {
		return nil
	}
	results := make([]JobResult, 0, len(job.Results)+1)
	if job.Result != nil {
		results = append(results, *job.Result)
	}
	if len(job.Results) > 0 {
		results = append(results, job.Results...)
	}
	return results
}

func cleanHistoryBatch(batch *BatchProgress) *BatchProgress {
	if batch == nil {
		return nil
	}
	return &BatchProgress{
		Total:     batch.Total,
		Succeeded: batch.Succeeded,
		Failed:    batch.Failed,
	}
}
