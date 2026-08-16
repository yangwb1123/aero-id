package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// operationStateOp builds an operation with an explicit CreatedAt (the shared
// crashedOperation helper leaves it zero) and the given steps.
func operationStateOp(status domain.OperationStatus, steps []*domain.OperationStep) *domain.Operation {
	return &domain.Operation{
		ID: idgen.NewID(), AccountID: idgen.NewID(), ActorUID: "u-1", TenantID: "tenant-a",
		HomeRegion: "local", OperationType: "source.erase", Status: status,
		CreatedAt: time.Now().UTC().Add(-time.Hour),
		Steps:     steps,
	}
}

// runnableListRepo overrides the two worker list feeds on top of crashTestRepo
// so each test drives the runnable vs reconcile-runnable path independently
// (crashTestRepo.ListReconcileRunnable otherwise returns the operation id and
// would double-emit on the runnable path).
type runnableListRepo struct {
	*crashTestRepo
	runnableIDs  []domain.ID
	reconcileIDs []domain.ID
}

func (f *runnableListRepo) ListRunnable(ctx context.Context, limit int) ([]domain.ID, error) {
	return f.runnableIDs, nil
}

func (f *runnableListRepo) ListReconcileRunnable(ctx context.Context, limit int) ([]domain.ID, error) {
	return f.reconcileIDs, nil
}

// setStatusErroringRepo fails the terminal persist to prove the outcome
// observation only follows a durable commit.
type setStatusErroringRepo struct{ *crashTestRepo }

func (f *setStatusErroringRepo) SetStatus(ctx context.Context, operationID domain.ID, status domain.OperationStatus,
	code, message string, result map[string]any) error {
	return errors.New("persist failed")
}

// getErroringRepo fails the reload to prove error paths emit nothing.
type getErroringRepo struct{ *crashTestRepo }

func (f *getErroringRepo) Get(ctx context.Context, id domain.ID) (*domain.Operation, error) {
	return nil, errors.New("reload failed")
}

// TestFinishOperationEmitsOutcomeOncePerTerminalStatus pins AC-1: finishOperation
// emits exactly one outcome observation per execution, labeled with the terminal
// status computed by inspectOperationProgress/progress.status(). The loaded
// status is non-terminal (running) so the transition guard fires for every case.
func TestFinishOperationEmitsOutcomeOncePerTerminalStatus(t *testing.T) {
	cases := []struct {
		name     string
		steps    []*domain.OperationStep
		expected domain.OperationStatus
	}{
		{"completed", []*domain.OperationStep{{Status: domain.StepSucceeded}}, domain.OperationCompleted},
		{"failed", []*domain.OperationStep{{Status: domain.StepFailed}}, domain.OperationFailed},
		// A failed step with a failed compensation is the partially_completed
		// fixture: CompensationStatus=Pending would map to unknown (pending
		// compensation => compensationUnknown) before failedStatus() runs.
		{"partially_completed", []*domain.OperationStep{{Status: domain.StepFailed, CompensationAction: "undo", CompensationStatus: domain.StepFailed}}, domain.OperationPartiallyCompleted},
		{"unknown", []*domain.OperationStep{{Status: domain.StepUnknown}}, domain.OperationUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &metricSpy{}
			op := operationStateOp(domain.OperationRunning, tc.steps)
			repo := &crashTestRepo{operation: op, step: op.Steps[0]}
			svc := NewOperationService(repo, WithOperationMetrics(spy)).(*operationService)

			if _, err := svc.finishOperation(context.Background(), op.ID); err != nil {
				t.Fatalf("finishOperation: %v", err)
			}
			if len(spy.operationOutcomes) != 1 {
				t.Fatalf("outcome observations = %d, want 1", len(spy.operationOutcomes))
			}
			if spy.operationOutcomes[0] != string(tc.expected) {
				t.Errorf("outcome = %q, want %q", spy.operationOutcomes[0], tc.expected)
			}
		})
	}
}

// TestFinishOperationTransitionGuard pins the terminal + transition guard: a
// reconcile pass on an already-unknown operation re-enters finishOperation and
// must not re-count the same outcome; a genuine unknown -> completed transition
// still emits.
func TestFinishOperationTransitionGuard(t *testing.T) {
	cases := []struct {
		name     string
		status   domain.OperationStatus
		steps    []*domain.OperationStep
		expected int
	}{
		{"unknown re-entry emits nothing", domain.OperationUnknown, []*domain.OperationStep{{Status: domain.StepUnknown}}, 0},
		{"unknown to completed emits once", domain.OperationUnknown, []*domain.OperationStep{{Status: domain.StepSucceeded}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &metricSpy{}
			op := operationStateOp(tc.status, tc.steps)
			repo := &crashTestRepo{operation: op, step: op.Steps[0]}
			svc := NewOperationService(repo, WithOperationMetrics(spy)).(*operationService)

			if _, err := svc.finishOperation(context.Background(), op.ID); err != nil {
				t.Fatalf("finishOperation: %v", err)
			}
			if len(spy.operationOutcomes) != tc.expected {
				t.Errorf("outcome observations = %d, want %d", len(spy.operationOutcomes), tc.expected)
			}
		})
	}
}

// TestProcessRunnableEmitsOperationAgeForOpenOperations pins AC-2: one age
// observation per open operation per worker pass, from both the runnable and
// the reconcile-runnable list feeds, with the loaded status label.
func TestProcessRunnableEmitsOperationAgeForOpenOperations(t *testing.T) {
	t.Run("runnable path", func(t *testing.T) {
		spy := &metricSpy{}
		op := operationStateOp(domain.OperationWaiting, []*domain.OperationStep{{Status: domain.StepPending}})
		repo := &runnableListRepo{crashTestRepo: &crashTestRepo{operation: op, step: op.Steps[0]},
			runnableIDs: []domain.ID{op.ID}}
		svc := NewOperationService(repo, WithOperationRunner(&crashRunner{}), WithOperationMetrics(spy)).(*operationService)

		svc.processRunnable(context.Background())
		if len(spy.operationAges) != 1 {
			t.Fatalf("age observations = %d, want 1", len(spy.operationAges))
		}
		obs := spy.operationAges[0]
		if obs.status != "waiting" {
			t.Errorf("age status = %q, want waiting", obs.status)
		}
		if diff := (time.Since(op.CreatedAt) - obs.age).Abs(); diff > 5*time.Second {
			t.Errorf("age = %v, want ~1h (diff %v)", obs.age, diff)
		}
	})
	t.Run("reconcile-runnable path", func(t *testing.T) {
		spy := &metricSpy{}
		op := operationStateOp(domain.OperationUnknown, []*domain.OperationStep{{Status: domain.StepUnknown}})
		repo := &runnableListRepo{crashTestRepo: &crashTestRepo{operation: op, step: op.Steps[0]},
			reconcileIDs: []domain.ID{op.ID}}
		svc := NewOperationService(repo, WithOperationRunner(&crashRunner{}), WithOperationMetrics(spy)).(*operationService)

		svc.processRunnable(context.Background())
		if len(spy.operationAges) != 1 {
			t.Fatalf("age observations = %d, want 1", len(spy.operationAges))
		}
		obs := spy.operationAges[0]
		if obs.status != "unknown" {
			t.Errorf("age status = %q, want unknown", obs.status)
		}
		if len(spy.operationOutcomes) != 0 {
			t.Errorf("outcome observations = %d, want 0 (no repeated unknown outcome)", len(spy.operationOutcomes))
		}
	})
	t.Run("noop default", func(t *testing.T) {
		op := operationStateOp(domain.OperationWaiting, []*domain.OperationStep{{Status: domain.StepPending}})
		repo := &runnableListRepo{crashTestRepo: &crashTestRepo{operation: op, step: op.Steps[0]},
			runnableIDs: []domain.ID{op.ID}}
		svc := NewOperationService(repo, WithOperationRunner(&crashRunner{})).(*operationService)

		svc.processRunnable(context.Background()) // must not panic with the noop default
	})
	t.Run("zero CreatedAt skips emission", func(t *testing.T) {
		spy := &metricSpy{}
		op := operationStateOp(domain.OperationWaiting, []*domain.OperationStep{{Status: domain.StepPending}})
		op.CreatedAt = time.Time{}
		repo := &runnableListRepo{crashTestRepo: &crashTestRepo{operation: op, step: op.Steps[0]},
			runnableIDs: []domain.ID{op.ID}}
		svc := NewOperationService(repo, WithOperationRunner(&crashRunner{}), WithOperationMetrics(spy)).(*operationService)

		svc.processRunnable(context.Background())
		if len(spy.operationAges) != 0 {
			t.Errorf("age observations = %d, want 0", len(spy.operationAges))
		}
	})
}

// TestFinishOperationNoEmissionOnErrorPaths pins AC-3: no outcome observation
// before the terminal status is durably persisted or when the reload fails.
func TestFinishOperationNoEmissionOnErrorPaths(t *testing.T) {
	t.Run("SetStatus error", func(t *testing.T) {
		spy := &metricSpy{}
		op := operationStateOp(domain.OperationRunning, []*domain.OperationStep{{Status: domain.StepSucceeded}})
		repo := &setStatusErroringRepo{crashTestRepo: &crashTestRepo{operation: op, step: op.Steps[0]}}
		svc := NewOperationService(repo, WithOperationMetrics(spy)).(*operationService)

		if _, err := svc.finishOperation(context.Background(), op.ID); err == nil {
			t.Fatal("finishOperation succeeded, want persist error")
		}
		if len(spy.operationOutcomes) != 0 {
			t.Errorf("outcome observations = %d, want 0", len(spy.operationOutcomes))
		}
	})
	t.Run("Get error", func(t *testing.T) {
		spy := &metricSpy{}
		op := operationStateOp(domain.OperationRunning, []*domain.OperationStep{{Status: domain.StepSucceeded}})
		repo := &getErroringRepo{crashTestRepo: &crashTestRepo{operation: op, step: op.Steps[0]}}
		svc := NewOperationService(repo, WithOperationMetrics(spy)).(*operationService)

		if _, err := svc.finishOperation(context.Background(), op.ID); err == nil {
			t.Fatal("finishOperation succeeded, want reload error")
		}
		if len(spy.operationOutcomes) != 0 {
			t.Errorf("outcome observations = %d, want 0", len(spy.operationOutcomes))
		}
	})
}
