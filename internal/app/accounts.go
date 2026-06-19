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
	"sync/atomic"
	"time"

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
}

type AccountSummary struct {
	ID               string        `json:"id"`
	Username         string        `json:"username"`
	Status           AccountStatus `json:"status"`
	Ready            bool          `json:"ready"`
	LoggedIn         bool          `json:"logged_in"`
	Persisted        bool          `json:"persisted"`
	Premium          bool          `json:"premium"`
	PremiumType      string        `json:"premium_type,omitempty"`
	PremiumUntil     string        `json:"premium_until,omitempty"`
	PremiumError     string        `json:"premium_error,omitempty"`
	PremiumCheckedAt string        `json:"premium_checked_at,omitempty"`
	TrafficLimit     int64         `json:"traffic_limit"`
	TrafficUsed      int64         `json:"traffic_used"`
	TrafficLimited   bool          `json:"traffic_limited"`
	LastError        string        `json:"last_error,omitempty"`
	LastFailedAt     string        `json:"last_failed_at,omitempty"`
	ParseErrorCount  int           `json:"parse_error_count"`
	ParseErrors      []ParseError  `json:"parse_errors,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

type AccountRuntime struct {
	ID       string
	Username string
	Client   *pikpak.Client
}

type accountRecord struct {
	ID               string        `json:"id"`
	Username         string        `json:"username"`
	Password         string        `json:"password"`
	SessionFile      string        `json:"session_file"`
	Status           AccountStatus `json:"status"`
	Premium          bool          `json:"premium"`
	PremiumType      string        `json:"premium_type,omitempty"`
	PremiumUntil     string        `json:"premium_until,omitempty"`
	PremiumError     string        `json:"premium_error,omitempty"`
	PremiumCheckedAt string        `json:"premium_checked_at,omitempty"`
	TrafficLimit     int64         `json:"traffic_limit,omitempty"`
	TrafficUsed      int64         `json:"traffic_used,omitempty"`
	TrafficPeriod    string        `json:"traffic_period,omitempty"`
	LastError        string        `json:"last_error,omitempty"`
	LastFailedAt     string        `json:"last_failed_at,omitempty"`
	ParseErrors      []ParseError  `json:"parse_errors,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

type ParseError struct {
	Time    string `json:"time"`
	JobID   string `json:"job_id,omitempty"`
	Message string `json:"message"`
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
	cursor   uint64 // rotating round-robin starting point for parallel resolves
}

const premiumRefreshInterval = 30 * time.Minute

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
	client := p.newClient(username, password, sessionFile)
	if err := client.Login(ctx, username, password); err != nil {
		return AccountSummary{}, err
	}
	premiumInfo, premiumErr := client.GetVIPInfo(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	existingState, existed := p.accounts[id]
	createdAt := now
	if existed {
		createdAt = existingState.record.CreatedAt
	}

	record := accountRecord{
		ID:            id,
		Username:      username,
		Password:      password,
		SessionFile:   sessionFile,
		Status:        AccountAvailable,
		TrafficLimit:  trafficLimit,
		TrafficPeriod: monthKey(now),
		CreatedAt:     createdAt,
		UpdatedAt:     now,
	}
	// Re-adding an existing account keeps its accrued usage for the month.
	if existed {
		record.TrafficUsed = existingState.record.TrafficUsed
		record.ParseErrors = append([]ParseError(nil), existingState.record.ParseErrors...)
		if existingState.record.TrafficPeriod != "" {
			record.TrafficPeriod = existingState.record.TrafficPeriod
		}
	}
	updatePremiumRecord(&record, premiumInfo, premiumErr, now)

	p.accounts[id] = &accountState{
		record: record,
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
			ID:            id,
			Username:      username,
			Password:      password,
			SessionFile:   sessionFile,
			Status:        AccountAvailable,
			TrafficLimit:  defaultAccountTraffic,
			TrafficPeriod: monthKey(now),
			CreatedAt:     now,
			UpdatedAt:     now,
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

func (p *AccountPool) RefreshPremiumInfo(ctx context.Context) {
	now := time.Now()

	type target struct {
		id     string
		client *pikpak.Client
	}

	p.mu.RLock()
	targets := make([]target, 0, len(p.order))
	for _, id := range p.order {
		state := p.accounts[id]
		if state == nil || state.client == nil {
			continue
		}
		if premiumInfoNeedsRefresh(state.record, now) {
			targets = append(targets, target{id: id, client: state.client})
		}
	}
	p.mu.RUnlock()

	for _, item := range targets {
		if ctx.Err() != nil {
			return
		}

		info, err := item.client.GetVIPInfo(ctx)

		p.mu.Lock()
		state := p.accounts[item.id]
		if state != nil {
			updatePremiumRecord(&state.record, info, err, time.Now())
			state.record.UpdatedAt = time.Now()
			_ = p.saveLocked()
		}
		p.mu.Unlock()
	}
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
	state.record.LastError = friendlyPikPakError(err)
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

func (p *AccountPool) RecordParseError(id, jobID, message string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state := p.accounts[id]
	if state == nil {
		return
	}
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
	_ = p.saveLocked()
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
	state.record.TrafficLimit = limitBytes
	state.record.UpdatedAt = time.Now()
	return p.saveLocked()
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
	now := time.Now()
	mk := monthKey(now)
	if state.record.TrafficPeriod != mk {
		state.record.TrafficUsed = 0
		state.record.TrafficPeriod = mk
	}
	state.record.TrafficUsed += bytes
	state.record.UpdatedAt = now
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
	now := time.Now()
	return AccountSummary{
		ID:               state.record.ID,
		Username:         state.record.Username,
		Status:           state.record.Status,
		Ready:            status.Ready,
		LoggedIn:         status.LoggedIn,
		Persisted:        status.Persisted,
		Premium:          state.record.Premium,
		PremiumType:      state.record.PremiumType,
		PremiumUntil:     state.record.PremiumUntil,
		PremiumError:     friendlyPikPakMessage(state.record.PremiumError),
		PremiumCheckedAt: state.record.PremiumCheckedAt,
		TrafficLimit:     state.record.TrafficLimit,
		TrafficUsed:      effectiveTrafficUsed(state.record, now),
		TrafficLimited:   accountTrafficLimited(state.record, now),
		LastError:        friendlyPikPakMessage(state.record.LastError),
		LastFailedAt:     state.record.LastFailedAt,
		ParseErrorCount:  len(state.record.ParseErrors),
		ParseErrors:      append([]ParseError(nil), state.record.ParseErrors...),
		CreatedAt:        state.record.CreatedAt,
		UpdatedAt:        state.record.UpdatedAt,
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

// monthKey is the calendar-month identifier used to scope traffic counters.
func monthKey(t time.Time) string {
	return t.Format("2006-01")
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
	return strings.Contains(lower, "record not found")
}

func isBadResourceUserError(message string) bool {
	return strings.TrimSpace(message) == badResourceParseUserError
}
