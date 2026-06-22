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
		// For CDK users the file size is known at this point, so refuse a pick
		// that would exceed the CDK's remaining traffic instead of silently
		// absorbing the overage at charge time (charge only clamps at zero).
		if job.CDKCode != "" {
			size := parseBytes(selectedItem.Size)
			if c, ok, err := s.cdk.get(job.CDKCode); err == nil && ok && size > c.RemainingBytes {
				return nil, http.StatusForbidden, "所选文件大小 " + formatTrafficLabel(size) + " 超过 CDK 剩余流量（剩余 " + formatTrafficLabel(c.RemainingBytes) + "），请选择更小的文件"
			}
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
			found := false
			for _, item := range current.Items {
				if item.ID == itemID {
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
		if job.CDKCode != "" {
			if c, ok, err := s.cdk.get(job.CDKCode); err == nil && ok && totalSize > c.RemainingBytes {
				return nil, http.StatusForbidden, "所选文件合计 " + formatTrafficLabel(totalSize) + " 超过 CDK 剩余流量（剩余 " + formatTrafficLabel(c.RemainingBytes) + "），请减少选择"
			}
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
			byID := make(map[string]struct{}, len(current.Items))
			for _, item := range current.Items {
				byID[item.ID] = struct{}{}
			}
			for _, id := range ordered {
				if _, ok := byID[id]; !ok {
					return selectionHTTPError(http.StatusBadRequest, "selected item was not found in the current share")
				}
			}
			current.Status = JobQueued
			current.Message = "queued"
			current.Error = ""
			current.Items = nil
			current.Share.SelectedID = ordered[0]
			current.Share.SelectedIDs = append([]string(nil), ordered...)
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

	// Charge is summed at completion, so gate on the summed size here to refuse a
	// batch that would overdraw the CDK instead of silently clamping at zero.
	if job.CDKCode != "" {
		if c, ok, err := s.cdk.get(job.CDKCode); err == nil && ok && totalSize > c.RemainingBytes {
			return nil, http.StatusForbidden, "所选文件合计 " + formatTrafficLabel(totalSize) + " 超过 CDK 剩余流量（剩余 " + formatTrafficLabel(c.RemainingBytes) + "），请减少选择"
		}
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
	Count     int `json:"count"`
	TrafficGB int `json:"traffic_gb"`
	Days      int `json:"days"`
}

type updateCDKRequest struct {
	TrafficGB int `json:"traffic_gb"`
	Days      int `json:"days"`
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
	created, err := s.cdk.createBatch(req.Count, int64(req.TrafficGB)*bytesPerGB, req.Days, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	views := make([]cdkView, 0, len(created))
	for _, c := range created {
		views = append(views, toCDKView(c, now))
	}
	s.logJob(LogSuccess, "", "已分发 CDK", "数量："+itoa(len(created)), "流量额度："+itoa(req.TrafficGB)+"G", "有效天数："+itoa(req.Days))
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
	updated, ok, err := s.cdk.update(code, int64(req.TrafficGB)*bytesPerGB, req.Days, now)
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
	ok, err := s.cdk.delete(code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "CDK 不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// --- CDK user portal ---

func (s *Server) handleUserPortal(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.userHTML)
}

// currentCDK returns the CDK bound to the request's cookie when it exists and is
// still valid (not expired).
func (s *Server) currentCDK(r *http.Request) (CDK, bool) {
	cookie, err := r.Cookie("cdk")
	if err != nil {
		return CDK{}, false
	}
	c, ok, err := s.cdk.get(normalizeCode(cookie.Value))
	if err != nil || !ok {
		return CDK{}, false
	}
	if c.ExpiresAt <= time.Now().Unix() {
		return CDK{}, false
	}
	return c, true
}

type userLoginRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleUserLogin(w http.ResponseWriter, r *http.Request) {
	var req userLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	code := normalizeCode(req.Code)
	if code == "" {
		writeError(w, http.StatusBadRequest, "请输入 CDK")
		return
	}

	now := time.Now()
	c, ok, err := s.cdk.get(code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "CDK 不存在")
		return
	}
	if c.ExpiresAt <= now.Unix() {
		writeError(w, http.StatusForbidden, "CDK 已过期")
		return
	}

	maxAge := int(c.ExpiresAt - now.Unix())
	if maxAge > 86400*30 {
		maxAge = 86400 * 30
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "cdk",
		Value:    c.Code,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
	writeJSON(w, http.StatusOK, s.userStatusPayload(c, now))
}

// userStatusResponse is the CDK status payload plus a live view of the global
// resolution queue, so the user portal can show how busy the system is.
type userStatusResponse struct {
	cdkView
	Queue struct {
		Waiting int  `json:"waiting"`
		Active  bool `json:"active"`
	} `json:"queue"`
}

func (s *Server) userStatusPayload(c CDK, now time.Time) userStatusResponse {
	resp := userStatusResponse{cdkView: toCDKView(c, now)}
	resp.Queue.Waiting = s.resolver.waiting()
	resp.Queue.Active = s.resolver.active()
	return resp
}

func (s *Server) handleUserStatus(w http.ResponseWriter, r *http.Request) {
	c, ok := s.currentCDK(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "CDK 无效或已过期")
		return
	}
	writeJSON(w, http.StatusOK, s.userStatusPayload(c, time.Now()))
}

func (s *Server) handleUserLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "cdk",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleUserCreateJob(w http.ResponseWriter, r *http.Request) {
	c, ok := s.currentCDK(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "CDK 无效或已过期")
		return
	}
	if !s.accounts.HasAccounts() {
		writeError(w, http.StatusServiceUnavailable, "服务暂未就绪，请稍后再试")
		return
	}

	var req createJobRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Input = strings.TrimSpace(req.Input)
	req.PassCode = strings.TrimSpace(req.PassCode)
	req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
	if req.Mode == "" {
		req.Mode = "direct"
	}
	if req.Mode != "direct" && req.Mode != "proxy" {
		writeError(w, http.StatusBadRequest, "mode must be direct or proxy")
		return
	}

	// Traffic is charged at resolve success (once the resource size is known), so
	// here we only gate on the CDK still having traffic left and not being
	// expired. A small overage is possible if parallel jobs race, which is fine.
	if _, err := s.cdk.hasTraffic(c.Code, time.Now()); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	// A multi-line submission fans out into one child job per link, parallelized
	// through the resolve queue under the same concurrency limit.
	lines := splitResourceLineSpecs(req.Input)
	if len(lines) > 1 {
		parent, status, msg := s.createBatchJob(lines, req.Mode, req.PassCode, c.Code, priorityUser, s.baseURL(r))
		if status != 0 {
			writeError(w, status, msg)
			return
		}
		s.logJob(LogInfo, parent.ID, "CDK 用户批量解析任务已创建", "CDK："+maskCDK(c.Code), "链接数："+itoa(len(lines)))
		view := toUserJobView(parent)
		view.QueueAhead = s.resolver.position(parent.ID)
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	rawInput := req.Input
	if len(lines) == 1 {
		rawInput = lines[0].raw
		req.Input = lines[0].clean
	}

	kind, err := detectResourceKind(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var share *ShareState
	if kind == ResourceShare {
		var passCode string
		share, passCode, err = shareStateAndPassCode(rawInput, req.PassCode)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		req.PassCode = passCode
	}

	job := &Job{
		ID:        newJobID(),
		Kind:      kind,
		Mode:      req.Mode,
		Input:     req.Input,
		PassCode:  req.PassCode,
		Status:    JobQueued,
		Stage:     StageTransfer,
		Message:   "queued",
		BaseURL:   s.baseURL(r),
		CDKCode:   c.Code,
		Share:     share,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	s.jobs.create(job)
	s.logJob(LogInfo, job.ID, "CDK 用户解析任务已创建", "CDK："+maskCDK(c.Code), "来源："+string(kind))
	s.resolver.enqueue(job.ID, priorityUser, func(ctx context.Context) {
		s.processJob(ctx, job.ID)
	})

	view := toUserJobView(job)
	view.QueueAhead = s.resolver.position(job.ID)
	writeJSON(w, http.StatusAccepted, view)
}

func (s *Server) handleUserGetJob(w http.ResponseWriter, r *http.Request) {
	c, ok := s.currentCDK(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "CDK 无效或已过期")
		return
	}
	job, ok := s.jobs.get(r.PathValue("id"))
	if !ok || job.CDKCode != c.Code {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	view := toUserJobView(job)
	view.QueueAhead = s.resolver.position(job.ID)
	writeJSON(w, http.StatusOK, view)
}

// selectItemsRequest carries the CDK-user multi-select payload. item_ids is the
// canonical field; item_id is accepted as a single-value fallback so older
// callers still work.
type selectItemsRequest struct {
	ItemIDs []string `json:"item_ids"`
	ItemID  string   `json:"item_id"`
}

func (s *Server) handleUserSelectItem(w http.ResponseWriter, r *http.Request) {
	c, ok := s.currentCDK(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "CDK 无效或已过期")
		return
	}
	job, ok := s.jobs.get(r.PathValue("id"))
	if !ok || job.CDKCode != c.Code {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	var req selectItemsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ids := req.ItemIDs
	if len(ids) == 0 && req.ItemID != "" {
		ids = []string{req.ItemID}
	}

	updated, status, msg := s.applyItemsSelection(job.ID, ids)
	if status != 0 {
		writeError(w, status, msg)
		return
	}
	view := toUserJobView(updated)
	view.QueueAhead = s.resolver.position(updated.ID)
	writeJSON(w, http.StatusAccepted, view)
}

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
	lower := strings.ToLower(message)
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
