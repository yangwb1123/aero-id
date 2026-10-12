//go:build integration

package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestCallbackKeyRotationPreservesDurableDeliveryAndSigningSecret(t *testing.T) {
	db, _, repo := startCallbackService(t, "integration-callback-key-rotation-audit-key")
	ctx := t.Context()
	secret := []byte("per-subscription-signing-secret-1")
	old := NewCallbackService(repo, CallbackConfig{MasterKey: oldCallbackMaster, SecretEnvelopeVersion: 1}).(*callbackService)
	versioned, err := old.encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	for name, ciphertext := range map[string]string{
		"legacy": legacyCallbackCiphertext(t, oldCallbackMaster, secret), "versioned": versioned,
	} {
		t.Run(name, func(t *testing.T) {
			sub := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a",
				Endpoint: callbackTestEndpoint, EventTypes: []string{}, SecretCiphertext: ciphertext, Enabled: true, Verified: true}
			if err := old.registerWithAudit(replayIdentityContext(), sub, sub.OwnerUID, sub.TenantID, sub.Endpoint, sub.EventTypes); err != nil {
				t.Fatal(err)
			}
			delivery := &domain.CallbackDelivery{SubscriptionID: sub.ID, EventID: idgen.NewID(), Attempt: 7,
				Action: "account.updated", Payload: map[string]any{"schema_version": 1}}
			if err := repo.EnqueueDelivery(ctx, delivery); err != nil {
				t.Fatal(err)
			}
			transport := &cannedTransport{status: http.StatusAccepted}
			missingKey := NewCallbackService(repo, CallbackConfig{MasterKey: newCallbackMaster,
				SecretEnvelopeVersion: 1, AllowPrivateTarget: true}).(*callbackService)
			missingKey.roundTripper = transport
			for i := 0; i < 2; i++ {
				missingKey.drain(ctx)
				row, err := repo.GetDelivery(ctx, delivery.ID)
				if err != nil {
					t.Fatal(err)
				}
				if row.Attempt != 7 || row.Status != "pending" || row.ErrorCode != "callback.secret_unavailable" || row.LeaseUntil != nil {
					t.Fatalf("missing-key drain spent remote budget or lost the row: %+v", row)
				}
				// Test-controlled clock eligibility: make the deferred row due again
				// without waiting the five-minute local-configuration backoff.
				if err := db.Exec("UPDATE callback_deliveries SET next_attempt_at = clock_timestamp() - INTERVAL '1 second' WHERE id = ?", delivery.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			if calls, _ := transport.snapshot(); calls != 0 {
				t.Fatalf("missing key made %d unsigned/invalid network calls", calls)
			}

			rotated := NewCallbackService(repo, CallbackConfig{MasterKey: newCallbackMaster,
				PreviousMasterKeys: []string{oldCallbackMaster}, SecretEnvelopeVersion: 1,
				AllowPrivateTarget: true}).(*callbackService)
			rotated.roundTripper = transport
			rotated.drain(ctx)
			row, err := repo.GetDelivery(ctx, delivery.ID)
			if err != nil {
				t.Fatal(err)
			}
			calls, headers := transport.snapshot()
			if calls != 1 || row.Status != "success" || row.Attempt != 8 || row.ErrorCode != "" {
				t.Fatalf("restored key did not resume original delivery: calls=%d, row=%+v", calls, row)
			}
			body, err := json.Marshal(delivery.Payload)
			if err != nil {
				t.Fatal(err)
			}
			mac := hmac.New(sha256.New, secret)
			_, _ = mac.Write([]byte(headers.Get("X-Aero-Callback-Timestamp") + "\n"))
			_, _ = mac.Write(body)
			if headers.Get("X-Aero-Callback-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) ||
				headers.Get("X-Aero-Callback-Event-ID") != delivery.EventID.String() {
				t.Fatal("master-key rotation changed the subscription signing secret or event identity")
			}
			var facts int64
			if err := db.Table("audit_ledger").Where("event_id = ?", sub.ID).Count(&facts).Error; err != nil || facts != 1 {
				t.Fatalf("registration fact lost/duplicated across rotation: count=%d error=%v", facts, err)
			}
		})
	}
}
