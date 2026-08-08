package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// eventRow is the GORM-mapped representation of the outbox_events table.
// ClaimToken/ClaimedAt are pointers: zero values must map to NULL (D9) — a
// plain uuid.UUID/time.Time would insert all-zeros instead of NULL on Append.
type eventRow struct {
	ID            uuid.UUID      `gorm:"column:id;primaryKey"`
	AggregateType string         `gorm:"column:aggregate_type"`
	AggregateID   string         `gorm:"column:aggregate_id"`
	EventType     string         `gorm:"column:event_type"`
	Payload       datatypes.JSON `gorm:"column:payload"`
	CreatedAt     time.Time      `gorm:"column:created_at"`
	PublishedAt   *time.Time     `gorm:"column:published_at"`
	RetryCount    int            `gorm:"column:retry_count"`
	LastRetryAt   *time.Time     `gorm:"column:last_retry_at"`
	DeadLetter    bool           `gorm:"column:dead_letter"`
	ClaimToken    *uuid.UUID     `gorm:"column:claim_token"`
	ClaimedAt     *time.Time     `gorm:"column:claimed_at"`
}

func (eventRow) TableName() string { return "outbox_events" }

func rowToEvent(r *eventRow) *Event {
	return &Event{
		ID:            r.ID,
		AggregateType: r.AggregateType,
		AggregateID:   r.AggregateID,
		EventType:     r.EventType,
		Payload:       []byte(r.Payload),
		CreatedAt:     r.CreatedAt,
		PublishedAt:   r.PublishedAt,
		RetryCount:    r.RetryCount,
		LastRetryAt:   r.LastRetryAt,
		DeadLetter:    r.DeadLetter,
	}
}

// outboxPurgeInterval is the pause between purge batches to spread IO load.
const outboxPurgeInterval = 100 * time.Millisecond

// GormRepository is the Postgres-backed Repository.
type GormRepository struct {
	db         *gorm.DB
	claimLease time.Duration
}

func NewGormRepository(db *gorm.DB, opts ...Option) *GormRepository {
	r := &GormRepository{db: db, claimLease: defaultClaimLease}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Append inserts the event using the caller's transaction so it shares the
// fate of the domain rows written in that same tx.
func (r *GormRepository) Append(ctx context.Context, tx *gorm.DB, e *Event) error {
	row := &eventRow{
		ID:            e.ID,
		AggregateType: e.AggregateType,
		AggregateID:   e.AggregateID,
		EventType:     e.EventType,
		Payload:       datatypes.JSON(e.Payload),
		CreatedAt:     e.CreatedAt,
		PublishedAt:   e.PublishedAt,
	}
	if err := tx.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("append outbox event: %w", err)
	}
	return nil
}

// PurgeDeadLetters deletes outbox events that are either dead letters created
// before cutoff, or published events created before publishedCutoff. Deletion
// is done in batches of up to batchSize (when <= 0, 500 is used) with a 100ms
// pause between each batch to limit DB IO impact. Returns the total number of
// rows deleted.
func (r *GormRepository) PurgeDeadLetters(ctx context.Context, cutoff time.Time, publishedCutoff time.Time, batchSize int) (int64, error) {
	if batchSize <= 0 {
		batchSize = 500
	}
	var total int64
	for {
		// Purge dead letters past cutoff. The outer dead_letter qual is
		// re-checked by EPQ against the new row version, so a row concurrently
		// flipped out of dead_letter (a future requeue surface) survives the
		// DELETE (mirror the delivery-leg guard in callback_audit.go).
		sub := r.db.WithContext(ctx).Model(&eventRow{}).
			Where("dead_letter = true AND created_at < ?", cutoff).
			Order("created_at ASC, id ASC").
			Select("id").
			Limit(batchSize)
		res := r.db.WithContext(ctx).
			Where("id IN (?) AND dead_letter = true", sub).
			Delete(&eventRow{})
		if res.Error != nil {
			return total, fmt.Errorf("purge dead letters: %w", res.Error)
		}
		total += res.RowsAffected

		// Also purge published events past publishedCutoff.
		sub2 := r.db.WithContext(ctx).Model(&eventRow{}).
			Where("published_at IS NOT NULL AND created_at < ?", publishedCutoff).
			Order("created_at ASC, id ASC").
			Select("id").
			Limit(batchSize)
		res2 := r.db.WithContext(ctx).
			Where("id IN (?) AND published_at IS NOT NULL", sub2).
			Delete(&eventRow{})
		if res2.Error != nil {
			return total, fmt.Errorf("purge published events: %w", res2.Error)
		}
		total += res2.RowsAffected

		if res.RowsAffected == 0 && res2.RowsAffected == 0 {
			break
		}
		time.Sleep(outboxPurgeInterval)
	}
	return total, nil
}

// outboxCleaner carries the retention bounds and the pause flag; tickOnce and
// runCleanerLoop are extracted with injected clock/tick sources so scheduling
// tests drive them deterministically (T-5.2) without wall-clock sleeps.
type outboxCleaner struct {
	repo              Repository
	deadLetterRetention time.Duration
	publishedRetention  time.Duration
	purgePaused         bool
}

// tickOnce performs one purge pass with an injected now: cutoffs are derived
// from now (never a wall clock inside the function) and, when paused, returns
// 0,nil WITHOUT touching the repository (no SELECT, no DELETE).
func tickOnce(ctx context.Context, c *outboxCleaner, now time.Time) (int64, error) {
	if c.purgePaused {
		return 0, nil
	}
	deadCutoff := now.Add(-c.deadLetterRetention)
	pubCutoff := now.Add(-c.publishedRetention)
	return c.repo.PurgeDeadLetters(ctx, deadCutoff, pubCutoff, 0)
}

// runCleanerLoop drives purge ticks from an injected tick channel; the caller
// cancels ctx to stop. Logging lives here so tickOnce stays pure.
func runCleanerLoop(ctx context.Context, ticks <-chan time.Time, log *zap.Logger, c *outboxCleaner) {
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			deleted, err := tickOnce(ctx, c, now.UTC())
			if err != nil {
				log.Warn("outbox purge failed", zap.Error(err))
			} else if deleted > 0 {
				log.Info("outbox purge complete",
					zap.Int64("deleted", deleted),
					zap.Time("dead_letter_cutoff", now.Add(-c.deadLetterRetention)),
					zap.Time("published_cutoff", now.Add(-c.publishedRetention)))
			}
		}
	}
}

// RunOutboxCleaner starts a background goroutine that periodically purges old
// outbox event rows. Dead letters older than deadLetterRetention and published
// events older than publishedRetention are deleted. Runs on the given interval.
// purgePaused suspends all purge work (no SELECT, no DELETE) so dead-letter
// replay cannot race a purge. Returns a stop function. A non-positive interval
// disables the cleaner.
func RunOutboxCleaner(repo Repository, log *zap.Logger, deadLetterRetention, publishedRetention, interval time.Duration, purgePaused bool) func() {
	if interval <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := time.NewTicker(interval)
	go func() {
		runCleanerLoop(ctx, t.C, log, &outboxCleaner{
			repo: repo, deadLetterRetention: deadLetterRetention,
			publishedRetention: publishedRetention, purgePaused: purgePaused,
		})
		t.Stop()
	}()
	return func() { cancel() }
}
