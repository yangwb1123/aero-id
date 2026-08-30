package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

// ReplicationChangeRepository is the commit-ordered scan used by new workers.
// It is separate from SnapshotRepository so legacy cursor implementations remain
// source-compatible during rollout.
type ReplicationChangeRepository interface {
	ListReplicationChangesAfter(context.Context, string, int64, int) ([]ReplicableSnapshot, error)
}

type replicationChangeRow struct {
	Sequence     int64             `gorm:"column:sequence"`
	SnapshotID   uuid.UUID         `gorm:"column:snapshot_id"`
	AccountID    uuid.UUID         `gorm:"column:account_id"`
	TenantID     string            `gorm:"column:tenant_id"`
	HomeRegion   string            `gorm:"column:home_region"`
	Source       string            `gorm:"column:source"`
	SourceRegion string            `gorm:"column:source_region"`
	Dataset      string            `gorm:"column:dataset"`
	Status       string            `gorm:"column:status"`
	Version      int64             `gorm:"column:version"`
	GeneratedAt  time.Time         `gorm:"column:generated_at"`
	LastSyncedAt *time.Time        `gorm:"column:last_synced_at"`
	ExpiresAt    *time.Time        `gorm:"column:expires_at"`
	ErrorCode    string            `gorm:"column:error_code"`
	Data         datatypes.JSONMap `gorm:"column:data"`
	Deleted      bool              `gorm:"column:deleted"`
	OccurredAt   time.Time         `gorm:"column:occurred_at"`
}

func (replicationChangeRow) TableName() string { return "account_snapshot_replication_changes" }

func (r *accountSnapshotRepo) ListReplicationChangesAfter(ctx context.Context, sourceRegion string,
	after int64, limit int) ([]ReplicableSnapshot, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	var rows []replicationChangeRow
	query := r.db.WithContext(ctx).Table("account_snapshot_replication_changes AS changes").
		Joins("JOIN accounts ON accounts.id = changes.account_id").
		Where("accounts.home_region = ? AND changes.sequence > ?", sourceRegion, after).
		Order("changes.sequence ASC").Limit(limit)
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ReplicableSnapshot, 0, len(rows))
	for i := range rows {
		row := rows[i]
		out = append(out, ReplicableSnapshot{Revision: row.Sequence, Deleted: row.Deleted, TenantID: row.TenantID,
			Snapshot: &domain.AccountSnapshot{ID: row.SnapshotID, AccountID: row.AccountID, Source: row.Source,
				SourceRegion: row.SourceRegion, Dataset: domain.AccountDataset(row.Dataset), Status: domain.SnapshotStatus(row.Status),
				Version: row.Version, GeneratedAt: row.GeneratedAt, LastSyncedAt: row.LastSyncedAt,
				ExpiresAt: row.ExpiresAt, ErrorCode: row.ErrorCode, Data: map[string]any(row.Data), UpdatedAt: row.OccurredAt}})
	}
	return out, nil
}

func appendReplicationChange(tx *gorm.DB, snapshot *domain.AccountSnapshot, deleted bool) error {
	var clock struct {
		NextSequence int64 `gorm:"column:next_sequence"`
	}
	if err := tx.Raw("SELECT next_sequence FROM replication_change_clock WHERE id = TRUE FOR UPDATE").Scan(&clock).Error; err != nil {
		return err
	}
	clock.NextSequence++
	if err := tx.Exec("UPDATE replication_change_clock SET next_sequence = ? WHERE id = TRUE", clock.NextSequence).Error; err != nil {
		return err
	}
	var account struct {
		TenantID   string `gorm:"column:tenant_id"`
		HomeRegion string `gorm:"column:home_region"`
	}
	if err := tx.Table("accounts").Select("tenant_id, home_region").Where("id = ?", snapshot.AccountID).Scan(&account).Error; err != nil {
		return err
	}
	return tx.Create(&replicationChangeRow{Sequence: clock.NextSequence, SnapshotID: snapshot.ID,
		AccountID: snapshot.AccountID, TenantID: account.TenantID, HomeRegion: account.HomeRegion, Source: snapshot.Source, SourceRegion: snapshot.SourceRegion,
		Dataset: string(snapshot.Dataset), Status: string(snapshot.Status), Version: snapshot.Version,
		GeneratedAt: snapshot.GeneratedAt, LastSyncedAt: snapshot.LastSyncedAt, ExpiresAt: snapshot.ExpiresAt,
		ErrorCode: snapshot.ErrorCode, Data: datatypes.JSONMap(snapshot.Data), Deleted: deleted, OccurredAt: snapshot.UpdatedAt}).Error
}

// DeleteSnapshot writes a tombstone before removing the mutable current row.
// The tombstone is what makes deletion recoverable after a cursor has moved.
func (r *accountSnapshotRepo) DeleteSnapshot(ctx context.Context, accountID domain.ID, source string,
	dataset domain.AccountDataset) error {
	var snapshot accountSnapshotRow
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("account_id = ? AND source = ? AND dataset = ?", accountID, source, dataset).First(&snapshot).Error; err != nil {
			return err
		}
		s := snapshotRowToDomain(&snapshot)
		if err := appendReplicationChange(tx, s, true); err != nil {
			return err
		}
		return tx.Delete(&accountSnapshotRow{}, "id = ?", snapshot.ID).Error
	})
}

type ReplicationSnapshotDeletionRepository interface {
	DeleteSnapshot(context.Context, domain.ID, string, domain.AccountDataset) error
}

var _ ReplicationChangeRepository = (*accountSnapshotRepo)(nil)
var _ ReplicationSnapshotDeletionRepository = (*accountSnapshotRepo)(nil)
