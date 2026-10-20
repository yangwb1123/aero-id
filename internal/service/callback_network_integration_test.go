//go:build integration

package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

type callbackLocalTLSRoundTripper struct {
	t      *testing.T
	server *httptest.Server
}

func (r callbackLocalTLSRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	// The actual callback transport/DNS/dial/connection owner remains in use;
	// only this local test's explicit CA is attached. No global resolver,
	// production trust/configuration, credentials or InsecureSkipVerify.
	client := callbackLoopbackClient(r.t, r.server, request.Context())
	return client.Transport.RoundTrip(request)
}

func TestCallbackNetworkPostgresLostAcknowledgmentRetainsOriginalSignedEvent(t *testing.T) {
	db, service, repo := startCallbackService(t, "integration-callback-network-original-event-key")
	svc := service.(*callbackService)
	svc.cfg.AllowPrivateTarget = true
	svc.cfg.RequestTimeout = 250 * time.Millisecond
	secret := []byte("original-callback-network-test-signing-secret")
	eventID := idgen.NewID()
	var mu sync.Mutex
	requests, effects := 0, 0
	seen := map[string]bool{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			t.Error(err)
		}
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(r.Header.Get("X-Aero-Callback-Timestamp") + "\n"))
		_, _ = mac.Write(body)
		var payload struct {
			EventID string `json:"event_id"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Error(err)
		}
		id := r.Header.Get("X-Aero-Callback-Event-ID")
		if id != eventID.String() || payload.EventID != id || r.Header.Get("X-Aero-Callback-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("original signed body/header event identity changed")
		}
		mu.Lock()
		requests++
		first := requests == 1
		if !seen[id] {
			seen[id] = true
			effects++
		}
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		if first {
			w.(http.Flusher).Flush()
			<-r.Context().Done() // Receiver effect already occurred; lose the body/acknowledgment.
			return
		}
		if _, err := io.WriteString(w, "accepted-original-event"); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	svc.roundTripper = callbackLocalTLSRoundTripper{t: t, server: server}
	ciphertext, err := svc.encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	sub := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a", Endpoint: server.URL,
		EventTypes: []string{"account.updated"}, SecretCiphertext: ciphertext, Enabled: true, Verified: true}
	if err := repo.CreateSubscription(t.Context(), sub); err != nil {
		t.Fatal(err)
	}
	delivery := &domain.CallbackDelivery{ID: idgen.NewID(), SubscriptionID: sub.ID, EventID: eventID, Action: "account.updated",
		Payload: map[string]any{"schema_version": 1, "event_id": eventID.String()}}
	if err := repo.EnqueueDelivery(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	svc.drain(t.Context())
	row, err := repo.GetDelivery(t.Context(), delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "pending" || row.Attempt != 1 || row.EventID != eventID || row.LeaseUntil != nil {
		t.Fatalf("lost acknowledgment changed retry/evidence: status=%s attempt=%d event=%s", row.Status, row.Attempt, row.EventID)
	}
	// Fixture-controlled eligibility of the same durable row, not another
	// command/replay/key or an increased retry/timeout budget.
	if err := db.Exec("UPDATE callback_deliveries SET next_attempt_at = clock_timestamp() - INTERVAL '1 second' WHERE id = ?", delivery.ID).Error; err != nil {
		t.Fatal(err)
	}
	svc.drain(t.Context())
	row, err = repo.GetDelivery(t.Context(), delivery.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "success" || row.Attempt != 2 || row.EventID != eventID || row.LeaseGeneration != 2 {
		t.Fatalf("same-event recovery failed: status=%s attempt=%d event=%s generation=%d", row.Status, row.Attempt, row.EventID, row.LeaseGeneration)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 || effects != 1 || len(seen) != 1 {
		t.Fatalf("receiver dedup proof: requests=%d effects=%d events=%d", requests, effects, len(seen))
	}
}
