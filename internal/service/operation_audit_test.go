package service

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/google/uuid"
)

// opRepoWithoutAudit implements the plain operation repository surface but NOT
// the OperationStateAuditRepository interfaces — the FM-9 fail-closed shape.
// The embedded interface satisfies the untouched OperationRepository methods.
type opRepoWithoutAudit struct {
	repository.OperationRepository
	finishStepCalls int
	statusCalls     int
	compCalls       int
	createCalls     int
}

func (f *opRepoWithoutAudit) Create(context.Context, *domain.Operation) error {
	f.createCalls++
	return nil
}

func (f *opRepoWithoutAudit) FinishStep(context.Context, domain.ID, string, domain.OperationStepStatus, string, string, map[string]any, string) error {
	f.finishStepCalls++
	return nil
}
func (f *opRepoWithoutAudit) SetStatus(context.Context, domain.ID, domain.OperationStatus, string, string, map[string]any) error {
	f.statusCalls++
	return nil
}
func (f *opRepoWithoutAudit) FinishCompensationStep(context.Context, domain.ID, string, domain.OperationStepStatus, string, string, map[string]any, string) error {
	f.compCalls++
	return nil
}

// opRepoWithAudit additionally implements OperationStateAuditRepository.
type opRepoWithAudit struct {
	opRepoWithoutAudit
	finishedWithAudit    int
	setWithAudit         int
	compWithAudit        int
	createWithAuditCalls int
	facts                []*domain.AuditFact
}

func (f *opRepoWithAudit) FinishStepWithAudit(ctx context.Context, id domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, code string, result map[string]any, message string, fact *domain.AuditFact) error {
	f.finishedWithAudit++
	f.facts = append(f.facts, fact)
	return nil
}
func (f *opRepoWithAudit) CreateWithAudit(ctx context.Context, op *domain.Operation, fact *domain.AuditFact) error {
	f.createWithAuditCalls++
	f.facts = append(f.facts, fact)
	return nil
}
func (f *opRepoWithAudit) SetStatusWithAudit(ctx context.Context, id domain.ID, status domain.OperationStatus, code, message string,
	result map[string]any, fact *domain.AuditFact) error {
	f.setWithAudit++
	f.facts = append(f.facts, fact)
	return nil
}
func (f *opRepoWithAudit) FinishCompensationStepWithAudit(ctx context.Context, id domain.ID, worker string, status domain.OperationStepStatus,
	sourceOperationID, code string, result map[string]any, message string, fact *domain.AuditFact) error {
	f.compWithAudit++
	f.facts = append(f.facts, fact)
	return nil
}

type auditRecordSpy struct {
	records int
	fail    error
}

func (s *auditRecordSpy) Record(ctx context.Context, input AuditFactInput) error {
	s.records++
	return s.fail
}
func (s *auditRecordSpy) Log(ctx context.Context, legacy *domain.AuditEvent) error { return nil }
func (s *auditRecordSpy) List(ctx context.Context, filter domain.AuditFactFilter) ([]*domain.AuditFact, string, error) {
	return nil, "", nil
}
func (s *auditRecordSpy) VerifyChain(ctx context.Context, source, partition string) error { return nil }

func testOperation() *domain.Operation {
	return &domain.Operation{ID: idgen.NewID(), AccountID: idgen.NewID(), ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-1", HomeRegion: "local", OperationType: "account.sync", Status: domain.OperationRunning}
}

func testStep() *domain.OperationStep {
	return &domain.OperationStep{ID: idgen.NewID(), StepKey: "step-1", Source: "aero-im", Status: domain.StepRunning}
}

// TestOperationAuditUnavailableWhenRepoLacksWithAudit (REQ-5 / FM-9): with
// audit wired and a repo lacking the WithAudit interfaces, the operation
// service must fail closed with operation.audit_unavailable and never fall
// back to a standalone Record — the second write path is gone.
func TestOperationAuditUnavailableWhenRepoLacksWithAudit(t *testing.T) {
	svc := &operationService{repo: &opRepoWithoutAudit{}, audit: &auditRecordSpy{}}
	op := testOperation()
	step := testStep()

	if err := svc.finishStep(t.Context(), op, step, "worker-1", domain.StepSucceeded, "src-1", "", nil, ""); err == nil {
		t.Fatal("finishStep must fail closed when the repo lacks FinishStepWithAudit")
	}
	if err := svc.setOperationStatus(t.Context(), op, domain.OperationCompleted, "", "", nil, "operation.finished", nil); err == nil {
		t.Fatal("setOperationStatus must fail closed when the repo lacks SetStatusWithAudit")
	}
	if err := svc.finishCompensationStep(t.Context(), op, step, "worker-1", domain.StepSucceeded, "src-1", "", nil, "", "operation.compensated"); err == nil {
		t.Fatal("finishCompensationStep must fail closed when the repo lacks FinishCompensationStepWithAudit")
	}
}

// TestOperationCreateWithAuditFailsClosedWhenRepoLacksWithAudit (FM-9): with
// audit wired and a repo lacking OperationAuditRepository, createWithAudit
// must fail closed with operation.audit_unavailable and never fall back to a
// plain Create that drops the operation.requested fact.
func TestOperationCreateWithAuditFailsClosedWhenRepoLacksWithAudit(t *testing.T) {
	repo := &opRepoWithoutAudit{}
	svc := &operationService{repo: repo, audit: &auditRecordSpy{}}
	if err := svc.createWithAudit(t.Context(), testOperation()); err == nil {
		t.Fatal("createWithAudit must fail closed when the repo lacks OperationAuditRepository")
	}
	if repo.createCalls != 0 {
		t.Fatalf("plain Create calls = %d, want 0 (no silent fallback)", repo.createCalls)
	}
}

// TestOperationCreateWithAuditWiredPath: with audit wired and a repo
// implementing OperationAuditRepository, the fact rides the create transaction
// and no standalone surface is touched.
func TestOperationCreateWithAuditWiredPath(t *testing.T) {
	repo := &opRepoWithAudit{}
	audit := &auditRecordSpy{}
	svc := &operationService{repo: repo, audit: audit}
	if err := svc.createWithAudit(t.Context(), testOperation()); err != nil {
		t.Fatalf("createWithAudit: %v", err)
	}
	if repo.createWithAuditCalls != 1 {
		t.Fatalf("CreateWithAudit calls = %d, want 1", repo.createWithAuditCalls)
	}
	if len(repo.facts) != 1 {
		t.Fatalf("facts appended = %d, want 1", len(repo.facts))
	}
	if audit.records != 0 {
		t.Fatalf("standalone Record calls = %d, want 0 (single write path)", audit.records)
	}
}

// TestOperationCreateWithAuditDisabledPlainPath: with s.audit == nil (unit-test
// construction only) createWithAudit uses the plain Create surface.
func TestOperationCreateWithAuditDisabledPlainPath(t *testing.T) {
	repo := &opRepoWithoutAudit{}
	svc := &operationService{repo: repo, audit: nil}
	if err := svc.createWithAudit(t.Context(), testOperation()); err != nil {
		t.Fatalf("createWithAudit: %v", err)
	}
	if repo.createCalls != 1 {
		t.Fatalf("plain Create calls = %d, want 1", repo.createCalls)
	}
}

// TestOperationAuditNoFallbackRecordWhenWired: the WithAudit repo receives the
// fact and the standalone Record surface is never touched.
func TestOperationAuditNoFallbackRecordWhenWired(t *testing.T) {
	repo := &opRepoWithAudit{}
	audit := &auditRecordSpy{}
	svc := &operationService{repo: repo, audit: audit}
	op := testOperation()
	step := testStep()

	if err := svc.finishStep(t.Context(), op, step, "worker-1", domain.StepSucceeded, "src-1", "", nil, ""); err != nil {
		t.Fatalf("finishStep: %v", err)
	}
	if err := svc.setOperationStatus(t.Context(), op, domain.OperationCompleted, "", "", nil, "operation.finished", nil); err != nil {
		t.Fatalf("setOperationStatus: %v", err)
	}
	if err := svc.finishCompensationStep(t.Context(), op, step, "worker-1", domain.StepSucceeded, "src-1", "", nil, "", "operation.compensated"); err != nil {
		t.Fatalf("finishCompensationStep: %v", err)
	}
	if repo.finishedWithAudit != 1 || repo.setWithAudit != 1 || repo.compWithAudit != 1 {
		t.Fatalf("WithAudit calls = %d/%d/%d, want 1/1/1", repo.finishedWithAudit, repo.setWithAudit, repo.compWithAudit)
	}
	if len(repo.facts) != 3 {
		t.Fatalf("facts appended = %d, want 3", len(repo.facts))
	}
	if audit.records != 0 {
		t.Fatalf("standalone Record calls = %d, want 0 (single write path)", audit.records)
	}
}

// TestOperationAuditDisabledPlainPath: with s.audit == nil the plain repo
// methods are used and no audit surface is consulted.
func TestOperationAuditDisabledPlainPath(t *testing.T) {
	repo := &opRepoWithoutAudit{}
	svc := &operationService{repo: repo, audit: nil}
	op := testOperation()
	step := testStep()

	if err := svc.finishStep(t.Context(), op, step, "worker-1", domain.StepSucceeded, "src-1", "", nil, ""); err != nil {
		t.Fatalf("finishStep: %v", err)
	}
	if err := svc.setOperationStatus(t.Context(), op, domain.OperationCompleted, "", "", nil, "operation.finished", nil); err != nil {
		t.Fatalf("setOperationStatus: %v", err)
	}
	if repo.finishStepCalls != 1 || repo.statusCalls != 1 {
		t.Fatalf("plain calls = %d/%d, want 1/1", repo.finishStepCalls, repo.statusCalls)
	}
}

// goldenOperation / goldenStep rebuild the derivation-contract fixture ID set
// (§3 goldens: namespace 38248e3a-32f0-4929-9b56-b2dbd75aa849, NUL-joined
// parts [action, tenant, operation, step-or-"", status]).
func goldenOperation() *domain.Operation {
	return &domain.Operation{ID: uuid.MustParse("0195b2c6-9f40-7c01-8000-000000000001"),
		AccountID: uuid.MustParse("33333333-3333-4333-8333-333333333333"), ActorUID: "actor-1", TenantID: "tenant-alpha",
		RequestID: "req-1", HomeRegion: "local", OperationType: "account.sync", Status: domain.OperationUnknown}
}

func goldenStep() *domain.OperationStep {
	return &domain.OperationStep{ID: uuid.MustParse("0195b2c6-9f40-7c02-8000-000000000001"),
		StepKey: "step-1", Source: "aero-im", Status: domain.StepRunning}
}

// TestOperationStepFinished_Anchored (M-1): operationAuditFact with the golden
// fixture set derives the contract's step-scoped event_id (G2b); identical
// retries re-derive it, and a different step or status splits the fact (G2 vs
// G2b — a status-changing reconcile records both outcomes).
func TestOperationStepFinished_Anchored(t *testing.T) {
	op, step := goldenOperation(), goldenStep()
	fact := operationAuditFact(t.Context(), op, step.ID, string(domain.StepSucceeded), "operation.step.finished", nil)
	const want = "6aba9a00-8639-5474-9bf6-fa430aa22db0"
	if fact.EventID.String() != want {
		t.Fatalf("step-scoped EventID = %s, want golden %s", fact.EventID, want)
	}
	retry := operationAuditFact(t.Context(), op, step.ID, string(domain.StepSucceeded), "operation.step.finished", nil)
	if retry.EventID != fact.EventID {
		t.Fatalf("identical retry re-derived %s, want %s", retry.EventID, fact.EventID)
	}
	if got := operationAuditFact(t.Context(), op, step.ID, string(domain.StepUnknown), "operation.step.finished", nil).EventID; got == fact.EventID {
		t.Fatal("changed step status must change the EventID")
	} else if got.String() != "0a0ccd8c-5984-5a83-8d15-f2438cfc4516" {
		t.Fatalf("unknown-status anchor = %s, want G2 0a0ccd8c-5984-5a83-8d15-f2438cfc4516", got)
	}
	otherStep := goldenStep()
	otherStep.ID = uuid.MustParse("99999999-9999-4999-8999-999999999999")
	if got := operationAuditFact(t.Context(), op, otherStep.ID, string(domain.StepSucceeded), "operation.step.finished", nil).EventID; got == fact.EventID {
		t.Fatal("changed step ID must change the EventID")
	}
}

// TestOperationReconciled_Anchored (M-2/M-4): the step-less operation-level
// shape (zero stepID, status slot filled) derives the contract's
// operation-scoped event_id (G3). Status is part of the anchor: a
// status-changing later pass records its own fact, an identical retry dedups.
func TestOperationReconciled_Anchored(t *testing.T) {
	op := goldenOperation()
	fact := operationAuditFact(t.Context(), op, domain.ID{}, string(op.Status), "operation.reconciled", map[string]any{"worker": "w-1"})
	const want = "73564c08-3ffc-5aee-8d6b-de886cb93737"
	if fact.EventID.String() != want {
		t.Fatalf("operation-scoped EventID = %s, want golden %s", fact.EventID, want)
	}
	if retry := operationAuditFact(t.Context(), op, domain.ID{}, string(op.Status), "operation.reconciled", nil).EventID; retry != fact.EventID {
		t.Fatalf("identical retry re-derived %s, want %s", retry, fact.EventID)
	}
	// Status sensitivity (D-6): operation.finished/operation.reconciled can
	// legitimately carry different statuses across passes; each status mints
	// its own event_id (G1/G3/G4/G7 all differ), and two passes with the same
	// status collapse to one anchor.
	statuses := map[string]string{
		"operation.started":      "running",
		"operation.compensating": "compensating",
		"operation.finished":     "completed",
		"operation.reconciled":   "unknown",
	}
	seen := map[domain.ID]string{}
	for action, status := range statuses {
		a := operationAuditFact(t.Context(), op, domain.ID{}, status, action, nil).EventID
		if prev, dup := seen[a]; dup {
			t.Fatalf("%s(%s) collides with %s", action, status, prev)
		}
		seen[a] = action
		if b := operationAuditFact(t.Context(), op, domain.ID{}, status, action, nil).EventID; b != a {
			t.Fatalf("%s not stable across calls: %s vs %s", action, b, a)
		}
		// Different status, same action -> different ID.
		if c := operationAuditFact(t.Context(), op, domain.ID{}, status+"-x", action, nil).EventID; c == a {
			t.Fatalf("%s status must be part of the anchor", action)
		}
	}
	if fact.EventID.String() != want {
		t.Fatalf("operation.reconciled EventID = %s, want golden %s", fact.EventID, want)
	}
	step := goldenStep()
	if got := operationAuditFact(t.Context(), op, step.ID, string(domain.StepSucceeded), "operation.step.finished", nil).EventID; got == fact.EventID {
		t.Fatal("step-scoped anchor must differ from the operation-scoped anchor")
	}
}
