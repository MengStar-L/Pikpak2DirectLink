package app

import (
	"context"
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

// queueEntry is one unit of work waiting for the single resolution slot.
type queueEntry struct {
	jobID    string
	priority int
	run      func(ctx context.Context)
}

// resolveQueue serializes link resolution so the system only ever drives PikPak
// for one job at a time (which keeps it under PikPak's rate-control radar),
// while still letting admin submissions jump the line and reporting queue
// positions back to callers.
//
// A single worker goroutine drains the queue. Each job runs under a hard
// timeout; if it exceeds the budget it is abandoned and the next job starts. A
// job that pauses for user input (selection_required) simply returns from its
// run func, freeing the slot — its continuation is re-enqueued at
// priorityResume so it resumes almost immediately.
type resolveQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	entries []*queueEntry
	current string // jobID currently holding the slot, "" when idle
	timeout time.Duration
	fail    func(jobID string, err error) // injected s.failJob, for panic/abort fallback
}

func newResolveQueue(timeout time.Duration, fail func(jobID string, err error)) *resolveQueue {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	q := &resolveQueue{
		timeout: timeout,
		fail:    fail,
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// enqueue appends a job and wakes the worker. The entry is placed after the last
// entry whose priority is greater than or equal to its own, so higher-priority
// jobs jump ahead while ties preserve submission order.
func (q *resolveQueue) enqueue(jobID string, priority int, run func(ctx context.Context)) {
	q.mu.Lock()
	defer q.mu.Unlock()

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

	q.cond.Signal()
}

// run is the worker loop. It must be started exactly once (go q.run()).
func (q *resolveQueue) run() {
	for {
		q.mu.Lock()
		for len(q.entries) == 0 {
			q.cond.Wait()
		}
		entry := q.entries[0]
		copy(q.entries, q.entries[1:])
		q.entries[len(q.entries)-1] = nil
		q.entries = q.entries[:len(q.entries)-1]
		q.current = entry.jobID
		q.mu.Unlock()

		q.execute(entry)

		q.mu.Lock()
		q.current = ""
		q.mu.Unlock()
	}
}

// execute runs one entry under the hard timeout, recovering from panics so a
// single bad job can never wedge the worker (and the whole queue) permanently.
func (q *resolveQueue) execute(entry *queueEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), q.timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil && q.fail != nil {
			q.fail(entry.jobID, fmt.Errorf("解析任务异常中断：%v", r))
		}
	}()
	entry.run(ctx)
}

// position reports how many jobs are ahead of jobID. A running job (or one not
// in the queue at all) reports 0; the head of the queue reports 1 while another
// job is running, since that running job will finish first.
func (q *resolveQueue) position(jobID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.current == jobID {
		return 0
	}
	for i, e := range q.entries {
		if e.jobID == jobID {
			ahead := i
			if q.current != "" {
				ahead++
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

// active reports whether a job currently holds the resolution slot.
func (q *resolveQueue) active() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.current != ""
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
