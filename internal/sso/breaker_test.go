package sso

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeClock is a manually-advanced clock for deterministic breaker timing tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestBreaker(threshold int, cooldown time.Duration) (*breaker, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	b := newBreaker(threshold, cooldown)
	b.now = clk.now
	return b, clk
}

func TestBreaker_TripsAfterThreshold(t *testing.T) {
	b, _ := newTestBreaker(3, time.Minute)

	for i := 0; i < 2; i++ {
		if !b.Allow() {
			t.Fatalf("failure %d: Allow should be true while closed", i)
		}
		b.Record(false)
		if b.State() != breakerClosed {
			t.Fatalf("after %d failures state = %s, want closed", i+1, b.State())
		}
	}
	// Third consecutive failure trips it.
	if !b.Allow() {
		t.Fatal("Allow should still be true on the threshold-th attempt")
	}
	b.Record(false)
	if b.State() != breakerOpen {
		t.Fatalf("state = %s, want open after reaching threshold", b.State())
	}
	if b.Allow() {
		t.Fatal("Allow must be false while open within cooldown")
	}
}

func TestBreaker_SuccessResetsFailureCount(t *testing.T) {
	b, _ := newTestBreaker(3, time.Minute)

	b.Allow()
	b.Record(false)
	b.Allow()
	b.Record(false) // 2 consecutive failures, one short of the threshold
	b.Allow()
	b.Record(true) // success clears the count
	if b.State() != breakerClosed {
		t.Fatalf("state = %s, want closed", b.State())
	}
	// Two more failures must not trip it (counter was reset).
	b.Allow()
	b.Record(false)
	b.Allow()
	b.Record(false)
	if b.State() != breakerClosed {
		t.Fatalf("state = %s, want still closed (success should have reset the count)", b.State())
	}
}

func TestBreaker_FastFailsDuringCooldownThenProbes(t *testing.T) {
	b, clk := newTestBreaker(1, 30*time.Second)

	b.Allow()
	b.Record(false) // trips immediately (threshold 1)
	if b.State() != breakerOpen {
		t.Fatalf("state = %s, want open", b.State())
	}

	// Within the cooldown: fast-fail.
	clk.advance(29 * time.Second)
	if b.Allow() {
		t.Fatal("Allow must be false before cooldown elapses")
	}

	// Cooldown elapsed: exactly one caller is admitted as the half-open probe.
	clk.advance(2 * time.Second)
	if !b.Allow() {
		t.Fatal("Allow should admit a probe once cooldown elapses")
	}
	if b.State() != breakerHalfOpen {
		t.Fatalf("state = %s, want half-open after admitting probe", b.State())
	}
	if b.Allow() {
		t.Fatal("a second concurrent caller must fast-fail while a probe is in flight")
	}
}

func TestBreaker_ProbeSuccessCloses(t *testing.T) {
	b, clk := newTestBreaker(1, time.Second)
	b.Allow()
	b.Record(false)
	clk.advance(2 * time.Second)
	b.Allow() // probe
	b.Record(true)
	if b.State() != breakerClosed {
		t.Fatalf("state = %s, want closed after successful probe", b.State())
	}
	if !b.Allow() {
		t.Fatal("Allow should be true again after recovery")
	}
}

func TestBreaker_ProbeFailureReopens(t *testing.T) {
	b, clk := newTestBreaker(1, time.Second)
	b.Allow()
	b.Record(false)
	clk.advance(2 * time.Second)
	b.Allow() // probe
	b.Record(false)
	if b.State() != breakerOpen {
		t.Fatalf("state = %s, want open after failed probe", b.State())
	}
	// And the cooldown restarts from the probe failure.
	if b.Allow() {
		t.Fatal("Allow must be false immediately after a failed probe re-opens the breaker")
	}
	clk.advance(2 * time.Second)
	if !b.Allow() {
		t.Fatal("Allow should admit another probe after the restarted cooldown")
	}
}

func TestBreaker_DisabledAlwaysAllows(t *testing.T) {
	b, _ := newTestBreaker(0, time.Minute)
	for i := 0; i < 10; i++ {
		if !b.Allow() {
			t.Fatal("disabled breaker must always allow")
		}
		b.Record(false)
	}
	if b.State() != breakerClosed {
		t.Fatalf("disabled breaker state = %s, want closed", b.State())
	}
}

func TestIsAvailabilityError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unavailable", status.Error(codes.Unavailable, "down"), true},
		{"deadline grpc", status.Error(codes.DeadlineExceeded, "slow"), true},
		{"context deadline", context.DeadlineExceeded, true},
		{"unauthenticated", status.Error(codes.Unauthenticated, "bad token"), false},
		{"permission denied", status.Error(codes.PermissionDenied, "nope"), false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		if got := isAvailabilityError(tc.err); got != tc.want {
			t.Errorf("%s: isAvailabilityError = %v, want %v", tc.name, got, tc.want)
		}
	}
}
