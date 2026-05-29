// Package outbox implements the transactional outbox pattern: domain
// side-effects are written as events in the SAME database transaction as the
// data that produced them, then drained asynchronously by a Relay. This makes
// downstream effects (counters, notifications) reliable instead of best-effort
// in the request path.
package outbox

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Event is a domain side-effect recorded for asynchronous delivery. ID is a
// UUIDv7 primary key (so events sort oldest-first). Payload is opaque JSON.
type Event struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   string
	EventType     string
	Payload       []byte
	CreatedAt     time.Time
	PublishedAt   *time.Time
}

// Repository persists and drains outbox events.
type Repository interface {
	// Append writes an event using the caller-supplied transaction so it commits
	// (or rolls back) together with the domain rows in the same tx.
	Append(ctx context.Context, tx *gorm.DB, e *Event) error
	// FetchUnpublished returns up to limit unpublished events, oldest-first.
	FetchUnpublished(ctx context.Context, limit int) ([]*Event, error)
	// MarkPublished stamps published_at on the given event IDs.
	MarkPublished(ctx context.Context, ids []uuid.UUID) error
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
