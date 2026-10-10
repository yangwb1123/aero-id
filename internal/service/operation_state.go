package service

import (
	"context"
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"gorm.io/gorm"
)

func (s *operationService) reconcileClaimed(ctx context.Context, operation *domain.Operation, worker string,
	reconciler OperationOutcomeReconciler) (*domain.Operation, error) {
	worker = operationWorkerRole(worker)
	if operationTerminalForReconcile(operation.Status) {
		return operation, nil
	}
	if err := s.checkOperationExecution(ctx, operation, nil, false, true, ""); err != nil {
		return nil, err
	}
	var err error
	operation, err = s.resumeCompensationForReconcile(ctx, operation, worker)
	if err != nil {
		return nil, err
	}
	if err := s.reconcileForwardSteps(ctx, operation, worker, reconciler); err != nil {
		return nil, err
	}
	if compensationReconciler, ok := s.runner.(OperationCompensationOutcomeReconciler); ok {
		if err := s.reconcileCompensations(ctx, operation, worker, compensationReconciler); err != nil {
			return nil, operationContinuationError(err, "operation.compensation_reconcile_failed", "failed to reconcile compensation outcome")
		}
	}
	// operation.reconciled is a standalone fact with no domain write of its own
	// (the subsequent finishOperation records operation.finished in-tx). Keep the
	// non-swallowed standalone Record — A1-class, same shape as operation.requested.
	// Status is captured at this pinned point (operation.Status as loaded for this
	// pass) so a status-changing later pass records its own fact (contract §2.3).
	if s.audit != nil {
		// audit-site: standalone-fact — A1-class standalone fact with no domain
		// write of its own (documented above); anchored event_id dedups retries.
		fact := operationAuditFact(ctx, operation, operationStepAnchor{}, string(operation.Status), "operation.reconciled", map[string]any{"worker": worker})
		persistCtx, persistCancel := operationPersistenceContext(ctx)
		defer persistCancel()
		if err := s.audit.Record(persistCtx, AuditFactInput{Source: fact.Source, SourceRegion: fact.SourceRegion, EventID: fact.EventID,
			OperationID: fact.OperationID, RequestID: fact.RequestID, ActorUID: fact.ActorUID, AccountID: fact.AccountID,
			TenantID: fact.TenantID, Action: fact.Action, ResourceType: fact.ResourceType, ResourceID: fact.ResourceID, Payload: fact.Payload}); err != nil {
			return nil, apperr.Internal("operation.audit_failed", "failed to record operation audit").Wrap(err)
		}
	}
	finishCtx, finishCancel := operationPersistenceContext(ctx)
	defer finishCancel()
	return s.finishOperation(finishCtx, operation.ID)
}

func operationTerminalForReconcile(status domain.OperationStatus) bool {
	return status == domain.OperationCompleted || status == domain.OperationFailed ||
		status == domain.OperationPartiallyCompleted
}

func (s *operationService) reconcileForwardSteps(ctx context.Context, operation *domain.Operation, worker string,
	reconciler OperationOutcomeReconciler) error {
	for {
		step, owner, err := s.claimUnknownStepForExecution(ctx, operation, worker)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return operationContinuationError(err, "operation.reconcile_claim_failed", "failed to claim unknown operation step")
		}
		status, sourceOperationID, result, reconcileErr := reconciledStepResult(ctx, reconciler, operation, step)
		if sourceOperationID == "" {
			sourceOperationID = step.SourceOperationID
		}
		code, message := outcomeErrorCode(status), outcomeErrorMessage(status)
		if reconcileErr != nil {
			status, code, message = operationRunStatus(reconcileErr)
			if status == domain.StepUnknown && result == nil {
				result = map[string]any{"error": "source outcome remains unknown"}
			}
		}
		persistCtx, persistCancel := operationPersistenceContext(ctx)
		err = s.finishStep(persistCtx, operation, step, owner, status, sourceOperationID,
			code, result, message)
		persistCancel()
		if err != nil {
			return apperr.Internal("operation.reconcile_step_failed", "failed to reconcile operation step").Wrap(err)
		}
		if status == domain.StepFailed {
			return s.abortReconciledOperation(ctx, operation, worker)
		}
	}
}

func reconciledStepResult(ctx context.Context, reconciler OperationOutcomeReconciler,
	operation *domain.Operation, step *domain.OperationStep) (domain.OperationStepStatus, string, map[string]any, error) {
	// Validate durable scope before invoking the reconciler. This is deliberately
	// independent of connector lookup so a malformed row is deterministic even
	// when the source adapter is unavailable during a rollout.
	if _, err := operationDatasetsForStep(step, connector.Source(step.Source)); err != nil {
		return domain.StepFailed, step.SourceOperationID, nil, err
	}
	remoteCtx, cancel := operationRemoteContext(ctx)
	status, sourceOperationID, result, err := reconciler.Reconcile(remoteCtx, operation, step)
	cancel()
	var correlated bool
	sourceOperationID, correlated = retainSourceOperationID(step.SourceOperationID, sourceOperationID)
	if !correlated {
		return domain.StepUnknown, sourceOperationID, result, sourceOperationIDConflict(connector.Source(step.Source))
	}
	if err != nil {
		if sourceOperationID == "" {
			sourceOperationID = step.SourceOperationID
		}
		return domain.StepUnknown, sourceOperationID, result, err
	}
	if !validReconciledStepStatus(status) {
		return domain.StepUnknown, sourceOperationID, result, nil
	}
	return status, sourceOperationID, result, nil
}

func (s *operationService) abortReconciledOperation(ctx context.Context, operation *domain.Operation, worker string) error {
	stateCtx, cancel := operationPersistenceContext(ctx)
	defer cancel()
	if err := s.repo.SkipPendingSteps(stateCtx, operation.ID, "operation.aborted", "operation stopped after a failed step"); err != nil {
		return apperr.Internal("operation.skip_steps_failed", "failed to close skipped operation steps").Wrap(err)
	}
	if err := s.compensate(ctx, operation, worker); err != nil {
		return operationContinuationError(err, "operation.compensation_failed", "failed to run operation compensation")
	}
	return nil
}

type operationProgress struct {
	failed, unknown, open, pending, running bool
	partial                                 bool
	compensationFailed, compensationUnknown bool
	uncompensatedSuccess                    bool
}

func inspectOperationProgress(steps []*domain.OperationStep) operationProgress {
	var progress operationProgress
	for _, step := range steps {
		if step != nil {
			progress.observe(step)
		}
	}
	return progress
}

func (p *operationProgress) observe(step *domain.OperationStep) {
	p.observeStatus(step.Status, step.CompensationAction, step.CompensationStatus)
	p.observeCompensation(step)
	if stepResultIsPartial(step.Result) {
		p.partial = true
	}
}

func (p *operationProgress) observeStatus(status domain.OperationStepStatus, compensationAction string,
	compensationStatus domain.OperationStepStatus) {
	switch status {
	case domain.StepPending:
		p.open, p.pending = true, true
	case domain.StepRunning:
		p.open, p.running = true, true
	case domain.StepFailed:
		p.failed = true
	case domain.StepUnknown:
		p.unknown = true
	case domain.StepSucceeded:
		if strings.TrimSpace(compensationAction) == "" || compensationStatus != domain.StepSucceeded {
			p.uncompensatedSuccess = true
		}
	}
}

func (p *operationProgress) observeCompensation(step *domain.OperationStep) {
	if strings.TrimSpace(step.CompensationAction) == "" {
		return
	}
	switch step.CompensationStatus {
	case domain.StepFailed:
		p.compensationFailed = true
	case domain.StepPending, domain.StepUnknown, domain.StepRunning:
		p.compensationUnknown = true
	}
}

func (p operationProgress) status() domain.OperationStatus {
	if !p.failed {
		if p.open && !p.unknown {
			return domain.OperationWaiting
		}
		if p.unknown {
			return domain.OperationUnknown
		}
		if p.partial {
			return domain.OperationPartiallyCompleted
		}
		return domain.OperationCompleted
	}
	if p.unknown || p.compensationUnknown {
		return domain.OperationUnknown
	}
	return p.failedStatus()
}

func (p operationProgress) failedStatus() domain.OperationStatus {
	if !p.failed {
		return domain.OperationCompleted
	}
	if p.partial || p.compensationFailed || p.uncompensatedSuccess {
		return domain.OperationPartiallyCompleted
	}
	return domain.OperationFailed
}

func stepResultIsPartial(result map[string]any) bool {
	status, ok := result["status"].(string)
	if !ok {
		return false
	}
	return status == "partial" || status == string(domain.OperationPartiallyCompleted)
}

//nolint:cyclop // terminal transition contains the durable state and audit error matrix.
func (s *operationService) finishOperation(ctx context.Context, id domain.ID) (*domain.Operation, error) {
	operation, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, apperr.Internal("operation.reload_failed", "failed to reload operation").Wrap(err)
	}
	progress := inspectOperationProgress(operation.Steps)
	if progress.failed && progress.pending && !progress.running {
		if err := s.repo.SkipPendingSteps(ctx, id, "operation.aborted", "operation stopped after a failed step"); err != nil {
			return nil, apperr.Internal("operation.skip_steps_failed", "failed to close skipped operation steps").Wrap(err)
		}
		progress.open = false
	}
	status := progress.status()
	if err := s.setOperationStatus(ctx, operation, status, "", "", nil, "operation.finished",
		map[string]any{"status": status}); err != nil {
		return nil, apperr.Internal("operation.finish_failed", "failed to finish operation").Wrap(err)
	}
	// Metrics: one terminal-outcome observation per genuine transition into a
	// terminal status. terminalOperationStatus keeps a two-worker race that
	// re-finishes an open operation as "waiting" out of the terminal-outcome
	// counter; the status != operation.Status guard stops a reconcile pass on
	// an already-unknown operation from re-counting the same outcome.
	// Emission follows a durable setOperationStatus commit, so error paths
	// emit nothing.
	if terminalOperationStatus(status) && status != operation.Status {
		s.metrics.ObserveOperationOutcome(string(status))
	}
	finished, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, apperr.Internal("operation.reload_failed", "failed to reload finished operation").Wrap(err)
	}
	return finished, nil
}
