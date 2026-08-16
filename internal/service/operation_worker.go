package service

import (
	"context"
	"errors"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

type OperationWorkerService interface {
	Start(context.Context, time.Duration) func()
}

//nolint:cyclop // worker execution maps claim, remote execution, unknown outcome, and terminal transitions.
func (s *operationService) runClaimed(ctx context.Context, operation *domain.Operation, worker string) (*domain.Operation, error) {
	if terminalOperationStatus(operation.Status) {
		return operation, nil
	}
	if worker == "" {
		worker = "inline"
	}
	if err := s.setOperationStatus(ctx, operation, domain.OperationRunning, "", "", nil, "operation.started",
		map[string]any{"status": domain.OperationRunning}); err != nil {
		return nil, apperr.Internal("operation.start_failed", "failed to start operation").Wrap(err)
	}
	for {
		step, claimErr := s.repo.ClaimNextStep(ctx, operation.ID, worker, 2*time.Minute)
		if claimErr != nil {
			if errors.Is(claimErr, gorm.ErrRecordNotFound) || errors.Is(claimErr, repository.ErrOperationStepUnknown) {
				break
			}
			return nil, apperr.Internal("operation.claim_failed", "failed to claim operation step").Wrap(claimErr)
		}
		if err := s.runClaimedStep(ctx, operation, step, worker); err != nil {
			return nil, err
		}
		if step.Status == domain.StepUnknown || step.Status == domain.StepFailed {
			break
		}
	}
	return s.finishOperation(ctx, operation.ID)
}

func terminalOperationStatus(status domain.OperationStatus) bool {
	return status == domain.OperationCompleted || status == domain.OperationFailed ||
		status == domain.OperationPartiallyCompleted || status == domain.OperationUnknown
}

func (s *operationService) runClaimedStep(ctx context.Context, operation *domain.Operation, step *domain.OperationStep, worker string) error {
	if s.runner == nil {
		if err := s.finishStep(ctx, operation, step, worker, domain.StepFailed, "", "operation.runner_unavailable", nil,
			"operation runner is not configured"); err != nil {
			return apperr.Internal("operation.finish_step_failed", "failed to finish operation step").Wrap(err)
		}
		return s.abortOperationSteps(ctx, operation, worker)
	}
	sourceOperationID, result, runErr := s.runner.Run(ctx, operation, step)
	status, code, message := operationRunStatus(runErr)
	if err := s.finishStep(ctx, operation, step, worker, status, sourceOperationID, code, result, message); err != nil {
		return apperr.Internal("operation.finish_step_failed", "failed to finish operation step").Wrap(err)
	}
	step.Status = status
	if status != domain.StepFailed {
		return nil
	}
	return s.abortOperationSteps(ctx, operation, worker)
}

func (s *operationService) abortOperationSteps(ctx context.Context, operation *domain.Operation, worker string) error {
	if err := s.repo.SkipPendingSteps(ctx, operation.ID, "operation.aborted", "operation stopped after a failed step"); err != nil {
		return apperr.Internal("operation.skip_steps_failed", "failed to close skipped operation steps").Wrap(err)
	}
	if err := s.compensate(ctx, operation, worker); err != nil {
		return apperr.Internal("operation.compensation_failed", "failed to run operation compensation").Wrap(err)
	}
	return nil
}

func operationRunStatus(runErr error) (domain.OperationStepStatus, string, string) {
	if runErr == nil {
		return domain.StepSucceeded, "", ""
	}
	status, code, message := domain.StepFailed, "operation.step_failed", "operation step failed"
	remoteUnknown := false
	if remote, ok := connector.IsRemote(runErr); ok {
		remoteUnknown = remote.Kind == connector.ErrorTimeout || remote.Kind == connector.ErrorUnavailable || remote.Kind == connector.ErrorUnknown
	}
	if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) || remoteUnknown {
		status, code, message = domain.StepUnknown, "operation.unknown_outcome", "remote outcome is unknown"
	}
	return status, code, message
}

func (s *operationService) Start(ctx context.Context, interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Second
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		defer close(done)
		for {
			select {
			case <-ticker.C:
				s.processRunnable(workerCtx)
			case <-workerCtx.Done():
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (s *operationService) processRunnable(ctx context.Context) {
	ids, err := s.repo.ListRunnable(ctx, 16)
	if err != nil {
		return
	}
	if !s.processOperations(ctx, ids, "operation-worker", false) {
		return
	}
	reconcileRepo, ok := s.repo.(interface {
		ListReconcileRunnable(context.Context, int) ([]domain.ID, error)
	})
	if !ok {
		return
	}
	ids, err = reconcileRepo.ListReconcileRunnable(ctx, 16)
	if err != nil {
		return
	}
	s.processOperations(ctx, ids, "operation-reconciler", true)
}

func (s *operationService) processOperations(ctx context.Context, ids []domain.ID, worker string, reconcile bool) bool {
	for _, id := range ids {
		operation, getErr := s.repo.Get(ctx, id)
		if getErr != nil || operation == nil || !s.operationInRegion(ctx, operation) {
			continue
		}
		// Metrics: one age observation per open operation that passed the load
		// guards (Get success, non-nil, in-region). The zero-CreatedAt skip is
		// defensive — time.Since(zero) would expose a ~5.4e13s gauge value.
		if !operation.CreatedAt.IsZero() {
			s.metrics.ObserveOperationAge(string(operation.Status), time.Since(operation.CreatedAt))
		}
		if reconcile {
			reconciler, ok := s.runner.(OperationOutcomeReconciler)
			if !ok {
				continue
			}
			if _, err := s.reconcileClaimed(ctx, operation, worker, reconciler); err != nil {
				return false
			}
			continue
		}
		if _, runErr := s.runClaimed(ctx, operation, worker); runErr != nil {
			return false
		}
	}
	return true
}

func (s *operationService) operationInRegion(ctx context.Context, operation *domain.Operation) bool {
	if s.accounts == nil {
		return true
	}
	account, err := s.accounts.GetByID(ctx, operation.TenantID, operation.AccountID)
	return err == nil && account != nil && account.HomeRegion == s.region
}
