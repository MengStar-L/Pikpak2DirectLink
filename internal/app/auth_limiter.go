package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	authIPFailureLimit           = 20
	authIPFailureWindow          = 5 * time.Minute
	authIdentityFailureLimit     = 10
	authIdentityFailureWindow    = 15 * time.Minute
	defaultAuthLimiterMaxEntries = 8192
	authLimiterSweepInterval     = time.Minute
	authInFlightRetryAfter       = time.Second
	passwordHashRetryAfter       = time.Second
)

var errPasswordHashBusy = errors.New("password hashing is at capacity")

type authLimitRule struct {
	limit  int
	window time.Duration
}

type authFailureWindow struct {
	rule     authLimitRule
	failures []time.Time
	inFlight int
	lastSeen time.Time
}

type authAttempt struct {
	ipKey       string
	identityKey string
}

type authAdmission struct {
	limiter *authLimiter
	attempt authAttempt
	active  bool
}

// authLimiter bounds authentication work across the admin login, user login,
// and email registration surfaces. Callers should share one instance per Server.
type authLimiter struct {
	mu         sync.Mutex
	windows    map[string]*authFailureWindow
	maxEntries int
	nextSweep  time.Time
}

func newAuthLimiter() *authLimiter {
	return newAuthLimiterWithMaxEntries(defaultAuthLimiterMaxEntries)
}

func newAuthLimiterWithMaxEntries(maxEntries int) *authLimiter {
	if maxEntries < 2 {
		maxEntries = 2
	}
	return &authLimiter{
		windows:    make(map[string]*authFailureWindow),
		maxEntries: maxEntries,
	}
}

func authAttemptForRequest(r *http.Request, identity string) authAttempt {
	ip := "unknown"
	if r != nil {
		ip = authRequestIP(r)
	}
	identity = strings.ToLower(strings.TrimSpace(identity))
	digest := sha256.Sum256([]byte(identity))
	return authAttempt{
		ipKey:       "ip:" + ip,
		identityKey: "identity:" + hex.EncodeToString(digest[:]),
	}
}

// authRequestIP trusts X-Forwarded-For only when the direct peer is loopback.
// Walking from right to left selects the first hop outside that trusted local
// proxy boundary and prevents a public peer from spoofing its source address.
func authRequestIP(r *http.Request) string {
	direct := remoteAddressIP(r.RemoteAddr)
	directIP := net.ParseIP(strings.Trim(direct, "[]"))
	if directIP == nil || !directIP.IsLoopback() {
		return direct
	}

	values := r.Header.Values("X-Forwarded-For")
	for valueIndex := len(values) - 1; valueIndex >= 0; valueIndex-- {
		parts := strings.Split(values[valueIndex], ",")
		for partIndex := len(parts) - 1; partIndex >= 0; partIndex-- {
			candidate := forwardedForIP(parts[partIndex])
			if candidate != nil && !candidate.IsLoopback() {
				return candidate.String()
			}
		}
	}
	return direct
}

func forwardedForIP(value string) net.IP {
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	return net.ParseIP(strings.Trim(value, "[]"))
}

func remoteAddressIP(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		if parsed := net.ParseIP(strings.Trim(host, "[]")); parsed != nil {
			return parsed.String()
		}
		return strings.ToLower(host)
	}
	if parsed := net.ParseIP(strings.Trim(remoteAddr, "[]")); parsed != nil {
		return parsed.String()
	}
	if remoteAddr == "" {
		return "unknown"
	}
	return strings.ToLower(remoteAddr)
}

// admit atomically checks both rate-limit windows and reserves one in-flight
// attempt in each. The reservation must be completed or cancelled by callers.
func (l *authLimiter) admit(attempt authAttempt, now time.Time) (*authAdmission, time.Duration, bool) {
	if l == nil {
		return nil, 0, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now, false)

	retry := l.windowRetryAfterLocked(attempt.ipKey, now)
	if identityRetry := l.windowRetryAfterLocked(attempt.identityKey, now); identityRetry > retry {
		retry = identityRetry
	}
	if retry > 0 {
		return nil, retry, false
	}

	ipEntry, ok := l.ensureWindowLocked(attempt.ipKey, authLimitRule{limit: authIPFailureLimit, window: authIPFailureWindow}, now)
	if !ok {
		return nil, authInFlightRetryAfter, false
	}
	ipEntry.inFlight++
	identityEntry, ok := l.ensureWindowLocked(attempt.identityKey, authLimitRule{limit: authIdentityFailureLimit, window: authIdentityFailureWindow}, now)
	if !ok {
		ipEntry.inFlight--
		l.deleteWindowIfIdleLocked(attempt.ipKey, ipEntry)
		return nil, authInFlightRetryAfter, false
	}
	identityEntry.inFlight++

	return &authAdmission{limiter: l, attempt: attempt, active: true}, 0, true
}

func (a *authAdmission) cancel(now time.Time) {
	a.complete(now, false, false)
}

func (a *authAdmission) fail(now time.Time) {
	a.complete(now, true, false)
}

func (a *authAdmission) succeed(now time.Time) {
	a.complete(now, false, true)
}

// consume applies an attempt to the rate budget without treating it as a
// successful login. Registration uses this for both accepted and rejected
// submissions so creating new identities cannot bypass per-IP admission.
func (a *authAdmission) consume(now time.Time) {
	a.complete(now, true, false)
}

func (a *authAdmission) complete(now time.Time, countAgainstLimit, clearIdentity bool) {
	if a == nil || !a.active || a.limiter == nil {
		return
	}
	a.active = false
	a.limiter.finishAdmission(a.attempt, now, countAgainstLimit, clearIdentity)
}

func (l *authLimiter) finishAdmission(attempt authAttempt, now time.Time, countAgainstLimit, clearIdentity bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.finishWindowLocked(attempt.ipKey, authLimitRule{limit: authIPFailureLimit, window: authIPFailureWindow}, now, countAgainstLimit, false)
	l.finishWindowLocked(attempt.identityKey, authLimitRule{limit: authIdentityFailureLimit, window: authIdentityFailureWindow}, now, countAgainstLimit, clearIdentity)
}

func (l *authLimiter) finishWindowLocked(key string, rule authLimitRule, now time.Time, countAgainstLimit, clear bool) {
	entry := l.windows[key]
	if entry == nil {
		return
	}
	if entry.inFlight > 0 {
		entry.inFlight--
	}
	if countAgainstLimit {
		l.recordFailureLocked(key, rule, now)
		entry = l.windows[key]
	}
	if entry == nil {
		return
	}
	if clear {
		entry.failures = nil
	}
	entry.lastSeen = now
	l.deleteWindowIfIdleLocked(key, entry)
}

func (l *authLimiter) ensureWindowLocked(key string, rule authLimitRule, now time.Time) (*authFailureWindow, bool) {
	if entry := l.windows[key]; entry != nil {
		entry.rule = rule
		entry.lastSeen = now
		return entry, true
	}
	if !l.makeRoomLocked() {
		return nil, false
	}
	entry := &authFailureWindow{rule: rule, lastSeen: now}
	l.windows[key] = entry
	return entry, true
}

func (l *authLimiter) deleteWindowIfIdleLocked(key string, entry *authFailureWindow) {
	if entry != nil && entry.inFlight == 0 && len(entry.failures) == 0 {
		delete(l.windows, key)
	}
}

// retryAfter reports how long the caller must wait for every exceeded window
// to admit another authentication attempt.
func (l *authLimiter) retryAfter(attempt authAttempt, now time.Time) (time.Duration, bool) {
	if l == nil {
		return 0, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now, false)

	ipRetry := l.windowRetryAfterLocked(attempt.ipKey, now)
	identityRetry := l.windowRetryAfterLocked(attempt.identityKey, now)
	if identityRetry > ipRetry {
		ipRetry = identityRetry
	}
	return ipRetry, ipRetry > 0
}

func (l *authLimiter) recordFailure(attempt authAttempt, now time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now, true)
	l.recordFailureLocked(attempt.ipKey, authLimitRule{limit: authIPFailureLimit, window: authIPFailureWindow}, now)
	l.recordFailureLocked(attempt.identityKey, authLimitRule{limit: authIdentityFailureLimit, window: authIdentityFailureWindow}, now)
}

// clearIdentity lets a legitimate successful login recover immediately while
// retaining the IP window, which still constrains attacks spread across users.
func (l *authLimiter) clearIdentity(attempt authAttempt) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if entry := l.windows[attempt.identityKey]; entry != nil {
		entry.failures = nil
		l.deleteWindowIfIdleLocked(attempt.identityKey, entry)
	}
	l.mu.Unlock()
}

func (l *authLimiter) recordFailureLocked(key string, rule authLimitRule, now time.Time) {
	entry := l.windows[key]
	if entry == nil {
		if !l.makeRoomLocked() {
			return
		}
		entry = &authFailureWindow{rule: rule}
		l.windows[key] = entry
	}
	entry.rule = rule
	entry.failures = pruneAuthFailures(entry.failures, now.Add(-rule.window))
	entry.failures = append(entry.failures, now)
	if len(entry.failures) > rule.limit {
		entry.failures = entry.failures[len(entry.failures)-rule.limit:]
	}
	entry.lastSeen = now
}

func (l *authLimiter) windowRetryAfterLocked(key string, now time.Time) time.Duration {
	entry := l.windows[key]
	if entry == nil {
		return 0
	}
	entry.failures = pruneAuthFailures(entry.failures, now.Add(-entry.rule.window))
	if len(entry.failures) == 0 && entry.inFlight == 0 {
		delete(l.windows, key)
		return 0
	}
	if len(entry.failures)+entry.inFlight < entry.rule.limit {
		return 0
	}
	if len(entry.failures) < entry.rule.limit {
		return authInFlightRetryAfter
	}
	retry := entry.failures[len(entry.failures)-entry.rule.limit].Add(entry.rule.window).Sub(now)
	if retry <= 0 {
		return 0
	}
	return retry
}

func pruneAuthFailures(failures []time.Time, cutoff time.Time) []time.Time {
	first := 0
	for first < len(failures) && !failures[first].After(cutoff) {
		first++
	}
	if first == len(failures) {
		return nil
	}
	return failures[first:]
}

func (l *authLimiter) sweepLocked(now time.Time, ensureRoom bool) {
	if !ensureRoom && !l.nextSweep.IsZero() && now.Before(l.nextSweep) {
		return
	}
	if ensureRoom && len(l.windows) < l.maxEntries && !l.nextSweep.IsZero() && now.Before(l.nextSweep) {
		return
	}
	for key, entry := range l.windows {
		entry.failures = pruneAuthFailures(entry.failures, now.Add(-entry.rule.window))
		if len(entry.failures) == 0 && entry.inFlight == 0 {
			delete(l.windows, key)
		}
	}
	l.nextSweep = now.Add(authLimiterSweepInterval)
}

func (l *authLimiter) makeRoomLocked() bool {
	if len(l.windows) < l.maxEntries {
		return true
	}
	var oldestKey string
	var oldest time.Time
	for key, entry := range l.windows {
		if entry.inFlight > 0 {
			continue
		}
		if oldestKey == "" || entry.lastSeen.Before(oldest) || (entry.lastSeen.Equal(oldest) && key < oldestKey) {
			oldestKey = key
			oldest = entry.lastSeen
		}
	}
	if oldestKey == "" {
		return false
	}
	delete(l.windows, oldestKey)
	return true
}

func writeAuthRateLimit(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int64((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	writeError(w, http.StatusTooManyRequests, "too many authentication attempts; try again later")
}

func passwordHashConcurrency(gomaxprocs int) int {
	if gomaxprocs > 8 {
		return 8
	}
	if gomaxprocs < 2 {
		return 2
	}
	return gomaxprocs
}

var defaultPasswordHashSlots = make(chan struct{}, passwordHashConcurrency(runtime.GOMAXPROCS(0)))

func acquirePasswordHashSlot() func() {
	defaultPasswordHashSlots <- struct{}{}
	return func() { <-defaultPasswordHashSlots }
}

func tryAcquirePasswordHashSlot(ctx context.Context, slots chan struct{}) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, errPasswordHashBusy
	}
}

func writePasswordHashBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(passwordHashRetryAfter/time.Second)))
	writeError(w, http.StatusServiceUnavailable, "authentication service is busy; try again later")
}
