package outbox

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// defaultBatchSize bounds how many events a single drain pass handles.
const defaultBatchSize = 100

// MetricsSink receives outbox-relay observability. *metrics.Registry satisfies it;
// the relay treats a nil sink as a no-op so existing NewRelay callers (and tests)
// need not provide one.
type MetricsSink interface {
	// ObserveOutboxPublished records one successfully published event by event_type.
	ObserveOutboxPublished(eventType string)
	// ObserveOutboxFailed records one event whose handler errored (left for retry).
	ObserveOutboxFailed(eventType string)
	// ObserveOutboxDrain records one drain pass: pending is the unpublished events
	// the pass observed, oldestAge is the age of the oldest such event (0 when the
	// outbox was empty), and dur is the pass duration.
	ObserveOutboxDrain(pending int, oldestAge, dur time.Duration)
}

// Relay periodically polls unpublished events and dispatches each to a Handler,
// marking only the successfully handled events published. An event whose
// handler errors is left unpublished and retried on the next tick.
type Relay struct {
	repo    Repository
	handler Handler
	log     *zap.Logger
	batch   int
	metrics MetricsSink // nil-safe: observations are skipped when unset
	now     func() time.Time
}

// NewRelay builds a Relay draining repo through handler. log may be nil. metrics
// is optional (variadic) so existing callers/tests keep working; only the first
// sink is used.
func NewRelay(repo Repository, handler Handler, log *zap.Logger, metrics ...MetricsSink) *Relay {
	if log == nil {
		log = zap.NewNop()
	}
	r := &Relay{repo: repo, handler: handler, log: log, batch: defaultBatchSize, now: time.Now}
	if len(metrics) > 0 {
		r.metrics = metrics[0]
	}
	return r
}

// drain handles one batch of unpublished events, returning the number handled.
// Per-event handler failures are logged and the event is skipped (retried next
// tick); only successfully handled events are marked published.
func (r *Relay) drain(ctx context.Context) int {
	start := r.now()
	events, err := r.repo.FetchUnpublished(ctx, r.batch)
	if err != nil {
		r.log.Warn("outbox: fetch unpublished failed", zap.Error(err))
		return 0
	}
	// Observe backlog from the batch: events are oldest-first, so events[0] is the
	// oldest unpublished event and len(events) is the pending count this pass saw.
	// This runs even on an empty outbox so the gauges decay back to zero.
	var oldestAge time.Duration
	if len(events) > 0 && !events[0].CreatedAt.IsZero() {
		oldestAge = start.Sub(events[0].CreatedAt)
	}
	if len(events) == 0 {
		r.observeDrain(0, 0, start)
		return 0
	}
	published := make([]uuid.UUID, 0, len(events))
	for _, e := range events {
		if err := r.handler.Handle(ctx, e); err != nil {
			r.log.Warn("outbox: handler failed; will retry",
				zap.String("event_id", e.ID.String()),
				zap.String("event_type", e.EventType),
				zap.Error(err))
			if r.metrics != nil {
				r.metrics.ObserveOutboxFailed(e.EventType)
			}
			continue
		}
		published = append(published, e.ID)
		if r.metrics != nil {
			r.metrics.ObserveOutboxPublished(e.EventType)
		}
	}
	r.observeDrain(len(events), oldestAge, start)
	if err := r.repo.MarkPublished(ctx, published); err != nil {
		r.log.Warn("outbox: mark published failed; will retry", zap.Error(err))
		return 0
	}
	return len(published)
}

// observeDrain reports the per-pass backlog/lag/duration when a sink is configured.
func (r *Relay) observeDrain(pending int, oldestAge time.Duration, start time.Time) {
	if r.metrics != nil {
		r.metrics.ObserveOutboxDrain(pending, oldestAge, r.now().Sub(start))
	}
}

// Start launches a background goroutine that drains the outbox every interval.
// It returns a stop func; call it to terminate the goroutine (mirrors
// TokenCache.RunCleaner).
func (r *Relay) Start(interval time.Duration) (stop func()) {
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		ctx := context.Background()
		for {
			select {
			case <-t.C:
				r.drain(ctx)
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}
