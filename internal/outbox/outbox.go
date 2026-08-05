// Package outbox implements the transactional outbox pattern: domain
// side-effects are written as events in the SAME database transaction as the
// data that produced them, then drained asynchronously by a Relay. This makes
// downstream effects (counters, notifications) reliable instead of best-effort
// in the request path.
package outbox

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Event is a domain side-effect recorded for asynchronous delivery. ID is a
// UUIDv7 primary key (so events sort oldest-first). Payload is opaque JSON.
// RetryCount, LastRetryAt, and DeadLetter track handler-failure history for
// exponential backoff; the relay skips events whose next retry is not yet due
// and dead-letter events beyond the max-retries threshold.
type Event struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   string
	EventType     string
	Payload       []byte
	CreatedAt     time.Time
	PublishedAt   *time.Time
	RetryCount    int
	LastRetryAt   *time.Time
	DeadLetter    bool
}

// Repository persists and drains outbox events.
type Repository interface {
	// Append writes an event using the caller-supplied transaction so it commits
	// (or rolls back) together with the domain rows in the same tx.
	Append(ctx context.Context, tx *gorm.DB, e *Event) error
	// FetchUnpublished returns up to limit unpublished (and not dead-letter)
	// events that are eligible for retry — either never retried (last_retry_at
	// IS NULL) or whose backoff window has elapsed. Oldest-first.
	FetchUnpublished(ctx context.Context, limit int) ([]*Event, error)
	// MarkPublished stamps published_at on the given event IDs.
	MarkPublished(ctx context.Context, ids []uuid.UUID) error
	// MarkFailed increments retry_count, sets last_retry_at to now, and when
	// retryCount >= maxRetries marks the event as dead_letter.
	MarkFailed(ctx context.Context, id uuid.UUID, retryCount, maxRetries int, at time.Time) error
	// PurgeDeadLetters deletes dead-letter events created before cutoff, and
	// published events created before publishedCutoff, in batches. Returns the
	// total number of deleted rows.
	PurgeDeadLetters(ctx context.Context, cutoff time.Time, publishedCutoff time.Time, batchSize int) (int64, error)
}

// Handler dispatches a single event to a downstream sink (a log sink today;
// notifications plug in here later). Returning an error leaves the event
// unpublished so the relay retries it on the next tick.
type Handler interface {
	Handle(ctx context.Context, e *Event) error
}

// HandlerFunc adapts a function to the Handler interface.
type HandlerFunc func(ctx context.Context, e *Event) error

func (f HandlerFunc) Handle(ctx context.Context, e *Event) error { return f(ctx, e) }

// NewEvent creates an outbox Event from its component parts. Payload is marshaled
// to JSON; marshal failures are programming errors and panic.
func NewEvent(aggregateType, aggregateID, eventType string, payload any) *Event {
	data, err := json.Marshal(payload)
	if err != nil {
		panic("outbox: marshal event payload: " + err.Error())
	}
	return &Event{
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       data,
		CreatedAt:     time.Now().UTC(),
	}
}
