package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const resolveHistoryTTL = 3 * time.Hour
const resolveHistoryCleanupInterval = time.Minute

type resolveHistoryStore struct {
	db *sql.DB
}

func newResolveHistoryStore(db *sql.DB) *resolveHistoryStore {
	return &resolveHistoryStore{db: db}
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
	if s.history == nil {
		return
	}
	if _, err := s.history.deleteExpired(now); err != nil {
		s.logJob(LogWarn, "", "CDK resolve history cleanup failed", err.Error())
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
	ID          string         `json:"id"`
	JobID       string         `json:"job_id"`
	Kind        ResourceKind   `json:"kind"`
	Mode        string         `json:"mode"`
	Input       string         `json:"input"`
	ResultCount int            `json:"result_count"`
	Batch       *BatchProgress `json:"batch,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	CompletedAt time.Time      `json:"completed_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
}

type resolveHistoryDetail struct {
	resolveHistorySummary
	Results []JobResult `json:"results"`
}

type storedResolveHistory struct {
	resolveHistoryDetail
	cdkCode string
}

func (s *resolveHistoryStore) saveJob(job *Job) error {
	if s == nil || s.db == nil || job == nil {
		return nil
	}
	if strings.TrimSpace(job.CDKCode) == "" || strings.TrimSpace(job.ParentID) != "" || job.Status != JobCompleted {
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
			(id, cdk_code, job_id, kind, mode, input, results_json, batch_json, created_at, completed_at, expires_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		job.ID,
		normalizeCode(job.CDKCode),
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
		`SELECT id, cdk_code, job_id, kind, mode, input, results_json, batch_json, created_at, completed_at, expires_at
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

func (s *resolveHistoryStore) get(cdkCode, id string, now time.Time) (resolveHistoryDetail, bool, error) {
	if s == nil || s.db == nil {
		return resolveHistoryDetail{}, false, nil
	}
	row := s.db.QueryRow(
		`SELECT id, cdk_code, job_id, kind, mode, input, results_json, batch_json, created_at, completed_at, expires_at
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

func (s *resolveHistoryStore) deleteExpired(now time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	res, err := s.db.Exec(`DELETE FROM cdk_resolve_history WHERE expires_at<=?`, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type resolveHistoryScanner interface {
	Scan(dest ...any) error
}

func scanResolveHistory(scanner resolveHistoryScanner) (storedResolveHistory, error) {
	var (
		id          string
		cdkCode     string
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
	if err := scanner.Scan(&id, &cdkCode, &jobID, &kind, &mode, &input, &resultsRaw, &batchRaw, &createdUnix, &doneUnix, &expiresUnix); err != nil {
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
		resolveHistoryDetail: resolveHistoryDetail{
			resolveHistorySummary: resolveHistorySummary{
				ID:          id,
				JobID:       jobID,
				Kind:        ResourceKind(kind),
				Mode:        mode,
				Input:       input,
				ResultCount: len(results),
				Batch:       batch,
				CreatedAt:   time.Unix(createdUnix, 0),
				CompletedAt: time.Unix(doneUnix, 0),
				ExpiresAt:   time.Unix(expiresUnix, 0),
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
