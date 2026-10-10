package service

import (
	"context"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// PrepareSyncJobCommand shares Create's target/scope checks. It has no writes,
// source calls, claims or audit-export continuation side effects.
func (s *syncJobService) PrepareSyncJobCommand(ctx context.Context, input SyncJobInput) (*domain.SyncJob, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := mustTenant(ctx)
	if err != nil {
		return nil, err
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if err := validateSyncCommandInput(input); err != nil {
		return nil, err
	}
	datasets, err := normalizeSyncJobDatasets(input.JobType, input.Datasets)
	if err != nil {
		return nil, err
	}
	fingerprint, err := syncJobFingerprint(ctx, input, datasets)
	if err != nil {
		return nil, err
	}
	return s.prepareNewSyncJob(ctx, input, datasets, fingerprint, actor, tenant)
}

func validateSyncCommandInput(input SyncJobInput) error {
	if input.JobType != domain.SyncJobSync && input.JobType != domain.SyncJobExport && input.JobType != domain.SyncJobErase {
		return apperr.BadRequest("command.invalid_kind", "command kind is not supported")
	}
	if input.AccountID == (domain.ID{}) || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 200 || len(input.Datasets) == 0 {
		return apperr.BadRequest("command.invalid_scope", "explicit command target and dataset scope are required")
	}
	return nil
}

func (s *syncJobService) prepareNewSyncJob(ctx context.Context, input SyncJobInput, datasets []domain.AccountDataset,
	fingerprint, actor, tenant string) (*domain.SyncJob, error) {
	if input.AccountID == (domain.ID{}) {
		view, err := s.accounts.GetMe(ctx)
		if err != nil {
			return nil, err
		}
		input.AccountID = view.Account.ID
	}
	if err := authorizeAccountTarget(ctx, s.accountRepo, s.permission, input.AccountID, actor, syncPermission(input.JobType)); err != nil {
		return nil, err
	}
	if err := requireHomeRegion(ctx, s.accountRepo, input.AccountID, s.region); err != nil {
		return nil, err
	}
	return &domain.SyncJob{ID: idgen.NewID(), AccountID: input.AccountID, ActorUID: actor, TenantID: tenant,
		RequestID: RequestIDFrom(ctx), JobType: input.JobType, Status: domain.SyncJobPending,
		IdempotencyKey: input.IdempotencyKey, RequestFingerprint: fingerprint,
		Request: map[string]any{"datasets": datasets}, Result: map[string]any{}}, nil
}
