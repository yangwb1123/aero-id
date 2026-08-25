package service

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// crashTestRepo is a stateful in-memory OperationRepository that models a
// worker crash: a step left running/unknown with an expired lease and a
// recorded source operation id.
type crashTestRepo struct {
	operation *domain.Operation
	step      *domain.OperationStep
	setStatus domain.OperationStatus
	finished  domain.OperationStepStatus
	claimNext func(ctx context.Context, operationID domain.ID, worker string, lease time.Duration) (*domain.OperationStep, error)
}

func (f *crashTestRepo) Create(ctx context.Context, operation *domain.Operation) error { return nil }
func (f *crashTestRepo) FindByIdempotency(ctx context.Context, key, tenant, account string) (*domain.Operation, error) {
	return nil, repository.ErrNotFound
}
func (f *crashTestRepo) Get(ctx context.Context, id domain.ID) (*domain.Operation, error) {
	clone := *f.operation
	steps := make([]*domain.OperationStep, len(f.operation.Steps))
	copy(steps, f.operation.Steps)
	clone.Steps = steps
	return &clone, nil
}
func (f *crashTestRepo) List(ctx context.Context, actor, tenant string, page domain.Page) ([]*domain.Operation, string, error) {
	return nil, "", nil
}
func (f *crashTestRepo) ListRunnable(ctx context.Context, limit int) ([]domain.ID, error) {
	return nil, nil
}
func (f *crashTestRepo) ListReconcileRunnable(ctx context.Context, limit int) ([]domain.ID, error) {
	return []domain.ID{f.operation.ID}, nil
}
func (f *crashTestRepo) ClaimNextStep(ctx context.Context, operationID domain.ID, worker string, lease time.Duration) (*domain.OperationStep, error) {
	if f.claimNext != nil {
		return f.claimNext(ctx, operationID, worker, lease)
	}
	return nil, gorm.ErrRecordNotFound
}
func (f *crashTestRepo) ClaimUnknownStep(ctx context.Context, operationID domain.ID, worker string, lease time.Duration) (*domain.OperationStep, error) {
	if f.step.Status != domain.StepUnknown {
		return nil, gorm.ErrRecordNotFound
	}
	// Model the real lease semantics: an unknown step with a live lease (e.g.
	// the unknown-retry pacing lease) is not claimable until it expires.
	if f.step.LeaseUntil != nil && f.step.LeaseUntil.After(time.Now().UTC()) {
		return nil, gorm.ErrRecordNotFound
	}
	step := *f.step
	f.step.Status = domain.StepRunning
	f.step.LeaseOwner = worker
	f.step.LeaseKind = "reconcile"
	until := time.Now().UTC().Add(lease)
	f.step.LeaseUntil = &until
	return &step, nil
}
func (f *crashTestRepo) FinishStep(ctx context.Context, operationID domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, code string, result map[string]any, message string) error {
	f.finished = status
	f.step.Status = status
	f.step.SourceOperationID = sourceOperationID
	f.step.LeaseOwner = ""
	f.step.LeaseKind = ""
	// Model the repository's unknown-retry pacing: an unknown reconcile result
	// keeps a short lease so ClaimUnknownStep does not return it immediately.
	if status == domain.StepUnknown {
		until := time.Now().UTC().Add(repository.ReconcileUnknownRetryDelay)
		f.step.LeaseUntil = &until
	} else {
		f.step.LeaseUntil = nil
	}
	return nil
}
func (f *crashTestRepo) ClaimCompensationStep(ctx context.Context, operationID domain.ID, worker string, lease time.Duration) (*domain.OperationStep, error) {
	return nil, gorm.ErrRecordNotFound
}
func (f *crashTestRepo) ClaimUnknownCompensationStep(ctx context.Context, operationID domain.ID, worker string, lease time.Duration) (*domain.OperationStep, error) {
	return nil, gorm.ErrRecordNotFound
}
func (f *crashTestRepo) FinishCompensationStep(ctx context.Context, operationID domain.ID, worker string,
	status domain.OperationStepStatus, sourceOperationID, code string, result map[string]any, message string) error {
	return nil
}
func (f *crashTestRepo) SkipPendingSteps(ctx context.Context, operationID domain.ID, code, message string) error {
	return nil
}
func (f *crashTestRepo) SetStatus(ctx context.Context, operationID domain.ID, status domain.OperationStatus, code, message string, result map[string]any) error {
	f.setStatus = status
	f.operation.Status = status
	return nil
}

// crashRunner records Run invocations (must stay zero during reconciliation)
// and answers source-outcome queries from a fixed table.
type crashRunner struct {
	runCalls        int
	reconcileCalls  int
	sourceOutcome   domain.OperationStepStatus
	sourceOperation string
}

func (r *crashRunner) Run(ctx context.Context, operation *domain.Operation, step *domain.OperationStep) (string, map[string]any, error) {
	r.runCalls++
	return "src-op-1", map[string]any{"ok": true}, nil
}

func (r *crashRunner) Reconcile(ctx context.Context, operation *domain.Operation, step *domain.OperationStep) (domain.OperationStepStatus, string, map[string]any, error) {
	r.reconcileCalls++
	return r.sourceOutcome, r.sourceOperation, map[string]any{"reconciled": true}, nil
}

func (r *crashRunner) ReconcileCompensation(ctx context.Context, operation *domain.Operation,
	step *domain.OperationStep) (domain.OperationStepStatus, string, map[string]any, error) {
	return domain.StepSucceeded, "", nil, nil
}

func crashedOperation(status domain.OperationStatus, stepStatus domain.OperationStepStatus) *domain.Operation {
	stepID := idgen.NewID()
	operationID := idgen.NewID()
	past := time.Now().UTC().Add(-time.Hour)
	return &domain.Operation{
		ID: operationID, AccountID: idgen.NewID(), ActorUID: "u-1", TenantID: "tenant-a",
		HomeRegion: "local", OperationType: "source.erase", Status: status,
		Steps: []*domain.OperationStep{{
			ID: stepID, OperationID: operationID, StepKey: "erase-vault", Source: "aero-vault",
			Action: "erase", Status: stepStatus, Attempt: 1, LeaseOwner: "dead-worker",
			RequestPayload: map[string]any{"datasets": []any{"aero-vault.tenants"}},
			LeaseKind:      "forward", SourceOperationID: "src-op-1", LeaseUntil: &past,
		}},
	}
}

func reconcileContext() context.Context {
	return WithIdentity(context.Background(), &domain.TokenIdentity{
		TenantID: "tenant-a", Subject: "u-1", Issuer: "snaplink", UID: "u-1",
	})
}

// allowAllPermissions satisfies the service PermissionChecker for tests.
type allowAllPermissions struct{}

func (allowAllPermissions) HasPermission(ctx context.Context, uid, permission string) (bool, error) {
	return true, nil
}

func crashTestService(repo repository.OperationRepository, runner OperationRunner) OperationService {
	return NewOperationService(repo,
		WithOperationRunner(runner),
		WithOperationPermission(allowAllPermissions{}),
		WithOperationRegion("local"),
	)
}

// TestOperationReconcile_AfterWorkerCrashDoesNotReRunRemoteCommand is the
// core release-gate scenario: the remote succeeded, the worker died before
// persisting the result; the reconciler must query the source outcome and
// never blindly re-execute the command.
func TestOperationReconcile_AfterWorkerCrashDoesNotReRunRemoteCommand(t *testing.T) {
	operation := crashedOperation(domain.OperationUnknown, domain.StepUnknown)
	repo := &crashTestRepo{operation: operation, step: operation.Steps[0]}
	runner := &crashRunner{sourceOutcome: domain.StepSucceeded, sourceOperation: "src-op-1"}
	svc := crashTestService(repo, runner)

	result, err := svc.Reconcile(reconcileContext(), operation.ID, "reconciler-1")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if runner.runCalls != 0 {
		t.Fatalf("Run called %d times after a crashed worker; the remote command must never be re-executed", runner.runCalls)
	}
	if runner.reconcileCalls != 1 {
		t.Fatalf("Reconcile called %d times, want 1", runner.reconcileCalls)
	}
	if repo.finished != domain.StepSucceeded {
		t.Errorf("step finished as %s, want succeeded", repo.finished)
	}
	if result.Status != domain.OperationCompleted {
		t.Errorf("operation status = %s, want completed", result.Status)
	}
}

// TestOperationReconcile_SourceStillUnknownKeepsOperationUnknown: when the
// source cannot confirm the outcome, the step stays unknown and the operation
// reports unknown — a timeout is not proof of failure and never a re-run.
func TestOperationReconcile_SourceStillUnknownKeepsOperationUnknown(t *testing.T) {
	operation := crashedOperation(domain.OperationUnknown, domain.StepUnknown)
	repo := &crashTestRepo{operation: operation, step: operation.Steps[0]}
	runner := &crashRunner{sourceOutcome: domain.StepUnknown, sourceOperation: "src-op-1"}
	svc := crashTestService(repo, runner)

	result, err := svc.Reconcile(reconcileContext(), operation.ID, "reconciler-2")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if runner.runCalls != 0 {
		t.Fatalf("Run called %d times; unknown outcome must not be re-executed", runner.runCalls)
	}
	if repo.finished != domain.StepUnknown {
		t.Errorf("step finished as %s, want unknown", repo.finished)
	}
	if result.Status != domain.OperationUnknown {
		t.Errorf("operation status = %s, want unknown", result.Status)
	}
}

// TestOperationReconcile_SourceFailedCompensates: the crashed remote command
// actually failed; the reconciler marks the step failed and the operation must
// not be reported as completed.
func TestOperationReconcile_SourceFailedCompensates(t *testing.T) {
	operation := crashedOperation(domain.OperationUnknown, domain.StepUnknown)
	repo := &crashTestRepo{operation: operation, step: operation.Steps[0]}
	runner := &crashRunner{sourceOutcome: domain.StepFailed, sourceOperation: "src-op-1"}
	svc := crashTestService(repo, runner)

	result, err := svc.Reconcile(reconcileContext(), operation.ID, "reconciler-3")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if runner.runCalls != 0 {
		t.Fatalf("Run called %d times after a confirmed failure", runner.runCalls)
	}
	if repo.finished != domain.StepFailed {
		t.Errorf("step finished as %s, want failed", repo.finished)
	}
	if result.Status == domain.OperationCompleted {
		t.Error("failed step must not yield a completed operation")
	}
}

// TestOperationRun_OrdinaryExecutionStillWorks guards the forward path: a
// fresh operation runs its step through the runner exactly once.
func TestOperationRun_OrdinaryExecutionStillWorks(t *testing.T) {
	operation := crashedOperation(domain.OperationWaiting, domain.StepPending)
	operation.Steps[0].LeaseOwner = ""
	operation.Steps[0].LeaseUntil = nil
	operation.Steps[0].LeaseKind = ""
	repo := &crashTestRepo{operation: operation, step: operation.Steps[0]}
	repo.claimNext = func(ctx context.Context, operationID domain.ID, worker string, lease time.Duration) (*domain.OperationStep, error) {
		if repo.step.Status == domain.StepPending {
			step := *repo.step
			repo.step.Status = domain.StepRunning
			repo.step.LeaseOwner = worker
			until := time.Now().UTC().Add(lease)
			repo.step.LeaseUntil = &until
			return &step, nil
		}
		return nil, gorm.ErrRecordNotFound
	}
	runner := &crashRunner{}
	svc := crashTestService(repo, runner)

	if _, err := svc.Run(reconcileContext(), operation.ID, "executor-1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if runner.runCalls != 1 {
		t.Errorf("Run calls = %d, want 1", runner.runCalls)
	}
	if repo.finished != domain.StepSucceeded {
		t.Errorf("step finished as %s, want succeeded", repo.finished)
	}
}
