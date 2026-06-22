package app

import (
	"context"
	"database/sql"
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
	"sync"
	"time"

	"pikpak2directlink/internal/pikpak"
)

//go:embed web/*
var webFS embed.FS

type Server struct {
	config               Config
	accounts             *AccountPool
	jobs                 *jobStore
	resolver             *resolveQueue
	logs                 *logStore
	authSessions         *authSessionStore
	creds                *credentialStore
	updater              *updater
	db                   *sql.DB
	cdk                  *cdkStore
	settings             *settingsStore
	gateHTML             []byte
	userHTML             []byte
	mux                  *http.ServeMux
	batchMu              sync.Mutex
	batches              map[string]*batchState
	healthCancel         context.CancelFunc
	healthDone           chan struct{}
	accountHealthProbe   accountHealthProbeFunc
	accountHealthRefresh accountRefreshLoginFunc
	nowFunc              func() time.Time
}

const resourceParseErrorThreshold = 2

type configResponse struct {
	Configured            bool   `json:"configured"`
	AccountCount          int    `json:"account_count"`
	FailedAccountCount    int    `json:"failed_account_count"`
	AvailableAccountCount int    `json:"available_account_count"`
	RootFolder            string `json:"root_folder"`
	AuthRequired          bool   `json:"auth_required"`
	Authenticated         bool   `json:"authenticated"`
	PasswordFixed         bool   `json:"password_fixed"`
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
	ItemIDs []string `json:"item_ids"`
	ItemID  string   `json:"item_id"`
}

type loginRequest struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	TrafficLimitGB int    `json:"traffic_limit_gb"`
}

type authLoginRequest struct {
	Password string `json:"password"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
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

	userHTML, err := fs.ReadFile(staticFiles, "user.html")
	if err != nil {
		return nil, err
	}

	db, err := openDatabase(cfg.DBFile)
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
		db:           db,
		cdk:          newCDKStore(db),
		settings:     newSettingsStore(db),
		gateHTML:     gateHTML,
		userHTML:     userHTML,
		mux:          http.NewServeMux(),
		batches:      make(map[string]*batchState),
	}

	if err := server.accounts.EnsureCredentialSchedule(time.Now(), server.accountHealthInterval()); err != nil {
		db.Close()
		return nil, err
	}

	// The updater logs into the shared console with no job context.
	server.updater = newUpdater(cfg.UpdateRepo, cfg.RequestTimeout, func(level LogLevel, message string, details ...string) {
		server.logJob(level, "", message, details...)
	})

	// Meter link resolution through the resolve queue. Concurrency is admin-
	// controllable and persisted in the settings table; the config value only
	// seeds the initial default the first time the server runs. Concurrency > 1
	// switches the per-job budget from the snappy serial timeout to the longer
	// parallel one.
	initialConcurrency := server.settings.getInt(settingKeyConcurrency, cfg.ResolveConcurrency)
	serialTimeout := cfg.QueueTimeout
	parallelTimeout := cfg.ParallelTimeout
	if savedTimeout := server.settings.getInt(settingKeyTaskTimeoutSeconds, 0); savedTimeout >= minTaskTimeoutSeconds && savedTimeout <= maxTaskTimeoutSeconds {
		timeout := time.Duration(savedTimeout) * time.Second
		serialTimeout = timeout
		parallelTimeout = timeout
	}
	serialTimeout, parallelTimeout = normalizeResolveTimeouts(serialTimeout, parallelTimeout, minResolveTaskTimeout(cfg))
	server.resolver = newResolveQueue(serialTimeout, parallelTimeout, initialConcurrency, server.failJob)
	go server.resolver.run()

	server.mux.Handle("GET /app.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedFile(w, r, staticFiles, "app.js", "application/javascript; charset=utf-8")
	}))
	server.mux.Handle("GET /aria2.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedFile(w, r, staticFiles, "aria2.js", "application/javascript; charset=utf-8")
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
	server.mux.HandleFunc("POST /api/auth/password", server.handleChangePassword)
	server.mux.HandleFunc("GET /api/config", server.handleConfig)
	server.mux.HandleFunc("GET /api/settings", server.handleGetSettings)
	server.mux.HandleFunc("PUT /api/settings", server.handleUpdateSettings)
	server.mux.HandleFunc("GET /api/update", server.handleUpdateStatus)
	server.mux.HandleFunc("POST /api/update/check", server.handleUpdateCheck)
	server.mux.HandleFunc("POST /api/update/install", server.handleUpdateInstall)
	server.mux.HandleFunc("GET /api/logs", server.handleListLogs)
	server.mux.HandleFunc("DELETE /api/logs", server.handleClearLogs)
	server.mux.HandleFunc("GET /api/accounts", server.handleListAccounts)
	server.mux.HandleFunc("POST /api/accounts", server.handleAddAccount)
	server.mux.HandleFunc("PATCH /api/accounts/{id}", server.handleUpdateAccount)
	server.mux.HandleFunc("DELETE /api/accounts/{id}", server.handleDeleteAccount)
	server.mux.HandleFunc("POST /api/accounts/{id}/reset", server.handleResetAccount)
	server.mux.HandleFunc("POST /api/accounts/{id}/refresh-login", server.handleRefreshAccountLogin)
	server.mux.HandleFunc("POST /api/jobs", server.handleCreateJob)
	server.mux.HandleFunc("GET /api/jobs/{id}", server.handleGetJob)
	server.mux.HandleFunc("POST /api/jobs/{id}/select", server.handleSelectItem)

	// Admin-only CDK management (behind the access gate).
	server.mux.HandleFunc("GET /api/cdks", server.handleListCDKs)
	server.mux.HandleFunc("POST /api/cdks", server.handleCreateCDKs)
	server.mux.HandleFunc("PATCH /api/cdks/{code}", server.handleUpdateCDK)
	server.mux.HandleFunc("DELETE /api/cdks/{code}", server.handleDeleteCDK)

	// Public CDK user portal. The handlers enforce CDK validity themselves.
	server.mux.Handle("GET /u", http.HandlerFunc(server.handleUserPortal))
	server.mux.Handle("GET /u/app.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedFile(w, r, staticFiles, "user.js", "application/javascript; charset=utf-8")
	}))
	server.mux.Handle("GET /u/aria2.js", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveEmbeddedFile(w, r, staticFiles, "aria2.js", "application/javascript; charset=utf-8")
	}))
	server.mux.HandleFunc("POST /api/u/login", server.handleUserLogin)
	server.mux.HandleFunc("GET /api/u/status", server.handleUserStatus)
	server.mux.HandleFunc("POST /api/u/logout", server.handleUserLogout)
	server.mux.HandleFunc("POST /api/u/jobs", server.handleUserCreateJob)
	server.mux.HandleFunc("GET /api/u/jobs/{id}", server.handleUserGetJob)
	server.mux.HandleFunc("POST /api/u/jobs/{id}/select", server.handleUserSelectItem)

	server.mux.HandleFunc("GET /proxy/{id}", server.handleProxy)
	server.mux.HandleFunc("HEAD /proxy/{id}", server.handleProxy)

	// Poll GitHub Releases in the background so the UI can surface an available
	// update. Installs are always user-initiated.
	go server.updater.runPeriodicCheck(context.Background(), cfg.UpdateCheckPeriod)
	server.startAccountHealthMonitor()

	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.authMiddleware(s.mux)
}

// Close releases server-held resources such as the database handle. It is safe
// to call on a nil database.
func (s *Server) Close() error {
	if s.healthCancel != nil {
		s.healthCancel()
	}
	if s.healthDone != nil {
		<-s.healthDone
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
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
			path == "/styles.css" ||
			path == "/u" ||
			strings.HasPrefix(path, "/u/") ||
			strings.HasPrefix(path, "/api/u/") ||
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

// handleChangePassword lets an authenticated admin rotate the access password.
// It is registered outside the auth middleware allowlist, so the caller is
// already known to hold a valid session. The current password must still be
// confirmed, and changing it logs out every other session.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if s.config.HasFixedPassword() {
		writeError(w, http.StatusConflict, "访问密码由 ACCESS_PASSWORD 环境变量固定，无法在网页内修改")
		return
	}
	if !s.creds.HasPassword() {
		writeError(w, http.StatusConflict, "尚未设置访问密码")
		return
	}

	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !s.creds.Verify(req.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "当前密码不正确")
		return
	}

	newPassword := strings.TrimSpace(req.NewPassword)
	if len(newPassword) < 6 {
		writeError(w, http.StatusBadRequest, "新密码至少 6 位")
		return
	}
	if s.creds.Verify(req.NewPassword) {
		writeError(w, http.StatusBadRequest, "新密码不能与当前密码相同")
		return
	}

	if err := s.creds.Set(req.NewPassword); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Invalidate every session (other devices are logged out), then mint a fresh
	// one for this client so the caller stays signed in.
	s.authSessions.invalidateAll()
	s.issueSession(w)
	s.logJob(LogSuccess, "", "访问密码已修改，其它设备的登录已失效")
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
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

	account, err := s.accounts.Add(ctx, req.Username, req.Password, int64(req.TrafficLimitGB)*bytesPerGB)
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

func (s *Server) handleRefreshAccountLogin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout*2)
	defer cancel()

	account, err := s.accounts.RefreshLogin(ctx, r.PathValue("id"))
	if err != nil {
		lowerErr := strings.ToLower(err.Error())
		status := http.StatusBadGateway
		switch {
		case strings.Contains(lowerErr, "not found"):
			status = http.StatusNotFound
		case strings.Contains(lowerErr, "missing"), strings.Contains(lowerErr, "required"), strings.Contains(lowerErr, "empty"):
			status = http.StatusBadRequest
		case strings.Contains(lowerErr, "password"), strings.Contains(lowerErr, "login"):
			status = http.StatusUnauthorized
		}
		writeError(w, status, friendlyPikPakError(err))
		return
	}
	checkedAt := s.now()
	s.accounts.MarkCredentialCheckSuccess(account.ID, checkedAt, checkedAt.Add(s.accountHealthInterval()), nil)
	if refreshed, ok := s.accounts.Summary(account.ID); ok {
		account = refreshed
	}
	s.logJob(LogSuccess, "", "PikPak account login refreshed", "account: "+account.Username)
	writeJSON(w, http.StatusOK, account)
}

type updateAccountRequest struct {
	TrafficLimitGB int `json:"traffic_limit_gb"`
}

// handleUpdateAccount lets an admin change an account's monthly downstream
// traffic budget (in GB).
func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	var req updateAccountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TrafficLimitGB < 1 {
		writeError(w, http.StatusBadRequest, "流量额度至少为 1G")
		return
	}
	if err := s.accounts.SetTrafficLimit(r.PathValue("id"), int64(req.TrafficLimitGB)*bytesPerGB); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
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

	// A multi-line submission fans out into one child job per link, parallelized
	// through the resolve queue. A single line keeps the original single-job flow
	// (multi-file resolution still pauses for a manual selection).
	lines := splitResourceLineSpecs(req.Input)
	if len(lines) > 1 {
		parent, status, msg := s.createBatchJob(lines, req.Mode, req.PassCode, "", priorityAdmin, s.baseURL(r))
		if status != 0 {
			writeError(w, status, msg)
			return
		}
		parent.QueueAhead = s.resolver.position(parent.ID)
		writeJSON(w, http.StatusAccepted, parent)
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
		share, passCode, err := shareStateAndPassCode(rawInput, req.PassCode)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		job.Share = share
		job.PassCode = passCode
	}

	s.jobs.create(job)
	s.logJob(LogInfo, job.ID, "解析任务已创建", "来源："+string(kind))
	s.resolver.enqueue(job.ID, priorityAdmin, func(ctx context.Context) {
		s.processJob(ctx, job.ID)
	})

	job.QueueAhead = s.resolver.position(job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	job, ok := s.jobs.get(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	job.QueueAhead = s.resolver.position(jobID)
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleSelectItem(w http.ResponseWriter, r *http.Request) {
	if !s.accounts.HasAccounts() {
		writeError(w, http.StatusServiceUnavailable, "please add at least one PikPak account first")
		return
	}

	var req selectItemRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ids := req.ItemIDs
	if len(ids) == 0 && req.ItemID != "" {
		ids = []string{req.ItemID}
	}

	updatedJob, status, msg := s.applyItemsSelection(r.PathValue("id"), ids)
	if status != 0 {
		writeError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusAccepted, updatedJob)
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	job, ok := s.jobs.get(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, proxyInvalidLinkError)
		return
	}

	// A job may carry a single result (admin path) or many (CDK-user batch). The
	// proxy token in the URL selects which file this request is for.
	providedToken := strings.TrimSpace(r.URL.Query().Get("token"))
	result := job.resultForToken(providedToken)
	if result == nil {
		// Fall back to the single result only when it has no token of its own, so
		// legacy single-file proxy links without a token keep working.
		if job.Result != nil && job.Result.ProxyToken == "" {
			result = job.Result
		} else {
			writeError(w, http.StatusForbidden, proxyInvalidLinkError)
			return
		}
	}
	if result.File.ID == "" {
		writeError(w, http.StatusConflict, proxyNotReadyError)
		return
	}

	sourceURL := strings.TrimSpace(result.DirectURL)
	if sourceURL == "" {
		// A multi-link parent merges results from several children, each possibly
		// resolved on a different account, so prefer the result's own account and
		// fall back to the job's for single-result (admin) jobs.
		accountID := result.AccountID
		if accountID == "" {
			accountID = job.AccountID
		}
		if accountID == "" {
			s.writeProxyFailure(w, http.StatusConflict, jobID, fmt.Errorf("job account is missing"))
			return
		}
		account, ok := s.accounts.Get(accountID)
		if !ok {
			s.writeProxyFailure(w, http.StatusConflict, jobID, fmt.Errorf("job account %q is no longer available", accountID))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
		defer cancel()

		file, err := account.Client.GetFile(ctx, result.File.ID)
		if err != nil {
			s.writeProxyFailure(w, http.StatusBadGateway, jobID, err)
			return
		}
		sourceURL = file.BestDownloadURL()
	}
	if sourceURL == "" {
		s.writeProxyFailure(w, http.StatusBadGateway, jobID, errors.New("download URL is empty"))
		return
	}

	upstreamMethod := r.Method
	if upstreamMethod == http.MethodHead {
		upstreamMethod = http.MethodGet
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), upstreamMethod, sourceURL, nil)
	if err != nil {
		s.writeProxyFailure(w, http.StatusBadGateway, jobID, err)
		return
	}
	copyHeaderIfPresent(proxyReq.Header, r.Header, "Range")
	copyHeaderIfPresent(proxyReq.Header, r.Header, "If-Range")
	copyHeaderIfPresent(proxyReq.Header, r.Header, "If-None-Match")
	copyHeaderIfPresent(proxyReq.Header, r.Header, "If-Modified-Since")

	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		s.writeProxyFailure(w, http.StatusBadGateway, jobID, err)
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
	if w.Header().Get("Content-Disposition") == "" && result.File.Name != "" {
		w.Header().Set("Content-Disposition", buildContentDisposition(result.File.Name))
	}

	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

const (
	proxyInvalidLinkError    = "代理链接无效或已过期"
	proxyNotReadyError       = "代理链接暂不可用，请稍后重试"
	proxyDownloadFailedError = "代理下载失败，请稍后重试；如多次失败请联系管理员。"
)

func (s *Server) writeProxyFailure(w http.ResponseWriter, status int, jobID string, err error) {
	if err != nil {
		s.logJob(LogError, jobID, "代理下载失败", err.Error())
	}
	writeError(w, status, proxyDownloadFailedError)
}

func (s *Server) processJob(ctx context.Context, jobID string) {
	// In parallel mode, try accounts in a rotating order so concurrent jobs fan
	// out across the pool instead of all starting on the first account.
	rotate := s.resolver.concurrencyValue() > 1
	accounts := s.accounts.ResolveOrder(rotate)
	if len(accounts) == 0 {
		s.logJob(LogError, jobID, "没有可用的 PikPak 账号")
		s.failJob(jobID, errors.New("no PikPak accounts are available"))
		return
	}

	var failures []string
	parseErrors := 0
	for _, account := range accounts {
		// Stop as soon as the global per-job budget (the resolve queue's hard
		// timeout) is spent — the next queued job should get its turn.
		if ctx.Err() != nil {
			break
		}

		attemptCtx, cancel := context.WithTimeout(ctx, s.config.ResolveTimeout)
		s.beginAccountAttempt(jobID, account)

		err := s.processJobWithAccount(attemptCtx, jobID, account)
		cancel()

		if err == nil {
			s.accounts.MarkAvailable(account.ID)
			s.finishAccountAttempt(jobID, account.ID, "success", "")
			return
		}

		s.cleanupTempResourcesBestEffort(jobID, account, "account attempt failed", false)

		// A CDK traffic overdraw is deterministic and not the account's fault:
		// retrying on other accounts would just repeat the expensive transfer and
		// hit the same refusal. Fail the job terminally instead.
		var overdraw errCDKOverdraw
		if errors.As(err, &overdraw) {
			s.accounts.MarkAvailable(account.ID)
			s.finishAccountAttempt(jobID, account.ID, "failed", overdraw.Error())
			s.logJob(LogWarn, jobID, "文件大小超过 CDK 剩余流量，已拒绝", overdraw.Error())
			s.failJob(jobID, overdraw)
			return
		}
		if isCDKRefusalError(err) {
			s.accounts.MarkAvailable(account.ID)
			s.finishAccountAttempt(jobID, account.ID, "failed", safeUserError(err.Error()))
			s.logJob(LogWarn, jobID, "CDK refused the resolved file", safeUserError(err.Error()))
			s.failJob(jobID, err)
			return
		}

		// A resource taken down by PikPak (copyright / harmful content / no longer
		// available) is also deterministic and not the account's fault. Every
		// account would hit the same refusal, so don't blacklist the account —
		// just fail this job terminally. This stops one dead link from marking the
		// whole account pool as failed.
		if isResourceUnavailableError(err) {
			message := friendlyPikPakError(err)
			s.accounts.MarkAvailable(account.ID)
			s.finishAccountAttempt(jobID, account.ID, "failed", message)
			s.logJob(LogWarn, jobID, "资源已被 PikPak 下架或失效，已终止解析", message)
			s.failJob(jobID, errors.New(message))
			return
		}

		// "record not found" is a bad-resource parse failure, not proof that the
		// account is broken. Two independent account hits are enough signal to
		// stop this link while leaving the rest of a multi-link batch running.
		if isResourceParseError(err) {
			parseErrors++
			message := friendlyPikPakError(err)
			s.accounts.MarkAvailable(account.ID)
			s.accounts.RecordParseError(account.ID, jobID, message)
			s.finishAccountAttempt(jobID, account.ID, "failed", message)
			s.logJob(LogWarn, jobID, fmt.Sprintf("资源解析错误（%d/%d），账号不禁用", parseErrors, resourceParseErrorThreshold), message)
			if parseErrors >= resourceParseErrorThreshold {
				s.failJob(jobID, errors.New(badResourceParseUserError))
				return
			}
			continue
		}

		// A global-budget timeout (or cancellation) is not the account's fault,
		// so don't mark it failed — just stop and let the next job run.
		if ctx.Err() != nil {
			s.finishAccountAttempt(jobID, account.ID, "failed", "解析超时")
			break
		}

		message := friendlyPikPakError(err)
		s.accounts.MarkFailed(account.ID, err)
		s.finishAccountAttempt(jobID, account.ID, "failed", message)
		failures = append(failures, account.Username+": "+message)
	}

	if ctx.Err() != nil {
		budget := s.resolver.currentTimeout()
		s.logJob(LogError, jobID, "解析超时，已自动跳过", "上限："+budget.String())
		s.failJob(jobID, fmt.Errorf("解析超时：%s 内未完成", budget))
		return
	}

	if parseErrors > 0 && len(failures) == 0 {
		s.logJob(LogWarn, jobID, "资源解析错误，已终止解析", badResourceParseUserError)
		s.failJob(jobID, errors.New(badResourceParseUserError))
		return
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

	// 清洗磁链，去除多余参数
	job := mustJob(s.jobs.get(jobID))
	magnetLink := normalizeMagnetLink(job.Input)
	if magnetLink != job.Input {
		s.logJob(LogInfo, jobID, "磁链已标准化清洗")
	}

	s.updateJobState(jobID, JobRunning, StageTransfer, "creating offline task", "")
	task, err := account.Client.CreateOfflineTask(ctx, magnetLink, folderID, "")
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

	shareParseTimeout := s.config.ShareParseTimeout
	if shareParseTimeout <= 0 {
		shareParseTimeout = 60 * time.Second
	}
	shareCtx, cancelShare := context.WithTimeout(ctx, shareParseTimeout)
	defer cancelShare()

	selectedIDs := selectedShareIDs(job.Share)
	selectedItems := selectedShareItems(job.Share)
	parentID := ""
	scopedByTail := false
	shareInfo := &pikpak.ShareListResponse{PassCodeToken: strings.TrimSpace(job.Share.PassCodeToken)}
	var err error
	if len(selectedIDs) == 0 || shareInfo.PassCodeToken == "" {
		parentID = shareInitialParentID(job.Share)
		scopedByTail = parentID != ""
		shareInfo, err = account.Client.GetShareInfo(shareCtx, job.Share.ShareID, job.PassCode, parentID)
		if err != nil {
			if parentID == "" || !shouldFallbackTailShareScope(nil, err) {
				if shareCtx.Err() == context.DeadlineExceeded || strings.Contains(err.Error(), "context deadline exceeded") {
					return fmt.Errorf("分享链接解析超时（%v），可能触发 PikPak 风控", shareParseTimeout)
				}
				return err
			}
			shareInfo, err = account.Client.GetShareInfo(shareCtx, job.Share.ShareID, job.PassCode, "")
			if err != nil {
				if shareCtx.Err() == context.DeadlineExceeded || strings.Contains(err.Error(), "context deadline exceeded") {
					return fmt.Errorf("分享链接解析超时（%v），可能触发 PikPak 风控", shareParseTimeout)
				}
				return err
			}
			scopedByTail = false
		}
		if parentID != "" && shouldFallbackTailShareScope(shareInfo, nil) {
			scopedByTail = false
			shareInfo, err = account.Client.GetShareInfo(shareCtx, job.Share.ShareID, job.PassCode, "")
			if err != nil {
				if shareCtx.Err() == context.DeadlineExceeded || strings.Contains(err.Error(), "context deadline exceeded") {
					return fmt.Errorf("分享链接解析超时（%v），可能触发 PikPak 风控", shareParseTimeout)
				}
				return err
			}
		}

		if err := s.updateSharePassCodeToken(jobID, shareInfo.PassCodeToken); err != nil {
			return err
		}
	}

	restoreIDs := []string{}
	var items []DownloadItem
	if len(selectedIDs) == 0 {
		if parentID != "" && !scopedByTail {
			item, found, err := s.findShareFileByID(shareCtx, account, job.Share.ShareID, shareInfo.PassCodeToken, shareInfo.Files, parentID, "")
			if err != nil {
				if shareCtx.Err() == context.DeadlineExceeded || strings.Contains(err.Error(), "context deadline exceeded") {
					return fmt.Errorf("分享链接解析超时（%v），可能触发 PikPak 风控", shareParseTimeout)
				}
				return err
			}
			if !found {
				return errors.New("share link target was not found in the share file list")
			}
			selectedIDs = []string{item.ID}
			selectedItems = []DownloadItem{item}
		} else {
			items, err = s.collectShareItems(shareCtx, account, job.Share.ShareID, shareInfo.PassCodeToken, shareInfo.Files, "")
			if err != nil {
				if shareCtx.Err() == context.DeadlineExceeded || strings.Contains(err.Error(), "context deadline exceeded") {
					return fmt.Errorf("分享链接解析超时（%v），可能触发 PikPak 风控", shareParseTimeout)
				}
				return err
			}
			decision, err := decideShareSourceItems(job, items, scopedByTail)
			if err != nil {
				return err
			}
			if len(decision.SelectionItems) > 0 {
				s.logJob(LogWarn, jobID, fmt.Sprintf("分享链接包含 %d 个项目，需要选择目标", len(decision.SelectionItems)), sampleItemDetail(decision.SelectionItems))
				return s.requestSelection(jobID, StageSourceSelection, "pick a file or folder from the share first", decision.SelectionItems, account.ID)
			}
			selectedIDs = decision.SelectedIDs
			selectedItems = decision.SelectedItems
		}
	}
	if len(selectedIDs) == 0 {
		return errors.New("share link did not return any file or folder")
	}
	restoreIDs = selectedIDs

	s.updateJobState(jobID, JobRunning, StageTransfer, "restoring the selected share item into PikPak", "")
	s.logJob(LogInfo, jobID, "分享文件正在转存到 PikPak 临时目录 ...")
	restoreResp, err := account.Client.RestoreShare(shareCtx, job.Share.ShareID, shareInfo.PassCodeToken, restoreIDs)
	if err != nil {
		return err
	}
	restoredIDs := restoreFileIDs(restoreResp)
	if len(restoredIDs) == 0 {
		return errors.New("share restore did not return any file id")
	}
	s.recordTempResource(jobID, account.ID, restoredIDs...)
	if len(restoredIDs) == 1 {
		_, _ = s.jobs.update(jobID, func(current *Job) error {
			current.FolderID = restoredIDs[0]
			return nil
		})
	}

	s.updateJobState(jobID, JobRunning, StageTransfer, "waiting for restored files to become ready", "")
	items, err = s.waitForRestoredShareItems(ctx, jobID, account, restoredIDs, selectedItems)
	if err != nil {
		return err
	}
	s.logJob(LogInfo, jobID, fmt.Sprintf("检测到 %d 个可用文件", len(items)), sampleItemDetail(items))
	return s.finishWithItems(ctx, jobID, account, items)
}

func (s *Server) updateSharePassCodeToken(jobID, token string) error {
	_, err := s.jobs.update(jobID, func(current *Job) error {
		if current.Share == nil {
			current.Share = &ShareState{}
		}
		current.Share.PassCodeToken = token
		return nil
	})
	return err
}

func shareInitialParentID(share *ShareState) string {
	if len(selectedShareIDs(share)) > 0 {
		return ""
	}
	if share == nil {
		return ""
	}
	return strings.TrimSpace(share.TailID)
}

func shouldFallbackTailShareScope(resp *pikpak.ShareListResponse, err error) bool {
	if err != nil {
		return isResourceParseError(err)
	}
	return resp == nil || len(resp.Files) == 0
}

type shareSourceDecision struct {
	SelectedIDs    []string
	SelectedItems  []DownloadItem
	SelectionItems []DownloadItem
}

func decideShareSourceItems(job *Job, items []DownloadItem, scopedByTail bool) (shareSourceDecision, error) {
	var decision shareSourceDecision
	if len(items) == 0 {
		return decision, errors.New("share link did not return any file or folder")
	}
	if scopedByTail {
		if job != nil && job.ResolveAll {
			return decision, errors.New("share folder target requires a file selection; submit it separately instead of in a batch")
		}
		decision.SelectionItems = items
		return decision, nil
	}

	tailID := ""
	if job != nil && job.Share != nil {
		tailID = strings.TrimSpace(job.Share.TailID)
	}
	if tailID != "" {
		if item, ok := downloadItemByID(items, tailID); ok {
			decision.SelectedIDs = []string{tailID}
			decision.SelectedItems = []DownloadItem{item}
			return decision, nil
		}
		return decision, errors.New("share link target was not found in the share file list")
	}

	if len(items) > 1 {
		if job != nil && job.ResolveAll {
			return decision, errors.New("share link contains multiple files; submit it separately and choose target files")
		}
		decision.SelectionItems = items
		return decision, nil
	}
	decision.SelectedIDs = []string{items[0].ID}
	decision.SelectedItems = []DownloadItem{items[0]}
	return decision, nil
}

func (s *Server) findShareFileByID(ctx context.Context, account AccountRuntime, shareID, passCodeToken string, files []pikpak.FileEntry, targetID, prefix string) (DownloadItem, bool, error) {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return DownloadItem{}, false, nil
	}
	if err := ctx.Err(); err != nil {
		return DownloadItem{}, false, err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return DownloadItem{}, false, err
		}
		currentPath := file.Name
		if prefix != "" {
			currentPath = path.Join(prefix, file.Name)
		}
		if file.ID == targetID && !file.IsFolder() {
			return downloadItemFromFile(file, currentPath), true, nil
		}
		if !file.IsFolder() {
			continue
		}
		resp, err := account.Client.GetShareFolder(ctx, shareID, passCodeToken, file.ID)
		if err != nil {
			return DownloadItem{}, false, err
		}
		item, found, err := s.findShareFileByID(ctx, account, shareID, passCodeToken, resp.Files, targetID, currentPath)
		if err != nil || found {
			return item, found, err
		}
	}
	return DownloadItem{}, false, nil
}

func (s *Server) finishWithItems(ctx context.Context, jobID string, account AccountRuntime, items []DownloadItem) error {
	if len(items) == 0 {
		return errors.New("no downloadable file was produced")
	}
	job := mustJob(s.jobs.get(jobID))
	// A batch child must never pause for a selection — resolve every file it
	// found and let the batch coordinator merge the results.
	if job.ResolveAll {
		if job.Kind == ResourceShare && len(items) > maxSelectedFilesPerResolve {
			return fmt.Errorf("share link contains %d files; submit it separately and choose at most %d target files", len(items), maxSelectedFilesPerResolve)
		}
		return s.completeJobBatch(ctx, jobID, account, items)
	}
	if job.ResolveSelected && len(items) > 1 {
		if len(items) > maxSelectedFilesPerResolve {
			s.logJob(LogWarn, jobID, fmt.Sprintf("检测到 %d 个可用文件，需要缩小选择范围", len(items)))
			return s.requestSelection(jobID, StageResultSelection, "choose fewer files to generate links", items, account.ID)
		}
		return s.completeJobBatch(ctx, jobID, account, items)
	}
	if len(items) > 1 {
		s.logJob(LogWarn, jobID, fmt.Sprintf("检测到 %d 个可用文件，需要选择目标文件", len(items)), sampleItemDetail(items))
		return s.requestSelection(jobID, StageResultSelection, "choose which file should become the final link", items, account.ID)
	}
	if err := s.cdkOverdrawError(jobID, items[0]); err != nil {
		return err
	}
	return s.completeJob(ctx, jobID, account, items[0])
}

// cdkOverdrawError returns a typed error when a CDK job's resolved file is
// larger than the CDK's remaining traffic. Non-CDK jobs and lookups that fail
// never block (the charge step still clamps at zero as a backstop).
func (s *Server) cdkOverdrawError(jobID string, item DownloadItem) error {
	cdkCode := mustJob(s.jobs.get(jobID)).CDKCode
	if cdkCode == "" {
		return nil
	}
	size := parseBytes(item.Size)
	c, ok, err := s.cdk.get(cdkCode)
	if err != nil || !ok {
		return nil
	}
	if size > c.RemainingBytes {
		return errCDKOverdraw{size: size, remaining: c.RemainingBytes}
	}
	return nil
}

func isCDKRefusalError(err error) bool {
	if err == nil {
		return false
	}
	var overdraw errCDKOverdraw
	if errors.As(err, &overdraw) {
		return true
	}
	return errors.Is(err, errCDKNotFound) ||
		errors.Is(err, errCDKExpired) ||
		errors.Is(err, errCDKExhausted)
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

func (s *Server) resolveExistingFile(ctx context.Context, jobID string, item DownloadItem) {
	job := mustJob(s.jobs.get(jobID))
	account, ok := s.accounts.Get(job.AccountID)
	if !ok {
		s.failJob(jobID, errors.New("job account is no longer available"))
		return
	}

	if err := s.completeJob(ctx, jobID, account, item); err != nil {
		if isCDKRefusalError(err) {
			s.failJob(jobID, err)
			return
		}
		s.accounts.MarkFailed(account.ID, err)
		s.failJob(jobID, err)
	}
}

// resolveExistingFiles is the multi-select counterpart of resolveExistingFile:
// it resolves a fresh link for each chosen file and stores them as Job.Results.
func (s *Server) resolveExistingFiles(ctx context.Context, jobID string, items []DownloadItem) {
	job := mustJob(s.jobs.get(jobID))
	account, ok := s.accounts.Get(job.AccountID)
	if !ok {
		s.failJob(jobID, errors.New("job account is no longer available"))
		return
	}

	if err := s.completeJobBatch(ctx, jobID, account, items); err != nil {
		if isCDKRefusalError(err) {
			s.failJob(jobID, err)
			return
		}
		s.accounts.MarkFailed(account.ID, err)
		s.failJob(jobID, err)
	}
}

func (s *Server) completeJob(ctx context.Context, jobID string, account AccountRuntime, item DownloadItem) error {
	result, err := s.resolveFileLink(ctx, jobID, account, item)
	if err != nil {
		return err
	}

	size := parseBytes(result.File.Size)
	if cdkCode := mustJob(s.jobs.get(jobID)).CDKCode; cdkCode != "" {
		if err := s.cdk.chargeIfEnough(cdkCode, size, time.Now()); err != nil {
			s.cleanupTempResourcesBestEffort(jobID, account, "CDK charge failed", false, item.ID)
			return err
		}
	}

	s.updateJobState(jobID, JobRunning, StageTransfer, "cleaning temporary PikPak files", "")
	s.logJob(LogInfo, jobID, "开始清理 PikPak 临时文件 ...")
	if err := s.cleanupJobTempResources(ctx, jobID, account, item.ID); err != nil {
		s.logJob(LogWarn, jobID, "temporary PikPak cleanup failed", err.Error())
		s.addJobWarning(jobID, "Temporary PikPak cleanup failed; generated links are still ready.")
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
		s.logJob(LogSuccess, jobID, "解析任务完成", "文件："+firstNonEmpty(result.File.Name, result.File.Path))
		// The direct link has now been delivered, so the resource's size counts
		// against this account's monthly downstream budget (and the CDK's traffic
		// allowance, when the job came from a CDK user).
		s.accounts.AddTraffic(account.ID, size)
		s.logJob(LogInfo, jobID, "已计入下行流量", "账号："+account.Username, "大小："+formatTrafficLabel(size))
	}
	return err
}

// resolveFileLink fetches a fresh direct link for a single already-transferred
// file and builds its JobResult (direct + proxy URL). It performs no job-state
// mutation, cleanup, or traffic charging, so it can back both the single-file
// completeJob and the multi-file batch path.
func (s *Server) resolveFileLink(ctx context.Context, jobID string, account AccountRuntime, item DownloadItem) (*JobResult, error) {
	s.updateJobState(jobID, JobRunning, StageTransfer, "requesting a fresh direct link", "")
	s.logJob(LogInfo, jobID, "开始解析所选文件的下载链接 ...", itemLogDetails(item)...)
	job := mustJob(s.jobs.get(jobID))
	var file *pikpak.FileEntry
	var err error
	if job.Kind == ResourceShare {
		file, err = account.Client.WaitForFileDownloadURL(ctx, item.ID, s.config.ShareURLTimeout, s.config.SharePollInterval)
	} else {
		file, err = account.Client.GetFile(ctx, item.ID)
	}
	if err != nil {
		return nil, err
	}

	directURL := file.BestDownloadURL()
	if directURL == "" {
		return nil, errors.New("PikPak returned an empty download URL")
	}
	s.logJob(LogSuccess, jobID, "直链获取成功", itemLogDetails(item)...)

	item.MimeType = firstNonEmpty(item.MimeType, file.MimeType)
	item.Size = firstNonEmpty(item.Size, file.Size)

	proxyToken := newJobID()
	result := &JobResult{
		File:       item,
		DirectURL:  directURL,
		ProxyURL:   strings.TrimRight(job.BaseURL, "/") + "/proxy/" + jobID + "?token=" + proxyToken,
		ProxyToken: proxyToken,
		AccountID:  account.ID,
	}
	result.applyPreferredURL(job.Mode)
	if expiresAt := file.ExpireAt(); !expiresAt.IsZero() {
		result.ExpiresAt = expiresAt.Format(time.RFC3339)
	}
	return result, nil
}

// completeJobBatch resolves a direct link for each selected file, accumulates
// them into Job.Results, cleans up once, and charges the CDK the summed size.
// It backs the CDK-user multi-select flow. A single GetFile failure aborts the
// whole batch (the job fails) rather than delivering a partial set.
func (s *Server) completeJobBatch(ctx context.Context, jobID string, account AccountRuntime, items []DownloadItem) error {
	// Gate the summed size against the CDK's remaining traffic before doing any
	// expensive link resolution. The user-select path is already pre-gated in
	// applyItemsSelection; this also covers the batch-child path, which resolves
	// every file unattended and would otherwise overdraw.
	if cdkCode := mustJob(s.jobs.get(jobID)).CDKCode; cdkCode != "" {
		var sum int64
		for _, item := range items {
			sum += parseBytes(item.Size)
		}
		if c, ok, err := s.cdk.get(cdkCode); err == nil && ok && sum > c.RemainingBytes {
			return errCDKOverdraw{size: sum, remaining: c.RemainingBytes}
		}
	}

	results := make([]JobResult, 0, len(items))
	var totalSize int64
	for _, item := range items {
		result, err := s.resolveFileLink(ctx, jobID, account, item)
		if err != nil {
			return err
		}
		results = append(results, *result)
		totalSize += parseBytes(result.File.Size)
	}

	fallbackIDs := downloadItemIDs(items)
	if cdkCode := mustJob(s.jobs.get(jobID)).CDKCode; cdkCode != "" {
		if err := s.cdk.chargeIfEnough(cdkCode, totalSize, time.Now()); err != nil {
			s.cleanupTempResourcesBestEffort(jobID, account, "CDK charge failed", false, fallbackIDs...)
			return err
		}
	}

	s.updateJobState(jobID, JobRunning, StageTransfer, "cleaning temporary PikPak files", "")
	s.logJob(LogInfo, jobID, "开始清理 PikPak 临时文件 ...")
	if err := s.cleanupJobTempResources(ctx, jobID, account, fallbackIDs...); err != nil {
		s.logJob(LogWarn, jobID, "temporary PikPak cleanup failed", err.Error())
		s.addJobWarning(jobID, "Temporary PikPak cleanup failed; generated links are still ready.")
	}
	s.logJob(LogSuccess, jobID, "PikPak 临时文件已清理")

	_, err := s.jobs.update(jobID, func(job *Job) error {
		job.Status = JobCompleted
		job.Stage = StageComplete
		job.Message = "ready"
		job.Error = ""
		job.Items = nil
		job.AccountID = account.ID
		job.Results = results
		return nil
	})
	if err == nil {
		s.logJob(LogSuccess, jobID, fmt.Sprintf("解析任务完成，共 %d 个文件", len(results)))
		s.accounts.AddTraffic(account.ID, totalSize)
		s.logJob(LogInfo, jobID, "已计入下行流量", "账号："+account.Username, "大小："+formatTrafficLabel(totalSize))
	}
	return err
}

func downloadItemIDs(items []DownloadItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if id := strings.TrimSpace(item.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return uniqueStrings(ids)
}

func selectedShareIDs(share *ShareState) []string {
	if share == nil {
		return nil
	}
	ids := append([]string(nil), share.SelectedIDs...)
	if len(ids) == 0 && strings.TrimSpace(share.SelectedID) != "" {
		ids = append(ids, share.SelectedID)
	}
	return uniqueStrings(ids)
}

func selectedShareItems(share *ShareState) []DownloadItem {
	if share == nil || len(share.SelectedItems) == 0 {
		return nil
	}
	return append([]DownloadItem(nil), share.SelectedItems...)
}

func (s *Server) recordTempResource(jobID, accountID string, ids ...string) {
	cleanIDs := uniqueStrings(ids)
	if len(cleanIDs) == 0 {
		return
	}
	_, _ = s.jobs.update(jobID, func(job *Job) error {
		job.TempAccountID = accountID
		job.TempIDs = uniqueStrings(append(job.TempIDs, cleanIDs...))
		if len(cleanIDs) == 1 && job.FolderID == "" {
			job.FolderID = cleanIDs[0]
		}
		return nil
	})
}

func (s *Server) clearTempResources(jobID string) {
	_, _ = s.jobs.update(jobID, func(job *Job) error {
		job.FolderID = ""
		job.TempAccountID = ""
		job.TempIDs = nil
		return nil
	})
}

func (s *Server) addJobWarning(jobID, warning string) {
	warning = strings.TrimSpace(warning)
	if warning == "" {
		return
	}
	_, _ = s.jobs.update(jobID, func(job *Job) error {
		for _, existing := range job.Warnings {
			if existing == warning {
				return nil
			}
		}
		job.Warnings = append(job.Warnings, warning)
		return nil
	})
}

func (s *Server) cleanupJobTempResources(ctx context.Context, jobID string, account AccountRuntime, fallbackIDs ...string) error {
	job, ok := s.jobs.get(jobID)
	if !ok {
		return errors.New("job not found")
	}
	if job.TempAccountID != "" && job.TempAccountID != account.ID {
		return nil
	}

	ids := append([]string(nil), job.TempIDs...)
	if len(ids) == 0 && strings.TrimSpace(job.FolderID) != "" {
		ids = append(ids, job.FolderID)
	}
	ids = append(ids, fallbackIDs...)
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		return nil
	}
	if err := account.Client.DeleteFiles(ctx, ids); err != nil {
		return err
	}
	s.clearTempResources(jobID)
	return nil
}

func (s *Server) cleanupTempResourcesBestEffort(jobID string, account AccountRuntime, reason string, warn bool, fallbackIDs ...string) {
	timeout := s.config.RequestTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := s.cleanupJobTempResources(ctx, jobID, account, fallbackIDs...); err != nil {
		s.logJob(LogWarn, jobID, "temporary PikPak cleanup failed", reason, err.Error())
		if warn {
			s.addJobWarning(jobID, "Temporary PikPak cleanup failed; generated links are still ready.")
		}
	}
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
		current.TempAccountID = account.ID
		current.TempIDs = uniqueStrings(append(current.TempIDs, folder.ID))
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

type restoredFileLister interface {
	ListFiles(ctx context.Context, parentID string) ([]pikpak.FileEntry, error)
}

func (s *Server) waitForRestoredShareItems(ctx context.Context, jobID string, account AccountRuntime, fileIDs []string, selectedItems []DownloadItem) ([]DownloadItem, error) {
	var restored []pikpak.FileEntry
	for _, fileID := range uniqueStrings(fileIDs) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := s.waitForRestoredFileEntry(ctx, account, fileID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.logJob(LogWarn, jobID, "恢复后的分享文件暂不可用，已跳过", err.Error())
			continue
		}
		restored = append(restored, *file)
	}
	if len(restored) == 0 {
		return nil, errors.New("restored share did not produce any file")
	}

	if len(selectedItems) > 0 {
		items, err := resolveRestoredSelectedItems(ctx, account.Client, restored, selectedItems)
		if err != nil {
			return nil, err
		}
		sortItems(items)
		return items, nil
	}

	items := make([]DownloadItem, 0, len(restored))
	for _, file := range restored {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.IsFolder() {
			return nil, fmt.Errorf("restored share item %q is a folder; choose target files before resolving", file.Name)
		}
		items = append(items, downloadItemFromFile(file, file.Name))
	}
	sortItems(items)
	return items, nil
}

func resolveRestoredSelectedItems(ctx context.Context, lister restoredFileLister, restored []pikpak.FileEntry, selectedItems []DownloadItem) ([]DownloadItem, error) {
	if len(selectedItems) == 0 {
		return nil, errors.New("selected share files are missing")
	}
	if len(restored) == len(selectedItems) && allRestoredEntriesAreFiles(restored) {
		items := make([]DownloadItem, 0, len(selectedItems))
		for i, file := range restored {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			items = append(items, restoredItemFromSelection(file, selectedItems[i]))
		}
		return items, nil
	}

	locator := newRestoredPathLocator(lister)
	used := make(map[string]struct{}, len(selectedItems))
	items := make([]DownloadItem, 0, len(selectedItems))
	for _, selected := range selectedItems {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item, ok, err := locateRestoredSelectedItem(ctx, locator, restored, selected, used)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("selected share file %q could not be found after restore", firstNonEmpty(selected.Path, selected.Name, selected.ID))
		}
		used[item.ID] = struct{}{}
		items = append(items, item)
	}
	return items, nil
}

func allRestoredEntriesAreFiles(restored []pikpak.FileEntry) bool {
	if len(restored) == 0 {
		return false
	}
	for _, file := range restored {
		if file.IsFolder() {
			return false
		}
	}
	return true
}

func locateRestoredSelectedItem(ctx context.Context, locator *restoredPathLocator, restored []pikpak.FileEntry, selected DownloadItem, used map[string]struct{}) (DownloadItem, bool, error) {
	for _, file := range restored {
		if err := ctx.Err(); err != nil {
			return DownloadItem{}, false, err
		}
		if file.IsFolder() || !restoredFileMatchesSelection(file, selected) {
			continue
		}
		if _, ok := used[file.ID]; ok {
			continue
		}
		return restoredItemFromSelection(file, selected), true, nil
	}

	for _, root := range restored {
		if err := ctx.Err(); err != nil {
			return DownloadItem{}, false, err
		}
		if !root.IsFolder() {
			continue
		}
		for _, segments := range selectedPathCandidates(root.Name, selected) {
			item, ok, err := locator.locate(ctx, root.ID, segments, selected)
			if err != nil {
				return DownloadItem{}, false, err
			}
			if !ok {
				continue
			}
			if _, usedAlready := used[item.ID]; usedAlready {
				continue
			}
			return item, true, nil
		}
	}
	return DownloadItem{}, false, nil
}

func restoredFileMatchesSelection(file pikpak.FileEntry, selected DownloadItem) bool {
	if file.IsFolder() {
		return false
	}
	if selected.Name != "" && file.Name != "" && file.Name != selected.Name {
		return false
	}
	if selected.Size != "" && file.Size != "" && file.Size != selected.Size {
		return false
	}
	return selected.Name != "" || selected.Size != ""
}

func restoredItemFromSelection(file pikpak.FileEntry, selected DownloadItem) DownloadItem {
	item := downloadItemFromFile(file, firstNonEmpty(selected.Path, selected.Name, file.Name))
	item.Name = firstNonEmpty(selected.Name, item.Name)
	item.Path = firstNonEmpty(selected.Path, item.Path)
	item.Kind = firstNonEmpty(item.Kind, selected.Kind)
	item.MimeType = firstNonEmpty(item.MimeType, selected.MimeType)
	item.Size = firstNonEmpty(item.Size, selected.Size)
	return item
}

type restoredPathLocator struct {
	lister restoredFileLister
	cache  map[string][]pikpak.FileEntry
}

func newRestoredPathLocator(lister restoredFileLister) *restoredPathLocator {
	return &restoredPathLocator{
		lister: lister,
		cache:  make(map[string][]pikpak.FileEntry),
	}
}

func (l *restoredPathLocator) locate(ctx context.Context, rootID string, segments []string, selected DownloadItem) (DownloadItem, bool, error) {
	if len(segments) == 0 {
		return DownloadItem{}, false, nil
	}
	parentID := rootID
	for i, segment := range segments {
		if err := ctx.Err(); err != nil {
			return DownloadItem{}, false, err
		}
		files, err := l.list(ctx, parentID)
		if err != nil {
			return DownloadItem{}, false, err
		}
		if i == len(segments)-1 {
			file, ok, err := chooseRestoredPathFile(files, segment, selected)
			if err != nil || !ok {
				return DownloadItem{}, ok, err
			}
			return restoredItemFromSelection(file, selected), true, nil
		}
		folder, ok, err := chooseRestoredPathFolder(files, segment)
		if err != nil || !ok {
			return DownloadItem{}, ok, err
		}
		parentID = folder.ID
	}
	return DownloadItem{}, false, nil
}

func (l *restoredPathLocator) list(ctx context.Context, parentID string) ([]pikpak.FileEntry, error) {
	if files, ok := l.cache[parentID]; ok {
		return files, nil
	}
	files, err := l.lister.ListFiles(ctx, parentID)
	if err != nil {
		return nil, err
	}
	l.cache[parentID] = files
	return files, nil
}

func chooseRestoredPathFolder(files []pikpak.FileEntry, name string) (pikpak.FileEntry, bool, error) {
	var matches []pikpak.FileEntry
	for _, file := range files {
		if file.IsFolder() && file.Name == name {
			matches = append(matches, file)
		}
	}
	if len(matches) == 0 {
		return pikpak.FileEntry{}, false, nil
	}
	if len(matches) > 1 {
		return pikpak.FileEntry{}, false, fmt.Errorf("restored folder path %q is ambiguous", name)
	}
	return matches[0], true, nil
}

func chooseRestoredPathFile(files []pikpak.FileEntry, name string, selected DownloadItem) (pikpak.FileEntry, bool, error) {
	var matches []pikpak.FileEntry
	for _, file := range files {
		if !file.IsFolder() && file.Name == name {
			matches = append(matches, file)
		}
	}
	if len(matches) == 0 {
		return pikpak.FileEntry{}, false, nil
	}
	if selected.Size != "" {
		var exact []pikpak.FileEntry
		var unknown []pikpak.FileEntry
		for _, file := range matches {
			switch file.Size {
			case selected.Size:
				exact = append(exact, file)
			case "":
				unknown = append(unknown, file)
			}
		}
		if len(exact) == 1 {
			return exact[0], true, nil
		}
		if len(exact) > 1 {
			matches = exact
		} else if len(unknown) > 0 {
			matches = unknown
		}
	}
	if len(matches) > 1 {
		return pikpak.FileEntry{}, false, fmt.Errorf("restored file path %q is ambiguous", name)
	}
	return matches[0], true, nil
}

func selectedPathCandidates(rootName string, selected DownloadItem) [][]string {
	base := firstNonEmpty(selected.Path, selected.Name)
	candidates := make([][]string, 0, 3)
	add := func(segments []string) {
		if len(segments) == 0 {
			return
		}
		key := strings.Join(segments, "\x00")
		for _, existing := range candidates {
			if strings.Join(existing, "\x00") == key {
				return
			}
		}
		candidates = append(candidates, segments)
	}

	segments := pathSegments(base)
	add(segments)
	if rootName != "" && len(segments) > 1 && segments[0] == rootName {
		add(segments[1:])
	}
	if selected.Name != "" {
		add(pathSegments(selected.Name))
	}
	return candidates
}

func pathSegments(value string) []string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return nil
	}
	parts := strings.Split(path.Clean(value), "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." || part == "/" {
			continue
		}
		segments = append(segments, part)
	}
	return segments
}

func (s *Server) waitForRestoredFileEntry(ctx context.Context, account AccountRuntime, fileID string) (*pikpak.FileEntry, error) {
	timeout := s.config.ShareURLTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	pollInterval := s.config.SharePollInterval
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}
	requestTimeout := s.config.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 20 * time.Second
	}
	if timeout < requestTimeout {
		requestTimeout = timeout
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		file, err := account.Client.GetFile(requestCtx, fileID)
		cancel()
		if err == nil {
			return file, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("waiting for restored file %s timed out: %w", fileID, lastErr)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (s *Server) waitForShareDirectURLs(ctx context.Context, jobID string, account AccountRuntime, items []DownloadItem) ([]DownloadItem, error) {
	s.logJob(LogInfo, jobID, "等待文件直链就绪 ...")

	readyItems := make([]DownloadItem, 0, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, err := account.Client.WaitForFileDownloadURL(ctx, item.ID, s.config.ShareURLTimeout, s.config.SharePollInterval)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.logJob(LogWarn, jobID, fmt.Sprintf("文件 %s 直链获取失败，已跳过", item.Name), err.Error())
			continue
		}

		if file.BestDownloadURL() != "" {
			item.MimeType = firstNonEmpty(item.MimeType, file.MimeType)
			item.Size = firstNonEmpty(item.Size, file.Size)
			readyItems = append(readyItems, item)
		} else {
			s.logJob(LogWarn, jobID, fmt.Sprintf("文件 %s 直链为空，已跳过", item.Name))
		}
	}

	if len(readyItems) == 0 {
		return nil, fmt.Errorf("所有文件直链获取失败，可能触发 PikPak 风控")
	}

	if len(readyItems) < len(items) {
		s.addJobWarning(jobID, fmt.Sprintf("Some files were skipped because their direct links were not ready (%d/%d ready).", len(readyItems), len(items)))
		s.logJob(LogWarn, jobID, fmt.Sprintf("部分文件直链获取失败：成功 %d/%d", len(readyItems), len(items)))
	}

	return readyItems, nil
}

func (s *Server) collectShareItems(ctx context.Context, account AccountRuntime, shareID, passCodeToken string, files []pikpak.FileEntry, prefix string) ([]DownloadItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var items []DownloadItem
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		currentPath := file.Name
		if prefix != "" {
			currentPath = path.Join(prefix, file.Name)
		}
		if file.IsFolder() {
			resp, err := account.Client.GetShareFolder(ctx, shareID, passCodeToken, file.ID)
			if err != nil {
				return nil, err
			}
			children, err := s.collectShareItems(ctx, account, shareID, passCodeToken, resp.Files, currentPath)
			if err != nil {
				return nil, err
			}
			items = append(items, children...)
			continue
		}
		items = append(items, downloadItemFromFile(file, currentPath))
	}
	sortItems(items)
	return items, nil
}

func downloadItemFromFile(file pikpak.FileEntry, itemPath string) DownloadItem {
	if itemPath == "" {
		itemPath = file.Name
	}
	return DownloadItem{
		ID:       file.ID,
		Name:     file.Name,
		Path:     itemPath,
		Kind:     file.Kind,
		MimeType: file.MimeType,
		Size:     file.Size,
	}
}

func downloadItemIDExists(items []DownloadItem, id string) bool {
	_, ok := downloadItemByID(items, id)
	return ok
}

func downloadItemByID(items []DownloadItem, id string) (DownloadItem, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return DownloadItem{}, false
	}
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return DownloadItem{}, false
}

func restoreFileIDs(resp *pikpak.RestoreShareResponse) []string {
	if resp == nil {
		return nil
	}
	ids := make([]string, 0, 1+len(resp.TaskInfo))
	if id := strings.TrimSpace(resp.FileID); id != "" {
		ids = append(ids, id)
	}
	for _, task := range resp.TaskInfo {
		if id := strings.TrimSpace(task.FileID); id != "" {
			ids = append(ids, id)
		}
	}
	return uniqueStrings(ids)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := account.Client.ListFiles(ctx, parentID)
	if err != nil {
		return nil, err
	}

	var items []DownloadItem
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
		switch {
		case account.Status == AccountFailed:
			failed++
		case account.TrafficLimited:
			// Counted as neither available nor failed: it's a temporary monthly cap.
		default:
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
		PasswordFixed:         s.config.HasFixedPassword(),
	}
}

// parseBytes parses a byte-count string (as PikPak reports file sizes). Empty or
// malformed values yield 0, so a missing size simply counts as no traffic.
func parseBytes(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
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
	return ""
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
