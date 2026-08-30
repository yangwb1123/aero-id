package outbox

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const markFailedSQL = `
WITH candidate AS MATERIALIZED (
    SELECT id,
           CASE WHEN retry_count >= 2147483647
                THEN 2147483647
                ELSE retry_count + 1
           END AS new_retry_count
    FROM outbox_events
    WHERE id = ?
      AND claim_token = ?
      AND claimed_at >= statement_timestamp() - ?::interval
)
UPDATE outbox_events AS o
SET retry_count = c.new_retry_count,
    last_retry_at = statement_timestamp(),
    dead_letter = (c.new_retry_count >= ?),
    next_attempt_at = CASE
        WHEN c.new_retry_count >= ? THEN statement_timestamp()
        ELSE statement_timestamp() + CASE
            WHEN c.new_retry_count <= 0 THEN interval '0 seconds'
            WHEN c.new_retry_count = 1 THEN interval '5 seconds'
            WHEN c.new_retry_count = 2 THEN interval '10 seconds'
            WHEN c.new_retry_count = 3 THEN interval '20 seconds'
            WHEN c.new_retry_count = 4 THEN interval '40 seconds'
            WHEN c.new_retry_count = 5 THEN interval '80 seconds'
            WHEN c.new_retry_count = 6 THEN interval '160 seconds'
            WHEN c.new_retry_count = 7 THEN interval '320 seconds'
            WHEN c.new_retry_count = 8 THEN interval '640 seconds'
            WHEN c.new_retry_count = 9 THEN interval '1280 seconds'
            WHEN c.new_retry_count = 10 THEN interval '2560 seconds'
            ELSE interval '1 hour'
        END
    END,
    claim_token = NULL,
    claimed_at = NULL
FROM candidate AS c
WHERE o.id = c.id
  AND o.claim_token = ?
  AND o.claimed_at >= statement_timestamp() - ?::interval`

// defaultClaimLease is how long a claimed event stays invisible to other
// claimers after its claim. Sized per D12: lease > renewEvery (10s) + longest
// single handler call (webhook timeout) + finalize margin.
const defaultClaimLease = 30 * time.Second

// Option configures a GormRepository.
type Option func(*GormRepository)

// WithClaimLease overrides the claim lease duration (default 30s). The lease
// must exceed the relay's renewal cadence plus the longest single handler
// call plus finalize margin; non-positive leases fall back to the default.
func WithClaimLease(d time.Duration) Option {
	return func(r *GormRepository) {
		if d > 0 {
			r.claimLease = d
		}
	}
}

// leaseInterval formats a duration as a Postgres interval literal.
func (r *GormRepository) leaseInterval() string {
	return fmt.Sprintf("%s", r.claimLease)
}

// ClaimBatch atomically claims up to limit unpublished, non-dead-letter, due
// events, returning them oldest-first with a fresh durable claim on each row.
// Rows with an unexpired claim are invisible to all other ClaimBatch calls.
// Claims expire after the repository's lease; expired rows are re-claimable
// with a new token.
func (r *GormRepository) ClaimBatch(ctx context.Context, limit int) ([]*ClaimedEvent, error) {
	if limit <= 0 {
		limit = defaultBatchSize
	}
	var rows []eventRow
	if err := r.db.WithContext(ctx).Raw(`
UPDATE outbox_events
SET claim_token = gen_random_uuid(),
    claimed_at  = statement_timestamp()
WHERE id IN (
    SELECT id
    FROM outbox_events
    WHERE published_at IS NULL
      AND NOT dead_letter
      AND next_attempt_at <= statement_timestamp()
      AND (claimed_at IS NULL OR claimed_at < statement_timestamp() - ?::interval)
    ORDER BY created_at ASC, id ASC
    LIMIT ?
    FOR UPDATE SKIP LOCKED
)
RETURNING id, claim_token, claimed_at,
          aggregate_type, aggregate_id, event_type, payload,
          created_at, published_at, retry_count, last_retry_at, next_attempt_at,
          dead_letter`,
		r.leaseInterval(), limit).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("claim batch: %w", err)
	}
	out := make([]*ClaimedEvent, len(rows))
	for i := range rows {
		out[i] = &ClaimedEvent{
			Event:     rowToEvent(&rows[i]),
			Claim:     Claim{EventID: rows[i].ID, Token: *rows[i].ClaimToken},
			ClaimedAt: *rows[i].ClaimedAt,
		}
	}
	// UPDATE ... RETURNING does not guarantee row order (the IN-subquery
	// ordering is lost), so re-sort to the documented oldest-first contract
	// the relay depends on for its backlog metric and backstop bound.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Event.CreatedAt.Equal(out[j].Event.CreatedAt) {
			return out[i].Event.ID.String() < out[j].Event.ID.String()
		}
		return out[i].Event.CreatedAt.Before(out[j].Event.CreatedAt)
	})
	return out, nil
}

// MarkPublished stamps published_at only on claims the caller still holds
// (matching token, unexpired). Valid claims are applied; if any claim is
// missing, foreign, or expired the returned error wraps ErrClaimLost.
func (r *GormRepository) MarkPublished(ctx context.Context, claims []Claim) error {
	if len(claims) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`UPDATE outbox_events o
SET published_at   = statement_timestamp(),
    next_attempt_at = statement_timestamp(),
    claim_token     = NULL,
    claimed_at      = NULL
FROM (VALUES `)
	args := make([]any, 0, len(claims)*2+1)
	for i, c := range claims {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(?::uuid, ?::uuid)")
		args = append(args, c.EventID, c.Token)
	}
	sb.WriteString(`) AS c(id, token)
WHERE o.id = c.id
  AND o.claim_token = c.token
  AND o.claimed_at  >= statement_timestamp() - ?::interval
  AND o.published_at IS NULL`)
	args = append(args, r.leaseInterval())

	// A batch finalization is all-or-nothing. Without the transaction, a
	// mixed batch can publish valid claims and then return ErrClaimLost for a
	// stale claim, making both metrics and the caller's result ambiguous.
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(sb.String(), args...)
		if res.Error != nil {
			return fmt.Errorf("mark published: %w", res.Error)
		}
		if res.RowsAffected < int64(len(claims)) {
			lost := int64(len(claims)) - res.RowsAffected
			return fmt.Errorf("mark published: %w (%d of %d claims stale)", ErrClaimLost, lost, len(claims))
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// MarkFailed increments retry_count, records last_retry_at, and schedules
// next_attempt_at only while the caller holds the claim; when the new
// retry_count reaches maxRetries the event becomes a dead letter. Stale claims
// are a no-op returning ErrClaimLost. The write is relative
// (retry_count = retry_count + 1);
// retryCount is used for relay logging and must equal the current
// retry_count + 1 when the claim is held.
//
//nolint:funlen // the fenced SQL keeps retry state and due time atomic.
func (r *GormRepository) MarkFailed(ctx context.Context, c Claim, retryCount, maxRetries int, _ time.Time) error {
	// retryCount is intentionally not used for the state transition. The
	// persisted value is incremented under the claim fence so a stale caller
	// cannot overwrite a newer attempt. The retained argument is only for API
	// compatibility with older Repository implementations.
	res := r.db.WithContext(ctx).Exec(markFailedSQL,
		c.EventID, c.Token, r.leaseInterval(), maxRetries, maxRetries,
		c.Token, r.leaseInterval())
	if res.Error != nil {
		return fmt.Errorf("mark failed: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("mark failed: %w", ErrClaimLost)
	}
	return nil
}

// Release clears the claim on rows the caller holds without changing retry
// state. It remains for interface compatibility; normal relay delivery does
// not call it now that ClaimBatch applies database-side eligibility.
func (r *GormRepository) Release(ctx context.Context, claims []Claim) error {
	if len(claims) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`UPDATE outbox_events o
SET claim_token = NULL,
    claimed_at  = NULL
FROM (VALUES `)
	args := make([]any, 0, len(claims)*2)
	for i, c := range claims {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(?::uuid, ?::uuid)")
		args = append(args, c.EventID, c.Token)
	}
	sb.WriteString(`) AS c(id, token)
WHERE o.id = c.id
  AND o.claim_token = c.token`)
	res := r.db.WithContext(ctx).Exec(sb.String(), args...)
	if res.Error != nil {
		return fmt.Errorf("release claim: %w", res.Error)
	}
	return nil
}

// RenewClaims extends claimed_at to now() on claims the caller still holds
// and that remain unexpired, unpublished, and non-dead-letter. The
// conjunction is the same as finalization — (id, token) pair + lease — so
// renewal can never extend a superseded or expired claim. Stale claims are
// silently skipped (finalization reports them); DB errors propagate.
func (r *GormRepository) RenewClaims(ctx context.Context, claims []Claim) error {
	if len(claims) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`UPDATE outbox_events o
SET claimed_at = statement_timestamp()
FROM (VALUES `)
	args := make([]any, 0, len(claims)*2+1)
	for i, c := range claims {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("(?::uuid, ?::uuid)")
		args = append(args, c.EventID, c.Token)
	}
	sb.WriteString(`) AS c(id, token)
WHERE o.id = c.id
  AND o.claim_token = c.token
  AND o.claimed_at  >= statement_timestamp() - ?::interval
  AND o.published_at IS NULL
  AND NOT o.dead_letter`)
	args = append(args, r.leaseInterval())

	if err := r.db.WithContext(ctx).Exec(sb.String(), args...).Error; err != nil {
		return fmt.Errorf("renew claims: %w", err)
	}
	return nil
}
