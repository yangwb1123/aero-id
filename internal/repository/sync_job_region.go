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
	var result *domain.SyncJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		now := time.Now().UTC()
		query := regionalSyncJobQuery(tx, region, "status = ?", string(domain.SyncJobPending)).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Order("created_at ASC, id ASC").First(&row)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			var stale syncJobRow
			staleQuery := regionalSyncJobQuery(tx, region, "status = ? AND lease_kind = ? AND lease_until < ?",
				string(domain.SyncJobRunning), "execute", now).
				Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
				Order("lease_until ASC, created_at ASC, id ASC").First(&stale)
			if errors.Is(staleQuery.Error, gorm.ErrRecordNotFound) {
				return query.Error
			}
			if staleQuery.Error != nil {
				return staleQuery.Error
			}
			if err := markSyncJobUnknown(tx, &stale, now); err != nil {
				return err
			}
			return ErrSyncJobUnknown
		}
		if query.Error != nil {
			return query.Error
		}
		return claimSyncJob(tx, &row, worker, lease, now, &result)
	})
	return result, err
}

func (r *syncJobRepo) ClaimUnknownNextForRegion(ctx context.Context, worker string, lease time.Duration, region string) (*domain.SyncJob, error) {
	var result *domain.SyncJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		now := time.Now().UTC()
		query := regionalSyncJobQuery(tx, region, "job_type = ? AND ((status = ? AND (lease_until IS NULL OR "+
			"lease_until < ?)) OR (status = ? AND lease_kind = ? AND lease_until < ?))",
			string(domain.SyncJobErase), string(domain.SyncJobUnknown), now, string(domain.SyncJobRunning), "reconcile", now).
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
			Order("updated_at ASC, id ASC").First(&row)
		if query.Error != nil {
			return query.Error
		}
		return claimSyncJob(tx, &row, worker, lease, now, &result)
	})
	return result, err
}

func regionalSyncJobQuery(tx *gorm.DB, region, condition string, args ...any) *gorm.DB {
	query := tx.Where(condition, args...)
	if region != "" {
		query = query.Where("EXISTS (SELECT 1 FROM accounts WHERE accounts.id = sync_jobs.account_id AND accounts.home_region = ?)", region)
	}
	return query
}

func markSyncJobUnknown(tx *gorm.DB, row *syncJobRow, now time.Time) error {
	return tx.Model(row).Updates(map[string]any{"status": string(domain.SyncJobUnknown),
		"error_code": "sync.unknown_outcome", "error_message": "worker lease expired before the result was recorded",
		"lease_owner": "", "lease_until": nil, "lease_kind": "", "updated_at": now}).Error
}

func claimSyncJob(tx *gorm.DB, row *syncJobRow, worker string, lease time.Duration, now time.Time,
	result **domain.SyncJob) error {
	until := now.Add(lease)
	if err := tx.Model(row).Updates(map[string]any{"status": string(domain.SyncJobRunning), "attempt": gorm.Expr("attempt + 1"),
		"lease_owner": worker, "lease_until": until, "lease_kind": "execute", "updated_at": now}).Error; err != nil {
		return err
	}
	row.Status, row.Attempt, row.LeaseOwner, row.LeaseUntil, row.LeaseKind = string(domain.SyncJobRunning), row.Attempt+1, worker, &until, "execute"
	converted, err := syncJobFromRow(row)
	*result = converted
	return err
}
