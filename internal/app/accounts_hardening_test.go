package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pikpak2directlink/internal/pikpak"
)

type countingAccountReplacer struct {
	mu      sync.Mutex
	calls   int
	records []accountRecord
	err     error
}

func (s *countingAccountReplacer) Replace(records []accountRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return s.err
	}
	s.records = cloneAccountRecords(records)
	return nil
}

func (s *countingAccountReplacer) snapshot() (int, []accountRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, cloneAccountRecords(s.records)
}

func cloneAccountRecords(records []accountRecord) []accountRecord {
	cloned := make([]accountRecord, len(records))
	copy(cloned, records)
	for i := range cloned {
		cloned[i].ParseErrors = append([]ParseError(nil), records[i].ParseErrors...)
	}
	return cloned
}

type premiumRefreshTracker struct {
	mu      sync.Mutex
	active  int
	max     int
	calls   int
	started chan struct{}
	release <-chan struct{}
}

func (t *premiumRefreshTracker) fetch(ctx context.Context) (*pikpak.VIPInfo, error) {
	t.mu.Lock()
	t.active++
	t.calls++
	if t.active > t.max {
		t.max = t.active
	}
	t.mu.Unlock()

	t.started <- struct{}{}
	select {
	case <-t.release:
	case <-ctx.Done():
		t.mu.Lock()
		t.active--
		t.mu.Unlock()
		return nil, ctx.Err()
	}

	t.mu.Lock()
	t.active--
	t.mu.Unlock()
	return &pikpak.VIPInfo{Data: pikpak.VIPInfoData{Status: "active", Type: "vip", Expire: "2030-01-01"}}, nil
}

func (t *premiumRefreshTracker) stats() (calls, max int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls, t.max
}

type premiumRefreshClient struct {
	pikpakClient
	get func(context.Context) (*pikpak.VIPInfo, error)
}

func (c *premiumRefreshClient) GetVIPInfo(ctx context.Context) (*pikpak.VIPInfo, error) {
	return c.get(ctx)
}

func (c *premiumRefreshClient) Status() pikpak.SessionStatus {
	return pikpak.SessionStatus{Ready: true, LoggedIn: true}
}

func TestRefreshPremiumInfoBatchesPersistenceAndLimitsConcurrency(t *testing.T) {
	const accountCount = 9
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	tracker := &premiumRefreshTracker{
		started: make(chan struct{}, accountCount),
		release: release,
	}
	replacer := &countingAccountReplacer{}
	pool := &AccountPool{
		accounts:       make(map[string]*accountState, accountCount),
		recordReplacer: replacer,
	}
	for i := range accountCount {
		id := "account-" + string(rune('a'+i))
		pool.order = append(pool.order, id)
		pool.accounts[id] = &accountState{
			record: accountRecord{ID: id, Username: id + "@example.com", Status: AccountAvailable},
			client: &premiumRefreshClient{get: tracker.fetch},
		}
	}

	done := make(chan error, 1)
	go func() { done <- pool.RefreshPremiumInfo(context.Background()) }()
	for range premiumRefreshConcurrency {
		select {
		case <-tracker.started:
		case <-time.After(time.Second):
			t.Fatal("premium refresh did not start four workers")
		}
	}
	select {
	case <-tracker.started:
		t.Fatal("premium refresh exceeded the four-request concurrency limit")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	released = true
	if err := <-done; err != nil {
		t.Fatalf("RefreshPremiumInfo: %v", err)
	}

	calls, maxConcurrent := tracker.stats()
	if calls != accountCount || maxConcurrent != premiumRefreshConcurrency {
		t.Fatalf("VIP calls/max concurrency = %d/%d, want %d/%d", calls, maxConcurrent, accountCount, premiumRefreshConcurrency)
	}
	replaceCalls, records := replacer.snapshot()
	if replaceCalls != 1 {
		t.Fatalf("Replace calls = %d, want 1", replaceCalls)
	}
	if len(records) != accountCount {
		t.Fatalf("persisted records = %d, want %d", len(records), accountCount)
	}
	for _, record := range records {
		if !record.Premium || record.PremiumType != "vip" || record.PremiumCheckedAt == "" {
			t.Fatalf("premium result was not applied: %+v", record)
		}
	}
}

func TestRefreshPremiumInfoDiscardsResultFromReplacedClient(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	oldClient := &premiumRefreshClient{get: func(ctx context.Context) (*pikpak.VIPInfo, error) {
		close(started)
		select {
		case <-release:
			return &pikpak.VIPInfo{Data: pikpak.VIPInfoData{Status: "active", Type: "stale-vip"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	newClient := &premiumRefreshClient{get: func(context.Context) (*pikpak.VIPInfo, error) {
		return &pikpak.VIPInfo{Data: pikpak.VIPInfoData{Status: "active", Type: "current-vip"}}, nil
	}}
	replacer := &countingAccountReplacer{}
	state := &accountState{
		record: accountRecord{ID: "account", Username: "account@example.com", Status: AccountAvailable},
		client: oldClient,
	}
	pool := &AccountPool{
		accounts:       map[string]*accountState{"account": state},
		order:          []string{"account"},
		recordReplacer: replacer,
	}

	done := make(chan error, 1)
	go func() { done <- pool.RefreshPremiumInfo(context.Background()) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("premium refresh did not start")
	}

	pool.mu.Lock()
	state.client = newClient
	pool.mu.Unlock()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RefreshPremiumInfo: %v", err)
	}

	if state.record.Premium || state.record.PremiumType != "" || state.record.PremiumCheckedAt != "" {
		t.Fatalf("stale premium result was applied: %+v", state.record)
	}
	if calls, _ := replacer.snapshot(); calls != 0 {
		t.Fatalf("Replace calls = %d, want 0 for a stale-only result", calls)
	}
}

func TestAccountStateMutationsReturnPersistenceErrorsAndRollBack(t *testing.T) {
	persistErr := errors.New("forced account persistence failure")
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name   string
		status AccountStatus
		apply  func(*AccountPool) error
	}{
		{
			name:   "credential success",
			status: AccountFailed,
			apply: func(pool *AccountPool) error {
				return pool.MarkCredentialCheckSuccess("account", now, now.Add(time.Hour), nil)
			},
		},
		{
			name:   "credential failure",
			status: AccountAvailable,
			apply: func(pool *AccountPool) error {
				return pool.MarkCredentialCheckFailed("account", errors.New("probe failed"), now, now.Add(time.Hour))
			},
		},
		{
			name:   "mark failed",
			status: AccountAvailable,
			apply: func(pool *AccountPool) error {
				return pool.MarkFailed("account", errors.New("request failed"))
			},
		},
		{
			name:   "mark available",
			status: AccountFailed,
			apply: func(pool *AccountPool) error {
				return pool.MarkAvailable("account")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replacer := &countingAccountReplacer{err: persistErr}
			before := accountRecord{
				ID:           "account",
				Username:     "account@example.com",
				Status:       tt.status,
				LastError:    "before",
				LastFailedAt: now.Add(-time.Hour).Format(time.RFC3339),
				UpdatedAt:    now.Add(-time.Hour),
			}
			pool := &AccountPool{
				accounts: map[string]*accountState{
					"account": {record: before, client: &premiumRefreshClient{}},
				},
				order:          []string{"account"},
				recordReplacer: replacer,
			}
			if err := tt.apply(pool); !errors.Is(err, persistErr) {
				t.Fatalf("error = %v, want %v", err, persistErr)
			}
			if calls, _ := replacer.snapshot(); calls != 1 {
				t.Fatalf("Replace calls = %d, want 1", calls)
			}
			if after := pool.accounts["account"].record; !reflect.DeepEqual(after, before) {
				t.Fatalf("failed persistence changed memory:\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

func TestRefreshPremiumInfoReturnsPersistenceErrorAndRollsBack(t *testing.T) {
	persistErr := errors.New("forced premium persistence failure")
	replacer := &countingAccountReplacer{err: persistErr}
	before := accountRecord{ID: "account", Username: "account@example.com", Status: AccountAvailable}
	pool := &AccountPool{
		accounts: map[string]*accountState{
			"account": {
				record: before,
				client: &premiumRefreshClient{get: func(context.Context) (*pikpak.VIPInfo, error) {
					return &pikpak.VIPInfo{Data: pikpak.VIPInfoData{Status: "active", Type: "vip"}}, nil
				}},
			},
		},
		order:          []string{"account"},
		recordReplacer: replacer,
	}

	if err := pool.RefreshPremiumInfo(context.Background()); !errors.Is(err, persistErr) {
		t.Fatalf("error = %v, want %v", err, persistErr)
	}
	if calls, _ := replacer.snapshot(); calls != 1 {
		t.Fatalf("Replace calls = %d, want 1", calls)
	}
	if after := pool.accounts["account"].record; !reflect.DeepEqual(after, before) {
		t.Fatalf("failed premium persistence changed memory:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func TestAccountHealthPersistenceFailureLogsAndRequestsRestart(t *testing.T) {
	persistErr := errors.New("forced health persistence failure")
	account := AccountRuntime{ID: "account", Username: "account@example.com"}
	pool := &AccountPool{
		accounts: map[string]*accountState{
			account.ID: {record: accountRecord{ID: account.ID, Username: account.Username, Status: AccountFailed}},
		},
		order:          []string{account.ID},
		recordReplacer: &countingAccountReplacer{err: persistErr},
	}
	srv := &Server{
		accounts:  pool,
		logs:      newLogStore(10),
		restartCh: make(chan struct{}),
		nowFunc:   func() time.Time { return time.Unix(1_700_000_000, 0) },
		accountHealthProbe: func(context.Context, AccountRuntime) (accountCredentialProbeResult, error) {
			return accountCredentialProbeResult{}, nil
		},
	}

	srv.runAccountCredentialCheck(context.Background(), account)
	select {
	case <-srv.RestartRequested():
	default:
		t.Fatal("persistence failure did not request a restart")
	}
	entries := srv.logs.list(0)
	if len(entries) != 1 || entries[0].Level != LogError || !strings.Contains(entries[0].Message, "persistence failed") {
		t.Fatalf("persistence log = %+v", entries)
	}
	if details := strings.Join(entries[0].Details, " "); !strings.Contains(details, persistErr.Error()) || !strings.Contains(details, account.Username) {
		t.Fatalf("persistence log details = %q", details)
	}
}
