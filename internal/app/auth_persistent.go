package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

const adminSessionMaxAge = 30 * 24 * time.Hour

type adminCredentialStore interface {
	HasPassword() bool
	Verify(string) bool
	SetInitial(string) error
	Set(string) error
}

type adminSessionStore interface {
	create() (string, error)
	validate(string) bool
	delete(string)
	invalidateAll()
}

func importLegacyAdminCredential(db *sql.DB, path string) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM admin_credentials`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = "data/auth.json"
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var rec credentialRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return err
	}
	if rec.Hash == "" || rec.Salt == "" {
		return errors.New("legacy admin credential is incomplete")
	}
	canonical, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT OR IGNORE INTO admin_credentials(id, password_hash, updated_at) VALUES(1,?,?)`,
		string(canonical), rec.UpdatedAt.Unix(),
	)
	return err
}

type databaseCredentialStore struct {
	mu  sync.RWMutex
	db  *sql.DB
	rec credentialRecord
}

func newDatabaseCredentialStore(db *sql.DB) (*databaseCredentialStore, error) {
	store := &databaseCredentialStore{db: db}
	var raw string
	err := db.QueryRow(`SELECT password_hash FROM admin_credentials WHERE id=1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), &store.rec); err != nil {
		return nil, err
	}
	return store, nil
}

func (c *databaseCredentialStore) HasPassword() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rec.Hash != ""
}

func (c *databaseCredentialStore) Verify(password string) bool {
	c.mu.RLock()
	rec := c.rec
	c.mu.RUnlock()
	return verifyPasswordRecord(rec, password)
}

func (c *databaseCredentialStore) SetInitial(password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rec.Hash != "" {
		return errors.New("password has already been set")
	}
	return c.setLocked(password, true)
}

func (c *databaseCredentialStore) Set(password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.setLocked(password, false)
}

func (c *databaseCredentialStore) setLocked(password string, initial bool) error {
	rec, err := hashPasswordRecord(password)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if initial {
		res, err := c.db.Exec(
			`INSERT OR IGNORE INTO admin_credentials(id, password_hash, updated_at) VALUES(1,?,?)`,
			string(raw), rec.UpdatedAt.Unix(),
		)
		if err != nil {
			return err
		}
		if rows, _ := res.RowsAffected(); rows != 1 {
			return errors.New("password has already been set")
		}
	} else {
		if _, err := c.db.Exec(
			`INSERT INTO admin_credentials(id, password_hash, updated_at) VALUES(1,?,?)
			 ON CONFLICT(id) DO UPDATE SET password_hash=excluded.password_hash, updated_at=excluded.updated_at`,
			string(raw), rec.UpdatedAt.Unix(),
		); err != nil {
			return err
		}
	}
	c.rec = rec
	return nil
}

type databaseAuthSessionStore struct {
	db  *sql.DB
	now func() time.Time
}

func newDatabaseAuthSessionStore(db *sql.DB, now func() time.Time) *databaseAuthSessionStore {
	if now == nil {
		now = time.Now
	}
	return &databaseAuthSessionStore{db: db, now: now}
}

func (s *databaseAuthSessionStore) create() (string, error) {
	token, err := generateSessionID()
	if err != nil {
		return "", err
	}
	now := s.now()
	_, err = s.db.Exec(
		`INSERT INTO admin_sessions(token_hash, expires_at, created_at) VALUES(?,?,?)`,
		sessionTokenDigest(token), now.Add(adminSessionMaxAge).Unix(), now.Unix(),
	)
	return token, err
}

func (s *databaseAuthSessionStore) validate(token string) bool {
	digest := sessionTokenDigest(token)
	if digest == "" {
		return false
	}
	var expiresAt int64
	err := s.db.QueryRow(`SELECT expires_at FROM admin_sessions WHERE token_hash=?`, digest).Scan(&expiresAt)
	if err != nil {
		return false
	}
	if expiresAt <= s.now().Unix() {
		_, _ = s.db.Exec(`DELETE FROM admin_sessions WHERE token_hash=?`, digest)
		return false
	}
	return true
}

func (s *databaseAuthSessionStore) delete(token string) {
	if digest := sessionTokenDigest(token); digest != "" {
		_, _ = s.db.Exec(`DELETE FROM admin_sessions WHERE token_hash=?`, digest)
	}
}

func (s *databaseAuthSessionStore) invalidateAll() {
	_, _ = s.db.Exec(`DELETE FROM admin_sessions`)
}
