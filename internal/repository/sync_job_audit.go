package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aero/aero-id/internal/domain"
)

func (r *syncJobRepo) finish(ctx context.Context, id domain.ID, worker string, status domain.SyncJobStatus,
	code, message string, result map[string]any, fact *domain.AuditFact) error {
	data, err := json.Marshal(resultOrEmpty(result))
	if err != nil {
		return fmt.Errorf("marshal sync result: %w", err)
	}
	now := time.Now().UTC()
	updates := syncJobFinishUpdates(status, code, message, data, now)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockExportPublicationTx(tx, id, status); err != nil {
			return err
		}
		// Lock before checking the lease. clock_timestamp() is required here:
		// now() is fixed at transaction start and can approve a worker that
		// waited past expiry for this row lock.
		var row syncJobRow
		locked := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ? AND status = ? AND lease_owner = ? AND lease_until > clock_timestamp()", id,
				string(domain.SyncJobRunning), worker).First(&row)
		if errors.Is(locked.Error, gorm.ErrRecordNotFound) {
			return ErrSyncJobLeaseLost
		}
		if locked.Error != nil {
			return locked.Error
		}
		if err := tx.Model(&row).Updates(updates).Error; err != nil {
			return err
		}
		return r.appendFinishedAudit(tx, ctx, fact)
	})
}

func syncJobFinishUpdates(status domain.SyncJobStatus, code, message string, result []byte, now time.Time) map[string]any {
	updates := map[string]any{"status": string(status), "error_code": code, "error_message": message,
		"result": datatypes.JSON(result), "lease_owner": "", "lease_until": nil, "lease_kind": "", "updated_at": now,
		"completed_at": nil}
	if status == domain.SyncJobCompleted || status == domain.SyncJobFailed ||
		(status == domain.SyncJobPartiallyComplete && code != "erase.local_projection_failed") {
		updates["completed_at"] = now
	}
	if status == domain.SyncJobUnknown || (status == domain.SyncJobPartiallyComplete && code == "erase.local_projection_failed") {
		// Reconciliation is durable work too. Keep a pacing lease after an
		// unresolved result so the automatic worker cannot hot-loop against a
		// timed-out source or a failed local projection.
		updates["lease_kind"] = "reconcile"
		updates["lease_until"] = gorm.Expr("clock_timestamp() + (? * interval '1 second')", syncUnknownRetryDelay.Seconds())
	}
	return updates
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
