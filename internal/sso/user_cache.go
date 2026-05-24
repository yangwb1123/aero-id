package sso

import (
	"context"
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

type UserCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	items   map[string]userEntry
	fetcher Fetcher
}

func NewUserCache(ttl time.Duration, fetcher Fetcher) *UserCache {
	return &UserCache{
		ttl:     ttl,
		items:   make(map[string]userEntry),
		fetcher: fetcher,
	}
}

// GetMany returns a map keyed by uid. UIDs not found in cache trigger a single
// batch call to the configured Fetcher. Duplicate input uids are deduplicated.
func (c *UserCache) GetMany(ctx context.Context, uids []string) (map[string]User, error) {
	out := make(map[string]User, len(uids))
	seen := make(map[string]struct{}, len(uids))
	var missing []string
	now := time.Now()

	c.mu.RLock()
	for _, u := range uids {
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		if e, ok := c.items[u]; ok && now.Before(e.expiry) {
			out[u] = e.user
		} else {
			missing = append(missing, u)
		}
	}
	c.mu.RUnlock()

	if len(missing) == 0 {
		return out, nil
	}
	fetched, err := c.fetcher(ctx, missing)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	exp := now.Add(c.ttl)
	for u, info := range fetched {
		c.items[u] = userEntry{user: info, expiry: exp}
		out[u] = info
	}
	c.mu.Unlock()
	return out, nil
}
