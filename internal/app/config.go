package app

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr                 string
	PublicBaseURL        string
	AccessPassword       string
	Username             string
	Password             string
	RootFolderName       string
	SessionFile          string
	AccountsFile         string
	AccountSessionDir    string
	AuthFile             string
	DBFile               string
	RequestTimeout       time.Duration
	ResolveTimeout       time.Duration
	QueueTimeout         time.Duration
	ParallelTimeout      time.Duration
	ResolveConcurrency   int
	PollInterval         time.Duration
	UpdateRepo           string
	UpdateCheckPeriod    time.Duration
	ShareParseTimeout    time.Duration // 分享链接解析超时
	ShareURLTimeout      time.Duration // 分享文件直链获取超时
	SharePollInterval    time.Duration // 分享文件直链轮询间隔
	AccountHealthURL     string
	AccountHealthEvery   time.Duration
	AccountRefreshGap    time.Duration
	AccountHealthTimeout time.Duration
}

func LoadConfig() Config {
	requestTimeout := durationOrDefault("PIKPAK_REQUEST_TIMEOUT", 20*time.Second)
	return Config{
		Addr:                 envOrDefault("ADDR", ":51873"),
		PublicBaseURL:        strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		AccessPassword:       os.Getenv("ACCESS_PASSWORD"),
		Username:             strings.TrimSpace(os.Getenv("PIKPAK_USERNAME")),
		Password:             os.Getenv("PIKPAK_PASSWORD"),
		RootFolderName:       envOrDefault("PIKPAK_ROOT_FOLDER", "Pikpak2DirectLink"),
		SessionFile:          envOrDefault("PIKPAK_SESSION_FILE", "data/pikpak-session.json"),
		AccountsFile:         envOrDefault("PIKPAK_ACCOUNTS_FILE", "data/pikpak-accounts.json"),
		AccountSessionDir:    envOrDefault("PIKPAK_ACCOUNT_SESSION_DIR", "data/accounts"),
		AuthFile:             envOrDefault("ACCESS_AUTH_FILE", "data/auth.json"),
		DBFile:               envOrDefault("DB_FILE", "data/pikpak.db"),
		RequestTimeout:       requestTimeout,
		ResolveTimeout:       durationOrDefault("RESOLVE_TIMEOUT", 12*time.Minute),
		QueueTimeout:         durationOrDefault("QUEUE_TIMEOUT", 60*time.Second),
		ParallelTimeout:      durationOrDefault("PARALLEL_QUEUE_TIMEOUT", 2*time.Minute),
		ResolveConcurrency:   intOrDefault("RESOLVE_CONCURRENCY", 1),
		PollInterval:         durationOrDefault("POLL_INTERVAL", 5*time.Second),
		UpdateRepo:           strings.TrimSpace(os.Getenv("UPDATE_REPO")),
		UpdateCheckPeriod:    durationOrDefault("UPDATE_CHECK_INTERVAL", 6*time.Hour),
		ShareParseTimeout:    durationOrDefault("SHARE_PARSE_TIMEOUT", 60*time.Second),
		ShareURLTimeout:      durationOrDefault("SHARE_URL_TIMEOUT", 60*time.Second),
		SharePollInterval:    durationOrDefault("SHARE_POLL_INTERVAL", 3*time.Second),
		AccountHealthURL:     envOrDefault("ACCOUNT_HEALTH_CHECK_URL", defaultAccountHealthCheckURL),
		AccountHealthEvery:   durationOrDefault("ACCOUNT_HEALTH_CHECK_INTERVAL", defaultAccountHealthCheckInterval),
		AccountRefreshGap:    durationOrDefault("ACCOUNT_AUTO_REFRESH_GAP", defaultAccountAutoRefreshGap),
		AccountHealthTimeout: durationOrDefault("ACCOUNT_HEALTH_CHECK_TIMEOUT", maxDuration(requestTimeout*3, time.Minute)),
	}
}

func (c Config) IsConfigured() bool {
	return c.Username != "" && c.Password != ""
}

// HasFixedPassword reports whether an access password was pinned via the
// ACCESS_PASSWORD environment variable. When set it overrides the first-visitor
// setup flow.
func (c Config) HasFixedPassword() bool {
	return strings.TrimSpace(c.AccessPassword) != ""
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func durationOrDefault(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	if parsed, err := time.ParseDuration(value); err == nil {
		return parsed
	}

	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	return fallback
}

func intOrDefault(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	if parsed, err := strconv.Atoi(value); err == nil {
		return parsed
	}
	return fallback
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
