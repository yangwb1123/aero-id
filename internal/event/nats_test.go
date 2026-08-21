package event

import (
	"context"
	"errors"
	"testing"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func TestTransientDeliveryError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"internal error retries", apperr.Internal("event.apply_failed", "db down"), true},
		{"unavailable retries", apperr.Unavailable("event.apply_failed", "source down"), true},
		{"invalid projection terminates", apperr.BadRequest("event.invalid_projection", "bad"), false},
		{"tenant mismatch terminates", apperr.Forbidden("event.tenant_mismatch", "wrong tenant"), false},
		{"residency policy terminates", apperr.Forbidden("event.region_not_allowed", "region"), false},
		{"plain error retries", errors.New("boom"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transientDeliveryError(tc.err); got != tc.want {
				t.Errorf("transientDeliveryError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestNATSConfigValidation(t *testing.T) {
	if err := validateNATSConfig(NATSConfig{}); err == nil {
		t.Fatal("empty config must fail")
	}
	if err := validateNATSConfig(NATSConfig{URL: "nats://x", Stream: "s"}); err == nil {
		t.Fatal("missing subject/durable must fail")
	}
	if err := validateNATSConfig(NATSConfig{URL: "nats://x", Stream: "s", Subject: "sub", DeadLetterSubject: "sub.dead-letter", Durable: "d"}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := validateNATSConfig(NATSConfig{URL: "nats://x", Stream: "s", Subject: "sub", DeadLetterSubject: "sub", Durable: "d"}); err == nil {
		t.Fatal("canonical and dead-letter subjects must not be identical")
	}
}

func TestNATSDefaults(t *testing.T) {
	bus := &NATSBus{cfg: NATSConfig{}}
	if got := bus.maxRedeliveries(); got != 5 {
		t.Errorf("maxRedeliveries = %d, want 5", got)
	}
	if got := bus.ackWait(); got.String() != "30s" {
		t.Errorf("ackWait = %v, want 30s", got)
	}
	bus = &NATSBus{cfg: NATSConfig{MaxRedeliveries: 2, AckWait: 0}}
	if got := bus.maxRedeliveries(); got != 2 {
		t.Errorf("maxRedeliveries = %d, want 2", got)
	}
	if bus.Close() != nil {
		t.Error("closing a nil-connection bus must be a no-op")
	}
}

var _ = context.Background
