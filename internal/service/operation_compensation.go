package service

import (
	"context"
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

func validReconciledStepStatus(status domain.OperationStepStatus) bool {
	return status == domain.StepSucceeded || status == domain.StepFailed || status == domain.StepUnknown
}

//nolint:cyclop,funlen // compensation is a durable Saga state machine with unknown-outcome handling.
func (s *operationService) compensate(ctx context.Context, operation *domain.Operation, worker string) error {
	loadCtx, loadCancel := operationPersistenceContext(ctx)
	current, err := s.repo.Get(loadCtx, operation.ID)
	loadCancel()
	if err != nil {
		return err
	}
	operation = current
	if !hasCompensation(operation.Steps) {
		return nil
	}
	if err := s.startCompensation(ctx, operation, worker); err != nil {
		return err
	}
	runner, configured := s.runner.(OperationCompensationRunner)
	for {
		if err := s.checkOperationExecution(ctx, operation, nil, true, false, ""); err != nil {
			return err
		}
		owner := operationLeaseOwner(worker)
		claimCtx, claimCancel := operationPersistenceContext(ctx)
		step, err := s.repo.ClaimCompensationStep(claimCtx, operation.ID, owner, operationLeaseDuration)
		claimCancel()
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, repository.ErrOperationStepUnknown) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.checkOperationExecution(ctx, operation, step, true, false, owner); err != nil {
			return err
		}
		status := domain.StepSucceeded
		code, message := "", ""
		var sourceOperationID string
		var result map[string]any
		if !configured {
			status, code, message = domain.StepFailed, "operation.compensation_runner_unavailable", "operation compensation runner is not configured"
		} else {
			remoteCtx, remoteCancel := operationRemoteContext(ctx)
			sourceOperationID, result, err = runner.RunCompensation(remoteCtx, operation, step)
			remoteCancel()
			var correlated bool
			sourceOperationID, correlated = retainSourceOperationID(step.CompensationSourceOperationID, sourceOperationID)
			if !correlated {
				err = sourceOperationIDConflict(connector.Source(step.Source))
			}
			if err != nil {
				status, code, message = domain.StepFailed, "operation.compensation_step_failed", "operation compensation step failed"
				if isUnknownOperationError(err) {
					status, code, message = domain.StepUnknown, "operation.compensation_unknown_outcome", "compensation outcome is unknown"
				}
			}
		}
		finishCtx, finishCancel := operationPersistenceContext(ctx)
		err = s.finishCompensationStep(finishCtx, operation, step, owner, status, sourceOperationID, code, result, message,
			"operation.compensation.finished")
		finishCancel()
		if err != nil {
			return err
		}
	}
}

//nolint:cyclop // compensation reconciliation preserves explicit succeeded, failed, and unknown outcomes.
func (s *operationService) reconcileCompensations(ctx context.Context, operation *domain.Operation, worker string,
	reconciler OperationCompensationOutcomeReconciler) error {
	for {
		if err := s.checkOperationExecution(ctx, operation, nil, true, true, ""); err != nil {
			return err
		}
		owner := operationLeaseOwner(worker)
		claimCtx, claimCancel := operationPersistenceContext(ctx)
		step, err := s.repo.ClaimUnknownCompensationStep(claimCtx, operation.ID, owner, operationLeaseDuration)
		claimCancel()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.checkOperationExecution(ctx, operation, step, true, true, owner); err != nil {
			return err
		}
		remoteCtx, remoteCancel := operationRemoteContext(ctx)
		status, sourceOperationID, result, reconcileErr := reconciler.ReconcileCompensation(remoteCtx, operation, step)
		remoteCancel()
		var correlated bool
		sourceOperationID, correlated = retainSourceOperationID(step.CompensationSourceOperationID, sourceOperationID)
		if !correlated {
			reconcileErr = sourceOperationIDConflict(connector.Source(step.Source))
		}
		if reconcileErr != nil || !validReconciledStepStatus(status) {
			status = domain.StepUnknown
			if sourceOperationID == "" {
				sourceOperationID = step.CompensationSourceOperationID
			}
			if result == nil {
				result = map[string]any{"error": "compensation outcome remains unknown"}
			}
		}
		code, message := "", ""
		if status == domain.StepFailed {
			code, message = "operation.compensation_source_failed", "source reported compensation failure"
		} else if status == domain.StepUnknown {
			code, message = "operation.compensation_unknown_outcome", "compensation outcome remains unknown"
		}
		finishCtx, finishCancel := operationPersistenceContext(ctx)
		err = s.finishCompensationStep(finishCtx, operation, step, owner, status, sourceOperationID, code, result, message,
			"operation.compensation.reconciled")
		finishCancel()
		if err != nil {
			return err
		}
	}
}

func hasCompensation(steps []*domain.OperationStep) bool {
	for _, step := range steps {
		if step != nil && step.Status == domain.StepSucceeded && strings.TrimSpace(step.CompensationAction) != "" {
			return true
		}
	}
	return false
}

// An uncertain forward outcome is not permission to start rollback. Resume
// compensation only after known failure or an already-entered compensation phase.
func shouldResumeCompensation(operation *domain.Operation) bool {
	knownFailure := false
	for _, step := range operation.Steps {
		if step.Status == domain.StepRunning || step.Status == domain.StepUnknown {
			return false
		}
		if step.Status == domain.StepFailed {
			knownFailure = true
		}
	}
	return (knownFailure || operation.Status == domain.OperationCompensating) && hasPendingCompensation(operation.Steps)
}

func hasPendingCompensation(steps []*domain.OperationStep) bool {
	for _, step := range steps {
		if step == nil || step.Status != domain.StepSucceeded || strings.TrimSpace(step.CompensationAction) == "" {
			continue
		}
		if step.CompensationStatus == domain.StepPending || step.CompensationStatus == domain.StepRunning {
			return true
		}
	}
	return false
}

func isUnknownOperationError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	remote, ok := connector.IsRemote(err)
	return ok && (remote.Kind == connector.ErrorTimeout || remote.Kind == connector.ErrorUnavailable || remote.Kind == connector.ErrorUnknown)
}
