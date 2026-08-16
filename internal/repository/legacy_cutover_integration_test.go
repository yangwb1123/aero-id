//go:build integration

package repository_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"gorm.io/gorm"
)

const cutoverAuditTestKey = "integration-cutover-audit-key-0123456789abcdef"

func startCutoverAuditRepo(t *testing.T, key string) (*gorm.DB, repository.LegacyCutoverAuditRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: key})
	cutoverRepo := repository.NewLegacyCutoverRepository(db, ledger)
	auditRepo, ok := cutoverRepo.(repository.LegacyCutoverAuditRepository)
	if !ok {
		t.Fatal("cutover repo must implement LegacyCutoverAuditRepository")
	}
	return db, auditRepo
}

func cutoverFact(eventID domain.ID, tenantID string) *domain.AuditFact {
	return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: tenantID + ":cutover",
		EventID: eventID, TenantID: tenantID, ActorUID: "admin-1", Action: "legacy.cutover.set",
		ResourceType: "tenant", ResourceID: tenantID, OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"status": "shadow", "previous": "legacy", "note": "dual read"}}
}

// TestLegacyCutoverSetWithAuditHappyPath (REQ-5): SetCutoverWithAudit commits
// the decision and its fact in one transaction — exactly one cutover row, one
// ledger row and one pending publication row.
func TestLegacyCutoverSetWithAuditHappyPath(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startCutoverAuditRepo(t, cutoverAuditTestKey)
	eventID := idgen.Anchor("legacy.cutover.set", "tenant-a", "shadow", "legacy")
	entry := &repository.LegacyCutover{TenantID: "tenant-a", Status: domain.LegacyCutoverStatus("shadow"),
		DecidedBy: "admin-1", Note: "dual read"}
	if err := auditRepo.SetCutoverWithAudit(ctx, entry, cutoverFact(eventID, "tenant-a")); err != nil {
		t.Fatalf("SetCutoverWithAudit: %v", err)
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, eventID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	var cutoverRows int64
	if err := db.Raw(`SELECT count(*) FROM legacy_cutover WHERE tenant_id = 'tenant-a'`).Scan(&cutoverRows).Error; err != nil {
		t.Fatal(err)
	}
	if cutoverRows != 1 {
		t.Fatalf("cutover rows = %d, want 1", cutoverRows)
	}
}

// TestLegacyCutoverSetWithAuditRollbackWithoutSigningKey (REQ-5 / keyless): a
// keyless ledger fails the append and the whole decision rolls back — the
// pre-existing row stays byte-identical and no ledger/publication rows appear.
func TestLegacyCutoverSetWithAuditRollbackWithoutSigningKey(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startCutoverAuditRepo(t, "")
	plain := repository.NewLegacyCutoverRepository(db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{}))
	entry := &repository.LegacyCutover{TenantID: "tenant-a", Status: domain.LegacyCutoverStatus("shadow"),
		DecidedBy: "admin-1", Note: "dual read"}
	if err := plain.SetCutover(ctx, entry); err != nil {
		t.Fatal(err)
	}
	eventID := idgen.Anchor("legacy.cutover.set", "tenant-a", "cut_over", "shadow")
	if err := auditRepo.SetCutoverWithAudit(ctx, &repository.LegacyCutover{TenantID: "tenant-a",
		Status: domain.LegacyCutoverStatusCutOver, DecidedBy: "admin-2", Note: "moved"},
		cutoverFact(eventID, "tenant-a")); err == nil {
		t.Fatal("SetCutoverWithAudit with a keyless ledger must fail")
	}
	var stored struct {
		Status    string
		DecidedBy string
		Note      string
	}
	if err := db.Raw(`SELECT status, decided_by, note FROM legacy_cutover WHERE tenant_id = 'tenant-a'`).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "shadow" || stored.DecidedBy != "admin-1" || stored.Note != "dual read" {
		t.Fatalf("pre-existing decision changed after failed set: %+v", stored)
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, eventID)
	if ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("rollback left ledger/publication rows = %d/%d, want 0/0", ledgerRows, publicationRows)
	}
}

// TestLegacyCutoverSetWithAuditDuplicateFold (REQ-5): a racing duplicate that
// read the same previous derives the same anchored event_id; the in-tx
// duplicate swallow keeps the retry successful at exactly one ledger row + one
// publication row.
func TestLegacyCutoverSetWithAuditDuplicateFold(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startCutoverAuditRepo(t, cutoverAuditTestKey)
	eventID := idgen.Anchor("legacy.cutover.set", "tenant-a", "shadow", "legacy")
	for i := 0; i < 2; i++ {
		entry := &repository.LegacyCutover{TenantID: "tenant-a", Status: domain.LegacyCutoverStatus("shadow"),
			DecidedBy: "admin-1", Note: "dual read"}
		if err := auditRepo.SetCutoverWithAudit(ctx, entry, cutoverFact(eventID, "tenant-a")); err != nil {
			t.Fatalf("SetCutoverWithAudit %d: %v", i, err)
		}
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, eventID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("ledger/publication rows after duplicate fold = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	var cutoverRows int64
	if err := db.Raw(`SELECT count(*) FROM legacy_cutover WHERE tenant_id = 'tenant-a'`).Scan(&cutoverRows).Error; err != nil {
		t.Fatal(err)
	}
	if cutoverRows != 1 {
		t.Fatalf("cutover rows after duplicate fold = %d, want 1", cutoverRows)
	}
}

// TestLegacyCutoverSetWithAuditPublicationInsertRollsBack (REQ-5 / FM-2):
// with the publication insert failing AFTER the ledger row is staged, the
// whole decision rolls back — zero cutover rows, zero ledger rows, zero
// publication rows. Proves the crash window on the cutover surface, mirroring
// the hold put/remove trigger cases.
func TestLegacyCutoverSetWithAuditPublicationInsertRollsBack(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startCutoverAuditRepo(t, cutoverAuditTestKey)
	installFailPublicationInsertTrigger(t, db)
	eventID := idgen.Anchor("legacy.cutover.set", "tenant-a", "shadow", "legacy")
	entry := &repository.LegacyCutover{TenantID: "tenant-a", Status: domain.LegacyCutoverStatus("shadow"),
		DecidedBy: "admin-1", Note: "dual read"}
	if err := auditRepo.SetCutoverWithAudit(ctx, entry, cutoverFact(eventID, "tenant-a")); err == nil {
		t.Fatal("SetCutoverWithAudit must fail when the publication insert fails")
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, eventID)
	if ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("ledger/publication rows after publication failure = %d/%d, want 0/0 (ledger rolled back with publication)", ledgerRows, publicationRows)
	}
	var cutoverRows int64
	if err := db.Raw(`SELECT count(*) FROM legacy_cutover WHERE tenant_id = 'tenant-a'`).Scan(&cutoverRows).Error; err != nil {
		t.Fatal(err)
	}
	if cutoverRows != 0 {
		t.Fatalf("cutover rows after publication failure = %d, want 0 (decision rolled back)", cutoverRows)
	}
}

// TestLegacyCutoverRegistry exercises the P8-04 registry + P8-05 readiness SQL
// against real PostgreSQL.
func TestLegacyCutoverRegistry(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	cutover := repository.NewLegacyCutoverRepository(db, ledger)

	// No decision => not found => default legacy at the service layer.
	if _, err := cutover.GetCutover(t.Context(), "tenant-a"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("get missing = %v, want not found", err)
	}
	now := time.Now().UTC()
	entry := &repository.LegacyCutover{TenantID: "tenant-a", Status: domain.LegacyCutoverStatus("shadow"),
		DecidedAt: now, DecidedBy: "admin-1", Note: "dual read"}
	if err := cutover.SetCutover(t.Context(), entry); err != nil {
		t.Fatalf("set: %v", err)
	}
	stored, err := cutover.GetCutover(t.Context(), "tenant-a")
	if err != nil || stored.Status != domain.LegacyCutoverStatus("shadow") {
		t.Fatalf("stored = %+v, %v", stored, err)
	}

	// Cleanup readiness against seeded legacy rows.
	accounts := repository.NewAccountRepository(db, ledger)
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
