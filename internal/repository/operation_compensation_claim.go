package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *operationRepo) claimCompensationStep(ctx context.Context, operationID domain.ID, worker string,
	lease time.Duration, reconcile bool) (*domain.OperationStep, error) {
	var result *domain.OperationStep
	unknown := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row operationStepRow
		if err := compensationClaimQuery(tx, operationID, reconcile).First(&row).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if row.CompensationStatus == string(domain.StepRunning) {
			if err := markUnknownCompensationTx(tx, &row, now); err != nil {
				return err
			}
			if err := r.appendExpiredAttempt(tx, ctx, &row, true); err != nil {
				return err
			}
			if !reconcile {
				unknown = true
				return nil
			}
		}
		var err error
		result, err = claimCompensationTx(tx, &row, worker, lease, now)
		return err
	})
	if err == nil && unknown {
		return nil, ErrOperationStepUnknown
	}
	return result, err
}

func compensationClaimQuery(tx *gorm.DB, operationID domain.ID, reconcile bool) *gorm.DB {
	status := domain.StepPending
	if reconcile {
		status = domain.StepUnknown
	}
	query := tx.Where("operation_id = ? AND status = ? AND compensation_action <> ''", operationID, string(domain.StepSucceeded))
	if !reconcile {
		query = query.Where(`NOT EXISTS (SELECT 1 FROM operation_steps earlier_compensation
		WHERE earlier_compensation.operation_id = operation_steps.operation_id AND earlier_compensation.status = 'succeeded'
		AND earlier_compensation.compensation_action <> '' AND earlier_compensation.compensation_status IN ('pending','running','unknown','failed')
		AND (earlier_compensation.created_at > operation_steps.created_at OR
		(earlier_compensation.created_at = operation_steps.created_at AND earlier_compensation.id > operation_steps.id)))`)
	}
	return query.
		Where("((compensation_status = ? AND (compensation_lease_until IS NULL OR compensation_lease_until < clock_timestamp())) OR "+
			"(compensation_status = ? AND compensation_lease_until < clock_timestamp()))", string(status), string(domain.StepRunning)).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).Order("created_at DESC, id DESC")
}

func claimCompensationTx(tx *gorm.DB, row *operationStepRow, worker string, lease time.Duration, now time.Time) (*domain.OperationStep, error) {
	until := now.Add(lease)
	if err := tx.Model(row).Updates(map[string]any{"compensation_status": string(domain.StepRunning),
		"compensation_attempt": gorm.Expr("compensation_attempt + 1"), "compensation_lease_owner": worker,
		"compensation_lease_until": databaseLeaseUntil(lease), "compensation_updated_at": now, "updated_at": now}).Error; err != nil {
		return nil, err
	}
	row.CompensationStatus, row.CompensationLeaseOwner, row.CompensationLeaseUntil = string(domain.StepRunning), worker, &until
	row.CompensationAttempt++
	row.CompensationUpdatedAt = &now
	attempt := &operationAttemptRow{ID: idgen.NewID(), OperationID: row.OperationID, StepID: row.ID,
		Attempt: row.CompensationAttempt, AttemptKind: "compensation", Status: string(domain.StepRunning), StartedAt: now}
	if err := tx.Create(attempt).Error; err != nil {
		return nil, fmt.Errorf("create compensation attempt: %w", err)
	}
	return operationStepRowToDomain(row)
}

func markUnknownCompensationTx(tx *gorm.DB, row *operationStepRow, now time.Time) error {
	if err := tx.Model(row).Updates(map[string]any{"compensation_status": string(domain.StepUnknown),
		"compensation_error_code":    "operation.compensation_unknown_outcome",
		"compensation_error_message": "worker lease expired before the source outcome was recorded",
		"compensation_lease_owner":   "", "compensation_lease_until": nil, "compensation_updated_at": now, "updated_at": now}).Error; err != nil {
		return err
	}
	row.CompensationStatus, row.CompensationLeaseOwner, row.CompensationLeaseUntil = string(domain.StepUnknown), "", nil
	return tx.Model(&operationAttemptRow{}).Where("step_id = ? AND attempt = ? AND attempt_kind = ? AND status = ?",
		row.ID, row.CompensationAttempt, "compensation", string(domain.StepRunning)).Updates(map[string]any{
		"status": string(domain.StepUnknown), "error_code": "operation.compensation_unknown_outcome", "finished_at": now}).Error
}
