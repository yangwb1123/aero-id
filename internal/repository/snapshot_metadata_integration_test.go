//go:build integration

package repository_test

import (
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func TestSnapshotMetadataScopeOmitsContentAndForeignAccounts(t *testing.T) {
	db, account, _, _ := newEventContentHarness(t)
	repo := repository.NewAccountSnapshotRepository(db)
	snapshot := summarySnapshot(account.ID, datasetTwo, 7, "fresh", "private content")
	if err := repo.UpsertSnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	metadata := repo.(repository.SnapshotMetadataRepository)
	values, err := metadata.ListSnapshotMetadata(t.Context(), account.ID, []domain.AccountDataset{datasetTwo})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].Version != 7 || len(values[0].Data) != 0 {
		t.Fatalf("metadata = %+v", values)
	}
	values, err = metadata.ListSnapshotMetadata(t.Context(), idgen.NewID(), []domain.AccountDataset{datasetTwo})
	if err != nil || len(values) != 0 {
		t.Fatalf("foreign scope = %+v, %v", values, err)
	}
	for _, scope := range [][]domain.AccountDataset{nil, {"unregistered.dataset"}} {
		if _, err := metadata.ListSnapshotMetadata(t.Context(), account.ID, scope); err == nil {
			t.Fatal("invalid metadata scope accepted")
		}
	}
}
