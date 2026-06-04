package app

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr              string
	PublicBaseURL     string
	Username          string
	Password          string
	RootFolderName    string
	SessionFile       string
	AccountsFile      string
	AccountSessionDir string
	RequestTimeout    time.Duration
	ResolveTimeout    time.Duration
	PollInterval      time.Duration
}

func LoadConfig() Config {
	return Config{
		Addr:              envOrDefault("ADDR", ":8080"),
		PublicBaseURL:     strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		Username:          strings.TrimSpace(os.Getenv("PIKPAK_USERNAME")),
		Password:          os.Getenv("PIKPAK_PASSWORD"),
		RootFolderName:    envOrDefault("PIKPAK_ROOT_FOLDER", "Pikpak2DirectLink"),
		SessionFile:       envOrDefault("PIKPAK_SESSION_FILE", "data/pikpak-session.json"),
		AccountsFile:      envOrDefault("PIKPAK_ACCOUNTS_FILE", "data/pikpak-accounts.json"),
		AccountSessionDir: envOrDefault("PIKPAK_ACCOUNT_SESSION_DIR", "data/accounts"),
		RequestTimeout:    durationOrDefault("PIKPAK_REQUEST_TIMEOUT", 20*time.Second),
		ResolveTimeout:    durationOrDefault("RESOLVE_TIMEOUT", 12*time.Minute),
		PollInterval:      durationOrDefault("POLL_INTERVAL", 5*time.Second),
	}
}

func (c Config) IsConfigured() bool {
	return c.Username != "" && c.Password != ""
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
