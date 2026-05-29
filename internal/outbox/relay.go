package outbox

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// defaultBatchSize bounds how many events a single drain pass handles.
const defaultBatchSize = 100

// Relay periodically polls unpublished events and dispatches each to a Handler,
// marking only the successfully handled events published. An event whose
// handler errors is left unpublished and retried on the next tick.
type Relay struct {
	repo    Repository
	handler Handler
	log     *zap.Logger
	batch   int
}

// NewRelay builds a Relay draining repo through handler. log may be nil.
func NewRelay(repo Repository, handler Handler, log *zap.Logger) *Relay {
	if log == nil {
		log = zap.NewNop()
	}
	return &Relay{repo: repo, handler: handler, log: log, batch: defaultBatchSize}
}

// drain handles one batch of unpublished events, returning the number handled.
// Per-event handler failures are logged and the event is skipped (retried next
// tick); only successfully handled events are marked published.
func (r *Relay) drain(ctx context.Context) int {
	events, err := r.repo.FetchUnpublished(ctx, r.batch)
	if err != nil {
		r.log.Warn("outbox: fetch unpublished failed", zap.Error(err))
		return 0
	}
	if len(events) == 0 {
		return 0
	}
	published := make([]uuid.UUID, 0, len(events))
	for _, e := range events {
		if err := r.handler.Handle(ctx, e); err != nil {
			r.log.Warn("outbox: handler failed; will retry",
				zap.String("event_id", e.ID.String()),
				zap.String("event_type", e.EventType),
				zap.Error(err))
			continue
		}
		published = append(published, e.ID)
	}
	if err := r.repo.MarkPublished(ctx, published); err != nil {
		r.log.Warn("outbox: mark published failed; will retry", zap.Error(err))
		return 0
	}
	return len(published)
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
