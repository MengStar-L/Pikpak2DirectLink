package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Job-resolution priorities. Higher numbers are dispatched first; within a
// priority, dispatch stays FIFO. A resume continuation (the user already waited
// a turn and just picked a file) outranks an admin queue-jump, which in turn
// outranks an ordinary user submission.
const (
	priorityUser   = 0
	priorityAdmin  = 1
	priorityResume = 2
)

// maxResolveConcurrency caps how many jobs an admin can run in parallel, so a
// fat-fingered value can't spawn an unbounded number of goroutines all hammering
// PikPak at once.
const maxResolveConcurrency = 32

var errResolveQueueClosed = errors.New("resolve queue is closed")
var errResolveQueueFull = errors.New("resolve queue is full")

// queueEntry is one unit of work waiting for a resolution slot.
type queueEntry struct {
	jobID    string
	priority int
	run      func(ctx context.Context)
}

// resolveQueue meters link resolution so the system only ever drives PikPak for
// a bounded number of jobs at a time (which keeps it under PikPak's rate-control
// radar), while still letting admin submissions jump the line and reporting
// queue positions back to callers.
//
// A single dispatcher goroutine drains the queue, spawning up to `concurrency`
// worker goroutines. With concurrency == 1 it behaves exactly like the original
// serial worker. The admin can change concurrency live; the dispatcher simply
// stops starting new jobs once the running set reaches the limit and resumes
// when a slot frees (or when the limit is raised).
//
// Each job runs under a hard timeout; if it exceeds the budget it is abandoned
// and the next job starts. The budget is shorter while serial (snappy
// turnaround) and longer while parallel (more jobs share PikPak's attention, so
// each can legitimately take longer). A job that pauses for user input
// (selection_required) simply returns from its run func, freeing the slot — its
// continuation is re-enqueued at priorityResume so it resumes almost
// immediately.
type resolveQueue struct {
	mu              sync.Mutex
	cond            *sync.Cond
	entries         []*queueEntry
	running         map[string]bool // jobIDs currently holding a slot
	concurrency     int
	serialTimeout   time.Duration
	parallelTimeout time.Duration
	fail            func(jobID string, err error) // injected s.failJob, for panic/abort fallback
	ctx             context.Context
	cancel          context.CancelFunc
	closed          bool
	runStarted      bool
	runDone         chan struct{}
	runDoneOnce     sync.Once
	shutdownOnce    sync.Once
	workers         sync.WaitGroup
}

func newResolveQueue(serialTimeout, parallelTimeout time.Duration, concurrency int, fail func(jobID string, err error)) *resolveQueue {
	if serialTimeout <= 0 {
		serialTimeout = 60 * time.Second
	}
	if parallelTimeout <= 0 {
		parallelTimeout = 2 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &resolveQueue{
		running:         make(map[string]bool),
		concurrency:     clampConcurrency(concurrency),
		serialTimeout:   serialTimeout,
		parallelTimeout: parallelTimeout,
		fail:            fail,
		ctx:             ctx,
		cancel:          cancel,
		runDone:         make(chan struct{}),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func clampConcurrency(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxResolveConcurrency {
		return maxResolveConcurrency
	}
	return n
}

// enqueue appends a job and wakes the dispatcher. The entry is placed after the
// last entry whose priority is greater than or equal to its own, so
// higher-priority jobs jump ahead while ties preserve submission order.
func (q *resolveQueue) enqueue(jobID string, priority int, run func(ctx context.Context)) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		if q.fail != nil {
			q.fail(jobID, errResolveQueueClosed)
		}
		return errResolveQueueClosed
	}
	if len(q.entries)+len(q.running) >= maxGlobalNonterminalJobs {
		q.mu.Unlock()
		if q.fail != nil {
			q.fail(jobID, errResolveQueueFull)
		}
		return errResolveQueueFull
	}

	insertAt := len(q.entries)
	for i, e := range q.entries {
		if e.priority < priority {
			insertAt = i
			break
		}
	}

	q.entries = append(q.entries, nil)
	copy(q.entries[insertAt+1:], q.entries[insertAt:])
	q.entries[insertAt] = &queueEntry{jobID: jobID, priority: priority, run: run}

	q.cond.Broadcast()
	q.mu.Unlock()
	return nil
}

// run is the dispatcher loop. It must be started exactly once (go q.run()). It
// waits until there is queued work AND a free slot, then pops the head and runs
// it in its own goroutine.
func (q *resolveQueue) run() {
	q.mu.Lock()
	if q.runStarted {
		q.mu.Unlock()
		return
	}
	q.runStarted = true
	q.mu.Unlock()
	defer q.runDoneOnce.Do(func() { close(q.runDone) })

	for {
		q.mu.Lock()
		for !q.closed && (len(q.entries) == 0 || len(q.running) >= q.concurrency) {
			q.cond.Wait()
		}
		if q.closed {
			q.mu.Unlock()
			return
		}
		entry := q.entries[0]
		copy(q.entries, q.entries[1:])
		q.entries[len(q.entries)-1] = nil
		q.entries = q.entries[:len(q.entries)-1]
		q.running[entry.jobID] = true
		q.workers.Add(1)
		q.mu.Unlock()

		go func(e *queueEntry) {
			defer q.workers.Done()
			q.execute(e)
			q.mu.Lock()
			delete(q.running, e.jobID)
			// A slot just freed (or the running set shrank below the limit) — let
			// the dispatcher re-evaluate.
			q.cond.Broadcast()
			q.mu.Unlock()
		}(entry)
	}
}

// execute runs one entry under the hard timeout, recovering from panics so a
// single bad job can never wedge a worker. The timeout is sampled at dispatch
// time so a live concurrency change applies to subsequent jobs.
func (q *resolveQueue) execute(entry *queueEntry) {
	ctx, cancel := context.WithTimeout(q.ctx, q.currentTimeout())
	defer cancel()
	defer func() {
		if r := recover(); r != nil && q.fail != nil {
			q.fail(entry.jobID, fmt.Errorf("解析任务异常中断：%v", r))
		}
	}()
	entry.run(ctx)
}

// shutdown stops accepting work, cancels active jobs, fails every queued job,
// and waits until the dispatcher and all workers have exited. It is safe to
// call more than once.
func (q *resolveQueue) shutdown() {
	q.shutdownOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		pending := q.entries
		q.entries = nil
		q.cancel()
		q.cond.Broadcast()
		runStarted := q.runStarted
		q.mu.Unlock()

		if q.fail != nil {
			for _, entry := range pending {
				q.fail(entry.jobID, errResolveQueueClosed)
			}
		}
		if runStarted {
			<-q.runDone
		}
		q.workers.Wait()
	})
}

// setConcurrency changes the number of parallel slots live. Raising it wakes the
// dispatcher so queued jobs can start immediately; lowering it just means no new
// jobs start until the running set drains below the new limit.
func (q *resolveQueue) setConcurrency(n int) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.concurrency = clampConcurrency(n)
	q.cond.Broadcast()
	return q.concurrency
}

func (q *resolveQueue) setTaskTimeout(timeout time.Duration) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.serialTimeout = timeout
	q.parallelTimeout = timeout
	return timeout
}

// concurrencyValue reports the current parallel-slot limit.
func (q *resolveQueue) concurrencyValue() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.concurrency
}

func (q *resolveQueue) timeoutSnapshot() (serial, parallel, current time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()
	serial = q.serialTimeout
	parallel = q.parallelTimeout
	if q.concurrency > 1 {
		current = q.parallelTimeout
	} else {
		current = q.serialTimeout
	}
	return serial, parallel, current
}

// currentTimeout returns the per-job budget for the current mode: the shorter
// serial budget when running one at a time, the longer parallel budget
// otherwise.
func (q *resolveQueue) currentTimeout() time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.concurrency > 1 {
		return q.parallelTimeout
	}
	return q.serialTimeout
}

// position reports how many jobs must finish before jobID starts. A running job
// (or one not in the queue at all) reports 0. For a queued job at index i with R
// jobs running and a limit of C, that is max(0, i + R - C + 1): it generalizes
// the serial case (C == 1) where the head reports 1 while another job runs.
func (q *resolveQueue) position(jobID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.running[jobID] {
		return 0
	}
	for i, e := range q.entries {
		if e.jobID == jobID {
			ahead := i + len(q.running) - q.concurrency + 1
			if ahead < 0 {
				ahead = 0
			}
			return ahead
		}
	}
	return 0
}

// waiting is the number of jobs queued but not yet started.
func (q *resolveQueue) waiting() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries)
}

// active reports whether any job currently holds a resolution slot.
func (q *resolveQueue) active() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.running) > 0
}

// runningCount is how many jobs currently hold a slot.
func (q *resolveQueue) runningCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.running)
}

// queuedIDs returns the waiting job IDs in dispatch order. Used by tests.
func (q *resolveQueue) queuedIDs() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	ids := make([]string, len(q.entries))
	for i, e := range q.entries {
		ids[i] = e.jobID
	}
	return ids
}
