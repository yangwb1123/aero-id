//go:build integration

package repository_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

func executionPostgresOperation(t *testing.T, db *gorm.DB, ledger repository.FullAuditLedgerRepository, compensation bool) (repository.OperationRepository, *domain.Operation) {
	t.Helper()
	key := idgen.NewID().String()
	account, err := repository.NewAccountRepository(db, ledger).EnsureByIdentity(t.Context(), "tenant-a", "snaplink", key, key, "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.NewRegionDirectoryRepository(db, ledger).EnsureDirectory(t.Context(), account.TenantID, account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	op := &domain.Operation{AccountID: account.ID, ActorUID: "actor-1", TenantID: account.TenantID, HomeRegion: "local", OperationType: "source.command",
		IdempotencyKey: key, Status: domain.OperationRunning, Steps: []*domain.OperationStep{{StepKey: "first", Source: "aero-vault", Action: "erase",
			Status: domain.StepPending, RequestPayload: map[string]any{"datasets": []string{"aero-vault.tenants"}}}}}
	if compensation {
		op.Status = domain.OperationCompensating
		op.Steps[0].Status = domain.StepSucceeded
		op.Steps[0].CompensationAction = "summary"
		op.Steps[0].CompensationStatus = domain.StepPending
	}
	repo := repository.NewOperationRepository(db, ledger)
	if err := repo.(repository.OperationAuditRepository).CreateWithAudit(t.Context(), op, auditWiringFact(idgen.NewID(), op.TenantID, "operation.created")); err != nil {
		t.Fatal(err)
	}
	return repo, op
}
func blockedExecutionFact(op *domain.Operation) *domain.AuditFact {
	return &domain.AuditFact{EventID: idgen.NewID(), Source: "aero-id", SourceRegion: op.HomeRegion, PartitionKey: op.TenantID + ":" + op.AccountID.String(),
		ActorUID: op.ActorUID, TenantID: op.TenantID, OperationID: op.ID, AccountID: op.AccountID, Action: "operation.execution.blocked", ResourceType: "operation", ResourceID: op.ID.String(), OccurredAt: time.Now().UTC(), Payload: map[string]any{"error_code": "operation.authorization_denied"}}
}
func containsExecutionID(ids []domain.ID, id domain.ID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
func executionSQL(t *testing.T, db *gorm.DB, query string, args ...any) {
	t.Helper()
	if err := db.Exec(query, args...).Error; err != nil {
		t.Fatal(err)
	}
}

func TestOperationExecutionPostgresFencingDeferralAndExpiredAttempts(t *testing.T) {
	db, ledger := startAttemptAuditLedger(t)
	t.Run("expired forward commits unknown and only permits status reconciliation", func(t *testing.T) {
		repo, op := executionPostgresOperation(t, db, ledger, false)
		step, err := repo.ClaimNextStep(t.Context(), op.ID, "old", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		lease := repo.(repository.OperationExecutionLeaseRepository)
		if err := lease.CheckExecutionLease(t.Context(), op.ID, step.ID, "old", false, false); err != nil {
			t.Fatal(err)
		}
		executionSQL(t, db, `UPDATE operation_steps SET lease_until=clock_timestamp()-interval '1 second',source_operation_id='original',result='{"version":7}' WHERE id=?`, step.ID)
		if _, err := repo.ClaimNextStep(t.Context(), op.ID, "second-command", time.Minute); !errors.Is(err, repository.ErrOperationStepUnknown) {
			t.Fatalf("expired forward: %v", err)
		}
		fresh, err := repo.Get(t.Context(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Steps[0].Status != domain.StepUnknown || fresh.Steps[0].Attempt != 1 || fresh.Steps[0].SourceOperationID != "original" {
			t.Fatal("unknown did not commit or receipt changed")
		}
		if _, err := repo.ClaimNextStep(t.Context(), op.ID, "no-reissue", time.Minute); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal("command was reissued", err)
		}
		if countLedgerRowsForAction(t, db, "operation.step.expired") != 1 || countAttemptRows(t, db, step.ID, true) != 1 {
			t.Fatal("expired attempt lacked transactional evidence")
		}
		query, err := repo.ClaimUnknownStep(t.Context(), op.ID, "query", time.Minute)
		if err != nil || query.Attempt != 2 || query.SourceOperationID != "original" {
			t.Fatal("cannot reconcile original receipt", err)
		}
		if err := lease.CheckExecutionLease(t.Context(), op.ID, step.ID, "old", false, false); !errors.Is(err, repository.ErrOperationStateConflict) {
			t.Fatal("old claimant remains live", err)
		}
		if err := lease.CheckExecutionLease(t.Context(), op.ID, step.ID, "query", false, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("expired compensation is not another compensation command", func(t *testing.T) {
		repo, op := executionPostgresOperation(t, db, ledger, true)
		step, err := repo.ClaimCompensationStep(t.Context(), op.ID, "old-comp", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		executionSQL(t, db, `UPDATE operation_steps SET compensation_lease_until=clock_timestamp()-interval '1 second',compensation_source_operation_id='original-comp',compensation_result='{"version":8}' WHERE id=?`, step.ID)
		if _, err := repo.ClaimCompensationStep(t.Context(), op.ID, "never-reissue", time.Minute); !errors.Is(err, repository.ErrOperationStepUnknown) {
			t.Fatalf("expired compensation: %v", err)
		}
		fresh, err := repo.Get(t.Context(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Steps[0].CompensationStatus != domain.StepUnknown || fresh.Steps[0].CompensationAttempt != 1 || fresh.Steps[0].CompensationSourceOperationID != "original-comp" {
			t.Fatal("compensation receipt was replaced")
		}
		if countLedgerRowsForAction(t, db, "operation.compensation.expired") != 1 || countAttemptRows(t, db, step.ID, true) != 1 {
			t.Fatal("compensation expiry lacked evidence")
		}
		query, err := repo.ClaimUnknownCompensationStep(t.Context(), op.ID, "query-comp", time.Minute)
		if err != nil || query.CompensationAttempt != 2 || query.CompensationSourceOperationID != "original-comp" {
			t.Fatal("cannot reconcile original compensation", err)
		}
	})
	t.Run("unstarted claims return pending with an atomic deferral fact", func(t *testing.T) {
		repo, op := executionPostgresOperation(t, db, ledger, false)
		step, err := repo.ClaimNextStep(t.Context(), op.ID, "claim", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		fact := attemptAuditFact(idgen.NewID(), op.TenantID, "operation.step.finished", "pending", "attempt", step.Attempt)
		if err := repo.(repository.OperationStateAuditRepository).FinishStepWithAudit(t.Context(), step.ID, "claim", domain.StepPending, "",
			"operation.authorization_denied", nil, "current action is not authorized", fact); err != nil {
			t.Fatal(err)
		}
		fresh, err := repo.Get(t.Context(), op.ID)
		if err != nil || fresh.Steps[0].Status != domain.StepPending || fresh.Steps[0].LeaseOwner != "" || fresh.Steps[0].SourceOperationID != "" {
			t.Fatal("unstarted effect became failed/unknown", err)
		}
		if countLedgerRowsForAction(t, db, "operation.step.finished") != 1 {
			t.Fatal("deferral lacked a fact")
		}
		compRepo, comp := executionPostgresOperation(t, db, ledger, true)
		compStep, err := compRepo.ClaimCompensationStep(t.Context(), comp.ID, "comp-claim", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		fact = attemptAuditFact(idgen.NewID(), comp.TenantID, "operation.compensation.deferred", "pending", "compensation_attempt", compStep.CompensationAttempt)
		if err := compRepo.(repository.OperationStateAuditRepository).FinishCompensationStepWithAudit(t.Context(), compStep.ID, "comp-claim", domain.StepPending, "",
			"operation.authorization_denied", nil, "current action is not authorized", fact); err != nil {
			t.Fatal(err)
		}
		fresh, err = compRepo.Get(t.Context(), comp.ID)
		if err != nil || fresh.Steps[0].CompensationStatus != domain.StepPending || fresh.Steps[0].Status != domain.StepSucceeded || fresh.Steps[0].CompensationLeaseOwner != "" {
			t.Fatal("unstarted compensation destroyed forward outcome", err)
		}
		if countLedgerRowsForAction(t, db, "operation.compensation.deferred") != 1 {
			t.Fatal("compensation deferral lacked a fact")
		}
		ids, err := compRepo.ListReconcileRunnable(t.Context(), 100)
		if err != nil || !containsExecutionID(ids, comp.ID) {
			t.Fatal("deferred compensation lost its worker continuation", err)
		}
	})
	t.Run("unknown compensation fences the next rollback effect", func(t *testing.T) {
		repo, op := executionPostgresOperation(t, db, ledger, true)
		executionSQL(t, db, `UPDATE operation_steps SET created_at=clock_timestamp()-interval '1 minute' WHERE operation_id=?`, op.ID)
		last := idgen.NewID()
		executionSQL(t, db, `INSERT INTO operation_steps (id,operation_id,step_key,source,action,status,compensation_action,compensation_status)
		VALUES (?,?,'last','aero-vault','erase','succeeded','summary','pending')`, last, op.ID)
		step, err := repo.ClaimCompensationStep(t.Context(), op.ID, "first-comp", time.Minute)
		if err != nil || step.ID != last {
			t.Fatal("compensation order changed", err)
		}
		if _, err := repo.ClaimCompensationStep(t.Context(), op.ID, "concurrent-next", time.Minute); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal("another compensation started while its predecessor was live", err)
		}
		executionSQL(t, db, `UPDATE operation_steps SET compensation_lease_until=clock_timestamp()-interval '1 second' WHERE id=?`, step.ID)
		if _, err := repo.ClaimCompensationStep(t.Context(), op.ID, "expired", time.Minute); !errors.Is(err, repository.ErrOperationStepUnknown) {
			t.Fatal(err)
		}
		if _, err := repo.ClaimCompensationStep(t.Context(), op.ID, "never-next", time.Minute); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal("unknown compensation authorized next effect", err)
		}
	})
	t.Run("blocked unknown preserves outcomes and is locally paced", func(t *testing.T) {
		repo, op := executionPostgresOperation(t, db, ledger, false)
		executionSQL(t, db, `UPDATE operations SET status='unknown',result='{"proof":"kept"}' WHERE id=?`, op.ID)
		executionSQL(t, db, `UPDATE operation_steps SET status='unknown',source_operation_id='original-unknown' WHERE operation_id=?`, op.ID)
		if err := repo.(repository.OperationExecutionBlockRepository).BlockExecutionWithAudit(t.Context(), op.ID, domain.OperationUnknown,
			"operation.authorization_denied", "current action is not authorized", blockedExecutionFact(op)); err != nil {
			t.Fatal(err)
		}
		fresh, err := repo.Get(t.Context(), op.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Status != domain.OperationUnknown || fresh.Result["proof"] != "kept" || fresh.Steps[0].SourceOperationID != "original-unknown" {
			t.Fatal("blocking destroyed prior outcome")
		}
		ids, err := repo.ListReconcileRunnable(t.Context(), 100)
		if err != nil || containsExecutionID(ids, op.ID) {
			t.Fatal("denial caused a hot loop", err)
		}
		executionSQL(t, db, `UPDATE operations SET updated_at=clock_timestamp()-interval '2 minutes' WHERE id=?`, op.ID)
		ids, err = repo.ListReconcileRunnable(t.Context(), 100)
		if err != nil || !containsExecutionID(ids, op.ID) {
			t.Fatal("blocked operation permanently lost", err)
		}
	})
	t.Run("directory fence and account disposition invalidate a live claim", func(t *testing.T) {
		repo, op := executionPostgresOperation(t, db, ledger, false)
		step, err := repo.ClaimNextStep(t.Context(), op.ID, "owner", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		lease := repo.(repository.OperationExecutionLeaseRepository)
		executionSQL(t, db, `UPDATE region_directory SET fenced_epoch=fenced_epoch+1 WHERE account_id=?`, op.AccountID)
		if err := lease.CheckExecutionLease(t.Context(), op.ID, step.ID, "owner", false, false); !errors.Is(err, repository.ErrOperationStateConflict) {
			t.Fatal("unfenced effect allowed", err)
		}
		executionSQL(t, db, `UPDATE region_directory SET fenced_epoch=fenced_epoch-1 WHERE account_id=?`, op.AccountID)
		executionSQL(t, db, `UPDATE accounts SET status='erased' WHERE id=?`, op.AccountID)
		if err := lease.CheckExecutionLease(t.Context(), op.ID, step.ID, "owner", false, false); !errors.Is(err, repository.ErrOperationStateConflict) {
			t.Fatal("erased effect allowed", err)
		}
	})
	t.Run("audit failure rolls back blocking and expiry transitions", func(t *testing.T) {
		repo, op := executionPostgresOperation(t, db, ledger, false)
		step, err := repo.ClaimNextStep(t.Context(), op.ID, "owner", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		executionSQL(t, db, `CREATE FUNCTION reject_execution_fact() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.action IN ('operation.execution.blocked','operation.step.expired') THEN RAISE EXCEPTION 'test audit rejection'; END IF; RETURN NEW; END $$`)
		executionSQL(t, db, `CREATE TRIGGER reject_execution_fact BEFORE INSERT ON audit_ledger FOR EACH ROW EXECUTE FUNCTION reject_execution_fact()`)
		t.Cleanup(func() {
			executionSQL(t, db, `DROP TRIGGER reject_execution_fact ON audit_ledger; DROP FUNCTION reject_execution_fact()`)
		})
		if err := repo.(repository.OperationExecutionBlockRepository).BlockExecutionWithAudit(t.Context(), op.ID, op.Status,
			"operation.authorization_denied", "not authorized", blockedExecutionFact(op)); err == nil {
			t.Fatal("audit failure ignored")
		}
		fresh, err := repo.Get(t.Context(), op.ID)
		if err != nil || fresh.ErrorCode != "" {
			t.Fatal("failed fact left blocked metadata", err)
		}
		executionSQL(t, db, `UPDATE operation_steps SET lease_until=clock_timestamp()-interval '1 second' WHERE id=?`, step.ID)
		if _, err := repo.ClaimNextStep(t.Context(), op.ID, "second", time.Minute); err == nil || errors.Is(err, repository.ErrOperationStepUnknown) {
			t.Fatal("expiry committed without fact", err)
		}
		fresh, err = repo.Get(t.Context(), op.ID)
		if err != nil || fresh.Steps[0].Status != domain.StepRunning || fresh.Steps[0].Attempt != 1 {
			t.Fatal("expiry did not roll back", err)
		}
	})
	t.Run("manual and worker competitors share one active lease", func(t *testing.T) {
		repo, op := executionPostgresOperation(t, db, ledger, false)
		var wait sync.WaitGroup
		outcomes := make(chan error, 2)
		for _, owner := range []string{"manual", "worker"} {
			wait.Go(func() { _, err := repo.ClaimNextStep(t.Context(), op.ID, owner, time.Minute); outcomes <- err })
		}
		wait.Wait()
		close(outcomes)
		successes := 0
		for err := range outcomes {
			if err == nil {
				successes++
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatal(err)
			}
		}
		if successes != 1 {
			t.Fatalf("active leases=%d", successes)
		}
	})
}
