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
	"github.com/aero/aero-id/internal/repository/testpg"
	"gorm.io/gorm"
)

func TestSyncRecoveryPostgresDuplicateAndLeaseFence(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "recovery-tenant", "snaplink", "recovery-user", "recovery-user", "local")
	if err != nil {
		t.Fatal(err)
	}
	jobs := repository.NewSyncJobRepository(db, ledger)
	job := &domain.SyncJob{ID: idgen.NewID(), AccountID: account.ID, ActorUID: "recovery-user", TenantID: account.TenantID,
		JobType: domain.SyncJobSync, Status: domain.SyncJobPending, IdempotencyKey: "recovery-1",
		Request: map[string]any{"datasets": []any{"aero-im.profile"}}}
	if err := jobs.Create(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	claimed, err := jobs.Claim(t.Context(), job.ID, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := repository.WithSyncJobWorker(t.Context(), claimed.LeaseOwner)
	recovery := jobs.(repository.SyncJobRecoveryRepository)
	request := domain.SummaryApplicationRequest{AccountID: account.ID, Source: "aero-im", SourceRegion: "local", Version: 7,
		RequestedDatasets: []domain.AccountDataset{"aero-im.profile"}, Complete: true}
	input := repository.SummaryReplayInput{JobID: job.ID, Source: "aero-im", ExecutionAttempt: claimed.Attempt, Request: request}
	if err := recovery.SaveSummaryReplay(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err := recovery.SaveSummaryReplay(ctx, input); err != nil {
		t.Fatalf("identical replay retry: %v", err)
	}
	if err := recovery.SaveSummaryReplay(ctx, repository.SummaryReplayInput{JobID: job.ID, Source: "aero-im", ExecutionAttempt: claimed.Attempt, Request: domain.SummaryApplicationRequest{AccountID: account.ID, Source: "aero-im", Version: 8}}); !errors.Is(err, repository.ErrSyncReplayConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrSyncReplayConflict", err)
	}
	replays, err := recovery.ListSummaryReplays(t.Context(), job.ID)
	if err != nil || len(replays) != 1 {
		t.Fatalf("replays = %d/%v, want one row", len(replays), err)
	}
	if err := recovery.ResolveSummaryReplay(ctx, job.ID, "aero-im", claimed.Attempt, replays[0].RequestSHA256); err != nil {
		t.Fatal(err)
	}

	if err := recovery.EnsureEraseOutcomes(ctx, job.ID, claimed.LeaseOwner, []repository.EraseOutcomeInput{{Source: "aero-im", RequestedDatasets: []domain.AccountDataset{"aero-im.profile"}}}); err != nil {
		t.Fatal(err)
	}
	if err := recovery.CheckpointEraseOutcome(ctx, repository.EraseOutcomeCheckpoint{JobID: job.ID, Source: "aero-im", Worker: claimed.LeaseOwner, Status: "unknown", ErrorCode: "source.timeout"}); err != nil {
		t.Fatal(err)
	}
	if err := recovery.CheckpointArtifact(ctx, repository.ArtifactCheckpoint{JobID: job.ID, Kind: "export", Worker: claimed.LeaseOwner,
		StorageKey: "recovery/" + job.ID.String() + "/artifact", ManifestSHA256: strings.Repeat("a", 64), SizeBytes: 3}); err != nil {
		t.Fatal(err)
	}

	if err := db.Exec("UPDATE sync_jobs SET lease_until = now() - interval '1 second' WHERE id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	staleCtx := repository.WithSyncJobWorker(t.Context(), claimed.LeaseOwner)
	if err := recovery.CheckpointEraseOutcome(staleCtx, repository.EraseOutcomeCheckpoint{JobID: job.ID, Source: "aero-im", Worker: claimed.LeaseOwner, Status: "completed"}); !errors.Is(err, repository.ErrSyncJobLeaseLost) {
		t.Fatalf("stale checkpoint error = %v, want ErrSyncJobLeaseLost", err)
	}
	if err := jobs.Finish(t.Context(), job.ID, claimed.LeaseOwner, domain.SyncJobCompleted, "", "", nil); !errors.Is(err, repository.ErrSyncJobLeaseLost) {
		t.Fatalf("stale finish error = %v, want ErrSyncJobLeaseLost", err)
	}
}

func TestSyncRecoveryPostgresConcurrentClaimsAreExclusive(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "claim-race-tenant", "snaplink", "claim-race-user", "claim-race-user", "local")
	if err != nil {
		t.Fatal(err)
	}
	jobs := repository.NewSyncJobRepository(db, ledger)
	const count = 12
	for i := 0; i < count; i++ {
		job := &domain.SyncJob{ID: idgen.NewID(), AccountID: account.ID, ActorUID: "claim-race-user", TenantID: account.TenantID,
			JobType: domain.SyncJobSync, Status: domain.SyncJobUnknown, IdempotencyKey: "claim-race-" + string(rune('a'+i))}
		if err := jobs.Create(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	claimed := make(chan *domain.SyncJob, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			job, err := jobs.ClaimUnknownNext(t.Context(), "recovery-race-worker", time.Minute)
			if err == nil {
				claimed <- job
				return
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Errorf("concurrent claim: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(claimed)

	seen := make(map[domain.ID]bool, count)
	for job := range claimed {
		if job == nil || seen[job.ID] {
			t.Fatalf("duplicate or nil claim: %#v", job)
		}
		seen[job.ID] = true
		if job.LeaseKind != "reconcile" || job.Status != domain.SyncJobRunning {
			t.Fatalf("claim = %#v, want running/reconcile", job)
		}
	}
	if len(seen) != count {
		t.Fatalf("claimed %d jobs, want %d", len(seen), count)
	}
}

func TestSyncRecoveryPostgresClaimsEveryRecoveryType(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "claim-tenant", "snaplink", "claim-user", "claim-user", "local")
	if err != nil {
		t.Fatal(err)
	}
	jobs := repository.NewSyncJobRepository(db, ledger)
	for i, jobType := range []domain.SyncJobType{domain.SyncJobSync, domain.SyncJobExport, domain.SyncJobAuditExport, domain.SyncJobReconcile, domain.SyncJobErase} {
		job := &domain.SyncJob{ID: idgen.NewID(), AccountID: account.ID, ActorUID: "claim-user", TenantID: account.TenantID,
			JobType: jobType, Status: domain.SyncJobUnknown, IdempotencyKey: "claim-" + string(rune('a'+i))}
		if err := jobs.Create(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[domain.SyncJobType]bool{}
	for range 5 {
		job, err := jobs.ClaimUnknownNext(t.Context(), "recovery-worker", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		seen[job.JobType] = true
		if job.LeaseKind != "reconcile" {
			t.Fatalf("job %s lease kind = %q, want reconcile", job.JobType, job.LeaseKind)
		}
	}
	if len(seen) != 5 {
		t.Fatalf("claimed types = %v, want all recovery types", seen)
	}
}
