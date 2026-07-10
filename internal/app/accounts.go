package app

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"pikpak2directlink/internal/pikpak"
)

type AccountStatus string

const (
	AccountAvailable AccountStatus = "available"
	AccountFailed    AccountStatus = "failed"
)

// bytesPerGB is the byte count of one "G" of traffic. We use the binary
// gibibyte (1024³) consistently for both account limits and CDK allowances, so
// it lines up with the byte sizes PikPak reports for files.
const bytesPerGB = int64(1) << 30

// defaultAccountTraffic is the monthly downstream-traffic budget assigned to a
// new account (or an older record that predates traffic tracking): 700G.
const defaultAccountTraffic = 700 * bytesPerGB

type AccountPoolConfig struct {
	AccountsFile   string
	SessionDir     string
	RootFolderName string
	RequestTimeout time.Duration
	Store          *accountStore
}

type AccountSummary struct {
	ID                    string        `json:"id"`
	Username              string        `json:"username"`
	Status                AccountStatus `json:"status"`
	Ready                 bool          `json:"ready"`
	LoggedIn              bool          `json:"logged_in"`
	Persisted             bool          `json:"persisted"`
	Premium               bool          `json:"premium"`
	PremiumType           string        `json:"premium_type,omitempty"`
	PremiumUntil          string        `json:"premium_until,omitempty"`
	PremiumError          string        `json:"premium_error,omitempty"`
	PremiumCheckedAt      string        `json:"premium_checked_at,omitempty"`
	TrafficLimit          int64         `json:"traffic_limit"`
	TrafficUsed           int64         `json:"traffic_used"`
	TrafficLimited        bool          `json:"traffic_limited"`
	LastError             string        `json:"last_error,omitempty"`
	LastFailedAt          string        `json:"last_failed_at,omitempty"`
	CredentialCheckedAt   string        `json:"credential_checked_at,omitempty"`
	CredentialNextCheckAt string        `json:"credential_next_check_at,omitempty"`
	CredentialCheckError  string        `json:"credential_check_error,omitempty"`
	ParseErrorCount       int           `json:"parse_error_count"`
	ParseErrors           []ParseError  `json:"parse_errors,omitempty"`
	CreatedAt             time.Time     `json:"created_at"`
	UpdatedAt             time.Time     `json:"updated_at"`
}

type AccountRuntime struct {
	ID       string
	Username string
	Client   pikpakClient
}

type pikpakClient interface {
	Login(ctx context.Context, username, password string) error
	Status() pikpak.SessionStatus
	EnsureRootFolder(ctx context.Context) (string, error)
	CreateFolder(ctx context.Context, name, parentID string) (*pikpak.FileEntry, error)
	CreateOfflineTask(ctx context.Context, sourceURL, parentID, name string) (*pikpak.TaskEntry, error)
	ListOfflineTasks(ctx context.Context, phases []string) ([]pikpak.TaskEntry, error)
	ListFiles(ctx context.Context, parentID string) ([]pikpak.FileEntry, error)
	GetFile(ctx context.Context, fileID string) (*pikpak.FileEntry, error)
	GetVIPInfo(ctx context.Context) (*pikpak.VIPInfo, error)
	GetShareInfo(ctx context.Context, shareID, passCode, parentID string) (*pikpak.ShareListResponse, error)
	GetShareFolder(ctx context.Context, shareID, passCodeToken, parentID string) (*pikpak.ShareListResponse, error)
	RestoreShare(ctx context.Context, shareID, passCodeToken string, fileIDs []string) (*pikpak.RestoreShareResponse, error)
	WaitForFileDownloadURL(ctx context.Context, fileID string, timeout, pollInterval time.Duration) (*pikpak.FileEntry, error)
	DeleteFiles(ctx context.Context, fileIDs []string) error
}

type accountRecord struct {
	ID                    string        `json:"id"`
	Username              string        `json:"username"`
	Password              string        `json:"password"`
	SessionFile           string        `json:"session_file"`
	Status                AccountStatus `json:"status"`
	Premium               bool          `json:"premium"`
	PremiumType           string        `json:"premium_type,omitempty"`
	PremiumUntil          string        `json:"premium_until,omitempty"`
	PremiumError          string        `json:"premium_error,omitempty"`
	PremiumCheckedAt      string        `json:"premium_checked_at,omitempty"`
	TrafficLimit          int64         `json:"traffic_limit,omitempty"`
	TrafficUsed           int64         `json:"traffic_used,omitempty"`
	TrafficPeriod         string        `json:"traffic_period,omitempty"`
	LastError             string        `json:"last_error,omitempty"`
	LastFailedAt          string        `json:"last_failed_at,omitempty"`
	CredentialCheckedAt   string        `json:"credential_checked_at,omitempty"`
	CredentialNextCheckAt string        `json:"credential_next_check_at,omitempty"`
	CredentialCheckError  string        `json:"credential_check_error,omitempty"`
	ParseErrors           []ParseError  `json:"parse_errors,omitempty"`
	CreatedAt             time.Time     `json:"created_at"`
	UpdatedAt             time.Time     `json:"updated_at"`
}

type ParseError struct {
	Time    string `json:"time"`
	JobID   string `json:"job_id,omitempty"`
	Message string `json:"message"`
}

type accountState struct {
	record accountRecord
	client pikpakClient
}

type accountRecordReplacer interface {
	Replace([]accountRecord) error
}

type AccountPool struct {
	mu             sync.RWMutex
	config         AccountPoolConfig
	accounts       map[string]*accountState
	order          []string
	cursor         uint64 // rotating round-robin starting point for parallel resolves
	store          *accountStore
	recordReplacer accountRecordReplacer
}

type accountPoolSnapshot struct {
	accounts map[string]*accountState
	order    []string
}

const (
	premiumRefreshInterval    = 30 * time.Minute
	premiumRefreshConcurrency = 4
)

const badResourceParseUserError = "该磁链连续遇到解析错误，请不要反复重试此链接。"

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
		store:    cfg.Store,
	}
	if cfg.Store != nil {
		pool.recordReplacer = cfg.Store
	}
	if err := pool.load(); err != nil {
		return nil, err
	}
	return pool, nil
}

func (p *AccountPool) Add(ctx context.Context, username, password string, trafficLimit int64) (AccountSummary, error) {
	username = strings.TrimSpace(username)
	if username == "" || strings.TrimSpace(password) == "" {
		return AccountSummary{}, errors.New("username and password are required")
	}
	if trafficLimit <= 0 {
		trafficLimit = defaultAccountTraffic
	}

	id, sessionFile := p.accountIdentity(username)
	var staged *stagingSessionStore
	var client *pikpak.Client
	if p.store != nil {
		staged = newStagingSessionStore()
		client = p.newClientWithSessionStore(username, password, staged)
	} else {
		client = p.newClient(id, username, password, sessionFile)
	}
	if err := client.Login(ctx, username, password); err != nil {
		return AccountSummary{}, err
	}
	premiumInfo, premiumErr := client.GetVIPInfo(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot := p.snapshotLocked()

	now := time.Now()
	existingState, existed := p.accounts[id]
	createdAt := now
	if existed {
		createdAt = existingState.record.CreatedAt
	}

	record := accountRecord{
		ID:                    id,
		Username:              username,
		Password:              password,
		SessionFile:           sessionFile,
		Status:                AccountAvailable,
		TrafficLimit:          trafficLimit,
		TrafficPeriod:         monthKey(now),
		CredentialNextCheckAt: formatAccountTime(now),
		CreatedAt:             createdAt,
		UpdatedAt:             now,
	}
	// Re-adding an existing account keeps its accrued usage for the month.
	if existed {
		record.TrafficUsed = existingState.record.TrafficUsed
		record.ParseErrors = append([]ParseError(nil), existingState.record.ParseErrors...)
		record.CredentialCheckedAt = existingState.record.CredentialCheckedAt
		record.CredentialNextCheckAt = existingState.record.CredentialNextCheckAt
		record.CredentialCheckError = existingState.record.CredentialCheckError
		if existingState.record.TrafficPeriod != "" {
			record.TrafficPeriod = existingState.record.TrafficPeriod
		}
	}
	updatePremiumRecord(&record, premiumInfo, premiumErr, now)
	if p.store != nil {
		session, err := staged.Load()
		if err != nil {
			return AccountSummary{}, fmt.Errorf("read staged PikPak session: %w", err)
		}
		record.SessionFile = ""
		if err := p.store.UpsertWithSession(record, session); err != nil {
			return AccountSummary{}, err
		}
		client = p.newClient(id, username, password, "")
	}

	p.accounts[id] = &accountState{
		record: record,
		client: client,
	}
	if !existed {
		p.order = append(p.order, id)
	}
	if p.store == nil {
		if err := p.saveOrRollbackLocked(snapshot); err != nil {
			return AccountSummary{}, err
		}
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
	snapshot := p.snapshotLocked()

	if _, ok := p.accounts[id]; ok {
		return nil
	}

	now := time.Now()
	record := accountRecord{
		ID:                    id,
		Username:              username,
		Password:              password,
		SessionFile:           sessionFile,
		Status:                AccountAvailable,
		TrafficLimit:          defaultAccountTraffic,
		TrafficPeriod:         monthKey(now),
		CredentialNextCheckAt: formatAccountTime(now),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if p.store != nil {
		record.SessionFile = ""
		if data, err := os.ReadFile(sessionFile); err == nil {
			if err := p.store.UpsertWithSession(record, data); err != nil {
				return err
			}
		} else if os.IsNotExist(err) {
			if err := p.store.Insert(record); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	p.accounts[id] = &accountState{record: record, client: p.newClient(id, username, password, sessionFile)}
	p.order = append(p.order, id)
	if p.store != nil {
		return nil
	}
	return p.saveOrRollbackLocked(snapshot)
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

func (p *AccountPool) Summary(id string) (AccountSummary, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.accounts[id] == nil {
		return AccountSummary{}, false
	}
	return p.summaryLocked(id), true
}

func (p *AccountPool) EnsureCredentialSchedule(now time.Time, interval time.Duration) error {
	if interval <= 0 {
		interval = defaultAccountHealthCheckInterval
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot := p.snapshotLocked()

	changed := false
	next := formatAccountTime(now.Add(interval))
	for _, id := range p.order {
		state := p.accounts[id]
		if state == nil {
			continue
		}
		if _, err := parseAccountTime(state.record.CredentialNextCheckAt); state.record.CredentialNextCheckAt == "" || err != nil {
			state.record.CredentialNextCheckAt = next
			state.record.UpdatedAt = now
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return p.saveOrRollbackLocked(snapshot)
}

type credentialCheckTarget struct {
	Account AccountRuntime
	NextAt  time.Time
}

func (p *AccountPool) NextCredentialCheck(now time.Time) (credentialCheckTarget, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var target credentialCheckTarget
	for _, id := range p.order {
		state := p.accounts[id]
		if state == nil {
			continue
		}
		nextAt, err := parseAccountTime(state.record.CredentialNextCheckAt)
		if err != nil {
			nextAt = now
		}
		if target.Account.ID != "" && !nextAt.Before(target.NextAt) {
			continue
		}
		target = credentialCheckTarget{
			Account: AccountRuntime{
				ID:       state.record.ID,
				Username: state.record.Username,
				Client:   state.client,
			},
			NextAt: nextAt,
		}
	}
	return target, target.Account.ID != ""
}

func (p *AccountPool) RefreshPremiumInfo(ctx context.Context) error {
	now := time.Now()

	type target struct {
		id     string
		state  *accountState
		client pikpakClient
	}
	type result struct {
		target    target
		info      *pikpak.VIPInfo
		err       error
		checkedAt time.Time
	}

	p.mu.RLock()
	targets := make([]target, 0, len(p.order))
	for _, id := range p.order {
		state := p.accounts[id]
		if state == nil || state.client == nil {
			continue
		}
		if premiumInfoNeedsRefresh(state.record, now) {
			targets = append(targets, target{
				id:     id,
				state:  state,
				client: state.client,
			})
		}
	}
	p.mu.RUnlock()
	if len(targets) == 0 {
		return nil
	}

	jobs := make(chan target, len(targets))
	results := make(chan result, len(targets))
	for _, item := range targets {
		jobs <- item
	}
	close(jobs)

	workerCount := min(len(targets), premiumRefreshConcurrency)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for item := range jobs {
				if ctx.Err() != nil {
					return
				}
				info, err := item.client.GetVIPInfo(ctx)
				results <- result{target: item, info: info, err: err, checkedAt: time.Now()}
			}
		}()
	}
	workers.Wait()
	close(results)

	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot := p.snapshotLocked()
	changed := false
	for item := range results {
		state := p.accounts[item.target.id]
		if state == nil || state != item.target.state || !samePikPakClient(state.client, item.target.client) {
			continue
		}
		updatePremiumRecord(&state.record, item.info, item.err, item.checkedAt)
		state.record.UpdatedAt = item.checkedAt
		changed = true
	}
	if !changed {
		return nil
	}
	return p.saveOrRollbackLocked(snapshot)
}

func samePikPakClient(current, snapshot pikpakClient) bool {
	currentValue := reflect.ValueOf(current)
	snapshotValue := reflect.ValueOf(snapshot)
	if !currentValue.IsValid() || !snapshotValue.IsValid() {
		return !currentValue.IsValid() && !snapshotValue.IsValid()
	}
	if currentValue.Type() != snapshotValue.Type() || !currentValue.Comparable() {
		return false
	}
	return currentValue.Interface() == snapshotValue.Interface()
}

func (p *AccountPool) MarkCredentialCheckSuccess(id string, checkedAt, nextAt time.Time, cleanupErr error) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return nil
	}
	snapshot := p.snapshotLocked()
	state.record.Status = AccountAvailable
	state.record.LastError = ""
	state.record.LastFailedAt = ""
	state.record.CredentialCheckedAt = formatAccountTime(checkedAt)
	state.record.CredentialNextCheckAt = formatAccountTime(nextAt)
	state.record.CredentialCheckError = ""
	if cleanupErr != nil {
		state.record.CredentialCheckError = "测试文件清理失败：" + friendlyPikPakError(cleanupErr)
	}
	state.record.UpdatedAt = checkedAt
	return p.saveOrRollbackLocked(snapshot)
}

func (p *AccountPool) MarkCredentialCheckFailed(id string, err error, checkedAt, nextAt time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return nil
	}
	snapshot := p.snapshotLocked()
	message := friendlyPikPakError(err)
	if message == "" {
		message = "账号凭据验证失败"
	}
	state.record.Status = AccountFailed
	state.record.LastError = message
	state.record.LastFailedAt = checkedAt.Format(time.RFC3339)
	state.record.CredentialCheckedAt = formatAccountTime(checkedAt)
	state.record.CredentialNextCheckAt = formatAccountTime(nextAt)
	state.record.CredentialCheckError = message
	state.record.UpdatedAt = checkedAt
	return p.saveOrRollbackLocked(snapshot)
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

// ResolveOrder returns accounts in the order a resolve job should try them.
//
// Accounts that have hit their monthly downstream-traffic limit are excluded
// entirely (in both modes) — that exclusion is the whole point of the limit, so
// they are not even used as a fallback. When rotate is false (serial mode) the
// remaining accounts keep their stored order, regardless of failure status, as
// before. When rotate is true (parallel mode) currently-available accounts come
// first, rotated by a per-call cursor so concurrent jobs fan out instead of all
// hammering the first one; failed accounts are appended last as a fallback.
func (p *AccountPool) ResolveOrder(rotate bool) []AccountRuntime {
	now := time.Now()
	p.mu.RLock()
	if !rotate {
		accounts := make([]AccountRuntime, 0, len(p.order))
		for _, id := range p.order {
			state := p.accounts[id]
			if state == nil || accountTrafficLimited(state.record, now) {
				continue
			}
			accounts = append(accounts, AccountRuntime{
				ID:       state.record.ID,
				Username: state.record.Username,
				Client:   state.client,
			})
		}
		p.mu.RUnlock()
		return accounts
	}

	var available, failed []AccountRuntime
	for _, id := range p.order {
		state := p.accounts[id]
		if state == nil || accountTrafficLimited(state.record, now) {
			continue
		}
		rt := AccountRuntime{
			ID:       state.record.ID,
			Username: state.record.Username,
			Client:   state.client,
		}
		if state.record.Status == AccountFailed {
			failed = append(failed, rt)
		} else {
			available = append(available, rt)
		}
	}
	p.mu.RUnlock()

	if len(available) > 1 {
		off := int((atomic.AddUint64(&p.cursor, 1) - 1) % uint64(len(available)))
		rotated := make([]AccountRuntime, 0, len(available))
		rotated = append(rotated, available[off:]...)
		rotated = append(rotated, available[:off]...)
		available = rotated
	}
	return append(available, failed...)
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
	snapshot := p.snapshotLocked()

	delete(p.accounts, id)
	for i, accountID := range p.order {
		if accountID == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}

	if err := p.saveOrRollbackLocked(snapshot); err != nil {
		return err
	}
	if p.store == nil {
		if err := os.Remove(state.record.SessionFile); err != nil && !os.IsNotExist(err) {
			return err
		}
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
	snapshot := p.snapshotLocked()
	state.record.Status = AccountAvailable
	state.record.LastError = ""
	state.record.LastFailedAt = ""
	state.record.UpdatedAt = time.Now()
	return p.saveOrRollbackLocked(snapshot)
}

func (p *AccountPool) RefreshLogin(ctx context.Context, id string) (AccountSummary, error) {
	p.mu.RLock()
	state := p.accounts[id]
	if state == nil {
		p.mu.RUnlock()
		return AccountSummary{}, errors.New("account not found")
	}
	record := state.record
	p.mu.RUnlock()

	if strings.TrimSpace(record.Username) == "" || strings.TrimSpace(record.Password) == "" {
		return AccountSummary{}, errors.New("account username or password is missing")
	}

	client := p.newClient(record.ID, record.Username, record.Password, record.SessionFile)
	if err := client.Login(ctx, record.Username, record.Password); err != nil {
		return AccountSummary{}, err
	}
	premiumInfo, premiumErr := client.GetVIPInfo(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()

	state = p.accounts[id]
	if state == nil {
		return AccountSummary{}, errors.New("account not found")
	}
	snapshot := p.snapshotLocked()

	now := time.Now()
	state.client = client
	state.record.Status = AccountAvailable
	state.record.LastError = ""
	state.record.LastFailedAt = ""
	updatePremiumRecord(&state.record, premiumInfo, premiumErr, now)
	state.record.UpdatedAt = now
	if err := p.saveOrRollbackLocked(snapshot); err != nil {
		return AccountSummary{}, err
	}
	return p.summaryLocked(id), nil
}

func (p *AccountPool) MarkFailed(id string, err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return nil
	}
	snapshot := p.snapshotLocked()
	state.record.Status = AccountFailed
	state.record.LastError = friendlyPikPakError(err)
	state.record.LastFailedAt = time.Now().Format(time.RFC3339)
	state.record.UpdatedAt = time.Now()
	return p.saveOrRollbackLocked(snapshot)
}

func (p *AccountPool) MarkAvailable(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return nil
	}
	snapshot := p.snapshotLocked()
	state.record.Status = AccountAvailable
	state.record.LastError = ""
	state.record.LastFailedAt = ""
	state.record.UpdatedAt = time.Now()
	return p.saveOrRollbackLocked(snapshot)
}

func (p *AccountPool) RecordParseError(id, jobID, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return
	}
	snapshot := p.snapshotLocked()
	message = strings.TrimSpace(message)
	if message == "" {
		message = "record not found"
	}
	now := time.Now()
	state.record.ParseErrors = append(state.record.ParseErrors, ParseError{
		Time:    now.Format(time.RFC3339),
		JobID:   strings.TrimSpace(jobID),
		Message: message,
	})
	state.record.UpdatedAt = now
	_ = p.saveOrRollbackLocked(snapshot)
}

func (p *AccountPool) DeleteParseError(id string, index int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return errors.New("account not found")
	}
	if index < 0 || index >= len(state.record.ParseErrors) {
		return errors.New("parse error not found")
	}
	snapshot := p.snapshotLocked()
	state.record.ParseErrors = append(state.record.ParseErrors[:index], state.record.ParseErrors[index+1:]...)
	state.record.UpdatedAt = time.Now()
	return p.saveOrRollbackLocked(snapshot)
}

// SetTrafficLimit updates an account's monthly downstream budget (in bytes).
func (p *AccountPool) SetTrafficLimit(id string, limitBytes int64) error {
	if limitBytes <= 0 {
		limitBytes = defaultAccountTraffic
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return errors.New("account not found")
	}
	snapshot := p.snapshotLocked()
	state.record.TrafficLimit = limitBytes
	state.record.UpdatedAt = time.Now()
	return p.saveOrRollbackLocked(snapshot)
}

// AddTraffic records bytes of downstream traffic against an account for the
// current month. The counter rolls over automatically when the calendar month
// changes, which is how the monthly "到达限行流量" state clears itself.
func (p *AccountPool) AddTraffic(id string, bytes int64) {
	if bytes <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return
	}
	snapshot := p.snapshotLocked()
	now := time.Now()
	mk := monthKey(now)
	if state.record.TrafficPeriod != mk {
		state.record.TrafficUsed = 0
		state.record.TrafficPeriod = mk
	}
	state.record.TrafficUsed += bytes
	state.record.UpdatedAt = now
	_ = p.saveOrRollbackLocked(snapshot)
}

type accountTrafficUpdate struct {
	pool    *AccountPool
	id      string
	record  accountRecord
	changed bool
	closed  bool
}

func (p *AccountPool) beginTrafficUpdate(id string, bytes int64, now time.Time) (*accountTrafficUpdate, error) {
	if p == nil || p.store == nil {
		return nil, errors.New("durable account storage is not configured")
	}
	p.mu.Lock()
	state := p.accounts[id]
	if state == nil {
		p.mu.Unlock()
		return nil, errors.New("account not found")
	}
	record := state.record
	record.ParseErrors = append([]ParseError(nil), state.record.ParseErrors...)
	update := &accountTrafficUpdate{pool: p, id: id, record: record, changed: bytes > 0}
	if bytes > 0 {
		period := monthKey(now)
		if update.record.TrafficPeriod != period {
			update.record.TrafficUsed = 0
			update.record.TrafficPeriod = period
		}
		update.record.TrafficUsed += bytes
		update.record.UpdatedAt = now
	}
	return update, nil
}

func (u *accountTrafficUpdate) writeTx(tx *sql.Tx) error {
	if u == nil || !u.changed {
		return nil
	}
	return u.pool.store.updateTx(tx, u.record)
}

func (u *accountTrafficUpdate) finish(committed bool) {
	if u == nil || u.closed {
		return
	}
	if committed && u.changed {
		if state := u.pool.accounts[u.id]; state != nil {
			state.record = u.record
		}
	}
	u.closed = true
	u.pool.mu.Unlock()
}

func (p *AccountPool) load() error {
	if p.store != nil {
		records, err := p.store.List()
		if err != nil {
			return err
		}
		for _, record := range records {
			recordCopy := record
			p.accounts[record.ID] = &accountState{
				record: recordCopy,
				client: p.newClient(record.ID, record.Username, record.Password, ""),
			}
			p.order = append(p.order, record.ID)
		}
		return nil
	}
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
		// Records created before traffic tracking get the default budget and the
		// current month as their baseline period.
		if record.TrafficLimit <= 0 {
			record.TrafficLimit = defaultAccountTraffic
		}
		if record.TrafficPeriod == "" {
			record.TrafficPeriod = monthKey(time.Now())
		}
		recordCopy := record
		p.accounts[record.ID] = &accountState{
			record: recordCopy,
			client: p.newClient(record.ID, record.Username, record.Password, record.SessionFile),
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
	if p.recordReplacer != nil {
		return p.recordReplacer.Replace(records)
	}
	if p.store != nil {
		return p.store.Replace(records)
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

func (p *AccountPool) snapshotLocked() accountPoolSnapshot {
	snapshot := accountPoolSnapshot{
		accounts: make(map[string]*accountState, len(p.accounts)),
		order:    append([]string(nil), p.order...),
	}
	for id, state := range p.accounts {
		if state == nil {
			snapshot.accounts[id] = nil
			continue
		}
		stateCopy := *state
		stateCopy.record.ParseErrors = append([]ParseError(nil), state.record.ParseErrors...)
		snapshot.accounts[id] = &stateCopy
	}
	return snapshot
}

func (p *AccountPool) saveOrRollbackLocked(snapshot accountPoolSnapshot) error {
	if err := p.saveLocked(); err != nil {
		p.accounts = snapshot.accounts
		p.order = snapshot.order
		return err
	}
	return nil
}

func (p *AccountPool) summaryLocked(id string) AccountSummary {
	state := p.accounts[id]
	if state == nil {
		return AccountSummary{}
	}

	status := state.client.Status()
	now := time.Now()
	return AccountSummary{
		ID:                    state.record.ID,
		Username:              state.record.Username,
		Status:                state.record.Status,
		Ready:                 status.Ready,
		LoggedIn:              status.LoggedIn,
		Persisted:             status.Persisted,
		Premium:               state.record.Premium,
		PremiumType:           state.record.PremiumType,
		PremiumUntil:          state.record.PremiumUntil,
		PremiumError:          friendlyPikPakMessage(state.record.PremiumError),
		PremiumCheckedAt:      state.record.PremiumCheckedAt,
		TrafficLimit:          state.record.TrafficLimit,
		TrafficUsed:           effectiveTrafficUsed(state.record, now),
		TrafficLimited:        accountTrafficLimited(state.record, now),
		LastError:             friendlyPikPakMessage(state.record.LastError),
		LastFailedAt:          state.record.LastFailedAt,
		CredentialCheckedAt:   state.record.CredentialCheckedAt,
		CredentialNextCheckAt: state.record.CredentialNextCheckAt,
		CredentialCheckError:  friendlyPikPakMessage(state.record.CredentialCheckError),
		ParseErrorCount:       len(state.record.ParseErrors),
		ParseErrors:           append([]ParseError(nil), state.record.ParseErrors...),
		CreatedAt:             state.record.CreatedAt,
		UpdatedAt:             state.record.UpdatedAt,
	}
}

func (p *AccountPool) newClient(id, username, password, sessionFile string) *pikpak.Client {
	config := pikpak.Config{
		Username:       username,
		Password:       password,
		SessionFile:    sessionFile,
		RootFolderName: p.config.RootFolderName,
		RequestTimeout: p.config.RequestTimeout,
	}
	if p.store != nil {
		config.SessionFile = ""
		config.SessionStore = p.store.SessionStore(id)
	}
	return pikpak.NewClient(config)
}

func (p *AccountPool) newClientWithSessionStore(username, password string, store pikpak.SessionStore) *pikpak.Client {
	return pikpak.NewClient(pikpak.Config{
		Username:       username,
		Password:       password,
		SessionStore:   store,
		RootFolderName: p.config.RootFolderName,
		RequestTimeout: p.config.RequestTimeout,
	})
}

func (p *AccountPool) accountIdentity(username string) (id, sessionFile string) {
	if p.store != nil {
		normalized := strings.ToLower(strings.TrimSpace(username))
		for existingID, state := range p.accounts {
			if state != nil && strings.ToLower(strings.TrimSpace(state.record.Username)) == normalized {
				return existingID, ""
			}
		}
		return "acct_" + strings.ReplaceAll(uuid.NewString(), "-", ""), ""
	}
	id = accountIDForUsername(username)
	return id, filepath.Join(p.config.SessionDir, id+".json")
}

func accountIDForUsername(username string) string {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return ""
	}
	return "acct_" + hex.EncodeToString([]byte(username))
}

// monthKey is the calendar-month identifier used to scope traffic counters.
func monthKey(t time.Time) string {
	return t.Format("2006-01")
}

func parseAccountTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("time is empty")
	}
	return time.Parse(time.RFC3339, value)
}

func formatAccountTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// effectiveTrafficUsed returns the bytes used in the month that contains now.
// A counter stamped with an earlier month is treated as already reset to 0,
// which is what makes the monthly refresh automatic (no scheduler needed).
func effectiveTrafficUsed(rec accountRecord, now time.Time) int64 {
	if rec.TrafficPeriod != monthKey(now) {
		return 0
	}
	if rec.TrafficUsed < 0 {
		return 0
	}
	return rec.TrafficUsed
}

// accountTrafficLimited reports whether an account has spent its monthly budget.
// A non-positive limit is treated as unlimited.
func accountTrafficLimited(rec accountRecord, now time.Time) bool {
	if rec.TrafficLimit <= 0 {
		return false
	}
	return effectiveTrafficUsed(rec, now) >= rec.TrafficLimit
}

func premiumInfoNeedsRefresh(record accountRecord, now time.Time) bool {
	if record.PremiumCheckedAt == "" {
		return true
	}

	checkedAt, err := time.Parse(time.RFC3339, record.PremiumCheckedAt)
	if err != nil {
		return true
	}
	return now.Sub(checkedAt) >= premiumRefreshInterval
}

func updatePremiumRecord(record *accountRecord, info *pikpak.VIPInfo, err error, checkedAt time.Time) {
	record.PremiumCheckedAt = checkedAt.Format(time.RFC3339)
	if err != nil {
		record.PremiumError = friendlyPikPakError(err)
		return
	}
	if info == nil {
		record.PremiumError = "empty premium response"
		return
	}

	record.Premium = info.IsPremium()
	record.PremiumType = strings.TrimSpace(info.Data.Type)
	record.PremiumUntil = strings.TrimSpace(info.Expiration())
	record.PremiumError = ""
}

func friendlyPikPakError(err error) string {
	if err == nil {
		return ""
	}
	return friendlyPikPakMessage(err.Error())
}

func friendlyPikPakMessage(message string) string {
	message = strings.TrimSpace(message)
	lower := strings.ToLower(message)
	if strings.Contains(lower, "result:review") ||
		strings.Contains(lower, `value:"review"`) ||
		strings.Contains(lower, "value:\"review\"") {
		return "PikPak 触发登录风控，请先在官方客户端完成验证后再重试。"
	}
	if isResourceUnavailableMessage(lower) {
		return "该资源涉及版权或违规内容，或已失效，已被 PikPak 下架，无法解析。"
	}
	return message
}

// isResourceUnavailableError reports whether an error is a deterministic
// resource-level refusal (the magnet/share was taken down for copyright or
// harmful content, or otherwise no longer exists). Such an error is NOT the
// account's fault — every account would hit the same refusal — so the caller
// must not blacklist the account over it.
func isResourceUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	return isResourceUnavailableMessage(strings.ToLower(err.Error()))
}

func isResourceUnavailableMessage(lower string) bool {
	return strings.Contains(lower, "copyright") ||
		strings.Contains(lower, "harmful content") ||
		strings.Contains(lower, "no longer available")
}

// isResourceParseError reports whether PikPak rejected this specific resource
// while the account/session itself should stay healthy.
func isResourceParseError(err error) bool {
	if err == nil {
		return false
	}
	return isResourceParseMessage(strings.ToLower(err.Error()))
}

func isResourceParseMessage(lower string) bool {
	return strings.Contains(lower, "record not found") ||
		(strings.Contains(lower, "selected share file") && strings.Contains(lower, "could not be found after restore"))
}

func isBadResourceUserError(message string) bool {
	return strings.TrimSpace(message) == badResourceParseUserError
}
