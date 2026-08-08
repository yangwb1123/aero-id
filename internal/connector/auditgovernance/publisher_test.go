package auditgovernance

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

func TestPublisherUsesDerivedSourceAndMatchingLedgerReceipt(t *testing.T) {
	const tenantID = "tenant-a"
	expectedSource, err := TenantSourceID("aero-id", tenantID)
	if err != nil {
		t.Fatal(err)
	}
	fact := &domain.AuditFact{
		ID: idgen.NewID(), TenantID: tenantID, ActorUID: "user-1", Action: "account.profile.updated",
		ResourceType: "account_profile", ResourceID: "account-1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"changed_fields": []string{"locale"}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			clientID, secret, ok := r.BasicAuth()
			if !ok || clientID != "client" || secret != "secret" {
				t.Errorf("unexpected token credentials")
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("scope") != auditScope ||
				r.Form.Get("resource") != auditResource {
				t.Error("Audit token request was not exact-scope/resource")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"service-token","token_type":"Bearer","expires_in":300}`))
		case "/api/v1/events":
			assertGovernanceRequest(t, r, expectedSource)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"receipt": map[string]any{
				"event_id": fact.ID.String(), "tenant_id": tenantID, "status": "ledgered", "accepted_at": time.Now().UTC(),
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: {
			TokenURL: server.URL + "/token", ClientID: "client", ClientSecret: "secret",
			Scope: auditScope, Resources: []string{auditResource},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(Config{
		Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
	}, tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(t.Context(), fact); err != nil {
		t.Fatal(err)
	}
}

func assertGovernanceRequest(t *testing.T, request *http.Request, expectedSource string) {
	t.Helper()
	if request.URL.Query().Get("wait_for") != "ledgered" || request.Header.Get("Authorization") != "Bearer service-token" {
		t.Errorf("missing ledger wait or bearer binding")
	}
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["source_system"] != expectedSource {
		t.Errorf("source_system = %v", body["source_system"])
	}
	if body["schema_id"] != "aero.id.audit-fact" || body["event_type"] != "aero.id.audit-fact" {
		t.Errorf("schema/event contract = %v/%v", body["schema_id"], body["event_type"])
	}
	if body["action"] != "account.profile.updated" {
		t.Errorf("action = %v, want original audit fact action", body["action"])
	}
	if _, supplied := body["tenant_id"]; supplied {
		t.Error("tenant_id must not be an event-body authorization input")
	}
}

// TestPublisherConflictReceiptIsIdempotentSuccess (FM-9): a duplicate
// delivery (outbox lease-expiry re-claim, FM-3) is answered by the endpoint
// with 202 + a conflict receipt echoing the original acceptance. That dedup
// acknowledgement must be a success — never ErrInvalidReceipt, which would
// retry and settle the duplicated successful event as dead_letter.
func TestPublisherConflictReceiptIsIdempotentSuccess(t *testing.T) {
	const tenantID = "tenant-a"
	fact := &domain.AuditFact{
		ID: idgen.NewID(), TenantID: tenantID, ActorUID: "user-1", Action: "account.profile.updated",
		ResourceType: "account_profile", ResourceID: "account-1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{"changed_fields": []string{"locale"}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"service-token","token_type":"Bearer","expires_in":300}`))
		case "/api/v1/events":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"receipt": map[string]any{
				"event_id": fact.ID.String(), "tenant_id": tenantID, "status": "ledgered",
				"accepted_at": time.Now().UTC(), "conflict": true,
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: {
			TokenURL: server.URL + "/token", ClientID: "client", ClientSecret: "secret",
			Scope: auditScope, Resources: []string{auditResource},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(Config{
		Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
	}, tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(t.Context(), fact); err != nil {
		t.Fatalf("conflict receipt must be idempotent success, got %v", err)
	}
}

// TestPublisherConflictReceiptMustEchoTheFact (FM-9 guard): only a conflict
// receipt that echoes this exact event_id/tenant_id with a non-zero
// accepted_at is an idempotent success; a misrouted or degenerate conflict
// stays ErrInvalidReceipt so a foreign idempotency state can never be
// acknowledged as this fact's success.
func TestPublisherConflictReceiptMustEchoTheFact(t *testing.T) {
	fact := &domain.AuditFact{ID: idgen.NewID(), TenantID: "tenant-a", Action: "test.action"}
	// buildReceipt returns a synthetic 202 response; the caller owns the body
	// (validateReceipt closes it too, so a defer in the caller is harmless).
	buildReceipt := func(eventID, tenantID, status string, conflict bool, acceptedAt time.Time) *http.Response {
		body, err := json.Marshal(map[string]any{"receipt": map[string]any{
			"event_id": eventID, "tenant_id": tenantID, "status": status,
			"accepted_at": acceptedAt, "conflict": conflict,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusAccepted,
			Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(body))}
	}
	checkInvalid := func(t *testing.T, name string, build func() *http.Response) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			response := build()
			defer response.Body.Close()
			if err := validateReceipt(response, fact); !errors.Is(err, ErrInvalidReceipt) {
				t.Fatalf("receipt error = %v, want ErrInvalidReceipt", err)
			}
		})
	}
	t.Run("matching echo is idempotent success", func(t *testing.T) {
		response := buildReceipt(fact.ID.String(), "tenant-a", "ledgered", true, time.Now().UTC())
		defer response.Body.Close()
		if err := validateReceipt(response, fact); err != nil {
			t.Fatalf("matching conflict receipt = %v, want nil", err)
		}
	})
	checkInvalid(t, "different event_id", func() *http.Response {
		return buildReceipt("11111111-1111-4111-8111-111111111111", "tenant-a", "ledgered", true, time.Now().UTC())
	})
	checkInvalid(t, "different tenant_id", func() *http.Response {
		return buildReceipt(fact.ID.String(), "tenant-b", "ledgered", true, time.Now().UTC())
	})
	checkInvalid(t, "zero accepted_at", func() *http.Response {
		return buildReceipt(fact.ID.String(), "tenant-a", "ledgered", true, time.Time{})
	})
	checkInvalid(t, "non-conflict rejected", func() *http.Response {
		return buildReceipt(fact.ID.String(), "tenant-a", "rejected", false, time.Now().UTC())
	})
}

func TestPublisherEventTypeDoesNotVaryWithFactAction(t *testing.T) {
	publisher := &Publisher{schemaID: "aero.id.audit-fact", schemaVersion: 1}
	first := &domain.AuditFact{ID: idgen.NewID(), Action: "account.created", OccurredAt: time.Now()}
	second := &domain.AuditFact{ID: idgen.NewID(), Action: "session.revoked", OccurredAt: time.Now()}

	for _, fact := range []*domain.AuditFact{first, second} {
		event := publisher.event(fact, "aero-id.source")
		if event.EventType != "aero.id.audit-fact" || event.SchemaID != "aero.id.audit-fact" {
			t.Fatalf("schema/event contract = %q/%q", event.SchemaID, event.EventType)
		}
		if event.Action != fact.Action {
			t.Fatalf("action = %q, want %q", event.Action, fact.Action)
		}
	}
}

func TestPublisherRejectsTenantlessFactBeforeNetwork(t *testing.T) {
	publisher := &Publisher{}
	if err := publisher.Publish(t.Context(), &domain.AuditFact{ID: idgen.NewID(), Action: "account.viewed"}); err == nil {
		t.Fatal("tenantless fact was accepted")
	}
}

func TestOAuthTokenSourceUsesPerTenantCredentialsAndRejectsUnboundTenant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID, secret, ok := r.BasicAuth()
		if !ok || secret != "secret-for-"+clientID {
			t.Error("wrong tenant credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-for-" + clientID, "token_type": "Bearer", "expires_in": 300,
		})
	}))
	defer server.Close()
	bindings := map[string]OAuthBindingConfig{
		"tenant-a": auditBinding(server.URL, "client-a"),
		"tenant-b": auditBinding(server.URL, "client-b"),
	}
	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", Bindings: bindings, AllowInsecureLoopback: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tenantID := range []string{"tenant-a", "tenant-b"} {
		source, _ := TenantSourceID("aero-id", tenantID)
		token, tokenErr := tokens.AccessToken(t.Context(), SourceBinding{TenantID: tenantID, SourceSystem: source})
		if tokenErr != nil || token != "token-for-client-"+tenantID[len("tenant-"):] {
			t.Fatalf("tenant-bound token = %q, %v", token, tokenErr)
		}
	}
	source, _ := TenantSourceID("aero-id", "tenant-c")
	_, err = tokens.AccessToken(t.Context(), SourceBinding{TenantID: "tenant-c", SourceSystem: source})
	if !errors.Is(err, ErrTenantUnbound) {
		t.Fatalf("unbound tenant error = %v", err)
	}
}

func TestOAuthTokenSourceRejectsCredentialReuse(t *testing.T) {
	binding := auditBinding("http://127.0.0.1", "same-client")
	bindings := map[string]OAuthBindingConfig{"tenant-a": binding, "tenant-b": binding}
	if _, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", Bindings: bindings, AllowInsecureLoopback: true,
	}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("duplicate credentials error = %v", err)
	}
}

func auditBinding(baseURL, clientID string) OAuthBindingConfig {
	return OAuthBindingConfig{
		TokenURL: baseURL + "/token", ClientID: clientID, ClientSecret: "secret-for-" + clientID,
		Scope: auditScope, Resources: []string{auditResource},
	}
}
