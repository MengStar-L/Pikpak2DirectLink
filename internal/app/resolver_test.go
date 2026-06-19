package app

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolveQueuePriorityOrdering(t *testing.T) {
	t.Parallel()

	q := newResolveQueue(time.Second, time.Second, 1, nil)
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

	q := newResolveQueue(time.Second, time.Second, 1, nil)
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
	q.running["running"] = true
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

	q := newResolveQueue(2*time.Second, 2*time.Second, 1, nil)
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

func TestResolveQueueRunsInParallel(t *testing.T) {
	t.Parallel()

	const limit = 3
	q := newResolveQueue(2*time.Second, 2*time.Second, limit, nil)
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
			time.Sleep(40 * time.Millisecond)
			atomic.AddInt32(&concurrent, -1)
		}
	}

	const total = 9
	wg.Add(total)
	for i := 0; i < total; i++ {
		q.enqueue("j"+strconv.Itoa(i), priorityUser, makeRun())
	}

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

	got := atomic.LoadInt32(&maxConcurrent)
	if got != limit {
		t.Fatalf("expected peak concurrency %d, got %d", limit, got)
	}
}

func TestResolveQueueParallelPosition(t *testing.T) {
	t.Parallel()

	// Two slots, two jobs "running", three waiting. With C=2 and R=2, the head of
	// the queue must wait for one running job to finish, then positions step by 1.
	q := newResolveQueue(time.Second, time.Second, 2, nil)
	noop := func(context.Context) {}
	q.enqueue("w1", priorityUser, noop)
	q.enqueue("w2", priorityUser, noop)
	q.enqueue("w3", priorityUser, noop)

	q.mu.Lock()
	q.running["r1"] = true
	q.running["r2"] = true
	q.mu.Unlock()

	for id, want := range map[string]int{"w1": 1, "w2": 2, "w3": 3} {
		if got := q.position(id); got != want {
			t.Fatalf("position(%s) = %d, want %d", id, got, want)
		}
	}
	if got := q.position("r1"); got != 0 {
		t.Fatalf("running job position = %d, want 0", got)
	}

	// A free slot (R < C) lets the head start immediately.
	q.mu.Lock()
	delete(q.running, "r2")
	q.mu.Unlock()
	if got := q.position("w1"); got != 0 {
		t.Fatalf("head position with a free slot = %d, want 0", got)
	}
}

func TestResolveQueueTimeoutTracksMode(t *testing.T) {
	t.Parallel()

	serial := 45 * time.Second
	parallel := 2 * time.Minute
	q := newResolveQueue(serial, parallel, 1, nil)

	if got := q.currentTimeout(); got != serial {
		t.Fatalf("serial-mode timeout = %s, want %s", got, serial)
	}
	q.setConcurrency(4)
	if got := q.currentTimeout(); got != parallel {
		t.Fatalf("parallel-mode timeout = %s, want %s", got, parallel)
	}
	if got := q.setConcurrency(999); got != maxResolveConcurrency {
		t.Fatalf("setConcurrency clamp = %d, want %d", got, maxResolveConcurrency)
	}
	if got := q.setConcurrency(0); got != 1 {
		t.Fatalf("setConcurrency floor = %d, want 1", got)
	}
	unified := 9 * time.Minute
	if got := q.setTaskTimeout(unified); got != unified {
		t.Fatalf("setTaskTimeout = %s, want %s", got, unified)
	}
	if got := q.currentTimeout(); got != unified {
		t.Fatalf("unified serial timeout = %s, want %s", got, unified)
	}
	q.setConcurrency(3)
	if got := q.currentTimeout(); got != unified {
		t.Fatalf("unified parallel timeout = %s, want %s", got, unified)
	}
}
