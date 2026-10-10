package service

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

func (s *operationService) checkOperationExecution(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	compensation, reconcile bool, owner string) error {
	admissionCtx, cancel := context.WithTimeout(ctx, operationAuthorizationBudget)
	defer cancel()
	cause := s.authorizeOperationExecution(admissionCtx, operation, step, compensation)
	if cause == nil {
		if step == nil {
			return nil
		}
		if err := s.checkExecutionLease(admissionCtx, operation, step, owner, compensation, reconcile); err != nil {
			return s.blockOperationExecution(ctx, operation, err)
		}
		return nil
	}
	if operation == nil {
		return cause
	}
	if step != nil {
		if err := s.deferUnauthorizedStep(ctx, operation, step, compensation, reconcile, owner, cause); err != nil {
			return err
		}
	}
	return s.blockOperationExecution(ctx, operation, cause)
}

func (s *operationService) checkExecutionLease(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	owner string, compensation, reconcile bool) error {
	repo, ok := s.repo.(repository.OperationExecutionLeaseRepository)
	if !ok {
		return apperr.Unavailable("operation.execution_lease_unavailable", "operation execution lease cannot be verified")
	}
	if err := repo.CheckExecutionLease(ctx, operation.ID, step.ID, owner, compensation, reconcile); err != nil || ctx.Err() != nil {
		return apperr.Unavailable("operation.execution_lease_lost", "operation execution lease cannot be verified; query the original receipt")
	}
	return nil
}

// This path is used ONLY before invoking the runner. A fresh forward claim
// returns to pending; an unknown/status-query claim stays unknown. Never call
// it after sending an effect, timing out, or losing a response/lease.
func (s *operationService) deferUnauthorizedStep(ctx context.Context, operation *domain.Operation, step *domain.OperationStep,
	compensation, reconcile bool, owner string, cause error) error {
	var app *apperr.AppError
	if !errors.As(cause, &app) {
		return cause
	}
	status := domain.StepPending
	if reconcile {
		status = domain.StepUnknown
	}
	persistCtx, cancel := operationPersistenceContext(ctx)
	defer cancel()
	var err error
	if compensation {
		err = s.finishCompensationStep(persistCtx, operation, step, owner, status, step.CompensationSourceOperationID, app.Code,
			step.CompensationResult, app.Message, "operation.compensation.deferred")
	} else {
		err = s.finishStep(persistCtx, operation, step, owner, status, step.SourceOperationID, app.Code, step.Result, app.Message)
	}
	if err != nil {
		return apperr.Unavailable("operation.execution_defer_unknown", "operation deferral outcome is unavailable; query the original receipt").Wrap(err)
	}
	return nil
}

func (s *operationService) claimUnknownStepForExecution(ctx context.Context,
	operation *domain.Operation, worker string) (*domain.OperationStep, string, error) {
	if err := s.checkOperationExecution(ctx, operation, nil, false, true, ""); err != nil {
		return nil, "", err
	}
	owner := operationLeaseOwner(worker)
	persistCtx, cancel := operationPersistenceContext(ctx)
	step, err := s.repo.ClaimUnknownStep(persistCtx, operation.ID, owner, operationLeaseDuration)
	cancel()
	if err != nil {
		return nil, owner, err
	}
	if err := s.checkOperationExecution(ctx, operation, step, false, true, owner); err != nil {
		return nil, owner, err
	}
	return step, owner, nil
}

func isOperationExecutionBlocked(err error) bool {
	var app *apperr.AppError
	if !errors.As(err, &app) {
		return false
	}
	switch app.Code {
	case "operation.authorization_denied", "operation.authorization_unavailable", "operation.execution_scope_invalid",
		"operation.invalid_type", invalidOperationDatasetScopeCode, "account.erased", "account.home_region_required":
		return true
	default:
		return false
	}
}

func operationContinuationError(err error, code, message string) error {
	var app *apperr.AppError
	if errors.As(err, &app) {
		return err
	}
	return apperr.Internal(code, message).Wrap(err)
}

func (s *operationService) resumeCompensationForReconcile(ctx context.Context, operation *domain.Operation, worker string) (*domain.Operation, error) {
	if !shouldResumeCompensation(operation) {
		return operation, nil
	}
	if err := s.compensate(ctx, operation, worker); err != nil {
		return nil, operationContinuationError(err, "operation.compensation_failed", "failed to resume operation compensation")
	}
	persistCtx, cancel := operationPersistenceContext(ctx)
	defer cancel()
	current, err := s.repo.Get(persistCtx, operation.ID)
	if err != nil {
		return nil, apperr.Unavailable("operation.reload_failed", "failed to reload operation").Wrap(err)
	}
	if current == nil {
		return nil, apperr.Unavailable("operation.reload_failed", "failed to reload operation")
	}
	return current, nil
}

func (s *operationService) startCompensation(ctx context.Context, operation *domain.Operation, worker string) error {
	if err := s.checkOperationExecution(ctx, operation, nil, true, false, ""); err != nil {
		return err
	}
	if operation.Status == domain.OperationUnknown || operation.Status == domain.OperationCompensating {
		return nil
	}
	persistCtx, cancel := operationPersistenceContext(ctx)
	defer cancel()
	if err := s.setOperationStatus(persistCtx, operation, domain.OperationCompensating, "", "", nil, "operation.compensating",
		map[string]any{"worker_role": worker}); err != nil {
		return err
	}
	operation.Status = domain.OperationCompensating
	return nil
}

func (s *operationService) startClaimedOperation(ctx context.Context, operation *domain.Operation, worker string) error {
	if err := s.checkOperationExecution(ctx, operation, nil, false, false, ""); err != nil {
		return err
	}
	startCtx, cancel := operationPersistenceContext(ctx)
	defer cancel()
	if err := s.setOperationStatus(startCtx, operation, domain.OperationRunning, "", "", nil, "operation.started",
		map[string]any{"status": domain.OperationRunning, "worker_role": worker}); err != nil {
		return apperr.Internal("operation.start_failed", "failed to start operation").Wrap(err)
	}
	operation.Status = domain.OperationRunning
	return nil
}
