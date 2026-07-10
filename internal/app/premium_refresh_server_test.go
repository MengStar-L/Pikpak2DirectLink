package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"pikpak2directlink/internal/pikpak"
)

type blockingVIPClient struct {
	*fakePikPakClient
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (c *blockingVIPClient) GetVIPInfo(ctx context.Context) (*pikpak.VIPInfo, error) {
	if c.calls.Add(1) == 1 {
		close(c.started)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.release:
		return &pikpak.VIPInfo{}, nil
	}
}

func TestListAccountsReturnsCachedDataWhilePremiumRefreshRuns(t *testing.T) {
	client := &blockingVIPClient{
		fakePikPakClient: &fakePikPakClient{},
		started:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	now := time.Now().UTC()
	pool := &AccountPool{
		config: AccountPoolConfig{AccountsFile: filepath.Join(t.TempDir(), "accounts.json")},
		accounts: map[string]*accountState{
			"account": {
				record: accountRecord{ID: "account", Username: "account@example.com", CreatedAt: now, UpdatedAt: now},
				client: client,
			},
		},
		order: []string{"account"},
	}
	s := &Server{
		accounts:  pool,
		config:    Config{RequestTimeout: time.Second},
		logs:      newLogStore(10),
		restartCh: make(chan struct{}),
	}

	recorder := httptest.NewRecorder()
	startedAt := time.Now()
	s.handleListAccounts(recorder, httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list accounts status = %d", recorder.Code)
	}
	if elapsed := time.Since(startedAt); elapsed > 200*time.Millisecond {
		t.Fatalf("cached account response waited %s for premium refresh", elapsed)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("premium refresh did not start")
	}

	// A second list request must coalesce with the running refresh.
	s.handleListAccounts(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	time.Sleep(20 * time.Millisecond)
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("coalesced premium refresh calls = %d, want 1", got)
	}
	close(client.release)
	deadline := time.Now().Add(time.Second)
	for {
		s.premiumRefreshMu.Lock()
		active := s.premiumRefreshActive
		s.premiumRefreshMu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("premium refresh did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close server: %v", err)
	}
}
