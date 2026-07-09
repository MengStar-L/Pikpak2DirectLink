package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// settingKeyConcurrency stores the admin-chosen number of parallel resolution
// slots. It is the runtime source of truth (it survives restarts); the config
// value only supplies the initial default when this key is absent.
const settingKeyConcurrency = "resolve_concurrency"

const (
	settingKeyTaskTimeoutSeconds = "resolve_task_timeout_seconds"
	minTaskTimeoutSeconds        = 60
	maxTaskTimeoutSeconds        = 12 * 60 * 60
)

func minResolveTaskTimeout(cfg Config) time.Duration {
	requestTimeout := cfg.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 20 * time.Second
	}
	shareParseTimeout := cfg.ShareParseTimeout
	if shareParseTimeout <= 0 {
		shareParseTimeout = 60 * time.Second
	}
	shareURLTimeout := cfg.ShareURLTimeout
	if shareURLTimeout <= 0 {
		shareURLTimeout = 60 * time.Second
	}

	minimum := time.Duration(minTaskTimeoutSeconds) * time.Second
	shareBudget := shareParseTimeout + shareURLTimeout + 2*requestTimeout + 30*time.Second
	if shareBudget > minimum {
		minimum = shareBudget
	}
	return ceilDuration(minimum, time.Minute)
}

func maxResolveTaskTimeout(minimum time.Duration) time.Duration {
	maximum := time.Duration(maxTaskTimeoutSeconds) * time.Second
	if minimum > maximum {
		return minimum
	}
	return maximum
}

func normalizeResolveTimeouts(serialTimeout, parallelTimeout, minimum time.Duration) (time.Duration, time.Duration) {
	if serialTimeout < minimum {
		serialTimeout = minimum
	}
	if parallelTimeout < minimum {
		parallelTimeout = minimum
	}
	return serialTimeout, parallelTimeout
}

func ceilDuration(value, unit time.Duration) time.Duration {
	if unit <= 0 || value <= 0 {
		return value
	}
	if value%unit == 0 {
		return value
	}
	return (value/unit + 1) * unit
}

type settingsStore struct {
	db *sql.DB
}

func newSettingsStore(db *sql.DB) *settingsStore {
	return &settingsStore{db: db}
}

// getInt reads an integer setting, returning fallback when the key is absent or
// unparseable.
func (s *settingsStore) getInt(key string, fallback int) int {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func (s *settingsStore) setInt(key string, value int) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, strconv.Itoa(value),
	)
	return err
}

func (s *settingsStore) getInt64(key string, fallback int64) int64 {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func (s *settingsStore) setInt64(key string, value int64) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, strconv.FormatInt(value, 10),
	)
	return err
}

func (s *settingsStore) getString(key, fallback string) string {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return fallback
	}
	return raw
}

func (s *settingsStore) setString(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value,
	)
	return err
}

func (s *settingsStore) delete(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key=?`, key)
	return err
}

func (s *settingsStore) getBool(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(s.getString(key, "")))
	if raw == "" {
		return fallback
	}
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func (s *settingsStore) setBool(key string, value bool) error {
	if value {
		return s.setString(key, "true")
	}
	return s.setString(key, "false")
}

// --- HTTP (admin, behind the access gate) ---

// settingsResponse is the admin-facing view of the resolution settings. It
// reports the live mode so the UI can label things, plus the budgets so the
// admin understands the timeout that applies in each mode.
type settingsResponse struct {
	Concurrency      int  `json:"concurrency"`
	MaxConcurrency   int  `json:"max_concurrency"`
	Parallel         bool `json:"parallel"`
	Running          int  `json:"running"`
	Waiting          int  `json:"waiting"`
	SerialTimeoutS   int  `json:"serial_timeout_seconds"`
	ParallelTimeoutS int  `json:"parallel_timeout_seconds"`
	TaskTimeoutS     int  `json:"task_timeout_seconds"`
	MinTaskTimeoutS  int  `json:"min_task_timeout_seconds"`
	MaxTaskTimeoutS  int  `json:"max_task_timeout_seconds"`
}

const (
	settingKeyLinuxDoClientID            = "linuxdo_client_id"
	settingKeyLinuxDoClientSecret        = "linuxdo_client_secret"
	settingKeyLinuxDoLoginEnabled        = "linuxdo_login_enabled"
	settingKeyLinuxDoRegistrationEnabled = "linuxdo_registration_enabled"
	settingKeyEmailLoginEnabled          = "email_login_enabled"
	settingKeyEmailRegistrationEnabled   = "email_registration_enabled"
)

type authSettingsResponse struct {
	LinuxDoClientID               string `json:"linuxdo_client_id"`
	LinuxDoClientSecretConfigured bool   `json:"linuxdo_client_secret_configured"`
	LinuxDoCallbackURL            string `json:"linuxdo_callback_url"`
	LinuxDoConfigured             bool   `json:"linuxdo_configured"`
	LinuxDoLoginEnabled           bool   `json:"linuxdo_login_enabled"`
	LinuxDoRegistrationEnabled    bool   `json:"linuxdo_registration_enabled"`
	EmailLoginEnabled             bool   `json:"email_login_enabled"`
	EmailRegistrationEnabled      bool   `json:"email_registration_enabled"`
}

type updateAuthSettingsRequest struct {
	LinuxDoClientID            *string `json:"linuxdo_client_id"`
	LinuxDoClientSecret        *string `json:"linuxdo_client_secret"`
	ClearLinuxDoClientSecret   bool    `json:"clear_linuxdo_client_secret"`
	LinuxDoLoginEnabled        *bool   `json:"linuxdo_login_enabled"`
	LinuxDoRegistrationEnabled *bool   `json:"linuxdo_registration_enabled"`
	EmailLoginEnabled          *bool   `json:"email_login_enabled"`
	EmailRegistrationEnabled   *bool   `json:"email_registration_enabled"`
}

func (s *Server) linuxDoClientID() string {
	return strings.TrimSpace(s.settings.getString(settingKeyLinuxDoClientID, ""))
}

func (s *Server) linuxDoClientSecret() string {
	return strings.TrimSpace(s.settings.getString(settingKeyLinuxDoClientSecret, ""))
}

func (s *Server) linuxDoOAuthConfigured() bool {
	return s.linuxDoClientID() != "" && s.linuxDoClientSecret() != ""
}

func (s *Server) authSettingsPayload(r *http.Request) authSettingsResponse {
	clientID := s.linuxDoClientID()
	secretConfigured := s.linuxDoClientSecret() != ""
	return authSettingsResponse{
		LinuxDoClientID:               clientID,
		LinuxDoClientSecretConfigured: secretConfigured,
		LinuxDoCallbackURL:            strings.TrimRight(s.baseURL(r), "/") + "/api/u/auth/linuxdo/callback",
		LinuxDoConfigured:             clientID != "" && secretConfigured,
		LinuxDoLoginEnabled:           s.settings.getBool(settingKeyLinuxDoLoginEnabled, true),
		LinuxDoRegistrationEnabled:    s.settings.getBool(settingKeyLinuxDoRegistrationEnabled, true),
		EmailLoginEnabled:             s.settings.getBool(settingKeyEmailLoginEnabled, true),
		EmailRegistrationEnabled:      s.settings.getBool(settingKeyEmailRegistrationEnabled, false),
	}
}

func (s *Server) settingsPayload() settingsResponse {
	concurrency := s.resolver.concurrencyValue()
	serialTimeout, parallelTimeout, currentTimeout := s.resolver.timeoutSnapshot()
	minTimeout := minResolveTaskTimeout(s.config)
	maxTimeout := maxResolveTaskTimeout(minTimeout)
	return settingsResponse{
		Concurrency:      concurrency,
		MaxConcurrency:   maxResolveConcurrency,
		Parallel:         concurrency > 1,
		Running:          s.resolver.runningCount(),
		Waiting:          s.resolver.waiting(),
		SerialTimeoutS:   int(serialTimeout.Seconds()),
		ParallelTimeoutS: int(parallelTimeout.Seconds()),
		TaskTimeoutS:     int(currentTimeout.Seconds()),
		MinTaskTimeoutS:  int(minTimeout.Seconds()),
		MaxTaskTimeoutS:  int(maxTimeout.Seconds()),
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsPayload())
}

func (s *Server) handleGetAuthSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.authSettingsPayload(r))
}

func (s *Server) handleUpdateAuthSettings(w http.ResponseWriter, r *http.Request) {
	var req updateAuthSettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.LinuxDoClientID != nil {
		if err := s.settings.setString(settingKeyLinuxDoClientID, strings.TrimSpace(*req.LinuxDoClientID)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.ClearLinuxDoClientSecret {
		if err := s.settings.delete(settingKeyLinuxDoClientSecret); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else if req.LinuxDoClientSecret != nil {
		secret := strings.TrimSpace(*req.LinuxDoClientSecret)
		if secret != "" {
			if err := s.settings.setString(settingKeyLinuxDoClientSecret, secret); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if req.LinuxDoLoginEnabled != nil {
		if err := s.settings.setBool(settingKeyLinuxDoLoginEnabled, *req.LinuxDoLoginEnabled); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.LinuxDoRegistrationEnabled != nil {
		if err := s.settings.setBool(settingKeyLinuxDoRegistrationEnabled, *req.LinuxDoRegistrationEnabled); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.EmailLoginEnabled != nil {
		if err := s.settings.setBool(settingKeyEmailLoginEnabled, *req.EmailLoginEnabled); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.EmailRegistrationEnabled != nil {
		if err := s.settings.setBool(settingKeyEmailRegistrationEnabled, *req.EmailRegistrationEnabled); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.logJob(LogSuccess, "", "user authentication settings updated")
	writeJSON(w, http.StatusOK, s.authSettingsPayload(r))
}

type updateSettingsRequest struct {
	Concurrency        int `json:"concurrency"`
	TaskTimeoutSeconds int `json:"task_timeout_seconds"`
	TaskTimeoutMinutes int `json:"task_timeout_minutes"`
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req updateSettingsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Concurrency == 0 {
		req.Concurrency = s.resolver.concurrencyValue()
	}
	if req.Concurrency < 1 {
		writeError(w, http.StatusBadRequest, "并发数至少为 1")
		return
	}
	if req.Concurrency > maxResolveConcurrency {
		writeError(w, http.StatusBadRequest, "并发数最多为 "+strconv.Itoa(maxResolveConcurrency))
		return
	}
	timeoutSeconds := req.TaskTimeoutSeconds
	if timeoutSeconds == 0 && req.TaskTimeoutMinutes > 0 {
		timeoutSeconds = req.TaskTimeoutMinutes * 60
	}
	if timeoutSeconds > 0 {
		minTimeout := minResolveTaskTimeout(s.config)
		maxTimeout := maxResolveTaskTimeout(minTimeout)
		if timeoutSeconds < int(minTimeout.Seconds()) {
			writeError(w, http.StatusBadRequest, "任务超时时间至少为 "+strconv.Itoa(int(minTimeout.Minutes()))+" 分钟")
			return
		}
		if timeoutSeconds > int(maxTimeout.Seconds()) {
			writeError(w, http.StatusBadRequest, "任务超时时间最多为 "+strconv.Itoa(int(maxTimeout.Minutes()))+" 分钟")
			return
		}
	}

	applied := s.resolver.setConcurrency(req.Concurrency)
	if err := s.settings.setInt(settingKeyConcurrency, applied); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if timeoutSeconds > 0 {
		appliedTimeout := s.resolver.setTaskTimeout(time.Duration(timeoutSeconds) * time.Second)
		if err := s.settings.setInt(settingKeyTaskTimeoutSeconds, int(appliedTimeout.Seconds())); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	timeoutText := s.resolver.currentTimeout().String()
	if applied > 1 {
		s.logJob(LogSuccess, "", "已开启并行解析", "并发数："+strconv.Itoa(applied), "任务超时："+timeoutText)
	} else {
		s.logJob(LogSuccess, "", "已切换为串行解析", "任务超时："+timeoutText)
	}
	writeJSON(w, http.StatusOK, s.settingsPayload())
}
