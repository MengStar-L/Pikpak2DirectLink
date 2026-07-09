package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// --- shared job-selection helper (used by admin and CDK-user endpoints) ---

type selectionUpdateError struct {
	status  int
	message string
}

func (e selectionUpdateError) Error() string {
	return e.message
}

func selectionHTTPError(status int, message string) error {
	return selectionUpdateError{status: status, message: message}
}

func selectionErrorResponse(err error) (int, string) {
	var selectionErr selectionUpdateError
	if errors.As(err, &selectionErr) {
		return selectionErr.status, selectionErr.message
	}
	return http.StatusConflict, err.Error()
}

func (s *Server) jobQuotaSelectionError(job *Job, size int64) error {
	if job == nil || size <= 0 {
		return nil
	}
	if job.UserID != "" && s.users != nil {
		return s.users.hasQuota(job.UserID, size, job.Mode == "proxy", s.now())
	}
	if job.CDKCode != "" && s.cdk != nil {
		c, ok, err := s.cdk.get(job.CDKCode)
		if err == nil && ok && size > c.RemainingBytes {
			return errCDKOverdraw{size: size, remaining: c.RemainingBytes}
		}
	}
	return nil
}

func quotaSelectionMessage(err error) string {
	var userOverdraw errUserQuotaOverdraw
	if errors.As(err, &userOverdraw) {
		return "Selected files exceed remaining quota."
	}
	var cdkOverdraw errCDKOverdraw
	if errors.As(err, &cdkOverdraw) {
		return "Selected files exceed remaining CDK traffic."
	}
	if errors.Is(err, errUserQuotaExhausted) {
		return "User quota has been used up."
	}
	return err.Error()
}

// applyItemSelection resolves a pending selection on a job and kicks the next
// stage. It returns the updated job, or an HTTP status code + message on error
// (status 0 means success).
func (s *Server) applyItemSelection(jobID, itemID string) (*Job, int, string) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, http.StatusBadRequest, "item_id is required"
	}

	job, ok := s.jobs.get(jobID)
	if !ok {
		return nil, http.StatusNotFound, "job not found"
	}
	if job.Status != JobSelectionRequired {
		return nil, http.StatusConflict, "job is not waiting for a selection"
	}

	var selectedItem *DownloadItem
	if job.Stage == StageResultSelection {
		for _, item := range job.Items {
			if item.ID == itemID {
				copyItem := item
				selectedItem = &copyItem
				break
			}
		}
		if selectedItem == nil {
			return nil, http.StatusBadRequest, "selected file was not found in the current result set"
		}
		if err := s.jobQuotaSelectionError(job, parseBytes(selectedItem.Size)); err != nil {
			return nil, http.StatusForbidden, quotaSelectionMessage(err)
		}
	}

	_, err := s.jobs.update(jobID, func(current *Job) error {
		if current.Status != JobSelectionRequired {
			return selectionHTTPError(http.StatusConflict, "job is not waiting for a selection")
		}
		current.Status = JobQueued
		current.Message = "queued"
		current.Error = ""
		switch current.Stage {
		case StageSourceSelection:
			if current.Share == nil {
				return selectionHTTPError(http.StatusConflict, "share context is missing")
			}
			var selected DownloadItem
			found := false
			for _, item := range current.Items {
				if item.ID == itemID {
					selected = item
					found = true
					break
				}
			}
			if !found {
				return selectionHTTPError(http.StatusBadRequest, "selected item was not found in the current share")
			}
			current.Items = nil
			current.Share.SelectedID = itemID
			current.Share.SelectedIDs = []string{itemID}
			current.Share.SelectedItems = []DownloadItem{selected}
			current.Stage = StageTransfer
			current.ResolveSelected = true
		case StageResultSelection:
			current.Message = "queued"
		default:
			return selectionHTTPError(http.StatusConflict, "job cannot accept selections right now")
		}
		return nil
	})
	if err != nil {
		status, msg := selectionErrorResponse(err)
		return nil, status, msg
	}

	// Resume continuations jump to the front of the queue: the user already
	// waited a turn and just made a choice, so they shouldn't re-queue behind
	// everyone else.
	if job.Stage == StageSourceSelection {
		s.resolver.enqueue(jobID, priorityResume, func(ctx context.Context) {
			s.processJob(ctx, jobID)
		})
	} else {
		item := *selectedItem
		s.resolver.enqueue(jobID, priorityResume, func(ctx context.Context) {
			s.resolveExistingFile(ctx, jobID, item)
		})
	}

	updated, _ := s.jobs.get(jobID)
	return updated, 0, ""
}

// applyItemsSelection is the multi-select counterpart of applyItemSelection.
// It accepts several selected files, gates them against the CDK's remaining
// traffic as a SUM, and resolves each into its own link.
func (s *Server) applyItemsSelection(jobID string, itemIDs []string) (*Job, int, string) {
	seen := make(map[string]bool, len(itemIDs))
	ordered := make([]string, 0, len(itemIDs))
	for _, id := range itemIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ordered = append(ordered, id)
	}
	if len(ordered) == 0 {
		return nil, http.StatusBadRequest, "请至少选择一个文件"
	}
	if len(ordered) > maxSelectedFilesPerResolve {
		return nil, http.StatusBadRequest, fmt.Sprintf("单次最多解析 %d 个文件，请减少选择后重试", maxSelectedFilesPerResolve)
	}

	job, ok := s.jobs.get(jobID)
	if !ok {
		return nil, http.StatusNotFound, "job not found"
	}
	if job.Status != JobSelectionRequired {
		return nil, http.StatusConflict, "job is not waiting for a selection"
	}

	if job.Stage == StageSourceSelection {
		byID := make(map[string]DownloadItem, len(job.Items))
		for _, item := range job.Items {
			byID[item.ID] = item
		}
		var totalSize int64
		for _, id := range ordered {
			item, ok := byID[id]
			if !ok {
				return nil, http.StatusBadRequest, "selected item was not found in the current share"
			}
			totalSize += parseBytes(item.Size)
		}
		if err := s.jobQuotaSelectionError(job, totalSize); err != nil {
			return nil, http.StatusForbidden, quotaSelectionMessage(err)
		}

		_, err := s.jobs.update(jobID, func(current *Job) error {
			if current.Status != JobSelectionRequired {
				return selectionHTTPError(http.StatusConflict, "job is not waiting for a selection")
			}
			if current.Stage != StageSourceSelection {
				return selectionHTTPError(http.StatusConflict, "job cannot accept selections right now")
			}
			if current.Share == nil {
				return selectionHTTPError(http.StatusConflict, "share context is missing")
			}
			byID := make(map[string]DownloadItem, len(current.Items))
			for _, item := range current.Items {
				byID[item.ID] = item
			}
			selected := make([]DownloadItem, 0, len(ordered))
			for _, id := range ordered {
				item, ok := byID[id]
				if !ok {
					return selectionHTTPError(http.StatusBadRequest, "selected item was not found in the current share")
				}
				selected = append(selected, item)
			}
			current.Status = JobQueued
			current.Message = "queued"
			current.Error = ""
			current.Items = nil
			current.Share.SelectedID = ordered[0]
			current.Share.SelectedIDs = append([]string(nil), ordered...)
			current.Share.SelectedItems = selected
			current.Stage = StageTransfer
			current.ResolveSelected = true
			return nil
		})
		if err != nil {
			status, msg := selectionErrorResponse(err)
			return nil, status, msg
		}

		s.resolver.enqueue(jobID, priorityResume, func(ctx context.Context) {
			s.processJob(ctx, jobID)
		})

		updated, _ := s.jobs.get(jobID)
		return updated, 0, ""
	}
	if job.Stage != StageResultSelection {
		return nil, http.StatusConflict, "job cannot accept selections right now"
	}

	byID := make(map[string]DownloadItem, len(job.Items))
	for _, item := range job.Items {
		byID[item.ID] = item
	}
	selected := make([]DownloadItem, 0, len(ordered))
	var totalSize int64
	for _, id := range ordered {
		item, ok := byID[id]
		if !ok {
			return nil, http.StatusBadRequest, "selected file was not found in the current result set"
		}
		selected = append(selected, item)
		totalSize += parseBytes(item.Size)
	}

	if err := s.jobQuotaSelectionError(job, totalSize); err != nil {
		return nil, http.StatusForbidden, quotaSelectionMessage(err)
	}

	_, err := s.jobs.update(jobID, func(current *Job) error {
		if current.Status != JobSelectionRequired {
			return selectionHTTPError(http.StatusConflict, "job is not waiting for a selection")
		}
		if current.Stage != StageResultSelection {
			return selectionHTTPError(http.StatusConflict, "job cannot accept selections right now")
		}
		byID := make(map[string]struct{}, len(current.Items))
		for _, item := range current.Items {
			byID[item.ID] = struct{}{}
		}
		for _, id := range ordered {
			if _, ok := byID[id]; !ok {
				return selectionHTTPError(http.StatusBadRequest, "selected file was not found in the current result set")
			}
		}
		current.Status = JobQueued
		current.Message = "queued"
		current.Error = ""
		return nil
	})
	if err != nil {
		status, msg := selectionErrorResponse(err)
		return nil, status, msg
	}

	s.resolver.enqueue(jobID, priorityResume, func(ctx context.Context) {
		s.resolveExistingFiles(ctx, jobID, selected)
	})

	updated, _ := s.jobs.get(jobID)
	return updated, 0, ""
}

// --- admin CDK management ---

type createCDKRequest struct {
	Count      int  `json:"count"`
	TrafficGB  int  `json:"traffic_gb"`
	Days       int  `json:"days"`
	AllowProxy bool `json:"allow_proxy"`
}

type updateCDKRequest struct {
	TrafficGB  int  `json:"traffic_gb"`
	Days       int  `json:"days"`
	AllowProxy bool `json:"allow_proxy"`
}

func (s *Server) handleListCDKs(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	cdks, err := s.cdk.list()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]cdkView, 0, len(cdks))
	for _, c := range cdks {
		views = append(views, toCDKView(c, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"cdks": views})
}

func (s *Server) handleCreateCDKs(w http.ResponseWriter, r *http.Request) {
	var req createCDKRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Count < 1 {
		writeError(w, http.StatusBadRequest, "分发数量至少为 1")
		return
	}
	if req.Count > maxCDKBatch {
		writeError(w, http.StatusBadRequest, "单次最多分发 100 个")
		return
	}
	if req.TrafficGB < 1 {
		writeError(w, http.StatusBadRequest, "流量额度至少为 1G")
		return
	}
	if req.Days < 1 {
		writeError(w, http.StatusBadRequest, "到期天数至少为 1")
		return
	}

	now := time.Now()
	created, err := s.cdk.createBatch(req.Count, int64(req.TrafficGB)*bytesPerGB, req.Days, req.AllowProxy, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	views := make([]cdkView, 0, len(created))
	for _, c := range created {
		views = append(views, toCDKView(c, now))
	}
	proxyLabel := "否"
	if req.AllowProxy {
		proxyLabel = "是"
	}
	s.logJob(LogSuccess, "", "已分发 CDK", "数量："+itoa(len(created)), "流量额度："+itoa(req.TrafficGB)+"G", "有效天数："+itoa(req.Days), "支持中转："+proxyLabel)
	writeJSON(w, http.StatusCreated, map[string]any{"cdks": views})
}

func (s *Server) handleUpdateCDK(w http.ResponseWriter, r *http.Request) {
	code := normalizeCode(r.PathValue("code"))
	var req updateCDKRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TrafficGB < 0 {
		writeError(w, http.StatusBadRequest, "流量额度不能为负")
		return
	}
	if req.Days < 1 {
		writeError(w, http.StatusBadRequest, "到期天数至少为 1")
		return
	}

	now := time.Now()
	updated, ok, err := s.cdk.update(code, int64(req.TrafficGB)*bytesPerGB, req.Days, req.AllowProxy, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "CDK 不存在")
		return
	}
	writeJSON(w, http.StatusOK, toCDKView(updated, now))
}

func (s *Server) handleDeleteCDK(w http.ResponseWriter, r *http.Request) {
	code := normalizeCode(r.PathValue("code"))
	_, ok, err := s.cdk.revoke(code, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "CDK 不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// handleDeleteExpiredCDKs removes every CDK that has already expired and reports
// how many were cleared.
func (s *Server) handleDeleteExpiredCDKs(w http.ResponseWriter, _ *http.Request) {
	deleted, err := s.cdk.deleteExpired(time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if deleted > 0 {
		s.logJob(LogSuccess, "", "已清理过期 CDK", "数量："+itoa(int(deleted)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// --- user portal handlers live in user_handlers.go ---

// userJobView is the CDK-user-facing projection of a Job. It deliberately omits
// admin-only details such as which PikPak account was used.
type userJobView struct {
	ID         string         `json:"id"`
	Kind       ResourceKind   `json:"kind"`
	Mode       string         `json:"mode"`
	Status     JobStatus      `json:"status"`
	Stage      JobStage       `json:"stage"`
	Message    string         `json:"message,omitempty"`
	Error      string         `json:"error,omitempty"`
	Items      []DownloadItem `json:"items,omitempty"`
	Result     *JobResult     `json:"result,omitempty"`
	Results    []JobResult    `json:"results,omitempty"`
	Batch      *BatchProgress `json:"batch,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	QueueAhead int            `json:"queue_ahead"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// genericUserJobError is the fallback failure text a CDK user sees. Internal
// errors are deliberately collapsed into it because the raw error can embed
// platform secrets — most notably the PikPak account usernames, which
// processJob concatenates into the "all accounts failed" error. CDK users must
// never receive those.
const genericUserJobError = "解析失败，请稍后重试；如多次失败请联系管理员。"

// toUserJobView is the CDK-user-facing projection of a Job. It deliberately
// omits admin-only details such as which PikPak account was used, and — for the
// same reason — never forwards the raw Message or Error, both of which can carry
// the platform account username. The progress message is reconstructed from the
// job's status/stage so it stays informative without leaking anything.
func toUserJobView(job *Job) userJobView {
	view := userJobView{
		ID:        job.ID,
		Kind:      job.Kind,
		Mode:      job.Mode,
		Status:    job.Status,
		Stage:     job.Stage,
		Message:   safeUserMessage(job),
		Items:     job.Items,
		Result:    job.Result,
		Results:   job.Results,
		Batch:     job.Batch,
		Warnings:  job.Warnings,
		CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt,
	}
	if job.Status == JobFailed || job.Error != "" {
		view.Error = safeUserError(job.Error)
	}
	return view
}

func safeUserError(message string) string {
	message = strings.TrimSpace(message)
	if isBadResourceUserError(message) {
		return badResourceParseUserError
	}
	if message == "" {
		return genericUserJobError
	}
	if message == errCDKNotFound.Error() {
		return "CDK is invalid."
	}
	if message == errCDKExpired.Error() {
		return "CDK has expired."
	}
	if message == errCDKExhausted.Error() {
		return "CDK traffic has been used up."
	}
	if message == errUserQuotaExhausted.Error() {
		return "User quota has been used up."
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "user quota") || strings.Contains(lower, "remaining quota") {
		return "Selected files exceed remaining quota."
	}
	if strings.Contains(lower, "cdk") && strings.Contains(lower, "traffic") {
		return "Selected files exceed remaining CDK traffic."
	}
	if strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "timeout") ||
		strings.Contains(message, "超时") {
		return "Parsing timed out. Please try again later."
	}
	if isResourceUnavailableMessage(lower) {
		return friendlyPikPakMessage(message)
	}
	return genericUserJobError
}

// safeUserMessage derives a CDK-user-facing progress string purely from the
// job's status and stage. The job's internal Message is never exposed because
// it can embed platform details (e.g. "starting with <account-username>").
func safeUserMessage(job *Job) string {
	switch job.Status {
	case JobQueued:
		// The portal overrides this with a queue-position string; this is the
		// fallback before the first poll.
		return "排队中"
	case JobRunning:
		// A batch parent reports its rollup; the count carries no platform detail.
		if job.Kind == ResourceBatch && job.Batch != nil {
			return fmt.Sprintf("解析中：%d/%d 条完成", job.Batch.Succeeded+job.Batch.Failed, job.Batch.Total)
		}
		return "正在解析，请稍候…"
	case JobSelectionRequired:
		if job.Stage == StageSourceSelection {
			return "请选择要转存的项目"
		}
		return "请选择要生成链接的文件"
	case JobCompleted:
		if job.Kind == ResourceBatch && job.Batch != nil {
			return fmt.Sprintf("解析成功 %d/%d 条", job.Batch.Succeeded, job.Batch.Total)
		}
		return "解析完成"
	default:
		// Failed jobs carry their text in Error; anything else gets no message.
		return ""
	}
}

// maskCDK keeps just the leading block of a code for log correlation without
// printing the whole credential.
func maskCDK(code string) string {
	if len(code) <= 4 {
		return code
	}
	return code[:4] + "…"
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
