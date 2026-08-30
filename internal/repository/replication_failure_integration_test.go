//go:build integration

package repository_test

import (
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestReplicationTerminalFailureConcurrentAttempts(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "replication-failure-user", "replication-failure-user", "local")
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewReplicationTerminalFailureRepository(db)
	base := repository.ReplicationTerminalFailure{SourceRegion: "local", Target: "test-target",
		EnvelopeEventID: idgen.NewID(), SnapshotID: idgen.NewID(), TenantID: "tenant-a", AccountID: account.ID,
		Source: "aero-id", Dataset: domain.AccountDataset("aero-vault.buckets"), SnapshotVersion: 1,
		RemoteStatus: 400, ReasonCode: "event.remote_rejected", TerminalCode: "event.remote_rejected",
		FirstFailedAt: time.Now().UTC(), LastFailedAt: time.Now().UTC()}
	const n = 3
	results := make(chan struct {
		failure repository.ReplicationTerminalFailure
		parked  bool
		err     error
	}, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			failure, parked, err := repo.RecordTerminalAttempt(t.Context(), base, n)
			results <- struct {
				failure repository.ReplicationTerminalFailure
				parked  bool
				err     error
			}{failure, parked, err}
		}()
	}
	wg.Wait()
	close(results)
	parked := 0
	for result := range results {
		if result.err != nil {
			t.Errorf("record attempt: %v", result.err)
		}
		if result.parked {
			parked++
		}
	}
	if parked != 1 {
		t.Fatalf("parking transitions = %d, want 1", parked)
	}
	failure, err := repo.GetTerminalFailure(t.Context(), base.SourceRegion, base.Target, base.EnvelopeEventID)
	if err != nil {
		t.Fatal(err)
	}
	if failure.TerminalAttempts != n || failure.State != "parked" {
		t.Fatalf("failure = attempts %d state %q, want %d parked", failure.TerminalAttempts, failure.State, n)
	}
	_, transitioned, err := repo.RecordTerminalAttempt(t.Context(), base, n)
	if err != nil {
		t.Fatal(err)
	}
	if transitioned {
		t.Fatal("already parked row reported a new transition")
	}
	failure, err = repo.GetTerminalFailure(t.Context(), base.SourceRegion, base.Target, base.EnvelopeEventID)
	if err != nil {
		t.Fatal(err)
	}
	if failure.TerminalAttempts != n {
		t.Fatalf("parked row changed to %d attempts", failure.TerminalAttempts)
	}
}
