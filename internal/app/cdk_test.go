package app

import (
	"errors"
	"net/http"
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

func TestCDKCreateAndList(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, err := store.createBatch(3, 5*bytesPerGB, 30, now)
	if err != nil {
		t.Fatalf("createBatch: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("expected 3 CDKs, got %d", len(created))
	}
	seen := map[string]bool{}
	for _, c := range created {
		if seen[c.Code] {
			t.Fatalf("duplicate code generated: %s", c.Code)
		}
		seen[c.Code] = true
		if c.RemainingBytes != 5*bytesPerGB {
			t.Fatalf("expected remaining 5G, got %d", c.RemainingBytes)
		}
	}

	list, err := store.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected list of 3, got %d", len(list))
	}
}

func TestCDKHasTrafficGuards(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 2*bytesPerGB, 30, now)
	code := created[0].Code

	if _, err := store.hasTraffic(code, now); err != nil {
		t.Fatalf("hasTraffic on fresh CDK: %v", err)
	}

	// Drain it, then it must report exhausted.
	if err := store.charge(code, 2*bytesPerGB); err != nil {
		t.Fatalf("charge: %v", err)
	}
	if _, err := store.hasTraffic(code, now); err != errCDKExhausted {
		t.Fatalf("expected errCDKExhausted, got %v", err)
	}

	// Unknown code.
	if _, err := store.hasTraffic("NOPE-NOPE-NOPE-NOPE", now); err != errCDKNotFound {
		t.Fatalf("expected errCDKNotFound, got %v", err)
	}

	// Expired code.
	exp, _ := store.createBatch(1, 5*bytesPerGB, 1, now)
	later := now.Add(48 * time.Hour)
	if _, err := store.hasTraffic(exp[0].Code, later); err != errCDKExpired {
		t.Fatalf("expected errCDKExpired, got %v", err)
	}
}

func TestCDKCharge(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 5*bytesPerGB, 30, now)
	code := created[0].Code

	if err := store.charge(code, 2*bytesPerGB); err != nil {
		t.Fatalf("charge: %v", err)
	}
	c, _, _ := store.get(code)
	if c.RemainingBytes != 3*bytesPerGB || c.UsedBytes != 2*bytesPerGB {
		t.Fatalf("after charge 2G: remaining=%d used=%d", c.RemainingBytes, c.UsedBytes)
	}

	// Overdraw clamps remaining at zero but still accumulates used.
	if err := store.charge(code, 10*bytesPerGB); err != nil {
		t.Fatalf("overdraw charge: %v", err)
	}
	c, _, _ = store.get(code)
	if c.RemainingBytes != 0 {
		t.Fatalf("remaining should clamp at 0, got %d", c.RemainingBytes)
	}
	if c.UsedBytes != 12*bytesPerGB {
		t.Fatalf("used should accumulate to 12G, got %d", c.UsedBytes)
	}
}

func TestCDKChargeIfEnoughDoesNotOverdrawConcurrently(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)
	created, _ := store.createBatch(1, 1*bytesPerGB, 30, now)
	code := created[0].Code
	chargeSize := int64(700 * 1024 * 1024)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.chargeIfEnough(code, chargeSize, now)
		}()
	}
	wg.Wait()
	close(errs)

	successes := 0
	overdraws := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if _, ok := errAsOverdraw(err); ok {
			overdraws++
			continue
		}
		t.Fatalf("unexpected charge error: %v", err)
	}
	if successes != 1 || overdraws != 1 {
		t.Fatalf("successes=%d overdraws=%d, want 1 each", successes, overdraws)
	}

	c, _, _ := store.get(code)
	if c.RemainingBytes != bytesPerGB-chargeSize || c.UsedBytes != chargeSize {
		t.Fatalf("remaining=%d used=%d, want remaining=%d used=%d", c.RemainingBytes, c.UsedBytes, bytesPerGB-chargeSize, chargeSize)
	}
}

func TestCDKUpdateAndDelete(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	store := newTestCDKStore(t)

	created, _ := store.createBatch(1, 5*bytesPerGB, 10, now)
	code := created[0].Code

	updated, ok, err := store.update(code, 20*bytesPerGB, 60, now)
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if updated.RemainingBytes != 20*bytesPerGB {
		t.Fatalf("expected remaining 20G, got %d", updated.RemainingBytes)
	}
	wantExpiry := now.Add(60 * 24 * time.Hour).Unix()
	if updated.ExpiresAt != wantExpiry {
		t.Fatalf("expected expiry %d, got %d", wantExpiry, updated.ExpiresAt)
	}

	deleted, err := store.delete(code)
	if err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
	if _, ok, _ := store.get(code); ok {
		t.Fatalf("expected code to be gone after delete")
	}
}

func TestCDKViewDaysLeft(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := CDK{
		Code:           "AAAA-BBBB-CCCC-DDDD",
		RemainingBytes: 5 * bytesPerGB,
		ExpiresAt:      now.Add(72 * time.Hour).Unix(),
		CreatedAt:      now.Unix(),
	}
	v := toCDKView(c, now)
	if v.Expired {
		t.Fatal("should not be expired")
	}
	if v.DaysLeft != 3 {
		t.Fatalf("expected 3 days left, got %d", v.DaysLeft)
	}
	if v.RemainingBytes != 5*bytesPerGB || v.RemainingLabel != "5 GB" {
		t.Fatalf("unexpected remaining view: bytes=%d label=%q", v.RemainingBytes, v.RemainingLabel)
	}

	expiredView := toCDKView(CDK{ExpiresAt: now.Add(-time.Hour).Unix()}, now)
	if !expiredView.Expired || expiredView.DaysLeft != 0 {
		t.Fatalf("expected expired view, got %+v", expiredView)
	}
}

// TestUserJobViewHidesAccountInfo locks in the security boundary: a CDK user
// must never receive the raw error/message, which embed PikPak account
// usernames (the "all accounts failed: <email>: ..." leak).
func TestUserJobViewHidesAccountInfo(t *testing.T) {
	leak := "all PikPak accounts failed: alice@passinbox.com: record not found; bob@passinbox.com: record not found"

	failed := toUserJobView(&Job{
		ID:      "job1",
		Status:  JobFailed,
		Stage:   StageFailed,
		Message: "starting with alice@passinbox.com",
		Error:   leak,
	})
	if failed.Error != genericUserJobError {
		t.Fatalf("failed job error should be generic, got %q", failed.Error)
	}
	if failed.Message != "" {
		t.Fatalf("failed job should expose no message, got %q", failed.Message)
	}
	if contains(failed.Error, "@") || contains(failed.Message, "@") {
		t.Fatalf("account email leaked: error=%q message=%q", failed.Error, failed.Message)
	}

	// A running job's internal "starting with <username>" message must not pass
	// through either.
	running := toUserJobView(&Job{
		ID:      "job2",
		Status:  JobRunning,
		Stage:   StageTransfer,
		Message: "starting with bob@passinbox.com",
	})
	if contains(running.Message, "@") || contains(running.Message, "bob") {
		t.Fatalf("running message leaked account info: %q", running.Message)
	}
	if running.Error != "" {
		t.Fatalf("running job should have no error, got %q", running.Error)
	}

	// Defense in depth: an error set without a failed status is still scrubbed.
	weird := toUserJobView(&Job{ID: "job3", Status: JobRunning, Error: leak})
	if weird.Error != genericUserJobError {
		t.Fatalf("stray error not scrubbed, got %q", weird.Error)
	}

	badResource := toUserJobView(&Job{ID: "job4", Status: JobFailed, Error: badResourceParseUserError})
	if badResource.Error != badResourceParseUserError {
		t.Fatalf("bad resource error should be user-visible, got %q", badResource.Error)
	}
	if contains(badResource.Error, "@") {
		t.Fatalf("bad resource error leaked account info: %q", badResource.Error)
	}

	overdraw := toUserJobView(&Job{ID: "job5", Status: JobFailed, Error: (errCDKOverdraw{size: 2 * bytesPerGB, remaining: bytesPerGB}).Error()})
	if overdraw.Error == genericUserJobError {
		t.Fatalf("CDK overdraw should be a safe user-visible error, got generic")
	}
	if contains(overdraw.Error, "@") {
		t.Fatalf("CDK overdraw leaked account info: %q", overdraw.Error)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// TestApplyItemsSelectionRejectsOverdraw locks in the traffic gate: a CDK user
// picking result files whose SUMMED size exceeds the CDK's remaining traffic
// must be refused at selection time with a 403, not silently absorbed at charge
// time. A selection that fits is accepted.
func TestApplyItemsSelectionRejectsOverdraw(t *testing.T) {
	now := time.Now()
	store := newTestCDKStore(t)
	created, _ := store.createBatch(1, 1*bytesPerGB, 30, now)
	code := created[0].Code

	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{cdk: store, jobs: newJobStore(10), resolver: noopResolver}

	items := []DownloadItem{
		{ID: "a", Name: "a.bin", Size: itoa64(512 * 1024 * 1024)}, // 0.5G
		{ID: "b", Name: "b.bin", Size: itoa64(512 * 1024 * 1024)}, // 0.5G
		{ID: "c", Name: "c.bin", Size: itoa64(512 * 1024 * 1024)}, // 0.5G
	}
	mkJob := func(id string) string {
		job := &Job{ID: id, Status: JobSelectionRequired, Stage: StageResultSelection, CDKCode: code, Items: items}
		s.jobs.create(job)
		return id
	}

	// 1.5G summed > 1G remaining → refused.
	if _, status, msg := s.applyItemsSelection(mkJob("job-over"), []string{"a", "b", "c"}); status != 403 {
		t.Fatalf("expected 403 for oversized batch, got status=%d msg=%q", status, msg)
	}

	// 1.0G summed == 1G remaining → allowed (not strictly greater).
	if _, status, msg := s.applyItemsSelection(mkJob("job-fit"), []string{"a", "b"}); status != 0 {
		t.Fatalf("expected fitting batch to succeed, got status=%d msg=%q", status, msg)
	}

	// Empty selection is a bad request.
	if _, status, _ := s.applyItemsSelection(mkJob("job-empty"), []string{"  "}); status != 400 {
		t.Fatalf("expected 400 for empty selection, got status=%d", status)
	}

	// Unknown item id is rejected.
	if _, status, _ := s.applyItemsSelection(mkJob("job-unknown"), []string{"a", "zzz"}); status != 400 {
		t.Fatalf("expected 400 for unknown item, got status=%d", status)
	}
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

	if _, status, msg := s.applyItemSelection("source-job", "unknown"); status != 400 {
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
	if _, status, msg := s.applyItemSelection("source-job", "known"); status != 409 {
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
	if updated.Share == nil || updated.Share.SelectedID != "b" || len(updated.Share.SelectedIDs) != 2 || updated.Share.SelectedIDs[0] != "b" || updated.Share.SelectedIDs[1] != "a" {
		t.Fatalf("selected share ids = %+v, want [b a]", updated.Share)
	}
	if len(updated.Share.SelectedItems) != 2 || updated.Share.SelectedItems[0].Path != "root/b.bin" || updated.Share.SelectedItems[1].Path != "root/a.bin" {
		t.Fatalf("selected source items = %+v, want paths [root/b.bin root/a.bin]", updated.Share.SelectedItems)
	}
	if !updated.ResolveSelected {
		t.Fatal("source multi selection should mark the job to resolve selected files directly")
	}
	if len(updated.Items) != 0 {
		t.Fatalf("selection items should be cleared, got %+v", updated.Items)
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

func TestApplyItemsSelectionRejectsSourceOverdraw(t *testing.T) {
	now := time.Now()
	store := newTestCDKStore(t)
	created, _ := store.createBatch(1, 1*bytesPerGB, 30, now)
	code := created[0].Code

	noopResolver := newResolveQueue(time.Second, time.Second, 1, func(string, error) {})
	s := &Server{cdk: store, jobs: newJobStore(10), resolver: noopResolver}
	s.jobs.create(&Job{
		ID:      "source-job",
		Status:  JobSelectionRequired,
		Stage:   StageSourceSelection,
		Share:   &ShareState{ShareID: "share"},
		CDKCode: code,
		Items: []DownloadItem{
			{ID: "a", Name: "a.bin", Size: itoa64(512 * 1024 * 1024)},
			{ID: "b", Name: "b.bin", Size: itoa64(512 * 1024 * 1024)},
			{ID: "c", Name: "c.bin", Size: itoa64(512 * 1024 * 1024)},
		},
	})

	if _, status, msg := s.applyItemsSelection("source-job", []string{"a", "b", "c"}); status != http.StatusForbidden {
		t.Fatalf("expected 403 for oversized source selection, got status=%d msg=%q", status, msg)
	}
}

// TestResultForToken locks in proxy-link routing for batch jobs: each resolved
// file's token must select its own result, across both the single Result and
// the multi-file Results slice.
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

// TestCDKOverdrawError covers the single-file backstop used by finishWithItems:
// it returns a typed errCDKOverdraw only when a CDK job's file exceeds remaining
// traffic, and never blocks non-CDK jobs.
func TestCDKOverdrawError(t *testing.T) {
	now := time.Now()
	store := newTestCDKStore(t)
	created, _ := store.createBatch(1, 1*bytesPerGB, 30, now)
	code := created[0].Code

	s := &Server{cdk: store, jobs: newJobStore(10)}

	cdkJob := &Job{ID: "cdk-job", CDKCode: code}
	s.jobs.create(cdkJob)
	if err := s.cdkOverdrawError(cdkJob.ID, DownloadItem{Size: itoa64(2 * bytesPerGB)}); err == nil {
		t.Fatal("expected overdraw error for 2G file against 1G CDK")
	} else if _, ok := errAsOverdraw(err); !ok {
		t.Fatalf("expected errCDKOverdraw, got %T", err)
	}
	if err := s.cdkOverdrawError(cdkJob.ID, DownloadItem{Size: itoa64(512 * 1024 * 1024)}); err != nil {
		t.Fatalf("0.5G file should fit 1G CDK, got %v", err)
	}

	// A job with no CDK is never gated.
	plainJob := &Job{ID: "plain-job"}
	s.jobs.create(plainJob)
	if err := s.cdkOverdrawError(plainJob.ID, DownloadItem{Size: itoa64(99 * bytesPerGB)}); err != nil {
		t.Fatalf("non-CDK job must never be gated, got %v", err)
	}
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}

func errAsOverdraw(err error) (errCDKOverdraw, bool) {
	var o errCDKOverdraw
	ok := errors.As(err, &o)
	return o, ok
}
