package sso

import (
	"context"
	"errors"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errCircuitOpen is returned by validateUpstream when the breaker is open and no
// stale identity is available to serve. It wraps into the normal "sso validate"
// error so callers see a uniform validation failure.
var errCircuitOpen = errors.New("sso circuit breaker open")

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

func (s breakerState) String() string {
	switch s {
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// breaker is a minimal three-state circuit breaker guarding the SSO ValidateToken
// RPC. It trips after a configurable number of consecutive availability failures,
// fast-fails for a cooldown window, then admits a single probe to test recovery.
//
// Only availability failures (SSO unreachable/too slow) drive it — see
// isAvailabilityError; a healthy SSO rejecting a token must not trip it. It is
// safe for concurrent use and is only ever driven by the singleflight leader, so
// a cache-miss stampede records at most one outcome per upstream attempt.
//
// A threshold <= 0 disables the breaker entirely (Allow always true, Record a
// no-op), preserving the original always-attempt behaviour for dev stacks.
type breaker struct {
	threshold int
	cooldown  time.Duration

	mu       sync.Mutex
	state    breakerState
	failures int
	openedAt time.Time
	now      func() time.Time // injectable clock for deterministic tests
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	// A tripping breaker with no cooldown would re-probe on every call, defeating
	// the fast-fail; pick a safe default rather than surprise the operator.
	if threshold > 0 && cooldown <= 0 {
		cooldown = 10 * time.Second
	}
	return &breaker{
		threshold: threshold,
		cooldown:  cooldown,
		state:     breakerClosed,
		now:       time.Now,
	}
}

// Allow reports whether a caller may attempt the upstream RPC. When the breaker
// is open it returns false until the cooldown elapses, at which point the first
// caller is promoted to a half-open probe (returns true) and all others continue
// to fast-fail until that probe's outcome is recorded.
func (b *breaker) Allow() bool {
	if b.threshold <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerOpen:
		if b.now().Sub(b.openedAt) >= b.cooldown {
			b.state = breakerHalfOpen // this caller becomes the lone probe
			return true
		}
		return false
	case breakerHalfOpen:
		return false // a probe is already in flight
	default: // closed
		return true
	}
}

// Record feeds an upstream attempt's outcome back into the breaker. A success
// closes the breaker and clears the failure count; a failure either re-opens a
// half-open probe or, once consecutive failures reach the threshold, trips a
// closed breaker open.
func (b *breaker) Record(success bool) {
	if b.threshold <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.failures = 0
		b.state = breakerClosed
		return
	}
	b.failures++
	switch b.state {
	case breakerHalfOpen:
		b.state = breakerOpen
		b.openedAt = b.now()
	case breakerClosed:
		if b.failures >= b.threshold {
			b.state = breakerOpen
			b.openedAt = b.now()
		}
	}
}

// State returns the current state; used by tests and reserved for future metrics.
func (b *breaker) State() breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// isAvailabilityError reports whether err signals SSO being unreachable or too
// slow (gRPC Unavailable / DeadlineExceeded, or a context deadline) — as opposed
// to a business rejection like an invalid token, which proves SSO is alive.
func isAvailabilityError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}
