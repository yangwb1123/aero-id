package ratelimit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/rueidis"
)

// newMiniRedis starts an in-process miniredis server and returns a rueidis
// client connected to it. This helper is duplicated from
// internal/idempotency/store_redis_test.go (newMiniRedis, lines 167-186) so the
// ratelimit package stays self-contained; keep both copies in sync if the
// harness ever needs to change.
func newMiniRedis(t *testing.T) (*miniredis.Miniredis, rueidis.Client) {
	t.Helper()

	srv, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}

	client, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:  []string{srv.Addr()},
		DisableCache: true, // miniredis does not support RESP3 client-side caching
		AlwaysRESP2:  true,
	})
	if err != nil {
		srv.Close()
		t.Fatalf("rueidis.NewClient: %v", err)
	}

	return srv, client
}

// hashedKey mirrors RedisLimiter.key: raw keys must never be written to Redis.
func hashedKey(v string) string {
	sum := sha256.Sum256([]byte(v))
	return fmt.Sprintf("aero-id:rate:%x", sum[:])
}

// R1: a fixed window admits exactly burst calls, then denies with retry > 0.
func TestRedisLimiter_allowsExactlyBurst(t *testing.T) {
	cases := []struct {
		name  string
		rate  float64
		burst int
		want  time.Duration
	}{
		{name: "window floored to 1s", rate: 10, burst: 3, want: time.Second},
		{name: "window = burst/rate = 5s", rate: 1, burst: 5, want: 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, client := newMiniRedis(t)
			defer srv.Close()
			defer client.Close()

			l := NewRedisLimiter(client, tc.rate, tc.burst)
			if l == nil {
				t.Fatalf("NewRedisLimiter(%v, %d) = nil, want non-nil", tc.rate, tc.burst)
			}
			if l.window != tc.want {
				t.Fatalf("window = %v, want %v", l.window, tc.want)
			}

			ctx := context.Background()
			for i := 0; i < tc.burst; i++ {
				ok, retry, err := l.Allow(ctx, "k")
				if !ok || retry != 0 || err != nil {
					t.Fatalf("call %d = (ok=%v, retry=%v, err=%v), want (true, 0, nil)", i+1, ok, retry, err)
				}
			}
			ok, retry, err := l.Allow(ctx, "k")
			if ok || retry <= 0 || err != nil {
				t.Fatalf("call %d = (ok=%v, retry=%v, err=%v), want (false, retry>0, nil)", tc.burst+1, ok, retry, err)
			}
		})
	}
}

// R2: once the PEXPIRE window elapses the counter restarts and a full fresh
// budget is admitted; the key re-exists with the window re-armed.
func TestRedisLimiter_windowExpiryResetsCount(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()

	l := NewRedisLimiter(client, 1, 5) // window 5s
	if l == nil {
		t.Fatalf("NewRedisLimiter(1, 5) = nil, want non-nil")
	}
	ctx := context.Background()
	hashed := l.key("k")

	for i := 0; i < 5; i++ {
		if ok, retry, err := l.Allow(ctx, "k"); !ok || retry != 0 || err != nil {
			t.Fatalf("first window call %d = (ok=%v, retry=%v, err=%v), want (true, 0, nil)", i+1, ok, retry, err)
		}
	}
	if ok, _, _ := l.Allow(ctx, "k"); ok {
		t.Fatal("6th call before expiry should be denied")
	}

	// Advance past the window: the key expires, the next INCR hits the
	// count==1 branch and re-arms PEXPIRE.
	srv.FastForward(5*time.Second + time.Millisecond)

	for i := 0; i < 5; i++ {
		if ok, retry, err := l.Allow(ctx, "k"); !ok || retry != 0 || err != nil {
			t.Fatalf("post-expiry call %d = (ok=%v, retry=%v, err=%v), want (true, 0, nil)", i+1, ok, retry, err)
		}
	}
	if ok, _, _ := l.Allow(ctx, "k"); ok {
		t.Fatal("6th call after expiry should be denied")
	}
	if !srv.Exists(hashed) {
		t.Fatalf("key %q missing after expiry reset (PEXPIRE was not re-armed)", hashed)
	}
	ttl, err := client.Do(ctx, client.B().Pttl().Key(hashed).Build()).AsInt64()
	if err != nil {
		t.Fatalf("PTTL error: %v", err)
	}
	if ttl < 4900 || ttl > 5000 {
		t.Fatalf("PTTL = %d ms, want ~5000 ms (window re-armed on fresh count)", ttl)
	}
}

// R3: on deny, the retry hint is the remaining PTTL of the window in ms.
func TestRedisLimiter_denyRetryHintMatchesTTL(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()

	l := NewRedisLimiter(client, 1, 5) // window 5s
	if l == nil {
		t.Fatalf("NewRedisLimiter(1, 5) = nil, want non-nil")
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if ok, _, _ := l.Allow(ctx, "k"); !ok {
			t.Fatalf("first window call %d should pass", i+1)
		}
	}
	if ok, _, _ := l.Allow(ctx, "k"); ok {
		t.Fatal("6th call should be denied")
	}

	srv.FastForward(2500 * time.Millisecond)
	ok, retry, err := l.Allow(ctx, "k")
	if ok || err != nil {
		t.Fatalf("post-fast-forward call = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	if retry <= 0 || retry > 5*time.Second {
		t.Fatalf("retry = %v, want (0, 5s]", retry)
	}
	diff := retry - 2500*time.Millisecond
	if diff < 0 {
		diff = -diff
	}
	if diff > 50*time.Millisecond {
		t.Fatalf("retry = %v, want ~2500ms (±50ms)", retry)
	}
}

// R3 fallback: when PTTL reports no expiry (< 1 ms), deny falls back to the
// full window duration exactly.
func TestRedisLimiter_pttlFallbackUsesWindow(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()

	l := NewRedisLimiter(client, 1, 5) // window 5s
	if l == nil {
		t.Fatalf("NewRedisLimiter(1, 5) = nil, want non-nil")
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if ok, _, _ := l.Allow(ctx, "k"); !ok {
			t.Fatalf("first window call %d should pass", i+1)
		}
	}
	if ok, _, _ := l.Allow(ctx, "k"); ok {
		t.Fatal("6th call should be denied")
	}

	// PERSIST strips the expiry so PTTL returns -1, forcing the fallback.
	hashed := l.key("k")
	n, err := client.Do(ctx, client.B().Persist().Key(hashed).Build()).AsInt64()
	if err != nil || n != 1 {
		t.Fatalf("PERSIST = (%d, %v), want (1, nil)", n, err)
	}

	ok, retry, err := l.Allow(ctx, "k")
	if ok || err != nil {
		t.Fatalf("post-PERSIST call = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	if retry != 5*time.Second {
		t.Fatalf("retry = %v, want exactly 5s (full-window fallback for PTTL=-1)", retry)
	}
}

// R4: invalid constructor arguments yield a nil limiter; valid args do not.
func TestNewRedisLimiter_invalidArgsNil(t *testing.T) {
	_, client := newMiniRedis(t)
	defer client.Close()

	cases := []struct {
		name   string
		client rueidis.Client
		rate   float64
		burst  int
	}{
		{name: "nil client", client: nil, rate: 1, burst: 1},
		{name: "rate zero", client: client, rate: 0, burst: 1},
		{name: "burst zero", client: client, rate: 1, burst: 0},
		{name: "rate negative", client: client, rate: -1, burst: 1},
		{name: "burst negative", client: client, rate: 1, burst: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewRedisLimiter(tc.client, tc.rate, tc.burst); got != nil {
				t.Fatalf("NewRedisLimiter(client=%v, rate=%v, burst=%d) = %v, want nil", tc.client != nil, tc.rate, tc.burst, got)
			}
		})
	}
	if got := NewRedisLimiter(client, 1, 1); got == nil {
		t.Fatal("NewRedisLimiter(valid args) = nil, want non-nil")
	}
}

// R5: a disabled limiter (nil receiver, nil client, or burst <= 0) always
// allows — the middleware depends on fail-open for the disabled state.
func TestRedisLimiter_disabledAlwaysAllows(t *testing.T) {
	_, client := newMiniRedis(t)
	defer client.Close()

	ctx := context.Background()
	limiters := []*RedisLimiter{
		nil,
		{client: nil, rate: 1, burst: 1},
		{client: client, rate: 1, burst: 0},
	}
	for _, l := range limiters {
		for i := 0; i < 100; i++ {
			ok, retry, err := l.Allow(ctx, "k")
			if !ok || retry != 0 || err != nil {
				t.Fatalf("iteration %d: got (ok=%v, retry=%v, err=%v), want (true, 0, nil)", i, ok, retry, err)
			}
		}
	}
}

// R6: Redis only ever sees the sha256-hashed key, never the raw value.
func TestRedisLimiter_hashesRedisKey(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()

	l := NewRedisLimiter(client, 1, 5)
	if l == nil {
		t.Fatalf("NewRedisLimiter(1, 5) = nil, want non-nil")
	}
	ctx := context.Background()

	const raw = "actor-secret-1"
	if ok, retry, err := l.Allow(ctx, raw); !ok || retry != 0 || err != nil {
		t.Fatalf("Allow(%q) = (ok=%v, retry=%v, err=%v), want (true, 0, nil)", raw, ok, retry, err)
	}

	expected := hashedKey(raw)
	if !srv.Exists(expected) {
		t.Fatalf("hashed key %q not present in Redis", expected)
	}
	if srv.Exists(raw) {
		t.Fatal("raw key was written to Redis")
	}
	if got := l.key(raw); got != expected {
		t.Fatalf("l.key(%q) = %q, want %q", raw, got, expected)
	}
	for _, k := range srv.Keys() {
		if strings.Contains(k, raw) {
			t.Fatalf("Redis key %q contains raw value %q", k, raw)
		}
	}
}

// R7: a closed client surfaces the mapped INCR error — the tripwire the
// middleware's fail-closed policy depends on. An error must never be swallowed
// into (true, ...).
func TestRedisLimiter_closedClientErrors(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer client.Close()

	// Simulate a Redis outage: closing miniredis kills the listener and any
	// active connection, so the next INCR fails. (rueidis' standalone client
	// lazily reconnects after Client.Close(), so closing the server is the
	// deterministic in-process way to force the INCR site to error.)
	srv.Close()

	l := NewRedisLimiter(client, 1, 5)
	if l == nil {
		t.Fatalf("NewRedisLimiter(1, 5) = nil, want non-nil")
	}

	ok, retry, err := l.Allow(context.Background(), "k")
	if ok {
		t.Fatal("Allow after client.Close returned allowed=true, want false")
	}
	if retry != 0 {
		t.Fatalf("Allow after client.Close returned retry=%v, want 0", retry)
	}
	if err == nil {
		t.Fatal("Allow after client.Close returned nil error, want mapped error")
	}
	if !strings.Contains(err.Error(), "redis rate limit increment") {
		t.Fatalf("error %q does not contain %q", err, "redis rate limit increment")
	}
}

// R8: windows are request-anchored fixed windows — the instant the previous
// window expires, a full second burst is re-admitted back-to-back (2x burst
// across a boundary). This pins the documented divergence from the smooth
// local token bucket rather than silently changing it.
func TestRedisLimiter_fixedWindow_boundaryAllowsSecondFullBurst(t *testing.T) {
	srv, client := newMiniRedis(t)
	defer srv.Close()
	defer client.Close()

	l := NewRedisLimiter(client, 1, 5) // window 5s
	if l == nil {
		t.Fatalf("NewRedisLimiter(1, 5) = nil, want non-nil")
	}
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if ok, retry, err := l.Allow(ctx, "k"); !ok || retry != 0 || err != nil {
			t.Fatalf("first window call %d = (ok=%v, retry=%v, err=%v), want (true, 0, nil)", i+1, ok, retry, err)
		}
	}
	if ok, _, _ := l.Allow(ctx, "k"); ok {
		t.Fatal("6th call in first window should be denied")
	}

	// Control: a distinct key has its own counter and is unaffected.
	if ok, _, err := l.Allow(ctx, "other"); !ok || err != nil {
		t.Fatalf("distinct key 'other' = (ok=%v, err=%v), want (true, nil)", ok, err)
	}

	// Cross the boundary with no further time advance after: all 5 pass
	// back-to-back, then the 6th denies again. Total admitted = 10 = 2x burst.
	srv.FastForward(5*time.Second + time.Millisecond)
	for i := 0; i < 5; i++ {
		if ok, retry, err := l.Allow(ctx, "k"); !ok || retry != 0 || err != nil {
			t.Fatalf("second window call %d = (ok=%v, retry=%v, err=%v), want (true, 0, nil)", i+1, ok, retry, err)
		}
	}
	if ok, _, _ := l.Allow(ctx, "k"); ok {
		t.Fatal("6th call in second window should be denied")
	}
}

// R8 contrast: the local token bucket does NOT re-admit a full burst at a
// boundary — at rate=1 it refills smoothly, exactly one token per second.
// Documents the fixed-window vs token-bucket divergence in executable form.
func TestLocalLimiter_tokenBucket_refillsSmoothly(t *testing.T) {
	l := New(1, 5, time.Minute) // same parameters as the Redis fixed-window tests
	now := time.Now()
	l.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if !l.Allow("k") {
			t.Fatalf("call %d within burst should pass", i+1)
		}
	}
	if l.Allow("k") {
		t.Fatal("6th call should be blocked")
	}

	// After 1s at rate=1/s exactly one token has refilled: one pass, then
	// blocked again — a fixed window at the same boundary would admit 5.
	now = now.Add(time.Second)
	if !l.Allow("k") {
		t.Fatal("call after 1s refill should pass (1 token accrued)")
	}
	if l.Allow("k") {
		t.Fatal("call immediately after the refill should be blocked (only 1 token)")
	}

	// After another 5s the full capacity of 5 is available and consumed.
	now = now.Add(5 * time.Second)
	for i := 0; i < 5; i++ {
		if !l.Allow("k") {
			t.Fatalf("call %d after full refill should pass", i+1)
		}
	}
	if l.Allow("k") {
		t.Fatal("call beyond full refill should be blocked")
	}
}
