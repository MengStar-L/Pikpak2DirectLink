package app

import (
	"sync"
	"time"
)

type LogLevel string

const (
	LogInfo    LogLevel = "info"
	LogSuccess LogLevel = "success"
	LogWarn    LogLevel = "warn"
	LogError   LogLevel = "error"
)

type LogEntry struct {
	ID      int64    `json:"id"`
	Time    string   `json:"time"`
	Level   LogLevel `json:"level"`
	JobID   string   `json:"job_id,omitempty"`
	Message string   `json:"message"`
	Details []string `json:"details,omitempty"`
}

type logStore struct {
	mu      sync.RWMutex
	nextID  int64
	limit   int
	entries []LogEntry
}

func newLogStore(limit int) *logStore {
	if limit <= 0 {
		limit = 300
	}
	return &logStore{
		limit: limit,
	}
}

func (s *logStore) add(level LogLevel, jobID, message string, details ...string) {
	if message == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	entry := LogEntry{
		ID:      s.nextID,
		Time:    time.Now().Format(time.RFC3339),
		Level:   level,
		JobID:   jobID,
		Message: message,
	}
	for _, detail := range details {
		if detail != "" {
			entry.Details = append(entry.Details, detail)
		}
	}

	s.entries = append(s.entries, entry)
	if extra := len(s.entries) - s.limit; extra > 0 {
		s.entries = append([]LogEntry(nil), s.entries[extra:]...)
	}
}

func (s *logStore) list(after int64) []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries := make([]LogEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.ID > after {
			copyEntry := entry
			if len(entry.Details) > 0 {
				copyEntry.Details = append([]string(nil), entry.Details...)
			}
			entries = append(entries, copyEntry)
		}
	}
	return entries
}

func (s *logStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = nil
}
