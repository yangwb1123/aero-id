//go:build integration

package repository_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// TestLegacyCutoverRegistry exercises the P8-04 registry + P8-05 readiness SQL
// against real PostgreSQL.
func TestLegacyCutoverRegistry(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	cutover := repository.NewLegacyCutoverRepository(db)

	// No decision => not found => default legacy at the service layer.
	if _, err := cutover.GetCutover(t.Context(), "tenant-a"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("get missing = %v, want not found", err)
	}
	now := time.Now().UTC()
	entry := &repository.LegacyCutover{TenantID: "tenant-a", Status: repository.LegacyCutoverStatusShadow,
		DecidedAt: now, DecidedBy: "admin-1", Note: "dual read"}
	if err := cutover.SetCutover(t.Context(), entry); err != nil {
		t.Fatalf("set: %v", err)
	}
	stored, err := cutover.GetCutover(t.Context(), "tenant-a")
	if err != nil || stored.Status != repository.LegacyCutoverStatusShadow {
		t.Fatalf("stored = %+v, %v", stored, err)
	}

	// Cleanup readiness against seeded legacy rows.
	accounts := repository.NewAccountRepository(db)
	account, err := accounts.EnsureByIdentity(t.Context(), "tenant-a", "snaplink", "u-clean", "u-clean", "local")
	if err != nil {
		t.Fatal(err)
	}
	seedLegacyRow(t, db, "INSERT INTO files (id, owner_uid, name, size, mime_type, storage_key, storage_type, created_at, updated_at) VALUES (gen_random_uuid(), $1, 'x.txt', 1, 'text/plain', 'k', 'local', $2, $2)", account.CanonicalUID, now)

	cleanup := repository.NewLegacyCleanupRepository(db)
	items, err := cleanup.CleanupReadiness(t.Context(), "tenant-a")
	if err != nil {
		t.Fatalf("readiness: %v", err)
	}
	byName := map[string]repository.CleanupReadinessItem{}
	for _, item := range items {
		byName[item.Check] = item
	}
	if item, ok := byName["legacy_rows_drained"]; !ok || item.Ready {
		t.Errorf("legacy rows must block readiness: %+v", item)
	}
	if _, ok := byName["rollback_window_elapsed"]; !ok {
		t.Errorf("rollback window item missing: %+v", items)
	}
	// Audit facts exist for this tenant? none seeded; the check reports count.
	if _, ok := byName["audit_retained"]; !ok {
		t.Errorf("audit check missing: %+v", items)
	}
}
