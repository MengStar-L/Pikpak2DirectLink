package app

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type ResourceKind string

const (
	ResourceMagnet ResourceKind = "magnet"
	ResourceShare  ResourceKind = "share"
	// ResourceBatch is the parent job created for a multi-line submission. It owns
	// no resource of its own; it fans out one child job per line and merges their
	// results.
	ResourceBatch ResourceKind = "batch"
)

const maxSelectedFilesPerResolve = 100

type JobStatus string

const (
	JobQueued            JobStatus = "queued"
	JobRunning           JobStatus = "running"
	JobSelectionRequired JobStatus = "selection_required"
	JobCompleted         JobStatus = "completed"
	JobFailed            JobStatus = "failed"
)

type JobStage string

const (
	StageTransfer        JobStage = "transfer"
	StageSourceSelection JobStage = "source_selection"
	StageResultSelection JobStage = "result_selection"
	StageComplete        JobStage = "complete"
	StageFailed          JobStage = "failed"
)

type DownloadItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	MimeType string `json:"mime_type,omitempty"`
	Size     string `json:"size,omitempty"`
}

type JobResult struct {
	File       DownloadItem `json:"file"`
	URL        string       `json:"url"`
	DirectURL  string       `json:"direct_url"`
	ProxyURL   string       `json:"proxy_url"`
	ProxyToken string       `json:"proxy_token"`
	ExpiresAt  string       `json:"expires_at,omitempty"`
	// AccountID records which PikPak account produced this link. The single-file
	// and CDK-batch paths leave it empty (the job's own AccountID applies), but a
	// multi-link parent merges results from several children that may each have
	// used a different account, so the proxy handler needs it to re-fetch a fresh
	// direct link after expiry.
	AccountID string `json:"-"`
}

func preferredResultURL(mode, directURL, proxyURL string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "proxy") {
		return firstNonEmpty(proxyURL, directURL)
	}
	return firstNonEmpty(directURL, proxyURL)
}

func (r *JobResult) applyPreferredURL(mode string) {
	if r == nil {
		return
	}
	r.URL = preferredResultURL(mode, r.DirectURL, r.ProxyURL)
}

// BatchProgress is the parent-job rollup for a multi-line submission, surfaced to
// the front-end so it can show "解析成功 x/x 条".
type BatchProgress struct {
	Total     int            `json:"total"`
	Succeeded int            `json:"succeeded"`
	Failed    int            `json:"failed"`
	Failures  []BatchFailure `json:"failures,omitempty"`
}

type BatchFailure struct {
	Label string `json:"label"`
	Error string `json:"error"`
}

type AccountAttempt struct {
	AccountID string `json:"account_id"`
	Username  string `json:"username"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type ShareState struct {
	ShareID       string         `json:"share_id"`
	TailID        string         `json:"tail_id,omitempty"`
	PassCodeToken string         `json:"pass_code_token,omitempty"`
	SelectedID    string         `json:"selected_id,omitempty"`
	SelectedIDs   []string       `json:"selected_ids,omitempty"`
	SelectedItems []DownloadItem `json:"-"`
}

type Job struct {
	ID               string           `json:"id"`
	Kind             ResourceKind     `json:"kind"`
	Mode             string           `json:"mode"`
	Input            string           `json:"input"`
	OriginalInput    string           `json:"-"`
	PassCode         string           `json:"pass_code,omitempty"`
	Status           JobStatus        `json:"status"`
	Stage            JobStage         `json:"stage"`
	Message          string           `json:"message,omitempty"`
	Error            string           `json:"error,omitempty"`
	BaseURL          string           `json:"-"`
	FolderID         string           `json:"-"`
	CDKCode          string           `json:"-"`
	UserID           string           `json:"-"`
	ProxyAllowed     bool             `json:"-"`
	AccountID        string           `json:"account_id,omitempty"`
	Share            *ShareState      `json:"share,omitempty"`
	Items            []DownloadItem   `json:"items,omitempty"`
	AccountAttempts  []AccountAttempt `json:"account_attempts,omitempty"`
	Result           *JobResult       `json:"result,omitempty"`
	Results          []JobResult      `json:"results,omitempty"`
	Warnings         []string         `json:"warnings,omitempty"`
	QueueAhead       int              `json:"queue_ahead"`
	FailureCode      string           `json:"failure_code,omitempty"`
	DetailsAvailable bool             `json:"details_available"`
	ChargedBytes     int64            `json:"charged_bytes"`
	TempAccountID    string           `json:"-"`
	TempIDs          []string         `json:"-"`
	// ResolveAll marks a child job that must auto-resolve every file it finds
	// (never pause at selection_required). Set on the children a batch fans out.
	ResolveAll bool `json:"-"`
	// ResolveSelected marks a resumed share job whose source-selection choices
	// are already the final files to parse, so it should not ask for a second
	// result-selection pass after restoring them.
	ResolveSelected bool `json:"-"`
	// ParentID links a child job back to its batch parent.
	ParentID string `json:"-"`
	// Batch is the rollup carried by a parent (kind == batch) job.
	Batch     *BatchProgress `json:"batch,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// resultForToken returns the resolved file whose proxy token matches, searching
// both the single-file Result (admin path) and the multi-file Results (CDK-user
// batch path). Returns nil when no token matches.
func (j *Job) resultForToken(token string) *JobResult {
	if token == "" {
		return nil
	}
	if j.Result != nil && j.Result.ProxyToken == token {
		return j.Result
	}
	for i := range j.Results {
		if j.Results[i].ProxyToken == token {
			return &j.Results[i]
		}
	}
	return nil
}

type jobStore struct {
	mu       sync.RWMutex
	jobs     map[string]*Job
	order    []string
	capacity int
	durable  *sqlJobStore
}

type atomicCommitError struct {
	err       error
	uncertain bool
}

func (e *atomicCommitError) Error() string { return e.err.Error() }
func (e *atomicCommitError) Unwrap() error { return e.err }

func newJobStore(capacity int, durable ...*sqlJobStore) *jobStore {
	if capacity <= 0 {
		capacity = 200
	}
	store := &jobStore{
		jobs:     make(map[string]*Job),
		capacity: capacity,
	}
	if len(durable) > 0 {
		store.durable = durable[0]
	}
	return store
}

func (s *jobStore) create(job *Job) error {
	copyJob := cloneJob(job)
	if copyJob == nil {
		return errors.New("job is required")
	}
	copyJob.DetailsAvailable = true
	if s.durable != nil {
		if err := s.durable.create(copyJob); err != nil {
			return err
		}
	}
	job.DetailsAvailable = true

	s.mu.Lock()
	defer s.mu.Unlock()
	s.putLocked(copyJob)
	return nil
}

func (s *jobStore) putLocked(job *Job) {
	if _, exists := s.jobs[job.ID]; !exists {
		s.order = append(s.order, job.ID)
	}
	s.jobs[job.ID] = cloneJob(job)

	if len(s.order) > s.capacity {
		for len(s.order) > s.capacity {
			idx := s.firstEvictableLocked()
			if idx < 0 {
				break
			}
			toEvict := s.order[idx]
			delete(s.jobs, toEvict)
			s.order = append(s.order[:idx], s.order[idx+1:]...)
		}
	}
}

func (s *jobStore) firstEvictableLocked() int {
	for i, id := range s.order {
		job := s.jobs[id]
		if job == nil || isTerminalJobStatus(job.Status) {
			return i
		}
	}
	return -1
}

func isTerminalJobStatus(status JobStatus) bool {
	return status == JobCompleted || status == JobFailed
}

func (s *jobStore) get(id string) (*Job, bool) {
	job, ok, _ := s.getWithError(id)
	return job, ok
}

func (s *jobStore) getWithError(id string) (*Job, bool, error) {
	s.mu.RLock()
	job, ok := s.jobs[id]
	job = cloneJob(job)
	s.mu.RUnlock()

	if ok && (s.durable == nil || !isTerminalJobStatus(job.Status)) {
		return job, true, nil
	}
	if s.durable == nil {
		return nil, false, nil
	}

	stored, found, err := s.durable.getAny(id, s.durable.currentTime())
	if err != nil {
		return nil, false, err
	}
	if !found {
		if ok {
			s.mu.Lock()
			delete(s.jobs, id)
			for i, existingID := range s.order {
				if existingID == id {
					s.order = append(s.order[:i], s.order[i+1:]...)
					break
				}
			}
			s.mu.Unlock()
		}
		return nil, false, nil
	}

	s.mu.Lock()
	s.putLocked(stored)
	s.mu.Unlock()
	return cloneJob(stored), true, nil
}

func (s *jobStore) update(id string, fn func(*Job) error) (*Job, error) {
	if _, ok, err := s.getWithError(id); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.New("job not found")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return nil, errors.New("job not found")
	}

	copyJob := cloneJob(job)
	if err := fn(copyJob); err != nil {
		return nil, err
	}
	copyJob.UpdatedAt = time.Now().UTC()
	if s.durable != nil {
		copyJob.UpdatedAt = s.durable.currentTime()
		copyJob.DetailsAvailable = true
		if err := s.durable.upsert(copyJob); err != nil {
			return nil, err
		}
	}
	s.jobs[id] = copyJob
	return cloneJob(copyJob), nil
}

func (s *jobStore) updateAtomic(db *sql.DB, id string, fn func(*Job) error, extra func(*sql.Tx, *Job) error) (*Job, error) {
	if s == nil || s.durable == nil || db == nil {
		return nil, errors.New("durable job transaction is not configured")
	}
	if _, ok, err := s.getWithError(id); err != nil {
		return nil, err
	} else if !ok {
		return nil, errors.New("job not found")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.jobs[id]
	if current == nil {
		return nil, errors.New("job not found")
	}
	updated := cloneJob(current)
	if err := fn(updated); err != nil {
		return nil, err
	}
	updated.UpdatedAt = s.durable.currentTime()
	updated.DetailsAvailable = true

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if extra != nil {
		if err := extra(tx, updated); err != nil {
			return nil, err
		}
	}
	if err := s.durable.writeTx(tx, updated, false, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		stored, found, readErr := s.durable.getAny(id, s.durable.currentTime())
		if readErr == nil && found && sameJobPayload(stored, updated) {
			s.jobs[id] = stored
			return cloneJob(stored), nil
		}
		if readErr != nil {
			return nil, &atomicCommitError{err: errors.Join(err, readErr), uncertain: true}
		}
		return nil, &atomicCommitError{err: err}
	}
	s.jobs[id] = updated
	return cloneJob(updated), nil
}

func sameJobPayload(left, right *Job) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func cloneJob(job *Job) *Job {
	if job == nil {
		return nil
	}

	copyJob := *job
	if len(job.Items) > 0 {
		copyJob.Items = append([]DownloadItem(nil), job.Items...)
	}
	if len(job.AccountAttempts) > 0 {
		copyJob.AccountAttempts = append([]AccountAttempt(nil), job.AccountAttempts...)
	}
	if job.Share != nil {
		shareCopy := *job.Share
		if len(job.Share.SelectedIDs) > 0 {
			shareCopy.SelectedIDs = append([]string(nil), job.Share.SelectedIDs...)
		}
		if len(job.Share.SelectedItems) > 0 {
			shareCopy.SelectedItems = append([]DownloadItem(nil), job.Share.SelectedItems...)
		}
		copyJob.Share = &shareCopy
	}
	if job.Result != nil {
		resultCopy := *job.Result
		resultCopy.File = job.Result.File
		copyJob.Result = &resultCopy
	}
	if len(job.Results) > 0 {
		copyJob.Results = append([]JobResult(nil), job.Results...)
	}
	if len(job.Warnings) > 0 {
		copyJob.Warnings = append([]string(nil), job.Warnings...)
	}
	if len(job.TempIDs) > 0 {
		copyJob.TempIDs = append([]string(nil), job.TempIDs...)
	}
	if job.Batch != nil {
		batchCopy := *job.Batch
		if len(job.Batch.Failures) > 0 {
			batchCopy.Failures = append([]BatchFailure(nil), job.Batch.Failures...)
		}
		copyJob.Batch = &batchCopy
	}
	return &copyJob
}

func newJobID() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func detectResourceKind(input string) (ResourceKind, error) {
	cleaned := strings.TrimSpace(input)
	switch {
	case strings.HasPrefix(strings.ToLower(cleaned), "magnet:?"):
		return ResourceMagnet, nil
	case looksLikeShareLink(cleaned):
		return ResourceShare, nil
	default:
		return "", errors.New("only magnet links and PikPak share links are supported")
	}
}

func looksLikeShareLink(input string) bool {
	lower := strings.ToLower(input)
	return strings.Contains(lower, "pikpak.com/s/") || strings.Contains(lower, "mypikpak.com/s/")
}

// splitResourceLines breaks a submission into individual links, one per line. It
// trims surrounding whitespace, drops blank lines, and removes exact duplicates
// while preserving first-seen order. A single-link submission therefore yields a
// one-element slice, so callers can branch on len() to keep the existing
// single-job behavior intact.
func splitResourceLines(input string) []string {
	specs := splitResourceLineSpecs(input)
	lines := make([]string, 0, len(specs))
	for _, spec := range specs {
		lines = append(lines, spec.clean)
	}
	return lines
}

type resourceLineSpec struct {
	raw   string
	clean string
}

func splitResourceLineSpecs(input string) []resourceLineSpec {
	seen := make(map[string]bool)
	var specs []resourceLineSpec
	for _, raw := range strings.Split(input, "\n") {
		raw = strings.TrimSpace(raw)
		clean := normalizeResourceInput(raw)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		specs = append(specs, resourceLineSpec{raw: raw, clean: clean})
	}
	return specs
}

func normalizeResourceInput(input string) string {
	cleaned := strings.TrimSpace(input)
	if strings.HasPrefix(strings.ToLower(cleaned), "magnet:?") {
		return normalizeMagnetLink(cleaned)
	}
	if looksLikeShareLink(cleaned) {
		return normalizeShareLink(cleaned)
	}
	return cleaned
}

func normalizeShareLink(input string) string {
	cleaned := strings.TrimSpace(input)
	for _, sep := range []string{"?", "#"} {
		if cut := strings.Index(cleaned, sep); cut >= 0 {
			cleaned = strings.TrimSpace(cleaned[:cut])
		}
	}
	return cleaned
}

type parsedShareLink struct {
	ShareID  string
	TailID   string
	PassCode string
}

func parseShareLink(input string) (shareID, tailID string, err error) {
	parts, err := parseShareLinkParts(input)
	if err != nil {
		return "", "", err
	}
	return parts.ShareID, parts.TailID, nil
}

func parseShareLinkParts(input string) (parsedShareLink, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return parsedShareLink{}, errors.New("invalid PikPak share link")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return parsedShareLink{}, errors.New("invalid PikPak share link")
	}

	pathParts := splitSharePath(u.Path)
	if len(pathParts) == 0 {
		// url.Parse treats "mypikpak.com/s/..." as a relative path, so u.Path
		// already works. This fallback is mainly for malformed-but-pasted text
		// that still contains a query/fragment after a valid share path.
		pathParts = splitSharePath(trimURLSuffix(raw))
	}
	if len(pathParts) == 0 {
		return parsedShareLink{}, errors.New("invalid PikPak share link")
	}

	parts := parsedShareLink{
		ShareID:  pathParts[0],
		PassCode: sharePassCodeFromQuery(u.Query()),
	}
	if len(pathParts) > 1 {
		parts.TailID = pathParts[len(pathParts)-1]
	}
	return parts, nil
}

func shareStateAndPassCode(input, defaultPassCode string) (*ShareState, string, error) {
	parts, err := parseShareLinkParts(input)
	if err != nil {
		return nil, "", err
	}
	passCode := strings.TrimSpace(defaultPassCode)
	if passCode == "" {
		passCode = parts.PassCode
	}
	return &ShareState{ShareID: parts.ShareID, TailID: parts.TailID}, passCode, nil
}

func splitSharePath(pathValue string) []string {
	chunks := strings.Split(pathValue, "/")
	for i, chunk := range chunks {
		if !strings.EqualFold(strings.TrimSpace(chunk), "s") {
			continue
		}
		if i+1 >= len(chunks) {
			return nil
		}

		shareID := cleanPathPart(chunks[i+1])
		if shareID == "" {
			return nil
		}
		parts := []string{shareID}
		for _, chunk := range chunks[i+2:] {
			if part := cleanPathPart(chunk); part != "" {
				parts = append(parts, part)
			}
		}
		return parts
	}
	return nil
}

func cleanPathPart(part string) string {
	part = strings.TrimSpace(part)
	if part == "" {
		return ""
	}
	if unescaped, err := url.PathUnescape(part); err == nil {
		part = unescaped
	}
	return strings.TrimSpace(part)
}

func trimURLSuffix(input string) string {
	if cut := strings.IndexAny(input, "?#"); cut >= 0 {
		return input[:cut]
	}
	return input
}

func sharePassCodeFromQuery(query url.Values) string {
	for key, values := range query {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
		switch normalized {
		case "pwd", "passcode", "password":
			for _, value := range values {
				if value = strings.TrimSpace(value); value != "" {
					return value
				}
			}
		}
	}
	return ""
}

func sortItems(items []DownloadItem) {
	sort.Slice(items, func(i, j int) bool {
		left := items[i].Path
		right := items[j].Path
		if left == right {
			return items[i].Name < items[j].Name
		}
		return left < right
	})
}
