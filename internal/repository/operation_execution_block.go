package repository

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/gorm"
)

// Blocking changes only diagnostic metadata, never receipts, results, step
// outcomes or operation terminality. The fact and metadata commit together.
type OperationExecutionBlockRepository interface {
	BlockExecutionWithAudit(context.Context, domain.ID, domain.OperationStatus, string, string, *domain.AuditFact) error
}

var operationExecutionBlockCodes = []string{"operation.authorization_denied", "operation.authorization_unavailable",
	"operation.execution_scope_invalid", "operation.invalid_type", "operation.invalid_dataset_scope", "account.erased", "account.home_region_required"}

func (r *operationRepo) BlockExecutionWithAudit(ctx context.Context, id domain.ID, expected domain.OperationStatus,
	code, message string, fact *domain.AuditFact) error {
	if fact == nil || fact.OperationID != id || fact.Action != "operation.execution.blocked" {
		return ErrOperationStateConflict
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&operationRow{}).Where("id = ? AND status = ? AND status NOT IN ?", id, string(expected),
			[]string{string(domain.OperationCompleted), string(domain.OperationFailed), string(domain.OperationPartiallyCompleted)}).
			Where("tenant_id = ? AND account_id = ? AND actor_uid = ?", fact.TenantID, fact.AccountID, fact.ActorUID).
			Updates(map[string]any{"error_code": code, "error_message": message, "updated_at": gorm.Expr("clock_timestamp()")})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrOperationStateConflict
		}
		return r.appendAudit(tx, ctx, fact)
	})
}

func paceBlockedOperationQuery(query *gorm.DB) *gorm.DB {
	return query.Where("NOT (error_code IN ? AND updated_at > clock_timestamp() - interval '1 minute')", operationExecutionBlockCodes)
}
