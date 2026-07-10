package app

import (
	"errors"
	"testing"
	"time"
)

func newQuotaReservationTestStore(t *testing.T) (*userStore, time.Time) {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Unix(1_700_000_000, 0)
	if _, err := db.Exec(
		`INSERT INTO users(id, email, created_at, updated_at) VALUES('usr_quota', 'quota-reservation@example.com', ?, ?)`,
		now.Unix(), now.Unix(),
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return newUserStore(db), now
}

func addQuotaTestSubscription(t *testing.T, store *userStore, id string, remaining int64, expiresAt time.Time, allowProxy bool, createdAt time.Time) {
	t.Helper()
	if _, err := store.db.Exec(
		`INSERT INTO user_subscriptions
		 (id, user_id, remaining_bytes, used_bytes, expires_at, created_at, allow_proxy)
		 VALUES(?, 'usr_quota', ?, 0, ?, ?, ?)`,
		id, remaining, expiresAt.Unix(), createdAt.Unix(), b2i(allowProxy),
	); err != nil {
		t.Fatalf("insert subscription %s: %v", id, err)
	}
}

func TestQuotaReservationUsesExpiryOrderAndSettlesExactlyOnce(t *testing.T) {
	store, now := newQuotaReservationTestStore(t)
	addQuotaTestSubscription(t, store, "sub_early", 100, now.Add(time.Hour), true, now)
	addQuotaTestSubscription(t, store, "sub_late", 100, now.Add(2*time.Hour), true, now)

	if err := store.reserveQuota("job_settle", "usr_quota", 150, false, now); err != nil {
		t.Fatalf("reserveQuota: %v", err)
	}
	if err := store.reserveQuota("job_settle", "usr_quota", 150, false, now); err != nil {
		t.Fatalf("idempotent reserveQuota: %v", err)
	}
	if err := store.reserveQuota("job_settle", "usr_quota", 140, false, now); !errors.Is(err, errQuotaReservationConflict) {
		t.Fatalf("conflicting reserveQuota error = %v", err)
	}

	var earlyRemaining, lateRemaining int64
	if err := store.db.QueryRow(`SELECT remaining_bytes FROM user_subscriptions WHERE id='sub_early'`).Scan(&earlyRemaining); err != nil {
		t.Fatalf("read early subscription: %v", err)
	}
	if err := store.db.QueryRow(`SELECT remaining_bytes FROM user_subscriptions WHERE id='sub_late'`).Scan(&lateRemaining); err != nil {
		t.Fatalf("read late subscription: %v", err)
	}
	if earlyRemaining != 0 || lateRemaining != 50 {
		t.Fatalf("reserved remaining = %d/%d, want 0/50", earlyRemaining, lateRemaining)
	}

	settled, err := store.settleQuotaReservation("job_settle")
	if err != nil || settled != 150 {
		t.Fatalf("settleQuotaReservation = %d, %v; want 150, nil", settled, err)
	}
	settled, err = store.settleQuotaReservation("job_settle")
	if err != nil || settled != 0 {
		t.Fatalf("second settleQuotaReservation = %d, %v; want 0, nil", settled, err)
	}

	var earlyUsed, lateUsed int64
	if err := store.db.QueryRow(`SELECT used_bytes FROM user_subscriptions WHERE id='sub_early'`).Scan(&earlyUsed); err != nil {
		t.Fatalf("read early used bytes: %v", err)
	}
	if err := store.db.QueryRow(`SELECT used_bytes FROM user_subscriptions WHERE id='sub_late'`).Scan(&lateUsed); err != nil {
		t.Fatalf("read late used bytes: %v", err)
	}
	if earlyUsed != 100 || lateUsed != 50 {
		t.Fatalf("settled used = %d/%d, want 100/50", earlyUsed, lateUsed)
	}
}

func TestQuotaReservationFiltersProxyBucketsAndReleases(t *testing.T) {
	store, now := newQuotaReservationTestStore(t)
	addQuotaTestSubscription(t, store, "sub_direct", 100, now.Add(time.Hour), false, now)
	addQuotaTestSubscription(t, store, "sub_proxy", 100, now.Add(2*time.Hour), true, now)

	if err := store.reserveQuota("job_release", "usr_quota", 80, true, now); err != nil {
		t.Fatalf("reserveQuota: %v", err)
	}
	var directRemaining, proxyRemaining int64
	if err := store.db.QueryRow(`SELECT remaining_bytes FROM user_subscriptions WHERE id='sub_direct'`).Scan(&directRemaining); err != nil {
		t.Fatalf("read direct subscription: %v", err)
	}
	if err := store.db.QueryRow(`SELECT remaining_bytes FROM user_subscriptions WHERE id='sub_proxy'`).Scan(&proxyRemaining); err != nil {
		t.Fatalf("read proxy subscription: %v", err)
	}
	if directRemaining != 100 || proxyRemaining != 20 {
		t.Fatalf("proxy reservation remaining = %d/%d, want 100/20", directRemaining, proxyRemaining)
	}

	released, err := store.releaseQuotaReservation("job_release")
	if err != nil || released != 80 {
		t.Fatalf("releaseQuotaReservation = %d, %v; want 80, nil", released, err)
	}
	released, err = store.releaseQuotaReservation("job_release")
	if err != nil || released != 0 {
		t.Fatalf("second releaseQuotaReservation = %d, %v; want 0, nil", released, err)
	}
	if err := store.db.QueryRow(`SELECT remaining_bytes FROM user_subscriptions WHERE id='sub_proxy'`).Scan(&proxyRemaining); err != nil {
		t.Fatalf("read restored proxy subscription: %v", err)
	}
	if proxyRemaining != 100 {
		t.Fatalf("released proxy remaining = %d, want 100", proxyRemaining)
	}
}

func TestQuotaReservationInsufficientQuotaRollsBack(t *testing.T) {
	store, now := newQuotaReservationTestStore(t)
	addQuotaTestSubscription(t, store, "sub_only", 100, now.Add(time.Hour), true, now)

	err := store.reserveQuota("job_overdraw", "usr_quota", 101, false, now)
	var overdraw errUserQuotaOverdraw
	if !errors.As(err, &overdraw) {
		t.Fatalf("reserveQuota error = %v, want errUserQuotaOverdraw", err)
	}
	var remaining, reservations int64
	if err := store.db.QueryRow(`SELECT remaining_bytes FROM user_subscriptions WHERE id='sub_only'`).Scan(&remaining); err != nil {
		t.Fatalf("read remaining: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM user_quota_reservations`).Scan(&reservations); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if remaining != 100 || reservations != 0 {
		t.Fatalf("failed reservation changed state: remaining=%d reservations=%d", remaining, reservations)
	}
}

func TestReconcileQuotaReservationsUsesDurableJobState(t *testing.T) {
	store, now := newQuotaReservationTestStore(t)
	addQuotaTestSubscription(t, store, "sub_shared", 1_000, now.Add(24*time.Hour), true, now)
	jobs := []struct {
		id              string
		status          JobStatus
		recordExpiresAt time.Time
		chargedBytes    int64
	}{
		{id: "job_active", status: JobQueued, recordExpiresAt: now.Add(time.Hour)},
		{id: "job_completed", status: JobCompleted, recordExpiresAt: now.Add(time.Hour), chargedBytes: 10},
		{id: "job_expired", status: JobQueued, recordExpiresAt: now.Add(-time.Second)},
		{id: "job_failed", status: JobFailed, recordExpiresAt: now.Add(time.Hour)},
	}
	for _, job := range jobs {
		if _, err := store.db.Exec(
			`INSERT INTO resolve_jobs
			 (id, owner_type, owner_id, kind, mode, status, charged_bytes, record_expires_at, created_at, updated_at)
			 VALUES(?, 'user', 'usr_quota', 'magnet', 'direct', ?, ?, ?, ?, ?)`,
			job.id, string(job.status), job.chargedBytes, job.recordExpiresAt.Unix(), now.Unix(), now.Unix(),
		); err != nil {
			t.Fatalf("insert job %s: %v", job.id, err)
		}
	}
	for _, jobID := range []string{"job_active", "job_completed", "job_expired", "job_failed", "job_orphan"} {
		if err := store.reserveQuota(jobID, "usr_quota", 10, false, now); err != nil {
			t.Fatalf("reserve %s: %v", jobID, err)
		}
	}

	reconciled, err := store.reconcileQuotaReservations(now)
	if err != nil {
		t.Fatalf("reconcileQuotaReservations: %v", err)
	}
	if reconciled.SettledJobs != 1 || reconciled.SettledBytes != 10 || reconciled.ReleasedJobs != 3 || reconciled.ReleasedBytes != 30 {
		t.Fatalf("reconcile result = %+v", reconciled)
	}
	var remaining, used int64
	if err := store.db.QueryRow(`SELECT remaining_bytes, used_bytes FROM user_subscriptions WHERE id='sub_shared'`).Scan(&remaining, &used); err != nil {
		t.Fatalf("read reconciled subscription: %v", err)
	}
	if remaining != 980 || used != 10 {
		t.Fatalf("reconciled subscription = remaining:%d used:%d, want 980/10", remaining, used)
	}
	var reservationJobID string
	if err := store.db.QueryRow(`SELECT job_id FROM user_quota_reservations`).Scan(&reservationJobID); err != nil {
		t.Fatalf("read remaining reservation: %v", err)
	}
	if reservationJobID != "job_active" {
		t.Fatalf("remaining reservation = %q, want job_active", reservationJobID)
	}

	second, err := store.reconcileQuotaReservations(now)
	if err != nil || second != (quotaReservationReconcileResult{}) {
		t.Fatalf("second reconciliation = %+v, %v", second, err)
	}
}

func TestReconcileQuotaReservationsRejectsCompletedChargeMismatch(t *testing.T) {
	store, now := newQuotaReservationTestStore(t)
	addQuotaTestSubscription(t, store, "sub_mismatch", 100, now.Add(time.Hour), true, now)
	if _, err := store.db.Exec(
		`INSERT INTO resolve_jobs
		 (id, owner_type, owner_id, kind, mode, status, charged_bytes, record_expires_at, created_at, updated_at)
		 VALUES('job_mismatch', 'user', 'usr_quota', 'magnet', 'direct', 'completed', 5, ?, ?, ?)`,
		now.Add(time.Hour).Unix(), now.Unix(), now.Unix(),
	); err != nil {
		t.Fatalf("insert completed job: %v", err)
	}
	if err := store.reserveQuota("job_mismatch", "usr_quota", 10, false, now); err != nil {
		t.Fatalf("reserveQuota: %v", err)
	}

	if _, err := store.reconcileQuotaReservations(now); err == nil {
		t.Fatal("reconciliation unexpectedly accepted mismatched charged bytes")
	}
	var remaining, used, reservations int64
	if err := store.db.QueryRow(`SELECT remaining_bytes, used_bytes FROM user_subscriptions WHERE id='sub_mismatch'`).Scan(&remaining, &used); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM user_quota_reservations WHERE job_id='job_mismatch'`).Scan(&reservations); err != nil {
		t.Fatalf("count reservation: %v", err)
	}
	if remaining != 90 || used != 0 || reservations != 1 {
		t.Fatalf("mismatch reconciliation was not atomic: remaining=%d used=%d reservations=%d", remaining, used, reservations)
	}
}
