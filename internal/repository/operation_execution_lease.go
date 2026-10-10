package repository

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
)

// This is a bounded local pre-effect check, not a transaction with a remote
// source. The source still rechecks its resource ACL and fencing contract.
type OperationExecutionLeaseRepository interface {
	CheckExecutionLease(context.Context, domain.ID, domain.ID, string, bool, bool) error
}

func (r *operationRepo) CheckExecutionLease(ctx context.Context, operationID, stepID domain.ID, owner string, compensation, reconcile bool) error {
	query := r.db.WithContext(ctx).Model(&operationStepRow{}).Where("id = ? AND operation_id = ?", stepID, operationID)
	if compensation {
		query = query.Where("compensation_status = ? AND compensation_lease_owner = ? AND compensation_lease_until > clock_timestamp()",
			string(domain.StepRunning), owner)
	} else {
		kind := "forward"
		if reconcile {
			kind = "reconcile"
		}
		query = query.Where("status = ? AND lease_owner = ? AND lease_kind = ? AND lease_until > clock_timestamp()", string(domain.StepRunning), owner, kind)
	}
	query = query.Where("EXISTS (SELECT 1 FROM operations execution JOIN accounts target ON target.id = execution.account_id "+
		"AND target.tenant_id = execution.tenant_id JOIN region_directory directory ON directory.account_id = target.id "+
		"AND directory.tenant_id = target.tenant_id JOIN region_epochs epoch ON epoch.region = directory.home_region "+
		"WHERE execution.id = ? AND target.status <> ? AND execution.home_region = target.home_region "+
		"AND target.home_region = directory.home_region AND directory.fenced_epoch = epoch.epoch "+
		"AND execution.status NOT IN ?)", operationID, string(domain.AccountErased),
		[]string{string(domain.OperationCompleted), string(domain.OperationFailed), string(domain.OperationPartiallyCompleted)})
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrOperationStateConflict
	}
	return nil
}
