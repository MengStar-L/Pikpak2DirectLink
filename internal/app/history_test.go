package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestResolveHistoryMigration(t *testing.T) {
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if has, err := columnExists(db, "cdk_resolve_history", "results_json"); err != nil || !has {
		t.Fatalf("history table missing results_json: has=%v err=%v", has, err)
	}
	if has := sqliteObjectExists(t, db, "index", "idx_cdk_resolve_history_expires"); !has {
		t.Fatal("history expiry index missing")
	}
}

func TestResolveHistorySaveListsOnlySuccessfulCDKJobs(t *testing.T) {
	db := newHistoryTestDB(t)
	store := newResolveHistoryStore(db)
	now := time.Unix(1_700_000_000, 0)

	success := historyTestJob("job-success", "CDK-AAAA", now)
	success.OriginalInput = "  magnet:?xt=urn:btih:raw  "
	if err := store.saveJob(success); err != nil {
		t.Fatalf("save success: %v", err)
	}
	if err := store.saveJob(&Job{
		ID:        "job-failed",
		Kind:      ResourceMagnet,
		Mode:      "direct",
		Input:     "magnet:?xt=failed",
		CDKCode:   "CDK-AAAA",
		Status:    JobFailed,
		Result:    success.Result,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save failed job: %v", err)
	}
	if err := store.saveJob(&Job{
		ID:        "job-empty",
		Kind:      ResourceMagnet,
		Mode:      "direct",
		Input:     "magnet:?xt=empty",
		CDKCode:   "CDK-AAAA",
		Status:    JobCompleted,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save empty job: %v", err)
	}
	if err := store.saveJob(&Job{
		ID:        "job-admin",
		Kind:      ResourceMagnet,
		Mode:      "direct",
		Input:     "magnet:?xt=admin",
		Status:    JobCompleted,
		Result:    success.Result,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save admin job: %v", err)
	}
	if err := store.saveJob(&Job{
		ID:        "job-child",
		Kind:      ResourceMagnet,
		Mode:      "direct",
		Input:     "magnet:?xt=child",
		CDKCode:   "CDK-AAAA",
		ParentID:  "parent",
		Status:    JobCompleted,
		Result:    success.Result,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save child job: %v", err)
	}

	list, err := store.list("CDK-AAAA", now)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("history len = %d, want 1: %+v", len(list), list)
	}
	if list[0].ID != "job-success" || list[0].ResultCount != 1 {
		t.Fatalf("summary = %+v, want job-success with one result", list[0])
	}
	if list[0].Input != "magnet:?xt=urn:btih:raw" {
		t.Fatalf("input = %q, want trimmed original input", list[0].Input)
	}
}

func TestResolveHistoryPartialBatchOmitsFailureDetails(t *testing.T) {
	db := newHistoryTestDB(t)
	store := newResolveHistoryStore(db)
	now := time.Unix(1_700_000_000, 0)
	job := historyTestJob("job-batch", "CDK-BBBB", now)
	job.Kind = ResourceBatch
	job.Result = nil
	job.Results = []JobResult{historyTestResult("file-a")}
	job.Batch = &BatchProgress{
		Total:     2,
		Succeeded: 1,
		Failed:    1,
		Failures:  []BatchFailure{{Label: "link 2", Error: "secret failure"}},
	}

	if err := store.saveJob(job); err != nil {
		t.Fatalf("save batch: %v", err)
	}
	detail, ok, err := store.get("CDK-BBBB", "job-batch", now)
	if err != nil || !ok {
		t.Fatalf("get batch: ok=%v err=%v", ok, err)
	}
	if detail.ResultCount != 1 || len(detail.Results) != 1 {
		t.Fatalf("detail result count = %d/%d, want 1", detail.ResultCount, len(detail.Results))
	}
	if detail.Batch == nil || detail.Batch.Total != 2 || detail.Batch.Succeeded != 1 || detail.Batch.Failed != 1 {
		t.Fatalf("batch summary = %+v, want 1/2 partial success", detail.Batch)
	}
	if len(detail.Batch.Failures) != 0 {
		t.Fatalf("failure details should not be saved, got %+v", detail.Batch.Failures)
	}
}

func TestResolveHistoryExpiryCleanup(t *testing.T) {
	db := newHistoryTestDB(t)
	store := newResolveHistoryStore(db)
	now := time.Unix(1_700_000_000, 0)
	old := historyTestJob("job-old", "CDK-CCCC", now.Add(-4*time.Hour))
	fresh := historyTestJob("job-fresh", "CDK-CCCC", now)
	if err := store.saveJob(old); err != nil {
		t.Fatalf("save old: %v", err)
	}
	if err := store.saveJob(fresh); err != nil {
		t.Fatalf("save fresh: %v", err)
	}

	list, err := store.list("CDK-CCCC", now)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "job-fresh" {
		t.Fatalf("visible history = %+v, want only fresh", list)
	}
	deleted, err := store.deleteExpired(now)
	if err != nil {
		t.Fatalf("deleteExpired: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
}

func TestUserHistoryHandlersScopeToCurrentUser(t *testing.T) {
	db := newHistoryTestDB(t)
	users := newUserStore(db)
	history := newResolveHistoryStore(db)
	now := time.Now().Truncate(time.Second)
	firstUser, err := users.createEmailUser("first@example.com", "secret-pass", now)
	if err != nil {
		t.Fatalf("create first user: %v", err)
	}
	secondUser, err := users.createEmailUser("second@example.com", "secret-pass", now)
	if err != nil {
		t.Fatalf("create second user: %v", err)
	}
	token, err := users.createSession(firstUser.ID, now)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	firstJob := historyTestUserJob("job-first", firstUser.ID, now)
	secondJob := historyTestUserJob("job-second", secondUser.ID, now)
	if err := history.saveJob(firstJob); err != nil {
		t.Fatalf("save first: %v", err)
	}
	if err := history.saveJob(secondJob); err != nil {
		t.Fatalf("save second: %v", err)
	}

	s := &Server{users: users, history: history, nowFunc: func() time.Time { return now }}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/u/history", nil)
	req.AddCookie(&http.Cookie{Name: userSessionCookieName, Value: token})
	s.handleUserHistoryList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		History []resolveHistorySummary `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(payload.History) != 1 || payload.History[0].ID != "job-first" {
		t.Fatalf("history payload = %+v, want only first CDK job", payload.History)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/u/history/job-second", nil)
	req.SetPathValue("id", "job-second")
	req.AddCookie(&http.Cookie{Name: userSessionCookieName, Value: token})
	s.handleUserHistoryGet(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-CDK detail status = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/u/history/job-first", nil)
	req.SetPathValue("id", "job-first")
	req.AddCookie(&http.Cookie{Name: userSessionCookieName, Value: token})
	s.handleUserHistoryGet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("own detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	var detail resolveHistoryDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ID != "job-first" || len(detail.Results) != 1 {
		t.Fatalf("detail = %+v, want first job with result", detail)
	}
}

func historyTestUserJob(id, userID string, completedAt time.Time) *Job {
	job := historyTestJob(id, "", completedAt)
	job.UserID = userID
	return job
}

func newHistoryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func historyTestJob(id, cdkCode string, completedAt time.Time) *Job {
	result := historyTestResult("file-" + id)
	return &Job{
		ID:        id,
		Kind:      ResourceMagnet,
		Mode:      "direct",
		Input:     "magnet:?xt=urn:btih:" + id,
		CDKCode:   cdkCode,
		Status:    JobCompleted,
		Result:    &result,
		CreatedAt: completedAt.Add(-2 * time.Minute),
		UpdatedAt: completedAt,
	}
}

func historyTestResult(id string) JobResult {
	return JobResult{
		File: DownloadItem{
			ID:   id,
			Name: id + ".bin",
			Kind: "file",
			Size: "1024",
		},
		URL:        "https://cdn.example/" + id,
		DirectURL:  "https://cdn.example/" + id,
		ProxyURL:   "https://proxy.example/" + id,
		ProxyToken: "token-" + id,
	}
}

func sqliteObjectExists(t *testing.T, db *sql.DB, typ, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?`, typ, name).Scan(&count); err != nil {
		t.Fatalf("sqlite_master lookup: %v", err)
	}
	return count > 0
}
