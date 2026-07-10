package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestCDKStore(t *testing.T) *cdkStore {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return newCDKStore(db)
}

func TestCDKAdminHandlersExposeCredentialSnapshot(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()
	now := time.Now()
	created, err := srv.cdk.createBatch(2, 3*bytesPerGB, 45, true, now)
	if err != nil {
		t.Fatalf("create CDKs: %v", err)
	}
	adminToken, err := srv.authSessions.create()
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	adminCookie := &http.Cookie{Name: "session", Value: adminToken}

	req := httptest.NewRequest(http.MethodGet, "/api/cdks", nil)
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var listPayload struct {
		CDKs []cdkView `json:"cdks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listPayload.CDKs) != 2 || listPayload.CDKs[0].GrantBytes != 3*bytesPerGB || listPayload.CDKs[0].Status != "unredeemed" {
		t.Fatalf("credential list = %+v", listPayload.CDKs)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw list: %v", err)
	}
	first := raw["cdks"].([]any)[0].(map[string]any)
	for _, legacyField := range []string{"remaining_bytes", "used_bytes", "expires_at", "days_left", "expired"} {
		if _, exists := first[legacyField]; exists {
			t.Fatalf("legacy field %q leaked in CDK response: %v", legacyField, first)
		}
	}

	patchReq := jsonRequest(http.MethodPatch, "/api/cdks/"+created[0].Code, `{"traffic_gb":4,"days":60,"allow_proxy":false}`)
	patchReq.AddCookie(adminCookie)
	patchRec := httptest.NewRecorder()
	handler.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}
	var patched cdkView
	if err := json.Unmarshal(patchRec.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patched.GrantBytes != 4*bytesPerGB || patched.DurationDays != 60 || patched.AllowProxy {
		t.Fatalf("patched credential = %+v", patched)
	}

	if _, err := srv.db.Exec(
		`UPDATE cdks SET redeemed_by_user_id='usr_redeemed', redeemed_at=? WHERE code=?`,
		now.Unix(), created[0].Code,
	); err != nil {
		t.Fatalf("mark redeemed: %v", err)
	}
	patchReq = jsonRequest(http.MethodPatch, "/api/cdks/"+created[0].Code, `{"traffic_gb":1,"days":10,"allow_proxy":true}`)
	patchReq.AddCookie(adminCookie)
	patchRec = httptest.NewRecorder()
	handler.ServeHTTP(patchRec, patchReq)
	if patchRec.Code != http.StatusConflict {
		t.Fatalf("patch redeemed status=%d body=%s", patchRec.Code, patchRec.Body.String())
	}
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/cdks/"+created[0].Code, nil)
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusConflict {
		t.Fatalf("delete redeemed status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}

	for attempt := 0; attempt < 2; attempt++ {
		deleteReq = httptest.NewRequest(http.MethodDelete, "/api/cdks/"+created[1].Code, nil)
		deleteReq.AddCookie(adminCookie)
		deleteRec = httptest.NewRecorder()
		handler.ServeHTTP(deleteRec, deleteReq)
		if deleteRec.Code != http.StatusOK {
			t.Fatalf("revoke attempt %d status=%d body=%s", attempt+1, deleteRec.Code, deleteRec.Body.String())
		}
	}
}

func TestHandleUserRedeemCDKCreatesSubscription(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()
	now := time.Now()
	created, err := srv.cdk.createBatch(1, 3*bytesPerGB, 45, true, now)
	if err != nil {
		t.Fatalf("create cdk: %v", err)
	}
	user := createTestUserSession(t, srv)

	req := jsonRequest(http.MethodPost, "/api/u/cdks/redeem", `{"code":"`+created[0].Code+`"}`)
	req.AddCookie(user)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem status = %d body=%s", rec.Code, rec.Body.String())
	}

	var payload userStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Quota.TotalRemainingBytes != 3*bytesPerGB || len(payload.Subscriptions) != 1 {
		t.Fatalf("quota/subscriptions = %+v/%+v", payload.Quota, payload.Subscriptions)
	}
	if payload.Subscriptions[0].DaysLeft != 45 || !payload.Subscriptions[0].AllowProxy {
		t.Fatalf("subscription = %+v, want 45 proxy days", payload.Subscriptions[0])
	}
	credential, ok, err := srv.cdk.get(created[0].Code)
	if err != nil || !ok || credential.RedeemedAt == 0 || credential.RedeemedByUserID == "" {
		t.Fatalf("redeemed cdk = %+v ok=%v err=%v", credential, ok, err)
	}
	if credential.GrantBytes != 3*bytesPerGB || credential.DurationDays != 45 {
		t.Fatalf("credential snapshot changed during redemption: %+v", credential)
	}
}

func TestHandleUserRedeemCDKRejectsRepeat(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()
	created, err := srv.cdk.createBatch(1, bytesPerGB, 30, true, time.Now())
	if err != nil {
		t.Fatalf("create cdk: %v", err)
	}
	user := createTestUserSession(t, srv)

	for i, want := range []int{http.StatusOK, http.StatusConflict} {
		req := jsonRequest(http.MethodPost, "/api/u/cdks/redeem", `{"code":"`+created[0].Code+`"}`)
		req.AddCookie(user)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("redeem #%d status = %d body=%s, want %d", i+1, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestConcurrentUserRedeemCDKSucceedsExactlyOnce(t *testing.T) {
	srv := newTestServer(t)
	now := time.Now()
	created, err := srv.cdk.createBatch(1, bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("create cdk: %v", err)
	}
	const userID = "usr_concurrent_redeem"
	if _, err := srv.db.Exec(
		`INSERT INTO users(id, email, display_name, created_at, updated_at) VALUES(?,?,?,?,?)`,
		userID, "concurrent@example.com", "Concurrent", now.Unix(), now.Unix(),
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	const attempts = 12
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := srv.users.redeemCDK(userID, created[0].Code, now)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded := 0
	conflicted := 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, errVoucherRedeemed):
			conflicted++
		default:
			t.Fatalf("unexpected redeem error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != attempts-1 {
		t.Fatalf("redeem results = success:%d conflict:%d", succeeded, conflicted)
	}
	var subscriptions int
	if err := srv.db.QueryRow(
		`SELECT COUNT(*) FROM user_subscriptions WHERE user_id=? AND source_cdk_code=?`,
		userID, created[0].Code,
	).Scan(&subscriptions); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if subscriptions != 1 {
		t.Fatalf("subscriptions = %d, want 1", subscriptions)
	}
}

func TestUserRedeemCDKRejectsUnsafeStoredDurationAtomically(t *testing.T) {
	srv := newTestServer(t)
	now := time.Now()
	created, err := srv.cdk.createBatch(1, bytesPerGB, 30, true, now)
	if err != nil {
		t.Fatalf("create cdk: %v", err)
	}
	if _, err := srv.db.Exec(`UPDATE cdks SET duration_days=? WHERE code=?`, maxCDKDurationDays+1, created[0].Code); err != nil {
		t.Fatalf("set unsafe duration: %v", err)
	}
	const userID = "usr_unsafe_duration"
	if _, err := srv.db.Exec(
		`INSERT INTO users(id, email, display_name, created_at, updated_at) VALUES(?,?,?,?,?)`,
		userID, "unsafe-duration@example.com", "Unsafe Duration", now.Unix(), now.Unix(),
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	if _, err := srv.users.redeemCDK(userID, created[0].Code, now); !errors.Is(err, errVoucherDuration) {
		t.Fatalf("redeem error = %v, want errVoucherDuration", err)
	}
	var subscriptions, redeemed int
	if err := srv.db.QueryRow(
		`SELECT COUNT(*) FROM user_subscriptions WHERE source_cdk_code=?`,
		created[0].Code,
	).Scan(&subscriptions); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if err := srv.db.QueryRow(
		`SELECT COUNT(*) FROM cdks WHERE code=? AND redeemed_at IS NOT NULL`,
		created[0].Code,
	).Scan(&redeemed); err != nil {
		t.Fatalf("read redemption audit: %v", err)
	}
	if subscriptions != 0 || redeemed != 0 {
		t.Fatalf("unsafe redemption changed state: subscriptions=%d redeemed=%d", subscriptions, redeemed)
	}
}

func TestCDKHandlersRejectOverflowingGrantAndDuration(t *testing.T) {
	srv := newTestServer(t)
	created, err := srv.cdk.createBatch(1, bytesPerGB, 30, true, time.Now())
	if err != nil {
		t.Fatalf("create cdk: %v", err)
	}
	tests := []struct {
		name   string
		method string
		body   string
		update bool
	}{
		{name: "create grant overflow", method: http.MethodPost, body: `{"count":1,"traffic_gb":8589934592,"days":30,"allow_proxy":true}`},
		{name: "create duration overflow", method: http.MethodPost, body: `{"count":1,"traffic_gb":1,"days":106752,"allow_proxy":true}`},
		{name: "update grant overflow", method: http.MethodPatch, body: `{"traffic_gb":8589934592,"days":30,"allow_proxy":true}`, update: true},
		{name: "update duration overflow", method: http.MethodPatch, body: `{"traffic_gb":1,"days":106752,"allow_proxy":true}`, update: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := "/api/cdks"
			if tt.update {
				target += "/" + created[0].Code
			}
			req := jsonRequest(tt.method, target, tt.body)
			rec := httptest.NewRecorder()
			if tt.update {
				req.SetPathValue("code", created[0].Code)
				srv.handleUpdateCDK(rec, req)
			} else {
				srv.handleCreateCDKs(rec, req)
			}
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleUserRedeemCDKRequiresUserSession(t *testing.T) {
	srv := newTestServer(t)
	handler := srv.Handler()
	created, err := srv.cdk.createBatch(1, bytesPerGB, 30, true, time.Now())
	if err != nil {
		t.Fatalf("create cdk: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/u/cdks/redeem", `{"code":"`+created[0].Code+`"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("redeem without session status = %d body=%s, want 401", rec.Code, rec.Body.String())
	}
}

func createTestUserSession(t *testing.T, srv *Server) *http.Cookie {
	t.Helper()
	user, err := srv.users.createEmailUser("user@example.com", "secret-pass", time.Now())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token, err := srv.users.createSession(user.ID, time.Now())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: userSessionCookieName, Value: token}
}

func TestCDKViewUsesAuditStatus(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	credential := CDK{
		Code:             "AAAA-BBBB-CCCC-DDDD",
		GrantBytes:       5 * bytesPerGB,
		DurationDays:     30,
		AllowProxy:       true,
		CreatedAt:        now.Unix(),
		RedeemedByUserID: "usr_1",
		RedeemedAt:       now.Add(time.Hour).Unix(),
		RevokedAt:        now.Add(2 * time.Hour).Unix(),
	}
	view := toCDKView(credential)
	if view.Status != "redeemed" {
		t.Fatalf("status=%q, want redeemed to take precedence", view.Status)
	}
	if view.GrantBytes != 5*bytesPerGB || view.GrantLabel != "5 GB" || view.RedeemedAt == "" || view.RevokedAt == "" {
		t.Fatalf("credential view = %+v", view)
	}
}

func TestUserJobViewHidesAccountInfo(t *testing.T) {
	leak := "all PikPak accounts failed: alice@passinbox.com: record not found; bob@passinbox.com: record not found"

	failed := toUserJobView(&Job{
		ID:      "job1",
		Status:  JobFailed,
		Stage:   StageFailed,
		Message: "starting with alice@passinbox.com",
		Error:   leak,
	})
	if failed.Error != genericUserJobError || failed.Message != "" {
		t.Fatalf("failed job leaked details: error=%q message=%q", failed.Error, failed.Message)
	}
	if contains(failed.Error, "@") || contains(failed.Message, "@") {
		t.Fatalf("account email leaked: error=%q message=%q", failed.Error, failed.Message)
	}

	running := toUserJobView(&Job{
		ID:      "job2",
		Status:  JobRunning,
		Stage:   StageTransfer,
		Message: "starting with bob@passinbox.com",
	})
	if contains(running.Message, "@") || contains(running.Message, "bob") || running.Error != "" {
		t.Fatalf("running job leaked details: error=%q message=%q", running.Error, running.Message)
	}

	weird := toUserJobView(&Job{ID: "job3", Status: JobRunning, Error: leak})
	if weird.Error != genericUserJobError {
		t.Fatalf("stray error not scrubbed, got %q", weird.Error)
	}
	badResource := toUserJobView(&Job{ID: "job4", Status: JobFailed, Error: badResourceParseUserError})
	if badResource.Error != badResourceParseUserError || contains(badResource.Error, "@") {
		t.Fatalf("bad resource error = %q", badResource.Error)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func TestApplyItemSelectionRejectsUnknownSourceItem(t *testing.T) {
	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{jobs: newJobStore(10), resolver: noopResolver}
	s.jobs.create(&Job{
		ID:     "source-job",
		Status: JobSelectionRequired,
		Stage:  StageSourceSelection,
		Share:  &ShareState{ShareID: "share"},
		Items:  []DownloadItem{{ID: "known", Name: "known"}},
	})

	if _, status, msg := s.applyItemSelection("source-job", "unknown"); status != http.StatusBadRequest {
		t.Fatalf("status=%d msg=%q, want 400", status, msg)
	}
}

func TestApplyItemSelectionDuplicateSubmitOnlyQueuesOnce(t *testing.T) {
	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{jobs: newJobStore(10), resolver: noopResolver}
	s.jobs.create(&Job{
		ID:     "source-job",
		Status: JobSelectionRequired,
		Stage:  StageSourceSelection,
		Share:  &ShareState{ShareID: "share"},
		Items:  []DownloadItem{{ID: "known", Name: "known", Path: "folder/known"}},
	})

	updated, status, msg := s.applyItemSelection("source-job", "known")
	if status != 0 {
		t.Fatalf("first selection status=%d msg=%q, want success", status, msg)
	}
	if updated.Share == nil || len(updated.Share.SelectedItems) != 1 || updated.Share.SelectedItems[0].Path != "folder/known" {
		t.Fatalf("selected source item = %+v, want path folder/known", updated.Share)
	}
	if _, status, msg := s.applyItemSelection("source-job", "known"); status != http.StatusConflict {
		t.Fatalf("second selection status=%d msg=%q, want 409", status, msg)
	}
	if queued := noopResolver.queuedIDs(); len(queued) != 1 || queued[0] != "source-job" {
		t.Fatalf("queued IDs = %v, want [source-job]", queued)
	}
}

func TestApplyItemsSelectionAcceptsMultipleSourceItems(t *testing.T) {
	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{jobs: newJobStore(10), resolver: noopResolver}
	s.jobs.create(&Job{
		ID:     "source-job",
		Status: JobSelectionRequired,
		Stage:  StageSourceSelection,
		Share:  &ShareState{ShareID: "share"},
		Items: []DownloadItem{
			{ID: "a", Name: "a.bin", Path: "root/a.bin", Size: itoa64(256 * 1024 * 1024)},
			{ID: "b", Name: "b.bin", Path: "root/b.bin", Size: itoa64(256 * 1024 * 1024)},
			{ID: "c", Name: "c.bin", Path: "root/c.bin", Size: itoa64(256 * 1024 * 1024)},
		},
	})

	updated, status, msg := s.applyItemsSelection("source-job", []string{"b", "a", "b"})
	if status != 0 {
		t.Fatalf("source multi selection status=%d msg=%q, want success", status, msg)
	}
	if updated.Status != JobQueued || updated.Stage != StageTransfer {
		t.Fatalf("updated job status/stage = %s/%s, want queued/transfer", updated.Status, updated.Stage)
	}
	if updated.Share == nil || len(updated.Share.SelectedIDs) != 2 || updated.Share.SelectedIDs[0] != "b" || updated.Share.SelectedIDs[1] != "a" {
		t.Fatalf("selected share ids = %+v, want [b a]", updated.Share)
	}
	if len(updated.Share.SelectedItems) != 2 || updated.Share.SelectedItems[0].Path != "root/b.bin" || updated.Share.SelectedItems[1].Path != "root/a.bin" {
		t.Fatalf("selected source items = %+v", updated.Share.SelectedItems)
	}
	if !updated.ResolveSelected || len(updated.Items) != 0 {
		t.Fatalf("selection state = resolve:%v items:%+v", updated.ResolveSelected, updated.Items)
	}
	if queued := noopResolver.queuedIDs(); len(queued) != 1 || queued[0] != "source-job" {
		t.Fatalf("queued IDs = %v, want [source-job]", queued)
	}
}

func TestApplyItemsSelectionRejectsTooManyItems(t *testing.T) {
	ids := make([]string, maxSelectedFilesPerResolve+1)
	for i := range ids {
		ids[i] = "file-" + strconv.Itoa(i)
	}
	s := &Server{jobs: newJobStore(10)}
	if _, status, msg := s.applyItemsSelection("job-any", ids); status != http.StatusBadRequest {
		t.Fatalf("status=%d msg=%q, want 400", status, msg)
	}
}

func TestResultForToken(t *testing.T) {
	job := &Job{
		Result:  &JobResult{ProxyToken: "single-tok", File: DownloadItem{ID: "s"}},
		Results: []JobResult{{ProxyToken: "tok-a", File: DownloadItem{ID: "a"}}, {ProxyToken: "tok-b", File: DownloadItem{ID: "b"}}},
	}
	if r := job.resultForToken("tok-b"); r == nil || r.File.ID != "b" {
		t.Fatalf("expected result b, got %+v", r)
	}
	if r := job.resultForToken("single-tok"); r == nil || r.File.ID != "s" {
		t.Fatalf("expected single result, got %+v", r)
	}
	if r := job.resultForToken("nope"); r != nil {
		t.Fatalf("expected nil for unknown token, got %+v", r)
	}
	if r := job.resultForToken(""); r != nil {
		t.Fatalf("expected nil for empty token, got %+v", r)
	}
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}
