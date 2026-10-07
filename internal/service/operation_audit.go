package service

import (
	"context"
	"strconv"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func operationRequestedAudit(ctx context.Context, operation *domain.Operation) *domain.AuditFact {
	steps := make([]map[string]any, 0, len(operation.Steps))
	for _, step := range operation.Steps {
		if step == nil {
			continue
		}
		entry := map[string]any{"step_key": step.StepKey, "source": step.Source, "action": step.Action}
		if datasets, err := operationDatasetsForStep(step, connector.Source(step.Source)); err == nil && len(datasets) > 0 {
			entry["datasets"] = datasetNames(datasets)
		}
		steps = append(steps, entry)
	}
	return buildAuditFact(ctx, AuditFactInput{Source: "aero-id", SourceRegion: operation.HomeRegion,
		EventID: operation.ID, OperationID: operation.ID, RequestID: operation.RequestID, ActorUID: operation.ActorUID,
		AccountID: operation.AccountID, TenantID: operation.TenantID, Action: "operation.requested",
		ResourceType: "operation", ResourceID: operation.ID.String(), Payload: map[string]any{"type": operation.OperationType, "steps": steps}})
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
	// "attempt" is the durable claim counter behind the anchor's sixth slot
	// (operation_steps.attempt), so the redacted row stays self-identifying
	// even while a mixed-version fleet derives the same logical fact twice.
	payload := map[string]any{"step_id": step.ID.String(), "step_key": step.StepKey, "source": step.Source,
		"action": step.Action, "status": status, "error_code": code, "attempt": step.Attempt,
		"source_operation_id": sourceOperationID}
	if datasets, err := operationDatasetsForStep(step, connector.Source(step.Source)); err == nil && len(datasets) > 0 {
		payload["datasets"] = datasetNames(datasets)
	}
	if s.audit != nil {
		repo, ok := s.repo.(repository.OperationStateAuditRepository)
		if !ok {
			return apperr.Internal("operation.audit_unavailable", "operation audit persistence is unavailable")
		}
		return repo.FinishStepWithAudit(ctx, step.ID, worker, status, sourceOperationID, code, result, message,
			operationAuditFact(ctx, operation, operationStepAnchor{id: step.ID, attempt: step.Attempt},
				string(status), "operation.step.finished", payload))
	}
	return s.repo.FinishStep(ctx, step.ID, worker, status, sourceOperationID, code, result, message)
}

// operationStepAnchor binds a step identity to that step's durable attempt
// counter as read from the claim which produced the fact
// (domain.OperationStep.Attempt / .CompensationAttempt, incremented in-tx by
// claimForwardStep, claimReconcileStep and claimCompensationStep together with
// a new operation_attempts row). The zero value is the step-less case.
//
// Binding id and attempt in one value means a step-scoped fact cannot be
// minted without an attempt: an attempt-less step-scoped anchor would fold
// every reconcile pass of a still-unknown step back onto a single ledger row.
type operationStepAnchor struct {
	id      domain.ID
	attempt int
}

// operationAuditFact builds a step-scoped or operation-scoped audit fact with a
// deterministic event_id anchored on the fact's own identity fields (contract
// §2.3). The shape splits on whether the fact is scoped to a step:
//
//   - step-less (operation.started | operation.compensating |
//     operation.reconciled | operation.finished): the 5-tuple
//     [action, tenant, operation, "", status]. The status slot is mandatory
//     here too — operation.finished and operation.reconciled legitimately
//     carry different statuses across passes, and omitting it would swallow
//     the second outcome. The sixth attempt slot is OMITTED rather than
//     emitted empty, because arity is part of the encoding
//     (Anchor("a","b") != Anchor("a","b","")): appending "" would silently re-derive
//     every operation-level fact and break dedup against the facts already in
//     the ledger.
//   - step-scoped (operation.step.finished | operation.compensation.finished |
//     operation.compensation.reconciled): the same 5 slots plus a sixth,
//     strconv.Itoa(attempt) taken from the durable claim.
//
// The sixth slot is what stops repeated unknown-outcome reconciliations from
// folding into one ledger row: every re-query claims a new attempt (new
// operation_attempts row, attempt+1), so each pass records its own fact, while
// a retry of the same attempt re-derives the same event_id and is deduped by
// the ledger guard (tenant, source, event_id). Slots 1–2 remain
// [tenant, operation], so no appended slot can merge two tenants, operations or
// steps, and a changed status still mints its own event_id (G2 vs G2b).
func operationAuditFact(ctx context.Context, operation *domain.Operation,
	anchor operationStepAnchor, status string, action string, payload map[string]any) *domain.AuditFact {
	if operation == nil {
		return nil
	}
	parts := []string{action, operation.TenantID, operation.ID.String(), "", status}
	if anchor.id != (domain.ID{}) {
		parts[3] = anchor.id.String()
		parts = append(parts, strconv.Itoa(anchor.attempt))
	}
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
			operationAuditFact(ctx, operation, operationStepAnchor{}, string(status), action, payload))
	}
	return s.repo.SetStatus(ctx, operation.ID, status, code, message, result)
}

func (s *operationService) finishCompensationStep(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	worker string, status domain.OperationStepStatus, sourceOperationID, code string, result map[string]any, message, action string) error {
	// The durable compensation counter mirrors the operation_steps column name
	// so a forward attempt can never be confused with a compensation one.
	payload := map[string]any{"step_id": step.ID.String(), "step_key": step.StepKey, "status": status,
		"error_code": code, "compensation_attempt": step.CompensationAttempt}
	if s.audit != nil {
		repo, ok := s.repo.(repository.OperationStateAuditRepository)
		if !ok {
			return apperr.Internal("operation.audit_unavailable", "operation audit persistence is unavailable")
		}
		return repo.FinishCompensationStepWithAudit(ctx, step.ID, worker, status, sourceOperationID, code, result, message,
			operationAuditFact(ctx, operation, operationStepAnchor{id: step.ID, attempt: step.CompensationAttempt},
				string(status), action, payload))
	}
	return s.repo.FinishCompensationStep(ctx, step.ID, worker, status, sourceOperationID, code, result, message)
}
