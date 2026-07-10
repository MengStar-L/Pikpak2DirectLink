package app

import (
	"errors"
	"testing"
	"time"
)

func newDurableFailureTestServer(t *testing.T) (*Server, *jobStore) {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cipher := newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef"))
	durable := newSQLJobStore(db, cipher)
	jobs := newJobStore(20, durable)
	s := &Server{
		db:        db,
		jobs:      jobs,
		logs:      newLogStore(20),
		batches:   make(map[string]*batchState),
		restartCh: make(chan struct{}),
	}
	return s, jobs
}

func TestBatchChildDoneKeepsCoordinatorWhenParentPersistenceFails(t *testing.T) {
	s, jobs := newDurableFailureTestServer(t)
	now := time.Now().UTC()
	parent := &Job{
		ID: "parent", Kind: ResourceBatch, Status: JobRunning, Stage: StageTransfer,
		Batch: &BatchProgress{Total: 1}, CreatedAt: now, UpdatedAt: now,
	}
	child := &Job{
		ID: "child", ParentID: parent.ID, Status: JobCompleted, Stage: StageComplete,
		Results:   []JobResult{{File: DownloadItem{ID: "file", Name: "file.bin"}}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := jobs.create(parent); err != nil {
		t.Fatal(err)
	}
	if err := jobs.create(child); err != nil {
		t.Fatal(err)
	}
	s.registerBatch(&batchState{parentID: parent.ID, total: 1})
	if _, err := s.db.Exec(`CREATE TRIGGER fail_batch_parent BEFORE UPDATE ON resolve_jobs
		WHEN OLD.id='parent' BEGIN SELECT RAISE(ABORT, 'parent write failed'); END`); err != nil {
		t.Fatal(err)
	}

	s.batchChildDone(parent.ID, child.ID, "child")
	if s.batchByID(parent.ID) == nil {
		t.Fatal("batch coordinator was removed after parent persistence failure")
	}
	stored, ok := jobs.get(parent.ID)
	if !ok || stored.Status != JobRunning {
		t.Fatalf("parent state = %+v, want running", stored)
	}
	select {
	case <-s.RestartRequested():
	default:
		t.Fatal("parent persistence failure did not request restart")
	}
}

func TestFailJobRequestsRestartWhenTerminalPersistenceFails(t *testing.T) {
	s, jobs := newDurableFailureTestServer(t)
	now := time.Now().UTC()
	job := &Job{ID: "job", Status: JobRunning, Stage: StageTransfer, CreatedAt: now, UpdatedAt: now}
	if err := jobs.create(job); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_terminal BEFORE UPDATE ON resolve_jobs
		WHEN OLD.id='job' BEGIN SELECT RAISE(ABORT, 'terminal write failed'); END`); err != nil {
		t.Fatal(err)
	}

	s.failJob(job.ID, errors.New("resolve failed"))
	stored, ok := jobs.get(job.ID)
	if !ok || stored.Status != JobRunning {
		t.Fatalf("job state = %+v, want original running state", stored)
	}
	select {
	case <-s.RestartRequested():
	default:
		t.Fatal("terminal persistence failure did not request restart")
	}
}

func TestExpireSelectionJobsMarksTimedOutJobsTerminal(t *testing.T) {
	jobs := newJobStore(10)
	now := time.Now().UTC()
	job := &Job{
		ID: "selection", UserID: "user", Status: JobSelectionRequired, Stage: StageResultSelection,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := jobs.create(job); err != nil {
		t.Fatal(err)
	}
	s := &Server{jobs: jobs, logs: newLogStore(10), restartCh: make(chan struct{})}
	s.expireSelectionJobs(now)
	stored, _ := jobs.get(job.ID)
	if stored.Status != JobFailed || stored.FailureCode != "selection_timeout" {
		t.Fatalf("expired selection state = %s/%s", stored.Status, stored.FailureCode)
	}
}

func TestExpiredSelectionSnapshotDoesNotFailResumedJob(t *testing.T) {
	jobs := newJobStore(10)
	now := time.Now().UTC()
	cutoff := now.Add(-selectionRequiredTimeout)
	job := &Job{
		ID: "selection-race", UserID: "user", Status: JobSelectionRequired, Stage: StageResultSelection,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}
	if err := jobs.create(job); err != nil {
		t.Fatal(err)
	}
	ids := jobs.expiredSelectionIDs(cutoff)
	if len(ids) != 1 || ids[0] != job.ID {
		t.Fatalf("expired selection snapshot = %v", ids)
	}
	if _, err := jobs.update(job.ID, func(current *Job) error {
		current.Status = JobQueued
		current.Message = "queued"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{jobs: jobs, logs: newLogStore(10), restartCh: make(chan struct{})}
	s.expireSelectionJob(ids[0], cutoff)
	stored, _ := jobs.get(job.ID)
	if stored.Status != JobQueued || stored.FailureCode != "" {
		t.Fatalf("resumed job was overwritten by stale expiry snapshot: %s/%s", stored.Status, stored.FailureCode)
	}
}
