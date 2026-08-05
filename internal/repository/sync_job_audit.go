package repository

import (
	"context"
	"encoding/json"
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
		if fact == nil {
			return nil
		}
		if r.audit == nil {
			return fmt.Errorf("transactional audit repository is not configured")
		}
		return r.audit.AppendInTx(ctx, tx, fact)
	})
}
