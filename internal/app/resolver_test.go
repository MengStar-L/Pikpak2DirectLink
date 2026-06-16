package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveQueuePriorityOrdering(t *testing.T) {
	t.Parallel()

	q := newResolveQueue(time.Second, nil)
	noop := func(context.Context) {}

	// Enqueue interleaved priorities; the worker is never started, so entries
	// stay put and we can inspect dispatch order directly.
	q.enqueue("u1", priorityUser, noop)
	q.enqueue("u2", priorityUser, noop)
	q.enqueue("a1", priorityAdmin, noop)
	q.enqueue("r1", priorityResume, noop)
	q.enqueue("a2", priorityAdmin, noop)
	q.enqueue("u3", priorityUser, noop)

	got := q.queuedIDs()
	want := []string{"r1", "a1", "a2", "u1", "u2", "u3"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, got)
		}
	}
}

func TestResolveQueuePosition(t *testing.T) {
	t.Parallel()

	q := newResolveQueue(time.Second, nil)
	noop := func(context.Context) {}
	q.enqueue("u1", priorityUser, noop)
	q.enqueue("u2", priorityUser, noop)

	// Idle worker: the head reports 0 ahead.
	if got := q.position("u1"); got != 0 {
		t.Fatalf("expected position 0 for head with idle worker, got %d", got)
	}
	if got := q.position("u2"); got != 1 {
		t.Fatalf("expected position 1, got %d", got)
	}

	// Simulate a job holding the slot: everyone in the queue shifts back one.
	q.mu.Lock()
	q.current = "running"
	q.mu.Unlock()

	if got := q.position("u1"); got != 1 {
		t.Fatalf("expected position 1 with a running job, got %d", got)
	}
	if got := q.position("u2"); got != 2 {
		t.Fatalf("expected position 2, got %d", got)
	}
	if got := q.position("running"); got != 0 {
		t.Fatalf("expected running job position 0, got %d", got)
	}
	if got := q.position("missing"); got != 0 {
		t.Fatalf("expected unknown job position 0, got %d", got)
	}
}

func TestResolveQueueRunsSerially(t *testing.T) {
	t.Parallel()

	q := newResolveQueue(2*time.Second, nil)
	go q.run()

	var concurrent, maxConcurrent int32
	var wg sync.WaitGroup

	makeRun := func() func(context.Context) {
		return func(context.Context) {
			defer wg.Done()
			n := atomic.AddInt32(&concurrent, 1)
			for {
				old := atomic.LoadInt32(&maxConcurrent)
				if n <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt32(&concurrent, -1)
		}
	}

	const total = 5
	wg.Add(total)
	q.enqueue("u1", priorityUser, makeRun())
	q.enqueue("u2", priorityUser, makeRun())
	q.enqueue("a1", priorityAdmin, makeRun())
	q.enqueue("r1", priorityResume, makeRun())
	q.enqueue("u3", priorityUser, makeRun())

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for queued jobs to run")
	}

	if got := atomic.LoadInt32(&maxConcurrent); got != 1 {
		t.Fatalf("expected serial execution (max concurrency 1), got %d", got)
	}
	if q.active() {
		t.Fatalf("expected queue idle after draining")
	}
	if w := q.waiting(); w != 0 {
		t.Fatalf("expected empty queue after draining, got %d waiting", w)
	}
}
