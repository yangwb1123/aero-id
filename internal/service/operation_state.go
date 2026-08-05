package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"gorm.io/gorm"
)

func (s *operationService) reconcileClaimed(ctx context.Context, operation *domain.Operation, worker string,
	reconciler OperationOutcomeReconciler) (*domain.Operation, error) {
	if operationTerminalForReconcile(operation.Status) {
		return operation, nil
	}
	if hasPendingCompensation(operation.Steps) {
		if err := s.compensate(ctx, operation, worker); err != nil {
			return nil, apperr.Internal("operation.compensation_failed", "failed to resume operation compensation").Wrap(err)
		}
	}
	if err := s.reconcileForwardSteps(ctx, operation, worker, reconciler); err != nil {
		return nil, err
	}
	if compensationReconciler, ok := s.runner.(OperationCompensationOutcomeReconciler); ok {
		if err := s.reconcileCompensations(ctx, operation, worker, compensationReconciler); err != nil {
			return nil, apperr.Internal("operation.compensation_reconcile_failed", "failed to reconcile compensation outcome").Wrap(err)
		}
	}
	if err := s.recordOperationAudit(ctx, operation, "operation.reconciled", map[string]any{"worker": worker}); err != nil {
		return nil, apperr.Internal("operation.audit_failed", "failed to record operation audit").Wrap(err)
	}
	return s.finishOperation(ctx, operation.ID)
}

func operationTerminalForReconcile(status domain.OperationStatus) bool {
	return status == domain.OperationCompleted || status == domain.OperationFailed ||
		status == domain.OperationPartiallyCompleted
}

func (s *operationService) reconcileForwardSteps(ctx context.Context, operation *domain.Operation, worker string,
	reconciler OperationOutcomeReconciler) error {
	for {
		step, err := s.repo.ClaimUnknownStep(ctx, operation.ID, worker, 2*time.Minute)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return apperr.Internal("operation.reconcile_claim_failed", "failed to claim unknown operation step").Wrap(err)
		}
		status, sourceOperationID, result := reconciledStepResult(ctx, reconciler, operation, step)
		if err := s.finishStep(ctx, operation, step, worker, status, sourceOperationID,
			outcomeErrorCode(status), result, outcomeErrorMessage(status)); err != nil {
			return apperr.Internal("operation.reconcile_step_failed", "failed to reconcile operation step").Wrap(err)
		}
		if status == domain.StepFailed {
			return s.abortReconciledOperation(ctx, operation, worker)
		}
	}
}

func reconciledStepResult(ctx context.Context, reconciler OperationOutcomeReconciler,
	operation *domain.Operation, step *domain.OperationStep) (domain.OperationStepStatus, string, map[string]any) {
	status, sourceOperationID, result, err := reconciler.Reconcile(ctx, operation, step)
	if err != nil {
		return domain.StepUnknown, step.SourceOperationID, map[string]any{"error": "source outcome remains unknown"}
	}
	if !validReconciledStepStatus(status) {
		return domain.StepUnknown, sourceOperationID, result
	}
	return status, sourceOperationID, result
}

func (s *operationService) abortReconciledOperation(ctx context.Context, operation *domain.Operation, worker string) error {
	if err := s.repo.SkipPendingSteps(ctx, operation.ID, "operation.aborted", "operation stopped after a failed step"); err != nil {
		return apperr.Internal("operation.skip_steps_failed", "failed to close skipped operation steps").Wrap(err)
	}
	if err := s.compensate(ctx, operation, worker); err != nil {
		return apperr.Internal("operation.compensation_failed", "failed to run operation compensation").Wrap(err)
	}
	return nil
}

type operationProgress struct {
	failed, unknown, open, pending, running bool
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
	if p.compensationFailed || p.uncompensatedSuccess {
		return domain.OperationPartiallyCompleted
	}
	return domain.OperationFailed
}

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
	return s.repo.Get(ctx, id)
}
