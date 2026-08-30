package outbox

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

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
	// ObserveOutboxDrain records one drain pass. pending is the eligible
	// unpublished backlog, claimed is this batch size, and oldestAge is the age
	// of the oldest eligible event. When eligibilityKnown is false, pending and
	// oldestAge are unavailable and must not overwrite prior values.
	ObserveOutboxDrain(eligiblePending int, claimed int, oldestEligibleAge time.Duration, duration time.Duration, eligibilityKnown bool)
}

// outboxOperationalMetrics is optional to preserve the small MetricsSink
// contract for existing embedders. The production registry implements it;
// relay tests can inject a sink that only observes the required drain series.
type outboxOperationalMetrics interface {
	ObserveOutboxClaimError()
	ObserveOutboxClaimLost()
	ObserveOutboxFinalizationError()
	ObserveOutboxRenewalError()
	ObserveOutboxDeadLetter(eventType string)
}

// defaultRenewEvery is how often the relay extends the claims of a pending
// batch while it is still handling it (D12). Sized so the default lease
// (30s) comfortably exceeds cadence + longest single handler call + margin.
const defaultRenewEvery = 10 * time.Second

// Relay periodically polls unpublished events and dispatches each to a Handler,
// marking events published only after durable finalization succeeds. An event whose
// handler errors is left unpublished, its retry_count incremented and a
// backoff applied; after maxRetries failures it becomes a dead letter and is
// no longer picked up by the relay.
//
// Delivery is at-least-once: each drain pass claims a batch with a durable
// token (ClaimBatch), holds the claim across the handler window (renewing it
// on long drains), and finalizes conditionally on still holding an unexpired
// claim. A claim lost to lease expiry or a crash means the event is simply
// re-claimed and re-delivered by the next pass.
type Relay struct {
	repo       Repository
	handler    Handler
	log        *zap.Logger
	batch      int
	maxRetries int
	metrics    MetricsSink // nil-safe: observations are skipped when unset
	now        func() time.Time
	renewEvery time.Duration
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
	r := &Relay{repo: repo, handler: handler, log: log, batch: defaultBatchSize, maxRetries: maxRetries, now: time.Now, renewEvery: defaultRenewEvery}
	if len(metrics) > 0 {
		r.metrics = metrics[0]
	}
	return r
}

// handle invokes the handler, converting a handler panic into a handler error
// so the poison event follows the MarkFailed/dead-letter path instead of
// crashing the drain goroutine (crash-loop hardening). Infrastructure panics
// (repo calls, renewal) are intentionally not recovered.
func (r *Relay) handle(ctx context.Context, e *Event) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("outbox: handler panic; treating as failure",
				zap.String("event_id", e.ID.String()),
				zap.String("event_type", e.EventType),
				zap.Any("panic", rec),
				zap.Stack("stack"))
			err = fmt.Errorf("outbox: handler panic: %v", rec)
		}
	}()
	return r.handler.Handle(ctx, e)
}

// drain handles one batch of unpublished events, returning the number handled.
// Per-event handler failures are logged, the event's retry_count is
// incremented with a backoff delay applied; after maxRetries failures the
// event becomes a dead letter. ClaimBatch excludes events until their database-
// owned due time, so the relay never claims or releases a future retry.
//
// The batch is claimed with a durable token (ClaimBatch); long drains renew
// the claim every renewEvery so a live handler never loses its claim (D12);
// finalization is conditional on still holding an unexpired claim, so a crash
// between claim and finalize simply re-delivers after lease expiry.
// After every drain pass (including empty ones) a single aggregated summary line
// is logged at info level: "outbox drain: handled=X failed=Y pending=Z oldest_age=... duration=...".
//
//nolint:cyclop,funlen // retry, backoff, claim, and dead-letter transitions are one durable outbox state machine.
func (r *Relay) drain(ctx context.Context) int {
	start := r.now()
	stats, statsErr := r.repo.GetEligibilityStats(ctx)
	eligibilityKnown := statsErr == nil
	if statsErr != nil {
		r.log.Warn("outbox: eligibility stats failed; continuing delivery", zap.Error(statsErr))
	}
	claimed, err := r.repo.ClaimBatch(ctx, r.batch)
	if err != nil {
		r.observeClaimError()
		// A failed claim means this pass did not establish a trustworthy
		// observation, even if the preceding stats query succeeded. Preserve
		// the last known backlog and mark the current state unknown.
		r.observeDrain(0, 0, 0, start, false)
		r.logDrain(0, 0, 0, 0, 0, r.now().Sub(start), false)
		r.log.Warn("outbox: claim batch failed", zap.Error(err))
		return 0
	}
	var oldestAge time.Duration
	if eligibilityKnown && stats.OldestCreatedAt != nil {
		observedAt := start
		if !stats.ObservedAt.IsZero() {
			observedAt = stats.ObservedAt
		}
		oldestAge = observedAt.Sub(*stats.OldestCreatedAt)
		if oldestAge < 0 {
			oldestAge = 0
		}
	}
	if len(claimed) == 0 {
		r.observeDrain(stats.Pending, 0, oldestAge, start, eligibilityKnown)
		r.logDrain(stats.Pending, 0, 0, 0, oldestAge, r.now().Sub(start), eligibilityKnown)
		return 0
	}
	published := make([]Claim, 0, len(claimed))
	var failed int
	now := r.now()
	lastRenew := now
	// held is every claim of this drain that is still ours and not yet cleared
	// by MarkFailed. Renewal extends the
	// whole held set — including queued and in-flight claims — so the lease
	// sizing rule (lease > renewEvery + single call + margin) holds for every
	// batch position, not just the head (a claim minted at batch start ages
	// while earlier events are handled; renewing only already-handled claims
	// lets the tail expire mid-handling).
	held := make([]Claim, 0, len(claimed))
	for _, ce := range claimed {
		held = append(held, ce.Claim)
	}
	dropHeld := func(c Claim) {
		for i, h := range held {
			if h == c {
				held = append(held[:i], held[i+1:]...)
				return
			}
		}
	}
	for _, ce := range claimed {
		e := ce.Event
		// Refresh the clock each iteration so renewal cadence and failure
		// timestamps observe elapsed real/fake time across a long drain.
		now = r.now()
		// Renewal (D12): keep the batch's claims alive across long handler
		// chains. Runs before each handler call so the claim is fresh when the
		// next call starts; DB errors degrade to lease expiry (at-least-once).
		if len(held) > 0 && now.Sub(lastRenew) >= r.renewEvery {
			if err := r.repo.RenewClaims(ctx, held); err != nil {
				r.observeRenewalError()
				r.log.Warn("outbox: renew claims failed; lease expiry will re-deliver", zap.Error(err))
			}
			lastRenew = now
		}
		if err := r.handle(ctx, e); err != nil {
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
			// The failed claim is cleared by MarkFailed and drops out of the
			// renewal set (F4).
			if err := r.repo.MarkFailed(ctx, ce.Claim, e.RetryCount+1, r.maxRetries, now); err != nil {
				r.observeFinalizationError(err)
				if errors.Is(err, ErrClaimLost) {
					r.log.Warn("outbox: claim lost on mark failed; event will be re-claimed after lease expiry",
						zap.String("event_id", e.ID.String()), zap.Error(err))
				} else {
					r.log.Warn("outbox: mark failed failed", zap.String("event_id", e.ID.String()), zap.Error(err))
				}
			} else if e.RetryCount+1 >= r.maxRetries {
				r.observeDeadLetter(e.EventType)
			}
			dropHeld(ce.Claim)
			failed++
			continue
		}
		published = append(published, ce.Claim)
	}
	if err := r.repo.MarkPublished(ctx, published); err != nil {
		r.observeFinalizationError(err)
		// Do not count handler success as publication: finalization may have
		// updated some rows before reporting a stale claim, so this conservative
		// metric never asserts durable publication without a clean result.
		r.observeDrain(stats.Pending, len(claimed), oldestAge, start, eligibilityKnown)
		r.logDrain(stats.Pending, len(claimed), len(published), failed, oldestAge, r.now().Sub(start), eligibilityKnown)
		if errors.Is(err, ErrClaimLost) {
			r.log.Warn("outbox: some claims lost on mark published; stale events will be re-claimed",
				zap.Int("handled", len(published)), zap.Error(err))
			return 0
		}
		r.log.Warn("outbox: mark published failed; will retry", zap.Error(err))
		return 0
	}
	for _, ce := range claimed {
		if r.metrics != nil && ce.Event.PublishedAt == nil {
			// Claims in published are exactly the successful handler set. A clean
			// batch finalization confirms their durable transition.
			if containsClaim(published, ce.Claim) {
				r.metrics.ObserveOutboxPublished(ce.Event.EventType)
			}
		}
	}
	r.observeDrain(stats.Pending, len(claimed), oldestAge, start, eligibilityKnown)
	r.logDrain(stats.Pending, len(claimed), len(published), failed, oldestAge, r.now().Sub(start), eligibilityKnown)
	return len(published)
}

func (r *Relay) operationalMetrics() (outboxOperationalMetrics, bool) {
	if r.metrics == nil {
		return nil, false
	}
	metrics, ok := r.metrics.(outboxOperationalMetrics)
	return metrics, ok
}

func (r *Relay) observeClaimError() {
	if metrics, ok := r.operationalMetrics(); ok {
		metrics.ObserveOutboxClaimError()
	}
}

func (r *Relay) observeFinalizationError(err error) {
	if metrics, ok := r.operationalMetrics(); ok {
		if errors.Is(err, ErrClaimLost) {
			metrics.ObserveOutboxClaimLost()
		} else {
			metrics.ObserveOutboxFinalizationError()
		}
	}
}

func (r *Relay) observeRenewalError() {
	if metrics, ok := r.operationalMetrics(); ok {
		metrics.ObserveOutboxRenewalError()
	}
}

func (r *Relay) observeDeadLetter(eventType string) {
	if metrics, ok := r.operationalMetrics(); ok {
		metrics.ObserveOutboxDeadLetter(eventType)
	}
}

// observeDrain reports the per-pass backlog/lag/duration when a sink is configured.
func (r *Relay) observeDrain(pending, claimed int, oldestAge time.Duration, start time.Time, eligibilityKnown bool) {
	if r.metrics != nil {
		r.metrics.ObserveOutboxDrain(pending, claimed, oldestAge, r.now().Sub(start), eligibilityKnown)
	}
}

func containsClaim(claims []Claim, want Claim) bool {
	for _, claim := range claims {
		if claim == want {
			return true
		}
	}
	return false
}

// logDrain emits a single summary without presenting fabricated pending/age
// values when eligibility statistics were unavailable.
func (r *Relay) logDrain(pending, claimed, handled, failed int, oldestAge, duration time.Duration, known bool) {
	fields := []zap.Field{
		zap.Int("handled", handled),
		zap.Int("failed", failed),
		zap.Int("claimed", claimed),
		zap.Bool("eligibility_known", known),
		zap.Duration("duration", duration),
	}
	if known {
		fields = append(fields, zap.Int("pending", pending), zap.Duration("oldest_age", oldestAge))
	}
	r.log.Info("outbox drain", fields...)
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
