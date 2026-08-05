package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
)

func (r *operationRepo) finishStep(ctx context.Context, stepID domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, errorCode string, result map[string]any, errorMessage string, fact *domain.AuditFact) error {
	data, err := jsonMap(result)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var step operationStepRow
		if err := tx.Where("id = ?", stepID).First(&step).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		attemptKind := step.LeaseKind
		if attemptKind != "reconcile" {
			attemptKind = "forward"
		}
		// A reconcile attempt that still cannot determine the outcome keeps a
		// short lease instead of clearing it: ClaimUnknownStep only returns
		// unknown steps with an expired/nil lease, so this paces re-queries and
		// prevents a hot loop while the source stays uncertain (P6-09).
		leaseUntil := (*time.Time)(nil)
		if status == domain.StepUnknown && attemptKind == "reconcile" {
			until := now.Add(ReconcileUnknownRetryDelay)
			leaseUntil = &until
		}
		updated := tx.Model(&operationStepRow{}).Where("id = ? AND status = ? AND lease_owner = ?", stepID,
			string(domain.StepRunning), worker).Updates(map[string]any{"status": string(status),
			"source_operation_id": sourceOperationID, "error_code": errorCode, "error_message": errorMessage,
			"result": data, "lease_owner": "", "lease_until": leaseUntil, "lease_kind": "", "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrOperationStepLeaseLost
		}
		finished := now
		if err := tx.Model(&operationAttemptRow{}).
			Where("step_id = ? AND attempt = ? AND attempt_kind = ? AND status = ?", stepID, step.Attempt, attemptKind, string(domain.StepRunning)).
			Updates(map[string]any{"status": string(status), "source_operation_id": sourceOperationID,
				"error_code": errorCode, "finished_at": &finished}).Error; err != nil {
			return err
		}
		return r.appendAudit(tx, ctx, fact)
	})
}

func (r *operationRepo) finishCompensationStep(ctx context.Context, stepID domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, errorCode string, result map[string]any, errorMessage string, fact *domain.AuditFact) error {
	data, err := jsonMap(result)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var step operationStepRow
		if err := tx.Where("id = ?", stepID).First(&step).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		updated := tx.Model(&operationStepRow{}).Where("id = ? AND compensation_status = ? AND compensation_lease_owner = ?", stepID,
			string(domain.StepRunning), worker).Updates(map[string]any{"compensation_status": string(status),
			"compensation_source_operation_id": sourceOperationID, "compensation_error_code": errorCode,
			"compensation_error_message": errorMessage, "compensation_result": data,
			"compensation_lease_owner": "", "compensation_lease_until": nil, "compensation_updated_at": now, "updated_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrOperationStepLeaseLost
		}
		finished := now
		if err := tx.Model(&operationAttemptRow{}).
			Where("step_id = ? AND attempt = ? AND attempt_kind = ? AND status = ?",
				stepID, step.CompensationAttempt, "compensation", string(domain.StepRunning)).
			Updates(map[string]any{"status": string(status), "source_operation_id": sourceOperationID,
				"error_code": errorCode, "finished_at": &finished}).Error; err != nil {
			return err
		}
		return r.appendAudit(tx, ctx, fact)
	})
}

func (r *operationRepo) setStatus(ctx context.Context, id domain.ID, status domain.OperationStatus, code, message string,
	result map[string]any, fact *domain.AuditFact) error {
	data, err := jsonMap(result)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	updates := map[string]any{"status": string(status), "error_code": code, "error_message": message,
		"result": data, "updated_at": now, "completed_at": nil}
	if status == domain.OperationCompleted || status == domain.OperationFailed || status == domain.OperationPartiallyCompleted {
		updates["completed_at"] = now
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&operationRow{}).Where("id = ?", id).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return r.appendAudit(tx, ctx, fact)
	})
}

func (r *operationRepo) appendAudit(tx *gorm.DB, ctx context.Context, fact *domain.AuditFact) error {
	if fact == nil {
		return nil
	}
	if r.audit == nil {
		return fmt.Errorf("transactional audit repository is not configured")
	}
	return r.audit.AppendInTx(ctx, tx, fact)
}
