package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// ErrRegionEpochConflict is returned when a promotion CAS fails because the
// expected epoch no longer matches (another region promoted first).
var ErrRegionEpochConflict = errors.New("region epoch conflict")

// RegionDirectory is the durable home-region record for one account.
type RegionDirectory struct {
	ID              domain.ID
	AccountID       domain.ID
	TenantID        string
	HomeRegion      string
	ResolverVersion string
	FencedEpoch     int64
	PromotedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// RegionDirectoryRepository persists home-region assignments and fencing
// epochs (P6-02). Promotions are compare-and-swap on the epoch.
type RegionDirectoryRepository interface {
	// EnsureDirectory creates the directory row for a newly provisioned account
	// (idempotent; never downgrades an existing home region).
	EnsureDirectory(ctx context.Context, tenantID string, accountID domain.ID, homeRegion, resolverVersion string) error
	// GetDirectory loads the account's directory entry.
	GetDirectory(ctx context.Context, tenantID string, accountID domain.ID) (*RegionDirectory, error)
	// RegionEpoch reads the durable epoch of a region.
	RegionEpoch(ctx context.Context, region string) (int64, error)
	// PromoteWithFencing moves the account's writer to targetRegion. It CASes
	// on expectedEpoch (the caller's observed directory epoch) and bumps the
	// target region's epoch atomically, proving old writers are fenced.
	// Returns the new epoch. Fails with ErrRegionEpochConflict on a stale CAS.
	PromoteWithFencing(ctx context.Context, tenantID string, accountID domain.ID, targetRegion string, expectedEpoch int64) (int64, error)
}

type regionDirectoryRow struct {
	ID              domain.ID  `gorm:"column:id;primaryKey"`
	AccountID       domain.ID  `gorm:"column:account_id"`
	TenantID        string     `gorm:"column:tenant_id"`
	HomeRegion      string     `gorm:"column:home_region"`
	ResolverVersion string     `gorm:"column:resolver_version"`
	FencedEpoch     int64      `gorm:"column:fenced_epoch"`
	PromotedAt      *time.Time `gorm:"column:promoted_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (regionDirectoryRow) TableName() string { return "region_directory" }

type regionEpochRow struct {
	Region    string    `gorm:"column:region;primaryKey"`
	Epoch     int64     `gorm:"column:epoch"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (regionEpochRow) TableName() string { return "region_epochs" }

type regionDirectoryRepo struct {
	db *gorm.DB
}

func NewRegionDirectoryRepository(db *gorm.DB) RegionDirectoryRepository {
	return &regionDirectoryRepo{db: db}
}

func (r *regionDirectoryRepo) EnsureDirectory(ctx context.Context, tenantID string, accountID domain.ID,
	homeRegion, resolverVersion string) error {
	now := time.Now().UTC()
	row := &regionDirectoryRow{ID: idgen.NewID(), AccountID: accountID, TenantID: tenantID,
		HomeRegion: homeRegion, ResolverVersion: resolverVersion, FencedEpoch: 0, CreatedAt: now, UpdatedAt: now}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		insert := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "account_id"}},
			DoNothing: true, // never downgrade/rewrite an existing assignment
		}).Create(row)
		if insert.Error != nil {
			return fmt.Errorf("create region directory: %w", insert.Error)
		}
		if insert.RowsAffected == 0 {
			return nil
		}
		return ensureRegionEpochRow(tx, homeRegion, now)
	})
	if err != nil {
		return err
	}
	return nil
}

func (r *regionDirectoryRepo) GetDirectory(ctx context.Context, tenantID string, accountID domain.ID) (*RegionDirectory, error) {
	var row regionDirectoryRow
	if err := r.db.WithContext(ctx).Where("tenant_id = ? AND account_id = ?", tenantID, accountID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return regionDirectoryRowToDomain(&row), nil
}

func (r *regionDirectoryRepo) RegionEpoch(ctx context.Context, region string) (int64, error) {
	var row regionEpochRow
	err := r.db.WithContext(ctx).Where("region = ?", region).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil // a region with no epoch row starts at 0
	}
	if err != nil {
		return 0, err
	}
	return row.Epoch, nil
}

func (r *regionDirectoryRepo) PromoteWithFencing(ctx context.Context, tenantID string, accountID domain.ID,
	targetRegion string, expectedEpoch int64) (int64, error) {
	var newEpoch int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the directory row so concurrent promotions serialize.
		var row regionDirectoryRow
		query := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("tenant_id = ? AND account_id = ?", tenantID, accountID).First(&row)
		if query.Error != nil {
			if errors.Is(query.Error, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return query.Error
		}
		if row.FencedEpoch != expectedEpoch {
			return ErrRegionEpochConflict
		}
		// Bump the target region's durable epoch — the fencing proof.
		var target regionEpochRow
		err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("region = ?", targetRegion).First(&target).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// The region epoch row is shared by every account in the region; a
		// promotion must never lower it (that would fence out current writers
		// and could re-admit a fenced-out generation). Bump from the higher
		// of the row's current value and the account's observed epoch.
		newEpoch = max(target.Epoch, expectedEpoch) + 1
		now := time.Now().UTC()
		if err := upsertRegionEpoch(tx, targetRegion, newEpoch, now); err != nil {
			return err
		}
		// Point the directory at the new writer with the new epoch.
		update := tx.Model(&regionDirectoryRow{}).Where("tenant_id = ? AND account_id = ?", tenantID, accountID).
			Updates(map[string]any{"home_region": targetRegion, "fenced_epoch": newEpoch,
				"promoted_at": now, "updated_at": now})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrRegionEpochConflict
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newEpoch, nil
}

// ensureRegionEpochRow creates the region's epoch row at 0 when it does not
// exist yet. The row is shared by every account in the region, so an existing
// row is never touched: a new account must not reset the region's fencing
// epoch (that would fence out every current writer).
func ensureRegionEpochRow(tx *gorm.DB, region string, now time.Time) error {
	row := &regionEpochRow{Region: region, Epoch: 0, UpdatedAt: now}
	insert := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "region"}},
		DoNothing: true,
	}).Create(row)
	if insert.Error != nil {
		return fmt.Errorf("ensure region epoch: %w", insert.Error)
	}
	return nil
}

func upsertRegionEpoch(tx *gorm.DB, region string, epoch int64, now time.Time) error {
	row := &regionEpochRow{Region: region, Epoch: epoch, UpdatedAt: now}
	insert := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "region"}},
		DoUpdates: clause.AssignmentColumns([]string{"epoch", "updated_at"}),
	}).Create(row)
	if insert.Error != nil {
		return fmt.Errorf("upsert region epoch: %w", insert.Error)
	}
	return nil
}

func regionDirectoryRowToDomain(row *regionDirectoryRow) *RegionDirectory {
	return &RegionDirectory{
		ID: row.ID, AccountID: row.AccountID, TenantID: row.TenantID, HomeRegion: row.HomeRegion,
		ResolverVersion: row.ResolverVersion, FencedEpoch: row.FencedEpoch, PromotedAt: row.PromotedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
