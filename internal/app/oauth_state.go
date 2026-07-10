package app

import (
	"sync"
	"time"
)

type oauthStateStore struct {
	mu     sync.Mutex
	states map[string]time.Time
}

func newOAuthStateStore() *oauthStateStore {
	return &oauthStateStore{states: make(map[string]time.Time)}
}

func (s *oauthStateStore) create(now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := generateSessionID()
	if err != nil {
		return "", err
	}
	s.states[state] = now.Add(10 * time.Minute)
	return state, nil
}

func (s *oauthStateStore) consume(state string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.states[state]
	if !ok {
		return false
	}
	delete(s.states, state)
	return expiry.After(now)
}
