//go:build integration

package repository_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"

	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"gorm.io/gorm"
)

// TestLegalHoldExemptsActorsFromPurge (P4-07): actors under an active hold keep
// their legacy audit rows across a purge; the purge respects expiry.
func TestLegalHoldExemptsActorsFromPurge(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	audit := repository.NewAuditRepository(db)
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	holds := repository.NewAuditHoldRepository(db, ledger)
	old := time.Now().UTC().Add(-200 * 24 * time.Hour)

	seedLegacyRow(t, db, "INSERT INTO audit_events (actor_uid, action, subject_type, subject_id, created_at) VALUES ('u-held', 'x', 'file', 'f1', $1)", old)
	seedLegacyRow(t, db, "INSERT INTO audit_events (actor_uid, action, subject_type, subject_id, created_at) VALUES ('u-free', 'x', 'file', 'f2', $1)", old)

	// Hold u-held, purge, verify only u-free rows were removed.
	if err := holds.PutHold(t.Context(), &repository.AuditHold{TenantID: "tenant-a",
		ScopeType: repository.AuditHoldScopeActor, ScopeID: "u-held", CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	exempt, err := holds.HoldScopeIDs(t.Context(), repository.AuditHoldScopeActor)
	if err != nil || len(exempt) != 1 || exempt[0] != "u-held" {
		t.Fatalf("exempt = %v, %v", exempt, err)
	}
	purger, ok := audit.(interface {
		PurgeOlderThanExcluding(context.Context, time.Time, int, []string) (int64, error)
	})
	if !ok {
		t.Fatal("AuditRepository must support exemption-aware purge")
	}
	deleted, err := purger.PurgeOlderThanExcluding(t.Context(), time.Now().UTC(), 0, exempt)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (only the free actor's row)", deleted)
	}
	var remaining int64
	db.Raw("SELECT count(*) FROM audit_events WHERE actor_uid = 'u-held'").Scan(&remaining)
	if remaining != 1 {
		t.Errorf("held actor rows = %d, want 1 (legal hold exemption)", remaining)
	}

	// Lifting the hold makes the rows eligible again.
	if err := holds.RemoveHold(t.Context(), "tenant-a", repository.AuditHoldScopeActor, "u-held"); err != nil {
		t.Fatal(err)
	}
	exempt, _ = holds.HoldScopeIDs(t.Context(), repository.AuditHoldScopeActor)
	deleted, err = purger.PurgeOlderThanExcluding(t.Context(), time.Now().UTC(), 0, exempt)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("deleted after lifting hold = %d, want 1", deleted)
	}
}

const auditHoldTestKey = "integration-audit-hold-key-0123456789abcdef"

func startAuditHoldAuditRepo(t *testing.T, key string) (*gorm.DB, repository.AuditHoldAuditRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: key})
	holdRepo := repository.NewAuditHoldRepository(db, ledger)
	auditRepo, ok := holdRepo.(repository.AuditHoldAuditRepository)
	if !ok {
		t.Fatal("hold repo must implement AuditHoldAuditRepository")
	}
	return db, auditRepo
}

func holdFact(eventID domain.ID, tenantID, action string) *domain.AuditFact {
	return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: tenantID + ":hold",
		EventID: eventID, TenantID: tenantID, ActorUID: "admin-1", Action: action,
		ResourceType: "audit_hold", ResourceID: "acct-1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"reason": "evidence order"}}
}

func countAuditHoldRows(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM audit_holds WHERE tenant_id = 'tenant-a' AND scope_type = 'account' AND scope_id = 'acct-1'`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestAuditHoldPutWithAuditHappyPath (REQ-4): PutHoldWithAudit commits the
// hold and its fact in one transaction — exactly one hold row, one ledger row
// and one pending publication row, with the RETURNING copy-back intact.
func TestAuditHoldPutWithAuditHappyPath(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startAuditHoldAuditRepo(t, auditHoldTestKey)
	eventID := idgen.Anchor("audit_hold.set", "tenant-a", "account", "acct-1", "evidence order", "")
	hold := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
		ScopeID: "acct-1", Reason: "evidence order", CreatedBy: "admin-1"}
	if err := auditRepo.PutHoldWithAudit(ctx, hold, holdFact(eventID, "tenant-a", "audit_hold.set")); err != nil {
		t.Fatalf("PutHoldWithAudit: %v", err)
	}
	if hold.ID == (domain.ID{}) || hold.CreatedAt.IsZero() {
		t.Fatalf("hold must carry RETURNING copy-back, got %+v", hold)
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, eventID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	if rows := countAuditHoldRows(t, db); rows != 1 {
		t.Fatalf("hold rows = %d, want 1", rows)
	}
}

// TestAuditHoldPutWithAuditRollbackWithoutSigningKey (REQ-4 / keyless): a
// ledger without a signing key fails the append inside the transaction and
// the whole mutation rolls back — zero hold rows, zero ledger rows, zero
// publication rows. Nothing commits without its durable fact.
func TestAuditHoldPutWithAuditRollbackWithoutSigningKey(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startAuditHoldAuditRepo(t, "")
	eventID := idgen.Anchor("audit_hold.set", "tenant-a", "account", "acct-1", "evidence order", "")
	hold := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
		ScopeID: "acct-1", Reason: "evidence order", CreatedBy: "admin-1"}
	if err := auditRepo.PutHoldWithAudit(ctx, hold, holdFact(eventID, "tenant-a", "audit_hold.set")); err == nil {
		t.Fatal("PutHoldWithAudit with a keyless ledger must fail")
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, eventID)
	if ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("rollback left ledger/publication rows = %d/%d, want 0/0", ledgerRows, publicationRows)
	}
	if rows := countAuditHoldRows(t, db); rows != 0 {
		t.Fatalf("rollback left hold rows = %d, want 0", rows)
	}
}

// TestAuditHoldPutWithAuditDuplicateFold (REQ-4): a crash-retry re-derives
// the same anchored event_id; the in-tx duplicate swallow keeps the retry
// successful and the ledger at exactly one row + one publication.
func TestAuditHoldPutWithAuditDuplicateFold(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startAuditHoldAuditRepo(t, auditHoldTestKey)
	eventID := idgen.Anchor("audit_hold.set", "tenant-a", "account", "acct-1", "evidence order", "")
	for i := 0; i < 2; i++ {
		hold := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
			ScopeID: "acct-1", Reason: "evidence order", CreatedBy: "admin-1"}
		if err := auditRepo.PutHoldWithAudit(ctx, hold, holdFact(eventID, "tenant-a", "audit_hold.set")); err != nil {
			t.Fatalf("PutHoldWithAudit %d: %v", i, err)
		}
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, eventID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("ledger/publication rows after duplicate fold = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	if rows := countAuditHoldRows(t, db); rows != 1 {
		t.Fatalf("hold rows after duplicate fold = %d, want 1", rows)
	}
}

// TestAuditHoldRemoveWithAuditHappyPath (REQ-4): RemoveHoldWithAudit deletes
// the hold and appends audit_hold.removed in one transaction.
func TestAuditHoldRemoveWithAuditHappyPath(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startAuditHoldAuditRepo(t, auditHoldTestKey)
	hold := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
		ScopeID: "acct-1", Reason: "evidence order", CreatedBy: "admin-1"}
	setEvent := idgen.Anchor("audit_hold.set", "tenant-a", "account", "acct-1", "evidence order", "")
	if err := auditRepo.PutHoldWithAudit(ctx, hold, holdFact(setEvent, "tenant-a", "audit_hold.set")); err != nil {
		t.Fatal(err)
	}
	removedEvent := idgen.Anchor("audit_hold.removed", "tenant-a", "account", "acct-1")
	if err := auditRepo.RemoveHoldWithAudit(ctx, "tenant-a", repository.AuditHoldScopeAccount, "acct-1", holdFact(removedEvent, "tenant-a", "audit_hold.removed")); err != nil {
		t.Fatalf("RemoveHoldWithAudit: %v", err)
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, removedEvent)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("removed ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	if rows := countAuditHoldRows(t, db); rows != 0 {
		t.Fatalf("hold rows after remove = %d, want 0", rows)
	}
}

// TestAuditHoldRemoveWithAuditRollbackWithoutSigningKey (REQ-4 / keyless): a
// failed append rolls the DELETE back too — the pre-existing hold survives
// byte-identical (same expires_at, reason, created_by) and no ledger or
// publication rows appear.
func TestAuditHoldRemoveWithAuditRollbackWithoutSigningKey(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startAuditHoldAuditRepo(t, "")
	expiry := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Microsecond)
	plain := repository.NewAuditHoldRepository(db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{}))
	hold := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
		ScopeID: "acct-1", Reason: "evidence order", CreatedBy: "admin-1", ExpiresAt: &expiry}
	if err := plain.PutHold(ctx, hold); err != nil {
		t.Fatal(err)
	}
	removedEvent := idgen.Anchor("audit_hold.removed", "tenant-a", "account", "acct-1")
	if err := auditRepo.RemoveHoldWithAudit(ctx, "tenant-a", repository.AuditHoldScopeAccount, "acct-1", holdFact(removedEvent, "tenant-a", "audit_hold.removed")); err == nil {
		t.Fatal("RemoveHoldWithAudit with a keyless ledger must fail")
	}
	var stored struct {
		Reason    string
		CreatedBy string
		ExpiresAt *time.Time
	}
	if err := db.Raw(`SELECT reason, created_by, expires_at FROM audit_holds WHERE tenant_id = 'tenant-a' AND scope_type = 'account' AND scope_id = 'acct-1'`).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Reason != "evidence order" || stored.CreatedBy != "admin-1" || stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(expiry) {
		t.Fatalf("pre-existing hold changed after failed remove: %+v", stored)
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, removedEvent)
	if ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("rollback left ledger/publication rows = %d/%d, want 0/0", ledgerRows, publicationRows)
	}
}

// TestAuditHoldPutWithAuditPublicationInsertRollsBack (REQ-4 / FM-2): with
// the publication insert failing AFTER the ledger row is staged, the whole
// mutation rolls back — zero hold rows, zero ledger rows, zero publication
// rows. The keyless case only proves the pre-insert guard; this trigger case
// is the one that exercises the crash window for the put surface.
func TestAuditHoldPutWithAuditPublicationInsertRollsBack(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startAuditHoldAuditRepo(t, auditHoldTestKey)
	installFailPublicationInsertTrigger(t, db)
	eventID := idgen.Anchor("audit_hold.set", "tenant-a", "account", "acct-1", "evidence order", "")
	hold := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
		ScopeID: "acct-1", Reason: "evidence order", CreatedBy: "admin-1"}
	if err := auditRepo.PutHoldWithAudit(ctx, hold, holdFact(eventID, "tenant-a", "audit_hold.set")); err == nil {
		t.Fatal("PutHoldWithAudit must fail when the publication insert fails")
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, eventID)
	if ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("ledger/publication rows after publication failure = %d/%d, want 0/0 (ledger rolled back with publication)", ledgerRows, publicationRows)
	}
	if rows := countAuditHoldRows(t, db); rows != 0 {
		t.Fatalf("hold rows after publication failure = %d, want 0 (mutation rolled back)", rows)
	}
}

// TestAuditHoldRemoveWithAuditPublicationInsertRollsBack (REQ-4 / FM-2):
// RemoveHoldWithAudit with a pre-existing hold under a failing publication
// insert — the DELETE rolls back with the append and the pre-existing row
// survives byte-identical across every column, with no ledger or publication
// rows left behind.
func TestAuditHoldRemoveWithAuditPublicationInsertRollsBack(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startAuditHoldAuditRepo(t, auditHoldTestKey)
	expiry := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Microsecond)
	plain := repository.NewAuditHoldRepository(db, repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{}))
	hold := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
		ScopeID: "acct-1", Reason: "evidence order", CreatedBy: "admin-1", ExpiresAt: &expiry}
	if err := plain.PutHold(ctx, hold); err != nil {
		t.Fatal(err)
	}
	installFailPublicationInsertTrigger(t, db)
	removedEvent := idgen.Anchor("audit_hold.removed", "tenant-a", "account", "acct-1")
	if err := auditRepo.RemoveHoldWithAudit(ctx, "tenant-a", repository.AuditHoldScopeAccount, "acct-1", holdFact(removedEvent, "tenant-a", "audit_hold.removed")); err == nil {
		t.Fatal("RemoveHoldWithAudit must fail when the publication insert fails")
	}
	var stored struct {
		ID        string
		TenantID  string
		ScopeType string
		ScopeID   string
		Reason    string
		CreatedBy string
		CreatedAt time.Time
		ExpiresAt *time.Time
	}
	if err := db.Raw(`SELECT id::text, tenant_id, scope_type, scope_id, reason, created_by, created_at, expires_at
		FROM audit_holds WHERE tenant_id = 'tenant-a' AND scope_type = 'account' AND scope_id = 'acct-1'`).Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ID != hold.ID.String() || stored.TenantID != "tenant-a" || stored.ScopeType != "account" || stored.ScopeID != "acct-1" ||
		stored.Reason != "evidence order" || stored.CreatedBy != "admin-1" || !stored.CreatedAt.Equal(hold.CreatedAt) ||
		stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(expiry) {
		t.Fatalf("pre-existing hold changed after failed remove: %+v", stored)
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, removedEvent)
	if ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("ledger/publication rows after publication failure = %d/%d, want 0/0", ledgerRows, publicationRows)
	}
}

// TestAuditHoldRemoveWithAuditDuplicateFold (REQ-4.4 / REQ-6.3): Remove is the
// only surface that records its fact even when 0 rows were deleted; a second
// RemoveHoldWithAudit for an already-lifted hold re-derives the same anchored
// event_id, the in-tx duplicate swallow keeps the retry successful, and the
// ledger stays at exactly one row + one publication with the hold still
// absent — the 9th REQ-6 case (the remove duplicate fold).
func TestAuditHoldRemoveWithAuditDuplicateFold(t *testing.T) {
	ctx := context.Background()
	db, auditRepo := startAuditHoldAuditRepo(t, auditHoldTestKey)
	setEvent := idgen.Anchor("audit_hold.set", "tenant-a", "account", "acct-1", "evidence order", "")
	hold := &repository.AuditHold{TenantID: "tenant-a", ScopeType: repository.AuditHoldScopeAccount,
		ScopeID: "acct-1", Reason: "evidence order", CreatedBy: "admin-1"}
	if err := auditRepo.PutHoldWithAudit(ctx, hold, holdFact(setEvent, "tenant-a", "audit_hold.set")); err != nil {
		t.Fatal(err)
	}
	removedEvent := idgen.Anchor("audit_hold.removed", "tenant-a", "account", "acct-1")
	for i := 0; i < 2; i++ {
		if err := auditRepo.RemoveHoldWithAudit(ctx, "tenant-a", repository.AuditHoldScopeAccount, "acct-1", holdFact(removedEvent, "tenant-a", "audit_hold.removed")); err != nil {
			t.Fatalf("RemoveHoldWithAudit %d: %v", i, err)
		}
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, removedEvent)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("ledger/publication rows after remove duplicate fold = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	if rows := countAuditHoldRows(t, db); rows != 0 {
		t.Fatalf("hold rows after remove duplicate fold = %d, want 0 (hold stays absent)", rows)
	}
}

// TestAuditChainAnchorInputs verifies the P4-03 anchor inputs against a real
// ledger: partitions are enumerated and the chain head carries the newest
// sequence with a chained record hash.
func TestAuditChainAnchorInputs(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "test-key"})
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		fact := &domain.AuditFact{ID: idgen.NewID(), Source: "aero-vault", SourceRegion: "us-west",
			PartitionKey: "tenant-a", TenantID: "tenant-a", Action: "vault.object.created",
			ResourceType: "object", ResourceID: "obj-1", OccurredAt: now.Add(time.Duration(i) * time.Second),
			Payload: map[string]any{"dataset": "aero-vault.buckets", "version": i + 1}}
		if err := ledger.Append(t.Context(), fact); err != nil {
			t.Fatal(err)
		}
	}
	partitions, err := ledger.ChainPartitions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(partitions) != 1 || partitions[0].Source != "aero-vault" || partitions[0].Partition != "tenant-a" {
		t.Fatalf("partitions = %+v", partitions)
	}
	head, err := ledger.ChainHead(t.Context(), "aero-vault", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if head.ChainSequence != 3 {
		t.Errorf("head sequence = %d, want 3", head.ChainSequence)
	}
	if head.RecordHash == "" || head.PreviousHash == "" || head.Signature == "" {
		t.Errorf("head must carry hash chain and signature: %+v", head)
	}
	if err := ledger.VerifyChain(t.Context(), "aero-vault", "tenant-a"); err != nil {
		t.Fatalf("chain verification: %v", err)
	}
}
