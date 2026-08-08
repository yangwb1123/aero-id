package service

import (
	"context"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func operationRequestedAudit(ctx context.Context, operation *domain.Operation) *domain.AuditFact {
	return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: operation.HomeRegion,
		EventID: operation.ID, OperationID: operation.ID, RequestID: operation.RequestID, ActorUID: operation.ActorUID,
		AccountID: operation.AccountID, TenantID: operation.TenantID, Action: "operation.requested",
		ResourceType: "operation", ResourceID: operation.ID.String(), Payload: map[string]any{"type": operation.OperationType}})
}

func (s *operationService) createWithAudit(ctx context.Context, operation *domain.Operation) error {
	if s.audit == nil {
		return s.repo.Create(ctx, operation)
	}
	repo, ok := s.repo.(repository.OperationAuditRepository)
	if !ok {
		// Fail closed, matching finishStep/setOperationStatus/finishCompensationStep
		// below: a repo without the WithAudit surface must not silently fall back
		// to a plain Create that drops the operation.requested fact.
		return apperr.Internal("operation.audit_unavailable", "operation audit persistence is unavailable")
	}
	return repo.CreateWithAudit(ctx, operation, operationRequestedAudit(ctx, operation))
}

func (s *operationService) finishStep(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	worker string, status domain.OperationStepStatus, sourceOperationID, code string, result map[string]any, message string) error {
	payload := map[string]any{"step_id": step.ID.String(), "step_key": step.StepKey, "source": step.Source,
		"status": status, "error_code": code}
	if s.audit != nil {
		repo, ok := s.repo.(repository.OperationStateAuditRepository)
		if !ok {
			return apperr.Internal("operation.audit_unavailable", "operation audit persistence is unavailable")
		}
		return repo.FinishStepWithAudit(ctx, step.ID, worker, status, sourceOperationID, code, result, message,
			operationAuditFact(ctx, operation, step.ID, string(status), "operation.step.finished", payload))
	}
	return s.repo.FinishStep(ctx, step.ID, worker, status, sourceOperationID, code, result, message)
}

// operationAuditFact builds a step-scoped or operation-scoped audit fact with a
// deterministic event_id anchored on the fact's own identity fields: the
// uniform 5-tuple [action, tenant, operation, step-or-"", status] (contract
// §2.3). The status slot is mandatory for step-less facts too: operation.finished
// and operation.reconciled can legitimately carry different statuses across
// passes, and omitting the slot would swallow the second outcome. Retrying the
// same logical fact re-derives the same event_id so the ledger dedup guard
// (tenant, source, event_id) swallows the duplicate; a re-reconcile that
// changes a terminal status mints a different event_id and both outcomes are
// preserved (G2 vs G2b).
func operationAuditFact(ctx context.Context, operation *domain.Operation,
	stepID domain.ID, status string, action string, payload map[string]any) *domain.AuditFact {
	if operation == nil {
		return nil
	}
	step := ""
	if stepID != (domain.ID{}) {
		step = stepID.String()
	}
	parts := []string{action, operation.TenantID, operation.ID.String(), step, status}
	return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: operation.HomeRegion, EventID: idgen.Anchor(parts...),
		OperationID: operation.ID, RequestID: operation.RequestID, ActorUID: operation.ActorUID, AccountID: operation.AccountID,
		TenantID: operation.TenantID, Action: action, ResourceType: "operation", ResourceID: operation.ID.String(), Payload: payload})
}

func (s *operationService) setOperationStatus(ctx context.Context, operation *domain.Operation, status domain.OperationStatus,
	code, message string, result map[string]any, action string, payload map[string]any) error {
	if operation == nil {
		return s.repo.SetStatus(ctx, domain.ID{}, status, code, message, result)
	}
	if s.audit != nil {
		repo, ok := s.repo.(repository.OperationStateAuditRepository)
		if !ok {
			return apperr.Internal("operation.audit_unavailable", "operation audit persistence is unavailable")
		}
		return repo.SetStatusWithAudit(ctx, operation.ID, status, code, message, result,
			operationAuditFact(ctx, operation, domain.ID{}, string(status), action, payload))
	}
	return s.repo.SetStatus(ctx, operation.ID, status, code, message, result)
}

func (s *operationService) finishCompensationStep(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	worker string, status domain.OperationStepStatus, sourceOperationID, code string, result map[string]any, message, action string) error {
	payload := map[string]any{"step_id": step.ID.String(), "step_key": step.StepKey, "status": status, "error_code": code}
	if s.audit != nil {
		repo, ok := s.repo.(repository.OperationStateAuditRepository)
		if !ok {
			return apperr.Internal("operation.audit_unavailable", "operation audit persistence is unavailable")
		}
		return repo.FinishCompensationStepWithAudit(ctx, step.ID, worker, status, sourceOperationID, code, result, message,
			operationAuditFact(ctx, operation, step.ID, string(status), action, payload))
	}
	return s.repo.FinishCompensationStep(ctx, step.ID, worker, status, sourceOperationID, code, result, message)
}
