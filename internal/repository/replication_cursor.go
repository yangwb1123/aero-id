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

type ReplicationCursorRepository interface {
	GetCursor(context.Context, string, string) (domain.ID, int64, error)
	SaveCursor(context.Context, string, string, domain.ID, int64) error
}

type ReplicationCursorCheckpointRepository interface {
	GetCursorCheckpoint(context.Context, string, string) (domain.ID, time.Time, int64, error)
	SaveCursorCheckpoint(context.Context, string, string, time.Time, domain.ID, int64) error
}

type ReplicationCursorLeaseRepository interface {
	ReplicationCursorRepository
	ClaimCursor(context.Context, string, string, string, time.Duration) (bool, error)
	SaveCursorOwned(context.Context, string, string, string, time.Duration, domain.ID, int64) error
	ReleaseCursor(context.Context, string, string, string) error
}

type ReplicationCursorCheckpointLeaseRepository interface {
	ReplicationCursorLeaseRepository
	SaveCursorCheckpointOwned(context.Context, string, string, string, time.Duration, time.Time, domain.ID, int64) error
}

// Generation fencing is optional on the compatibility interfaces. Production
// uses it for both cursor and terminal-failure writes; old test fakes continue
// to implement the smaller lease interface.
type ReplicationCursorGenerationLeaseRepository interface {
	ReplicationCursorLeaseRepository
	ClaimCursorGeneration(context.Context, string, string, string, time.Duration) (bool, int64, error)
}
type ReplicationCursorCheckpointGenerationRepository interface {
	SaveCursorCheckpointOwnedGeneration(context.Context, string, string, string, int64, time.Duration, time.Time, domain.ID, int64) error
}
type ReplicationCursorGenerationSaveRepository interface {
	SaveCursorOwnedGeneration(context.Context, string, string, string, int64, time.Duration, domain.ID, int64) error
}

type ReplicationCursorSequenceRepository interface {
	GetCursorSequence(context.Context, string, string) (int64, int64, error)
	SaveCursorSequence(context.Context, string, string, int64, domain.ID, int64) error
}
type ReplicationCursorSequenceLeaseRepository interface {
	ReplicationCursorSequenceRepository
	SaveCursorSequenceOwned(context.Context, string, string, string, int64, time.Duration, int64, domain.ID, int64) error
}
type ReplicationCursorSequenceGenerationRepository interface {
	SaveCursorSequenceOwnedGeneration(context.Context, string, string, string, int64, time.Duration, int64, domain.ID, int64) error
}

var ErrReplicationCursorLeaseLost = errors.New("replication cursor lease lost")

type replicationCursorRow struct {
	ID                      domain.ID  `gorm:"column:id;primaryKey"`
	SourceRegion            string     `gorm:"column:source_region"`
	Target                  string     `gorm:"column:target"`
	LastSnapshotID          *domain.ID `gorm:"column:last_snapshot_id"`
	LastUpdatedAt           *time.Time `gorm:"column:last_updated_at"`
	Published               int64      `gorm:"column:published"`
	LeaseOwner              string     `gorm:"column:lease_owner"`
	LeaseUntil              *time.Time `gorm:"column:lease_until"`
	LeaseGeneration         int64      `gorm:"column:lease_generation"`
	LastReplicationSequence int64      `gorm:"column:last_replication_sequence"`
	UpdatedAt               time.Time  `gorm:"column:updated_at"`
}

func (replicationCursorRow) TableName() string { return "replication_cursors" }

type replicationCursorRepo struct{ db *gorm.DB }

func NewReplicationCursorRepository(db *gorm.DB) ReplicationCursorRepository {
	return &replicationCursorRepo{db: db}
}

func (r *replicationCursorRepo) load(ctx context.Context, region, target string, row *replicationCursorRow) error {
	return r.db.WithContext(ctx).Where("source_region = ? AND target = ?", region, target).First(row).Error
}
func (r *replicationCursorRepo) GetCursor(ctx context.Context, region, target string) (domain.ID, int64, error) {
	var row replicationCursorRow
	if err := r.load(ctx, region, target, &row); errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ID{}, 0, nil
	} else if err != nil {
		return domain.ID{}, 0, err
	} else if row.LastSnapshotID != nil {
		return *row.LastSnapshotID, row.Published, nil
	}
	return domain.ID{}, row.Published, nil
}
func (r *replicationCursorRepo) SaveCursor(ctx context.Context, region, target string, id domain.ID, published int64) error {
	return r.saveCursor(ctx, region, target, nil, id, published)
}
func (r *replicationCursorRepo) GetCursorCheckpoint(ctx context.Context, region, target string) (domain.ID, time.Time, int64, error) {
	var row replicationCursorRow
	if err := r.load(ctx, region, target, &row); errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ID{}, time.Time{}, 0, nil
	} else if err != nil {
		return domain.ID{}, time.Time{}, 0, err
	}
	var id domain.ID
	if row.LastSnapshotID != nil {
		id = *row.LastSnapshotID
	}
	var updated time.Time
	if row.LastUpdatedAt != nil {
		updated = row.LastUpdatedAt.UTC()
	}
	return id, updated, row.Published, nil
}
func (r *replicationCursorRepo) SaveCursorCheckpoint(ctx context.Context, region, target string, updated time.Time, id domain.ID, published int64) error {
	updated = updated.UTC()
	return r.saveCursor(ctx, region, target, &updated, id, published)
}
func (r *replicationCursorRepo) saveCursor(ctx context.Context, region, target string, updated *time.Time, id domain.ID, published int64) error {
	now := time.Now().UTC()
	row := &replicationCursorRow{ID: idgen.NewID(), SourceRegion: region, Target: target, LastSnapshotID: &id,
		LastUpdatedAt: updated, Published: published, UpdatedAt: now}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_region"}, {Name: "target"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_snapshot_id", "last_updated_at", "published", "updated_at"})}).Create(row).Error
}

func (r *replicationCursorRepo) ClaimCursor(ctx context.Context, region, target, owner string, lease time.Duration) (bool, error) {
	ok, _, err := r.ClaimCursorGeneration(ctx, region, target, owner, lease)
	return ok, err
}
func (r *replicationCursorRepo) ClaimCursorGeneration(ctx context.Context, region, target, owner string, lease time.Duration) (bool, int64, error) {
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	var row struct {
		Generation int64 `gorm:"column:lease_generation"`
	}
	result := r.db.WithContext(ctx).Raw(`
INSERT INTO replication_cursors
 (id, source_region, target, last_snapshot_id, published, updated_at, lease_owner, lease_until, lease_generation)
VALUES (?, ?, ?, NULL, 0, now(), ?, now() + (? * interval '1 second'), 1)
ON CONFLICT (source_region, target) DO UPDATE SET
 lease_owner=EXCLUDED.lease_owner, lease_until=EXCLUDED.lease_until,
 lease_generation=replication_cursors.lease_generation+1, updated_at=now()
WHERE replication_cursors.lease_until IS NULL OR replication_cursors.lease_until < now()
 OR replication_cursors.lease_owner=EXCLUDED.lease_owner
RETURNING lease_generation`, idgen.NewID(), region, target, owner, lease.Seconds()).Scan(&row)
	return result.RowsAffected == 1, row.Generation, result.Error
}
func (r *replicationCursorRepo) SaveCursorOwned(ctx context.Context, region, target, owner string,
	lease time.Duration, id domain.ID, published int64) error {
	return r.saveCursorOwned(ctx, region, target, owner, 0, lease, nil, id, published)
}
func (r *replicationCursorRepo) SaveCursorCheckpointOwned(ctx context.Context, region, target, owner string,
	lease time.Duration, updated time.Time, id domain.ID, published int64) error {
	return r.saveCursorOwned(ctx, region, target, owner, 0, lease, &updated, id, published)
}
func (r *replicationCursorRepo) SaveCursorOwnedGeneration(ctx context.Context, region, target, owner string,
	generation int64, lease time.Duration, id domain.ID, published int64) error {
	return r.saveCursorOwned(ctx, region, target, owner, generation, lease, nil, id, published)
}
func (r *replicationCursorRepo) SaveCursorCheckpointOwnedGeneration(ctx context.Context, region, target, owner string,
	generation int64, lease time.Duration, updated time.Time, id domain.ID, published int64) error {
	return r.saveCursorOwned(ctx, region, target, owner, generation, lease, &updated, id, published)
}
func (r *replicationCursorRepo) saveCursorOwned(ctx context.Context, region, target, owner string,
	generation int64, lease time.Duration, updated *time.Time, id domain.ID, published int64) error {
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	where := "source_region=? AND target=? AND lease_owner=? AND lease_until > now()"
	args := []any{id, updated, published, lease.Seconds(), region, target, owner}
	if generation > 0 {
		where += " AND lease_generation=?"
		args = append(args, generation)
	}
	query := "UPDATE replication_cursors SET last_snapshot_id=?, last_updated_at=?, published=?, updated_at=now(), " +
		"lease_until=now() + (? * interval '1 second') WHERE " + where
	result := r.db.WithContext(ctx).Exec(query, args...)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrReplicationCursorLeaseLost
	}
	return nil
}
func (r *replicationCursorRepo) ReleaseCursor(ctx context.Context, region, target, owner string) error {
	return r.db.WithContext(ctx).Model(&replicationCursorRow{}).
		Where("source_region=? AND target=? AND lease_owner=?", region, target, owner).
		Updates(map[string]any{"lease_owner": "", "lease_until": nil, "updated_at": time.Now().UTC()}).Error
}

func (r *replicationCursorRepo) GetCursorSequence(ctx context.Context, region, target string) (int64, int64, error) {
	var row replicationCursorRow
	if err := r.load(ctx, region, target, &row); errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, 0, nil
	} else if err != nil {
		return 0, 0, err
	}
	return row.LastReplicationSequence, row.Published, nil
}
func (r *replicationCursorRepo) SaveCursorSequence(ctx context.Context, region, target string, sequence int64, id domain.ID, published int64) error {
	return r.saveCursorSequence(ctx, region, target, "", 0, 0, sequence, id, published)
}
func (r *replicationCursorRepo) SaveCursorSequenceOwned(ctx context.Context, region, target, owner string,
	generation int64, lease time.Duration, sequence int64, id domain.ID, published int64) error {
	return r.saveCursorSequence(ctx, region, target, owner, generation, lease, sequence, id, published)
}
func (r *replicationCursorRepo) SaveCursorSequenceOwnedGeneration(ctx context.Context, region, target, owner string,
	generation int64, lease time.Duration, sequence int64, id domain.ID, published int64) error {
	return r.saveCursorSequence(ctx, region, target, owner, generation, lease, sequence, id, published)
}
func (r *replicationCursorRepo) saveCursorSequence(ctx context.Context, region, target, owner string,
	generation int64, lease time.Duration, sequence int64, id domain.ID, published int64) error {
	if owner == "" {
		return r.db.WithContext(ctx).Exec(
			"UPDATE replication_cursors SET last_replication_sequence=?, last_snapshot_id=?, published=?, updated_at=now() WHERE source_region=? AND target=?",
			sequence, id, published, region, target).Error
	}
	if lease <= 0 {
		lease = 2 * time.Minute
	}
	result := r.db.WithContext(ctx).Exec(
		"UPDATE replication_cursors SET last_replication_sequence=?, last_snapshot_id=?, published=?, updated_at=now(), "+
			"updated_at=now(), lease_until=now() + (? * interval '1 second') "+
			"WHERE source_region=? AND target=? AND lease_owner=? AND lease_generation=? AND lease_until > now()",
		sequence, id, published, lease.Seconds(), region, target, owner, generation)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrReplicationCursorLeaseLost
	}
	return nil
}

var _ ReplicationCursorLeaseRepository = (*replicationCursorRepo)(nil)
var _ ReplicationCursorGenerationLeaseRepository = (*replicationCursorRepo)(nil)
