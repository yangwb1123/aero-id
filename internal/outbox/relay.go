package outbox

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// defaultBatchSize bounds how many events a single drain pass handles.
const defaultBatchSize = 100

// DefaultMaxRetries is the fallback when config specifies zero or negative.
const DefaultMaxRetries = 20

// maxBackoff caps the per-event retry delay so a persistently failing event
// does not stay queued forever; after 20 attempts at 5s base (2^19 ≈ 4.3h)
// the cap at 1h kicks in. Beyond maxRetries the event becomes a dead letter.
const maxBackoff = time.Hour

// backoff returns how long to wait before retrying an event that has failed
// n times (n > 0). Formula: min(base * 2^(n-1), maxBackoff).
func backoff(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	// 2^(n-1), guarded by float64 precision cap at 1024 (n=11) which maps to
	// ~85min — well past the 1h cap, so the LEAST never overflows.
	exp := math.Pow(2, float64(n-1))
	d := time.Duration(float64(5*time.Second) * exp)
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

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
// handler errors is left unpublished, its retry_count incremented and a
// backoff applied; after maxRetries failures it becomes a dead letter and is
// no longer picked up by the relay.
type Relay struct {
	repo       Repository
	handler    Handler
	log        *zap.Logger
	batch      int
	maxRetries int
	metrics    MetricsSink // nil-safe: observations are skipped when unset
	now        func() time.Time
}

// NewRelay builds a Relay draining repo through handler. maxRetries is the
// per-event retry ceiling before dead-lettering; 0 or negative defaults to
// DefaultMaxRetries. log may be nil. metrics is optional (variadic) so
// existing callers/tests keep working; only the first sink is used.
func NewRelay(repo Repository, handler Handler, maxRetries int, log *zap.Logger, metrics ...MetricsSink) *Relay {
	if log == nil {
		log = zap.NewNop()
	}
	if maxRetries <= 0 {
		maxRetries = DefaultMaxRetries
	}
	r := &Relay{repo: repo, handler: handler, log: log, batch: defaultBatchSize, maxRetries: maxRetries, now: time.Now}
	if len(metrics) > 0 {
		r.metrics = metrics[0]
	}
	return r
}

// drain handles one batch of unpublished events, returning the number handled.
// Per-event handler failures are logged, the event's retry_count is
// incremented with a backoff delay applied; after maxRetries failures the
// event becomes a dead letter. Events still inside their backoff window are
// skipped (not handled, not failed-forward).
// After every drain pass (including empty ones) a single aggregated summary line
// is logged at info level: "outbox drain: handled=X failed=Y pending=Z oldest_age=... duration=...".
//
//nolint:cyclop,funlen // retry, backoff, and dead-letter transitions are one durable outbox state machine.
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
		r.log.Info("outbox drain",
			zap.Int("pending", 0),
			zap.Duration("duration", r.now().Sub(start)),
		)
		return 0
	}
	published := make([]uuid.UUID, 0, len(events))
	var failed int
	now := r.now()
	for _, e := range events {
		// Skip events still in their backoff window.
		if e.LastRetryAt != nil {
			next := e.LastRetryAt.Add(backoff(e.RetryCount))
			if now.Before(next) {
				continue
			}
		}
		if err := r.handler.Handle(ctx, e); err != nil {
			r.log.Warn("outbox: handler failed; will retry",
				zap.String("event_id", e.ID.String()),
				zap.String("event_type", e.EventType),
				zap.Int("retry_count", e.RetryCount+1),
				zap.Int("max_retries", r.maxRetries),
				zap.Error(err))
			if r.metrics != nil {
				r.metrics.ObserveOutboxFailed(e.EventType)
			}
			// Record the failure so the backoff is tracked; if this pushes
			// retry_count past maxRetries the event becomes a dead letter.
			if err := r.repo.MarkFailed(ctx, e.ID, e.RetryCount+1, r.maxRetries, now); err != nil {
				r.log.Warn("outbox: mark failed failed", zap.String("event_id", e.ID.String()), zap.Error(err))
			}
			failed++
			continue
		}
		published = append(published, e.ID)
		if r.metrics != nil {
			r.metrics.ObserveOutboxPublished(e.EventType)
		}
	}
	r.observeDrain(len(events), oldestAge, start)
	dur := r.now().Sub(start)
	// Aggregate summary: one info line replacing dozens of per-event logs.
	// Individual handler failures are still logged at warn level above; this line
	// gives operators a quick pulse on outbox health without the noise.
	r.log.Info("outbox drain",
		zap.Int("handled", len(published)),
		zap.Int("failed", failed),
		zap.Int("pending", len(events)),
		zap.Duration("oldest_age", oldestAge),
		zap.Duration("duration", dur),
	)
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
				r.drain(context.Background())
				return
			}
		}
	}()
	return func() { close(done) }
}
