package metrics

import (
	"fmt"
	"io"
)

// idempotencyMetrics counts idempotency middleware failures and degrades,
// keyed by op. It reuses the counterMap primitive from control_plane.go:
// add only takes the counterMap mutex (never r.mu) and snapshot is only
// called while holding r.mu, so there is no lock-ordering hazard.
type idempotencyMetrics struct{ counts counterMap }

// ObserveIdempotency records one idempotency middleware failure or degrade per
// op: "complete_failed" (store.Complete error on the success path),
// "release_failed" (store.Release error on the 5xx or overflow path), or
// "capture_overflow" (response capture exceeded maxIdempotencyCaptureBytes).
func (r *Registry) ObserveIdempotency(op string) {
	r.idem.counts.add(op, 1)
}

// writeIdempotencyText renders the idempotency middleware series. Caller holds
// r.mu. Zero-count ops are not rendered (conditional, like the outbox series).
func (r *Registry) writeIdempotencyText(w io.Writer) {
	counts := r.idem.counts.snapshot()
	if len(counts) == 0 {
		return
	}
	fmt.Fprintln(w, "# HELP aeroid_idempotency_total Idempotency middleware failures and degrades by op.")
	fmt.Fprintln(w, "# TYPE aeroid_idempotency_total counter")
	for _, op := range sortedKeys(counts) {
		fmt.Fprintf(w, "aeroid_idempotency_total{op=%q} %d\n", op, counts[op])
	}
}
