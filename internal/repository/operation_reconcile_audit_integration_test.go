//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// This file proves the attempt-scoped half of the operation audit anchor
// against real Postgres: the ledger guard (tenant_id, source, event_id) must
// hold one row per DURABLE ATTEMPT, not one row per logical fact, so repeated
// unknown-outcome reconciliation passes stop folding into a single row. The
// anchors below are exactly what internal/service.operationAuditFact derives
// ([action, tenant, operation, step, status, decimal attempt]); the service
// derivation itself is unit-covered in internal/service/operation_audit_test.go.

func startAttemptAuditLedger(t *testing.T) (*gorm.DB, repository.FullAuditLedgerRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-anchor-key-0123456789abcdef"})
}

// attemptAuditFact builds the fact a service finishStep/finishCompensationStep
// hands to the repository for one durable attempt. attemptKey mirrors the
// operation_steps column name so the two counters stay distinguishable.
func attemptAuditFact(eventID domain.ID, tenantID, action, status, attemptKey string, attempt int) *domain.AuditFact {
	return &domain.AuditFact{EventID: eventID, Source: "aero-id", SourceRegion: "local",
		PartitionKey: tenantID + ":p-1", TenantID: tenantID, ActorUID: "actor-1", Action: action,
		ResourceType: "operation", ResourceID: "operation-1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"status": status, attemptKey: attempt}}
}

func countLedgerRowsForAction(t *testing.T, db *gorm.DB, action string) int64 {
	t.Helper()
	var total, distinct int64
	if err := db.Raw(`SELECT count(*), count(DISTINCT event_id) FROM audit_ledger WHERE action = ?`, action).
		Row().Scan(&total, &distinct); err != nil {
		t.Fatalf("count ledger rows for %s: %v", action, err)
	}
	if total != distinct {
		t.Fatalf("ledger rows for %s = %d but distinct event_id = %d (fact folded)", action, total, distinct)
	}
	return total
}

func ledgerAttemptPayloads(t *testing.T, db *gorm.DB, action, attemptKey string) map[string]int {
	t.Helper()
	type row struct {
		EventID string
		Attempt *int
	}
	var rows []row
	if err := db.Raw(`SELECT event_id::text AS event_id, payload->>'`+attemptKey+`' AS attempt FROM audit_ledger WHERE action = ?`, action).
		Scan(&rows).Error; err != nil {
		t.Fatalf("read %s payloads: %v", action, err)
	}
	got := map[string]int{}
	for _, r := range rows {
		if r.Attempt == nil {
			t.Fatalf("fact %s has no %s in its redacted payload", r.EventID, attemptKey)
		}
		got[r.EventID] = *r.Attempt
	}
	return got
}

func countAttemptRows(t *testing.T, db *gorm.DB, stepID domain.ID, requireFinished bool) int64 {
	t.Helper()
	query := `SELECT count(*) FROM operation_attempts WHERE step_id = ?`
	if requireFinished {
		query += ` AND status = 'unknown' AND finished_at IS NOT NULL`
	}
	var n int64
	if err := db.Raw(query, stepID).Scan(&n).Error; err != nil {
		t.Fatalf("count attempt rows: %v", err)
	}
	return n
}

// TestOperationUnknownReconcileAuditFacts: a step whose remote outcome stays
// unknown is re-queried forever, one durable attempt per pass (attempt+1 plus a
// new operation_attempts row, paced by ReconcileUnknownRetryDelay). Each pass
// must append its OWN audit fact. Before the attempt slot existed in the
// anchor, every pass re-derived the same event_id, the ledger unique guard
// fired, appendAudit swallowed the duplicate, and N attempts left exactly ONE
// durable audit fact. Re-finishing at the SAME attempt must still be swallowed
// (retry/crash-after-commit), so a pass is recorded once and only once.
func TestOperationUnknownReconcileAuditFacts(t *testing.T) {
	ctx := context.Background()
	db, ledger := startAttemptAuditLedger(t)
	account, err := repository.NewAccountRepository(db, ledger).EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-att", "u-att", "local")
	if err != nil {
		t.Fatal(err)
	}
	op := &domain.Operation{
		AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-att", HomeRegion: "local", OperationType: "account.sync",
		Status: domain.OperationRequested, IdempotencyKey: "idem-att",
		Steps: []*domain.OperationStep{
			{StepKey: "step-1", Source: "aero-im", Action: "sync.profile", Status: domain.StepPending},
		},
	}
	if err := repository.NewOperationRepository(db, ledger).(repository.OperationAuditRepository).
		CreateWithAudit(ctx, op, auditWiringFact(idgen.NewID(), "tenant-a", "operation.created")); err != nil {
		t.Fatalf("CreateWithAudit: %v", err)
	}
	opRepo := repository.NewOperationRepository(db, ledger)
	opStateRepo := opRepo.(repository.OperationStateAuditRepository)

	// Pass 1: forward claim -> attempt 1, outcome not confirmable -> unknown.
	step, err := opRepo.ClaimNextStep(ctx, op.ID, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextStep: %v", err)
	}
	if step.Attempt != 1 {
		t.Fatalf("forward claim attempt = %d, want 1", step.Attempt)
	}
	anchor1 := idgen.Anchor("operation.step.finished", "tenant-a", op.ID.String(), step.ID.String(), "unknown", "1")
	if err := opStateRepo.FinishStepWithAudit(ctx, step.ID, "worker-1", domain.StepUnknown, "src-1",
		"operation.unknown_outcome", nil, "",
		attemptAuditFact(anchor1, "tenant-a", "operation.step.finished", "unknown", "attempt", step.Attempt)); err != nil {
		t.Fatalf("finish unknown (forward): %v", err)
	}

	// Pass 2: reconcile claim -> attempt 2, still unconfirmable -> unknown.
	step, err = opRepo.ClaimUnknownStep(ctx, op.ID, "reconciler-1", 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimUnknownStep: %v", err)
	}
	if step.Attempt != 2 {
		t.Fatalf("reconcile claim attempt = %d, want 2 (claim must increment)", step.Attempt)
	}
	anchor2 := idgen.Anchor("operation.step.finished", "tenant-a", op.ID.String(), step.ID.String(), "unknown", "2")
	if anchor2 == anchor1 {
		t.Fatal("successive reconcile attempts must derive distinct anchors")
	}
	if err := opStateRepo.FinishStepWithAudit(ctx, step.ID, "reconciler-1", domain.StepUnknown, "src-1",
		"operation.unknown_outcome", nil, "",
		attemptAuditFact(anchor2, "tenant-a", "operation.step.finished", "unknown", "attempt", step.Attempt)); err != nil {
		t.Fatalf("finish unknown (reconcile): %v", err)
	}

	// Two durable attempts, two durable facts.
	if n := countLedgerRowsForAction(t, db, "operation.step.finished"); n != 2 {
		t.Fatalf("operation.step.finished ledger rows = %d, want 2 (one per attempt)", n)
	}
	if n := countAuditLedgerRows(t, db, anchor1); n != 1 {
		t.Fatalf("ledger rows for attempt-1 anchor = %d, want 1", n)
	}
	if n := countAuditLedgerRows(t, db, anchor2); n != 1 {
		t.Fatalf("ledger rows for attempt-2 anchor = %d, want 1", n)
	}
	payloads := ledgerAttemptPayloads(t, db, "operation.step.finished", "attempt")
	if payloads[anchor1.String()] != 1 || payloads[anchor2.String()] != 2 || len(payloads) != 2 {
		t.Fatalf("payload attempt values = %v, want attempt 1 and 2", payloads)
	}
	if n := countAttemptRows(t, db, step.ID, true); n != 2 {
		t.Fatalf("finished operation_attempts rows = %d, want 2", n)
	}
	if n := countAttemptRows(t, db, step.ID, false); n != 2 {
		t.Fatalf("operation_attempts rows = %d, want 2 (one per claim)", n)
	}

	// Same-attempt re-finish (crash-after-commit retry): the anchor re-derives
	// identically, so the duplicate is swallowed in-tx and the domain write
	// still commits. No third fact and no third attempt row.
	if err := db.Exec(`UPDATE operation_steps SET status = 'running', lease_owner = 'reconciler-1',
		lease_until = clock_timestamp() + interval '2 minutes', lease_kind = 'reconcile' WHERE id = ?`, step.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := opStateRepo.FinishStepWithAudit(ctx, step.ID, "reconciler-1", domain.StepUnknown, "src-1",
		"operation.unknown_outcome", nil, "",
		attemptAuditFact(anchor2, "tenant-a", "operation.step.finished", "unknown", "attempt", 2)); err != nil {
		t.Fatalf("same-attempt re-finish must swallow the duplicate, got %v", err)
	}
	if n := countAuditLedgerRows(t, db, anchor2); n != 1 {
		t.Fatalf("ledger rows for attempt-2 anchor after re-finish = %d, want 1 (retry deduped)", n)
	}
	if n := countLedgerRowsForAction(t, db, "operation.step.finished"); n != 2 {
		t.Fatalf("operation.step.finished ledger rows after re-finish = %d, want 2", n)
	}
	if n := countAttemptRows(t, db, step.ID, false); n != 2 {
		t.Fatalf("operation_attempts rows after re-finish = %d, want 2 (no claim happened)", n)
	}

	// Pass 3 at the production cadence: the pacing lease expires, the next tick
	// re-claims, and the third attempt records a third fact.
	if err := db.Exec(`UPDATE operation_steps SET lease_until = NULL WHERE id = ?`, step.ID).Error; err != nil {
		t.Fatal(err)
	}
	step, err = opRepo.ClaimUnknownStep(ctx, op.ID, "reconciler-1", 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimUnknownStep (third pass): %v", err)
	}
	if step.Attempt != 3 {
		t.Fatalf("third reconcile claim attempt = %d, want 3", step.Attempt)
	}
	anchor3 := idgen.Anchor("operation.step.finished", "tenant-a", op.ID.String(), step.ID.String(), "unknown", "3")
	if err := opStateRepo.FinishStepWithAudit(ctx, step.ID, "reconciler-1", domain.StepUnknown, "src-1",
		"operation.unknown_outcome", nil, "",
		attemptAuditFact(anchor3, "tenant-a", "operation.step.finished", "unknown", "attempt", step.Attempt)); err != nil {
		t.Fatalf("finish unknown (third pass): %v", err)
	}
	if n := countLedgerRowsForAction(t, db, "operation.step.finished"); n != 3 {
		t.Fatalf("operation.step.finished ledger rows = %d, want 3 (one per attempt)", n)
	}
	if n := countAttemptRows(t, db, step.ID, true); n != 3 {
		t.Fatalf("finished operation_attempts rows = %d, want 3", n)
	}
	payloads = ledgerAttemptPayloads(t, db, "operation.step.finished", "attempt")
	if payloads[anchor3.String()] != 3 || len(payloads) != 3 {
		t.Fatalf("payload attempt values = %v, want attempts 1, 2 and 3", payloads)
	}
}

// TestOperationUnknownReconcileCompensationAuditFacts: the same folding defect
// on the compensation axis. Each unknown compensation reconcile claims a new
// compensation_attempt (and a new operation_attempts row), so each pass records
// its own operation.compensation.finished fact keyed by
// compensation_attempt — the forward counter is never mixed in.
func TestOperationUnknownReconcileCompensationAuditFacts(t *testing.T) {
	ctx := context.Background()
	db, ledger := startAttemptAuditLedger(t)
	account, err := repository.NewAccountRepository(db, ledger).EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-comp", "u-comp", "local")
	if err != nil {
		t.Fatal(err)
	}
	op := &domain.Operation{
		AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-comp", HomeRegion: "local", OperationType: "account.sync",
		Status: domain.OperationRequested, IdempotencyKey: "idem-comp",
		Steps: []*domain.OperationStep{
			{StepKey: "step-1", Source: "aero-im", Action: "sync.profile", Status: domain.StepSucceeded,
				CompensationAction: "undo.profile", CompensationStatus: domain.StepPending},
		},
	}
	if err := repository.NewOperationRepository(db, ledger).(repository.OperationAuditRepository).
		CreateWithAudit(ctx, op, auditWiringFact(idgen.NewID(), "tenant-a", "operation.created")); err != nil {
		t.Fatalf("CreateWithAudit: %v", err)
	}
	opRepo := repository.NewOperationRepository(db, ledger)
	opStateRepo := opRepo.(repository.OperationStateAuditRepository)

	step, err := opRepo.ClaimCompensationStep(ctx, op.ID, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimCompensationStep: %v", err)
	}
	if step.CompensationAttempt != 1 {
		t.Fatalf("compensation claim attempt = %d, want 1", step.CompensationAttempt)
	}
	anchor1 := idgen.Anchor("operation.compensation.finished", "tenant-a", op.ID.String(), step.ID.String(), "unknown", "1")
	if err := opStateRepo.FinishCompensationStepWithAudit(ctx, step.ID, "worker-1", domain.StepUnknown, "src-1",
		"operation.compensation_unknown_outcome", nil, "",
		attemptAuditFact(anchor1, "tenant-a", "operation.compensation.finished", "unknown", "compensation_attempt",
			step.CompensationAttempt)); err != nil {
		t.Fatalf("finish compensation unknown: %v", err)
	}

	if err := db.Exec(`UPDATE operation_steps SET compensation_lease_until = NULL WHERE id = ?`, step.ID).Error; err != nil {
		t.Fatal(err)
	}
	step, err = opRepo.ClaimUnknownCompensationStep(ctx, op.ID, "reconciler-1", 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimUnknownCompensationStep: %v", err)
	}
	if step.CompensationAttempt != 2 {
		t.Fatalf("compensation reconcile attempt = %d, want 2 (claim must increment)", step.CompensationAttempt)
	}
	anchor2 := idgen.Anchor("operation.compensation.finished", "tenant-a", op.ID.String(), step.ID.String(), "unknown", "2")
	if anchor2 == anchor1 {
		t.Fatal("successive compensation attempts must derive distinct anchors")
	}
	if err := opStateRepo.FinishCompensationStepWithAudit(ctx, step.ID, "reconciler-1", domain.StepUnknown, "src-1",
		"operation.compensation_unknown_outcome", nil, "",
		attemptAuditFact(anchor2, "tenant-a", "operation.compensation.finished", "unknown", "compensation_attempt",
			step.CompensationAttempt)); err != nil {
		t.Fatalf("finish compensation unknown (reconcile): %v", err)
	}

	if n := countLedgerRowsForAction(t, db, "operation.compensation.finished"); n != 2 {
		t.Fatalf("operation.compensation.finished ledger rows = %d, want 2 (one per attempt)", n)
	}
	payloads := ledgerAttemptPayloads(t, db, "operation.compensation.finished", "compensation_attempt")
	if payloads[anchor1.String()] != 1 || payloads[anchor2.String()] != 2 || len(payloads) != 2 {
		t.Fatalf("compensation_attempt payload values = %v, want 1 and 2", payloads)
	}
	if n := countAttemptRows(t, db, step.ID, true); n != 2 {
		t.Fatalf("finished operation_attempts rows = %d, want 2", n)
	}
}
