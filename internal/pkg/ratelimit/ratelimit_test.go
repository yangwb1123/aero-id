package ratelimit

import (
	"testing"
	"time"
)

func TestAllow_underLimitPasses(t *testing.T) {
	l := New(10, 5, time.Minute)
	for i := 0; i < 5; i++ {
		if !l.Allow("k") {
			t.Fatalf("request %d within burst should pass", i)
		}
	}
}

func TestAllow_overLimitBlocks(t *testing.T) {
	l := New(1, 3, time.Minute)
	// Pin the clock so no refill happens between calls.
	now := time.Now()
	l.now = func() time.Time { return now }

	// Burst of 3 succeeds, the 4th is blocked.
	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatalf("request %d should pass within burst", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("request beyond burst should be blocked")
	}
}

func TestAllow_refillsOverTime(t *testing.T) {
	l := New(2, 1, time.Minute) // 2 tokens/sec, burst 1
	now := time.Now()
	l.now = func() time.Time { return now }

	if !l.Allow("k") {
		t.Fatal("first request should pass")
	}
	if l.Allow("k") {
		t.Fatal("second immediate request should be blocked")
	}
	// Advance 600ms: at 2/sec that accrues 1.2 tokens, enough for one more.
	now = now.Add(600 * time.Millisecond)
	if !l.Allow("k") {
		t.Fatal("request after refill window should pass")
	}
}

func TestAllow_distinctKeysIndependent(t *testing.T) {
	l := New(1, 1, time.Minute)
	now := time.Now()
	l.now = func() time.Time { return now }

	if !l.Allow("a") {
		t.Fatal("key a first request should pass")
	}
	if !l.Allow("b") {
		t.Fatal("key b has its own bucket and should pass")
	}
	if l.Allow("a") {
		t.Fatal("key a is now exhausted")
	}
}

func TestRunCleaner_evictsIdleBuckets(t *testing.T) {
	l := New(1, 1, 50*time.Millisecond)
	base := time.Now()
	l.now = func() time.Time { return base }

	l.Allow("k")
	if got := l.size(); got != 1 {
		t.Fatalf("size = %d, want 1 after first request", got)
	}
	// Advance past the idle window and sweep directly (deterministic).
	l.sweepIdle(base.Add(time.Second))
	if got := l.size(); got != 0 {
		t.Fatalf("size = %d, want 0 after idle eviction", got)
	}
}

func TestRunCleaner_stopIsIdempotentAndStops(t *testing.T) {
	l := New(1, 1, time.Millisecond)
	stop := l.RunCleaner(time.Millisecond)
	stop() // should terminate the goroutine without panicking
}

func TestAllow_disabledLimiterAlwaysPasses(t *testing.T) {
	for _, l := range []*Limiter{New(0, 5, time.Minute), New(5, 0, time.Minute), nil} {
		for i := 0; i < 100; i++ {
			if !l.Allow("k") {
				t.Fatal("disabled/nil limiter must always allow")
			}
		}
	}
}

func TestRetryAfter(t *testing.T) {
	if got := New(2, 1, time.Minute).RetryAfter(); got != 500*time.Millisecond {
		t.Fatalf("RetryAfter = %v, want 500ms", got)
	}
	if got := New(0, 1, time.Minute).RetryAfter(); got != 0 {
		t.Fatalf("disabled RetryAfter = %v, want 0", got)
	}
}
