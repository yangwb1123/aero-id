package middleware

import "github.com/aero/aero-id/internal/pkg/metrics"

// metricsIdempotencyObserver adapts the metrics Registry to IdempotencyObserver.
// The error value is deliberately dropped: the metric is op-labelled only,
// keeping label cardinality bounded.
type metricsIdempotencyObserver struct{ reg *metrics.Registry }

func (o metricsIdempotencyObserver) CompleteFailed(error) {
	o.reg.ObserveIdempotency("complete_failed")
}

func (o metricsIdempotencyObserver) ReleaseFailed(error) {
	o.reg.ObserveIdempotency("release_failed")
}

func (o metricsIdempotencyObserver) Overflowed() {
	o.reg.ObserveIdempotency("capture_overflow")
}

// NewMetricsIdempotencyObserver returns an IdempotencyObserver that counts
// middleware failures and degrades in reg. reg must not be nil.
func NewMetricsIdempotencyObserver(reg *metrics.Registry) IdempotencyObserver {
	return metricsIdempotencyObserver{reg: reg}
}
