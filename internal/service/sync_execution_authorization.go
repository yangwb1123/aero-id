package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

const syncAuthorizationBudget = 3 * time.Second

// The stored initiator must hold current authority grants. No worker/operator
// privileges, historical tokens, fabricated JWT identity or ownership fallback.
func (s *syncJobService) checkSyncExecution(ctx context.Context, job *domain.SyncJob, allowErased bool) error {
	ctx, cancel := context.WithTimeout(ctx, syncAuthorizationBudget)
	defer cancel()
	if err := s.syncExecutionAccount(ctx, job, allowErased); err != nil {
		return err
	}
	permissions := []string{syncPermission(job.JobType)}
	if job.JobType == domain.SyncJobAuditExport {
		permissions = append(permissions, domain.PermAuditRead)
	}
	for _, permission := range permissions {
		allowed, err := s.executionPermission.HasPermission(ctx, job.ActorUID, permission)
		if err != nil || ctx.Err() != nil {
			return apperr.Unavailable("sync.authorization_unavailable", "current sync authorization is unavailable")
		}
		if !allowed {
			return apperr.Forbidden("sync.authorization_denied", "current sync action is not authorized")
		}
	}
	repo, ok := s.repo.(repository.SyncJobExecutionRepository)
	if !ok {
		return apperr.Unavailable("sync.execution_lease_unavailable", "sync execution lease cannot be verified")
	}
	if err := repo.CheckExecutionLease(ctx, job, s.region, allowErased); err != nil || ctx.Err() != nil {
		return apperr.Unavailable("sync.execution_lease_lost", "sync execution lease cannot be verified; query the original receipt")
	}
	return nil
}

func (s *syncJobService) syncExecutionAccount(ctx context.Context, job *domain.SyncJob, allowErased bool) error {
	if !validSyncExecutionScope(job) {
		return apperr.Forbidden("sync.execution_scope_invalid", "durable sync scope is unavailable")
	}
	if isNilExecutionDependency(s.accountRepo) || isNilExecutionDependency(s.executionPermission) {
		return apperr.Unavailable("sync.authorization_unavailable", "current sync authorization is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, job.TenantID, job.AccountID)
	if err != nil {
		return apperr.Unavailable("sync.authorization_unavailable", "current sync target is unavailable")
	}
	if !sameSyncExecutionAccount(account, job) {
		return apperr.Forbidden("sync.execution_scope_invalid", "durable sync scope is unavailable")
	}
	if account.Status == domain.AccountErased && !allowErased {
		return apperr.Forbidden("account.erased", "account is erased")
	}
	if account.HomeRegion != s.region {
		return apperr.Conflict("account.home_region_required", "account write must be routed to its home region")
	}
	return nil
}
func validSyncExecutionScope(job *domain.SyncJob) bool {
	return job != nil && job.AccountID != (domain.ID{}) && strings.TrimSpace(job.ActorUID) != "" &&
		strings.TrimSpace(job.TenantID) != "" && validSyncJobType(job.JobType)
}
func sameSyncExecutionAccount(account *domain.Account, job *domain.SyncJob) bool {
	return account != nil && account.ID == job.AccountID && account.TenantID == job.TenantID
}

func (s *syncJobService) pauseSyncExecution(ctx context.Context, job *domain.SyncJob, cause error,
	beforeEffects bool, result map[string]any) (*domain.SyncJob, error) {
	repo, ok := s.repo.(repository.SyncJobExecutionRepository)
	if !ok {
		return nil, apperr.Unavailable("sync.execution_audit_unavailable", "sync execution blocking is unavailable")
	}
	var app *apperr.AppError
	if !errors.As(cause, &app) {
		return nil, apperr.Unavailable("sync.authorization_unavailable", "current sync authorization is unavailable")
	}
	if job.JobType == domain.SyncJobExport || job.JobType == domain.SyncJobAuditExport {
		if result == nil {
			result = job.Result
		}
		result = artifactResultMetadata(job.JobType, result)
	}
	fact := buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: s.region, AccountID: job.AccountID, ActorUID: job.ActorUID,
		TenantID: job.TenantID, RequestID: job.RequestID, Action: "sync.execution.blocked", ResourceType: "sync_job", ResourceID: job.ID.String(),
		Payload: map[string]any{"job_type": job.JobType, "error_code": app.Code, "attempt": job.Attempt}})
	fact.TenantID = job.TenantID
	fact.PartitionKey = job.TenantID + ":" + job.AccountID.String()
	fact.EventID = idgen.Anchor("sync.execution.blocked", job.TenantID, job.ID.String(), app.Code, fact.ID.String())
	persistCtx, cancel := syncPersistenceContext(ctx)
	defer cancel()
	input := repository.SyncJobExecutionDeferral{Job: job, BeforeEffects: beforeEffects, Code: app.Code, Message: app.Message, Result: result}
	if err := repo.DeferExecutionWithAudit(persistCtx, input, fact); err != nil {
		return nil, apperr.Unavailable("sync.execution_defer_unknown", "sync deferral outcome is unavailable; query the original receipt").Wrap(err)
	}
	return nil, cause
}

func isSyncExecutionBlocked(err error) bool {
	var app *apperr.AppError
	if !errors.As(err, &app) {
		return false
	}
	return strings.HasPrefix(app.Code, "sync.authorization_") || strings.HasPrefix(app.Code, "sync.execution_lease_") ||
		app.Code == "sync.execution_scope_invalid" || app.Code == "account.erased" || app.Code == "account.home_region_required"
}
