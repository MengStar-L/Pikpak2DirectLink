package app

import (
	"net/http"
	"sync"
)

const (
	maxProxyActiveRequests            = 16
	maxProxyRequestsPerToken          = 4
	maxProxyMultipartRequests         = 4
	maxProxyBufferedBytes       int64 = 64 << 20
	maxProxyFailureCacheEntries       = 4096
)

type proxyLimiter struct {
	mu           sync.Mutex
	active       int
	perToken     map[string]int
	maxActive    int
	maxPerToken  int
	multipart    int
	maxMultipart int
	buffered     int64
	maxBuffered  int64
}

func newProxyLimiter(maxActive, maxPerToken, maxMultipart int) *proxyLimiter {
	return &proxyLimiter{
		perToken:     make(map[string]int),
		maxActive:    maxActive,
		maxPerToken:  maxPerToken,
		maxMultipart: maxMultipart,
		maxBuffered:  maxProxyBufferedBytes,
	}
}

func newDefaultProxyLimiter() *proxyLimiter {
	return newProxyLimiter(maxProxyActiveRequests, maxProxyRequestsPerToken, maxProxyMultipartRequests)
}

func (l *proxyLimiter) tryAcquire(token string) (func(), int) {
	if l == nil {
		return func() {}, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxPerToken > 0 && l.perToken[token] >= l.maxPerToken {
		return nil, http.StatusTooManyRequests
	}
	if l.maxActive > 0 && l.active >= l.maxActive {
		return nil, http.StatusServiceUnavailable
	}
	l.active++
	l.perToken[token]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.active--
			if l.perToken[token] <= 1 {
				delete(l.perToken, token)
			} else {
				l.perToken[token]--
			}
		})
	}, 0
}

func (l *proxyLimiter) tryAcquireMultipart(bufferBytes int64) (func(), bool) {
	if l == nil {
		return func() {}, true
	}
	if bufferBytes < 0 {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxMultipart > 0 && l.multipart >= l.maxMultipart {
		return nil, false
	}
	if l.maxBuffered > 0 && bufferBytes > l.maxBuffered-l.buffered {
		return nil, false
	}
	l.multipart++
	l.buffered += bufferBytes
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.multipart--
			l.buffered -= bufferBytes
			l.mu.Unlock()
		})
	}, true
}
