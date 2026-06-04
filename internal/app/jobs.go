package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type ResourceKind string

const (
	ResourceMagnet ResourceKind = "magnet"
	ResourceShare  ResourceKind = "share"
)

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
	File      DownloadItem `json:"file"`
	DirectURL string       `json:"direct_url"`
	ProxyURL  string       `json:"proxy_url"`
	ExpiresAt string       `json:"expires_at,omitempty"`
}

type AccountAttempt struct {
	AccountID string `json:"account_id"`
	Username  string `json:"username"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

type ShareState struct {
	ShareID       string `json:"share_id"`
	TailID        string `json:"tail_id,omitempty"`
	PassCodeToken string `json:"pass_code_token,omitempty"`
	SelectedID    string `json:"selected_id,omitempty"`
}

type Job struct {
	ID              string       `json:"id"`
	Kind            ResourceKind `json:"kind"`
	Mode            string       `json:"mode"`
	Input           string       `json:"input"`
	PassCode        string       `json:"pass_code,omitempty"`
	Status          JobStatus    `json:"status"`
	Stage           JobStage     `json:"stage"`
	Message         string       `json:"message,omitempty"`
	Error           string       `json:"error,omitempty"`
	BaseURL         string       `json:"-"`
	FolderID        string       `json:"-"`
	AccountID       string       `json:"account_id,omitempty"`
	Share           *ShareState  `json:"share,omitempty"`
	Items           []DownloadItem
	AccountAttempts []AccountAttempt `json:"account_attempts,omitempty"`
	Result          *JobResult       `json:"result,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func newJobStore() *jobStore {
	return &jobStore{
		jobs: make(map[string]*Job),
	}
}

func (s *jobStore) create(job *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = cloneJob(job)
}

func (s *jobStore) get(id string) (*Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	return cloneJob(job), true
}

func (s *jobStore) update(id string, fn func(*Job) error) (*Job, error) {
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
	copyJob.UpdatedAt = time.Now()
	s.jobs[id] = copyJob
	return cloneJob(copyJob), nil
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
		copyJob.Share = &shareCopy
	}
	if job.Result != nil {
		resultCopy := *job.Result
		resultCopy.File = job.Result.File
		copyJob.Result = &resultCopy
	}
	return &copyJob
}

func newJobID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return time.Now().UTC().Format("20060102150405")
	}
	return hex.EncodeToString(buf)
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

var shareLinkPattern = regexp.MustCompile(`(?i)/s/([^/?#]+)(?:/(.*))?$`)

func parseShareLink(input string) (shareID, tailID string, err error) {
	matches := shareLinkPattern.FindStringSubmatch(strings.TrimSpace(input))
	if len(matches) < 2 {
		return "", "", errors.New("invalid PikPak share link")
	}

	shareID = strings.TrimSpace(matches[1])
	if shareID == "" {
		return "", "", errors.New("invalid PikPak share link")
	}

	if len(matches) >= 3 && matches[2] != "" {
		chunks := strings.Split(matches[2], "/")
		for i := len(chunks) - 1; i >= 0; i-- {
			part := strings.TrimSpace(chunks[i])
			if part != "" {
				tailID = part
				break
			}
		}
	}

	return shareID, tailID, nil
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
