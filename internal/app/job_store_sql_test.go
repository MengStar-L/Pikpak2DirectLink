package app

import (
	"bytes"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSQLJobStoreRoundTripAndOwnerQueries(t *testing.T) {
	store, db, now := newTestSQLJobStore(t)
	job := fullSQLJobFixture(now)

	if err := store.create(job, resolveJobWriteMetadata{
		FailureCode: "initial_code", ErrorCategory: "source", ChargedBytes: 42,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	created, ok, err := store.get(job.ID, resolveJobOwnerUser, job.UserID, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("get created job = ok %v, err %v", ok, err)
	}
	wantCreated := cloneJob(job)
	wantCreated.FailureCode = "initial_code"
	wantCreated.DetailsAvailable = true
	wantCreated.ChargedBytes = 42
	if !reflect.DeepEqual(created, wantCreated) {
		t.Fatalf("created job mismatch\n got: %#v\nwant: %#v", created, wantCreated)
	}

	updated := cloneJob(job)
	updated.Message = "updated message"
	updated.UpdatedAt = now.Add(time.Hour)
	updated.Results[0].DirectURL = "https://download.example/updated"
	if err := store.upsert(updated, resolveJobWriteMetadata{ChargedBytes: 99}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, ok, err := store.get(job.ID, resolveJobOwnerUser, job.UserID, now.Add(time.Hour))
	if err != nil || !ok {
		t.Fatalf("get updated job = ok %v, err %v", ok, err)
	}
	wantUpdated := cloneJob(updated)
	wantUpdated.DetailsAvailable = true
	wantUpdated.ChargedBytes = 99
	if !reflect.DeepEqual(got, wantUpdated) {
		t.Fatalf("updated job mismatch\n got: %#v\nwant: %#v", got, wantUpdated)
	}

	jobs, err := store.listByUser(job.UserID, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("list by user: %v", err)
	}
	if len(jobs) != 1 || !reflect.DeepEqual(jobs[0], wantUpdated) {
		t.Fatalf("list by user = %#v; want updated job", jobs)
	}

	if _, ok, err := store.get(job.ID, resolveJobOwnerUser, "another-user", now.Add(time.Hour)); err != nil || ok {
		t.Fatalf("wrong-owner get = ok %v, err %v; want false, nil", ok, err)
	}
	if _, ok, err := store.get(job.ID, resolveJobOwnerCDK, "some-cdk", now.Add(time.Hour)); err != nil || ok {
		t.Fatalf("wrong owner type get = ok %v, err %v; want false, nil", ok, err)
	}

	var (
		ownerType   string
		ownerID     string
		phase       string
		resultCount int
		charged     int64
	)
	if err := db.QueryRow(`SELECT owner_type, owner_id, phase, result_count, charged_bytes
		FROM resolve_jobs WHERE id=?`, job.ID).Scan(&ownerType, &ownerID, &phase, &resultCount, &charged); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if ownerType != resolveJobOwnerUser || ownerID != job.UserID || phase != string(job.Stage) || resultCount != 3 || charged != 99 {
		t.Fatalf("metadata = %q, %q, %q, %d, %d", ownerType, ownerID, phase, resultCount, charged)
	}
}

func TestSQLJobStorePayloadIsEncrypted(t *testing.T) {
	store, db, now := newTestSQLJobStore(t)
	job := &Job{
		ID: "encrypted-job", Kind: ResourceShare, Mode: "proxy",
		Input:         "https://mypikpak.com/s/secret-share?pwd=hidden-passcode",
		OriginalInput: "raw-secret-input", PassCode: "hidden-passcode",
		Status: JobCompleted, Stage: StageComplete, UserID: "user-1",
		Result: &JobResult{
			DirectURL:  "https://origin.example/private-download-token",
			ProxyURL:   "https://service.example/proxy/encrypted-job?token=secret-proxy-token",
			ProxyToken: "secret-proxy-token", AccountID: "secret-account-id",
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.create(job); err != nil {
		t.Fatalf("create: %v", err)
	}

	var encrypted string
	if err := db.QueryRow(`SELECT payload_encrypted FROM resolve_jobs WHERE id=?`, job.ID).Scan(&encrypted); err != nil {
		t.Fatalf("read encrypted payload: %v", err)
	}
	for _, secret := range []string{
		job.Input, job.OriginalInput, job.PassCode, job.Result.DirectURL,
		job.Result.ProxyToken, job.Result.AccountID,
	} {
		if strings.Contains(encrypted, secret) {
			t.Fatalf("encrypted payload contains plaintext %q", secret)
		}
	}
	if !strings.HasPrefix(encrypted, secretEnvelopeVersion+".") {
		t.Fatalf("payload %q is not a secret envelope", encrypted)
	}
}

func TestSQLJobStoreAssignsStableChildIndexes(t *testing.T) {
	store, db, now := newTestSQLJobStore(t)
	parent := &Job{
		ID: "parent", Kind: ResourceBatch, Mode: "direct", Status: JobRunning,
		Stage: StageTransfer, UserID: "user-1", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.create(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	for _, id := range []string{"child-a", "child-b"} {
		child := &Job{
			ID: id, ParentID: parent.ID, Kind: ResourceMagnet, Mode: "direct",
			Status: JobQueued, Stage: StageTransfer, UserID: parent.UserID,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := store.create(child); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	assertChildIndex(t, db, "child-a", 0)
	assertChildIndex(t, db, "child-b", 1)

	childA, ok, err := store.get("child-a", resolveJobOwnerUser, parent.UserID, now)
	if err != nil || !ok {
		t.Fatalf("get child-a = ok %v, err %v", ok, err)
	}
	childA.Message = "still queued"
	if err := store.upsert(childA); err != nil {
		t.Fatalf("upsert child-a: %v", err)
	}
	assertChildIndex(t, db, "child-a", 0)
}

func TestSQLJobStoreMarksNonterminalJobsInterrupted(t *testing.T) {
	store, db, now := newTestSQLJobStore(t)
	statuses := []JobStatus{JobQueued, JobRunning, JobSelectionRequired, JobCompleted}
	for i, status := range statuses {
		job := &Job{
			ID: "restart-" + string(rune('a'+i)), Kind: ResourceMagnet,
			Mode: "direct", Status: status, Stage: StageTransfer,
			CreatedAt: now, UpdatedAt: now,
		}
		if status == JobCompleted {
			job.Stage = StageComplete
		}
		if err := store.create(job); err != nil {
			t.Fatalf("create %s: %v", job.ID, err)
		}
	}

	restartedAt := now.Add(time.Hour)
	count, err := store.markNonterminalInterrupted(restartedAt)
	if err != nil {
		t.Fatalf("mark interrupted: %v", err)
	}
	if count != 3 {
		t.Fatalf("mark interrupted count = %d; want 3", count)
	}

	for _, id := range []string{"restart-a", "restart-b", "restart-c"} {
		job, ok, err := store.get(id, resolveJobOwnerAdmin, "ignored", restartedAt)
		if err != nil || !ok {
			t.Fatalf("get %s = ok %v, err %v", id, ok, err)
		}
		if job.Status != JobFailed || job.Stage != StageFailed || job.Error != resolveJobRestartError || !job.UpdatedAt.Equal(restartedAt) {
			t.Fatalf("interrupted %s = status %q, stage %q, error %q, updated %v", id, job.Status, job.Stage, job.Error, job.UpdatedAt)
		}
		var failureCode string
		if err := db.QueryRow(`SELECT failure_code FROM resolve_jobs WHERE id=?`, id).Scan(&failureCode); err != nil {
			t.Fatalf("read failure code for %s: %v", id, err)
		}
		if failureCode != "service_restart" {
			t.Fatalf("failure code for %s = %q", id, failureCode)
		}
	}

	completed, ok, err := store.get("restart-d", resolveJobOwnerAdmin, "ignored", restartedAt)
	if err != nil || !ok || completed.Status != JobCompleted {
		t.Fatalf("completed job changed = %#v, ok %v, err %v", completed, ok, err)
	}
}

func TestSQLJobStoreScrubsDetailsAndDeletesRecords(t *testing.T) {
	store, db, now := newTestSQLJobStore(t)
	job := &Job{
		ID: "expiring", Kind: ResourceMagnet, Mode: "direct", Input: "secret magnet",
		Status: JobCompleted, Stage: StageComplete, UserID: "user-1",
		Result: &JobResult{ProxyToken: "expiring-token"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.create(job); err != nil {
		t.Fatalf("create: %v", err)
	}

	if count, err := store.scrubExpired(now.Add(resolveJobDetailsTTL - time.Second)); err != nil || count != 0 {
		t.Fatalf("early scrub = %d, %v; want 0, nil", count, err)
	}
	if count, err := store.scrubExpired(now.Add(resolveJobDetailsTTL)); err != nil || count != 1 {
		t.Fatalf("due scrub = %d, %v; want 1, nil", count, err)
	}
	if _, ok, err := store.get(job.ID, resolveJobOwnerUser, job.UserID, now.Add(resolveJobDetailsTTL)); err != nil || ok {
		t.Fatalf("get scrubbed details = ok %v, err %v; want false, nil", ok, err)
	}

	var (
		rowCount int
		payload  sql.NullString
	)
	if err := db.QueryRow(`SELECT COUNT(*), MAX(payload_encrypted) FROM resolve_jobs WHERE id=?`, job.ID).Scan(&rowCount, &payload); err != nil {
		t.Fatalf("read scrubbed record: %v", err)
	}
	if rowCount != 1 || payload.Valid {
		t.Fatalf("scrubbed row = count %d, payload %#v", rowCount, payload)
	}

	if count, err := store.deleteExpired(now.Add(resolveJobRecordTTL - time.Second)); err != nil || count != 0 {
		t.Fatalf("early delete = %d, %v; want 0, nil", count, err)
	}
	if count, err := store.deleteExpired(now.Add(resolveJobRecordTTL)); err != nil || count != 1 {
		t.Fatalf("due delete = %d, %v; want 1, nil", count, err)
	}
}

func TestSQLJobStorePreservesBatchResultAccountIDs(t *testing.T) {
	store, _, now := newTestSQLJobStore(t)
	job := &Job{
		ID: "batch-accounts", Kind: ResourceBatch, Mode: "proxy",
		Status: JobCompleted, Stage: StageComplete, UserID: "user-1",
		Results: []JobResult{
			{ProxyToken: "token-a", AccountID: "account-a"},
			{ProxyToken: "token-b", AccountID: "account-b"},
		},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.create(job); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, ok, err := store.get(job.ID, resolveJobOwnerUser, job.UserID, now)
	if err != nil || !ok {
		t.Fatalf("get = ok %v, err %v", ok, err)
	}
	if len(got.Results) != 2 || got.Results[0].AccountID != "account-a" || got.Results[1].AccountID != "account-b" {
		t.Fatalf("result account IDs = %#v", got.Results)
	}
}

func TestSQLJobStoreListsCDKJobsWithNormalizedCode(t *testing.T) {
	store, _, now := newTestSQLJobStore(t)
	job := &Job{
		ID: "cdk-job", Kind: ResourceMagnet, Mode: "direct", CDKCode: "  ab-cd  ",
		Status: JobCompleted, Stage: StageComplete, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.create(job); err != nil {
		t.Fatalf("create: %v", err)
	}
	jobs, err := store.listByCDK("AB-CD", now)
	if err != nil {
		t.Fatalf("list by CDK: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("CDK jobs = %#v", jobs)
	}
}

func TestSQLJobStoreRotatesPreviousKeyPayloads(t *testing.T) {
	store, db, now := newTestSQLJobStore(t)
	job := &Job{
		ID: "rotate-job", Kind: ResourceMagnet, Mode: "direct",
		Status: JobCompleted, Stage: StageComplete,
		Input: "magnet:?xt=urn:btih:secret", CreatedAt: now, UpdatedAt: now,
	}
	if err := store.create(job); err != nil {
		t.Fatalf("create with previous key: %v", err)
	}

	previousKey := bytes.Repeat([]byte{0x7a}, secretKeySize)
	currentKey := bytes.Repeat([]byte{0x4b}, secretKeySize)
	rotatingCipher, err := NewSecretCipher(encodeTestKey(currentKey), []string{encodeTestKey(previousKey)})
	if err != nil {
		t.Fatalf("construct rotating cipher: %v", err)
	}
	rotatingStore := newSQLJobStore(db, rotatingCipher)
	if err := rotatingStore.RotateSecrets(); err != nil {
		t.Fatalf("rotate secrets: %v", err)
	}

	var envelope string
	if err := db.QueryRow(`SELECT payload_encrypted FROM resolve_jobs WHERE id=?`, job.ID).Scan(&envelope); err != nil {
		t.Fatalf("read rotated payload: %v", err)
	}
	if needsRotation, err := rotatingCipher.NeedsRotation(envelope); err != nil || needsRotation {
		t.Fatalf("rotated payload needs rotation = %v, err %v", needsRotation, err)
	}

	currentOnly := newTestSecretCipher(t, currentKey)
	currentStore := newSQLJobStore(db, currentOnly)
	got, ok, err := currentStore.get(job.ID, resolveJobOwnerAdmin, resolveJobOwnerAdmin, now)
	if err != nil || !ok || got.Input != job.Input {
		t.Fatalf("read with current key = %#v, ok %v, err %v", got, ok, err)
	}
}

func newTestSQLJobStore(t *testing.T) (*sqlJobStore, *sql.DB, time.Time) {
	t.Helper()
	db, err := openDatabase(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x7a}, secretKeySize))
	now := time.Date(2026, 7, 10, 8, 30, 0, 0, time.UTC)
	store := newSQLJobStore(db, cipher)
	store.now = func() time.Time { return now }
	return store, db, now
}

func fullSQLJobFixture(now time.Time) *Job {
	return &Job{
		ID: "full-job", Kind: ResourceShare, Mode: "proxy",
		Input: "https://mypikpak.com/s/clean", OriginalInput: "https://mypikpak.com/s/clean?pwd=raw",
		PassCode: "pass-code", Status: JobCompleted, Stage: StageComplete,
		Message: "complete", Error: "non-sensitive failure detail", BaseURL: "https://service.example",
		FolderID: "folder-id", CDKCode: "CDK-IGNORED-FOR-USER", UserID: "user-1",
		ProxyAllowed: true, AccountID: "job-account",
		Share: &ShareState{
			ShareID: "share-id", TailID: "tail-id", PassCodeToken: "pass-code-token",
			SelectedID: "selected-id", SelectedIDs: []string{"selected-a", "selected-b"},
			SelectedItems: []DownloadItem{{ID: "selected-a", Name: "selected.bin", Path: "folder/selected.bin", Kind: "file", Size: "12"}},
		},
		Items:           []DownloadItem{{ID: "item-1", Name: "item.bin", Path: "item.bin", Kind: "file", MimeType: "application/octet-stream", Size: "42"}},
		AccountAttempts: []AccountAttempt{{AccountID: "attempt-account", Username: "account@example.com", Status: "failed", Error: "quota"}},
		Result: &JobResult{
			File: DownloadItem{ID: "single", Name: "single.bin", Path: "single.bin", Kind: "file"},
			URL:  "https://service.example/proxy/full-job", DirectURL: "https://download.example/single",
			ProxyURL: "https://service.example/proxy/full-job?token=single-token", ProxyToken: "single-token",
			ExpiresAt: "2026-07-10T10:30:00Z", AccountID: "single-account",
		},
		Results: []JobResult{
			{File: DownloadItem{ID: "a", Name: "a.bin"}, ProxyToken: "token-a", AccountID: "account-a"},
			{File: DownloadItem{ID: "b", Name: "b.bin"}, ProxyToken: "token-b", AccountID: "account-b"},
		},
		Warnings: []string{"warning one"}, QueueAhead: 3, TempAccountID: "temp-account",
		TempIDs: []string{"temp-a", "temp-b"}, ResolveAll: true, ResolveSelected: true,
		Batch:     &BatchProgress{Total: 3, Succeeded: 2, Failed: 1, Failures: []BatchFailure{{Label: "link 3", Error: "failed"}}},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
}

func assertChildIndex(t *testing.T, db *sql.DB, id string, want int64) {
	t.Helper()
	var got sql.NullInt64
	if err := db.QueryRow(`SELECT child_index FROM resolve_jobs WHERE id=?`, id).Scan(&got); err != nil {
		t.Fatalf("read child index for %s: %v", id, err)
	}
	if !got.Valid || got.Int64 != want {
		t.Fatalf("child index for %s = %#v; want %d", id, got, want)
	}
}
