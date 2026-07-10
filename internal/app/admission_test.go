package app

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestJobAdmissionEnforcesGlobalAndUserLimitsBeforeCreate(t *testing.T) {
	store := newJobStore(maxGlobalNonterminalJobs + 10)
	admission := newJobAdmission(store)
	now := time.Now().UTC()

	for i := 0; i < maxUserNonterminalJobs; i++ {
		if err := store.create(&Job{
			ID:        fmt.Sprintf("user-a-%d", i),
			UserID:    "user-a",
			Status:    JobQueued,
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed user job: %v", err)
		}
	}

	called := false
	err := admission.withCapacity("user-a", 1, func() error {
		called = true
		return nil
	})
	var limitErr *jobAdmissionError
	if !errors.As(err, &limitErr) || limitErr.Global {
		t.Fatalf("user admission error = %v, want per-user limit", err)
	}
	if called {
		t.Fatal("create callback ran after per-user admission rejection")
	}

	for i := maxUserNonterminalJobs; i < maxGlobalNonterminalJobs; i++ {
		if err := store.create(&Job{
			ID:        fmt.Sprintf("other-%d", i),
			UserID:    fmt.Sprintf("user-%d", i),
			Status:    JobRunning,
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed global job: %v", err)
		}
	}

	err = admission.withCapacity("new-user", 1, func() error {
		called = true
		return nil
	})
	if !errors.As(err, &limitErr) || !limitErr.Global {
		t.Fatalf("global admission error = %v, want global limit", err)
	}
}

func TestJobAdmissionSerializesConcurrentCreates(t *testing.T) {
	store := newJobStore(10)
	admission := newJobAdmissionWithLimits(store, 1, 1, 1)
	now := time.Now().UTC()
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)

	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- admission.withCapacity("user", 1, func() error {
				return store.create(&Job{ID: fmt.Sprintf("job-%d", i), UserID: "user", Status: JobQueued, CreatedAt: now, UpdatedAt: now})
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var accepted, rejected int
	for err := range errs {
		if err == nil {
			accepted++
		} else {
			var limitErr *jobAdmissionError
			if !errors.As(err, &limitErr) {
				t.Fatalf("unexpected admission error: %v", err)
			}
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted/rejected = %d/%d, want 1/1", accepted, rejected)
	}
}

func TestJobAdmissionCapsSelectionJobsAndFindsExpired(t *testing.T) {
	store := newJobStore(20)
	admission := newJobAdmissionWithLimits(store, 20, 20, 2)
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		if err := store.create(&Job{
			ID:        fmt.Sprintf("selection-%d", i),
			UserID:    "user-a",
			Status:    JobSelectionRequired,
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now.Add(-time.Hour),
		}); err != nil {
			t.Fatalf("seed selection job: %v", err)
		}
	}
	if err := admission.allowSelection("user-a", "new-job"); err == nil {
		t.Fatal("expected selection admission rejection")
	}
	if err := admission.allowSelection("user-b", "new-job"); err != nil {
		t.Fatalf("other user selection rejected: %v", err)
	}

	expired := store.expiredSelectionIDs(now.Add(-selectionRequiredTimeout))
	if len(expired) != 2 {
		t.Fatalf("expired selection IDs = %v, want two", expired)
	}
	if got := store.expiredSelectionIDs(now.Add(-2 * time.Hour)); len(got) != 0 {
		t.Fatalf("unexpected recently-cutoff selections: %v", got)
	}
}

func TestJobAdmissionSerializesSelectionTransition(t *testing.T) {
	store := newJobStore(20)
	admission := newJobAdmissionWithLimits(store, 20, 20, 1)
	now := time.Now().UTC()
	for _, id := range []string{"job-a", "job-b"} {
		if err := store.create(&Job{ID: id, UserID: "user", Status: JobRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"job-a", "job-b"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- admission.withSelection("user", id, func() error {
				_, err := store.update(id, func(job *Job) error {
					job.Status = JobSelectionRequired
					return nil
				})
				return err
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	var accepted, rejected int
	for err := range errs {
		if err == nil {
			accepted++
		} else {
			var limitErr *jobAdmissionError
			if !errors.As(err, &limitErr) || !limitErr.Selection {
				t.Fatalf("selection transition error = %v", err)
			}
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("selection transitions accepted/rejected = %d/%d, want 1/1", accepted, rejected)
	}
}

func TestRequestSelectionReturnsCapacityErrorWithoutPersistenceWrapping(t *testing.T) {
	store := newJobStore(20)
	now := time.Now().UTC()
	for i := 0; i < maxUserSelectionJobs; i++ {
		if err := store.create(&Job{
			ID: fmt.Sprintf("waiting-%d", i), UserID: "user", Status: JobSelectionRequired,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	candidate := &Job{ID: "candidate", UserID: "user", Status: JobRunning, CreatedAt: now, UpdatedAt: now}
	if err := store.create(candidate); err != nil {
		t.Fatal(err)
	}
	s := &Server{jobs: store, admission: newJobAdmission(store)}

	err := s.requestSelection(candidate.ID, StageResultSelection, "choose a file", []DownloadItem{{ID: "file"}}, "account")
	var limitErr *jobAdmissionError
	if !errors.As(err, &limitErr) || !limitErr.Selection {
		t.Fatalf("requestSelection error = %v, want selection admission error", err)
	}
	var persistenceErr *jobPersistenceError
	if errors.As(err, &persistenceErr) {
		t.Fatalf("selection capacity was wrapped as persistence failure: %v", err)
	}
	stored, _ := store.get(candidate.ID)
	if stored.Status != JobRunning {
		t.Fatalf("capacity-rejected job status = %s, want running for caller finalization", stored.Status)
	}
}
