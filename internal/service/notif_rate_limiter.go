package service

import (
	"sync"
	"time"
)

// notifRateLimiter is a simple per-(recipient, type) sliding-window counter
// that drops events exceeding a per-minute threshold. It is used by
// NotificationDispatcher to prevent notification floods.
type notifRateLimiter struct {
	mu   sync.Mutex
	rate int                    // max notifications per recipient+type per minute
	win  time.Duration          // sliding window (always 1 minute)
	keys map[string][]time.Time // key=recipient:type → timestamps
	now  func() time.Time       // injectable clock for tests
}

// newNotifRateLimiter creates a rate limiter that allows up to rate events per
// recipient+type per minute.
func newNotifRateLimiter(rate int) *notifRateLimiter {
	return &notifRateLimiter{
		rate: rate,
		win:  time.Minute,
		keys: make(map[string][]time.Time),
		now:  time.Now,
	}
}

// NewNotifRateLimiterForTest creates a rate limiter with a test-friendly rate.
// Exported for use in tests across packages; production code uses
// WithDispatcherRateLimiter.
func NewNotifRateLimiterForTest(rate int) *notifRateLimiter {
	return newNotifRateLimiter(rate)
}

// Allow forwards to the unexported allow method for cross-package tests.
func (rl *notifRateLimiter) Allow(recipientUID, notifType string) bool {
	return rl.allow(recipientUID, notifType)
}

// allow returns true when the event should proceed, false when the rate limit
// would be exceeded. Cleanup of stale entries happens inline to keep memory
// bounded without a background goroutine.
func (rl *notifRateLimiter) allow(recipientUID, notifType string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	key := recipientUID + ":" + notifType
	now := rl.now()
	cutoff := now.Add(-rl.win)

	// Trim expired entries in-place.
	entries := rl.keys[key]
	firstLive := 0
	for i, t := range entries {
		if t.After(cutoff) {
			firstLive = i
			break
		}
		firstLive = i + 1
	}
	entries = entries[firstLive:]

	if len(entries) >= rl.rate {
		rl.keys[key] = entries
		return false
	}

	entries = append(entries, now)
	rl.keys[key] = entries
	return true
}
