package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type SnapshotRepository interface {
	GetSnapshot(ctx context.Context, accountID domain.ID, source string, dataset domain.AccountDataset) (*domain.AccountSnapshot, error)
	ListSnapshots(ctx context.Context, accountID domain.ID, datasets []domain.AccountDataset) ([]*domain.AccountSnapshot, error)
	UpsertSnapshot(ctx context.Context, snapshot *domain.AccountSnapshot) error
	// ListReplicableSnapshots returns approved projections of accounts homed in
	// the given region, ordered by snapshot id ascending after afterID (P6-03).
	ListReplicableSnapshots(ctx context.Context, homeRegion string, afterID domain.ID, limit int) ([]ReplicableSnapshot, error)
}

// ReplicableSnapshot pairs a projection with its account tenant for canonical
// envelope construction during cross-region replication.
type ReplicableSnapshot struct {
	Snapshot *domain.AccountSnapshot
	TenantID string
}

// SnapshotReconciler is the optional write seam used only after a source has
// declared a complete, versioned summary. It marks omitted datasets unavailable
// without deleting their diagnostic history.
type SnapshotReconciler interface {
	MarkUnavailable(context.Context, domain.ID, string, []domain.AccountDataset, int64) (int, error)
}

type accountSnapshotRepo struct{ db *gorm.DB }

func NewAccountSnapshotRepository(db *gorm.DB) SnapshotRepository {
	return &accountSnapshotRepo{db: db}
}

type accountSnapshotRow struct {
	ID           uuid.UUID         `gorm:"column:id;primaryKey"`
	AccountID    uuid.UUID         `gorm:"column:account_id"`
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
	CreatedAt    time.Time         `gorm:"column:created_at"`
	UpdatedAt    time.Time         `gorm:"column:updated_at"`
}

func (accountSnapshotRow) TableName() string { return "account_snapshots" }

func snapshotRowToDomain(row *accountSnapshotRow) *domain.AccountSnapshot {
	return &domain.AccountSnapshot{
		ID:           row.ID,
		AccountID:    row.AccountID,
		Source:       row.Source,
		SourceRegion: row.SourceRegion,
		Dataset:      domain.AccountDataset(row.Dataset),
		Status:       domain.SnapshotStatus(row.Status),
		Version:      row.Version,
		GeneratedAt:  row.GeneratedAt,
		LastSyncedAt: row.LastSyncedAt,
		ExpiresAt:    row.ExpiresAt,
		ErrorCode:    row.ErrorCode,
		Data:         map[string]any(row.Data),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func snapshotDomainToRow(snapshot *domain.AccountSnapshot) *accountSnapshotRow {
	snapshot.Normalize()
	return &accountSnapshotRow{
		ID:           snapshot.ID,
		AccountID:    snapshot.AccountID,
		Source:       snapshot.Source,
		SourceRegion: snapshot.SourceRegion,
		Dataset:      string(snapshot.Dataset),
		Status:       string(snapshot.Status),
		Version:      snapshot.Version,
		GeneratedAt:  snapshot.GeneratedAt,
		LastSyncedAt: snapshot.LastSyncedAt,
		ExpiresAt:    snapshot.ExpiresAt,
		ErrorCode:    snapshot.ErrorCode,
		Data:         datatypes.JSONMap(snapshot.Data),
		CreatedAt:    snapshot.CreatedAt,
		UpdatedAt:    snapshot.UpdatedAt,
	}
}

func (r *accountSnapshotRepo) GetSnapshot(ctx context.Context, accountID domain.ID, source string,
	dataset domain.AccountDataset) (*domain.AccountSnapshot, error) {
	var row accountSnapshotRow
	err := r.db.WithContext(ctx).Where("account_id = ? AND source = ? AND dataset = ?", accountID, source, dataset).First(&row).Error
	if err != nil {
		return nil, err
	}
	return snapshotRowToDomain(&row), nil
}

func (r *accountSnapshotRepo) ListSnapshots(ctx context.Context, accountID domain.ID, datasets []domain.AccountDataset) ([]*domain.AccountSnapshot, error) {
	query := r.db.WithContext(ctx).Where("account_id = ?", accountID).Order("source ASC, dataset ASC")
	if len(datasets) > 0 {
		names := make([]string, len(datasets))
		for i, dataset := range datasets {
			names[i] = string(dataset)
		}
		query = query.Where("dataset IN ?", names)
	}
	var rows []accountSnapshotRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]*domain.AccountSnapshot, len(rows))
	for i := range rows {
		result[i] = snapshotRowToDomain(&rows[i])
	}
	return result, nil
}

func (r *accountSnapshotRepo) UpsertSnapshot(ctx context.Context, snapshot *domain.AccountSnapshot) error {
	now := time.Now().UTC()
	if snapshot.ID == (domain.ID{}) {
		snapshot.ID = idgen.NewID()
	}
	if snapshot.GeneratedAt.IsZero() {
		snapshot.GeneratedAt = now
	}
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = now
	}
	snapshot.UpdatedAt = now
	row := snapshotDomainToRow(snapshot)
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "account_id"}, {Name: "source"}, {Name: "dataset"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"source_region", "status", "version", "generated_at", "last_synced_at", "expires_at", "error_code", "data", "updated_at",
		}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "account_snapshots.version <= EXCLUDED.version"}}},
	}).Create(row).Error
}

// replicableSnapshotScan is the explicit-column scan target for
// ListReplicableSnapshots. Named fields + explicit columns: the historical
// embedded-struct scan with `Select("account_snapshots.*, ...")` returned a
// fully zeroed snapshot row (GORM did not map the embedded struct), which
// would publish degenerate envelopes and zero-account audit facts.
type replicableSnapshotScan struct {
	ID           uuid.UUID         `gorm:"column:id"`
	AccountID    uuid.UUID         `gorm:"column:account_id"`
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
	CreatedAt    time.Time         `gorm:"column:created_at"`
	UpdatedAt    time.Time         `gorm:"column:updated_at"`
	TenantID     string            `gorm:"column:tenant_id"`
}

// replicableSnapshotColumns is the explicit column list for the join scan
// (same pattern as the audit_publication_replay/callback_audit join scans).
const replicableSnapshotColumns = "account_snapshots.id, account_snapshots.account_id, account_snapshots.source, " +
	"account_snapshots.source_region, account_snapshots.dataset, account_snapshots.status, " +
	"account_snapshots.version, account_snapshots.generated_at, account_snapshots.last_synced_at, " +
	"account_snapshots.expires_at, account_snapshots.error_code, account_snapshots.data, " +
	"account_snapshots.created_at, account_snapshots.updated_at, accounts.tenant_id"

func (r *accountSnapshotRepo) ListReplicableSnapshots(ctx context.Context, homeRegion string, afterID domain.ID, limit int) ([]ReplicableSnapshot, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	var rows []replicableSnapshotScan
	query := r.db.WithContext(ctx).
		Table("account_snapshots").
		Select(replicableSnapshotColumns).
		Joins("JOIN accounts ON accounts.id = account_snapshots.account_id AND accounts.home_region = ?", homeRegion).
		Where("account_snapshots.status IN ?", []string{string(domain.SnapshotFresh), string(domain.SnapshotStale)})
	if afterID != (domain.ID{}) {
		query = query.Where("account_snapshots.id > ?", afterID)
	}
	if err := query.Order("account_snapshots.id ASC").Limit(limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ReplicableSnapshot, 0, len(rows))
	for i := range rows {
		row := rows[i]
		out = append(out, ReplicableSnapshot{Snapshot: &domain.AccountSnapshot{
			ID: row.ID, AccountID: row.AccountID, Source: row.Source, SourceRegion: row.SourceRegion,
			Dataset: domain.AccountDataset(row.Dataset), Status: domain.SnapshotStatus(row.Status),
			Version: row.Version, GeneratedAt: row.GeneratedAt, LastSyncedAt: row.LastSyncedAt,
			ExpiresAt: row.ExpiresAt, ErrorCode: row.ErrorCode, Data: map[string]any(row.Data),
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}, TenantID: row.TenantID})
	}
	return out, nil
}

func (r *accountSnapshotRepo) MarkUnavailable(ctx context.Context, accountID domain.ID, source string,
	datasets []domain.AccountDataset, version int64) (int, error) {
	if len(datasets) == 0 || version <= 0 {
		return 0, nil
	}
	names := make([]string, len(datasets))
	for i, dataset := range datasets {
		names[i] = string(dataset)
	}
	result := r.db.WithContext(ctx).Model(&accountSnapshotRow{}).
		Where("account_id = ? AND source = ? AND dataset IN ? AND version <= ?", accountID, source, names, version).
		Updates(map[string]any{"status": string(domain.SnapshotUnavailable), "version": version,
			"error_code": "source.reconciled_absent", "last_synced_at": time.Now().UTC(), "updated_at": time.Now().UTC()})
	return int(result.RowsAffected), result.Error
}
