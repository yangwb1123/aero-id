package repository

import (
	"context"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const expiredExportTimestampSQL = `CASE WHEN pg_input_is_valid(
 COALESCE(j.result->>'expires_at', a.continuation->'_artifact_result'->>'expires_at', ''), 'timestamptz')
 THEN COALESCE(j.result->>'expires_at', a.continuation->'_artifact_result'->>'expires_at')::timestamptz
 ELSE '-infinity'::timestamptz END`

func (r *syncJobRepo) ListExpiredExports(ctx context.Context, now time.Time, limit int) ([]*domain.SyncJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var rows []struct {
		Job               syncJobRow `gorm:"embedded"`
		CleanupStorageKey string
	}
	err := r.db.WithContext(ctx).Table("sync_jobs AS j").
		Select("j.*, COALESCE(j.result->>'_storage_key', a.storage_key) AS cleanup_storage_key").
		Joins("LEFT JOIN sync_job_artifacts AS a ON a.job_id = j.id AND a.kind = j.job_type").
		Where("j.job_type IN ?", []string{string(domain.SyncJobExport), string(domain.SyncJobAuditExport)}).
		Where("COALESCE(j.result->>'_storage_key', a.storage_key, '') <> ''").
		Where("j.status <> ? OR j.lease_until <= clock_timestamp()", string(domain.SyncJobRunning)).
		Where(expiredExportTimestampSQL+" <= ?", now).
		Order("j.updated_at ASC, j.id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	result := make([]*domain.SyncJob, 0, len(rows))
	for i := range rows {
		job, err := syncJobFromRow(&rows[i].Job)
		if err != nil {
			return nil, err
		}
		job.Result["_storage_key"] = rows[i].CleanupStorageKey
		result = append(result, job)
	}
	return result, nil
}

func (r *syncJobRepo) ClearExportArtifact(ctx context.Context, id domain.ID, storageKey string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row syncJobRow
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_type IN ?", id,
				[]string{string(domain.SyncJobExport), string(domain.SyncJobAuditExport)}).First(&row).Error; err != nil {
			return err
		}
		job, err := syncJobFromRow(&row)
		if err != nil {
			return err
		}
		if job.Result["_storage_key"] == storageKey {
			if err := consumeDownloadPointerTx(tx, &row, job); err != nil {
				return err
			}
		}
		// The key predicate fences a concurrent replacement. Also retire a
		// consumed checkpoint after confirmed deletion even with no job pointer.
		return tx.Where("job_id = ? AND storage_key = ?", id, storageKey).Delete(&artifactRow{}).Error
	})
}
