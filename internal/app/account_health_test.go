package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"pikpak2directlink/internal/pikpak"
)

type fakeHealthClient struct {
	calls       []string
	shareResp   *pikpak.ShareListResponse
	shareErr    error
	restoreResp *pikpak.RestoreShareResponse
	restoreErr  error
	deleteErr   error
	deletedIDs  []string
}

func (f *fakeHealthClient) GetShareInfo(context.Context, string, string, string) (*pikpak.ShareListResponse, error) {
	f.calls = append(f.calls, "share")
	return f.shareResp, f.shareErr
}

func (f *fakeHealthClient) RestoreShare(_ context.Context, _ string, _ string, fileIDs []string) (*pikpak.RestoreShareResponse, error) {
	f.calls = append(f.calls, "restore:"+joinTestIDs(fileIDs))
	return f.restoreResp, f.restoreErr
}

func (f *fakeHealthClient) DeleteFiles(_ context.Context, ids []string) error {
	f.calls = append(f.calls, "delete:"+joinTestIDs(ids))
	f.deletedIDs = append([]string(nil), ids...)
	return f.deleteErr
}

func joinTestIDs(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += id
	}
	return out
}

func TestProbeAccountCredentialByTransferCleansRestoredFile(t *testing.T) {
	t.Parallel()

	client := &fakeHealthClient{
		shareResp: &pikpak.ShareListResponse{
			PassCodeToken: "pass-token",
			Files:         []pikpak.FileEntry{{ID: "share-file"}},
		},
		restoreResp: &pikpak.RestoreShareResponse{FileID: "restored-file"},
	}

	result, err := probeAccountCredentialByTransfer(context.Background(), defaultAccountHealthCheckURL, client)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if result.CleanupErr != nil {
		t.Fatalf("cleanup err = %v", result.CleanupErr)
	}
	if !reflect.DeepEqual(result.RestoredIDs, []string{"restored-file"}) {
		t.Fatalf("restored IDs = %v", result.RestoredIDs)
	}
	if !reflect.DeepEqual(client.calls, []string{"share", "restore:share-file", "delete:restored-file"}) {
		t.Fatalf("calls = %v", client.calls)
	}
}

func TestProbeAccountCredentialByTransferTreatsCleanupFailureAsWarning(t *testing.T) {
	t.Parallel()

	cleanupErr := errors.New("delete failed")
	client := &fakeHealthClient{
		shareResp: &pikpak.ShareListResponse{
			PassCodeToken: "pass-token",
			Files:         []pikpak.FileEntry{{ID: "share-file"}},
		},
		restoreResp: &pikpak.RestoreShareResponse{TaskInfo: []pikpak.RestoreTaskInfo{{FileID: "restored-file"}}},
		deleteErr:   cleanupErr,
	}

	result, err := probeAccountCredentialByTransfer(context.Background(), defaultAccountHealthCheckURL, client)
	if err != nil {
		t.Fatalf("probe should still succeed when cleanup fails: %v", err)
	}
	if !errors.Is(result.CleanupErr, cleanupErr) {
		t.Fatalf("cleanup err = %v, want %v", result.CleanupErr, cleanupErr)
	}
}

func newAccountHealthTestServer(t *testing.T, now time.Time) *Server {
	t.Helper()
	db, err := openDatabase(":memory:")
	if err != nil {
		t.Fatalf("openDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	dir := t.TempDir()
	pool, err := NewAccountPool(AccountPoolConfig{
		AccountsFile:   filepath.Join(dir, "accounts.json"),
		SessionDir:     filepath.Join(dir, "sessions"),
		RootFolderName: "Pikpak2DirectLink",
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}

	return &Server{
		config: Config{
			AccountHealthURL:     defaultAccountHealthCheckURL,
			AccountHealthEvery:   6 * time.Hour,
			AccountRefreshGap:    30 * time.Minute,
			AccountHealthTimeout: time.Second,
		},
		accounts: pool,
		settings: newSettingsStore(db),
		logs:     newLogStore(50),
		nowFunc:  func() time.Time { return now },
	}
}

func TestAccountCredentialCheckSuccessUpdatesSchedule(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	s := newAccountHealthTestServer(t, now)
	s.accounts.injectAccount(accountRecord{ID: "a", Username: "a@example.com", Status: AccountFailed, CredentialNextCheckAt: now.Format(time.RFC3339)})
	s.accountHealthProbe = func(context.Context, AccountRuntime) (accountCredentialProbeResult, error) {
		return accountCredentialProbeResult{RestoredIDs: []string{"test-file"}}, nil
	}

	s.runAccountCredentialCheck(context.Background(), AccountRuntime{ID: "a", Username: "a@example.com"})

	got := s.accounts.List()[0]
	if got.Status != AccountAvailable {
		t.Fatalf("status = %q, want available", got.Status)
	}
	if got.CredentialCheckedAt != now.Format(time.RFC3339) {
		t.Fatalf("checked_at = %q, want %q", got.CredentialCheckedAt, now.Format(time.RFC3339))
	}
	if got.CredentialNextCheckAt != now.Add(6*time.Hour).Format(time.RFC3339) {
		t.Fatalf("next_check_at = %q", got.CredentialNextCheckAt)
	}
}

func TestAccountCredentialCheckCleanupFailureKeepsAccountAvailable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	s := newAccountHealthTestServer(t, now)
	s.accounts.injectAccount(accountRecord{ID: "a", Username: "a@example.com", Status: AccountFailed, CredentialNextCheckAt: now.Format(time.RFC3339)})
	s.accountHealthProbe = func(context.Context, AccountRuntime) (accountCredentialProbeResult, error) {
		return accountCredentialProbeResult{RestoredIDs: []string{"test-file"}, CleanupErr: errors.New("cleanup failed")}, nil
	}

	s.runAccountCredentialCheck(context.Background(), AccountRuntime{ID: "a", Username: "a@example.com"})

	got := s.accounts.List()[0]
	if got.Status != AccountAvailable {
		t.Fatalf("status = %q, want available", got.Status)
	}
	if got.CredentialCheckError == "" {
		t.Fatal("expected cleanup warning in credential_check_error")
	}
}

func TestAccountCredentialCheckFailureAutoRefreshesThenRechecks(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	s := newAccountHealthTestServer(t, now)
	s.accounts.injectAccount(accountRecord{ID: "a", Username: "a@example.com", Status: AccountAvailable, CredentialNextCheckAt: now.Format(time.RFC3339)})

	probeCalls := 0
	refreshCalls := 0
	s.accountHealthProbe = func(context.Context, AccountRuntime) (accountCredentialProbeResult, error) {
		probeCalls++
		if probeCalls == 1 {
			return accountCredentialProbeResult{}, errors.New("restore failed")
		}
		return accountCredentialProbeResult{RestoredIDs: []string{"test-file"}}, nil
	}
	s.accountHealthRefresh = func(context.Context, string) (AccountSummary, error) {
		refreshCalls++
		return AccountSummary{ID: "a"}, nil
	}

	s.runAccountCredentialCheck(context.Background(), AccountRuntime{ID: "a", Username: "a@example.com"})

	if probeCalls != 2 || refreshCalls != 1 {
		t.Fatalf("probeCalls=%d refreshCalls=%d, want 2/1", probeCalls, refreshCalls)
	}
	got := s.accounts.List()[0]
	if got.Status != AccountAvailable {
		t.Fatalf("status = %q, want available", got.Status)
	}
	if last := s.settings.getInt64(settingKeyLastAutoAccountRefresh, 0); last != now.Unix() {
		t.Fatalf("last auto refresh unix = %d, want %d", last, now.Unix())
	}
}

func TestAccountCredentialCheckAutoRefreshIsThrottled(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	s := newAccountHealthTestServer(t, now)
	s.accounts.injectAccount(accountRecord{ID: "a", Username: "a@example.com", Status: AccountAvailable, CredentialNextCheckAt: now.Format(time.RFC3339)})
	if err := s.settings.setInt64(settingKeyLastAutoAccountRefresh, now.Add(-10*time.Minute).Unix()); err != nil {
		t.Fatalf("set last refresh: %v", err)
	}

	refreshCalls := 0
	s.accountHealthProbe = func(context.Context, AccountRuntime) (accountCredentialProbeResult, error) {
		return accountCredentialProbeResult{}, errors.New("restore failed")
	}
	s.accountHealthRefresh = func(context.Context, string) (AccountSummary, error) {
		refreshCalls++
		return AccountSummary{ID: "a"}, nil
	}

	s.runAccountCredentialCheck(context.Background(), AccountRuntime{ID: "a", Username: "a@example.com"})

	if refreshCalls != 0 {
		t.Fatalf("refreshCalls = %d, want 0", refreshCalls)
	}
	got := s.accounts.List()[0]
	wantNext := now.Add(20 * time.Minute).Format(time.RFC3339)
	if got.Status != AccountFailed || got.CredentialNextCheckAt != wantNext {
		t.Fatalf("status/next = %q/%q, want failed/%q", got.Status, got.CredentialNextCheckAt, wantNext)
	}
}
