//go:build integration

package repository_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

func TestCallbackDeliveryDeferralKeepsAttemptAndDedupReceipt(t *testing.T) {
	db, repo, auditRepo := startCallbackAuditRepo(t, callbackAuditTestKey)
	ctx := t.Context()
	subID, eventID := idgen.NewID(), idgen.NewID()
	sub := &domain.CallbackSubscription{ID: subID, OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: "https://callback.example/hook", EventTypes: []string{}, SecretCiphertext: "fixture", Enabled: true, Verified: true}
	if err := auditRepo.CreateSubscriptionWithAudit(ctx, sub, callbackFact(subID, "tenant-a", "callback.registered")); err != nil {
		t.Fatal(err)
	}
	delivery := &domain.CallbackDelivery{SubscriptionID: subID, EventID: eventID, Attempt: 7,
		Action: "account.updated", Payload: map[string]any{"schema_version": 1}}
	if err := repo.EnqueueDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.FetchDueDeliveries(ctx, 1, "worker-1", time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	deferrer := repo.(repository.CallbackDeliveryDeferrer)
	if err := deferrer.DeferDelivery(ctx, delivery.ID, "worker-1", claimed[0].LeaseGeneration,
		"callback.secret_unavailable", 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "pending" || after.Attempt != 7 || after.LeaseOwner != "" || after.LeaseUntil != nil ||
		after.ErrorCode != "callback.secret_unavailable" || !after.NextAttemptAt.After(time.Now().Add(4*time.Minute)) {
		t.Fatalf("delivery after local deferral has incorrect state: %+v", after)
	}
	if after.EventID != eventID || after.SubscriptionID != subID || after.Payload["schema_version"] != float64(1) {
		t.Fatal("deferral changed the durable delivery fact")
	}
	if err := repo.EnqueueDelivery(ctx, &domain.CallbackDelivery{SubscriptionID: subID, EventID: eventID,
		Action: "account.updated", Payload: delivery.Payload}); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := db.Table("callback_deliveries").Where("subscription_id = ? AND event_id = ?", subID, eventID).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("dedup receipt rows = %d, want 1", rows)
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, subID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("deferral changed audit/publication rows: %d/%d", ledgerRows, publicationRows)
	}
	if err := deferrer.DeferDelivery(ctx, delivery.ID, "worker-1", claimed[0].LeaseGeneration,
		"callback.secret_unavailable", time.Minute); !errors.Is(err, repository.ErrCallbackDeliveryLeaseLost) {
		t.Fatalf("second settlement with old claim = %v, want lost lease", err)
	}
}

func TestCallbackDeliveryDeferralFencesExpiredAndReclaimedLeases(t *testing.T) {
	db, repo, _ := startCallbackAuditRepo(t, callbackAuditTestKey)
	ctx := t.Context()
	sub := &domain.CallbackSubscription{OwnerUID: "u-1", TenantID: "tenant-a", Endpoint: "https://callback.example/hook",
		EventTypes: []string{}, SecretCiphertext: "fixture", Enabled: true, Verified: true}
	if err := repo.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	delivery := &domain.CallbackDelivery{SubscriptionID: sub.ID, EventID: idgen.NewID(), Attempt: 7,
		Action: "account.updated", Payload: map[string]any{}}
	if err := repo.EnqueueDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	first, err := repo.FetchDueDeliveries(ctx, 1, "same-worker", time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim = %d, %v", len(first), err)
	}
	expire := func() {
		t.Helper()
		if err := db.Exec("UPDATE callback_deliveries SET lease_until = clock_timestamp() - INTERVAL '1 second' WHERE id = ?", delivery.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	deferrer := repo.(repository.CallbackDeliveryDeferrer)
	expire()
	if err := deferrer.DeferDelivery(ctx, delivery.ID, "same-worker", first[0].LeaseGeneration,
		"callback.secret_unavailable", time.Minute); !errors.Is(err, repository.ErrCallbackDeliveryLeaseLost) {
		t.Fatalf("expired lease deferral = %v", err)
	}
	second, err := repo.FetchDueDeliveries(ctx, 1, "same-worker", time.Minute)
	if err != nil || len(second) != 1 || second[0].LeaseGeneration <= first[0].LeaseGeneration {
		t.Fatalf("same-owner reclaim = %d, %v", len(second), err)
	}
	if err := deferrer.DeferDelivery(ctx, delivery.ID, "same-worker", first[0].LeaseGeneration,
		"callback.secret_unavailable", time.Minute); !errors.Is(err, repository.ErrCallbackDeliveryLeaseLost) {
		t.Fatalf("stale generation deferral = %v", err)
	}
	row, err := repo.GetDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "delivering" || row.Attempt != 7 || row.LeaseGeneration != second[0].LeaseGeneration || row.ErrorCode != "" {
		t.Fatalf("stale deferral changed new owner's delivery: %+v", row)
	}
	if err := deferrer.DeferDelivery(ctx, delivery.ID, "same-worker", second[0].LeaseGeneration,
		"callback.secret_unavailable", time.Minute); err != nil {
		t.Fatalf("current generation deferral: %v", err)
	}
}
