//go:build integration

package repository_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// TestRegionDirectoryPromotionWithFencing exercises the durable fencing
// protocol (P6-02) against real PostgreSQL: promotion bumps the target region
// epoch atomically, the old region is fenced out, and a stale CAS loses.
func TestRegionDirectoryPromotionWithFencing(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	accounts := repository.NewAccountRepository(db)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "u-fence", "u-fence", "local")
	if err != nil {
		t.Fatal(err)
	}
	directory := repository.NewRegionDirectoryRepository(db)

	if err := directory.EnsureDirectory(t.Context(), "tenant-a", account.ID, "local", "v1"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// Idempotent re-ensure never downgrades.
	if err := directory.EnsureDirectory(t.Context(), "tenant-a", account.ID, "eu-west", "v1"); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	entry, err := directory.GetDirectory(t.Context(), "tenant-a", account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.HomeRegion != "local" {
		t.Fatalf("home region = %s, want local (no downgrade)", entry.HomeRegion)
	}

	// Promotion with the observed epoch (0) succeeds; epoch bumps to 1.
	newEpoch, err := directory.PromoteWithFencing(t.Context(), "tenant-a", account.ID, "us-east", 0)
	if err != nil || newEpoch != 1 {
		t.Fatalf("promote = %d, %v; want 1, nil", newEpoch, err)
	}
	entry, err = directory.GetDirectory(t.Context(), "tenant-a", account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.HomeRegion != "us-east" || entry.FencedEpoch != 1 {
		t.Fatalf("after promote: %+v", entry)
	}
	if epoch, err := directory.RegionEpoch(t.Context(), "us-east"); err != nil || epoch != 1 {
		t.Fatalf("us-east epoch = %d, %v; want 1", epoch, err)
	}
	if epoch, err := directory.RegionEpoch(t.Context(), "local"); err != nil || epoch != 0 {
		t.Fatalf("local epoch = %d, %v; want 0 (fenced out)", epoch, err)
	}

	// A stale CAS (expected 0, actual 1) must lose.
	if _, err := directory.PromoteWithFencing(t.Context(), "tenant-a", account.ID, "eu-west", 0); !errors.Is(err, repository.ErrRegionEpochConflict) {
		t.Fatalf("stale promote error = %v, want epoch conflict", err)
	}
	// The current epoch still wins.
	if epoch, err := directory.PromoteWithFencing(t.Context(), "tenant-a", account.ID, "eu-west", 1); err != nil || epoch != 2 {
		t.Fatalf("promote with current epoch = %d, %v; want 2", epoch, err)
	}

	// Tenant isolation: another tenant cannot see or promote this account.
	if _, err := directory.GetDirectory(t.Context(), "tenant-b", account.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("cross-tenant directory read error = %v, want not found", err)
	}
	if _, err := directory.PromoteWithFencing(t.Context(), "tenant-b", account.ID, "us-west", 1); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("cross-tenant promote error = %v, want not found", err)
	}
}
