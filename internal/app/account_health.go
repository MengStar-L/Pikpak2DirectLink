package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"pikpak2directlink/internal/pikpak"
)

const (
	defaultAccountHealthCheckURL      = "https://mypikpak.com/s/VOveL7ZI01ViAz9VVKGgSWDlo2"
	defaultAccountHealthCheckInterval = 6 * time.Hour
	defaultAccountAutoRefreshGap      = 30 * time.Minute
	settingKeyLastAutoAccountRefresh  = "last_auto_account_refresh_unix"
)

type accountCredentialProbeResult struct {
	RestoredIDs []string
	CleanupErr  error
}

type accountHealthProbeFunc func(context.Context, AccountRuntime) (accountCredentialProbeResult, error)
type accountRefreshLoginFunc func(context.Context, string) (AccountSummary, error)

type accountHealthClient interface {
	GetShareInfo(ctx context.Context, shareID, passCode, parentID string) (*pikpak.ShareListResponse, error)
	RestoreShare(ctx context.Context, shareID, passCodeToken string, fileIDs []string) (*pikpak.RestoreShareResponse, error)
	DeleteFiles(ctx context.Context, ids []string) error
}

func probeAccountCredentialByTransfer(ctx context.Context, healthURL string, client accountHealthClient) (accountCredentialProbeResult, error) {
	if client == nil {
		return accountCredentialProbeResult{}, errors.New("PikPak client is missing")
	}
	share, passCode, err := shareStateAndPassCode(healthURL, "")
	if err != nil {
		return accountCredentialProbeResult{}, err
	}

	shareInfo, err := client.GetShareInfo(ctx, share.ShareID, passCode, "")
	if err != nil {
		return accountCredentialProbeResult{}, err
	}
	testID := firstShareFileID(shareInfo)
	if testID == "" {
		return accountCredentialProbeResult{}, errors.New("health check share did not return any file")
	}

	restoreResp, err := client.RestoreShare(ctx, share.ShareID, shareInfo.PassCodeToken, []string{testID})
	if err != nil {
		return accountCredentialProbeResult{}, err
	}
	restoredIDs := restoreFileIDs(restoreResp)
	if len(restoredIDs) == 0 {
		return accountCredentialProbeResult{}, errors.New("health check restore did not return any file id")
	}

	result := accountCredentialProbeResult{RestoredIDs: restoredIDs}
	if err := client.DeleteFiles(ctx, restoredIDs); err != nil {
		result.CleanupErr = err
	}
	return result, nil
}

func firstShareFileID(resp *pikpak.ShareListResponse) string {
	if resp == nil {
		return ""
	}
	for _, file := range resp.Files {
		if id := strings.TrimSpace(file.ID); id != "" {
			return id
		}
	}
	return ""
}

func (s *Server) startAccountHealthMonitor() {
	if s.accounts == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.healthCancel = cancel
	s.healthDone = make(chan struct{})
	go func() {
		defer close(s.healthDone)
		s.runAccountHealthMonitor(ctx)
	}()
}

func (s *Server) runAccountHealthMonitor(ctx context.Context) {
	for {
		now := s.now()
		target, ok := s.accounts.NextCredentialCheck(now)
		if !ok {
			if !sleepContext(ctx, time.Minute) {
				return
			}
			continue
		}
		if target.NextAt.After(now) {
			delay := target.NextAt.Sub(now)
			if delay > time.Minute {
				delay = time.Minute
			}
			if !sleepContext(ctx, delay) {
				return
			}
			continue
		}
		s.runAccountCredentialCheck(ctx, target.Account)
	}
}

func (s *Server) runAccountCredentialCheck(ctx context.Context, account AccountRuntime) {
	if account.ID == "" {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.accountHealthTimeout())
	result, err := s.accountHealthProbeFunc()(probeCtx, account)
	cancel()
	if ctx.Err() != nil {
		return
	}

	checkedAt := s.now()
	if err == nil {
		s.accounts.MarkCredentialCheckSuccess(account.ID, checkedAt, checkedAt.Add(s.accountHealthInterval()), result.CleanupErr)
		if result.CleanupErr != nil {
			s.logJob(LogWarn, "", "PikPak account health check cleanup failed", "account: "+account.Username, result.CleanupErr.Error())
		} else {
			s.logJob(LogSuccess, "", "PikPak account health check passed", "account: "+account.Username)
		}
		return
	}

	s.logJob(LogWarn, "", "PikPak account health check failed", "account: "+account.Username, friendlyPikPakError(err))
	s.handleAccountHealthFailure(ctx, account, err)
}

func (s *Server) handleAccountHealthFailure(ctx context.Context, account AccountRuntime, probeErr error) {
	now := s.now()
	allowed, allowedAt, reserveErr := s.reserveAutoAccountRefresh(now)
	if reserveErr != nil {
		s.accounts.MarkCredentialCheckFailed(account.ID, reserveErr, now, now.Add(s.accountAutoRefreshGap()))
		s.logJob(LogError, "", "PikPak account auto refresh could not reserve refresh slot", "account: "+account.Username, reserveErr.Error())
		return
	}
	if !allowed {
		s.accounts.MarkCredentialCheckFailed(account.ID, probeErr, now, allowedAt)
		s.logJob(LogWarn, "", "PikPak account auto refresh delayed", "account: "+account.Username, "next: "+allowedAt.Format(time.RFC3339))
		return
	}

	refreshCtx, cancelRefresh := context.WithTimeout(ctx, s.accountHealthTimeout())
	_, refreshErr := s.accountHealthRefreshFunc()(refreshCtx, account.ID)
	cancelRefresh()
	if ctx.Err() != nil {
		return
	}
	if refreshErr != nil {
		checkedAt := s.now()
		s.accounts.MarkCredentialCheckFailed(account.ID, refreshErr, checkedAt, checkedAt.Add(s.accountHealthInterval()))
		s.logJob(LogError, "", "PikPak account auto refresh failed", "account: "+account.Username, friendlyPikPakError(refreshErr))
		return
	}

	refreshed, ok := s.accounts.Get(account.ID)
	if !ok {
		return
	}
	recheckCtx, cancelRecheck := context.WithTimeout(ctx, s.accountHealthTimeout())
	result, recheckErr := s.accountHealthProbeFunc()(recheckCtx, refreshed)
	cancelRecheck()
	if ctx.Err() != nil {
		return
	}

	checkedAt := s.now()
	if recheckErr != nil {
		s.accounts.MarkCredentialCheckFailed(account.ID, recheckErr, checkedAt, checkedAt.Add(s.accountHealthInterval()))
		s.logJob(LogError, "", "PikPak account health check still failed after auto refresh", "account: "+account.Username, friendlyPikPakError(recheckErr))
		return
	}

	s.accounts.MarkCredentialCheckSuccess(account.ID, checkedAt, checkedAt.Add(s.accountHealthInterval()), result.CleanupErr)
	if result.CleanupErr != nil {
		s.logJob(LogWarn, "", "PikPak account health check cleanup failed after auto refresh", "account: "+account.Username, result.CleanupErr.Error())
	}
	s.logJob(LogSuccess, "", "PikPak account auto refresh restored availability", "account: "+account.Username)
}

func (s *Server) reserveAutoAccountRefresh(now time.Time) (bool, time.Time, error) {
	gap := s.accountAutoRefreshGap()
	if gap <= 0 || s.settings == nil {
		return true, now, nil
	}
	lastUnix := s.settings.getInt64(settingKeyLastAutoAccountRefresh, 0)
	if lastUnix > 0 {
		allowedAt := time.Unix(lastUnix, 0).Add(gap)
		if now.Before(allowedAt) {
			return false, allowedAt, nil
		}
	}
	if err := s.settings.setInt64(settingKeyLastAutoAccountRefresh, now.Unix()); err != nil {
		return false, now.Add(gap), err
	}
	return true, now, nil
}

func (s *Server) accountHealthProbeFunc() accountHealthProbeFunc {
	if s.accountHealthProbe != nil {
		return s.accountHealthProbe
	}
	return func(ctx context.Context, account AccountRuntime) (accountCredentialProbeResult, error) {
		if account.Client == nil {
			return accountCredentialProbeResult{}, fmt.Errorf("PikPak client is missing")
		}
		return probeAccountCredentialByTransfer(ctx, s.config.AccountHealthURL, account.Client)
	}
}

func (s *Server) accountHealthRefreshFunc() accountRefreshLoginFunc {
	if s.accountHealthRefresh != nil {
		return s.accountHealthRefresh
	}
	return s.accounts.RefreshLogin
}

func (s *Server) accountHealthInterval() time.Duration {
	if s.config.AccountHealthEvery > 0 {
		return s.config.AccountHealthEvery
	}
	return defaultAccountHealthCheckInterval
}

func (s *Server) accountAutoRefreshGap() time.Duration {
	if s.config.AccountRefreshGap > 0 {
		return s.config.AccountRefreshGap
	}
	return defaultAccountAutoRefreshGap
}

func (s *Server) accountHealthTimeout() time.Duration {
	if s.config.AccountHealthTimeout > 0 {
		return s.config.AccountHealthTimeout
	}
	requestTimeout := s.config.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 20 * time.Second
	}
	return maxDuration(requestTimeout*3, time.Minute)
}

func (s *Server) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		delay = time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
