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
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "u-fence", "u-fence", "local")
	if err != nil {
		t.Fatal(err)
	}
	directory := repository.NewRegionDirectoryRepository(db, ledger)

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
	account, err = accounts.GetByID(t.Context(), "tenant-a", account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if account.HomeRegion != "us-east" {
		t.Fatalf("account routing region = %s, want us-east", account.HomeRegion)
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
	account, err = accounts.GetByID(t.Context(), "tenant-a", account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if account.HomeRegion != "eu-west" {
		t.Fatalf("account routing region after second promotion = %s, want eu-west", account.HomeRegion)
	}
}

// TestRegionDirectoryRegionEpochsNeverDowngrade: region epoch rows are shared
// by every account, so a low-epoch account's promotion (or a brand-new
// account) must never lower a region's epoch: downgrades fence out legitimate
// current writers and can re-admit an old fenced-out generation.
func TestRegionDirectoryRegionEpochsNeverDowngrade(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	accounts := repository.NewAccountRepository(db, ledger)
	directory := repository.NewRegionDirectoryRepository(db, ledger)

	first, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "u-first", "u-first", "local")
	if err != nil {
		t.Fatal(err)
	}
	second, err := accounts.EnsureByIdentity(t.Context(), "tenant-b", "snaplink", "u-second", "u-second", "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.EnsureDirectory(t.Context(), "tenant-a", first.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	// The first account promotes three times: us-east row reaches 3.
	if _, err := directory.PromoteWithFencing(t.Context(), "tenant-a", first.ID, "us-east", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.PromoteWithFencing(t.Context(), "tenant-a", first.ID, "local", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.PromoteWithFencing(t.Context(), "tenant-a", first.ID, "us-east", 2); err != nil {
		t.Fatal(err)
	}
	if epoch, err := directory.RegionEpoch(t.Context(), "us-east"); err != nil || epoch != 3 {
		t.Fatalf("us-east epoch = %d, %v; want 3", epoch, err)
	}

	// A second, low-epoch (0) account promoting into us-east must bump the
	// shared row (max(3, 0)+1 = 4), never lower it (old code wrote 1).
	if err := directory.EnsureDirectory(t.Context(), "tenant-b", second.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	newEpoch, err := directory.PromoteWithFencing(t.Context(), "tenant-b", second.ID, "us-east", 0)
	if err != nil {
		t.Fatal(err)
	}
	if newEpoch != 4 {
		t.Fatalf("second promote epoch = %d, want 4 (max(3,0)+1)", newEpoch)
	}
	if epoch, err := directory.RegionEpoch(t.Context(), "us-east"); err != nil || epoch != 4 {
		t.Fatalf("us-east epoch = %d, %v; want 4 (never downgraded)", epoch, err)
	}

	// Creating a brand-new account in "local" must not reset the shared row
	// (old code overwrote it with 0).
	third, err := accounts.EnsureByIdentity(t.Context(), "tenant-c", "snaplink", "u-third", "u-third", "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.EnsureDirectory(t.Context(), "tenant-c", third.ID, "local", "v1"); err != nil {
		t.Fatal(err)
	}
	if epoch, err := directory.RegionEpoch(t.Context(), "local"); err != nil || epoch != 2 {
		t.Fatalf("local epoch after new account = %d, %v; want 2 (not reset to 0)", epoch, err)
	}
}
