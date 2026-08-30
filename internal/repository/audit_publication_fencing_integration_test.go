//go:build integration

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

// TestAuditPublicationLeaseFencesExpiredAndReclaimedSameWorker verifies both
// sides of the lease fence: an expired claim cannot settle before reclaim, and
// a stale claim cannot settle after the same worker ID reclaims the row.
func TestAuditPublicationLeaseFencesExpiredAndReclaimedSameWorker(t *testing.T) {
	ctx := context.Background()
	db, ledger := startAuditLedger(t, auditLedgerTestKey)
	publications, ok := ledger.(AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "same-worker-fence",
		TenantID: "tenant-a", EventID: idgen.NewID(), Action: "test.same-worker-fence", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
	if err := ledger.Append(ctx, fact); err != nil {
		t.Fatal(err)
	}
	first, err := publications.ClaimPublication(ctx, "worker-1", time.Minute, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %d items, err %v; want one", len(first), err)
	}
	original := first[0]
	if original.LeaseGeneration == 0 {
		t.Fatal("first claim generation = 0, want a fencing generation")
	}
	if err := db.Exec(`UPDATE audit_publications SET lease_until = now() - interval '1 minute' WHERE id = ?`, original.ID).Error; err != nil {
		t.Fatal(err)
	}

	if err := publications.FinishPublication(ctx, original.ID, "worker-1", original.LeaseGeneration, 1,
		"completed", "", time.Now().UTC()); !errors.Is(err, ErrAuditPublicationLeaseLost) {
		t.Fatalf("finish after expiry before reclaim = %v, want ErrAuditPublicationLeaseLost", err)
	}
	second, err := publications.ClaimPublication(ctx, "worker-1", time.Minute, 10)
	if err != nil || len(second) != 1 {
		t.Fatalf("same-worker reclaim = %d items, err %v; want one", len(second), err)
	}
	current := second[0]
	if current.LeaseGeneration <= original.LeaseGeneration {
		t.Fatalf("reclaim generation = %d, want greater than stale %d", current.LeaseGeneration, original.LeaseGeneration)
	}
	if err := publications.FinishPublication(ctx, original.ID, "worker-1", original.LeaseGeneration, 1,
		"completed", "", time.Now().UTC()); !errors.Is(err, ErrAuditPublicationLeaseLost) {
		t.Fatalf("stale same-worker finish = %v, want ErrAuditPublicationLeaseLost", err)
	}
	if err := publications.FinishPublication(ctx, current.ID, "worker-1", current.LeaseGeneration, 1,
		"completed", "", time.Now().UTC()); err != nil {
		t.Fatalf("current same-worker finish: %v", err)
	}
}
