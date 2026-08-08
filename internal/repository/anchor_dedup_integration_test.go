//go:build integration

package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// This file is the Postgres-backed half of the deterministic event-ID
// acceptance mapping (task-1-design.md §9): T-5.2 and M-1…M-6. The service
// unit half lives in internal/service/{audit_ledger,audit_hold,
// legacy_cutover,sync_anchor,operation_audit}_test.go.

func startAnchorLedger(t *testing.T) (*gorm.DB, repository.AuditLedgerRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-anchor-key-0123456789abcdef"})
}

// TestAnchoredEventIDAppendsOnce (T-5.2): two Appends with the same anchored
// EventID yield exactly one row in audit_ledger AND one in audit_publications;
// the second append returns ErrAuditDuplicate (pre-check path).
func TestAnchoredEventIDAppendsOnce(t *testing.T) {
	ctx := context.Background()
	db, ledger := startAnchorLedger(t)
	eventID := idgen.Anchor("operation.step.finished", "tenant-a", "0195b2c6-9f40-7c01-8000-000000000001",
		"0195b2c6-9f40-7c02-8000-000000000001", "succeeded")
	fact := func() *domain.AuditFact {
		return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "tenant-a:acc-1",
			TenantID: "tenant-a", EventID: eventID, Action: "operation.step.finished", ResourceType: "operation",
			ResourceID: "0195b2c6-9f40-7c01-8000-000000000001", OccurredAt: time.Now().UTC(), Payload: map[string]any{"status": "succeeded"}}
	}
	if err := ledger.Append(ctx, fact()); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if err := ledger.Append(ctx, fact()); !errors.Is(err, repository.ErrAuditDuplicate) {
		t.Fatalf("second Append err = %v, want ErrAuditDuplicate", err)
	}
	if n := countAuditLedgerRows(t, db, eventID); n != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1", n)
	}
	if n := countAuditPublicationRows(t, db, eventID); n != 1 {
		t.Fatalf("publication rows = %d, want exactly 1 (one publication per fact)", n)
	}
}

// TestOperationStartedReclaimDedupInTx (M-1): a ListRunnable re-claim re-applies
// operation.started with the same anchored event_id; the in-tx duplicate is
// swallowed, the idempotent status UPDATE commits, and the worker batch
// continues with exactly one fact.
func TestOperationStartedReclaimDedupInTx(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-anchor-key-0123456789abcdef"})
	accountRepo := repository.NewAccountRepository(db, ledger)
	account, err := accountRepo.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-m1", "u-m1", "local")
	if err != nil {
		t.Fatal(err)
	}
	op := &domain.Operation{
		AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-1", HomeRegion: "local", OperationType: "account.sync",
		Status: domain.OperationRequested, IdempotencyKey: "idem-1",
		Steps: []*domain.OperationStep{
			{StepKey: "step-1", Source: "aero-im", Action: "sync.profile", Status: domain.StepPending},
		},
	}
	opRepo := repository.NewOperationRepository(db, ledger).(repository.OperationAuditRepository)
	if err := opRepo.CreateWithAudit(ctx, op, auditWiringFact(idgen.NewID(), "tenant-a", "operation.created")); err != nil {
		t.Fatalf("CreateWithAudit: %v", err)
	}
	opStateRepo := repository.NewOperationRepository(db, ledger).(repository.OperationStateAuditRepository)
	started := auditWiringFact(idgen.Anchor("operation.started", "tenant-a", op.ID.String(), "", "running"),
		"tenant-a", "operation.started")

	// First start, then the crash-retry re-claim re-applying the same anchor.
	for i := 0; i < 2; i++ {
		if err := opStateRepo.SetStatusWithAudit(ctx, op.ID, domain.OperationRunning, "", "", nil, started); err != nil {
			t.Fatalf("SetStatusWithAudit %d: %v", i, err)
		}
	}
	var row struct{ Status string }
	if err := db.Raw(`SELECT status FROM operations WHERE id = ?`, op.ID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != string(domain.OperationRunning) {
		t.Fatalf("operation status = %s, want running (committed with swallowed duplicate)", row.Status)
	}
	if n := countAuditLedgerRows(t, db, started.EventID); n != 1 {
		t.Fatalf("ledger rows for operation.started = %d, want exactly 1", n)
	}
}

// TestStepReconcileStatusChangingAndConfirming (M-2): an unknown->unknown
// re-claim commits one fact with the pacing lease and no error (FM-6); a
// status-changing unknown->succeeded re-claim mints a distinct anchor (G2 vs
// G2b) and records the second outcome.
func TestStepReconcileStatusChangingAndConfirming(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-anchor-key-0123456789abcdef"})
	accountRepo := repository.NewAccountRepository(db, ledger)
	account, err := accountRepo.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-m2", "u-m2", "local")
	if err != nil {
		t.Fatal(err)
	}
	op := &domain.Operation{
		AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-2", HomeRegion: "local", OperationType: "account.sync",
		Status: domain.OperationRequested, IdempotencyKey: "idem-2",
		Steps: []*domain.OperationStep{
			{StepKey: "step-1", Source: "aero-im", Action: "sync.profile", Status: domain.StepPending},
		},
	}
	opRepo := repository.NewOperationRepository(db, ledger).(repository.OperationAuditRepository)
	if err := opRepo.CreateWithAudit(ctx, op, auditWiringFact(idgen.NewID(), "tenant-a", "operation.created")); err != nil {
		t.Fatalf("CreateWithAudit: %v", err)
	}
	opStateRepo := repository.NewOperationRepository(db, ledger).(repository.OperationStateAuditRepository)
	step, err := repository.NewOperationRepository(db, ledger).ClaimNextStep(ctx, op.ID, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextStep: %v", err)
	}
	unknownAnchor := idgen.Anchor("operation.step.finished", "tenant-a", op.ID.String(), step.ID.String(), "unknown")
	// Forward attempt cannot determine the outcome -> unknown, lease cleared.
	if err := opStateRepo.FinishStepWithAudit(ctx, step.ID, "worker-1", domain.StepUnknown,
		"src-1", "operation.unknown_outcome", nil, "", auditWiringFact(unknownAnchor, "tenant-a", "operation.step.finished")); err != nil {
		t.Fatalf("finish unknown (forward): %v", err)
	}
	// Reconcile re-claim; status-CONFIRMING unknown->unknown re-applies the
	// same anchor: swallowed in-tx, pacing lease set, no error, one fact.
	step, err = repository.NewOperationRepository(db, ledger).ClaimUnknownStep(ctx, op.ID, "reconciler-1", 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimUnknownStep 1: %v", err)
	}
	if err := opStateRepo.FinishStepWithAudit(ctx, step.ID, "reconciler-1", domain.StepUnknown,
		"src-1", "operation.unknown_outcome", nil, "", auditWiringFact(unknownAnchor, "tenant-a", "operation.step.finished")); err != nil {
		t.Fatalf("status-confirming reconcile must swallow the duplicate, got %v", err)
	}
	if n := countAuditLedgerRows(t, db, unknownAnchor); n != 1 {
		t.Fatalf("ledger rows for unknown anchor = %d, want exactly 1", n)
	}
	var stepRow struct {
		Status    string
		LeaseKind string
		Lease     *time.Time `gorm:"column:lease_until"`
	}
	if err := db.Raw(`SELECT status, lease_kind, lease_until FROM operation_steps WHERE id = ?`, step.ID).Scan(&stepRow).Error; err != nil {
		t.Fatal(err)
	}
	if stepRow.Status != string(domain.StepUnknown) || stepRow.LeaseKind != "" || stepRow.Lease == nil || !stepRow.Lease.After(time.Now().UTC()) {
		t.Fatalf("step after confirming reconcile = %+v, want unknown with future pacing lease", stepRow)
	}
	// Lease expiry (simulated): status-CHANGING unknown->succeeded mints G2b.
	if err := db.Exec(`UPDATE operation_steps SET lease_until = NULL WHERE id = ?`, step.ID).Error; err != nil {
		t.Fatal(err)
	}
	step, err = repository.NewOperationRepository(db, ledger).ClaimUnknownStep(ctx, op.ID, "reconciler-1", 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimUnknownStep 2: %v", err)
	}
	succeededAnchor := idgen.Anchor("operation.step.finished", "tenant-a", op.ID.String(), step.ID.String(), "succeeded")
	if succeededAnchor == unknownAnchor {
		t.Fatal("status-changing anchor must differ from the confirming anchor (G2b != G2)")
	}
	if err := opStateRepo.FinishStepWithAudit(ctx, step.ID, "reconciler-1", domain.StepSucceeded,
		"src-1", "", map[string]any{"ok": true}, "", auditWiringFact(succeededAnchor, "tenant-a", "operation.step.finished")); err != nil {
		t.Fatalf("status-changing reconcile: %v", err)
	}
	if n := countAuditLedgerRows(t, db, succeededAnchor); n != 1 {
		t.Fatalf("ledger rows for succeeded anchor = %d, want 1", n)
	}
	var total int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE event_id IN (?, ?)`, unknownAnchor, succeededAnchor).Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("ledger rows across both outcomes = %d, want 2 (both recorded)", total)
	}
}

// TestCompensationAnchors (M-3): operation.compensating,
// operation.compensation.finished, and operation.compensation.reconciled
// derive per §2.3 and persist as distinct facts.
func TestCompensationAnchors(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-anchor-key-0123456789abcdef"})
	accountRepo := repository.NewAccountRepository(db, ledger)
	account, err := accountRepo.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-m3", "u-m3", "local")
	if err != nil {
		t.Fatal(err)
	}
	op := &domain.Operation{
		AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-3", HomeRegion: "local", OperationType: "account.sync",
		Status: domain.OperationRequested, IdempotencyKey: "idem-3",
		Steps: []*domain.OperationStep{
			{StepKey: "step-1", Source: "aero-im", Action: "sync.profile", Status: domain.StepSucceeded,
				CompensationAction: "undo.profile", CompensationStatus: domain.StepPending},
		},
	}
	opRepo := repository.NewOperationRepository(db, ledger).(repository.OperationAuditRepository)
	if err := opRepo.CreateWithAudit(ctx, op, auditWiringFact(idgen.NewID(), "tenant-a", "operation.created")); err != nil {
		t.Fatalf("CreateWithAudit: %v", err)
	}
	opStateRepo := repository.NewOperationRepository(db, ledger).(repository.OperationStateAuditRepository)

	compensating := idgen.Anchor("operation.compensating", "tenant-a", op.ID.String(), "", "compensating")
	if err := opStateRepo.SetStatusWithAudit(ctx, op.ID, domain.OperationCompensating, "", "", nil,
		auditWiringFact(compensating, "tenant-a", "operation.compensating")); err != nil {
		t.Fatalf("operation.compensating: %v", err)
	}

	step, err := repository.NewOperationRepository(db, ledger).ClaimCompensationStep(ctx, op.ID, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimCompensationStep: %v", err)
	}
	finishedUnknown := idgen.Anchor("operation.compensation.finished", "tenant-a", op.ID.String(), step.ID.String(), "unknown")
	if err := opStateRepo.FinishCompensationStepWithAudit(ctx, step.ID, "worker-1", domain.StepUnknown,
		"src-1", "operation.compensation_unknown_outcome", nil, "",
		auditWiringFact(finishedUnknown, "tenant-a", "operation.compensation.finished")); err != nil {
		t.Fatalf("operation.compensation.finished (unknown): %v", err)
	}
	reconciled := idgen.Anchor("operation.compensation.reconciled", "tenant-a", op.ID.String(), step.ID.String(), "succeeded")
	if err := db.Exec(`UPDATE operation_steps SET compensation_lease_until = NULL WHERE id = ?`, step.ID).Error; err != nil {
		t.Fatal(err)
	}
	step, err = repository.NewOperationRepository(db, ledger).ClaimUnknownCompensationStep(ctx, op.ID, "reconciler-1", 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimUnknownCompensationStep: %v", err)
	}
	if err := opStateRepo.FinishCompensationStepWithAudit(ctx, step.ID, "reconciler-1", domain.StepSucceeded,
		"src-1", "", nil, "", auditWiringFact(reconciled, "tenant-a", "operation.compensation.reconciled")); err != nil {
		t.Fatalf("operation.compensation.reconciled: %v", err)
	}
	for _, e := range []domain.ID{compensating, finishedUnknown, reconciled} {
		if n := countAuditLedgerRows(t, db, e); n != 1 {
			t.Fatalf("ledger rows for %s = %d, want 1", e, n)
		}
	}
	if compensating == finishedUnknown || finishedUnknown == reconciled || compensating == reconciled {
		t.Fatal("compensation anchors must be pairwise distinct")
	}
}

// TestOperationLevelAnchors (M-4): operation.finished carries the terminal
// status in the anchor — two passes with the same status collapse to one
// fact, different statuses record both (D-6).
func TestOperationLevelAnchors(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-anchor-key-0123456789abcdef"})
	accountRepo := repository.NewAccountRepository(db, ledger)
	account, err := accountRepo.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-m4", "u-m4", "local")
	if err != nil {
		t.Fatal(err)
	}
	op := &domain.Operation{
		AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-4", HomeRegion: "local", OperationType: "account.sync",
		Status: domain.OperationRequested, IdempotencyKey: "idem-4",
		Steps: []*domain.OperationStep{
			{StepKey: "step-1", Source: "aero-im", Action: "sync.profile", Status: domain.StepPending},
		},
	}
	opRepo := repository.NewOperationRepository(db, ledger).(repository.OperationAuditRepository)
	if err := opRepo.CreateWithAudit(ctx, op, auditWiringFact(idgen.NewID(), "tenant-a", "operation.created")); err != nil {
		t.Fatalf("CreateWithAudit: %v", err)
	}
	opStateRepo := repository.NewOperationRepository(db, ledger).(repository.OperationStateAuditRepository)
	finished := idgen.Anchor("operation.finished", "tenant-a", op.ID.String(), "", "completed")
	for i := 0; i < 2; i++ {
		if err := opStateRepo.SetStatusWithAudit(ctx, op.ID, domain.OperationCompleted, "", "", nil,
			auditWiringFact(finished, "tenant-a", "operation.finished")); err != nil {
			t.Fatalf("operation.finished pass %d: %v", i, err)
		}
	}
	if n := countAuditLedgerRows(t, db, finished); n != 1 {
		t.Fatalf("ledger rows after same-status passes = %d, want 1", n)
	}
	failed := idgen.Anchor("operation.finished", "tenant-a", op.ID.String(), "", "failed")
	if failed == finished {
		t.Fatal("status must be part of the step-less anchor (G7 sensitivity)")
	}
	if err := opStateRepo.SetStatusWithAudit(ctx, op.ID, domain.OperationFailed, "x", "", nil,
		auditWiringFact(failed, "tenant-a", "operation.finished")); err != nil {
		t.Fatalf("operation.finished (failed): %v", err)
	}
	if n := countAuditLedgerRows(t, db, failed); n != 1 {
		t.Fatalf("ledger rows for failed status = %d, want 1 (second outcome recorded)", n)
	}
}

// TestSyncJobFinishedParity (M-5, integration): the in-tx FinishWithAudit path
// mints the §2.3 sync.job.finished anchor, and a re-driven finish with the
// SAME status/event_id is swallowed in-tx — one ledger row and one
// publication (the fallback Record path shares the derivation via the single
// syncJobFinishedAudit minting point, covered in the service unit test).
func TestSyncJobFinishedParity(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-anchor-key-0123456789abcdef"})
	accountRepo := repository.NewAccountRepository(db, ledger)
	account, err := accountRepo.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-m5", "u-m5", "local")
	if err != nil {
		t.Fatal(err)
	}
	syncRepo := repository.NewSyncJobRepository(db, ledger)
	job := &domain.SyncJob{AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-5", JobType: domain.SyncJobSync, Status: domain.SyncJobPending,
		IdempotencyKey: "idem-5", Request: map[string]any{"datasets": []any{}}, Result: map[string]any{}}
	if err := syncRepo.Create(ctx, job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	claimed, err := syncRepo.Claim(ctx, job.ID, "sync-worker", 5*time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	finished := idgen.Anchor("sync.job.finished", "tenant-a", job.ID.String(), "completed")
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "tenant-a:" + job.AccountID.String(),
		TenantID: "tenant-a", EventID: finished, Action: "sync.job.finished", ResourceType: "sync_job",
		ResourceID: job.ID.String(), OccurredAt: time.Now().UTC(), Payload: map[string]any{"status": "completed"}}
	auditRepo := syncRepo.(repository.SyncJobAuditRepository)
	if err := auditRepo.FinishWithAudit(ctx, job.ID, claimed.LeaseOwner, domain.SyncJobCompleted, "", "", map[string]any{}, fact); err != nil {
		t.Fatalf("FinishWithAudit: %v", err)
	}
	// Re-driven finish with the same status re-derives the same anchor and the
	// in-tx duplicate is swallowed (lease re-armed to model the retry).
	if err := db.Exec(`UPDATE sync_jobs SET status = ?, lease_owner = ?, lease_until = ?, lease_kind = ? WHERE id = ?`,
		"running", "sync-worker", time.Now().UTC().Add(5*time.Minute), "execute", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := auditRepo.FinishWithAudit(ctx, job.ID, "sync-worker", domain.SyncJobCompleted, "", "", map[string]any{}, fact); err != nil {
		t.Fatalf("re-driven FinishWithAudit must swallow the duplicate, got %v", err)
	}
	if n := countAuditLedgerRows(t, db, finished); n != 1 {
		t.Fatalf("ledger rows for sync.job.finished = %d, want exactly 1", n)
	}
	if n := countAuditPublicationRows(t, db, finished); n != 1 {
		t.Fatalf("publication rows = %d, want exactly 1", n)
	}
}

// TestPutHoldCopyBack (M-6 / D-9): PutHold returns the FINAL stored
// id/created_at/expires_at via RETURNING on both the insert and the upsert
// path; the response is truthful (the handler's created_at is never zero).
func TestPutHoldCopyBack(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	holds := repository.NewAuditHoldRepository(db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-anchor-key-0123456789abcdef"}))
	expiry := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Microsecond)

	first := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
		ScopeID: "acct-1", Reason: "evidence order #1", CreatedBy: "admin-1", ExpiresAt: &expiry}
	if err := holds.PutHold(t.Context(), first); err != nil {
		t.Fatalf("first PutHold: %v", err)
	}
	if first.ID == (domain.ID{}) || first.CreatedAt.IsZero() {
		t.Fatalf("first put must return stored id and created_at, got %+v", first)
	}
	if first.ExpiresAt == nil || !first.ExpiresAt.Equal(expiry) {
		t.Fatalf("first put expires_at = %v, want %v", first.ExpiresAt, expiry)
	}

	time.Sleep(5 * time.Millisecond)
	second := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
		ScopeID: "acct-1", Reason: "renewed", CreatedBy: "admin-2", ExpiresAt: nil}
	if err := holds.PutHold(t.Context(), second); err != nil {
		t.Fatalf("second PutHold: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-put ID = %s, want stable %s (id never in DoUpdates)", second.ID, first.ID)
	}
	if !second.CreatedAt.After(first.CreatedAt) {
		t.Fatalf("re-put created_at = %v, want newer than %v (refresh semantics)", second.CreatedAt, first.CreatedAt)
	}
	if second.ExpiresAt != nil {
		t.Fatalf("re-put expires_at = %v, want nil (returning reflects the final row)", second.ExpiresAt)
	}
	var stored struct {
		ID        domain.ID
		CreatedAt time.Time
		Reason    string
	}
	if err := db.Raw(`SELECT id, created_at, reason FROM audit_holds WHERE tenant_id = 'tenant-a' AND scope_id = 'acct-1'`).
		Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ID != second.ID || !stored.CreatedAt.Equal(second.CreatedAt) || stored.Reason != "renewed" {
		t.Fatalf("stored row %+v does not match the returned struct %+v", stored, second)
	}
}

// TestAnchoredRegistryAppendsOnce (T-10.1-A-2): table-driven generalization of
// the anchored-append-once pattern over the converted-mutation registry
// (configs-anchor-scheme-declaration §5): every deterministic-EventID mutation
// class must dedup to exactly one ledger row + one publication row with an
// unchanged chain_sequence on a retried append. Rows 1-14 of the registry are
// represented by their EventID derivation shape (pre-assigned v7 receipt or
// idgen.Anchor v5 with the site's part contract).
func TestAnchoredRegistryAppendsOnce(t *testing.T) {
	ctx := context.Background()
	db, ledger := startAnchorLedger(t)
	tenant := "tenant-a"
	accountID := idgen.NewID()
	operationID := idgen.NewID()
	jobID := idgen.NewID()
	reportID := idgen.NewID()
	subscriptionID := idgen.NewID()
	envelopeID := idgen.NewID()
	replayID := idgen.NewID()
	now := time.Now().UTC()

	cases := []struct {
		name   string
		action string
		event  domain.ID
		fact   func() *domain.AuditFact
	}{
		{"1 account.provisioned (pre-assigned accountID)",
			"account.provisioned", accountID,
			func() *domain.AuditFact {
				return anchorFact(tenant, accountID, accountID, "account", "account.provisioned", now)
			}},
		{"3 comment.report.resolve (pre-assigned reportID)",
			"comment.report.resolve", reportID,
			func() *domain.AuditFact {
				return anchorFact(tenant, reportID, reportID, "comment_report", "comment.report.resolve", now)
			}},
		{"4 operation.requested (pre-assigned operation.ID)",
			"operation.requested", operationID,
			func() *domain.AuditFact {
				return anchorFact(tenant, operationID, operationID, "operation", "operation.requested", now)
			}},
		{"6 sync.job.requested (pre-assigned job.ID)",
			"sync.job.requested", jobID,
			func() *domain.AuditFact {
				return anchorFact(tenant, jobID, jobID, "sync_job", "sync.job.requested", now)
			}},
		{"11 callback.registered (pre-assigned subscription.ID)",
			"callback.registered", subscriptionID,
			func() *domain.AuditFact {
				return anchorFact(tenant, subscriptionID, subscriptionID, "callback_subscription", "callback.registered", now)
			}},
		{"13 event ingest receipt (source envelope.EventID)",
			"source.event", envelopeID,
			func() *domain.AuditFact {
				return anchorFact(tenant, envelopeID, envelopeID, "object", "source.event", now)
			}},
		{"14 audit.replay.requested (pre-assigned operation.ID)",
			"audit.replay.requested", replayID,
			func() *domain.AuditFact {
				return anchorFact(tenant, replayID, replayID, "replay", "audit.replay.requested", now)
			}},
		{"5 operation.step.finished (Anchor 5-tuple)",
			"operation.step.finished", idgen.Anchor("operation.step.finished", tenant, operationID.String(), "step-1", "succeeded"),
			func() *domain.AuditFact {
				e := idgen.Anchor("operation.step.finished", tenant, operationID.String(), "step-1", "succeeded")
				return anchorFact(tenant, e, operationID, "operation", "operation.step.finished", now)
			}},
		{"7 sync.job.finished (Anchor 4-tuple)",
			"sync.job.finished", idgen.Anchor("sync.job.finished", tenant, jobID.String(), "completed"),
			func() *domain.AuditFact {
				e := idgen.Anchor("sync.job.finished", tenant, jobID.String(), "completed")
				return anchorFact(tenant, e, jobID, "sync_job", "sync.job.finished", now)
			}},
		{"8 audit_hold.set (Anchor with RenderAnchorTime)",
			"audit_hold.set", idgen.Anchor("audit_hold.set", tenant, "actor", "scope", "reason", "2026-01-01T00:00:00Z"),
			func() *domain.AuditFact {
				e := idgen.Anchor("audit_hold.set", tenant, "actor", "scope", "reason", "2026-01-01T00:00:00Z")
				return anchorFact(tenant, e, idgen.NewID(), "audit_hold", "audit_hold.set", now)
			}},
		{"10 legacy.cutover.set (Anchor 4-tuple)",
			"legacy.cutover.set", idgen.Anchor("legacy.cutover.set", tenant, "pending", ""),
			func() *domain.AuditFact {
				e := idgen.Anchor("legacy.cutover.set", tenant, "pending", "")
				return anchorFact(tenant, e, idgen.NewID(), "account", "legacy.cutover.set", now)
			}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ledger.Append(ctx, tc.fact()); err != nil {
				t.Fatalf("first Append: %v", err)
			}
			var seqBefore int64
			if err := db.Raw(`SELECT chain_sequence FROM audit_ledger WHERE event_id = ?`, tc.event).Scan(&seqBefore).Error; err != nil {
				t.Fatal(err)
			}
			if err := ledger.Append(ctx, tc.fact()); !errors.Is(err, repository.ErrAuditDuplicate) {
				t.Fatalf("second Append err = %v, want ErrAuditDuplicate", err)
			}
			if n := countAuditLedgerRows(t, db, tc.event); n != 1 {
				t.Fatalf("ledger rows = %d, want exactly 1", n)
			}
			if n := countAuditPublicationRows(t, db, tc.event); n != 1 {
				t.Fatalf("publication rows = %d, want exactly 1", n)
			}
			var seqAfter int64
			if err := db.Raw(`SELECT chain_sequence FROM audit_ledger WHERE event_id = ?`, tc.event).Scan(&seqAfter).Error; err != nil {
				t.Fatal(err)
			}
			if seqAfter != seqBefore {
				t.Fatalf("chain_sequence changed on duplicate append: %d → %d", seqBefore, seqAfter)
			}
		})
	}
}

// anchorFact builds an append fact with the given anchored event ID.
func anchorFact(tenant string, eventID, resourceID domain.ID, resourceType, action string, at time.Time) *domain.AuditFact {
	return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: tenant + ":anchor",
		TenantID: tenant, EventID: eventID, Action: action, ResourceType: resourceType,
		ResourceID: resourceID.String(), OccurredAt: at, Payload: map[string]any{}}
}
