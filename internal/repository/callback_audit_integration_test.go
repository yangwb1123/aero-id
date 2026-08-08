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

const callbackAuditTestKey = "integration-callback-audit-key-0123456789abcdef"

func startCallbackAuditRepo(t *testing.T, key string) (*gorm.DB, repository.CallbackRepository, repository.CallbackAuditRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: key})
	callbackRepo := repository.NewCallbackRepository(db, ledger)
	auditRepo, ok := callbackRepo.(repository.CallbackAuditRepository)
	if !ok {
		t.Fatal("callback repo must implement CallbackAuditRepository")
	}
	return db, callbackRepo, auditRepo
}

func callbackFact(eventID domain.ID, tenantID, action string) *domain.AuditFact {
	return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: tenantID + ":cb",
		EventID: eventID, TenantID: tenantID, ActorUID: "u-1", Action: action,
		ResourceType: "callback_subscription", ResourceID: eventID.String(), OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
}

func countCallbackAuditRows(t *testing.T, db *gorm.DB, eventID domain.ID) (int64, int64) {
	t.Helper()
	var ledgerRows, publicationRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID).Scan(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&publicationRows).Error; err != nil {
		t.Fatal(err)
	}
	return ledgerRows, publicationRows
}

// TestCallbackAuditHappyPath (REQ-3): each callback mutation commits its audit
// fact in the same transaction — one ledger row + one publication row per call.
func TestCallbackAuditHappyPath(t *testing.T) {
	ctx := context.Background()
	db, callbackRepo, auditRepo := startCallbackAuditRepo(t, callbackAuditTestKey)

	// CreateSubscriptionWithAudit: subscription + fact atomically.
	subID := idgen.NewID()
	subscription := &domain.CallbackSubscription{ID: subID, OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{"account.provisioned"},
		SecretCiphertext: "cipher", Enabled: true, Verified: true}
	if err := auditRepo.CreateSubscriptionWithAudit(ctx, subscription, callbackFact(subID, "tenant-a", "callback.registered")); err != nil {
		t.Fatalf("CreateSubscriptionWithAudit: %v", err)
	}
	ledgerRows, publicationRows := countCallbackAuditRows(t, db, subID)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	var subRows int64
	if err := db.Raw(`SELECT count(*) FROM callback_subscriptions WHERE id = ?`, subID).Scan(&subRows).Error; err != nil {
		t.Fatal(err)
	}
	if subRows != 1 {
		t.Fatalf("subscription rows = %d, want 1", subRows)
	}

	// EnqueueDelivery + ResetDeliveryWithAudit: reset + fact atomically.
	deliveryID := idgen.NewID()
	delivery := &domain.CallbackDelivery{ID: deliveryID, SubscriptionID: subID, EventID: idgen.NewID(),
		Action: "account.provisioned", Payload: map[string]any{"schema_version": 1}, Status: "delivering",
		Attempt: 2, LeaseOwner: "worker-1", LeaseUntil: ptrTime(time.Now().UTC().Add(time.Minute)),
		NextAttemptAt: time.Now().UTC()}
	if err := callbackRepo.EnqueueDelivery(ctx, delivery); err != nil {
		t.Fatalf("EnqueueDelivery: %v", err)
	}
	replayEvent := idgen.NewID()
	if err := auditRepo.ResetDeliveryWithAudit(ctx, deliveryID, callbackFact(replayEvent, "tenant-a", "callback.delivery.replayed")); err != nil {
		t.Fatalf("ResetDeliveryWithAudit: %v", err)
	}
	ledgerRows, publicationRows = countCallbackAuditRows(t, db, replayEvent)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("replay ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	var resetRow struct {
		Status     string
		LeaseOwner string
		ErrorCode  string
	}
	if err := db.Raw(`SELECT status, lease_owner, error_code FROM callback_deliveries WHERE id = ?`, deliveryID).Scan(&resetRow).Error; err != nil {
		t.Fatal(err)
	}
	if resetRow.Status != "pending" || resetRow.LeaseOwner != "" || resetRow.ErrorCode != "manual_replay" {
		t.Fatalf("delivery after reset = %+v, want pending//manual_replay", resetRow)
	}

	// DeleteSubscriptionWithAudit: delete + fact atomically.
	deleteEvent := idgen.NewID()
	if err := auditRepo.DeleteSubscriptionWithAudit(ctx, "u-1", "tenant-a", subID, callbackFact(deleteEvent, "tenant-a", "callback.deleted")); err != nil {
		t.Fatalf("DeleteSubscriptionWithAudit: %v", err)
	}
	ledgerRows, publicationRows = countCallbackAuditRows(t, db, deleteEvent)
	if ledgerRows != 1 || publicationRows != 1 {
		t.Fatalf("delete ledger/publication rows = %d/%d, want 1/1", ledgerRows, publicationRows)
	}
	if err := db.Raw(`SELECT count(*) FROM callback_subscriptions WHERE id = ?`, subID).Scan(&subRows).Error; err != nil {
		t.Fatal(err)
	}
	if subRows != 0 {
		t.Fatalf("subscription rows after delete = %d, want 0", subRows)
	}
}

// TestCallbackAuditRollbackWithoutSigningKey (C2 injection / FM-1): with a
// keyless ledger the in-tx append fails and the WHOLE mutation rolls back —
// the domain write shares the transaction with AppendInTx. The signing-key
// injection replaces the old duplicate-event_id recipe (unusable for replay
// since C2 gives every replay a fresh EventID).
func TestCallbackAuditRollbackWithoutSigningKey(t *testing.T) {
	ctx := context.Background()
	db, callbackRepo, auditRepo := startCallbackAuditRepo(t, "")

	// Create: subscription absent, zero ledger rows.
	subID := idgen.NewID()
	subscription := &domain.CallbackSubscription{ID: subID, OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{"account.provisioned"},
		SecretCiphertext: "cipher", Enabled: true, Verified: true}
	if err := auditRepo.CreateSubscriptionWithAudit(ctx, subscription, callbackFact(subID, "tenant-a", "callback.registered")); err == nil {
		t.Fatal("CreateSubscriptionWithAudit with keyless ledger must fail")
	}
	var subRows int64
	if err := db.Raw(`SELECT count(*) FROM callback_subscriptions WHERE id = ?`, subID).Scan(&subRows).Error; err != nil {
		t.Fatal(err)
	}
	if subRows != 0 {
		t.Fatalf("subscription rows after failed create = %d, want 0 (rolled back)", subRows)
	}
	if ledgerRows, publicationRows := countCallbackAuditRows(t, db, subID); ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("ledger/publication rows after failed create = %d/%d, want 0/0", ledgerRows, publicationRows)
	}

	// Reset: seed a delivery into the in-flight state through the claim path,
	// then a failed reset must leave the delivery byte-identical.
	seedID := idgen.NewID()
	delivery := &domain.CallbackDelivery{ID: seedID, SubscriptionID: subID, EventID: idgen.NewID(),
		Action: "account.provisioned", Payload: map[string]any{}, Status: "pending"}
	// Seed the subscription row directly (plain create path on the same repo).
	if err := callbackRepo.CreateSubscription(ctx, subscription); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	if err := callbackRepo.EnqueueDelivery(ctx, delivery); err != nil {
		t.Fatalf("seed delivery: %v", err)
	}
	claimed, err := callbackRepo.FetchDueDeliveries(ctx, 50, "worker-1", 5*time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim seed delivery = %d, err %v; want 1", len(claimed), err)
	}
	before := claimed[0]
	replayEvent := idgen.NewID()
	if err := auditRepo.ResetDeliveryWithAudit(ctx, seedID, callbackFact(replayEvent, "tenant-a", "callback.delivery.replayed")); err == nil {
		t.Fatal("ResetDeliveryWithAudit with keyless ledger must fail")
	}
	var after struct {
		Status     string
		Attempt    int
		LeaseOwner string
		ErrorCode  string
	}
	if err := db.Raw(`SELECT status, attempt, lease_owner, error_code FROM callback_deliveries WHERE id = ?`, seedID).Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Status != before.Status || after.Attempt != before.Attempt || after.LeaseOwner != before.LeaseOwner || after.ErrorCode != before.ErrorCode {
		t.Fatalf("delivery after failed reset = %+v, want byte-identical to claimed %+v", after, before)
	}
	if ledgerRows, publicationRows := countCallbackAuditRows(t, db, replayEvent); ledgerRows != 0 || publicationRows != 0 {
		t.Fatalf("ledger/publication rows after failed reset = %d/%d, want 0/0", ledgerRows, publicationRows)
	}

	// Delete: the subscription survives a failed delete.
	deleteEvent := idgen.NewID()
	if err := auditRepo.DeleteSubscriptionWithAudit(ctx, "u-1", "tenant-a", subID, callbackFact(deleteEvent, "tenant-a", "callback.deleted")); err == nil {
		t.Fatal("DeleteSubscriptionWithAudit with keyless ledger must fail")
	}
	if err := db.Raw(`SELECT count(*) FROM callback_subscriptions WHERE id = ?`, subID).Scan(&subRows).Error; err != nil {
		t.Fatal(err)
	}
	if subRows != 1 {
		t.Fatalf("subscription rows after failed delete = %d, want 1 (rollback preserved the row)", subRows)
	}
}

// TestRequeueDeadRollsBackWithoutSigningKey (T-10.2 delivery leg / FM-1):
// the RequeueDead analogue of the publication rollback — an unkeyed REAL
// ledger (integration, not a fake) makes the in-tx fact append fail, and the
// whole reset rolls back: the delivery stays dead_letter byte-identical and
// zero ledger/publication rows appear for the fact.
func TestRequeueDeadRollsBackWithoutSigningKey(t *testing.T) {
	ctx := context.Background()
	db, callbacks, _ := startCallbackAuditRepo(t, "")

	sub := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{"account.provisioned"},
		SecretCiphertext: "cipher", Enabled: true, Verified: true}
	if err := callbacks.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	delivery := &domain.CallbackDelivery{SubscriptionID: sub.ID, EventID: idgen.NewID(),
		Action: "account.provisioned", Payload: map[string]any{}}
	if err := callbacks.EnqueueDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	fetched, err := callbacks.FetchDueDeliveries(ctx, 10, "test-worker", 5*time.Minute)
	if err != nil || len(fetched) != 1 {
		t.Fatalf("fetch = %v, %v; want 1", fetched, err)
	}
	if err := callbacks.MarkDelivery(ctx, fetched[0].ID, "test-worker", "dead_letter", 0, "", "delivery.failed",
		time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	fact := callbackFact(idgen.NewID(), "tenant-a", "callback.delivery.requeued")
	intent := deliveryReplayIntent("", nil)
	intent.Envelope = *fact
	n, facts, err := callbacks.RequeueDead(ctx, nil, 0, intent)
	if err == nil {
		t.Fatalf("RequeueDead with keyless ledger must fail, got n=%d facts=%d nil", n, len(facts))
	}
	var row struct {
		Status     string
		Attempt    int
		LeaseOwner any
		ErrorCode  string
	}
	if err := db.Raw(`SELECT status, attempt, lease_owner, error_code FROM callback_deliveries WHERE id = ?`, fetched[0].ID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	// The whole requeue transaction rolled back, so the row is bit-for-bit the
	// pre-requeue state: dead_letter with the attempt MarkDelivery persisted
	// (0 → 1 via attempt+1) and the original error code.
	if row.Status != "dead_letter" || row.Attempt != 1 || row.LeaseOwner != nil || row.ErrorCode != "delivery.failed" {
		t.Fatalf("delivery after rolled-back requeue = %+v, want dead_letter/1/nil/delivery.failed", row)
	}
	// The whole requeue transaction rolled back: zero ledger/publication rows
	// for the replay action.
	var replayRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'callback.delivery.requeued'`).Scan(&replayRows).Error; err != nil {
		t.Fatal(err)
	}
	if replayRows != 0 {
		t.Fatalf("ledger rows after rolled-back requeue = %d, want 0", replayRows)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
