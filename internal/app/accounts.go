package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pikpak2directlink/internal/pikpak"
)

type AccountStatus string

const (
	AccountAvailable AccountStatus = "available"
	AccountFailed    AccountStatus = "failed"
)

type AccountPoolConfig struct {
	AccountsFile   string
	SessionDir     string
	RootFolderName string
	RequestTimeout time.Duration
}

type AccountSummary struct {
	ID           string        `json:"id"`
	Username     string        `json:"username"`
	Status       AccountStatus `json:"status"`
	Ready        bool          `json:"ready"`
	LoggedIn     bool          `json:"logged_in"`
	Persisted    bool          `json:"persisted"`
	LastError    string        `json:"last_error,omitempty"`
	LastFailedAt string        `json:"last_failed_at,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

type AccountRuntime struct {
	ID       string
	Username string
	Client   *pikpak.Client
}

type accountRecord struct {
	ID           string        `json:"id"`
	Username     string        `json:"username"`
	Password     string        `json:"password"`
	SessionFile  string        `json:"session_file"`
	Status       AccountStatus `json:"status"`
	LastError    string        `json:"last_error,omitempty"`
	LastFailedAt string        `json:"last_failed_at,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

type accountState struct {
	record accountRecord
	client *pikpak.Client
}

type AccountPool struct {
	mu       sync.RWMutex
	config   AccountPoolConfig
	accounts map[string]*accountState
	order    []string
}

func NewAccountPool(cfg AccountPoolConfig) (*AccountPool, error) {
	cfg.AccountsFile = strings.TrimSpace(cfg.AccountsFile)
	if cfg.AccountsFile == "" {
		cfg.AccountsFile = "data/pikpak-accounts.json"
	}
	cfg.SessionDir = strings.TrimSpace(cfg.SessionDir)
	if cfg.SessionDir == "" {
		cfg.SessionDir = "data/accounts"
	}

	pool := &AccountPool{
		config:   cfg,
		accounts: make(map[string]*accountState),
	}
	if err := pool.load(); err != nil {
		return nil, err
	}
	return pool, nil
}

func (p *AccountPool) Add(ctx context.Context, username, password string) (AccountSummary, error) {
	username = strings.TrimSpace(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return AccountSummary{}, errors.New("username and password are required")
	}

	id, sessionFile := p.accountIdentity(username)
	client := p.newClient(username, password, sessionFile)
	if err := client.Login(ctx, username, password); err != nil {
		return AccountSummary{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	record, existed := p.accounts[id]
	createdAt := now
	if existed {
		createdAt = record.record.CreatedAt
	}

	p.accounts[id] = &accountState{
		record: accountRecord{
			ID:          id,
			Username:    username,
			Password:    password,
			SessionFile: sessionFile,
			Status:      AccountAvailable,
			CreatedAt:   createdAt,
			UpdatedAt:   now,
		},
		client: client,
	}
	if !existed {
		p.order = append(p.order, id)
	}
	if err := p.saveLocked(); err != nil {
		return AccountSummary{}, err
	}
	return p.summaryLocked(id), nil
}

func (p *AccountPool) AddBootstrap(username, password, sessionFile string) error {
	username = strings.TrimSpace(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return nil
	}

	id, defaultSessionFile := p.accountIdentity(username)
	if strings.TrimSpace(sessionFile) == "" {
		sessionFile = defaultSessionFile
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.accounts[id]; ok {
		return nil
	}

	now := time.Now()
	p.accounts[id] = &accountState{
		record: accountRecord{
			ID:          id,
			Username:    username,
			Password:    password,
			SessionFile: sessionFile,
			Status:      AccountAvailable,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		client: p.newClient(username, password, sessionFile),
	}
	p.order = append(p.order, id)
	return p.saveLocked()
}

func (p *AccountPool) List() []AccountSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	summaries := make([]AccountSummary, 0, len(p.order))
	for _, id := range p.order {
		summaries = append(summaries, p.summaryLocked(id))
	}
	return summaries
}

func (p *AccountPool) Snapshot() []AccountRuntime {
	p.mu.RLock()
	defer p.mu.RUnlock()

	accounts := make([]AccountRuntime, 0, len(p.order))
	for _, id := range p.order {
		state := p.accounts[id]
		if state == nil {
			continue
		}
		accounts = append(accounts, AccountRuntime{
			ID:       state.record.ID,
			Username: state.record.Username,
			Client:   state.client,
		})
	}
	return accounts
}

func (p *AccountPool) Get(id string) (AccountRuntime, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	state := p.accounts[id]
	if state == nil {
		return AccountRuntime{}, false
	}
	return AccountRuntime{
		ID:       state.record.ID,
		Username: state.record.Username,
		Client:   state.client,
	}, true
}

func (p *AccountPool) HasAccounts() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.order) > 0
}

func (p *AccountPool) Delete(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return errors.New("account not found")
	}

	delete(p.accounts, id)
	for i, accountID := range p.order {
		if accountID == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}

	if err := p.saveLocked(); err != nil {
		return err
	}
	if err := os.Remove(state.record.SessionFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (p *AccountPool) ResetFailure(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return errors.New("account not found")
	}
	state.record.Status = AccountAvailable
	state.record.LastError = ""
	state.record.LastFailedAt = ""
	state.record.UpdatedAt = time.Now()
	return p.saveLocked()
}

func (p *AccountPool) MarkFailed(id string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return
	}
	state.record.Status = AccountFailed
	state.record.LastError = err.Error()
	state.record.LastFailedAt = time.Now().Format(time.RFC3339)
	state.record.UpdatedAt = time.Now()
	_ = p.saveLocked()
}

func (p *AccountPool) MarkAvailable(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return
	}
	state.record.Status = AccountAvailable
	state.record.LastError = ""
	state.record.LastFailedAt = ""
	state.record.UpdatedAt = time.Now()
	_ = p.saveLocked()
}

func (p *AccountPool) load() error {
	data, err := os.ReadFile(p.config.AccountsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var records []accountRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return err
	}

	for _, record := range records {
		record.Username = strings.TrimSpace(record.Username)
		if record.ID == "" || record.Username == "" {
			continue
		}
		if record.SessionFile == "" {
			record.SessionFile = filepath.Join(p.config.SessionDir, record.ID+".json")
		}
		if record.Status == "" {
			record.Status = AccountAvailable
		}
		recordCopy := record
		p.accounts[record.ID] = &accountState{
			record: recordCopy,
			client: p.newClient(record.Username, record.Password, record.SessionFile),
		}
		p.order = append(p.order, record.ID)
	}
	return nil
}

func (p *AccountPool) saveLocked() error {
	records := make([]accountRecord, 0, len(p.order))
	for _, id := range p.order {
		state := p.accounts[id]
		if state == nil {
			continue
		}
		records = append(records, state.record)
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(p.config.AccountsFile)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(p.config.AccountsFile, data, 0o600)
}

func (p *AccountPool) summaryLocked(id string) AccountSummary {
	state := p.accounts[id]
	if state == nil {
		return AccountSummary{}
	}

	status := state.client.Status()
	return AccountSummary{
		ID:           state.record.ID,
		Username:     state.record.Username,
		Status:       state.record.Status,
		Ready:        status.Ready,
		LoggedIn:     status.LoggedIn,
		Persisted:    status.Persisted,
		LastError:    state.record.LastError,
		LastFailedAt: state.record.LastFailedAt,
		CreatedAt:    state.record.CreatedAt,
		UpdatedAt:    state.record.UpdatedAt,
	}
}

func (p *AccountPool) newClient(username, password, sessionFile string) *pikpak.Client {
	return pikpak.NewClient(pikpak.Config{
		Username:       username,
		Password:       password,
		SessionFile:    sessionFile,
		RootFolderName: p.config.RootFolderName,
		RequestTimeout: p.config.RequestTimeout,
	})
}

func (p *AccountPool) accountIdentity(username string) (id, sessionFile string) {
	id = accountIDForUsername(username)
	return id, filepath.Join(p.config.SessionDir, id+".json")
}

func accountIDForUsername(username string) string {
	if username != "" {
		return "acct_" + hex.EncodeToString([]byte(strings.ToLower(strings.TrimSpace(username))))
	}

	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("acct_%d", time.Now().UnixNano())
	}
	return "acct_" + hex.EncodeToString(buf)
}
