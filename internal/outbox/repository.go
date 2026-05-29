package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
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
	}
}

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

// FetchUnpublished returns up to limit unpublished events, oldest-first, served
// by the idx_outbox_unpublished partial index.
func (r *GormRepository) FetchUnpublished(ctx context.Context, limit int) ([]*Event, error) {
	var rows []eventRow
	if err := r.db.WithContext(ctx).
		Where("published_at IS NULL").
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
