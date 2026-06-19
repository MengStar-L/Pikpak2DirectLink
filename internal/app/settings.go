package app

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
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

func (s *Server) settingsPayload() settingsResponse {
	concurrency := s.resolver.concurrencyValue()
	serialTimeout, parallelTimeout, currentTimeout := s.resolver.timeoutSnapshot()
	return settingsResponse{
		Concurrency:      concurrency,
		MaxConcurrency:   maxResolveConcurrency,
		Parallel:         concurrency > 1,
		Running:          s.resolver.runningCount(),
		Waiting:          s.resolver.waiting(),
		SerialTimeoutS:   int(serialTimeout.Seconds()),
		ParallelTimeoutS: int(parallelTimeout.Seconds()),
		TaskTimeoutS:     int(currentTimeout.Seconds()),
		MinTaskTimeoutS:  minTaskTimeoutSeconds,
		MaxTaskTimeoutS:  maxTaskTimeoutSeconds,
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsPayload())
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
		if timeoutSeconds < minTaskTimeoutSeconds {
			writeError(w, http.StatusBadRequest, "任务超时时间至少为 1 分钟")
			return
		}
		if timeoutSeconds > maxTaskTimeoutSeconds {
			writeError(w, http.StatusBadRequest, "任务超时时间最多为 "+strconv.Itoa(maxTaskTimeoutSeconds/60)+" 分钟")
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
