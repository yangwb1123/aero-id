package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// eventRow is the GORM-mapped representation of the outbox_events table.
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
type GormRepository struct{ db *gorm.DB }

func NewGormRepository(db *gorm.DB) *GormRepository { return &GormRepository{db: db} }

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

// FetchUnpublished returns up to limit unpublished events that are not dead
// letters, ordered oldest-first. The caller (relay) applies the per-event
// backoff check in Go before handling. Served by the partial index.
//
// Uses FOR UPDATE SKIP LOCKED so concurrent instances each obtain a disjoint
// set of events — preventing duplicate processing in multi-instance deployments.
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
	var rows []eventRow
	if err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
		Where("published_at IS NULL AND NOT dead_letter").
		Order("created_at ASC, id ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("fetch unpublished: %w", err)
	}
	out := make([]*Event, len(rows))
	for i := range rows {
		out[i] = rowToEvent(&rows[i])
	}
	return out, nil
}

// MarkPublished stamps published_at on the given event IDs.
func (r *GormRepository) MarkPublished(ctx context.Context, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).Model(&eventRow{}).
		Where("id IN ?", ids).
		UpdateColumn("published_at", time.Now().UTC()).Error; err != nil {
		return fmt.Errorf("mark published: %w", err)
	}
	return nil
}

// MarkFailed increments retry_count, sets last_retry_at to now, and when
// retryCount >= maxRetries marks the event as dead_letter so the relay stops
// picking it up.
func (r *GormRepository) MarkFailed(ctx context.Context, id uuid.UUID, retryCount int, maxRetries int, at time.Time) error {
	updates := map[string]any{
		"retry_count":   retryCount,
		"last_retry_at": at,
	}
	if retryCount >= maxRetries {
		updates["dead_letter"] = true
	}
	if err := r.db.WithContext(ctx).Model(&eventRow{}).
		Where("id = ?", id).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("mark failed: %w", err)
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
		// Purge dead letters past cutoff.
		sub := r.db.WithContext(ctx).Model(&eventRow{}).
			Where("dead_letter = true AND created_at < ?", cutoff).
			Order("created_at ASC, id ASC").
			Select("id").
			Limit(batchSize)
		res := r.db.WithContext(ctx).
			Where("id IN (?)", sub).
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

// RunOutboxCleaner starts a background goroutine that periodically purges old
// outbox event rows. Dead letters older than deadLetterRetention and published
// events older than publishedRetention are deleted. Runs on the given interval.
// Returns a stop function. A non-positive interval disables the cleaner.
func RunOutboxCleaner(repo Repository, log *zap.Logger, deadLetterRetention, publishedRetention, interval time.Duration) func() {
	if interval <= 0 {
		return func() {}
	}
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		ctx := context.Background()
		for {
			select {
			case <-t.C:
				now := time.Now().UTC()
				deadCutoff := now.Add(-deadLetterRetention)
				pubCutoff := now.Add(-publishedRetention)
				deleted, err := repo.PurgeDeadLetters(ctx, deadCutoff, pubCutoff, 0)
				if err != nil {
					log.Warn("outbox purge failed", zap.Error(err))
				} else if deleted > 0 {
					log.Info("outbox purge complete",
						zap.Int64("deleted", deleted),
						zap.Time("dead_letter_cutoff", deadCutoff),
						zap.Time("published_cutoff", pubCutoff))
				}
			case <-done:
				t.Stop()
				return
			}
		}
	}()
	return func() { close(done) }
}
