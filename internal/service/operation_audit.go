package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func operationRequestedAudit(ctx context.Context, operation *domain.Operation) *domain.AuditFact {
	return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: operation.HomeRegion,
		EventID: operation.ID, OperationID: operation.ID, RequestID: operation.RequestID, ActorUID: operation.ActorUID,
		AccountID: operation.AccountID, TenantID: operation.TenantID, Action: "operation.requested",
		ResourceType: "operation", ResourceID: operation.ID.String(), Payload: map[string]any{"type": operation.OperationType}})
}

func (s *operationService) createWithAudit(ctx context.Context, operation *domain.Operation) (bool, error) {
	if s.audit == nil {
		return false, s.repo.Create(ctx, operation)
	}
	repo, ok := s.repo.(repository.OperationAuditRepository)
	if !ok {
		return false, s.repo.Create(ctx, operation)
	}
	return true, repo.CreateWithAudit(ctx, operation, operationRequestedAudit(ctx, operation))
}

func (s *operationService) finishStep(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	worker string, status domain.OperationStepStatus, sourceOperationID, code string, result map[string]any, message string) error {
	payload := map[string]any{"step_id": step.ID.String(), "step_key": step.StepKey, "source": step.Source,
		"status": status, "error_code": code}
	if s.audit != nil {
		if repo, ok := s.repo.(repository.OperationStateAuditRepository); ok {
			return repo.FinishStepWithAudit(ctx, step.ID, worker, status, sourceOperationID, code, result, message,
				operationAuditFact(ctx, operation, "operation.step.finished", payload))
		}
	}
	if err := s.repo.FinishStep(ctx, step.ID, worker, status, sourceOperationID, code, result, message); err != nil {
		return err
	}
	return s.recordOperationAudit(ctx, operation, "operation.step.finished", payload)
}

func operationAuditFact(ctx context.Context, operation *domain.Operation, action string, payload map[string]any) *domain.AuditFact {
	if operation == nil {
		return nil
	}
	return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: operation.HomeRegion, EventID: idgen.NewID(),
		OperationID: operation.ID, RequestID: operation.RequestID, ActorUID: operation.ActorUID, AccountID: operation.AccountID,
		TenantID: operation.TenantID, Action: action, ResourceType: "operation", ResourceID: operation.ID.String(), Payload: payload})
}

func (s *operationService) setOperationStatus(ctx context.Context, operation *domain.Operation, status domain.OperationStatus,
	code, message string, result map[string]any, action string, payload map[string]any) error {
	if operation == nil {
		return s.repo.SetStatus(ctx, domain.ID{}, status, code, message, result)
	}
	if s.audit != nil {
		if repo, ok := s.repo.(repository.OperationStateAuditRepository); ok {
			return repo.SetStatusWithAudit(ctx, operation.ID, status, code, message, result,
				operationAuditFact(ctx, operation, action, payload))
		}
	}
	if err := s.repo.SetStatus(ctx, operation.ID, status, code, message, result); err != nil {
		return err
	}
	if action == "" {
		return nil
	}
	return s.recordOperationAudit(ctx, operation, action, payload)
}

func (s *operationService) finishCompensationStep(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	worker string, status domain.OperationStepStatus, sourceOperationID, code string, result map[string]any, message, action string) error {
	payload := map[string]any{"step_id": step.ID.String(), "step_key": step.StepKey, "status": status, "error_code": code}
	if s.audit != nil {
		if repo, ok := s.repo.(repository.OperationStateAuditRepository); ok {
			return repo.FinishCompensationStepWithAudit(ctx, step.ID, worker, status, sourceOperationID, code, result, message,
				operationAuditFact(ctx, operation, action, payload))
		}
	}
	if err := s.repo.FinishCompensationStep(ctx, step.ID, worker, status, sourceOperationID, code, result, message); err != nil {
		return err
	}
	return s.recordOperationAudit(ctx, operation, action, payload)
}

func (s *operationService) recordOperationAudit(ctx context.Context, operation *domain.Operation, action string, payload map[string]any) error {
	if s.audit == nil || operation == nil {
		return nil
	}
	fact := operationAuditFact(ctx, operation, action, payload)
	return s.audit.Record(ctx, AuditFactInput{Source: fact.Source, SourceRegion: fact.SourceRegion, EventID: fact.EventID,
		OperationID: fact.OperationID, RequestID: fact.RequestID, ActorUID: fact.ActorUID, AccountID: fact.AccountID,
		TenantID: fact.TenantID, Action: fact.Action, ResourceType: fact.ResourceType, ResourceID: fact.ResourceID, Payload: fact.Payload})
}
