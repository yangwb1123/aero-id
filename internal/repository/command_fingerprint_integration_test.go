//go:build integration

package repository_test

import (
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestCommandFingerprintsRoundTripAndRejectMalformedValues(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "actor-1", "actor-1", "local")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := "v1:" + strings.Repeat("a", 64)
	operations := repository.NewOperationRepository(db, ledger)
	operation := &domain.Operation{AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		HomeRegion: "local", OperationType: "sync", Status: domain.OperationRequested,
		IdempotencyKey: "operation-1", RequestFingerprint: fingerprint}
	if err := operations.Create(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	foundOperation, err := operations.FindByIdempotency(t.Context(), "actor-1", "tenant-a", "operation-1")
	if err != nil || foundOperation.RequestFingerprint != fingerprint {
		t.Fatalf("operation fingerprint round trip = %v/%v", foundOperation, err)
	}
	jobs := repository.NewSyncJobRepository(db, ledger)
	job := &domain.SyncJob{AccountID: account.ID, ActorUID: "actor-1", TenantID: "tenant-a",
		JobType: domain.SyncJobSync, Status: domain.SyncJobPending, IdempotencyKey: "sync-1", RequestFingerprint: fingerprint}
	if err := jobs.Create(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	foundJob, err := jobs.FindByIdempotency(t.Context(), "actor-1", "tenant-a", "sync-1")
	if err != nil || foundJob.RequestFingerprint != fingerprint {
		t.Fatalf("sync fingerprint round trip = %v/%v", foundJob, err)
	}
	for _, table := range []string{"operations", "sync_jobs"} {
		if err := db.Exec("UPDATE "+table+" SET request_fingerprint = ?", "invalid").Error; err == nil {
			t.Fatalf("%s accepted malformed fingerprint", table)
		}
	}
}
