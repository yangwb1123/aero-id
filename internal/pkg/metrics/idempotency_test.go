package metrics_test

import (
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/pkg/metrics"
)

func TestObserveIdempotencyRendersSeries(t *testing.T) {
	r := metrics.NewRegistry()
	r.ObserveIdempotency("complete_failed")
	r.ObserveIdempotency("complete_failed")
	r.ObserveIdempotency("capture_overflow")

	var b strings.Builder
	r.WriteText(&b)
	out := b.String()
	if !strings.Contains(out, `aeroid_idempotency_total{op="complete_failed"} 2`) {
		t.Errorf("exposition missing complete_failed=2:\n%s", out)
	}
	if !strings.Contains(out, `aeroid_idempotency_total{op="capture_overflow"} 1`) {
		t.Errorf("exposition missing capture_overflow=1:\n%s", out)
	}
	if strings.Contains(out, `aeroid_idempotency_total{op="release_failed"}`) {
		t.Errorf("zero-count op must not render:\n%s", out)
	}
}

func TestObserveIdempotencyEmptyRegistry(t *testing.T) {
	r := metrics.NewRegistry()
	var b strings.Builder
	r.WriteText(&b)
	if strings.Contains(b.String(), "aeroid_idempotency_total") {
		t.Errorf("idempotency series must not render with zero counts:\n%s", b.String())
	}
}
