package app

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// pbkdf2Iterations controls the work factor for password hashing. 600k matches
// the OWASP recommendation for PBKDF2-HMAC-SHA256.
const pbkdf2Iterations = 600000

type authSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]time.Time
}

func newAuthSessionStore() *authSessionStore {
	return &authSessionStore{
		sessions: make(map[string]time.Time),
	}
}

func (s *authSessionStore) create() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	sessionID := generateSessionID()
	s.sessions[sessionID] = time.Now().Add(30 * 24 * time.Hour)
	return sessionID
}

func (s *authSessionStore) validate(sessionID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	expiry, ok := s.sessions[sessionID]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		return false
	}
	return true
}

func (s *authSessionStore) delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// invalidateAll drops every session, e.g. after the access password changes so
// any other signed-in clients are forced to log in again.
func (s *authSessionStore) invalidateAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = make(map[string]time.Time)
}

func generateSessionID() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(buf)
}

// credentialStore persists a salted PBKDF2 hash of the admin password to disk so
// the first visitor can set a password that survives restarts. The plaintext is
// never written; only the salt and derived key are stored.
type credentialStore struct {
	mu   sync.RWMutex
	path string
	rec  credentialRecord
}

type credentialRecord struct {
	Algo       string    `json:"algo"`
	Iterations int       `json:"iterations"`
	Salt       string    `json:"salt"`
	Hash       string    `json:"hash"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func newCredentialStore(path string) (*credentialStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "data/auth.json"
	}
	store := &credentialStore{path: path}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (c *credentialStore) load() error {
	data, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var rec credentialRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return err
	}
	c.rec = rec
	return nil
}

// HasPassword reports whether an admin password has already been set.
func (c *credentialStore) HasPassword() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rec.Hash != ""
}

// Set replaces the stored password with a freshly salted PBKDF2 hash.
func (c *credentialStore) Set(password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setLocked(password)
}

// SetInitial sets the password only when none exists yet. It is the atomic
// backing for the first-visitor setup flow: concurrent callers cannot both win.
func (c *credentialStore) SetInitial(password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rec.Hash != "" {
		return errors.New("password has already been set")
	}
	return c.setLocked(password)
}

func (c *credentialStore) setLocked(password string) error {
	if strings.TrimSpace(password) == "" {
		return errors.New("password is required")
	}

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, 32)
	if err != nil {
		return err
	}

	c.rec = credentialRecord{
		Algo:       "pbkdf2-sha256",
		Iterations: pbkdf2Iterations,
		Salt:       hex.EncodeToString(salt),
		Hash:       hex.EncodeToString(hash),
		UpdatedAt:  time.Now(),
	}
	return c.saveLocked()
}

// Verify checks a candidate password against the stored hash in constant time.
func (c *credentialStore) Verify(password string) bool {
	c.mu.RLock()
	rec := c.rec
	c.mu.RUnlock()

	if rec.Hash == "" {
		return false
	}
	salt, err := hex.DecodeString(rec.Salt)
	if err != nil {
		return false
	}
	expected, err := hex.DecodeString(rec.Hash)
	if err != nil {
		return false
	}
	iterations := rec.Iterations
	if iterations <= 0 {
		iterations = pbkdf2Iterations
	}
	candidate, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(candidate, expected) == 1
}

func (c *credentialStore) saveLocked() error {
	data, err := json.MarshalIndent(c.rec, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(c.path, data, 0o600)
}
