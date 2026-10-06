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
// parts [action, tenant, operation, step-or-"", status, attempt]).
// goldenStep carries Attempt: 1 so step-scoped derivations exercise the sixth
// slot; every claim increments it in-tx, so attempt 1 is the first pass.
func goldenOperation() *domain.Operation {
	return &domain.Operation{ID: uuid.MustParse("0195b2c6-9f40-7c01-8000-000000000001"),
		AccountID: uuid.MustParse("33333333-3333-4333-8333-333333333333"), ActorUID: "actor-1", TenantID: "tenant-alpha",
		RequestID: "req-1", HomeRegion: "local", OperationType: "account.sync", Status: domain.OperationUnknown}
}

func goldenStep() *domain.OperationStep {
	return &domain.OperationStep{ID: uuid.MustParse("0195b2c6-9f40-7c02-8000-000000000001"),
		StepKey: "step-1", Source: "aero-im", Status: domain.StepRunning, Attempt: 1}
}

func goldenAnchor(id domain.ID, attempt int) operationStepAnchor {
	return operationStepAnchor{id: id, attempt: attempt}
}

// TestOperationStepFinished_Anchored (M-1): operationAuditFact with the golden
// fixture set derives the contract's step-scoped event_id as a 6-tuple — the
// first five slots unchanged plus the durable attempt (G18/G19); identical
// retries re-derive it, and a different step or status splits the fact.
func TestOperationStepFinished_Anchored(t *testing.T) {
	op, step := goldenOperation(), goldenStep()
	fact := operationAuditFact(t.Context(), op, goldenAnchor(step.ID, step.Attempt), string(domain.StepSucceeded), "operation.step.finished", nil)
	const want = "c3f2623b-0201-5c84-890c-19559a50b061"
	if fact.EventID.String() != want {
		t.Fatalf("step-scoped EventID = %s, want golden %s", fact.EventID, want)
	}
	retry := operationAuditFact(t.Context(), op, goldenAnchor(step.ID, step.Attempt), string(domain.StepSucceeded), "operation.step.finished", nil)
	if retry.EventID != fact.EventID {
		t.Fatalf("identical retry re-derived %s, want %s", retry.EventID, fact.EventID)
	}
	if got := operationAuditFact(t.Context(), op, goldenAnchor(step.ID, step.Attempt), string(domain.StepUnknown), "operation.step.finished", nil).EventID; got == fact.EventID {
		t.Fatal("changed step status must change the EventID")
	} else if got.String() != "4674e436-85bf-59a2-af06-b75d564c6cd4" {
		t.Fatalf("unknown-status anchor = %s, want G19 4674e436-85bf-59a2-af06-b75d564c6cd4", got)
	}
	otherStep := goldenStep()
	otherStep.ID = uuid.MustParse("99999999-9999-4999-8999-999999999999")
	if got := operationAuditFact(t.Context(), op, goldenAnchor(otherStep.ID, otherStep.Attempt), string(domain.StepSucceeded), "operation.step.finished", nil).EventID; got == fact.EventID {
		t.Fatal("changed step ID must change the EventID")
	}
}

// TestOperationReconciled_Anchored (M-2/M-4): the step-less operation-level
// shape (zero stepID, status slot filled) derives the contract's
// operation-scoped event_id (G3). Status is part of the anchor: a
// status-changing later pass records its own fact, an identical retry dedups.
func TestOperationReconciled_Anchored(t *testing.T) {
	op := goldenOperation()
	fact := operationAuditFact(t.Context(), op, operationStepAnchor{}, string(op.Status), "operation.reconciled", map[string]any{"worker": "w-1"})
	const want = "73564c08-3ffc-5aee-8d6b-de886cb93737"
	if fact.EventID.String() != want {
		t.Fatalf("operation-scoped EventID = %s, want golden %s", fact.EventID, want)
	}
	if retry := operationAuditFact(t.Context(), op, operationStepAnchor{}, string(op.Status), "operation.reconciled", nil).EventID; retry != fact.EventID {
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
		a := operationAuditFact(t.Context(), op, operationStepAnchor{}, status, action, nil).EventID
		if prev, dup := seen[a]; dup {
			t.Fatalf("%s(%s) collides with %s", action, status, prev)
		}
		seen[a] = action
		if b := operationAuditFact(t.Context(), op, operationStepAnchor{}, status, action, nil).EventID; b != a {
			t.Fatalf("%s not stable across calls: %s vs %s", action, b, a)
		}
		// Different status, same action -> different ID.
		if c := operationAuditFact(t.Context(), op, operationStepAnchor{}, status+"-x", action, nil).EventID; c == a {
			t.Fatalf("%s status must be part of the anchor", action)
		}
	}
	if fact.EventID.String() != want {
		t.Fatalf("operation.reconciled EventID = %s, want golden %s", fact.EventID, want)
	}
	step := goldenStep()
	if got := operationAuditFact(t.Context(), op, goldenAnchor(step.ID, step.Attempt), string(domain.StepSucceeded), "operation.step.finished", nil).EventID; got == fact.EventID {
		t.Fatal("step-scoped anchor must differ from the operation-scoped anchor")
	}
}

// TestOperationAuditFactAttemptScopedAnchor (AC-1/REQ-1/REQ-4/REQ-9): the
// step-scoped derivation appends the durable claim counter as a sixth slot, so
// every attempt of a still-unknown step records its own ledger fact while a
// retry at the same attempt re-derives the same event_id and dedups. Step-less
// anchors keep their historical 5-tuple byte-for-byte.
func TestOperationAuditFactAttemptScopedAnchor(t *testing.T) {
	op, step := goldenOperation(), goldenStep()
	derive := func(anchor operationStepAnchor, status, action string) domain.ID {
		return operationAuditFact(t.Context(), op, anchor, status, action, nil).EventID
	}

	t.Run("forward step attempts are distinct and each retry is stable", func(t *testing.T) {
		cases := []struct {
			status string
			a1, a2 string
		}{
			{string(domain.StepSucceeded), "c3f2623b-0201-5c84-890c-19559a50b061", "62337cbc-021e-5d1b-a4a2-9552d613463c"},
			{string(domain.StepUnknown), "4674e436-85bf-59a2-af06-b75d564c6cd4", "41d7c885-6a29-5b19-a25b-a99b2ff9d31f"},
		}
		seen := map[string]string{}
		for _, tc := range cases {
			first := derive(goldenAnchor(step.ID, 1), tc.status, "operation.step.finished")
			if first.String() != tc.a1 {
				t.Fatalf("attempt 1 (%s) = %s, want %s", tc.status, first, tc.a1)
			}
			second := derive(goldenAnchor(step.ID, 2), tc.status, "operation.step.finished")
			if second.String() != tc.a2 {
				t.Fatalf("attempt 2 (%s) = %s, want %s", tc.status, second, tc.a2)
			}
			if first == second {
				t.Fatalf("attempt slot must discriminate: attempt 1 and 2 (%s) derived the same EventID %s", tc.status, first)
			}
			// Same attempt re-derived (retry/crash-after-commit) -> same ID.
			for i := 0; i < 3; i++ {
				if got := derive(goldenAnchor(step.ID, 2), tc.status, "operation.step.finished"); got != second {
					t.Fatalf("attempt 2 retry %d re-derived %s, want %s", i, got, second)
				}
			}
			seen[tc.status] = first.String()
		}
		if seen[string(domain.StepSucceeded)] == seen[string(domain.StepUnknown)] {
			t.Fatal("status must remain part of the step-scoped anchor")
		}
	})

	t.Run("compensation attempts are distinct and each retry is stable", func(t *testing.T) {
		cases := []struct {
			action string
			status string
			a1, a2 string
		}{
			{"operation.compensation.finished", string(domain.StepSucceeded), "6aa41161-2b25-59e7-8b8e-2cc821f36894", "919d2391-ff57-5090-a30d-f995dc3e1888"},
			{"operation.compensation.reconciled", string(domain.StepUnknown), "1d924a85-7d33-5947-b30a-9c12d3abf2bb", "5ac8e8e3-f8cd-5624-8243-7f87cb10f65a"},
		}
		for _, tc := range cases {
			first := derive(goldenAnchor(step.ID, 1), tc.status, tc.action)
			if first.String() != tc.a1 {
				t.Fatalf("%s attempt 1 = %s, want %s", tc.action, first, tc.a1)
			}
			second := derive(goldenAnchor(step.ID, 2), tc.status, tc.action)
			if second.String() != tc.a2 {
				t.Fatalf("%s attempt 2 = %s, want %s", tc.action, second, tc.a2)
			}
			if first == second {
				t.Fatalf("%s attempt slot must discriminate, got %s for both", tc.action, first)
			}
			if got := derive(goldenAnchor(step.ID, 2), tc.status, tc.action); got != second {
				t.Fatalf("%s attempt 2 retry re-derived %s, want %s", tc.action, got, second)
			}
		}
	})

	t.Run("step-less anchors keep the historical five-tuple", func(t *testing.T) {
		reconciled := derive(operationStepAnchor{}, string(domain.OperationUnknown), "operation.reconciled")
		if reconciled.String() != "73564c08-3ffc-5aee-8d6b-de886cb93737" {
			t.Fatalf("operation.reconciled = %s, want unchanged golden 73564c08-3ffc-5aee-8d6b-de886cb93737", reconciled)
		}
		// Arity is part of the encoding: a step-less fact must NOT derive the
		// 6-tuple, so it can never collide with an attempt-"0" step fact.
		if withSlot := derive(goldenAnchor(step.ID, 0), string(domain.OperationUnknown), "operation.step.finished"); withSlot == reconciled {
			t.Fatal("step-less anchor must omit the attempt slot")
		} else if withSlot.String() != "c9140059-e0c9-5e63-8bcb-3adf8e962756" {
			t.Fatalf("attempt 0 step fact = %s, want c9140059-e0c9-5e63-8bcb-3adf8e962756", withSlot)
		}
	})

	t.Run("families are disjoint", func(t *testing.T) {
		seen := map[domain.ID]string{}
		for attempt := 1; attempt <= 3; attempt++ {
			for _, action := range []string{"operation.step.finished", "operation.compensation.finished",
				"operation.compensation.reconciled"} {
				for _, status := range []string{string(domain.StepSucceeded), string(domain.StepUnknown), string(domain.StepFailed)} {
					id := derive(goldenAnchor(step.ID, attempt), status, action)
					key := action + "/" + status + "/" + string(rune('0'+attempt))
					if prev, dup := seen[id]; dup {
						t.Fatalf("%s collided with %s", key, prev)
					}
					seen[id] = key
				}
			}
		}
	})

	// Wiring: the fact handed to the repository must carry the attempt taken
	// from the claimed step, and the payload must survive key-allow-list
	// redaction (buildAuditFact redacts at construction, so the fake below
	// observes the post-redaction map).
	t.Run("service wiring feeds the durable counter and payload key", func(t *testing.T) {
		repo := &opRepoWithAudit{}
		svc := &operationService{repo: repo, audit: &auditRecordSpy{}}

		claimed := goldenStep()
		claimed.Attempt = 3
		if err := svc.finishStep(t.Context(), op, claimed, "worker-1", domain.StepSucceeded, "src-1", "", nil, ""); err != nil {
			t.Fatalf("finishStep: %v", err)
		}
		compensating := goldenStep()
		compensating.CompensationAttempt = 2
		if err := svc.finishCompensationStep(t.Context(), op, compensating, "worker-1", domain.StepSucceeded, "src-1", "",
			nil, "", "operation.compensation.finished"); err != nil {
			t.Fatalf("finishCompensationStep: %v", err)
		}
		if len(repo.facts) != 2 {
			t.Fatalf("facts handed to the repository = %d, want 2", len(repo.facts))
		}
		stepFact, compFact := repo.facts[0], repo.facts[1]
		wantStep := idgen.Anchor("operation.step.finished", op.TenantID, op.ID.String(), claimed.ID.String(), "succeeded", "3")
		if stepFact.EventID != wantStep {
			t.Fatalf("finishStep EventID = %s, want %s", stepFact.EventID, wantStep)
		}
		if got := stepFact.Payload["attempt"]; got != 3 {
			t.Fatalf("finishStep payload[attempt] = %v (%T), want 3 after redaction", got, got)
		}
		wantComp := idgen.Anchor("operation.compensation.finished", op.TenantID, op.ID.String(), compensating.ID.String(), "succeeded", "2")
		if compFact.EventID != wantComp {
			t.Fatalf("finishCompensationStep EventID = %s, want %s", compFact.EventID, wantComp)
		}
		if got := compFact.Payload["compensation_attempt"]; got != 2 {
			t.Fatalf("finishCompensationStep payload[compensation_attempt] = %v (%T), want 2 after redaction", got, got)
		}
		if _, present := stepFact.Payload["compensation_attempt"]; present {
			t.Fatal("forward step fact must not carry the compensation counter")
		}
	})
}
