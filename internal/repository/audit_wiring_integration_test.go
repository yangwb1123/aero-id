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
	"github.com/aero/aero-id/internal/event"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// auditWiringFact builds a fact whose (tenant_id, source, event_id) triple can
// be replayed to force ErrAuditDuplicate for the rollback-atomicity cases.
func auditWiringFact(eventID domain.ID, tenantID, action string) *domain.AuditFact {
	return &domain.AuditFact{
		EventID:      eventID,
		Source:       "aero-im",
		SourceRegion: "us-west",
		PartitionKey: "p-1",
		TenantID:     tenantID,
		ActorUID:     "actor-1",
		Action:       action,
		ResourceType: "profile",
		ResourceID:   "r-1",
		OccurredAt:   time.Now().UTC(),
		Payload:      map[string]any{"k": "v"},
	}
}

func countAuditLedgerRows(t *testing.T, db *gorm.DB, eventID domain.ID) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID).Scan(&n).Error; err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

func countAuditPublicationRows(t *testing.T, db *gorm.DB, eventID domain.ID) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&n).Error; err != nil {
		t.Fatalf("count audit publication rows: %v", err)
	}
	return n
}

// TestAuditWiringHappyPath is AC3a: every *WithAudit mutation commits its
// durable audit fact in the SAME transaction as the domain rows — one ledger
// row per call, with the fact's event_id and action preserved.
func TestAuditWiringHappyPath(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-wiring-key-0123456789abcdef"})
	accountRepo := repository.NewAccountRepository(db, ledger)
	profileRepo := accountRepo.(repository.AccountProfileAuditRepository)
	account, err := accountRepo.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-wiring", "u-wiring", "local")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Profile mutation + audit fact, one transaction.
	e1 := idgen.NewID()
	profile := &domain.AccountProfile{AccountID: account.ID, DisplayName: "first", Locale: "en"}
	if err := profileRepo.UpdateProfileWithAudit(ctx, profile, auditWiringFact(e1, "tenant-a", "account.profile.updated")); err != nil {
		t.Fatalf("UpdateProfileWithAudit: %v", err)
	}
	if n := countAuditLedgerRows(t, db, e1); n != 1 {
		t.Fatalf("ledger rows for e1 = %d, want 1", n)
	}
	if n := countAuditPublicationRows(t, db, e1); n != 1 {
		t.Fatalf("publication rows for e1 = %d, want 1 (one publication per fact)", n)
	}

	// 2. Operation creation + audit fact.
	e2 := idgen.NewID()
	op := &domain.Operation{
		AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-1", HomeRegion: "local", OperationType: "account.sync",
		Status: domain.OperationRequested, IdempotencyKey: "idem-1",
		RequestPayload: map[string]any{"op": "sync"},
		Steps: []*domain.OperationStep{
			{StepKey: "step-1", Source: "aero-im", Action: "sync.profile", Status: domain.StepPending},
		},
	}
	opRepo := repository.NewOperationRepository(db, ledger).(repository.OperationAuditRepository)
	if err := opRepo.CreateWithAudit(ctx, op, auditWiringFact(e2, "tenant-a", "operation.created")); err != nil {
		t.Fatalf("CreateWithAudit: %v", err)
	}
	if n := countAuditLedgerRows(t, db, e2); n != 1 {
		t.Fatalf("ledger rows for e2 = %d, want 1", n)
	}
	var opRow struct{ Status string }
	if err := db.Raw(`SELECT status FROM operations WHERE id = ?`, op.ID).Scan(&opRow).Error; err != nil {
		t.Fatal(err)
	}
	if opRow.Status != string(domain.OperationRequested) {
		t.Fatalf("operation status = %s, want requested (committed with the fact)", opRow.Status)
	}

	// 3. Step finalization + audit fact: claim, finish, one ledger row.
	e3 := idgen.NewID()
	opStateRepo := repository.NewOperationRepository(db, ledger).(repository.OperationStateAuditRepository)
	step, err := repository.NewOperationRepository(db, ledger).ClaimNextStep(ctx, op.ID, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextStep: %v", err)
	}
	if err := opStateRepo.FinishStepWithAudit(ctx, step.ID, "worker-1", domain.StepSucceeded,
		"src-op-1", "", map[string]any{"ok": true}, "", auditWiringFact(e3, "tenant-a", "operation.step.succeeded")); err != nil {
		t.Fatalf("FinishStepWithAudit: %v", err)
	}
	if n := countAuditLedgerRows(t, db, e3); n != 1 {
		t.Fatalf("ledger rows for e3 = %d, want 1", n)
	}
	var stepRow struct {
		Status     string
		LeaseOwner string
	}
	if err := db.Raw(`SELECT status, lease_owner FROM operation_steps WHERE id = ?`, step.ID).Scan(&stepRow).Error; err != nil {
		t.Fatal(err)
	}
	if stepRow.Status != string(domain.StepSucceeded) {
		t.Fatalf("step status = %s, want succeeded", stepRow.Status)
	}

	// 4. Event ingestion + audit fact: ledger row AND the receipt row commit.
	e4 := idgen.NewID()
	store := repository.NewEventStoreRepository(db)
	receipts := repository.NewEventReceiptRepositoryWithStore(db, store, ledger).(repository.EventSnapshotAuditRepository)
	envelope := &event.Envelope{
		EventID: idgen.NewID(), Source: "aero-vault", SourceRegion: "us-west",
		EventType: "vault.usage.updated", SchemaVersion: 1, AggregateType: "object",
		AggregateID: "obj-1", AggregateVersion: 1, TenantID: "tenant-a",
		OperationID: idgen.NewID(), CorrelationID: idgen.NewID(), CausationID: idgen.NewID(),
		TraceID: "trace-1", ActorUID: "u-1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"account_id": account.ID.String(), "dataset": "aero-vault.buckets", "data": map[string]any{"usage_bytes": float64(10)}},
	}
	mutation, err := envelope.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	result, err := receipts.ApplySnapshotEventWithAudit(ctx, envelope, mutation, auditWiringFact(e4, "tenant-a", "event.applied"))
	if err != nil {
		t.Fatalf("ApplySnapshotEventWithAudit: %v", err)
	}
	if result.Status != repository.EventApplied {
		t.Fatalf("apply status = %s, want applied", result.Status)
	}
	if n := countAuditLedgerRows(t, db, e4); n != 1 {
		t.Fatalf("ledger rows for e4 = %d, want 1", n)
	}
	var receiptRows int64
	if err := db.Raw(`SELECT count(*) FROM event_receipts WHERE event_id = ?`, envelope.EventID).Scan(&receiptRows).Error; err != nil {
		t.Fatal(err)
	}
	if receiptRows != 1 {
		t.Fatalf("receipt rows = %d, want 1 (receipt committed with the fact)", receiptRows)
	}
}

// TestAuditWiringRollbackAtomicity is AC3b: when the in-transaction audit
// append fails (ErrAuditDuplicate), the WHOLE mutation rolls back — the
// domain write is not committed without its audit fact, and the ledger is not
// double-written.
func TestAuditWiringRollbackAtomicity(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-wiring-key-0123456789abcdef"})
	accountRepo := repository.NewAccountRepository(db, ledger)
	profileRepo := accountRepo.(repository.AccountProfileAuditRepository)
	account, err := accountRepo.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-atomic", "u-atomic", "local")
	if err != nil {
		t.Fatal(err)
	}

	// Profile: the first mutation commits with e1; a fresh mutation reusing e1
	// must fail atomically — profile keeps the first values, ledger keeps one e1.
	e1 := idgen.NewID()
	profile := &domain.AccountProfile{AccountID: account.ID, DisplayName: "first", Locale: "en"}
	if err := profileRepo.UpdateProfileWithAudit(ctx, profile, auditWiringFact(e1, "tenant-a", "account.profile.updated")); err != nil {
		t.Fatalf("first UpdateProfileWithAudit: %v", err)
	}
	dupe := &domain.AccountProfile{AccountID: account.ID, DisplayName: "second", Locale: "fr"}
	err = profileRepo.UpdateProfileWithAudit(ctx, dupe, auditWiringFact(e1, "tenant-a", "account.profile.updated"))
	if !errors.Is(err, repository.ErrAuditDuplicate) {
		t.Fatalf("duplicate profile err = %v, want ErrAuditDuplicate", err)
	}
	var stored struct {
		DisplayName string
		Locale      string
	}
	if err := db.Raw(`SELECT display_name, locale FROM account_profiles WHERE account_id = ?`, account.ID).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.DisplayName != "first" || stored.Locale != "en" {
		t.Fatalf("profile after rollback = %+v, want first/en (mutation rolled back)", stored)
	}
	if n := countAuditLedgerRows(t, db, e1); n != 1 {
		t.Fatalf("ledger rows for e1 = %d, want exactly 1 (no double append)", n)
	}

	// Step: the step path is a Class B site (contract §4.2) — a duplicate
	// append is swallowed inside the transaction. A retried finalization of a
	// DIFFERENT step reusing an existing event_id (an artificial duplicate, as
	// real anchors are per-step) now COMMITS the step mutation with the append
	// swallowed: ledger keeps exactly one e3 row and step B is finalized.
	opRepo := repository.NewOperationRepository(db, ledger).(repository.OperationAuditRepository)
	opStateRepo := repository.NewOperationRepository(db, ledger).(repository.OperationStateAuditRepository)
	op := &domain.Operation{
		AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		RequestID: "req-2", HomeRegion: "local", OperationType: "account.sync",
		Status: domain.OperationRequested, IdempotencyKey: "idem-2",
		Steps: []*domain.OperationStep{
			{StepKey: "step-a", Source: "aero-im", Action: "sync.profile", Status: domain.StepPending},
			{StepKey: "step-b", Source: "aero-im", Action: "sync.profile", Status: domain.StepPending},
		},
	}
	if err := opRepo.CreateWithAudit(ctx, op, auditWiringFact(idgen.NewID(), "tenant-a", "operation.created")); err != nil {
		t.Fatalf("CreateWithAudit: %v", err)
	}
	stepA, err := repository.NewOperationRepository(db, ledger).ClaimNextStep(ctx, op.ID, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextStep A: %v", err)
	}
	e3 := idgen.NewID()
	if err := opStateRepo.FinishStepWithAudit(ctx, stepA.ID, "worker-a", domain.StepSucceeded,
		"src-op-a", "", nil, "", auditWiringFact(e3, "tenant-a", "operation.step.succeeded")); err != nil {
		t.Fatalf("FinishStepWithAudit A: %v", err)
	}
	stepB, err := repository.NewOperationRepository(db, ledger).ClaimNextStep(ctx, op.ID, "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextStep B: %v", err)
	}
	if err := opStateRepo.FinishStepWithAudit(ctx, stepB.ID, "worker-b", domain.StepSucceeded,
		"src-op-b", "", nil, "", auditWiringFact(e3, "tenant-a", "operation.step.succeeded")); err != nil {
		t.Fatalf("duplicate step append must be swallowed in-tx, got %v", err)
	}
	var stepBRow struct {
		Status     string
		LeaseOwner string
	}
	if err := db.Raw(`SELECT status, lease_owner FROM operation_steps WHERE id = ?`, stepB.ID).Scan(&stepBRow).Error; err != nil {
		t.Fatal(err)
	}
	if stepBRow.Status != string(domain.StepSucceeded) || stepBRow.LeaseOwner != "" {
		t.Fatalf("step B after swallowed duplicate = %+v, want succeeded/cleared lease (mutation committed)", stepBRow)
	}
	if n := countAuditLedgerRows(t, db, e3); n != 1 {
		t.Fatalf("ledger rows for e3 = %d, want exactly 1 (duplicate append swallowed)", n)
	}
}

// installFailPublicationInsertTrigger installs a BEFORE INSERT trigger on
// audit_publications that raises: the in-tree mechanism for failing an append
// AFTER the audit_ledger row is staged. The publication insert is the last
// statement of every WithAudit transaction, so this is the only failure mode
// that proves the crash window — a failure after the ledger row exists must
// roll the ledger row back with the mutation (never a ledger row without its
// publication, never a mutation without its fact).
func installFailPublicationInsertTrigger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE FUNCTION fail_publication_insert() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'publication insert blocked (test)';
		END;
		$$ LANGUAGE plpgsql`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_publication_insert_trigger
		BEFORE INSERT ON audit_publications FOR EACH ROW EXECUTE FUNCTION fail_publication_insert()`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DROP TRIGGER IF EXISTS fail_publication_insert_trigger ON audit_publications`).Error
		_ = db.Exec(`DROP FUNCTION IF EXISTS fail_publication_insert()`).Error
	})
}

// TestAuditWiringPublicationInsertRollsBackLedgerRow (FM-2): when the
// publication-row insert fails inside the shared transaction, the LEDGER row
// rolls back with it — a publication can never exist without its ledger row,
// and the drain's post-commit fact read stays safe.
func TestAuditWiringPublicationInsertRollsBackLedgerRow(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	installFailPublicationInsertTrigger(t, db)

	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "integration-audit-wiring-key-0123456789abcdef"})
	accountRepo := repository.NewAccountRepository(db, ledger)
	profileRepo := accountRepo.(repository.AccountProfileAuditRepository)
	account, err := accountRepo.EnsureByIdentity(ctx, "tenant-a", "snaplink", "u-fm2", "u-fm2", "local")
	if err != nil {
		t.Fatal(err)
	}
	eventID := idgen.NewID()
	profile := &domain.AccountProfile{AccountID: account.ID, DisplayName: "blocked"}
	err = profileRepo.UpdateProfileWithAudit(ctx, profile, auditWiringFact(eventID, "tenant-a", "account.profile.updated"))
	if err == nil {
		t.Fatal("mutation must fail when the publication insert fails")
	}
	if n := countAuditLedgerRows(t, db, eventID); n != 0 {
		t.Fatalf("ledger rows after publication failure = %d, want 0 (ledger rolled back with publication)", n)
	}
	if n := countAuditPublicationRows(t, db, eventID); n != 0 {
		t.Fatalf("publication rows after publication failure = %d, want 0", n)
	}
	var profileRow struct {
		DisplayName string
	}
	if err := db.Raw(`SELECT display_name FROM account_profiles WHERE account_id = ?`, account.ID).Scan(&profileRow).Error; err != nil {
		t.Fatal(err)
	}
	if profileRow.DisplayName == "blocked" {
		t.Fatal("profile mutation must roll back with the failed append")
	}
}
