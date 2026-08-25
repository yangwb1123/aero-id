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
	worker = operationWorkerRole(worker)
	startCtx, startCancel := operationPersistenceContext(ctx)
	if err := s.setOperationStatus(startCtx, operation, domain.OperationRunning, "", "", nil, "operation.started",
		map[string]any{"status": domain.OperationRunning, "worker_role": worker}); err != nil {
		startCancel()
		return nil, apperr.Internal("operation.start_failed", "failed to start operation").Wrap(err)
	}
	startCancel()
	for {
		owner := operationLeaseOwner(worker)
		step, claimErr := s.repo.ClaimNextStep(ctx, operation.ID, owner, operationLeaseDuration)
		if claimErr != nil {
			if errors.Is(claimErr, gorm.ErrRecordNotFound) || errors.Is(claimErr, repository.ErrOperationStepUnknown) {
				break
			}
			return nil, apperr.Internal("operation.claim_failed", "failed to claim operation step").Wrap(claimErr)
		}
		if err := s.runClaimedStep(ctx, operation, step, owner); err != nil {
			return nil, err
		}
		if step.Status == domain.StepUnknown || step.Status == domain.StepFailed {
			break
		}
	}
	finishCtx, finishCancel := operationPersistenceContext(ctx)
	defer finishCancel()
	return s.finishOperation(finishCtx, operation.ID)
}

func terminalOperationStatus(status domain.OperationStatus) bool {
	return status == domain.OperationCompleted || status == domain.OperationFailed ||
		status == domain.OperationPartiallyCompleted || status == domain.OperationUnknown
}

func (s *operationService) runClaimedStep(ctx context.Context, operation *domain.Operation, step *domain.OperationStep, worker string) error {
	stateCtx, stateCancel := operationPersistenceContext(ctx)
	defer stateCancel()
	if s.runner == nil {
		if err := s.finishStep(stateCtx, operation, step, worker, domain.StepFailed, "", "operation.runner_unavailable", nil,
			"operation runner is not configured"); err != nil {
			return apperr.Internal("operation.finish_step_failed", "failed to finish operation step").Wrap(err)
		}
		return s.abortOperationSteps(ctx, operation, worker)
	}
	remoteCtx, remoteCancel := operationRemoteContext(ctx)
	sourceOperationID, result, runErr := s.runner.Run(remoteCtx, operation, step)
	remoteCancel()
	status, code, message := operationRunStatus(runErr)
	if err := s.finishStep(stateCtx, operation, step, worker, status, sourceOperationID, code, result, message); err != nil {
		return apperr.Internal("operation.finish_step_failed", "failed to finish operation step").Wrap(err)
	}
	step.Status = status
	if status != domain.StepFailed {
		return nil
	}
	return s.abortOperationSteps(ctx, operation, worker)
}

func (s *operationService) abortOperationSteps(ctx context.Context, operation *domain.Operation, worker string) error {
	stateCtx, cancel := operationPersistenceContext(ctx)
	defer cancel()
	if err := s.repo.SkipPendingSteps(stateCtx, operation.ID, "operation.aborted", "operation stopped after a failed step"); err != nil {
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
	var appErr *apperr.AppError
	if errors.As(runErr, &appErr) && appErr.Code == invalidOperationDatasetScopeCode {
		return domain.StepFailed, invalidOperationDatasetScopeCode, appErr.Message
	}
	var summaryErr *repository.SummaryApplicationError
	if errors.As(runErr, &summaryErr) && summaryErr.Kind == repository.SummaryApplyCommitUnknown {
		return domain.StepUnknown, "projection.commit_unknown", "projection commit outcome is unknown"
	}
	if operationRunOutcomeUnknown(runErr) {
		return domain.StepUnknown, "operation.unknown_outcome", "remote outcome is unknown"
	}
	return domain.StepFailed, "operation.step_failed", "operation step failed"
}

func operationRunOutcomeUnknown(runErr error) bool {
	if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
		return true
	}
	remote, ok := connector.IsRemote(runErr)
	return ok && (remote.Kind == connector.ErrorTimeout || remote.Kind == connector.ErrorUnavailable || remote.Kind == connector.ErrorUnknown)
}

func (s *operationService) Start(ctx context.Context, interval time.Duration) func() {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.workerGate != nil {
		s.workerGate.accepting.Store(true)
	}
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
	return func() {
		// Fencing starts before cancellation. New transport calls and the next
		// worker tick are rejected while the in-flight attempt is finalized.
		if s.workerGate != nil {
			s.workerGate.beginDrain()
		}
		cancel()
		<-done
	}
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
