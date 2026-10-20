//go:build integration

package auditintegration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

const leaseFencingTestKey = "lease-fencing-test-key-0123456789abcdef"

// TestCallbackDeliveryLeaseFencingRejectsExpiredAndReclaimedFinishes verifies
// that callback settlement requires both a live lease and the claim generation.
// The same worker ID is deliberately reused to cover the reclaim ambiguity.
func TestCallbackDeliveryLeaseFencingRejectsExpiredAndReclaimedFinishes(t *testing.T) {
	db, callbacks, _ := startCallbackAuditRepo(t, leaseFencingTestKey)
	ctx := context.Background()
	sub := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: "https://callback.example.test/hook", EventTypes: []string{}, Enabled: true, Verified: true}
	if err := callbacks.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	delivery := &domain.CallbackDelivery{ID: idgen.NewID(), SubscriptionID: sub.ID, EventID: idgen.NewID(),
		Action: "test.lease_fence", Payload: map[string]any{}}
	if err := callbacks.EnqueueDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	first, err := callbacks.FetchDueDeliveries(ctx, 1, "same-worker", time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %d, %v; want 1", len(first), err)
	}
	if first[0].LeaseGeneration == 0 {
		t.Fatal("first claim has zero lease generation")
	}
	if err := db.Exec("UPDATE callback_deliveries SET lease_until = now() - interval '1 second' WHERE id = ?", first[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := callbacks.MarkDelivery(ctx, first[0].ID, "same-worker", first[0].LeaseGeneration, "success", 200, "", "", time.Now().UTC()); !errors.Is(err, repository.ErrCallbackDeliveryLeaseLost) {
		t.Fatalf("expired mark = %v, want lease lost", err)
	}
	second, err := callbacks.FetchDueDeliveries(ctx, 1, "same-worker", time.Minute)
	if err != nil || len(second) != 1 {
		t.Fatalf("same-worker reclaim = %d, %v; want 1", len(second), err)
	}
	if second[0].LeaseGeneration <= first[0].LeaseGeneration {
		t.Fatalf("lease generation = %d, want > %d", second[0].LeaseGeneration, first[0].LeaseGeneration)
	}
	if err := callbacks.MarkDelivery(ctx, second[0].ID, "same-worker", first[0].LeaseGeneration, "success", 200, "", "", time.Now().UTC()); !errors.Is(err, repository.ErrCallbackDeliveryLeaseLost) {
		t.Fatalf("stale reclaimed mark = %v, want lease lost", err)
	}
	if err := callbacks.MarkDelivery(ctx, second[0].ID, "same-worker", second[0].LeaseGeneration, "success", 200, "", "", time.Now().UTC()); err != nil {
		t.Fatalf("current mark: %v", err)
	}
	var attempt int
	if err := db.Raw("SELECT attempt FROM callback_deliveries WHERE id = ?", second[0].ID).Scan(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	if attempt != 1 {
		t.Fatalf("callback attempt after committed settlement = %d, want 1", attempt)
	}
}
