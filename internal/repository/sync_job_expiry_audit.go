package repository

import (
	"strconv"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *syncJobRepo) expireSyncExecutionTx(tx *gorm.DB, region string) error {
	var row syncJobRow
	if err := regionalSyncJobQuery(tx, region, "status = ? AND lease_kind = ? AND lease_until < clock_timestamp()", string(domain.SyncJobRunning), "execute").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
		Order("lease_until ASC, created_at ASC, id ASC").First(&row).Error; err != nil {
		return err
	}
	if err := tx.Model(&row).Updates(map[string]any{"status": string(domain.SyncJobUnknown), "error_code": "sync.unknown_outcome",
		"error_message": "worker lease expired before the result was recorded", "lease_owner": "", "lease_until": nil,
		"lease_kind": "", "updated_at": gorm.Expr("clock_timestamp()")}).Error; err != nil {
		return err
	}
	var home string
	if err := tx.Table("accounts").Select("home_region").Where("id = ? AND tenant_id = ?", row.AccountID, row.TenantID).Scan(&home).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	fact := &domain.AuditFact{ID: idgen.NewID(), EventID: idgen.Anchor("sync.execution.expired", row.TenantID, row.ID.String(), strconv.Itoa(row.Attempt)),
		Source: "aero-id", SourceRegion: home, AccountID: row.AccountID, ActorUID: row.ActorUID, TenantID: row.TenantID,
		RequestID: row.RequestID, Action: "sync.execution.expired", ResourceType: "sync_job", ResourceID: row.ID.String(),
		PartitionKey: row.TenantID + ":" + row.AccountID.String(), OccurredAt: now,
		Payload: map[string]any{"job_type": row.JobType, "attempt": row.Attempt, "status": "unknown"}}
	return r.appendFinishedAudit(tx, tx.Statement.Context, fact)
}
