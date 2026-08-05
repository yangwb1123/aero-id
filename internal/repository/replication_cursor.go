package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// ReplicationCursorRepository persists the durable replication cursor (P6-03).
// The worker resumes exactly where it stopped after a crash; consumers dedup
// by receipt so at-least-once publishing is safe.
type ReplicationCursorRepository interface {
	GetCursor(ctx context.Context, sourceRegion, target string) (lastSnapshotID domain.ID, published int64, err error)
	SaveCursor(ctx context.Context, sourceRegion, target string, lastSnapshotID domain.ID, published int64) error
}

type replicationCursorRow struct {
	ID             domain.ID  `gorm:"column:id;primaryKey"`
	SourceRegion   string     `gorm:"column:source_region"`
	Target         string     `gorm:"column:target"`
	LastSnapshotID *domain.ID `gorm:"column:last_snapshot_id"`
	Published      int64      `gorm:"column:published"`
	UpdatedAt      time.Time  `gorm:"column:updated_at"`
}

func (replicationCursorRow) TableName() string { return "replication_cursors" }

type replicationCursorRepo struct {
	db *gorm.DB
}

func NewReplicationCursorRepository(db *gorm.DB) ReplicationCursorRepository {
	return &replicationCursorRepo{db: db}
}

func (r *replicationCursorRepo) GetCursor(ctx context.Context, sourceRegion, target string) (domain.ID, int64, error) {
	var row replicationCursorRow
	err := r.db.WithContext(ctx).Where("source_region = ? AND target = ?", sourceRegion, target).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ID{}, 0, nil
	}
	if err != nil {
		return domain.ID{}, 0, err
	}
	last := domain.ID{}
	if row.LastSnapshotID != nil {
		last = *row.LastSnapshotID
	}
	return last, row.Published, nil
}

func (r *replicationCursorRepo) SaveCursor(ctx context.Context, sourceRegion, target string, lastSnapshotID domain.ID, published int64) error {
	now := time.Now().UTC()
	row := &replicationCursorRow{ID: idgen.NewID(), SourceRegion: sourceRegion, Target: target,
		LastSnapshotID: &lastSnapshotID, Published: published, UpdatedAt: now}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_region"}, {Name: "target"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_snapshot_id", "published", "updated_at"}),
	}).Create(row).Error
}
