// Package outbox implements the transactional outbox pattern: domain
// side-effects are written as events in the SAME database transaction as the
// data that produced them, then drained asynchronously by a Relay. This makes
// downstream effects (counters, notifications) reliable instead of best-effort
// in the request path.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
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

// Claim identifies one event currently held by a relay instance. Finalization
// (MarkPublished/MarkFailed) and Release apply only while the claim is held
// and unexpired; Token is minted per claim and is never reusable.
type Claim struct {
	EventID uuid.UUID
	Token   uuid.UUID
}

// ClaimedEvent is an Event plus the durable claim under which it was fetched.
// The relay passes e.Event to the Handler and keeps the Claim for finalization.
type ClaimedEvent struct {
	Event     *Event
	Claim     Claim
	ClaimedAt time.Time // DB clock at claim time (observability)
}

// ErrClaimLost is returned by MarkPublished/MarkFailed when the caller no
// longer holds an unexpired claim for an event (lost to lease expiry, a newer
// re-claim, or never held). It is a normal at-least-once condition, not an
// infrastructure error: the event will be re-claimed and re-delivered.
var ErrClaimLost = errors.New("outbox: claim lost or expired")

// Repository persists and drains outbox events.
type Repository interface {
	// Append writes an event using the caller-supplied transaction so it commits
	// (or rolls back) together with the domain rows in the same tx.
	Append(ctx context.Context, tx *gorm.DB, e *Event) error

	// ClaimBatch atomically claims up to limit unpublished, non-dead-letter
	// events (backoff eligibility is applied by the relay after claiming),
	// returning them oldest-first with a fresh durable claim on each row. Rows
	// with an unexpired claim are invisible to all other ClaimBatch calls.
	// Claims expire after the repository's lease; expired rows are re-claimable
	// with a new token.
	ClaimBatch(ctx context.Context, limit int) ([]*ClaimedEvent, error)

	// MarkPublished stamps published_at only on claims the caller still holds
	// (matching token, unexpired). Valid claims are applied; if any claim is
	// missing, foreign, or expired the returned error wraps ErrClaimLost.
	MarkPublished(ctx context.Context, claims []Claim) error

	// MarkFailed increments retry_count and sets last_retry_at only while the
	// caller holds the claim; when the new retry_count reaches maxRetries the
	// event becomes a dead letter. Stale claims are a no-op returning
	// ErrClaimLost. The write is relative (retry_count = retry_count + 1);
	// retryCount is used for relay logging and must equal the current
	// retry_count + 1 when the claim is held.
	MarkFailed(ctx context.Context, c Claim, retryCount, maxRetries int, at time.Time) error

	// Release clears the claim on rows the caller holds without changing retry
	// state. Used for events skipped by the relay's Go backoff check (the
	// primary backoff exclusion). Stale claims are a no-op.
	Release(ctx context.Context, claims []Claim) error

	// RenewClaims extends claimed_at to now() on claims the caller still holds
	// and that remain unexpired, unpublished, and non-dead-letter. The
	// conjunction is the same as finalization — (id, token) pair + lease — so
	// renewal can never extend a superseded or expired claim. Stale claims are
	// silently skipped (finalization reports them); DB errors propagate. The
	// relay uses this to keep batch claims alive across long handler chains.
	RenewClaims(ctx context.Context, claims []Claim) error

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
