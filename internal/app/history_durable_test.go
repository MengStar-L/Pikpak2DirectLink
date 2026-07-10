package app

import (
	"testing"
	"time"
)

func TestDurableHistoryRetainsMetadataAfterDetailExpiry(t *testing.T) {
	durable, db, now := newTestSQLJobStore(t)
	history := newResolveHistoryStore(db, durable)
	job := &Job{
		ID: "history-durable", Kind: ResourceMagnet, Mode: "direct",
		Input: "magnet:?xt=urn:btih:private", UserID: "user-history",
		Status: JobFailed, Stage: StageFailed, FailureCode: "service_restart",
		Error: resolveJobRestartError, ChargedBytes: 456,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	if err := durable.create(job); err != nil {
		t.Fatalf("create durable history job: %v", err)
	}

	items, err := history.listByUser(job.UserID, now.Add(time.Minute))
	if err != nil || len(items) != 1 {
		t.Fatalf("history before expiry = %#v, err %v", items, err)
	}
	if !items[0].DetailsAvailable || items[0].FailureCode != "service_restart" || items[0].ChargedBytes != 456 {
		t.Fatalf("history metadata before expiry = %#v", items[0])
	}

	expiredAt := now.Add(resolveJobDetailsTTL)
	items, err = history.listByUser(job.UserID, expiredAt)
	if err != nil || len(items) != 1 {
		t.Fatalf("history after detail expiry = %#v, err %v", items, err)
	}
	if items[0].DetailsAvailable || items[0].Input != "" || items[0].FailureCode != "service_restart" {
		t.Fatalf("scrubbed history metadata = %#v", items[0])
	}
	if _, ok, err := history.getByUser(job.UserID, job.ID, expiredAt); !ok || err != errResolveJobDetailsExpired {
		t.Fatalf("expired history detail = ok %v, err %v", ok, err)
	}

	items, err = history.listByUser(job.UserID, now.Add(resolveJobRecordTTL))
	if err != nil || len(items) != 0 {
		t.Fatalf("history after record expiry = %#v, err %v", items, err)
	}
}
