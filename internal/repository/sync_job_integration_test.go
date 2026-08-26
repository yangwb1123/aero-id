//go:build integration

package repository_test

import (
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestListExpiredExportsIncludesAuditExports(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-export", "snaplink", "export-user", "export-user", "local")
	if err != nil {
		t.Fatal(err)
	}
	jobs := repository.NewSyncJobRepository(db, ledger)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	for i, jobType := range []domain.SyncJobType{domain.SyncJobExport, domain.SyncJobAuditExport} {
		job := &domain.SyncJob{ID: idgen.NewID(), AccountID: account.ID, ActorUID: "export-user", TenantID: "tenant-export",
			JobType: jobType, Status: domain.SyncJobCompleted, IdempotencyKey: []string{"expired-a", "expired-b"}[i],
			Result: map[string]any{"expires_at": past, "_storage_key": "expired-key"}}
		if err := jobs.Create(t.Context(), job); err != nil {
			t.Fatalf("create %s job: %v", jobType, err)
		}
	}
	futureJob := &domain.SyncJob{ID: idgen.NewID(), AccountID: account.ID, ActorUID: "export-user", TenantID: "tenant-export",
		JobType: domain.SyncJobAuditExport, Status: domain.SyncJobCompleted, IdempotencyKey: "future",
		Result: map[string]any{"expires_at": future, "_storage_key": "future-key"}}
	if err := jobs.Create(t.Context(), futureJob); err != nil {
		t.Fatal(err)
	}

	items, err := jobs.ListExpiredExports(t.Context(), time.Now().UTC(), 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[domain.SyncJobType]bool, len(items))
	for _, job := range items {
		seen[job.JobType] = true
	}
	if !seen[domain.SyncJobExport] || !seen[domain.SyncJobAuditExport] || len(items) != 2 {
		t.Fatalf("expired jobs = %+v, want one export and one audit_export", items)
	}
}
