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

// ReplicationCursorCheckpointRepository persists the source update timestamp
// alongside the UUID tie-breaker. It prevents an in-place snapshot update from
// being skipped after the cursor already passed that snapshot ID.
type ReplicationCursorCheckpointRepository interface {
	GetCursorCheckpoint(ctx context.Context, sourceRegion, target string) (lastSnapshotID domain.ID,
		lastUpdatedAt time.Time, published int64, err error)
	SaveCursorCheckpoint(ctx context.Context, sourceRegion, target string, lastUpdatedAt time.Time,
		lastSnapshotID domain.ID, published int64) error
}

// ReplicationCursorLeaseRepository adds a database-fenced worker lease to the
// compatibility cursor surface. Older deterministic fakes can still implement
// ReplicationCursorRepository; production wiring uses this stronger interface.
type ReplicationCursorLeaseRepository interface {
	ReplicationCursorRepository
	ClaimCursor(ctx context.Context, sourceRegion, target, owner string, lease time.Duration) (bool, error)
	SaveCursorOwned(ctx context.Context, sourceRegion, target, owner string, lease time.Duration,
		lastSnapshotID domain.ID, published int64) error
	ReleaseCursor(ctx context.Context, sourceRegion, target, owner string) error
}

// ReplicationCursorCheckpointLeaseRepository is the production combination of
// update-aware checkpoints and owner-fenced writes. The smaller interfaces stay
// compatible with existing fakes and migration tooling.
type ReplicationCursorCheckpointLeaseRepository interface {
	ReplicationCursorLeaseRepository
	SaveCursorCheckpointOwned(ctx context.Context, sourceRegion, target, owner string, lease time.Duration,
		lastUpdatedAt time.Time, lastSnapshotID domain.ID, published int64) error
}

var ErrReplicationCursorLeaseLost = errors.New("replication cursor lease lost")

type replicationCursorRow struct {
	ID             domain.ID  `gorm:"column:id;primaryKey"`
	SourceRegion   string     `gorm:"column:source_region"`
	Target         string     `gorm:"column:target"`
	LastSnapshotID *domain.ID `gorm:"column:last_snapshot_id"`
	LastUpdatedAt  *time.Time `gorm:"column:last_updated_at"`
	Published      int64      `gorm:"column:published"`
	LeaseOwner     string     `gorm:"column:lease_owner"`
	LeaseUntil     *time.Time `gorm:"column:lease_until"`
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
	err := r.loadCursor(ctx, sourceRegion, target, &row)
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
	return r.saveCursor(ctx, sourceRegion, target, nil, lastSnapshotID, published)
}

func (r *replicationCursorRepo) GetCursorCheckpoint(ctx context.Context, sourceRegion, target string) (domain.ID, time.Time, int64, error) {
	var row replicationCursorRow
	if err := r.loadCursor(ctx, sourceRegion, target, &row); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ID{}, time.Time{}, 0, nil
		}
		return domain.ID{}, time.Time{}, 0, err
	}
	last := domain.ID{}
	if row.LastSnapshotID != nil {
		last = *row.LastSnapshotID
	}
	updated := time.Time{}
	if row.LastUpdatedAt != nil {
		updated = row.LastUpdatedAt.UTC()
	}
	return last, updated, row.Published, nil
}

func (r *replicationCursorRepo) SaveCursorCheckpoint(ctx context.Context, sourceRegion, target string,
	lastUpdatedAt time.Time, lastSnapshotID domain.ID, published int64) error {
	checkpoint := lastUpdatedAt.UTC()
	return r.saveCursor(ctx, sourceRegion, target, &checkpoint, lastSnapshotID, published)
}

func (r *replicationCursorRepo) loadCursor(ctx context.Context, sourceRegion, target string, row *replicationCursorRow) error {
	return r.db.WithContext(ctx).Where("source_region = ? AND target = ?", sourceRegion, target).First(row).Error
}

func (r *replicationCursorRepo) saveCursor(ctx context.Context, sourceRegion, target string, lastUpdatedAt *time.Time,
	lastSnapshotID domain.ID, published int64) error {
	now := time.Now().UTC()
	row := &replicationCursorRow{ID: idgen.NewID(), SourceRegion: sourceRegion, Target: target,
		LastSnapshotID: &lastSnapshotID, LastUpdatedAt: lastUpdatedAt, Published: published, UpdatedAt: now}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_region"}, {Name: "target"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_snapshot_id", "last_updated_at", "published", "updated_at"}),
	}).Create(row).Error
}

// ClaimCursor atomically creates or claims an expired cursor lease using the
// database clock. A false result is a normal contention outcome, not an error.
func (r *replicationCursorRepo) ClaimCursor(ctx context.Context, sourceRegion, target, owner string, lease time.Duration) (bool, error) {
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	result := r.db.WithContext(ctx).Exec(`
INSERT INTO replication_cursors
    (id, source_region, target, last_snapshot_id, published, updated_at, lease_owner, lease_until)
VALUES (?, ?, ?, NULL, 0, now(), ?, now() + (? * interval '1 second'))
ON CONFLICT (source_region, target) DO UPDATE
SET lease_owner = EXCLUDED.lease_owner,
    lease_until = EXCLUDED.lease_until,
    updated_at = now()
WHERE replication_cursors.lease_until IS NULL
   OR replication_cursors.lease_until < now()
   OR replication_cursors.lease_owner = EXCLUDED.lease_owner`,
		idgen.NewID(), sourceRegion, target, owner, lease.Seconds())
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// SaveCursorOwned advances and renews the cursor only while the caller still
// owns a live lease. This fences a stale worker after another instance claims
// the expired lease.
func (r *replicationCursorRepo) SaveCursorOwned(ctx context.Context, sourceRegion, target, owner string,
	lease time.Duration, lastSnapshotID domain.ID, published int64) error {
	return r.saveCursorOwned(ctx, sourceRegion, target, owner, lease, nil, lastSnapshotID, published)
}

func (r *replicationCursorRepo) SaveCursorCheckpointOwned(ctx context.Context, sourceRegion, target, owner string,
	lease time.Duration, lastUpdatedAt time.Time, lastSnapshotID domain.ID, published int64) error {
	checkpoint := lastUpdatedAt.UTC()
	return r.saveCursorOwned(ctx, sourceRegion, target, owner, lease, &checkpoint, lastSnapshotID, published)
}

func (r *replicationCursorRepo) saveCursorOwned(ctx context.Context, sourceRegion, target, owner string,
	lease time.Duration, lastUpdatedAt *time.Time, lastSnapshotID domain.ID, published int64) error {
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	result := r.db.WithContext(ctx).Exec(`
UPDATE replication_cursors
SET last_snapshot_id = ?, last_updated_at = ?, published = ?, updated_at = now(),
    lease_until = now() + (? * interval '1 second')
WHERE source_region = ? AND target = ? AND lease_owner = ? AND lease_until > now()`,
		lastSnapshotID, lastUpdatedAt, published, lease.Seconds(), sourceRegion, target, owner)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrReplicationCursorLeaseLost
	}
	return nil
}

func (r *replicationCursorRepo) ReleaseCursor(ctx context.Context, sourceRegion, target, owner string) error {
	return r.db.WithContext(ctx).Model(&replicationCursorRow{}).
		Where("source_region = ? AND target = ? AND lease_owner = ?", sourceRegion, target, owner).
		Updates(map[string]any{"lease_owner": "", "lease_until": nil, "updated_at": time.Now().UTC()}).Error
}

var _ ReplicationCursorLeaseRepository = (*replicationCursorRepo)(nil)
