package repository

import (
	"context"
	"strconv"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *operationRepo) expireForwardAttemptTx(tx *gorm.DB, ctx context.Context, operationID domain.ID, now time.Time) (bool, error) {
	var step operationStepRow
	query := tx.Where("operation_steps.operation_id = ? AND operation_steps.status = ? AND "+
		"operation_steps.lease_kind = ? AND operation_steps.lease_until < clock_timestamp()", operationID, string(domain.StepRunning), "forward").
		Where(`NOT EXISTS (SELECT 1 FROM operation_steps previous_step WHERE previous_step.operation_id = operation_steps.operation_id
		AND (previous_step.created_at < operation_steps.created_at OR
		(previous_step.created_at = operation_steps.created_at AND previous_step.id < operation_steps.id))
		AND previous_step.status IN ('pending', 'running', 'unknown', 'failed'))`).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
		Order("lease_until ASC, created_at ASC, id ASC").First(&step)
	if query.Error != nil {
		return false, query.Error
	}
	if err := markUnknownForwardStep(tx, &step, now); err != nil {
		return false, err
	}
	if err := r.appendExpiredAttempt(tx, ctx, &step, false); err != nil {
		return false, err
	}
	return true, nil
}

func (r *operationRepo) appendExpiredAttempt(tx *gorm.DB, ctx context.Context, step *operationStepRow, compensation bool) error {
	var operation operationRow
	if err := tx.Where("id = ?", step.OperationID).Take(&operation).Error; err != nil {
		return err
	}
	action, attempt := "operation.step.expired", step.Attempt
	if compensation {
		action, attempt = "operation.compensation.expired", step.CompensationAttempt
	}
	accountID := domain.ID{}
	if operation.AccountID != nil {
		accountID = *operation.AccountID
	}
	fact := &domain.AuditFact{ID: idgen.NewID(), EventID: idgen.Anchor(action, operation.TenantID, operation.ID.String(), step.ID.String(),
		string(domain.StepUnknown), strconv.Itoa(attempt)), Source: "aero-id", SourceRegion: operation.HomeRegion,
		OperationID: operation.ID, AccountID: accountID, TenantID: operation.TenantID, ActorUID: operation.ActorUID, RequestID: operation.RequestID,
		PartitionKey: operation.TenantID + ":" + accountID.String(), Action: action, ResourceType: "operation", ResourceID: operation.ID.String(),
		OccurredAt: time.Now().UTC(), Payload: map[string]any{"step_id": step.ID.String(), "attempt": attempt, "status": domain.StepUnknown}}
	return r.appendAudit(tx, ctx, fact)
}
