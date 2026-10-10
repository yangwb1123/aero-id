//go:build integration

package repository_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

func syncExecutionPostgresJob(t *testing.T, db *gorm.DB, ledger repository.FullAuditLedgerRepository, kind domain.SyncJobType) (repository.SyncJobRepository, *domain.SyncJob) {
	t.Helper()
	key := idgen.NewID().String()
	account, err := repository.NewAccountRepository(db, ledger).EnsureByIdentity(t.Context(), "tenant-a", "snaplink", key, key, "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.NewRegionDirectoryRepository(db, ledger).EnsureDirectory(t.Context(), account.TenantID, account.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	repo := repository.NewSyncJobRepository(db, ledger)
	job := &domain.SyncJob{AccountID: account.ID, ActorUID: "actor", TenantID: account.TenantID, JobType: kind, Status: domain.SyncJobPending, IdempotencyKey: key,
		Request: map[string]any{"datasets": []string{"aero-vault.tenants"}}}
	if err := repo.Create(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.Claim(t.Context(), job.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return repo, claimed
}
func syncBlockedFact(job *domain.SyncJob) *domain.AuditFact {
	id := idgen.NewID()
	return &domain.AuditFact{ID: id, EventID: idgen.Anchor("sync.execution.blocked", job.TenantID, job.ID.String(), "sync.authorization_denied", id.String()),
		Source: "aero-id", SourceRegion: "local", ActorUID: job.ActorUID, TenantID: job.TenantID, AccountID: job.AccountID, PartitionKey: job.TenantID + ":" + job.AccountID.String(),
		Action: "sync.execution.blocked", ResourceType: "sync_job", ResourceID: job.ID.String(), OccurredAt: time.Now().UTC(), Payload: map[string]any{"error_code": "sync.authorization_denied"}}
}
func syncDeferral(job *domain.SyncJob, before bool) repository.SyncJobExecutionDeferral {
	return repository.SyncJobExecutionDeferral{Job: job, BeforeEffects: before, Code: "sync.authorization_denied", Message: "current sync action is not authorized"}
}

func TestSyncExecutionPostgresAuthorityDeferralLeaseAndExpiry(t *testing.T) {
	db, ledger := startAttemptAuditLedger(t)
	t.Run("expired execution commits unknown and an immutable fact", func(t *testing.T) {
		repo, job := syncExecutionPostgresJob(t, db, ledger, domain.SyncJobSync)
		before := countLedgerRowsForAction(t, db, "sync.execution.expired")
		executionSQL(t, db, `UPDATE sync_jobs SET lease_until=clock_timestamp()-interval '1 second',result='{"sources":{"aero-vault":{"source_operation_id":"original","version":7}}}' WHERE id=?`, job.ID)
		_, err := repo.(repository.RegionalSyncJobWorkerRepository).ClaimNextForRegion(t.Context(), "second", time.Minute, "local")
		if !errors.Is(err, repository.ErrSyncJobUnknown) {
			t.Fatalf("expiry %v", err)
		}
		fresh, err := repo.Get(t.Context(), job.ID)
		if err != nil || fresh.Status != domain.SyncJobUnknown || fresh.Attempt != job.Attempt || fresh.LeaseOwner != "" || fresh.Result["sources"] == nil {
			t.Fatal("unknown rolled back or lost original result", err)
		}
		if countLedgerRowsForAction(t, db, "sync.execution.expired") != before+1 {
			t.Fatal("expiry had no durable fact")
		}
		if _, err := repo.Claim(t.Context(), job.ID, "no-reissue", time.Minute); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal("expired write returned pending", err)
		}
	})
	t.Run("only an untouched execution returns pending with local pacing", func(t *testing.T) {
		repo, job := syncExecutionPostgresJob(t, db, ledger, domain.SyncJobSync)
		gate := repo.(repository.SyncJobExecutionRepository)
		if err := gate.CheckExecutionLease(t.Context(), job, "local", false); err != nil {
			t.Fatal(err)
		}
		fact := syncBlockedFact(job)
		if err := gate.DeferExecutionWithAudit(t.Context(), syncDeferral(job, true), fact); err != nil {
			t.Fatal(err)
		}
		fresh, err := repo.Get(t.Context(), job.ID)
		if err != nil || fresh.Status != domain.SyncJobPending || fresh.CompletedAt != nil || fresh.LeaseOwner != "" || fresh.LeaseUntil == nil || !fresh.LeaseUntil.After(time.Now()) {
			t.Fatal("unstarted claim not paced pending", err)
		}
		if _, err := repo.ClaimNext(t.Context(), "background", time.Minute); !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal("blocked claim hot-looped", err)
		}
		claimed, err := repo.Claim(t.Context(), job.ID, "manual", time.Minute)
		if err != nil || claimed.LeaseOwner == job.LeaseOwner {
			t.Fatal("manual claim could not override pacing with a new token", err)
		}
		before := countLedgerRowsForAction(t, db, "sync.execution.blocked")
		if err := gate.DeferExecutionWithAudit(t.Context(), syncDeferral(job, true), syncBlockedFact(job)); !errors.Is(err, repository.ErrSyncJobLeaseLost) {
			t.Fatal("stale deferral changed replacement", err)
		}
		if countLedgerRowsForAction(t, db, "sync.execution.blocked") != before {
			t.Fatal("stale owner committed a fact")
		}
	})
	t.Run("original artifact checkpoint cannot become a new execution", func(t *testing.T) {
		repo, job := syncExecutionPostgresJob(t, db, ledger, domain.SyncJobExport)
		key := "recovery/" + job.ID.String() + "/original"
		expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
		checkpoint := repository.ArtifactCheckpoint{JobID: job.ID, Kind: "export", Worker: job.LeaseOwner, StorageKey: key, ManifestSHA256: strings.Repeat("a", 64), SizeBytes: 17,
			Continuation: map[string]any{"_artifact_result": map[string]any{"_storage_key": key, "expires_at": expiry}}}
		if err := repo.(repository.SyncJobRecoveryRepository).CheckpointArtifact(repository.WithSyncJobWorker(t.Context(), job.LeaseOwner), checkpoint); err != nil {
			t.Fatal(err)
		}
		if err := repo.(repository.SyncJobExecutionRepository).DeferExecutionWithAudit(t.Context(), syncDeferral(job, true), syncBlockedFact(job)); err != nil {
			t.Fatal(err)
		}
		fresh, err := repo.Get(t.Context(), job.ID)
		if err != nil || fresh.Status != domain.SyncJobUnknown {
			t.Fatal("checkpoint restored pending", err)
		}
		artifact, err := repo.(repository.SyncJobArtifactRepository).GetArtifact(t.Context(), job.ID, "export")
		if err != nil || artifact.StorageKey != key || artifact.ManifestSHA256 != checkpoint.ManifestSHA256 || artifact.SizeBytes != 17 || artifact.Continuation["_artifact_result"].(map[string]any)["expires_at"] != expiry {
			t.Fatal("deferral changed original artifact/TTL", err)
		}
	})
	t.Run("reconciliation preserves original unknown result", func(t *testing.T) {
		repo, job := syncExecutionPostgresJob(t, db, ledger, domain.SyncJobErase)
		executionSQL(t, db, `UPDATE sync_jobs SET status='unknown',lease_owner='',lease_until=NULL,result='{"sources":{"aero-vault":{"status":"unknown","source_operation_id":"original","version":7}}}' WHERE id=?`, job.ID)
		claimed, err := repo.ClaimUnknown(t.Context(), job.ID, "query", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.(repository.SyncJobExecutionRepository).DeferExecutionWithAudit(t.Context(), syncDeferral(claimed, true), syncBlockedFact(claimed)); err != nil {
			t.Fatal(err)
		}
		fresh, err := repo.Get(t.Context(), job.ID)
		if err != nil || fresh.Status != domain.SyncJobUnknown || fresh.Result["sources"].(map[string]any)["aero-vault"].(map[string]any)["source_operation_id"] != "original" {
			t.Fatal("unknown discarded original receipt", err)
		}
	})
	t.Run("current tenant disposition and directory fences reject a live lease", func(t *testing.T) {
		repo, job := syncExecutionPostgresJob(t, db, ledger, domain.SyncJobErase)
		gate := repo.(repository.SyncJobExecutionRepository)
		foreign := *job
		foreign.TenantID = "foreign"
		if err := gate.CheckExecutionLease(t.Context(), &foreign, "local", false); !errors.Is(err, repository.ErrSyncJobLeaseLost) {
			t.Fatal("foreign tenant admitted", err)
		}
		executionSQL(t, db, `UPDATE region_directory SET fenced_epoch=fenced_epoch+1 WHERE account_id=?`, job.AccountID)
		if err := gate.CheckExecutionLease(t.Context(), job, "local", false); !errors.Is(err, repository.ErrSyncJobLeaseLost) {
			t.Fatal("directory fence ignored", err)
		}
		executionSQL(t, db, `UPDATE region_directory SET fenced_epoch=fenced_epoch-1 WHERE account_id=?`, job.AccountID)
		executionSQL(t, db, `UPDATE accounts SET status='erased' WHERE id=?`, job.AccountID)
		if err := gate.CheckExecutionLease(t.Context(), job, "local", false); !errors.Is(err, repository.ErrSyncJobLeaseLost) {
			t.Fatal("erased source call admitted", err)
		}
		if err := gate.CheckExecutionLease(t.Context(), job, "local", true); err != nil {
			t.Fatal("idempotent local erase finish cannot reconcile", err)
		}
	})
	t.Run("audit rejection rolls back deferral and expired execution", func(t *testing.T) {
		repo, job := syncExecutionPostgresJob(t, db, ledger, domain.SyncJobSync)
		executionSQL(t, db, `CREATE FUNCTION reject_sync_execution_fact() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.action IN ('sync.execution.blocked','sync.execution.expired') THEN RAISE EXCEPTION 'test audit rejection'; END IF; RETURN NEW; END $$`)
		executionSQL(t, db, `CREATE TRIGGER reject_sync_execution_fact BEFORE INSERT ON audit_ledger FOR EACH ROW EXECUTE FUNCTION reject_sync_execution_fact()`)
		t.Cleanup(func() {
			executionSQL(t, db, `DROP TRIGGER reject_sync_execution_fact ON audit_ledger; DROP FUNCTION reject_sync_execution_fact()`)
		})
		if err := repo.(repository.SyncJobExecutionRepository).DeferExecutionWithAudit(t.Context(), syncDeferral(job, true), syncBlockedFact(job)); err == nil {
			t.Fatal("audit failure allowed deferral")
		}
		fresh, err := repo.Get(t.Context(), job.ID)
		if err != nil || fresh.Status != domain.SyncJobRunning || fresh.LeaseOwner != job.LeaseOwner || fresh.ErrorCode != "" {
			t.Fatal("failed deferral changed receipt", err)
		}
		executionSQL(t, db, `UPDATE sync_jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=?`, job.ID)
		if _, err := repo.ClaimNext(t.Context(), "background", time.Minute); err == nil || errors.Is(err, repository.ErrSyncJobUnknown) {
			t.Fatal("expired execution committed without audit", err)
		}
		fresh, err = repo.Get(t.Context(), job.ID)
		if err != nil || fresh.Status != domain.SyncJobRunning {
			t.Fatal("expiry did not roll back", err)
		}
		// Remove this fixture from the next subtest's stale-execute selection.
		executionSQL(t, db, `UPDATE sync_jobs SET status='unknown',lease_owner='',lease_until=NULL WHERE id=?`, job.ID)
	})
	t.Run("database clock after a row lock wait rejects late deferral", func(t *testing.T) {
		repo, job := syncExecutionPostgresJob(t, db, ledger, domain.SyncJobSync)
		tx := db.Begin()
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		defer tx.Rollback()
		executionSQL(t, tx, `UPDATE sync_jobs SET lease_until=clock_timestamp()+interval '120 milliseconds' WHERE id=?`, job.ID)
		// Commit the deadline, then hold the same row without changing it.
		if err := tx.Commit().Error; err != nil {
			t.Fatal(err)
		}
		tx = db.Begin()
		defer tx.Rollback()
		executionSQL(t, tx, `SELECT id FROM sync_jobs WHERE id=? FOR UPDATE`, job.ID)
		done := make(chan error, 1)
		go func() {
			done <- repo.(repository.SyncJobExecutionRepository).DeferExecutionWithAudit(t.Context(), syncDeferral(job, true), syncBlockedFact(job))
		}()
		time.Sleep(160 * time.Millisecond)
		if err := tx.Commit().Error; err != nil {
			t.Fatal(err)
		}
		if err := <-done; !errors.Is(err, repository.ErrSyncJobLeaseLost) {
			t.Fatal("lock wait resurrected expired lease", err)
		}
	})
	t.Run("manual and worker reconciliation share one active claim", func(t *testing.T) {
		repo, job := syncExecutionPostgresJob(t, db, ledger, domain.SyncJobErase)
		executionSQL(t, db, `UPDATE sync_jobs SET status='unknown',lease_owner='',lease_until=NULL WHERE id=?`, job.ID)
		var wait sync.WaitGroup
		results := make(chan error, 2)
		for _, owner := range []string{"manual", "worker"} {
			wait.Go(func() { _, err := repo.ClaimUnknown(t.Context(), job.ID, owner, time.Minute); results <- err })
		}
		wait.Wait()
		close(results)
		successes := 0
		for err := range results {
			if err == nil {
				successes++
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatal(err)
			}
		}
		if successes != 1 {
			t.Fatalf("active claims %d", successes)
		}
	})
}
