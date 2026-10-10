package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

func (r *syncJobRepo) ClaimNextForRegion(ctx context.Context, worker string, lease time.Duration, region string) (*domain.SyncJob, error) {
	return r.claimNextForRegion(ctx, worker, lease, region)
}
func (r *syncJobRepo) claimNextForRegion(ctx context.Context, worker string, lease time.Duration, region string) (*domain.SyncJob, error) {
	var result *domain.SyncJob
	unknown := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		query := pendingSyncExecutionQuery(tx, region).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Order("created_at ASC, id ASC").First(&row)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			if err := r.expireSyncExecutionTx(tx, region); err != nil {
				return err
			}
			unknown = true
			return nil
		}
		if query.Error != nil {
			return query.Error
		}
		return claimSyncJob(tx, &row, worker, lease, "execute", &result)
	})
	// A transaction sentinel is not a rollback error. Unknown plus the expiry
	// audit must commit before callers observe ErrSyncJobUnknown.
	if err == nil && unknown {
		return nil, ErrSyncJobUnknown
	}
	return result, err
}

func (r *syncJobRepo) ClaimUnknownNextForRegion(ctx context.Context, worker string, lease time.Duration, region string) (*domain.SyncJob, error) {
	var result *domain.SyncJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		query := regionalSyncJobQuery(tx, region, "(status = ? AND (lease_until IS NULL OR lease_until < clock_timestamp())) OR "+
			"(status = ? AND lease_kind = ? AND lease_until < clock_timestamp()) OR "+
			"(status = ? AND job_type = ? AND error_code = ? AND (lease_until IS NULL OR lease_until < clock_timestamp()))",
			string(domain.SyncJobUnknown), string(domain.SyncJobRunning), "reconcile",
			string(domain.SyncJobPartiallyComplete), string(domain.SyncJobErase), "erase.local_projection_failed").
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Order("updated_at ASC, id ASC").First(&row)
		if query.Error != nil {
			return query.Error
		}
		return claimSyncJob(tx, &row, worker, lease, "reconcile", &result)
	})
	return result, err
}

func regionalSyncJobQuery(tx *gorm.DB, region, condition string, args ...any) *gorm.DB {
	query := tx.Where(condition, args...)
	if region != "" {
		query = query.Where("EXISTS (SELECT 1 FROM accounts WHERE accounts.id = sync_jobs.account_id AND "+
			"accounts.tenant_id = sync_jobs.tenant_id AND accounts.home_region = ?)", region)
	}
	return query
}
func claimSyncJob(tx *gorm.DB, row *syncJobRow, _ string, lease time.Duration, leaseKind string, result **domain.SyncJob) error {
	if err := tx.Model(row).Updates(map[string]any{"status": string(domain.SyncJobRunning), "attempt": gorm.Expr("attempt + 1"),
		"lease_owner": newSyncLeaseOwner(), "lease_until": gorm.Expr("clock_timestamp() + (? * interval '1 second')", lease.Seconds()),
		"lease_kind": leaseKind, "completed_at": nil, "updated_at": gorm.Expr("clock_timestamp()")}).Error; err != nil {
		return err
	}
	if err := tx.First(row, "id = ?", row.ID).Error; err != nil {
		return err
	}
	converted, err := syncJobFromRow(row)
	*result = converted
	return err
}
