package repository

import (
	"context"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
)

func (r *operationRepo) ListRunnable(ctx context.Context, limit int) ([]domain.ID, error) {
	if limit <= 0 {
		limit = 16
	}
	var rows []struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	err := r.db.WithContext(ctx).Table("operations").Select("DISTINCT operations.id AS id").
		Where("operations.status IN ? AND EXISTS (SELECT 1 FROM operation_steps candidate WHERE candidate.operation_id = operations.id AND "+
			"((candidate.status = ? AND NOT EXISTS (SELECT 1 FROM operation_steps previous_step WHERE previous_step.operation_id = candidate.operation_id "+
			"AND (previous_step.created_at < candidate.created_at OR (previous_step.created_at = candidate.created_at AND previous_step.id < candidate.id)) "+
			"AND previous_step.status IN ('pending', 'running', 'unknown', 'failed'))) OR "+
			"(candidate.status = ? AND candidate.lease_kind = ? AND candidate.lease_until < now())))",
			[]string{string(domain.OperationRequested), string(domain.OperationRunning), string(domain.OperationWaiting)},
			string(domain.StepPending), string(domain.StepRunning), "forward").
		Order("operations.id ASC").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]domain.ID, len(rows))
	for i := range rows {
		ids[i] = domain.ID(rows[i].ID)
	}
	return ids, nil
}

func (r *operationRepo) ListReconcileRunnable(ctx context.Context, limit int) ([]domain.ID, error) {
	if limit <= 0 {
		limit = 16
	}
	var rows []struct {
		ID uuid.UUID `gorm:"column:id"`
	}
	unknown := string(domain.StepUnknown)
	running := string(domain.StepRunning)
	err := r.db.WithContext(ctx).Table("operations").Select("DISTINCT operations.id AS id").
		Where("operations.status IN ?", []string{string(domain.OperationUnknown), string(domain.OperationCompensating)}).
		Where("EXISTS (SELECT 1 FROM operation_steps candidate WHERE candidate.operation_id = operations.id AND "+
			"((candidate.status = ?) OR (candidate.status = ? AND candidate.lease_kind = ? AND candidate.lease_until < now()) OR "+
			"(candidate.status = ? AND candidate.compensation_action <> '' AND candidate.compensation_status = ?) OR "+
			"(candidate.status = ? AND candidate.compensation_action <> '' AND candidate.compensation_status = ? AND "+
			"candidate.compensation_lease_until < now())))",
			unknown, running, "reconcile", string(domain.StepSucceeded), unknown, string(domain.StepSucceeded), running).
		Order("operations.id ASC").Limit(limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]domain.ID, len(rows))
	for i := range rows {
		ids[i] = domain.ID(rows[i].ID)
	}
	return ids, nil
}
