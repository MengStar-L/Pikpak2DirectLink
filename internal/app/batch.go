package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// batchState tracks the live progress of one multi-link parent job. Each child's
// results are copied here the moment it finishes, so they survive even if the
// jobStore later evicts the child. The parent job is updated from this state on
// every child completion (live x/N) and written its final result set when the
// last child lands.
type batchState struct {
	mu        sync.Mutex
	parentID  string
	baseURL   string
	total     int
	done      int
	succeeded int
	results   []JobResult
	failures  []BatchFailure
}

func (s *Server) registerBatch(bs *batchState) {
	s.batchMu.Lock()
	s.batches[bs.parentID] = bs
	s.batchMu.Unlock()
}

func (s *Server) batchByID(parentID string) *batchState {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	return s.batches[parentID]
}

func (s *Server) removeBatch(parentID string) {
	s.batchMu.Lock()
	delete(s.batches, parentID)
	s.batchMu.Unlock()
}

// childSpec is one validated line of a batch submission.
type childSpec struct {
	input    string
	kind     ResourceKind
	share    *ShareState
	passCode string
	label    string
}

// createBatchJob validates every line, then creates a parent job plus one child
// job per line. Children carry ResolveAll so they resolve every file unattended,
// and are enqueued at the given priority so they fan out across the resolve queue
// under the admin's concurrency limit. It returns the parent job, or an HTTP
// status + message when a line cannot be recognized (status 0 means success).
func (s *Server) createBatchJob(lines []string, mode, defaultPassCode, cdkCode string, priority int, baseURL string) (*Job, int, string) {
	specs := make([]childSpec, 0, len(lines))
	for i, line := range lines {
		kind, err := detectResourceKind(line)
		if err != nil {
			return nil, 400, fmt.Sprintf("第 %d 行无法识别：%s", i+1, err.Error())
		}
		spec := childSpec{input: line, kind: kind, label: batchLinkLabel(i+1, line, kind)}
		if kind == ResourceShare {
			share, passCode, err := shareStateAndPassCode(line, defaultPassCode)
			if err != nil {
				return nil, 400, fmt.Sprintf("第 %d 行分享链接无效：%s", i+1, err.Error())
			}
			spec.share = share
			spec.passCode = passCode
		}
		specs = append(specs, spec)
	}

	now := time.Now()
	parentID := newJobID()
	parent := &Job{
		ID:        parentID,
		Kind:      ResourceBatch,
		Mode:      mode,
		Input:     strings.Join(lines, "\n"),
		Status:    JobRunning,
		Stage:     StageTransfer,
		Message:   fmt.Sprintf("解析中：0/%d 条完成", len(specs)),
		BaseURL:   baseURL,
		CDKCode:   cdkCode,
		Batch:     &BatchProgress{Total: len(specs)},
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.jobs.create(parent)

	bs := &batchState{parentID: parentID, baseURL: baseURL, total: len(specs)}
	s.registerBatch(bs)

	// Build and store every child before enqueuing any, so a child that finishes
	// quickly always finds its parent and batch state in place.
	type childRun struct {
		id    string
		label string
	}
	runs := make([]childRun, 0, len(specs))
	for _, spec := range specs {
		childID := newJobID()
		child := &Job{
			ID:         childID,
			Kind:       spec.kind,
			Mode:       mode,
			Input:      spec.input,
			PassCode:   spec.passCode,
			Status:     JobQueued,
			Stage:      StageTransfer,
			Message:    "queued",
			BaseURL:    baseURL,
			CDKCode:    cdkCode,
			ParentID:   parentID,
			ResolveAll: true,
			Share:      spec.share,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		s.jobs.create(child)
		runs = append(runs, childRun{id: childID, label: spec.label})
	}

	s.logJob(LogInfo, parentID, fmt.Sprintf("批量解析任务已创建，共 %d 条链接", len(specs)))
	for _, r := range runs {
		childID, label := r.id, r.label
		s.resolver.enqueue(childID, priority, func(ctx context.Context) {
			// batchChildDone must run even if processJob panics, or the parent would
			// hang forever waiting on a child that never reports. The resolve queue's
			// own recover then fails the child job for logging.
			defer s.batchChildDone(parentID, childID, label)
			s.processJob(ctx, childID)
		})
	}

	updated, _ := s.jobs.get(parentID)
	return updated, 0, ""
}

// batchChildDone is the continuation run after a child job reaches a terminal
// state. It folds the child's results into the parent and, once every child has
// reported, writes the parent's final result set and "x/x" message.
func (s *Server) batchChildDone(parentID, childID, label string) {
	bs := s.batchByID(parentID)
	if bs == nil {
		return
	}
	child, ok := s.jobs.get(childID)

	// Hold bs.mu across the parent update so concurrent children apply their
	// updates in counter order — otherwise a slower second-to-last child could
	// overwrite the final "completed" state with a stale "running k/N".
	bs.mu.Lock()
	defer bs.mu.Unlock()

	bs.done++
	if ok && child.Status == JobCompleted {
		for _, r := range child.Results {
			merged := r
			merged.File.Path = label + "/" + childResultPath(r.File)
			merged.ProxyURL = proxyURLForParent(bs.baseURL, parentID, r.ProxyToken)
			bs.results = append(bs.results, merged)
		}
		bs.succeeded++
	} else if ok && child.Status == JobFailed && isBadResourceUserError(child.Error) {
		bs.failures = append(bs.failures, BatchFailure{
			Label: label,
			Error: badResourceParseUserError,
		})
	}
	done, total, succeeded := bs.done, bs.total, bs.succeeded
	results := append([]JobResult(nil), bs.results...)
	failures := append([]BatchFailure(nil), bs.failures...)

	final := done >= total
	_, _ = s.jobs.update(parentID, func(p *Job) error {
		if p.Batch == nil {
			p.Batch = &BatchProgress{}
		}
		p.Batch.Total = total
		p.Batch.Succeeded = succeeded
		p.Batch.Failed = done - succeeded
		p.Batch.Failures = failures
		if !final {
			p.Status = JobRunning
			p.Message = fmt.Sprintf("解析中：%d/%d 条完成", done, total)
			return nil
		}
		p.Results = results
		p.Items = nil
		p.Message = fmt.Sprintf("解析成功 %d/%d 条", succeeded, total)
		if succeeded > 0 {
			p.Status = JobCompleted
			p.Stage = StageComplete
			p.Error = ""
		} else {
			p.Status = JobFailed
			p.Stage = StageFailed
			p.Error = "全部链接解析失败"
		}
		return nil
	})

	if final {
		s.removeBatch(parentID)
		s.logJob(LogSuccess, parentID, fmt.Sprintf("批量解析完成，成功 %d/%d 条", succeeded, total))
	}
}

// childResultPath is the in-link path of a resolved file, falling back to its
// name when the path is empty.
func childResultPath(file DownloadItem) string {
	if p := strings.TrimSpace(file.Path); p != "" {
		return p
	}
	return file.Name
}

// proxyURLForParent rebuilds a result's proxy link to point at the parent job, so
// the link keeps working after the child job is evicted from the store.
func proxyURLForParent(baseURL, parentID, token string) string {
	if token == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/proxy/" + parentID + "?token=" + token
}

// batchLinkLabel is the top-level sibling-folder name for one link's files. It is
// always prefixed with "链接N" for uniqueness, plus a friendly name pulled from
// the link when one is available.
func batchLinkLabel(index int, input string, kind ResourceKind) string {
	base := fmt.Sprintf("链接%d", index)
	if name := linkDisplayName(input, kind); name != "" {
		return base + " " + name
	}
	return base
}

func linkDisplayName(input string, kind ResourceKind) string {
	switch kind {
	case ResourceMagnet:
		if u, err := url.Parse(strings.TrimSpace(input)); err == nil {
			if dn := strings.TrimSpace(u.Query().Get("dn")); dn != "" {
				return sanitizePathSegment(dn)
			}
		}
	case ResourceShare:
		if shareID, _, err := parseShareLink(input); err == nil {
			return sanitizePathSegment(shareID)
		}
	}
	return ""
}

// sanitizePathSegment strips characters that would break the "/"-delimited path
// used to build the result tree.
func sanitizePathSegment(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r == '/' || r == '\\' || r < 0x20 {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}
