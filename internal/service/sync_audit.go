package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
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

func (s *syncJobService) createWithAudit(ctx context.Context, job *domain.SyncJob) (bool, error) {
	if s.audit == nil {
		return false, s.repo.Create(ctx, job)
	}
	repo, ok := s.repo.(repository.SyncJobAuditRepository)
	if !ok {
		return false, s.repo.Create(ctx, job)
	}
	return true, repo.CreateWithAudit(ctx, job, syncJobRequestedAudit(ctx, job))
}

func syncJobFinishedAudit(ctx context.Context, region string, job *domain.SyncJob, status domain.SyncJobStatus,
	code string, result map[string]any) *domain.AuditFact {
	_ = result
	return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: region, EventID: idgen.NewID(),
		RequestID: job.RequestID, ActorUID: job.ActorUID, AccountID: job.AccountID, TenantID: job.TenantID,
		Action: "sync.job.finished", ResourceType: "sync_job", ResourceID: job.ID.String(), Payload: map[string]any{
			"job_type": job.JobType, "status": status, "error_code": code,
		}})
}
