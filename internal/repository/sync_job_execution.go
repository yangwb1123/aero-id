package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// SyncJobExecutionRepository provides local admission and audited deferral.
// BeforeEffects is only valid at entry, before reading content/calling a source.
// Even then, existing durable recovery state prevents restoring pending.
type SyncJobExecutionRepository interface {
	CheckExecutionLease(context.Context, *domain.SyncJob, string, bool) error
	DeferExecutionWithAudit(context.Context, SyncJobExecutionDeferral, *domain.AuditFact) error
}

type SyncJobExecutionDeferral struct {
	Job           *domain.SyncJob
	BeforeEffects bool
	Code, Message string
	Result        map[string]any // nil preserves the authoritative result verbatim
}

func (r *syncJobRepo) CheckExecutionLease(ctx context.Context, job *domain.SyncJob, region string, allowErased bool) error {
	if job == nil || (allowErased && job.JobType != domain.SyncJobErase) {
		return ErrSyncJobLeaseLost
	}
	query := r.db.WithContext(ctx).Model(&syncJobRow{}).Where("id = ? AND tenant_id = ? AND account_id = ? AND actor_uid = ? AND job_type = ?",
		job.ID, job.TenantID, job.AccountID, job.ActorUID, string(job.JobType)).
		Where("status = ? AND lease_owner = ? AND lease_kind = ? AND lease_until > clock_timestamp()",
			string(domain.SyncJobRunning), job.LeaseOwner, job.LeaseKind).
		Where("EXISTS (SELECT 1 FROM accounts target JOIN region_directory directory ON directory.account_id = target.id "+
			"AND directory.tenant_id = target.tenant_id JOIN region_epochs epoch ON epoch.region = directory.home_region "+
			"WHERE target.id = sync_jobs.account_id AND target.tenant_id = sync_jobs.tenant_id AND target.home_region = ? "+
			"AND directory.home_region = target.home_region AND directory.fenced_epoch = epoch.epoch)", region)
	if !allowErased {
		query = query.Where("EXISTS (SELECT 1 FROM accounts target WHERE target.id = sync_jobs.account_id AND target.status <> ?)",
			string(domain.AccountErased))
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrSyncJobLeaseLost
	}
	return nil
}

func (r *syncJobRepo) DeferExecutionWithAudit(ctx context.Context, input SyncJobExecutionDeferral, fact *domain.AuditFact) error {
	if !validSyncExecutionDeferral(input, fact) {
		return ErrSyncReplayStateInvalid
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := r.lockRecoveryJob(tx, input.Job.ID, input.Job.LeaseOwner)
		if err != nil {
			return err
		}
		if !sameSyncExecutionRow(row, input.Job) {
			return ErrSyncJobLeaseLost
		}
		status, err := syncExecutionDeferralStatus(tx, row, input.BeforeEffects)
		if err != nil {
			return err
		}
		updates := map[string]any{"status": string(status), "error_code": input.Code, "error_message": input.Message, "completed_at": nil,
			"lease_owner": "", "lease_kind": "", "lease_until": gorm.Expr("clock_timestamp() + interval '1 minute'"),
			"updated_at": gorm.Expr("clock_timestamp()")}
		if input.Result != nil {
			data, err := json.Marshal(input.Result)
			if err != nil {
				return fmt.Errorf("marshal deferred sync metadata: %w", err)
			}
			updates["result"] = datatypes.JSON(data)
		}
		if err := tx.Model(row).Updates(updates).Error; err != nil {
			return err
		}
		fact.Payload["status"] = status
		return r.appendFinishedAudit(tx, ctx, fact)
	})
}

func syncExecutionDeferralStatus(tx *gorm.DB, row *syncJobRow, beforeEffects bool) (domain.SyncJobStatus, error) {
	if !beforeEffects || row.LeaseKind != "execute" {
		return domain.SyncJobUnknown, nil
	}
	started, err := syncExecutionHasRecovery(tx, row)
	if err != nil || started {
		return domain.SyncJobUnknown, err
	}
	return domain.SyncJobPending, nil
}

func validSyncExecutionDeferral(input SyncJobExecutionDeferral, fact *domain.AuditFact) bool {
	if input.Job == nil || fact == nil || fact.Action != "sync.execution.blocked" {
		return false
	}
	return fact.ResourceID == input.Job.ID.String() && fact.TenantID == input.Job.TenantID &&
		fact.ActorUID == input.Job.ActorUID && fact.AccountID == input.Job.AccountID && fact.Payload != nil
}
func sameSyncExecutionRow(row *syncJobRow, job *domain.SyncJob) bool {
	return row.TenantID == job.TenantID && row.AccountID == job.AccountID && row.ActorUID == job.ActorUID &&
		row.JobType == string(job.JobType) && row.LeaseKind == job.LeaseKind
}
func syncExecutionHasRecovery(tx *gorm.DB, row *syncJobRow) (bool, error) {
	var started bool
	err := tx.Raw("SELECT (?::jsonb <> '{}'::jsonb) OR EXISTS (SELECT 1 FROM sync_job_artifacts WHERE job_id = ?) OR "+
		"EXISTS (SELECT 1 FROM sync_job_summary_replays WHERE job_id = ?) OR EXISTS (SELECT 1 FROM sync_job_source_outcomes WHERE job_id = ?)",
		string(row.Result), row.ID, row.ID, row.ID).Scan(&started).Error
	return started, err
}

func pendingSyncExecutionQuery(tx *gorm.DB, region string) *gorm.DB {
	return regionalSyncJobQuery(tx, region, "status = ? AND (lease_until IS NULL OR lease_until < clock_timestamp())", string(domain.SyncJobPending))
}
