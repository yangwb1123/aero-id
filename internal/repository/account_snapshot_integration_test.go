//go:build integration

package repository_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestListReplicableSnapshotsIncludesUnavailableForErasePropagation(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-erase", "snaplink", "u-erase", "u-erase", "local")
	if err != nil {
		t.Fatal(err)
	}

	snapshots := repository.NewAccountSnapshotRepository(db)
	if err := snapshots.UpsertSnapshot(t.Context(), &domain.AccountSnapshot{
		AccountID: account.ID, Source: "aero-vault", SourceRegion: "local", Dataset: "aero-vault.buckets",
		Status: domain.SnapshotUnavailable, Version: 2, GeneratedAt: time.Now().UTC(), Data: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	versioned, ok := snapshots.(repository.ReplicationSnapshotRepository)
	if !ok {
		t.Fatal("production snapshot repository must implement update-aware replication")
	}
	items, err := versioned.ListReplicableSnapshotsAfter(t.Context(), "local", time.Time{}, domain.ID{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Snapshot.Status != domain.SnapshotUnavailable {
		t.Fatalf("replicated snapshots = %+v, want one unavailable snapshot", items)
	}
}
