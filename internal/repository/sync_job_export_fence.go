package repository

import (
	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
)

// Account -> job is shared with erase and final download admission. Object IO
// happens outside this short transaction; only publication is linearized here.
func lockExportPublicationTx(tx *gorm.DB, id domain.ID, status domain.SyncJobStatus) error {
	if status != domain.SyncJobCompleted {
		return nil
	}
	var job syncJobRow
	if err := tx.Select("account_id", "tenant_id", "job_type").Where("id = ?", id).Take(&job).Error; err != nil {
		return err
	}
	if job.JobType != string(domain.SyncJobExport) && job.JobType != string(domain.SyncJobAuditExport) {
		return nil
	}
	return lockExportAccountTx(tx, &job)
}

func lockExportAccountTx(tx *gorm.DB, job *syncJobRow) error {
	account, err := lockAccountDispositionTx(tx, job.AccountID)
	if err != nil {
		return err
	}
	if account.TenantID != job.TenantID {
		return gorm.ErrRecordNotFound
	}
	if account.Status == string(domain.AccountErased) {
		return ErrAccountErased
	}
	return nil
}
