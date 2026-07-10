package app

import (
	"errors"
	"testing"
	"time"
)

func TestFinalizeCompletedUserJobSettlesReservedQuotaAtomically(t *testing.T) {
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	db, err := openDatabase(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher := newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef"))
	accountStore := newAccountStore(db, cipher)
	accountRecord := accountRecord{
		ID: "account", Username: "account@example.com", Password: "password",
		Status: AccountAvailable, TrafficLimit: 1000, TrafficPeriod: monthKey(now),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := accountStore.Insert(accountRecord); err != nil {
		t.Fatal(err)
	}
	accounts, err := NewAccountPool(AccountPoolConfig{Store: accountStore})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,created_at,updated_at) VALUES('user','user@example.com',?,?)`, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_subscriptions(id,user_id,remaining_bytes,used_bytes,expires_at,created_at,allow_proxy)
		VALUES('subscription','user',500,0,?,?,1)`, now.Add(time.Hour).Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	users := newUserStore(db)
	durable := newSQLJobStore(db, cipher)
	jobs := newJobStore(20, durable)
	job := &Job{
		ID: "user-job", UserID: "user", Kind: ResourceMagnet, Mode: "direct",
		Status: JobRunning, Stage: StageTransfer, CreatedAt: now, UpdatedAt: now,
	}
	if err := jobs.create(job); err != nil {
		t.Fatal(err)
	}
	if err := users.reserveQuota(job.ID, job.UserID, 100, false, now); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		db: db, jobs: jobs, durableJobs: durable, users: users, accounts: accounts,
		accountStore: accountStore, tempCleanups: newProxyTempCleanupStore(db, cipher),
		nowFunc: func() time.Time { return now },
	}
	result := &JobResult{File: DownloadItem{ID: "file", Size: "100"}}
	if err := server.finalizeCompletedJob(job.ID, AccountRuntime{ID: accountRecord.ID}, result, nil, 100, "file"); err != nil {
		t.Fatalf("finalizeCompletedJob: %v", err)
	}

	var remaining, used, reservations int64
	if err := db.QueryRow(`SELECT remaining_bytes,used_bytes FROM user_subscriptions WHERE id='subscription'`).Scan(&remaining, &used); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_quota_reservations WHERE job_id=?`, job.ID).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if remaining != 400 || used != 100 || reservations != 0 {
		t.Fatalf("quota state = remaining:%d used:%d reservations:%d", remaining, used, reservations)
	}
}

func TestFinalizeCompletedUserJobRejectsInvalidatedReservationWithoutFallbackCharge(t *testing.T) {
	now := time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC)
	db, err := openDatabase(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher := newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef"))
	accountStore := newAccountStore(db, cipher)
	accountRecord := accountRecord{
		ID: "account-invalidated", Username: "account-invalidated@example.com", Password: "password",
		Status: AccountAvailable, TrafficLimit: 1000, TrafficPeriod: monthKey(now),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := accountStore.Insert(accountRecord); err != nil {
		t.Fatal(err)
	}
	accounts, err := NewAccountPool(AccountPoolConfig{Store: accountStore})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,email,created_at,updated_at) VALUES('user-invalidated','user-invalidated@example.com',?,?)`, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_subscriptions(id,user_id,remaining_bytes,used_bytes,expires_at,created_at,allow_proxy)
		VALUES('subscription-invalidated','user-invalidated',500,0,?,?,1)`, now.Add(time.Hour).Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	users := newUserStore(db)
	durable := newSQLJobStore(db, cipher)
	jobs := newJobStore(20, durable)
	job := &Job{
		ID: "user-job-invalidated", UserID: "user-invalidated", Kind: ResourceMagnet, Mode: "direct",
		Status: JobRunning, Stage: StageTransfer, CreatedAt: now, UpdatedAt: now,
	}
	if err := jobs.create(job); err != nil {
		t.Fatal(err)
	}
	if err := users.reserveQuota(job.ID, job.UserID, 100, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE user_quota_reservations SET quota_generation=? WHERE job_id=?`,
		invalidQuotaReservationGeneration, job.ID,
	); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		db: db, jobs: jobs, durableJobs: durable, users: users, accounts: accounts,
		accountStore: accountStore, tempCleanups: newProxyTempCleanupStore(db, cipher),
		nowFunc: func() time.Time { return now },
	}
	result := &JobResult{File: DownloadItem{ID: "file-invalidated", Size: "100"}}
	err = server.finalizeCompletedJob(job.ID, AccountRuntime{ID: accountRecord.ID}, result, nil, 100, "file-invalidated")
	if !errors.Is(err, errQuotaReservationInvalidated) || !errors.Is(err, errUserQuotaExhausted) {
		t.Fatalf("finalizeCompletedJob error = %v, want invalidated quota error", err)
	}

	stored, ok := jobs.get(job.ID)
	if !ok || stored.Status != JobRunning || stored.ChargedBytes != 0 {
		t.Fatalf("job after rejected finalization = %+v ok=%v", stored, ok)
	}
	var remaining, used, reservations int64
	if err := db.QueryRow(`SELECT remaining_bytes,used_bytes FROM user_subscriptions WHERE id='subscription-invalidated'`).Scan(&remaining, &used); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_quota_reservations WHERE job_id=?`, job.ID).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if remaining != 400 || used != 0 || reservations != 1 {
		t.Fatalf("invalidated finalization state = remaining:%d used:%d reservations:%d", remaining, used, reservations)
	}
	released, err := users.releaseQuotaReservation(job.ID)
	if err != nil || released != 0 {
		t.Fatalf("release invalidated reservation = %d, %v; want 0, nil", released, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_quota_reservations WHERE job_id=?`, job.ID).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 0 {
		t.Fatalf("invalidated reservations after cleanup = %d, want 0", reservations)
	}
}
