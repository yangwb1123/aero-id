// Package ratelimit provides a small, dependency-free, concurrency-safe
// per-key token-bucket limiter. It is used by the HTTP middleware and the gRPC
// interceptor to throttle requests per authenticated actor uid (falling back to
// client IP for unauthenticated traffic).
//
// The limiter is PER-INSTANCE: each running aero-id process keeps its own
// buckets in memory. Distributed/global rate limiting would need shared state
// (e.g. Redis) and is intentionally out of scope here.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// DistributedLimiter is the shared-state counterpart to Limiter. Implementations
// must make one decision atomically for a key; transport middleware falls back
// to the local limiter when the shared dependency is unavailable.
type DistributedLimiter interface {
	Allow(context.Context, string) (bool, time.Duration, error)
}

// DenyAllLimiter is a deterministic DistributedLimiter that always denies with
// a fixed retry hint. Useful for tests and for policy that must never admit a
// request when the shared limiter is intentionally disabled.
type DenyAllLimiter struct {
	Retry time.Duration
}

func (d DenyAllLimiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	if d.Retry <= 0 {
		d.Retry = time.Second
	}
	return false, d.Retry, nil
}

// bucket is a single token bucket. Tokens refill continuously at rate per
// second up to burst. Access is guarded by the parent Limiter's mutex, so the
// bucket itself needs no lock.
type bucket struct {
	tokens   float64
	last     time.Time // last time tokens were refilled
	lastSeen time.Time // last time the bucket was touched (for idle eviction)
}

// Limiter is a map of key -> token bucket with periodic eviction of idle
// buckets so memory stays bounded under churning keys.
type Limiter struct {
	rate  float64       // tokens added per second
	burst float64       // bucket capacity (max tokens)
	idle  time.Duration // buckets untouched for this long are evicted

	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time // injectable clock for tests
}

// New returns a Limiter allowing rate requests/second per key with a burst
// ceiling. Buckets idle for idleTTL are evicted by RunCleaner. A non-positive
// rate or burst yields a limiter that allows everything (fail-open by config).
func New(rate float64, burst int, idleTTL time.Duration) *Limiter {
	return &Limiter{
		rate:    rate,
		burst:   float64(burst),
		idle:    idleTTL,
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

// Allow reports whether a request for key may proceed, consuming one token if
// so. When the limiter is effectively disabled (non-positive rate or burst) it
// always allows — callers treat that as "limiting off".
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.rate <= 0 || l.burst <= 0 {
		return true
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		// New keys start full so a first request always succeeds, then they
		// consume one token immediately below.
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		// Refill based on elapsed time since the last update, capped at burst.
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens += elapsed * l.rate
			if b.tokens > l.burst {
				b.tokens = l.burst
			}
			b.last = now
		}
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// RetryAfter returns a conservative hint for how long the caller should wait
// before retrying key: the time to accrue one full token at the configured
// rate. It returns 0 when limiting is disabled.
func (l *Limiter) RetryAfter() time.Duration {
	if l == nil || l.rate <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) / l.rate)
}

// sweepIdle evicts buckets untouched for at least the idle window.
func (l *Limiter) sweepIdle(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) >= l.idle {
			delete(l.buckets, k)
		}
	}
}

// size reports the current number of live buckets (used by tests).
func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// RunCleaner starts a background goroutine that periodically evicts idle
// buckets. It mirrors sso.TokenCache.RunCleaner: it returns a stop func that
// terminates the goroutine. A non-positive interval disables the cleaner and
// returns a no-op stop.
func (l *Limiter) RunCleaner(interval time.Duration) (stop func()) {
	if l == nil || interval <= 0 {
		return func() {}
	}
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-t.C:
				l.sweepIdle(l.now())
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}
