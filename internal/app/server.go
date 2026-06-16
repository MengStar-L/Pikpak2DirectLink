package app

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"pikpak2directlink/internal/pikpak"
)

//go:embed web/*
var webFS embed.FS

type Server struct {
	config       Config
	accounts     *AccountPool
	jobs         *jobStore
	logs         *logStore
	authSessions *authSessionStore
	creds        *credentialStore
	updater      *updater
	gateHTML     []byte
	mux          *http.ServeMux
}

type configResponse struct {
	Configured            bool   `json:"configured"`
	AccountCount          int    `json:"account_count"`
	FailedAccountCount    int    `json:"failed_account_count"`
	AvailableAccountCount int    `json:"available_account_count"`
	RootFolder            string `json:"root_folder"`
	AuthRequired          bool   `json:"auth_required"`
	Authenticated         bool   `json:"authenticated"`
}

type authStatusResponse struct {
	Configured    bool `json:"configured"`
	Authenticated bool `json:"authenticated"`
}

type createJobRequest struct {
	Input    string `json:"input"`
	PassCode string `json:"pass_code"`
	Mode     string `json:"mode"`
}

type selectItemRequest struct {
	ItemID string `json:"item_id"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authLoginRequest struct {
	Password string `json:"password"`
}

func NewServer(cfg Config) (*Server, error) {
	staticFiles, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}

	gateHTML, err := fs.ReadFile(staticFiles, "gate.html")
	if err != nil {
		return nil, err
	}

	accounts, err := NewAccountPool(AccountPoolConfig{
		AccountsFile:   cfg.AccountsFile,
		SessionDir:     cfg.AccountSessionDir,
		RootFolderName: cfg.RootFolderName,
		RequestTimeout: cfg.RequestTimeout,
	})
	if err != nil {
		return nil, err
	}
	if cfg.IsConfigured() && !accounts.HasAccounts() {
		if err := accounts.AddBootstrap(cfg.Username, cfg.Password, cfg.SessionFile); err != nil {
			return nil, err
		}
	}

	creds, err := newCredentialStore(cfg.AuthFile)
	if err != nil {
		return nil, err
	}
	// A password pinned via ACCESS_PASSWORD takes precedence and disables the
	// first-visitor setup flow.
	if cfg.HasFixedPassword() {
		if err := creds.Set(cfg.AccessPassword); err != nil {
			return nil, err
		}
	}

	server := &Server{
		config:       cfg,
		accounts:     accounts,
		jobs:         newJobStore(200),
		logs:         newLogStore(500),
		authSessions: newAuthSessionStore(),
		creds:        creds,
		gateHTML:     gateHTML,
		mux:          http.NewServeMux(),
	}

	// The updater logs into the shared console with no job context.
	server.updater = newUpdater(cfg.UpdateRepo, cfg.RequestTimeout, func(level LogLevel, message string, details ...string) {
		server.logJob(level, "", message, details...)
	})

	server.mux.Handle("GET /app.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedFile(w, r, staticFiles, "app.js", "application/javascript; charset=utf-8")
	}))
	server.mux.Handle("GET /styles.css", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedFile(w, r, staticFiles, "styles.css", "text/css; charset=utf-8")
	}))
	server.mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedFile(w, r, staticFiles, "index.html", "text/html; charset=utf-8")
	}))
	server.mux.HandleFunc("GET /api/auth/status", server.handleAuthStatus)
	server.mux.HandleFunc("POST /api/auth/setup", server.handleAuthSetup)
	server.mux.HandleFunc("POST /api/auth/login", server.handleAuthLogin)
	server.mux.HandleFunc("POST /api/auth/logout", server.handleAuthLogout)
	server.mux.HandleFunc("GET /api/config", server.handleConfig)
	server.mux.HandleFunc("GET /api/update", server.handleUpdateStatus)
	server.mux.HandleFunc("POST /api/update/check", server.handleUpdateCheck)
	server.mux.HandleFunc("POST /api/update/install", server.handleUpdateInstall)
	server.mux.HandleFunc("GET /api/logs", server.handleListLogs)
	server.mux.HandleFunc("DELETE /api/logs", server.handleClearLogs)
	server.mux.HandleFunc("GET /api/accounts", server.handleListAccounts)
	server.mux.HandleFunc("POST /api/accounts", server.handleAddAccount)
	server.mux.HandleFunc("DELETE /api/accounts/{id}", server.handleDeleteAccount)
	server.mux.HandleFunc("POST /api/accounts/{id}/reset", server.handleResetAccount)
	server.mux.HandleFunc("POST /api/jobs", server.handleCreateJob)
	server.mux.HandleFunc("GET /api/jobs/{id}", server.handleGetJob)
	server.mux.HandleFunc("POST /api/jobs/{id}/select", server.handleSelectItem)
	server.mux.HandleFunc("GET /proxy/{id}", server.handleProxy)
	server.mux.HandleFunc("HEAD /proxy/{id}", server.handleProxy)

	// Poll GitHub Releases in the background so the UI can surface an available
	// update. Installs are always user-initiated.
	go server.updater.runPeriodicCheck(context.Background(), cfg.UpdateCheckPeriod)

	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.authMiddleware(s.mux)
}

// authenticated reports whether the request carries a valid session cookie.
func (s *Server) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie("session")
	return err == nil && s.authSessions.validate(cookie.Value)
}

// authMiddleware enforces the access gate for every request. Unauthenticated
// browsers never receive the application shell or its scripts — they are served
// a self-contained gate page instead — so the gate cannot be bypassed by
// editing the DOM in devtools. Only the auth endpoints (needed to log in) and
// the per-job proxy links (protected by their own tokens) stay open.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/api/auth/status" ||
			path == "/api/auth/setup" ||
			path == "/api/auth/login" ||
			strings.HasPrefix(path, "/proxy/") {
			next.ServeHTTP(w, r)
			return
		}

		if s.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}

		// API calls get a clean 401 so the front-end can react; everything else
		// (the app shell, scripts, styles, unknown paths) gets the gate page.
		if strings.HasPrefix(path, "/api/") {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		s.serveGate(w)
	})
}

func (s *Server) serveGate(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.gateHTML)
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, authStatusResponse{
		Configured:    s.creds.HasPassword(),
		Authenticated: s.authenticated(r),
	})
}

// handleAuthSetup lets the first visitor set the admin password. Once a password
// exists this endpoint is closed, so it can only ever be used once.
func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if s.creds.HasPassword() {
		writeError(w, http.StatusConflict, "password has already been set")
		return
	}

	var req authLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(strings.TrimSpace(req.Password)) < 6 {
		writeError(w, http.StatusBadRequest, "password must be at least 6 characters")
		return
	}

	if err := s.creds.SetInitial(req.Password); err != nil {
		writeError(w, http.StatusConflict, "password has already been set")
		return
	}

	s.issueSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "configured"})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !s.creds.HasPassword() {
		writeError(w, http.StatusConflict, "password has not been set yet")
		return
	}

	var req authLoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !s.creds.Verify(req.Password) {
		writeError(w, http.StatusUnauthorized, "incorrect password")
		return
	}

	s.issueSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "authenticated"})
}

func (s *Server) issueSession(w http.ResponseWriter) {
	sessionID := s.authSessions.create()
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400 * 30,
	})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err == nil {
		s.authSessions.delete(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.authStatusResponse(s.authenticated(r)))
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.updater.snapshot())
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, s.updater.check(ctx))
}

func (s *Server) handleUpdateInstall(w http.ResponseWriter, _ *http.Request) {
	if err := s.updater.startInstall(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, s.updater.snapshot())
}

func (s *Server) handleListLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("after")), 10, 64)
	writeJSON(w, http.StatusOK, map[string]any{
		"logs": s.logs.list(after),
	})
}

func (s *Server) handleClearLogs(w http.ResponseWriter, _ *http.Request) {
	s.logs.clear()
	writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
	defer cancel()
	s.accounts.RefreshPremiumInfo(ctx)

	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": s.accounts.List(),
	})
}

func (s *Server) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout*2)
	defer cancel()

	account, err := s.accounts.Add(ctx, req.Username, req.Password)
	if err != nil {
		lowerErr := strings.ToLower(err.Error())
		message := friendlyPikPakError(err)
		status := http.StatusUnauthorized
		switch {
		case strings.Contains(lowerErr, "required"), strings.Contains(lowerErr, "empty"):
			status = http.StatusBadRequest
		case strings.Contains(lowerErr, "password"), strings.Contains(lowerErr, "login"):
			status = http.StatusUnauthorized
		default:
			status = http.StatusBadGateway
		}
		writeError(w, status, message)
		return
	}

	writeJSON(w, http.StatusCreated, account)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.accounts.Delete(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleResetAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.accounts.ResetFailure(r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if !s.accounts.HasAccounts() {
		writeError(w, http.StatusServiceUnavailable, "please add at least one PikPak account first")
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
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if kind == ResourceShare {
		shareID, tailID, err := parseShareLink(req.Input)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		job.Share = &ShareState{
			ShareID: shareID,
			TailID:  tailID,
		}
	}

	s.jobs.create(job)
	s.logJob(LogInfo, job.ID, "解析任务已创建", "来源："+string(kind))
	go s.processJob(job.ID)

	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	job, ok := s.jobs.get(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleSelectItem(w http.ResponseWriter, r *http.Request) {
	if !s.accounts.HasAccounts() {
		writeError(w, http.StatusServiceUnavailable, "please add at least one PikPak account first")
		return
	}

	jobID := r.PathValue("id")
	var req selectItemRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.ItemID = strings.TrimSpace(req.ItemID)
	if req.ItemID == "" {
		writeError(w, http.StatusBadRequest, "item_id is required")
		return
	}

	job, ok := s.jobs.get(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	if job.Status != JobSelectionRequired {
		writeError(w, http.StatusConflict, "job is not waiting for a selection")
		return
	}

	var selectedItem *DownloadItem
	if job.Stage == StageResultSelection {
		for _, item := range job.Items {
			if item.ID == req.ItemID {
				copyItem := item
				selectedItem = &copyItem
				break
			}
		}
		if selectedItem == nil {
			writeError(w, http.StatusBadRequest, "selected file was not found in the current result set")
			return
		}
	}

	_, err := s.jobs.update(jobID, func(current *Job) error {
		current.Status = JobRunning
		current.Message = "resuming"
		current.Error = ""
		switch current.Stage {
		case StageSourceSelection:
			if current.Share == nil {
				return errors.New("share context is missing")
			}
			current.Items = nil
			current.Share.SelectedID = req.ItemID
			current.Stage = StageTransfer
		case StageResultSelection:
			current.Message = "resolving selected file"
		default:
			return errors.New("job cannot accept selections right now")
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	if job.Stage == StageSourceSelection {
		go s.processJob(jobID)
	} else {
		go s.resolveExistingFile(jobID, *selectedItem)
	}

	updatedJob, _ := s.jobs.get(jobID)
	writeJSON(w, http.StatusAccepted, updatedJob)
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	job, ok := s.jobs.get(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Result == nil || job.Result.File.ID == "" {
		writeError(w, http.StatusConflict, "job is not ready")
		return
	}

	expectedToken := job.Result.ProxyToken
	providedToken := strings.TrimSpace(r.URL.Query().Get("token"))
	if expectedToken != "" && providedToken != expectedToken {
		writeError(w, http.StatusForbidden, "invalid or missing proxy token")
		return
	}

	sourceURL := strings.TrimSpace(job.Result.DirectURL)
	if sourceURL == "" {
		if job.AccountID == "" {
			writeError(w, http.StatusConflict, "job account is missing")
			return
		}
		account, ok := s.accounts.Get(job.AccountID)
		if !ok {
			writeError(w, http.StatusConflict, "job account is no longer available")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
		defer cancel()

		file, err := account.Client.GetFile(ctx, job.Result.File.ID)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		sourceURL = file.BestDownloadURL()
	}
	if sourceURL == "" {
		writeError(w, http.StatusBadGateway, "download URL is empty")
		return
	}

	upstreamMethod := r.Method
	if upstreamMethod == http.MethodHead {
		upstreamMethod = http.MethodGet
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), upstreamMethod, sourceURL, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	copyHeaderIfPresent(proxyReq.Header, r.Header, "Range")
	copyHeaderIfPresent(proxyReq.Header, r.Header, "If-Range")
	copyHeaderIfPresent(proxyReq.Header, r.Header, "If-None-Match")
	copyHeaderIfPresent(proxyReq.Header, r.Header, "If-Modified-Since")

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	for _, key := range []string{
		"Accept-Ranges",
		"Cache-Control",
		"Content-Disposition",
		"Content-Length",
		"Content-Range",
		"Content-Type",
		"ETag",
		"Last-Modified",
	} {
		copyHeaderIfPresent(w.Header(), resp.Header, key)
	}
	if w.Header().Get("Content-Disposition") == "" && job.Result.File.Name != "" {
		w.Header().Set("Content-Disposition", buildContentDisposition(job.Result.File.Name))
	}

	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

func (s *Server) processJob(jobID string) {
	accounts := s.accounts.Snapshot()
	if len(accounts) == 0 {
		s.logJob(LogError, jobID, "没有可用的 PikPak 账号")
		s.failJob(jobID, errors.New("no PikPak accounts are available"))
		return
	}

	var failures []string
	for _, account := range accounts {
		ctx, cancel := context.WithTimeout(context.Background(), s.config.ResolveTimeout)
		s.beginAccountAttempt(jobID, account)

		err := s.processJobWithAccount(ctx, jobID, account)
		cancel()

		if err == nil {
			s.accounts.MarkAvailable(account.ID)
			s.finishAccountAttempt(jobID, account.ID, "success", "")
			return
		}

		message := friendlyPikPakError(err)
		s.accounts.MarkFailed(account.ID, err)
		s.finishAccountAttempt(jobID, account.ID, "failed", message)
		failures = append(failures, account.Username+": "+message)
	}

	err := fmt.Errorf("all PikPak accounts failed: %s", strings.Join(failures, "; "))
	s.logJob(LogError, jobID, "全部账号尝试失败", strings.Join(failures, "；"))
	s.failJob(jobID, err)
}

func (s *Server) processJobWithAccount(ctx context.Context, jobID string, account AccountRuntime) error {
	job, ok := s.jobs.get(jobID)
	if !ok {
		return errors.New("job not found")
	}

	s.updateJobState(jobID, JobRunning, job.Stage, "starting with "+account.Username, "")

	switch job.Kind {
	case ResourceMagnet:
		return s.processMagnet(ctx, jobID, account)
	case ResourceShare:
		return s.processShare(ctx, jobID, account)
	default:
		return fmt.Errorf("unsupported resource kind %q", job.Kind)
	}
}

func (s *Server) processMagnet(ctx context.Context, jobID string, account AccountRuntime) error {
	folderID, err := s.ensureJobFolder(ctx, jobID, account)
	if err != nil {
		return err
	}

	s.updateJobState(jobID, JobRunning, StageTransfer, "creating offline task", "")
	task, err := account.Client.CreateOfflineTask(ctx, mustJob(s.jobs.get(jobID)).Input, folderID, "")
	if err != nil {
		return err
	}
	s.logJob(LogInfo, jobID, "PikPak 离线任务已创建，等待云端完成缓存 ...")

	s.updateJobState(jobID, JobRunning, StageTransfer, "waiting for PikPak to finish the transfer", "")
	items, err := s.waitForTransferredFiles(ctx, account, folderID, task.ID)
	if err != nil {
		return err
	}
	s.logJob(LogInfo, jobID, fmt.Sprintf("检测到 %d 个可用文件", len(items)), sampleItemDetail(items))
	return s.finishWithItems(ctx, jobID, account, items)
}

func (s *Server) processShare(ctx context.Context, jobID string, account AccountRuntime) error {
	job := mustJob(s.jobs.get(jobID))
	if job.Share == nil {
		return errors.New("share context is missing")
	}

	s.updateJobState(jobID, JobRunning, StageTransfer, "inspecting share link", "")
	s.logJob(LogInfo, jobID, "开始读取 PikPak 分享链接 ...")
	shareInfo, err := account.Client.GetShareInfo(ctx, job.Share.ShareID, job.PassCode, "")
	if err != nil {
		return err
	}

	_, err = s.jobs.update(jobID, func(current *Job) error {
		if current.Share == nil {
			current.Share = &ShareState{}
		}
		current.Share.PassCodeToken = shareInfo.PassCodeToken
		return nil
	})
	if err != nil {
		return err
	}

	selectedID := job.Share.SelectedID
	if selectedID == "" {
		selectedID = job.Share.TailID
	}

	if selectedID == "" {
		items := shareItems(shareInfo.Files)
		if len(items) == 0 {
			return errors.New("share link did not return any file or folder")
		}
		if len(items) > 1 {
			s.logJob(LogWarn, jobID, fmt.Sprintf("分享链接包含 %d 个项目，需要选择目标", len(items)), sampleItemDetail(items))
			return s.requestSelection(jobID, StageSourceSelection, "pick a file or folder from the share first", items, account.ID)
		}
		selectedID = items[0].ID
	}

	folderID, err := s.ensureJobFolder(ctx, jobID, account)
	if err != nil {
		return err
	}

	s.updateJobState(jobID, JobRunning, StageTransfer, "restoring the selected share item into PikPak", "")
	s.logJob(LogInfo, jobID, "分享文件正在转存到 PikPak 临时目录 ...")
	if err := account.Client.RestoreShare(ctx, job.Share.ShareID, shareInfo.PassCodeToken, []string{selectedID}); err != nil {
		return err
	}

	s.updateJobState(jobID, JobRunning, StageTransfer, "waiting for restored files to appear", "")
	items, err := s.waitForTransferredFiles(ctx, account, folderID, "")
	if err != nil {
		return err
	}
	s.logJob(LogInfo, jobID, fmt.Sprintf("检测到 %d 个可用文件", len(items)), sampleItemDetail(items))
	return s.finishWithItems(ctx, jobID, account, items)
}

func (s *Server) finishWithItems(ctx context.Context, jobID string, account AccountRuntime, items []DownloadItem) error {
	if len(items) == 0 {
		return errors.New("no downloadable file was produced")
	}
	if len(items) > 1 {
		s.logJob(LogWarn, jobID, fmt.Sprintf("检测到 %d 个可用文件，需要选择目标文件", len(items)), sampleItemDetail(items))
		return s.requestSelection(jobID, StageResultSelection, "choose which file should become the final link", items, account.ID)
	}
	return s.completeJob(ctx, jobID, account, items[0])
}

func (s *Server) requestSelection(jobID string, stage JobStage, message string, items []DownloadItem, accountID string) error {
	sortItems(items)
	_, err := s.jobs.update(jobID, func(job *Job) error {
		job.Status = JobSelectionRequired
		job.Stage = stage
		job.Message = message
		job.Error = ""
		job.Items = items
		if accountID != "" {
			job.AccountID = accountID
		}
		return nil
	})
	return err
}

func (s *Server) resolveExistingFile(jobID string, item DownloadItem) {
	ctx, cancel := context.WithTimeout(context.Background(), s.config.RequestTimeout*3)
	defer cancel()

	job := mustJob(s.jobs.get(jobID))
	account, ok := s.accounts.Get(job.AccountID)
	if !ok {
		s.failJob(jobID, errors.New("job account is no longer available"))
		return
	}

	if err := s.completeJob(ctx, jobID, account, item); err != nil {
		s.accounts.MarkFailed(account.ID, err)
		s.failJob(jobID, err)
	}
}

func (s *Server) completeJob(ctx context.Context, jobID string, account AccountRuntime, item DownloadItem) error {
	s.updateJobState(jobID, JobRunning, StageTransfer, "requesting a fresh direct link", "")
	s.logJob(LogInfo, jobID, "开始解析所选文件的下载链接 ...", itemLogDetails(item)...)
	file, err := account.Client.GetFile(ctx, item.ID)
	if err != nil {
		return err
	}

	directURL := file.BestDownloadURL()
	if directURL == "" {
		return errors.New("PikPak returned an empty download URL")
	}
	s.logJob(LogSuccess, jobID, "直链获取成功", itemLogDetails(item)...)

	item.MimeType = firstNonEmpty(item.MimeType, file.MimeType)
	item.Size = firstNonEmpty(item.Size, file.Size)

	proxyToken := newJobID()
	result := &JobResult{
		File:       item,
		DirectURL:  directURL,
		ProxyURL:   strings.TrimRight(mustJob(s.jobs.get(jobID)).BaseURL, "/") + "/proxy/" + jobID + "?token=" + proxyToken,
		ProxyToken: proxyToken,
	}
	if expiresAt := file.ExpireAt(); !expiresAt.IsZero() {
		result.ExpiresAt = expiresAt.Format(time.RFC3339)
	}

	s.updateJobState(jobID, JobRunning, StageTransfer, "cleaning temporary PikPak files", "")
	s.logJob(LogInfo, jobID, "开始清理 PikPak 临时文件 ...")
	if err := s.cleanupJobFiles(ctx, jobID, account, item.ID); err != nil {
		return fmt.Errorf("direct link was created, but cleanup failed: %w", err)
	}
	s.logJob(LogSuccess, jobID, "PikPak 临时文件已清理")

	_, err = s.jobs.update(jobID, func(job *Job) error {
		job.Status = JobCompleted
		job.Stage = StageComplete
		job.Message = "ready"
		job.Error = ""
		job.Items = nil
		job.AccountID = account.ID
		job.Result = result
		return nil
	})
	if err == nil {
		s.logJob(LogSuccess, jobID, "解析任务完成", "文件："+firstNonEmpty(item.Name, item.Path))
	}
	return err
}

func (s *Server) cleanupJobFiles(ctx context.Context, jobID string, account AccountRuntime, fallbackFileID string) error {
	job, ok := s.jobs.get(jobID)
	if !ok {
		return errors.New("job not found")
	}

	cleanupID := strings.TrimSpace(job.FolderID)
	if cleanupID == "" {
		cleanupID = strings.TrimSpace(fallbackFileID)
	}
	if cleanupID == "" {
		return nil
	}
	return account.Client.DeleteFiles(ctx, []string{cleanupID})
}

func (s *Server) ensureJobFolder(ctx context.Context, jobID string, account AccountRuntime) (string, error) {
	job := mustJob(s.jobs.get(jobID))
	if job.FolderID != "" && job.AccountID == account.ID {
		return job.FolderID, nil
	}

	rootID, err := account.Client.EnsureRootFolder(ctx)
	if err != nil {
		return "", err
	}

	folder, err := account.Client.CreateFolder(ctx, "job-"+jobID, rootID)
	if err != nil {
		return "", err
	}

	_, err = s.jobs.update(jobID, func(current *Job) error {
		current.FolderID = folder.ID
		return nil
	})
	if err != nil {
		return "", err
	}
	return folder.ID, nil
}

func (s *Server) waitForTransferredFiles(ctx context.Context, account AccountRuntime, folderID, taskID string) ([]DownloadItem, error) {
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	var lastStableSignature string
	stableCount := 0

	for {
		items, err := s.collectFiles(ctx, account, folderID, "")
		if err == nil && len(items) > 0 {
			sortItems(items)
			if taskID == "" {
				signature := signatureForItems(items)
				if signature == lastStableSignature {
					stableCount++
				} else {
					lastStableSignature = signature
					stableCount = 1
				}
				if stableCount >= 2 {
					return items, nil
				}
			}

			phase, message, found, taskErr := s.lookupTask(ctx, account, taskID)
			if taskErr == nil && found && phase == "PHASE_TYPE_COMPLETE" {
				return items, nil
			}
			if taskErr == nil && found && phase == "PHASE_TYPE_ERROR" {
				if message == "" {
					message = "PikPak transfer failed"
				}
				return nil, errors.New(message)
			}
		}

		if taskID != "" {
			phase, message, found, err := s.lookupTask(ctx, account, taskID)
			if err == nil && found {
				switch phase {
				case "PHASE_TYPE_COMPLETE":
					items, collectErr := s.collectFiles(ctx, account, folderID, "")
					if collectErr != nil {
						return nil, collectErr
					}
					if len(items) > 0 {
						sortItems(items)
						return items, nil
					}
				case "PHASE_TYPE_ERROR":
					if message == "" {
						message = "PikPak transfer failed"
					}
					return nil, errors.New(message)
				}
			}
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for PikPak to finish")
		case <-ticker.C:
		}
	}
}

func (s *Server) lookupTask(ctx context.Context, account AccountRuntime, taskID string) (phase, message string, found bool, err error) {
	tasks, err := account.Client.ListOfflineTasks(ctx, nil)
	if err != nil {
		return "", "", false, err
	}
	for _, task := range tasks {
		if task.ID == taskID {
			return task.Phase, task.Message, true, nil
		}
	}
	return "", "", false, nil
}

func (s *Server) collectFiles(ctx context.Context, account AccountRuntime, parentID, prefix string) ([]DownloadItem, error) {
	files, err := account.Client.ListFiles(ctx, parentID)
	if err != nil {
		return nil, err
	}

	var items []DownloadItem
	for _, file := range files {
		currentPath := file.Name
		if prefix != "" {
			currentPath = path.Join(prefix, file.Name)
		}
		if file.IsFolder() {
			children, err := s.collectFiles(ctx, account, file.ID, currentPath)
			if err != nil {
				return nil, err
			}
			items = append(items, children...)
			continue
		}
		items = append(items, DownloadItem{
			ID:       file.ID,
			Name:     file.Name,
			Path:     currentPath,
			Kind:     file.Kind,
			MimeType: file.MimeType,
			Size:     file.Size,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].Path < items[j].Path
	})
	return items, nil
}

func shareItems(files []pikpak.FileEntry) []DownloadItem {
	items := make([]DownloadItem, 0, len(files))
	for _, file := range files {
		items = append(items, DownloadItem{
			ID:       file.ID,
			Name:     file.Name,
			Path:     file.Name,
			Kind:     file.Kind,
			MimeType: file.MimeType,
			Size:     file.Size,
		})
	}
	sortItems(items)
	return items
}

func (s *Server) beginAccountAttempt(jobID string, account AccountRuntime) {
	s.logJob(LogInfo, jobID, "开始尝试账号 "+account.Username)
	_, _ = s.jobs.update(jobID, func(job *Job) error {
		job.Status = JobRunning
		job.Error = ""
		job.AccountID = account.ID
		job.FolderID = ""
		job.AccountAttempts = append(job.AccountAttempts, AccountAttempt{
			AccountID: account.ID,
			Username:  account.Username,
			Status:    "running",
		})
		return nil
	})
}

func (s *Server) finishAccountAttempt(jobID, accountID, status, errText string) {
	switch status {
	case "success":
		s.logJob(LogSuccess, jobID, "账号解析成功")
	case "failed":
		s.logJob(LogError, jobID, "账号解析失败", errText)
	}

	_, _ = s.jobs.update(jobID, func(job *Job) error {
		for i := len(job.AccountAttempts) - 1; i >= 0; i-- {
			if job.AccountAttempts[i].AccountID == accountID && job.AccountAttempts[i].Status == "running" {
				job.AccountAttempts[i].Status = status
				job.AccountAttempts[i].Error = errText
				return nil
			}
		}
		job.AccountAttempts = append(job.AccountAttempts, AccountAttempt{
			AccountID: accountID,
			Status:    status,
			Error:     errText,
		})
		return nil
	})
}

func (s *Server) updateJobState(jobID string, status JobStatus, stage JobStage, message, errText string) {
	_, _ = s.jobs.update(jobID, func(job *Job) error {
		job.Status = status
		job.Stage = stage
		job.Message = message
		job.Error = errText
		return nil
	})
}

func (s *Server) failJob(jobID string, err error) {
	s.updateJobState(jobID, JobFailed, StageFailed, "", err.Error())
}

func (s *Server) baseURL(r *http.Request) string {
	if s.config.PublicBaseURL != "" {
		return s.config.PublicBaseURL
	}

	scheme := "http"
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = forwarded
	} else if r.TLS != nil {
		scheme = "https"
	}

	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) authStatusResponse(authenticated bool) configResponse {
	accounts := s.accounts.List()
	failed := 0
	available := 0
	for _, account := range accounts {
		if account.Status == AccountFailed {
			failed++
		} else {
			available++
		}
	}
	return configResponse{
		Configured:            len(accounts) > 0,
		AccountCount:          len(accounts),
		FailedAccountCount:    failed,
		AvailableAccountCount: available,
		RootFolder:            s.config.RootFolderName,
		AuthRequired:          true,
		Authenticated:         authenticated,
	}
}

func serveEmbeddedFile(w http.ResponseWriter, _ *http.Request, files fs.FS, name, contentType string) {
	data, err := fs.ReadFile(files, name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func copyHeaderIfPresent(dst, src http.Header, key string) {
	value := src.Get(key)
	if value != "" {
		dst.Set(key, value)
	}
}

func buildContentDisposition(filename string) string {
	escaped := url.PathEscape(filename)
	return fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", sanitizeDispositionFilename(filename), escaped)
}

func sanitizeDispositionFilename(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return "download"
	}

	var builder strings.Builder
	for _, char := range filename {
		switch {
		case char == '"' || char == '\\' || char == '/' || char < 0x20:
			builder.WriteByte('_')
		case char > 0x7e:
			builder.WriteByte('_')
		default:
			builder.WriteRune(char)
		}
	}
	if builder.Len() == 0 {
		return "download"
	}
	return builder.String()
}

func signatureForItems(items []DownloadItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.ID+":"+item.Path+":"+item.Size)
	}
	return strings.Join(parts, "|")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) logJob(level LogLevel, jobID, message string, details ...string) {
	if s.logs == nil {
		return
	}
	s.logs.add(level, jobID, message, details...)
}

func sampleItemDetail(items []DownloadItem) string {
	if len(items) == 0 {
		return ""
	}
	item := items[0]
	return "示例：" + firstNonEmpty(item.Path, item.Name)
}

func itemLogDetails(item DownloadItem) []string {
	details := []string{}
	if item.Name != "" {
		details = append(details, "文件："+item.Name)
	}
	if item.Path != "" && item.Path != item.Name {
		details = append(details, "路径："+item.Path)
	}
	if item.Size != "" {
		details = append(details, "大小："+item.Size)
	}
	return details
}

func mustJob(job *Job, ok bool) *Job {
	if !ok || job == nil {
		return &Job{}
	}
	return job
}
