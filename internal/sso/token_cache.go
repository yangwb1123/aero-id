package sso

import (
	"sync"
	"time"
)

// TokenCache is an in-memory token→uid cache with a maximum TTL ceiling.
// SSO-provided expirations longer than the ceiling are clamped. When configured
// with a stale window it also retains expired entries for that extra duration so
// the client can serve a last-known uid while SSO is unreachable.
type TokenCache struct {
	maxTTL   time.Duration
	staleFor time.Duration // grace window past expiry during which GetStale may serve
	mu       sync.RWMutex
	items    map[string]tokenEntry
}

type tokenEntry struct {
	uid    string
	expiry time.Time
}

func NewTokenCache(maxTTL time.Duration) *TokenCache {
	return NewTokenCacheWithStale(maxTTL, 0)
}

// NewTokenCacheWithStale retains expired entries for an extra staleFor window so the
// SSO client can serve a last-known uid when the upstream is unreachable. A staleFor
// of 0 disables stale serving (entries drop as soon as they expire).
func NewTokenCacheWithStale(maxTTL, staleFor time.Duration) *TokenCache {
	return &TokenCache{
		maxTTL:   maxTTL,
		staleFor: staleFor,
		items:    make(map[string]tokenEntry),
	}
}

func (c *TokenCache) Get(token string) (string, bool) {
	c.mu.RLock()
	e, ok := c.items[token]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiry) {
		return "", false
	}
	return e.uid, true
}

// GetStale returns the cached uid for a token even after it has expired, as long as
// the entry is still within the stale grace window (expiry+staleFor). It is the
// fallback used when SSO is unreachable; ok is false when the entry is absent or
// older than the grace window.
func (c *TokenCache) GetStale(token string) (string, bool) {
	c.mu.RLock()
	e, ok := c.items[token]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiry.Add(c.staleFor)) {
		return "", false
	}
	return e.uid, true
}

func (c *TokenCache) GetExpiry(token string) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[token]
	return e.expiry, ok
}

// Set stores token→uid with the lesser of (provided expiry, now+maxTTL).
func (c *TokenCache) Set(token, uid string, expiry time.Time) {
	ceiling := time.Now().Add(c.maxTTL)
	if expiry.IsZero() || expiry.After(ceiling) {
		expiry = ceiling
	}
	c.mu.Lock()
	c.items[token] = tokenEntry{uid: uid, expiry: expiry}
	c.mu.Unlock()
}

func (c *TokenCache) SweepExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.items {
		// Keep entries until they pass the stale grace window so GetStale can still
		// serve them during an SSO outage.
		if now.After(e.expiry.Add(c.staleFor)) {
			delete(c.items, k)
		}
	}
}

// RunCleaner starts a background goroutine that sweeps expired entries.
// It returns a stop func; call it to terminate the goroutine.
func (c *TokenCache) RunCleaner(interval time.Duration) (stop func()) {
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-t.C:
				c.SweepExpired(time.Now())
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}
