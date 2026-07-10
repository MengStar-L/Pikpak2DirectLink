package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	oauthStateTTL        = 10 * time.Minute
	oauthStateMaxEntries = 4096
)

type oauthStateEntry struct {
	nonceDigest [sha256.Size]byte
	expiresAt   time.Time
}

type oauthStateStore struct {
	mu         sync.Mutex
	states     map[string]oauthStateEntry
	ttl        time.Duration
	maxEntries int
	nextSweep  time.Time
}

func newOAuthStateStore() *oauthStateStore {
	return newOAuthStateStoreWithConfig(oauthStateTTL, oauthStateMaxEntries)
}

func newOAuthStateStoreWithConfig(ttl time.Duration, maxEntries int) *oauthStateStore {
	if ttl <= 0 {
		ttl = oauthStateTTL
	}
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &oauthStateStore{
		states:     make(map[string]oauthStateEntry),
		ttl:        ttl,
		maxEntries: maxEntries,
	}
}

func (s *oauthStateStore) create(nonce string, now time.Time) (string, error) {
	nonce = strings.TrimSpace(nonce)
	if nonce == "" {
		return "", errors.New("OAuth browser nonce is required")
	}
	state, err := generateSessionID()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now, len(s.states) >= s.maxEntries)
	if len(s.states) >= s.maxEntries {
		s.evictOldestLocked()
	}
	s.states[state] = oauthStateEntry{
		nonceDigest: sha256.Sum256([]byte(nonce)),
		expiresAt:   now.Add(s.ttl),
	}
	return state, nil
}

func (s *oauthStateStore) consume(state, nonce string, now time.Time) bool {
	state = strings.TrimSpace(state)
	nonce = strings.TrimSpace(nonce)
	if state == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now, false)
	entry, ok := s.states[state]
	if !ok {
		return false
	}
	delete(s.states, state)
	if nonce == "" {
		return false
	}
	actual := sha256.Sum256([]byte(nonce))
	return entry.expiresAt.After(now) && subtle.ConstantTimeCompare(actual[:], entry.nonceDigest[:]) == 1
}

func (s *oauthStateStore) sweepLocked(now time.Time, force bool) {
	if !force && !s.nextSweep.IsZero() && now.Before(s.nextSweep) {
		return
	}
	for state, entry := range s.states {
		if !entry.expiresAt.After(now) {
			delete(s.states, state)
		}
	}
	s.nextSweep = now.Add(time.Minute)
}

func (s *oauthStateStore) evictOldestLocked() {
	var oldestState string
	var oldestExpiry time.Time
	for state, entry := range s.states {
		if oldestState == "" || entry.expiresAt.Before(oldestExpiry) || (entry.expiresAt.Equal(oldestExpiry) && state < oldestState) {
			oldestState = state
			oldestExpiry = entry.expiresAt
		}
	}
	delete(s.states, oldestState)
}
