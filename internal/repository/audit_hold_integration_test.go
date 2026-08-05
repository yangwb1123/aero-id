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
)

// TestLegalHoldExemptsActorsFromPurge (P4-07): actors under an active hold keep
// their legacy audit rows across a purge; the purge respects expiry.
func TestLegalHoldExemptsActorsFromPurge(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	audit := repository.NewAuditRepository(db)
	holds := repository.NewAuditHoldRepository(db)
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
