package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func syncJobRequestedAudit(ctx context.Context, job *domain.SyncJob) *domain.AuditFact {
	return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", EventID: job.ID, RequestID: job.RequestID,
		ActorUID: job.ActorUID, AccountID: job.AccountID, TenantID: job.TenantID, Action: "sync.job.requested",
		ResourceType: "sync_job", ResourceID: job.ID.String(), Payload: map[string]any{
			"job_type": job.JobType, "datasets": datasetsFromRequest(job.Request),
		}})
}

func (s *syncJobService) createWithAudit(ctx context.Context, job *domain.SyncJob) error {
	if s.audit == nil {
		return s.repo.Create(ctx, job)
	}
	repo, ok := s.repo.(repository.SyncJobAuditRepository)
	if !ok {
		// Fail closed, matching operation.createWithAudit: a repo without the
		// WithAudit surface must not silently fall back to a plain Create that
		// drops the sync.job.requested fact.
		return apperr.Internal("sync.audit_unavailable", "sync job audit persistence is unavailable")
	}
	return repo.CreateWithAudit(ctx, job, syncJobRequestedAudit(ctx, job))
}

// syncJobFinishedAudit is the SINGLE minting point for sync.job.finished facts
// (contract §2.3 / D-7): both the in-tx FinishWithAudit path and the fallback
// Record path derive the same deterministic event_id from
// ["sync.job.finished", tenant, job, status], so a re-driven finish dedups at
// the ledger regardless of which path runs, and a status-changing retry
// records its own fact (out-of-order invariant §4.4.3).
func syncJobFinishedAudit(ctx context.Context, region string, job *domain.SyncJob, status domain.SyncJobStatus,
	code string, result map[string]any) *domain.AuditFact {
	_ = result
	return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: region,
		EventID:   idgen.Anchor("sync.job.finished", job.TenantID, job.ID.String(), string(status)),
		RequestID: job.RequestID, ActorUID: job.ActorUID, AccountID: job.AccountID, TenantID: job.TenantID,
		Action: "sync.job.finished", ResourceType: "sync_job", ResourceID: job.ID.String(), Payload: map[string]any{
			"job_type": job.JobType, "status": status, "error_code": code,
		}})
}
