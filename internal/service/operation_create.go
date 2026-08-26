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
	// source.command and replay are generic workflow envelopes. Their
	// authorization must follow the effect of every step; operation.manage or
	// audit.replay alone is not permission to smuggle an erase through them.
	if input.OperationType != "source.command" && input.OperationType != "replay" {
		return nil
	}
	seen := make(map[string]struct{}, len(input.Steps))
	for _, step := range input.Steps {
		permission := operationActionPermission(step.Action)
		if permission == "" {
			return apperr.BadRequest("operation.invalid_action", "operation action is not allow-listed")
		}
		// Generic source.command/replay envelopes require the permission implied
		// by every actual remote effect, including reads/syncs and erases.
		if _, ok := seen[permission]; ok {
			continue
		}
		seen[permission] = struct{}{}
		if err := s.requireOperationPermission(ctx, actor, permission); err != nil {
			return err
		}
	}
	if input.OperationType == "source.command" {
		return s.requireOperationPermission(ctx, actor, domain.PermOperationManage)
	}
	return nil
}

func (s *operationService) prepareOperation(ctx context.Context, actor, tenantID string,
	input OperationInput) (*domain.Operation, *domain.Operation, error) {
	if err := validateOperationInput(&input); err != nil {
		return nil, nil, err
	}
	if err := s.authorizeOperationCreate(ctx, actor, input); err != nil {
		return nil, nil, err
	}
	normalizedSteps, err := normalizeOperationDatasetScopes(input.Steps)
	if err != nil {
		return nil, nil, err
	}
	input.Steps = normalizedSteps
	existing, err := s.repo.FindByIdempotency(ctx, actor, tenantID, input.IdempotencyKey)
	if err == nil {
		return nil, existing, nil
	}
	if !isOperationNotFound(err) {
		return nil, nil, apperr.Internal("operation.idempotency_lookup_failed", "failed to check operation idempotency").Wrap(err)
	}
	if err := s.resolveOperationRegion(ctx, tenantID, &input); err != nil {
		return nil, nil, err
	}
	operation, err := buildOperation(ctx, actor, tenantID, input, s.region)
	return operation, nil, err
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
	return &domain.Operation{ID: idgen.NewID(), ActorUID: actor, AccountID: input.AccountID,
		TenantID: tenantID, RequestID: RequestIDFrom(ctx), HomeRegion: input.HomeRegion,
		OperationType: input.OperationType, Status: domain.OperationRequested,
		IdempotencyKey: input.IdempotencyKey, RequestPayload: requestPayload,
		Result: map[string]any{}, Steps: steps}, nil
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
