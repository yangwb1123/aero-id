package service

import (
	"context"
	"strings"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
)

func operationEffectPermissions(input OperationInput) ([]string, error) {
	generic := input.OperationType == "source.command" || input.OperationType == "replay"
	seen := make(map[string]bool)
	permissions := make([]string, 0, 3)
	for _, step := range input.Steps {
		for _, action := range []string{step.Action, step.CompensationAction} {
			if strings.TrimSpace(action) == "" {
				continue
			}
			permission := operationActionPermission(action)
			if permission == "" {
				return nil, apperr.BadRequest("operation.invalid_action", "operation action is not allow-listed")
			}
			// Typed self-service envelopes already authorize their main effect.
			// A differently-privileged compensation is never covered by it.
			if !generic && permission == operationPermission(input.OperationType) {
				continue
			}
			if !seen[permission] {
				seen[permission] = true
				permissions = append(permissions, permission)
			}
		}
	}
	if input.OperationType == "source.command" {
		permissions = append(permissions, domain.PermOperationManage)
	}
	return permissions, nil
}

func validateOperationCompensationScope(step OperationStepInput) error {
	if strings.TrimSpace(step.CompensationAction) == "" {
		return nil
	}
	step.Action = step.CompensationAction
	return validateOperationStepDatasetScope(step)
}

func operationInputFromPersisted(operation *domain.Operation) OperationInput {
	input := OperationInput{AccountID: operation.AccountID, OperationType: operation.OperationType,
		HomeRegion: operation.HomeRegion, RequestPayload: operation.RequestPayload, IdempotencyKey: operation.IdempotencyKey}
	for _, step := range operation.Steps {
		if step == nil {
			continue
		}
		input.Steps = append(input.Steps, OperationStepInput{StepKey: step.StepKey, Source: step.Source, Action: step.Action,
			RequestPayload: step.RequestPayload, CompensationAction: step.CompensationAction})
	}
	return input
}

func (s *operationService) authorizePersistedOperation(ctx context.Context, actor string, operation *domain.Operation) error {
	return s.authorizeOperationCreate(ctx, actor, operationInputFromPersisted(operation))
}
