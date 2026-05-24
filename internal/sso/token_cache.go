package sso

import (
	"sync"
	"time"
)

// TokenCache is an in-memory token→uid cache with a maximum TTL ceiling.
// SSO-provided expirations longer than the ceiling are clamped.
type TokenCache struct {
	maxTTL time.Duration
	mu     sync.RWMutex
	items  map[string]tokenEntry
}

type tokenEntry struct {
	uid    string
	expiry time.Time
}

func NewTokenCache(maxTTL time.Duration) *TokenCache {
	return &TokenCache{
		maxTTL: maxTTL,
		items:  make(map[string]tokenEntry),
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
		if now.After(e.expiry) {
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
