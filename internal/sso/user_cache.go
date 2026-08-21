package sso

import (
	"context"
	"errors"
	"sync"
	"time"
)

// User is the public shape held in cache (mirrors SSO `User` proto).
type User struct {
	UID      string
	Nickname string
	Avatar   string
}

// Fetcher is the function the cache delegates to on miss.
type Fetcher func(ctx context.Context, uids []string) (map[string]User, error)

type userEntry struct {
	user   User
	expiry time.Time
}

type userLookup struct {
	out     map[string]User
	missing []string
	stale   map[string]User
}

type UserCache struct {
	ttl      time.Duration
	staleFor time.Duration
	mu       sync.RWMutex
	items    map[string]userEntry
	fetcher  Fetcher
	now      func() time.Time
	// onLookup, when set, is called once per (deduplicated) requested uid with
	// whether it was served from cache. It lets the client export a hit ratio
	// without exposing cache internals. nil-safe.
	onLookup func(hit bool)
}

func NewUserCache(ttl time.Duration, fetcher Fetcher) *UserCache {
	return NewUserCacheWithStale(ttl, 0, fetcher)
}

// NewUserCacheWithStale keeps expired entries for staleFor so an SSO outage can
// still return last-known display data. A zero staleFor disables that fallback.
func NewUserCacheWithStale(ttl, staleFor time.Duration, fetcher Fetcher) *UserCache {
	return &UserCache{
		ttl:      ttl,
		staleFor: staleFor,
		items:    make(map[string]userEntry),
		fetcher:  fetcher,
		now:      time.Now,
	}
}

// SetLookupObserver registers a callback invoked once per deduplicated requested uid
// with its cache hit/miss outcome. Pass nil to disable.
func (c *UserCache) SetLookupObserver(fn func(hit bool)) { c.onLookup = fn }

// GetMany returns a map keyed by uid. UIDs not found in cache trigger a single
// batch call to the configured Fetcher. Duplicate input uids are deduplicated.
func (c *UserCache) GetMany(ctx context.Context, uids []string) (map[string]User, error) {
	lookup := c.lookupCached(uids, c.now())
	if len(lookup.missing) == 0 {
		return lookup.out, nil
	}
	fetched, err := c.fetcher(ctx, lookup.missing)
	if err != nil {
		c.addStaleOnAvailability(lookup.out, lookup.stale, err)
		return lookup.out, err
	}
	c.storeFetched(lookup.out, fetched, c.now())
	return lookup.out, nil
}

func (c *UserCache) lookupCached(uids []string, now time.Time) userLookup {
	lookup := userLookup{
		out:   make(map[string]User, len(uids)),
		stale: make(map[string]User),
	}
	seen := make(map[string]struct{}, len(uids))

	c.mu.RLock()
	for _, u := range uids {
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		if e, ok := c.items[u]; ok && now.Before(e.expiry) {
			lookup.out[u] = e.user
			if c.onLookup != nil {
				c.onLookup(true)
			}
		} else {
			lookup.missing = append(lookup.missing, u)
			if ok && c.staleFor > 0 && !now.After(e.expiry.Add(c.staleFor)) {
				lookup.stale[u] = e.user
			}
			if c.onLookup != nil {
				c.onLookup(false)
			}
		}
	}
	c.mu.RUnlock()
	return lookup
}

func (c *UserCache) addStaleOnAvailability(out, stale map[string]User, err error) {
	// Cache hits remain useful when the missing subset cannot be fetched. During
	// an availability failure, also serve entries inside the bounded stale grace
	// period; business errors never use stale data.
	if isAvailabilityError(err) || errors.Is(err, context.DeadlineExceeded) {
		for u, info := range stale {
			out[u] = info
		}
	}
}

func (c *UserCache) storeFetched(out map[string]User, fetched map[string]User, now time.Time) {
	c.mu.Lock()
	exp := now.Add(c.ttl)
	for u, info := range fetched {
		c.items[u] = userEntry{user: info, expiry: exp}
		out[u] = info
	}
	c.mu.Unlock()
}

// SweepExpired removes entries that are no longer eligible for either a fresh
// hit or stale serving. It is intentionally separate from GetMany so cleanup
// never adds lock or allocation work to the request hot path.
func (c *UserCache) SweepExpired(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for uid, e := range c.items {
		if now.After(e.expiry.Add(c.staleFor)) {
			delete(c.items, uid)
		}
	}
}

// RunCleaner starts a bounded-memory expiry sweep and returns an idempotent stop
// function suitable for composition-root shutdown.
func (c *UserCache) RunCleaner(interval time.Duration) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.SweepExpired(c.now())
			case <-done:
				return
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}
