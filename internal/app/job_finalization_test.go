package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"pikpak2directlink/internal/pikpak"
)

func TestFinalizeCompletedJobCommitsAllLedgersAtomically(t *testing.T) {
	for _, failCleanup := range []bool{false, true} {
		name := "commit"
		if failCleanup {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 7, 10, 4, 5, 6, 0, time.UTC)
			db, err := openDatabase(t.TempDir() + "/app.db")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cipher := newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef"))
			accountStore := newAccountStore(db, cipher)
			account := accountRecord{
				ID: "account", Username: "account@example.com", Password: "password",
				Status: AccountAvailable, TrafficLimit: 1000, TrafficPeriod: monthKey(now),
				CreatedAt: now, UpdatedAt: now,
			}
			if err := accountStore.Insert(account); err != nil {
				t.Fatal(err)
			}
			accounts, err := NewAccountPool(AccountPoolConfig{Store: accountStore})
			if err != nil {
				t.Fatal(err)
			}
			cdk := newCDKStore(db)
			codes, err := cdk.createBatch(1, 500, 1, true, now)
			if err != nil {
				t.Fatal(err)
			}
			durable := newSQLJobStore(db, cipher)
			jobs := newJobStore(20, durable)
			job := &Job{
				ID: "atomic-job", Kind: ResourceMagnet, Mode: "proxy", Status: JobRunning,
				Stage: StageTransfer, CDKCode: codes[0].Code, AccountID: account.ID,
				TempAccountID: account.ID, TempIDs: []string{"temporary-file-id"},
				CreatedAt: now, UpdatedAt: now,
			}
			if err := jobs.create(job); err != nil {
				t.Fatal(err)
			}
			cleanups := newProxyTempCleanupStore(db, cipher)
			if err := cleanups.record(job.ID, account.ID, job.TempIDs, now.Add(4*time.Hour), now); err != nil {
				t.Fatal(err)
			}
			if failCleanup {
				if _, err := db.Exec("CREATE TRIGGER fail_cleanup BEFORE UPDATE ON proxy_temp_cleanups BEGIN SELECT RAISE(ABORT, 'cleanup write failed'); END"); err != nil {
					t.Fatal(err)
				}
			}
			server := &Server{
				db: db, jobs: jobs, durableJobs: durable, accounts: accounts,
				accountStore: accountStore, cdk: cdk, tempCleanups: cleanups,
				nowFunc: func() time.Time { return now },
			}
			result := &JobResult{File: DownloadItem{ID: "temporary-file-id", Size: "100"}}
			err = server.finalizeCompletedJob(job.ID, AccountRuntime{ID: account.ID}, result, nil, 100, result.File.ID)

			storedCDK, ok, getErr := cdk.get(codes[0].Code)
			if getErr != nil || !ok {
				t.Fatalf("get CDK: ok=%v err=%v", ok, getErr)
			}
			storedAccounts, listErr := accountStore.List()
			if listErr != nil {
				t.Fatal(listErr)
			}
			storedJob, ok, getErr := durable.getAny(job.ID, now)
			if getErr != nil || !ok {
				t.Fatalf("get durable job: ok=%v err=%v", ok, getErr)
			}
			var cleanupCount int
			if err := db.QueryRow("SELECT COUNT(*) FROM proxy_temp_cleanups").Scan(&cleanupCount); err != nil {
				t.Fatal(err)
			}

			if failCleanup {
				if err == nil {
					t.Fatal("finalization succeeded despite cleanup trigger")
				}
				if storedCDK.RemainingBytes != 500 || storedAccounts[0].TrafficUsed != 0 ||
					storedJob.Status != JobRunning || cleanupCount != 1 {
					t.Fatalf("partial commit: cdk=%d traffic=%d job=%s cleanups=%d",
						storedCDK.RemainingBytes, storedAccounts[0].TrafficUsed, storedJob.Status, cleanupCount)
				}
				return
			}
			if err != nil {
				t.Fatalf("finalizeCompletedJob: %v", err)
			}
			if storedCDK.RemainingBytes != 400 || storedAccounts[0].TrafficUsed != 100 ||
				storedJob.Status != JobCompleted || storedJob.ChargedBytes != 100 || cleanupCount != 1 {
				t.Fatalf("incomplete commit: cdk=%d traffic=%d job=%s charged=%d cleanups=%d",
					storedCDK.RemainingBytes, storedAccounts[0].TrafficUsed, storedJob.Status,
					storedJob.ChargedBytes, cleanupCount)
			}
			var encryptedIDs string
			if err := db.QueryRow("SELECT file_ids_json FROM proxy_temp_cleanups").Scan(&encryptedIDs); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(encryptedIDs, "temporary-file-id") {
				t.Fatal("cleanup ledger stored a plaintext file ID")
			}

			secondErr := server.finalizeCompletedJob(job.ID, AccountRuntime{ID: account.ID}, result, nil, 100, result.File.ID)
			if !errors.Is(secondErr, errJobAlreadyCompleted) {
				t.Fatalf("second finalization error = %v, want errJobAlreadyCompleted", secondErr)
			}
			storedCDK, _, _ = cdk.get(codes[0].Code)
			storedAccounts, err = accountStore.List()
			if err != nil {
				t.Fatal(err)
			}
			if storedCDK.RemainingBytes != 400 || storedAccounts[0].TrafficUsed != 100 {
				t.Fatalf("duplicate charge: cdk=%d traffic=%d", storedCDK.RemainingBytes, storedAccounts[0].TrafficUsed)
			}
		})
	}
}

func TestRecordTempResourcePersistsCleanupIntentAtomically(t *testing.T) {
	for _, failCleanup := range []bool{false, true} {
		name := "commit"
		if failCleanup {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 7, 10, 4, 5, 6, 0, time.UTC)
			db, err := openDatabase(t.TempDir() + "/app.db")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cipher := newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef"))
			durable := newSQLJobStore(db, cipher)
			jobs := newJobStore(20, durable)
			if err := jobs.create(&Job{
				ID: "temp-intent-job", Kind: ResourceMagnet, Status: JobRunning,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			cleanups := newProxyTempCleanupStore(db, cipher)
			if failCleanup {
				if _, err := db.Exec("CREATE TRIGGER fail_cleanup BEFORE INSERT ON proxy_temp_cleanups BEGIN SELECT RAISE(ABORT, 'cleanup write failed'); END"); err != nil {
					t.Fatal(err)
				}
			}
			server := &Server{
				db: db, jobs: jobs, tempCleanups: cleanups,
				nowFunc: func() time.Time { return now },
			}
			err = server.recordTempResource("temp-intent-job", "account", "temporary-file-id")

			storedJob, ok, getErr := durable.getAny("temp-intent-job", now)
			if getErr != nil || !ok {
				t.Fatalf("get durable job: ok=%v err=%v", ok, getErr)
			}
			var cleanupCount int
			if err := db.QueryRow("SELECT COUNT(*) FROM proxy_temp_cleanups").Scan(&cleanupCount); err != nil {
				t.Fatal(err)
			}
			if failCleanup {
				if err == nil {
					t.Fatal("temporary resource write succeeded despite cleanup trigger")
				}
				if storedJob.TempAccountID != "" || len(storedJob.TempIDs) != 0 || cleanupCount != 0 {
					t.Fatalf("partial commit: account=%q ids=%v cleanups=%d", storedJob.TempAccountID, storedJob.TempIDs, cleanupCount)
				}
				return
			}
			if err != nil {
				t.Fatalf("recordTempResource: %v", err)
			}
			if storedJob.TempAccountID != "account" || !sameStringSet(storedJob.TempIDs, []string{"temporary-file-id"}) || cleanupCount != 1 {
				t.Fatalf("incomplete commit: account=%q ids=%v cleanups=%d", storedJob.TempAccountID, storedJob.TempIDs, cleanupCount)
			}
			var encryptedIDs string
			if err := db.QueryRow("SELECT file_ids_json FROM proxy_temp_cleanups").Scan(&encryptedIDs); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(encryptedIDs, "temporary-file-id") {
				t.Fatal("cleanup intent stored a plaintext file ID")
			}
		})
	}
}

func TestCleanupFailureDetachesActiveAccountButKeepsLedger(t *testing.T) {
	now := time.Date(2026, 7, 10, 4, 5, 6, 0, time.UTC)
	db, err := openDatabase(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher := newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef"))
	durable := newSQLJobStore(db, cipher)
	jobs := newJobStore(20, durable)
	if err := jobs.create(&Job{
		ID: "retry-job", Kind: ResourceMagnet, Status: JobRunning,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	cleanups := newProxyTempCleanupStore(db, cipher)
	server := &Server{
		db: db, jobs: jobs, tempCleanups: cleanups,
		config: Config{RequestTimeout: time.Second}, nowFunc: func() time.Time { return now },
	}
	if err := server.recordTempResource("retry-job", "account-a", "temp-a"); err != nil {
		t.Fatal(err)
	}
	clientA := &fakePikPakClient{deleteFiles: func(context.Context, []string) error {
		return errors.New("remote cleanup failed")
	}}
	cleanupErr := server.cleanupJobTempResources(context.Background(), "retry-job", AccountRuntime{ID: "account-a", Client: clientA})
	var persistenceErr *jobPersistenceError
	if cleanupErr == nil || errors.As(cleanupErr, &persistenceErr) {
		t.Fatalf("cleanup error = %v, want remote-only failure", cleanupErr)
	}
	storedJob, ok, err := durable.getAny("retry-job", now)
	if err != nil || !ok {
		t.Fatalf("get durable job: ok=%v err=%v", ok, err)
	}
	if storedJob.TempAccountID != "" || len(storedJob.TempIDs) != 0 {
		t.Fatalf("active cleanup state was not detached: account=%q ids=%v", storedJob.TempAccountID, storedJob.TempIDs)
	}

	if err := server.recordTempResource("retry-job", "account-b", "temp-b"); err != nil {
		t.Fatalf("record second account cleanup intent: %v", err)
	}
	due, err := cleanups.due(now.Add(4*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 || due[0].AccountID == due[1].AccountID {
		t.Fatalf("cleanup records = %+v, want one per account", due)
	}
}

func TestResolveExistingStorageFailureKeepsAccountAvailable(t *testing.T) {
	now := time.Date(2026, 7, 10, 4, 5, 6, 0, time.UTC)
	db, err := openDatabase(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher := newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef"))
	client := &fakePikPakClient{getFile: func(context.Context, string) (*pikpak.FileEntry, error) {
		return fakeDownloadFile("file", "https://cdn.example/file", now.Add(time.Hour)), nil
	}}
	accountStore := newAccountStore(db, cipher)
	account := accountRecord{
		ID: "account", Username: "account@example.com", Password: "password", Status: AccountAvailable,
		TrafficLimit: 1000, TrafficPeriod: monthKey(now), CreatedAt: now, UpdatedAt: now,
	}
	if err := accountStore.Insert(account); err != nil {
		t.Fatal(err)
	}
	accounts := fakeAccountPool(account.ID, client)
	accounts.store = accountStore
	accounts.accounts[account.ID].record = account
	durable := newSQLJobStore(db, cipher)
	jobs := newJobStore(20, durable)
	if err := jobs.create(&Job{
		ID: "storage-failure-job", Kind: ResourceMagnet, Mode: "proxy", Status: JobRunning,
		Stage: StageTransfer, AccountID: account.ID, BaseURL: "https://proxy.example",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER fail_cleanup BEFORE INSERT ON proxy_temp_cleanups BEGIN SELECT RAISE(ABORT, 'cleanup write failed'); END"); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		db: db, accounts: accounts, accountStore: accountStore, jobs: jobs, durableJobs: durable,
		tempCleanups: newProxyTempCleanupStore(db, cipher), logs: newLogStore(20),
		config: Config{RequestTimeout: time.Second}, nowFunc: func() time.Time { return now },
	}
	server.resolveExistingFile(context.Background(), "storage-failure-job", DownloadItem{ID: "file", Name: "file"})

	storedAccounts, err := accountStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if storedAccounts[0].Status != AccountAvailable || storedAccounts[0].TrafficUsed != 0 {
		t.Fatalf("storage failure changed account health: status=%s traffic=%d", storedAccounts[0].Status, storedAccounts[0].TrafficUsed)
	}
	storedJob, ok, err := durable.getAny("storage-failure-job", now)
	if err != nil || !ok {
		t.Fatalf("get durable job: ok=%v err=%v", ok, err)
	}
	if storedJob.Status != JobFailed || storedJob.Error != jobPersistenceUserError {
		t.Fatalf("job status=%s error=%q", storedJob.Status, storedJob.Error)
	}
	_, deleteCalls := client.counts()
	if deleteCalls != 0 {
		t.Fatalf("destructive cleanup calls after storage failure = %d, want 0", deleteCalls)
	}
}

func TestUncertainFinalizationDoesNotOverwriteCommittedDatabaseState(t *testing.T) {
	now := time.Date(2026, 7, 10, 4, 5, 6, 0, time.UTC)
	db, err := openDatabase(t.TempDir() + "/app.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cipher := newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef"))
	accountStore := newAccountStore(db, cipher)
	account := accountRecord{
		ID: "account", Username: "account@example.com", Password: "password", Status: AccountAvailable,
		TrafficLimit: 1000, TrafficPeriod: monthKey(now), CreatedAt: now, UpdatedAt: now,
	}
	if err := accountStore.Insert(account); err != nil {
		t.Fatal(err)
	}
	accounts := fakeAccountPool(account.ID, &fakePikPakClient{})
	accounts.store = accountStore
	accounts.accounts[account.ID].record = account

	durable := newSQLJobStore(db, cipher)
	jobs := newJobStore(20, durable)
	running := &Job{
		ID: "uncertain-job", Kind: ResourceMagnet, Status: JobRunning, Stage: StageTransfer,
		AccountID: account.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := jobs.create(running); err != nil {
		t.Fatal(err)
	}
	committed := cloneJob(running)
	committed.Status = JobCompleted
	committed.Stage = StageComplete
	committed.ChargedBytes = 100
	committed.UpdatedAt = now.Add(time.Second)
	if err := durable.upsert(committed); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE pikpak_accounts SET traffic_used=100 WHERE id=?`, account.ID); err != nil {
		t.Fatal(err)
	}

	server := &Server{accounts: accounts, jobs: jobs, logs: newLogStore(20), restartCh: make(chan struct{})}
	handled := server.handleJobPersistenceFailure(
		running.ID,
		AccountRuntime{ID: account.ID},
		&jobPersistenceError{operation: "persist completed job", err: errors.New("commit result unavailable"), uncertain: true},
		true,
	)
	if !handled {
		t.Fatal("uncertain persistence error was not handled")
	}
	select {
	case <-server.RestartRequested():
	default:
		t.Fatal("uncertain persistence error did not request a controlled restart")
	}
	storedAccounts, err := accountStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if storedAccounts[0].TrafficUsed != 100 {
		t.Fatalf("committed account traffic was overwritten: %d", storedAccounts[0].TrafficUsed)
	}
	storedJob, ok, err := durable.getAny(running.ID, now)
	if err != nil || !ok {
		t.Fatalf("get durable job: ok=%v err=%v", ok, err)
	}
	if storedJob.Status != JobCompleted || storedJob.ChargedBytes != 100 {
		t.Fatalf("committed job was overwritten: status=%s charged=%d", storedJob.Status, storedJob.ChargedBytes)
	}
	cached, ok := jobs.get(running.ID)
	if !ok {
		t.Fatal("uncertain branch removed the cached job")
	}
	if cached.Status != JobRunning {
		t.Fatalf("uncertain branch unexpectedly published state: status=%v", cached.Status)
	}
}
