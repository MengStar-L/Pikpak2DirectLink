package app

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// --- shared job-selection helper (used by admin and CDK-user endpoints) ---

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
	}

	_, err := s.jobs.update(jobID, func(current *Job) error {
		current.Status = JobQueued
		current.Message = "queued"
		current.Error = ""
		switch current.Stage {
		case StageSourceSelection:
			if current.Share == nil {
				return errors.New("share context is missing")
			}
			current.Items = nil
			current.Share.SelectedID = itemID
			current.Stage = StageTransfer
		case StageResultSelection:
			current.Message = "queued"
		default:
			return errors.New("job cannot accept selections right now")
		}
		return nil
	})
	if err != nil {
		return nil, http.StatusConflict, err.Error()
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

// --- admin CDK management ---

type createCDKRequest struct {
	Count     int `json:"count"`
	Remaining int `json:"remaining"`
	Days      int `json:"days"`
}

type updateCDKRequest struct {
	Remaining int `json:"remaining"`
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
	if req.Remaining < 1 {
		writeError(w, http.StatusBadRequest, "可解析次数至少为 1")
		return
	}
	if req.Days < 1 {
		writeError(w, http.StatusBadRequest, "到期天数至少为 1")
		return
	}

	now := time.Now()
	created, err := s.cdk.createBatch(req.Count, req.Remaining, req.Days, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	views := make([]cdkView, 0, len(created))
	for _, c := range created {
		views = append(views, toCDKView(c, now))
	}
	s.logJob(LogSuccess, "", "已分发 CDK", "数量："+itoa(len(created)), "可解析次数："+itoa(req.Remaining), "有效天数："+itoa(req.Days))
	writeJSON(w, http.StatusCreated, map[string]any{"cdks": views})
}

func (s *Server) handleUpdateCDK(w http.ResponseWriter, r *http.Request) {
	code := normalizeCode(r.PathValue("code"))
	var req updateCDKRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Remaining < 0 {
		writeError(w, http.StatusBadRequest, "可解析次数不能为负")
		return
	}
	if req.Days < 1 {
		writeError(w, http.StatusBadRequest, "到期天数至少为 1")
		return
	}

	now := time.Now()
	updated, ok, err := s.cdk.update(code, req.Remaining, req.Days, now)
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

	kind, err := detectResourceKind(req.Input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var share *ShareState
	if kind == ResourceShare {
		shareID, tailID, err := parseShareLink(req.Input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		share = &ShareState{ShareID: shareID, TailID: tailID}
	}

	// Reserve a credit only after the request is known to be runnable; the
	// reservation is refunded automatically if the job later fails.
	if _, err := s.cdk.reserve(c.Code, time.Now()); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
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

	var req selectItemRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	updated, status, msg := s.applyItemSelection(job.ID, req.ItemID)
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
	QueueAhead int            `json:"queue_ahead"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

func toUserJobView(job *Job) userJobView {
	return userJobView{
		ID:        job.ID,
		Kind:      job.Kind,
		Mode:      job.Mode,
		Status:    job.Status,
		Stage:     job.Stage,
		Message:   job.Message,
		Error:     job.Error,
		Items:     job.Items,
		Result:    job.Result,
		CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt,
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
