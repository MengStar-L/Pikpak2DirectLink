package app

import (
	"testing"
	"time"
)

func TestJobStorePersistsBeforePublishingAndReadsThrough(t *testing.T) {
	durable, _, now := newTestSQLJobStore(t)
	first := newJobStore(10, durable)
	job := &Job{
		ID: "durable-runtime", Kind: ResourceMagnet, Mode: "direct",
		Status: JobQueued, Stage: StageTransfer, CreatedAt: now, UpdatedAt: now,
	}
	if err := first.create(job); err != nil {
		t.Fatalf("create: %v", err)
	}

	second := newJobStore(10, durable)
	loaded, ok, err := second.getWithError(job.ID)
	if err != nil || !ok || loaded.Status != JobQueued {
		t.Fatalf("read-through = %#v, ok %v, err %v", loaded, ok, err)
	}
	if _, err := second.update(job.ID, func(job *Job) error {
		job.Status = JobCompleted
		job.Stage = StageComplete
		job.ChargedBytes = 123
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	third := newJobStore(10, durable)
	completed, ok, err := third.getWithError(job.ID)
	if err != nil || !ok || completed.Status != JobCompleted || completed.ChargedBytes != 123 {
		t.Fatalf("persisted update = %#v, ok %v, err %v", completed, ok, err)
	}
}

func TestJobStoreDoesNotPublishFailedDurableCreate(t *testing.T) {
	durable, db, now := newTestSQLJobStore(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	store := newJobStore(10, durable)
	err := store.create(&Job{
		ID: "not-published", Kind: ResourceMagnet, Mode: "direct",
		Status: JobQueued, Stage: StageTransfer, CreatedAt: now, UpdatedAt: now,
	})
	if err == nil {
		t.Fatal("create succeeded with a closed database")
	}
	store.mu.RLock()
	_, published := store.jobs["not-published"]
	store.mu.RUnlock()
	if published {
		t.Fatal("job was published to memory after durable create failed")
	}
}

func TestJobStoreStopsServingExpiredTerminalDetailsFromCache(t *testing.T) {
	durable, _, now := newTestSQLJobStore(t)
	store := newJobStore(10, durable)
	job := &Job{
		ID: "cached-expiry", Kind: ResourceMagnet, Mode: "proxy",
		Status: JobCompleted, Stage: StageComplete,
		Result: &JobResult{ProxyToken: "private-token"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.create(job); err != nil {
		t.Fatalf("create: %v", err)
	}
	durable.now = func() time.Time { return now.Add(resolveJobDetailsTTL) }
	if _, ok, err := store.getWithError(job.ID); err != nil || ok {
		t.Fatalf("expired cache read = ok %v, err %v; want false, nil", ok, err)
	}
	record, ok, err := durable.getRecord(job.ID, resolveJobOwnerAdmin, resolveJobOwnerAdmin, now.Add(resolveJobDetailsTTL))
	if err != nil || !ok || record.DetailsAvailable {
		t.Fatalf("expired metadata = %#v, ok %v, err %v", record, ok, err)
	}
}
