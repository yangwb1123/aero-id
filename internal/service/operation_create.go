package service

import (
	"context"
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

func isOperationNotFound(err error) bool {
	return errors.Is(err, repository.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound)
}

func validateOperationInput(input *OperationInput) error {
	input.OperationType = strings.ToLower(strings.TrimSpace(input.OperationType))
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.OperationType == "" || !allowedOperationType(input.OperationType) {
		return apperr.BadRequest("operation.invalid_type", "operation type is not allow-listed")
	}
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 200 {
		return apperr.BadRequest("operation.invalid_idempotency_key", "idempotency key is required")
	}
	if len(input.Steps) == 0 || len(input.Steps) > 32 {
		return apperr.BadRequest("operation.invalid_steps", "operation must contain one to thirty-two steps")
	}
	if input.AccountID == (domain.ID{}) {
		return apperr.BadRequest("operation.account_required", "operation account is required")
	}
	return nil
}

func (s *operationService) authorizeOperationCreate(ctx context.Context, actor string, input OperationInput) error {
	if input.OperationType == "replay" {
		if err := s.requireOperationPermission(ctx, actor, domain.PermAuditReplay); err != nil {
			return err
		}
	}
	if err := authorizeAccountTarget(ctx, s.accounts, s.permission, input.AccountID, actor,
		operationPermission(input.OperationType)); err != nil {
		return err
	}
	return s.authorizeEffectiveActions(ctx, actor, input)
}

func (s *operationService) authorizeEffectiveActions(ctx context.Context, actor string, input OperationInput) error {
	permissions, err := operationEffectPermissions(input)
	if err != nil {
		return err
	}
	for _, permission := range permissions {
		if err := s.requireOperationPermission(ctx, actor, permission); err != nil {
			return err
		}
	}
	return nil
}

func (s *operationService) prepareOperation(ctx context.Context, actor, tenantID string,
	input OperationInput) (*domain.Operation, *domain.Operation, error) {
	operation, err := s.prepareOperationCommand(ctx, actor, tenantID, input)
	if err != nil {
		return nil, nil, err
	}
	existing, err := s.repo.FindByIdempotency(ctx, actor, tenantID, operation.IdempotencyKey)
	if err == nil {
		existing, err = reuseOperation(existing, operation.RequestFingerprint)
		return nil, existing, err
	}
	if !isOperationNotFound(err) {
		return nil, nil, apperr.Internal("operation.idempotency_lookup_failed", "failed to check operation idempotency").Wrap(err)
	}
	return operation, nil, nil
}

// PrepareOperationCommand shares Create's validation/authorization/normalization
// without creating a receipt, claiming a lease or contacting any source.
func (s *operationService) PrepareOperationCommand(ctx context.Context, input OperationInput) (*domain.Operation, error) {
	actor, err := mustActor(ctx)
	if err != nil {
		return nil, err
	}
	tenant, err := mustTenant(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.ensureOperationWorkersAccepting(); err != nil {
		return nil, err
	}
	return s.prepareOperationCommand(ctx, actor, tenant, input)
}

func (s *operationService) prepareOperationCommand(ctx context.Context, actor, tenant string, input OperationInput) (*domain.Operation, error) {
	if err := validateOperationInput(&input); err != nil {
		return nil, err
	}
	if err := s.authorizeOperationCreate(ctx, actor, input); err != nil {
		return nil, err
	}
	steps, err := normalizeOperationDatasetScopes(input.Steps)
	if err != nil {
		return nil, err
	}
	input.Steps = steps
	if err := s.resolveOperationRegion(ctx, tenant, &input); err != nil {
		return nil, err
	}
	return buildOperation(ctx, actor, tenant, input, s.region)
}

func (s *operationService) resolveOperationRegion(ctx context.Context, tenantID string, input *OperationInput) error {
	if s.accounts == nil {
		return nil
	}
	account, err := s.accounts.GetByID(ctx, tenantID, input.AccountID)
	if err != nil {
		return mapAccountError(err, "account.lookup_failed", "failed to resolve target account")
	}
	if account == nil {
		return apperr.NotFound("account.not_found", "account not found")
	}
	if account.HomeRegion != s.region {
		return apperr.Conflict("account.home_region_required", "account write must be routed to its home region")
	}
	if input.HomeRegion != "" && input.HomeRegion != account.HomeRegion {
		return apperr.BadRequest("operation.home_region_invalid", "operation home region does not match account home region")
	}
	input.HomeRegion = account.HomeRegion
	return nil
}

func buildOperation(ctx context.Context, actor, tenantID string, input OperationInput, region string) (*domain.Operation, error) {
	requestPayload, err := normalizeOperationPayload(input.RequestPayload)
	if err != nil {
		return nil, err
	}
	if input.HomeRegion == "" {
		input.HomeRegion = region
	}
	steps, err := buildOperationSteps(input)
	if err != nil {
		return nil, err
	}
	operation := &domain.Operation{ID: idgen.NewID(), ActorUID: actor, AccountID: input.AccountID,
		TenantID: tenantID, RequestID: RequestIDFrom(ctx), HomeRegion: input.HomeRegion,
		OperationType: input.OperationType, Status: domain.OperationRequested,
		IdempotencyKey: input.IdempotencyKey, RequestPayload: requestPayload,
		Result: map[string]any{}, Steps: steps}
	operation.RequestFingerprint, err = operationFingerprint(ctx, operation)
	return operation, err
}

func buildOperationSteps(input OperationInput) ([]*domain.OperationStep, error) {
	steps := make([]*domain.OperationStep, 0, len(input.Steps))
	stepKeys := make(map[string]struct{}, len(input.Steps))
	for _, plan := range input.Steps {
		step, err := buildOperationStep(input.OperationType, plan, stepKeys)
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func buildOperationStep(operationType string, plan OperationStepInput, keys map[string]struct{}) (*domain.OperationStep, error) {
	stepKey, source, err := validateOperationStep(operationType, plan, keys)
	if err != nil {
		return nil, err
	}
	if err := validateOperationStepDatasetScope(plan); err != nil {
		return nil, err
	}
	compensation := strings.ToLower(strings.TrimSpace(plan.CompensationAction))
	if compensation != "" && !allowedOperationAction(compensation) {
		return nil, apperr.BadRequest("operation.invalid_compensation", "compensation action is not allow-listed")
	}
	if err := validateOperationCompensationScope(plan); err != nil {
		return nil, err
	}
	payload, err := normalizeOperationPayload(plan.RequestPayload)
	if err != nil {
		return nil, err
	}
	return &domain.OperationStep{StepKey: stepKey, Source: source,
		Action: strings.ToLower(strings.TrimSpace(plan.Action)), Status: domain.StepPending,
		RequestPayload: payload, CompensationAction: compensation}, nil
}

func validateOperationStep(operationType string, plan OperationStepInput,
	keys map[string]struct{}) (string, string, error) {
	stepKey := strings.TrimSpace(plan.StepKey)
	source := strings.TrimSpace(plan.Source)
	if stepKey == "" || source == "" || strings.TrimSpace(plan.Action) == "" || len(stepKey) > 128 || len(source) > 64 {
		return "", "", apperr.BadRequest("operation.invalid_step", "step key, source, and action are required")
	}
	if _, exists := keys[stepKey]; exists {
		return "", "", apperr.BadRequest("operation.duplicate_step", "operation step keys must be unique")
	}
	if !allowedOperationSource(source) || !allowedOperationAction(plan.Action) ||
		!operationActionMatchesType(operationType, plan.Action) {
		return "", "", apperr.BadRequest("operation.invalid_step", "step source or action is not allow-listed")
	}
	keys[stepKey] = struct{}{}
	return stepKey, source, nil
}
