package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

func (r *syncJobRepo) finish(ctx context.Context, id domain.ID, worker string, status domain.SyncJobStatus,
	code, message string, result map[string]any, fact *domain.AuditFact) error {
	data, err := json.Marshal(resultOrEmpty(result))
	if err != nil {
		return fmt.Errorf("marshal sync result: %w", err)
	}
	now := time.Now().UTC()
	updates := map[string]any{"status": string(status), "error_code": code, "error_message": message,
		"result": datatypes.JSON(data), "lease_owner": "", "lease_until": nil, "lease_kind": "", "updated_at": now,
		"completed_at": nil}
	if status == domain.SyncJobCompleted || status == domain.SyncJobPartiallyComplete || status == domain.SyncJobFailed {
		updates["completed_at"] = now
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&syncJobRow{}).Where("id = ? AND status = ? AND lease_owner = ?", id,
			string(domain.SyncJobRunning), worker).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrSyncJobLeaseLost
		}
		return r.appendFinishedAudit(tx, ctx, fact)
	})
}

// appendFinishedAudit appends the sync.job.finished fact inside the transaction.
// ErrAuditDuplicate is swallowed in-tx (contract §4.2): a duplicate append
// implies the retried finish already committed with identical anchor inputs,
// so the retry re-applies a no-op-equivalent idempotent UPDATE; the swallow
// runs after the domain write inside the same tx, so it can never mask a
// failed domain write.
func (r *syncJobRepo) appendFinishedAudit(tx *gorm.DB, ctx context.Context, fact *domain.AuditFact) error {
	if fact == nil {
		return nil
	}
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
		return fmt.Errorf("append sync job audit: %w", err)
	}
	return nil
}
