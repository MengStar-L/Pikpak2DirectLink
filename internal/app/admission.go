package app

import (
	"fmt"
	"sync"
	"time"
)

const (
	maxGlobalNonterminalJobs = 500
	maxUserNonterminalJobs   = 100
	maxUserSelectionJobs     = 10
	selectionRequiredTimeout = 30 * time.Minute
)

type jobAdmissionError struct {
	Global    bool
	Selection bool
	Limit     int
}

func (e *jobAdmissionError) Error() string {
	scope := "user"
	if e.Global {
		scope = "service"
	}
	kind := "active job"
	if e.Selection {
		kind = "selection job"
	}
	return fmt.Sprintf("%s %s limit reached (%d)", scope, kind, e.Limit)
}

type jobAdmission struct {
	mu             sync.Mutex
	jobs           *jobStore
	globalLimit    int
	userLimit      int
	selectionLimit int
}

func newJobAdmission(jobs *jobStore) *jobAdmission {
	return newJobAdmissionWithLimits(jobs, maxGlobalNonterminalJobs, maxUserNonterminalJobs, maxUserSelectionJobs)
}

func newJobAdmissionWithLimits(jobs *jobStore, globalLimit, userLimit, selectionLimit int) *jobAdmission {
	return &jobAdmission{
		jobs:           jobs,
		globalLimit:    globalLimit,
		userLimit:      userLimit,
		selectionLimit: selectionLimit,
	}
}

// withCapacity serializes the count-and-create window so concurrent requests
// cannot both pass the same capacity check before their jobs become visible.
func (a *jobAdmission) withCapacity(userID string, units int, create func() error) error {
	if a == nil || a.jobs == nil {
		return create()
	}
	if units < 1 {
		units = 1
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	global, user, _ := a.jobs.admissionCounts(userID, "")
	if a.globalLimit > 0 && global+units > a.globalLimit {
		return &jobAdmissionError{Global: true, Limit: a.globalLimit}
	}
	if userID != "" && a.userLimit > 0 && user+units > a.userLimit {
		return &jobAdmissionError{Limit: a.userLimit}
	}
	return create()
}

func (a *jobAdmission) allowSelection(userID, jobID string) error {
	return a.withSelection(userID, jobID, func() error { return nil })
}

func (a *jobAdmission) withSelection(userID, jobID string, update func() error) error {
	if a == nil || a.jobs == nil || userID == "" || a.selectionLimit <= 0 {
		return update()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _, selections := a.jobs.admissionCounts(userID, jobID)
	if selections >= a.selectionLimit {
		return &jobAdmissionError{Selection: true, Limit: a.selectionLimit}
	}
	return update()
}
