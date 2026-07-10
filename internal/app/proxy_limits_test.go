package app

import (
	"net/http"
	"testing"
	"time"
)

func TestProxyLimiterEnforcesGlobalAndPerTokenLimits(t *testing.T) {
	limiter := newProxyLimiter(2, 1, 1)
	releaseA, status := limiter.tryAcquire("token-a")
	if status != 0 || releaseA == nil {
		t.Fatalf("first token acquisition status = %d", status)
	}
	if release, status := limiter.tryAcquire("token-a"); release != nil || status != http.StatusTooManyRequests {
		t.Fatalf("same-token acquisition = (%v, %d), want 429", release != nil, status)
	}
	releaseB, status := limiter.tryAcquire("token-b")
	if status != 0 || releaseB == nil {
		t.Fatalf("second token acquisition status = %d", status)
	}
	if release, status := limiter.tryAcquire("token-c"); release != nil || status != http.StatusServiceUnavailable {
		t.Fatalf("global acquisition = (%v, %d), want 503", release != nil, status)
	}
	releaseA()
	if release, status := limiter.tryAcquire("token-c"); status != 0 || release == nil {
		t.Fatalf("acquisition after release status = %d", status)
	} else {
		release()
	}
	releaseB()
}

func TestProxyLimiterBoundsMultipartSlots(t *testing.T) {
	limiter := newProxyLimiter(4, 4, 1)
	release, ok := limiter.tryAcquireMultipart(8)
	if !ok || release == nil {
		t.Fatal("first multipart slot was rejected")
	}
	if second, ok := limiter.tryAcquireMultipart(8); ok || second != nil {
		t.Fatal("second multipart slot exceeded configured capacity")
	}
	release()
	if second, ok := limiter.tryAcquireMultipart(8); !ok || second == nil {
		t.Fatal("multipart slot was not released")
	} else {
		second()
	}
}

func TestProxyLimiterBoundsAndReleasesBufferedBytes(t *testing.T) {
	limiter := newProxyLimiter(4, 4, 4)
	limiter.maxBuffered = 10

	release, ok := limiter.tryAcquireMultipart(6)
	if !ok || release == nil {
		t.Fatal("first buffer reservation was rejected")
	}
	if second, ok := limiter.tryAcquireMultipart(5); ok || second != nil {
		t.Fatal("multipart buffer budget was exceeded")
	}
	release()
	if second, ok := limiter.tryAcquireMultipart(5); !ok || second == nil {
		t.Fatal("multipart buffer budget was not released")
	} else {
		second()
	}
}

func TestRememberProxyFailureSweepsExpiredAndCapsEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := &Server{
		nowFunc:       func() time.Time { return now },
		proxyFailures: make(map[string]proxyFailureCacheEntry),
	}
	s.proxyFailures["expired"] = proxyFailureCacheEntry{Until: now.Add(-time.Second), Status: http.StatusBadGateway}
	for i := 0; i < maxProxyFailureCacheEntries+10; i++ {
		s.rememberProxyFailure("job", "token-"+itoa(i), http.StatusBadGateway)
	}
	if _, ok := s.proxyFailures["expired"]; ok {
		t.Fatal("expired proxy failure was not swept")
	}
	if got := len(s.proxyFailures); got > maxProxyFailureCacheEntries {
		t.Fatalf("proxy failure cache size = %d, want <= %d", got, maxProxyFailureCacheEntries)
	}
}
