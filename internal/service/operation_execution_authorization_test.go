package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"gorm.io/gorm"
)

type executionGateRepo struct {
	*crashTestRepo
	claims, compClaims, queries int
	blocks                      []*domain.AuditFact
	blockErr                    error
	onClaim                     func()
}

func (r *executionGateRepo) BlockExecutionWithAudit(_ context.Context, _ domain.ID, status domain.OperationStatus, code, message string, fact *domain.AuditFact) error {
	if r.blockErr != nil {
		return r.blockErr
	}
	if status != r.operation.Status || fact == nil {
		panic("invalid blocking CAS/audit")
	}
	r.operation.ErrorCode, r.operation.ErrorMessage = code, message
	r.blocks = append(r.blocks, fact)
	return nil
}
func (r *executionGateRepo) ClaimNextStep(_ context.Context, _ domain.ID, owner string, lease time.Duration) (*domain.OperationStep, error) {
	for _, step := range r.operation.Steps {
		if step.Status != domain.StepPending {
			continue
		}
		r.claims++
		step.Status = domain.StepRunning
		step.Attempt++
		step.LeaseOwner = owner
		step.LeaseKind = "forward"
		until := time.Now().Add(lease)
		step.LeaseUntil = &until
		r.step = step
		if r.onClaim != nil {
			r.onClaim()
		}
		copy := *step
		return &copy, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (r *executionGateRepo) ClaimCompensationStep(_ context.Context, _ domain.ID, owner string, lease time.Duration) (*domain.OperationStep, error) {
	if r.step.CompensationStatus != domain.StepPending {
		return nil, gorm.ErrRecordNotFound
	}
	r.compClaims++
	r.step.CompensationStatus = domain.StepRunning
	r.step.CompensationAttempt++
	r.step.CompensationLeaseOwner = owner
	until := time.Now().Add(lease)
	r.step.CompensationLeaseUntil = &until
	if r.onClaim != nil {
		r.onClaim()
	}
	copy := *r.step
	return &copy, nil
}
func (r *executionGateRepo) ClaimUnknownStep(_ context.Context, _ domain.ID, owner string, lease time.Duration) (*domain.OperationStep, error) {
	if r.step.Status != domain.StepUnknown {
		return nil, gorm.ErrRecordNotFound
	}
	r.queries++
	r.step.Status = domain.StepRunning
	r.step.LeaseKind = "reconcile"
	r.step.Attempt++
	r.step.LeaseOwner = owner
	until := time.Now().Add(lease)
	r.step.LeaseUntil = &until
	if r.onClaim != nil {
		r.onClaim()
	}
	copy := *r.step
	return &copy, nil
}
func (r *executionGateRepo) FinishCompensationStep(_ context.Context, _ domain.ID, _ string, status domain.OperationStepStatus,
	id, code string, result map[string]any, _ string) error {
	r.step.CompensationStatus, r.step.CompensationSourceOperationID, r.step.CompensationErrorCode, r.step.CompensationResult = status, id, code, result
	r.step.CompensationLeaseOwner = ""
	r.step.CompensationLeaseUntil = nil
	return nil
}

type executionGateRunner struct {
	calls, compCalls int
	afterRun         func()
}

func (r *executionGateRunner) Run(_ context.Context, _ *domain.Operation, _ *domain.OperationStep) (string, map[string]any, error) {
	r.calls++
	if r.afterRun != nil {
		r.afterRun()
	}
	return "source-success", map[string]any{"status": "completed"}, nil
}
func (r *executionGateRunner) RunCompensation(context.Context, *domain.Operation, *domain.OperationStep) (string, map[string]any, error) {
	r.compCalls++
	return "comp-success", nil, nil
}

func executionGateFixture() (*operationService, *executionGateRepo, *executionGateRunner) {
	op := crashedOperation(domain.OperationRequested, domain.StepPending)
	op.Steps[0].LeaseOwner = ""
	op.Steps[0].LeaseUntil = nil
	op.Steps[0].SourceOperationID = ""
	repo := &executionGateRepo{crashTestRepo: &crashTestRepo{operation: op, step: op.Steps[0]}}
	runner := &executionGateRunner{}
	svc := NewOperationService(repo, operationTestExecutionPorts(), WithOperationRunner(runner)).(*operationService)
	return svc, repo, runner
}
func executionErrorCode(t *testing.T, err error) string {
	t.Helper()
	var app *apperr.AppError
	if !errors.As(err, &app) {
		t.Fatalf("untyped error: %v", err)
	}
	return app.Code
}

func TestOperationWorkerRequiresCurrentAuthorityAndDurableScope(t *testing.T) {
	for _, test := range []string{"denied", "outage", "missing", "typed-nil", "home-changed", "erased", "foreign-account", "foreign-tenant"} {
		t.Run(test, func(t *testing.T) {
			svc, repo, runner := executionGateFixture()
			svc.permission = allowAllPermissions{} // Legacy enqueue fallback cannot authorize execution.
			svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) {
				if test == "outage" {
					return false, errors.New("private authz endpoint")
				}
				return test != "denied", nil
			})
			switch test {
			case "missing":
				svc.executionPermission = nil
			case "typed-nil":
				var checker *operationAuthorizationPermission
				svc.executionPermission = checker
			case "home-changed", "erased", "foreign-account", "foreign-tenant":
				account := &domain.Account{ID: repo.operation.AccountID, TenantID: repo.operation.TenantID, HomeRegion: "local", Status: domain.AccountActive}
				if test == "home-changed" {
					account.HomeRegion = "other"
				}
				if test == "erased" {
					account.Status = domain.AccountErased
				}
				if test == "foreign-account" {
					account.ID = domain.ID{9}
				}
				if test == "foreign-tenant" {
					account.TenantID = "other"
				}
				svc.accounts = &unscopedExecutionAccount{account: account}
			}
			_, err := svc.runClaimed(t.Context(), repo.operation, "background")
			if err == nil || repo.claims != 0 || runner.calls != 0 || repo.operation.Status != domain.OperationRequested || repo.step.Status != domain.StepPending || len(repo.blocks) != 1 {
				t.Fatal("blocked command progressed or lacked durable evidence", err)
			}
		})
	}
}

type unscopedExecutionAccount struct {
	operationExecutionAccountFixture
	account *domain.Account
}

func (r *unscopedExecutionAccount) GetByID(context.Context, string, domain.ID) (*domain.Account, error) {
	return r.account, nil
}

func TestOperationExecutionRejectsMalformedDurableActionScope(t *testing.T) {
	for _, mutation := range []func(*domain.Operation){
		func(op *domain.Operation) { op.Steps[0].RequestPayload = nil },
		func(op *domain.Operation) {
			op.Steps[0].RequestPayload = map[string]any{"datasets": []string{"snaplink.identity"}}
		},
		func(op *domain.Operation) {
			op.Steps[0].CompensationAction = "delete"
			op.Steps[0].RequestPayload = nil
		},
		func(op *domain.Operation) { op.OperationType = "legacy.untyped" },
	} {
		svc, repo, runner := executionGateFixture()
		mutation(repo.operation)
		_, err := svc.runClaimed(t.Context(), repo.operation, "worker")
		if err == nil || repo.claims != 0 || runner.calls != 0 || len(repo.blocks) != 1 {
			t.Fatal("malformed durable scope produced an effect", err)
		}
	}
}

func TestOperationBlockedFactsUseDurableScopeAndDistinctExecutionIDs(t *testing.T) {
	svc, repo, runner := executionGateFixture()
	svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) { return false, nil })
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "operator", TenantID: "ambient-tenant", Verified: true})
	for range 2 {
		if _, err := svc.runClaimed(ctx, repo.operation, "worker"); executionErrorCode(t, err) != "operation.authorization_denied" {
			t.Fatal(err)
		}
	}
	if len(repo.blocks) != 2 || runner.calls != 0 {
		t.Fatal("denials were not independently audited")
	}
	for _, fact := range repo.blocks {
		if fact.TenantID != repo.operation.TenantID || fact.ActorUID != repo.operation.ActorUID || fact.EventID != idgen.Anchor("operation.execution.blocked", repo.operation.TenantID,
			repo.operation.ID.String(), "operation.authorization_denied", fact.ID.String()) {
			t.Fatal("ambient identity changed audit scope or event derivation")
		}
	}
	if repo.blocks[0].EventID == repo.blocks[1].EventID {
		t.Fatal("resource-wide dedup swallowed another blocked execution")
	}
}

func TestOperationWorkerRechecksAfterClaimWithoutChangingIntent(t *testing.T) {
	svc, repo, runner := executionGateFixture()
	allowed := true
	svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) { return allowed, nil })
	repo.onClaim = func() { allowed = false }
	result := repo.step.Result
	_, err := svc.runClaimed(t.Context(), repo.operation, "background")
	if executionErrorCode(t, err) != "operation.authorization_denied" || runner.calls != 0 || repo.claims != 1 || repo.step.Status != domain.StepPending ||
		repo.step.LeaseOwner != "" || repo.step.SourceOperationID != "" || len(repo.blocks) != 1 || len(repo.step.Result) != len(result) {
		t.Fatal("not-started claim was executed or turned into a remote failure", err)
	}
}

func TestOperationWorkerRevocationBetweenStepsPreservesSuccess(t *testing.T) {
	svc, repo, runner := executionGateFixture()
	second := *repo.step
	second.ID = domain.ID{5}
	second.StepKey = "second"
	repo.operation.Steps = append(repo.operation.Steps, &second)
	allowed := true
	runner.afterRun = func() { allowed = false }
	svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) { return allowed, nil })
	_, err := svc.runClaimed(t.Context(), repo.operation, "background")
	if executionErrorCode(t, err) != "operation.authorization_denied" || runner.calls != 1 || repo.claims != 1 || repo.operation.Steps[0].Status != domain.StepSucceeded ||
		second.Status != domain.StepPending || repo.operation.Steps[0].SourceOperationID != "source-success" {
		t.Fatal("revocation lost a receipt, executed next step or compensated", err)
	}
}

func TestUnknownForwardDoesNotAuthorizePendingCompensation(t *testing.T) {
	op := &domain.Operation{Status: domain.OperationUnknown, Steps: []*domain.OperationStep{
		{Status: domain.StepSucceeded, CompensationAction: "summary", CompensationStatus: domain.StepPending}, {Status: domain.StepUnknown},
	}}
	if shouldResumeCompensation(op) {
		t.Fatal("uncertainty became rollback authority")
	}
	op.Steps[1].Status = domain.StepFailed
	if !shouldResumeCompensation(op) {
		t.Fatal("known failure lost compensation continuation")
	}
	op.Steps[1].Status = domain.StepSucceeded
	if shouldResumeCompensation(op) {
		t.Fatal("confirmed forward success started rollback")
	}
}

func TestOperationCompensationRechecksItsActualPermission(t *testing.T) {
	svc, repo, runner := executionGateFixture()
	repo.operation.Status = domain.OperationCompensating
	repo.operation.OperationType = "source.command"
	repo.step.Status = domain.StepSucceeded
	repo.step.CompensationAction = "summary"
	repo.step.CompensationStatus = domain.StepPending
	allowed := true
	repo.onClaim = func() { allowed = false }
	svc.executionPermission = capabilityPermissions(func(_ context.Context, _ string, permission string) (bool, error) {
		if permission == domain.PermSourceSync {
			return allowed, nil
		}
		return true, nil
	})
	err := svc.compensate(t.Context(), repo.operation, "compensator")
	if executionErrorCode(t, err) != "operation.authorization_denied" || runner.compCalls != 0 || repo.compClaims != 1 || repo.step.CompensationStatus != domain.StepPending ||
		repo.step.Status != domain.StepSucceeded || repo.step.CompensationLeaseOwner != "" {
		t.Fatal("compensation permission was inferred from envelope", err)
	}
}

func TestOperationReconcileRevocationKeepsOriginalUnknownReceipt(t *testing.T) {
	svc, repo, runner := executionGateFixture()
	repo.operation.Status = domain.OperationUnknown
	repo.step.Status = domain.StepUnknown
	repo.step.SourceOperationID = "original-unknown"
	repo.step.Result = map[string]any{"version": 7}
	allowed := true
	repo.onClaim = func() { allowed = false }
	svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) { return allowed, nil })
	queryRunner := &crashRunner{}
	_, err := svc.reconcileClaimed(t.Context(), repo.operation, "reconciler", queryRunner)
	if executionErrorCode(t, err) != "operation.authorization_denied" || runner.calls != 0 || queryRunner.reconcileCalls != 0 || repo.step.Status != domain.StepUnknown ||
		repo.step.SourceOperationID != "original-unknown" || repo.step.Result["version"] != 7 || repo.operation.Status != domain.OperationUnknown {
		t.Fatal("unknown outcome became failure or a second command", err)
	}
}

func TestOperationExecutionUsesInitiatorNotAmbientOperatorAndBoundedChecks(t *testing.T) {
	svc, repo, _ := executionGateFixture()
	svc.executionPermission = capabilityPermissions(func(ctx context.Context, uid, _ string) (bool, error) {
		if uid != repo.operation.ActorUID {
			t.Fatal("ambient operator substituted durable actor")
		}
		identity, _ := IdentityFrom(ctx)
		if identity.UID != "admin" || !identity.Verified {
			t.Fatal("worker fabricated historical verified claims")
		}
		<-ctx.Done()
		return true, nil // Late success must not manufacture a grant.
	})
	ctx := WithIdentity(t.Context(), &domain.TokenIdentity{UID: "admin", TenantID: "tenant-a", Verified: true})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	defer cancel()
	if executionErrorCode(t, svc.authorizeOperationExecution(ctx, repo.operation, nil, false)) != "operation.authorization_unavailable" {
		t.Fatal("late grant accepted")
	}
}

func TestOperationWorkerLeaseLossDuringAuthorizationSendsNoEffect(t *testing.T) {
	svc, repo, runner := executionGateFixture()
	repo.onClaim = func() { repo.step.LeaseOwner = "new-owner" }
	_, err := svc.runClaimed(t.Context(), repo.operation, "old-owner")
	if executionErrorCode(t, err) != "operation.execution_lease_lost" || runner.calls != 0 || repo.step.Status != domain.StepRunning || repo.step.LeaseOwner != "new-owner" || len(repo.blocks) != 1 {
		t.Fatal("lost claimant executed or rewrote another owner's outcome", err)
	}
}

type executionBatchRepo struct {
	*executionGateRepo
	items map[domain.ID]*domain.Operation
}

func (r *executionBatchRepo) Get(_ context.Context, id domain.ID) (*domain.Operation, error) {
	r.operation = r.items[id]
	r.step = r.operation.Steps[0]
	copy := *r.operation
	return &copy, nil
}
func TestOperationWorkerBlockedTenantDoesNotStopFollowingWork(t *testing.T) {
	svc, repo, runner := executionGateFixture()
	first := repo.operation
	_, other, _ := executionGateFixture()
	second := other.operation
	second.ActorUID = "allowed-actor"
	batch := &executionBatchRepo{executionGateRepo: repo, items: map[domain.ID]*domain.Operation{first.ID: first, second.ID: second}}
	svc.repo = batch
	svc.executionPermission = capabilityPermissions(func(_ context.Context, uid, _ string) (bool, error) { return uid == "allowed-actor", nil })
	if !svc.processOperations(t.Context(), []domain.ID{first.ID, second.ID}, "worker", false) || runner.calls != 1 || first.Status != domain.OperationRequested ||
		first.Steps[0].Status != domain.StepPending || second.Status != domain.OperationCompleted {
		t.Fatal("blocked operation starved unrelated work or changed outcomes")
	}
}

func TestOperationBlockedAuditFailureStopsBeforeClaim(t *testing.T) {
	svc, repo, runner := executionGateFixture()
	repo.blockErr = errors.New("ledger failed")
	svc.executionPermission = capabilityPermissions(func(context.Context, string, string) (bool, error) { return false, nil })
	_, err := svc.runClaimed(t.Context(), repo.operation, "background")
	if executionErrorCode(t, err) != "operation.execution_block_unknown" || runner.calls != 0 || repo.claims != 0 {
		t.Fatal("audit failure opened execution", err)
	}
}
