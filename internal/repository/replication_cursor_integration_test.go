//go:build integration

package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository/testpg"
)

func TestReplicationCursorLeaseFencesWorkers(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, testpg.RepoRoot(t)+"/migrations")
	repo := NewReplicationCursorRepository(db)
	ctx := t.Context()
	sourceRegion := "us-west"
	target := "nats:aero.events.canonical"
	lease := time.Minute
	leaseRepo, ok := repo.(ReplicationCursorLeaseRepository)
	if !ok {
		t.Fatal("production replication cursor repository must implement lease fencing")
	}

	claimed, err := leaseRepo.ClaimCursor(ctx, sourceRegion, target, "worker-a", lease)
	if err != nil || !claimed {
		t.Fatalf("worker-a claim = %v, %v; want true, nil", claimed, err)
	}
	claimed, err = leaseRepo.ClaimCursor(ctx, sourceRegion, target, "worker-b", lease)
	if err != nil || claimed {
		t.Fatalf("worker-b concurrent claim = %v, %v; want false, nil", claimed, err)
	}

	snapshotID := idgen.NewID()
	if err := leaseRepo.SaveCursorOwned(ctx, sourceRegion, target, "worker-b", lease, snapshotID, 1); !errors.Is(err, ErrReplicationCursorLeaseLost) {
		t.Fatalf("stale worker save = %v, want ErrReplicationCursorLeaseLost", err)
	}
	if err := leaseRepo.SaveCursorOwned(ctx, sourceRegion, target, "worker-a", lease, snapshotID, 1); err != nil {
		t.Fatalf("owner save: %v", err)
	}
	lastID, published, err := repo.GetCursor(ctx, sourceRegion, target)
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	if lastID != snapshotID || published != 1 {
		t.Fatalf("cursor = %s/%d, want %s/1", lastID, published, snapshotID)
	}
	if err := leaseRepo.ReleaseCursor(ctx, sourceRegion, target, "worker-a"); err != nil {
		t.Fatalf("release owner: %v", err)
	}
	claimed, err = leaseRepo.ClaimCursor(ctx, sourceRegion, target, "worker-b", lease)
	if err != nil || !claimed {
		t.Fatalf("worker-b claim after release = %v, %v; want true, nil", claimed, err)
	}
}
