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

func TestReplicationChangesSurviveTiesRollbackAndDeletion(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-changes", "snaplink", "changes-user", "changes-user", "local")
	if err != nil {
		t.Fatal(err)
	}
	snapshots := repository.NewAccountSnapshotRepository(db)
	changeLog, ok := snapshots.(repository.ReplicationChangeRepository)
	if !ok {
		t.Fatal("snapshot repository does not expose the change log")
	}
	deletions, ok := snapshots.(repository.ReplicationSnapshotDeletionRepository)
	if !ok {
		t.Fatal("snapshot repository does not expose tombstones")
	}

	snapshot := &domain.AccountSnapshot{ID: idgen.NewID(), AccountID: account.ID, Source: "aero-vault",
		SourceRegion: "local", Dataset: "aero-vault.buckets", Status: domain.SnapshotFresh, Version: 7,
		GeneratedAt: time.Now().UTC(), Data: map[string]any{"same": true}}
	if err := snapshots.UpsertSnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := snapshots.UpsertSnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	first, err := changeLog.ListReplicationChangesAfter(t.Context(), "local", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[1].Revision <= first[0].Revision {
		t.Fatalf("changes = %v, want two strictly ordered same-version revisions", first)
	}
	if err := deletions.DeleteSnapshot(t.Context(), account.ID, "aero-vault", "aero-vault.buckets"); err != nil {
		t.Fatal(err)
	}
	second, err := changeLog.ListReplicationChangesAfter(t.Context(), "local", first[1].Revision, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || !second[0].Deleted || second[0].Revision <= first[1].Revision {
		t.Fatalf("deletion change = %v, want ordered tombstone", second)
	}
}
