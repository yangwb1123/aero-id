package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SyncJobExportDownloadRepository is the final admission point after bounded
// object IO. It fences disposition and one-time consumption with in-tx audit.
type SyncJobExportDownloadRepository interface {
	AdmitExportDownload(context.Context, domain.ID, string, string, bool, *domain.AuditFact) error
}

func (r *syncJobRepo) AdmitExportDownload(ctx context.Context, id domain.ID, kind, key string,
	oneTime bool, fact *domain.AuditFact) error {
	if fact == nil || r.audit == nil {
		return fmt.Errorf("transactional download audit is required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockExportPublicationTx(tx, id, domain.SyncJobCompleted); err != nil {
			return err
		}
		var row syncJobRow
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND job_type = ? AND status = ?", id, kind, string(domain.SyncJobCompleted)).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		job, err := syncJobFromRow(&row)
		if err != nil {
			return err
		}
		if err := validateDownloadAdmissionTx(tx, job, key, fact); err != nil {
			return err
		}
		if oneTime {
			if err := consumeDownloadPointerTx(tx, &row, job); err != nil {
				return err
			}
		}
		return r.appendFinishedAudit(tx, ctx, fact)
	})
}

func validateDownloadAdmissionTx(tx *gorm.DB, job *domain.SyncJob, key string, fact *domain.AuditFact) error {
	if !downloadScopeMatches(job, key, fact) {
		return ErrNotFound
	}
	var now time.Time
	if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return err
	}
	expires, ok := syncJobExpiry(job.Result["expires_at"])
	if !ok || !now.Before(expires) {
		return ErrNotFound
	}
	return nil
}

func downloadScopeMatches(job *domain.SyncJob, key string, fact *domain.AuditFact) bool {
	return (job.JobType == domain.SyncJobExport || job.JobType == domain.SyncJobAuditExport) &&
		key != "" && job.Result["_storage_key"] == key && fact.TenantID == job.TenantID &&
		fact.AccountID == job.AccountID && fact.ResourceID == job.ID.String()
}

func consumeDownloadPointerTx(tx *gorm.DB, row *syncJobRow, job *domain.SyncJob) error {
	delete(job.Result, "_storage_key")
	job.Result["artifact_deleted_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(job.Result)
	if err != nil {
		return err
	}
	// Keep the internal checkpoint/TTL until storage deletion is confirmed.
	// It is not downloadable without the public job pointer.
	return tx.Model(row).Updates(map[string]any{"result": datatypes.JSON(data), "updated_at": time.Now().UTC()}).Error
}
