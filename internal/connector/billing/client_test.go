package billing

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

func TestClientUsesServerConfiguredTenantBindingAndTenantlessPaths(t *testing.T) {
	now := time.Now().UTC()
	entitlement := &domain.EntitlementProjection{
		TenantID: "tenant-1", SubscriptionID: "sub-1", Plan: domain.PlanRef{ID: "full", Version: 1},
		Revision: 2, Active: true, Features: map[string]bool{"account_profiles": true},
		Limits: map[string]domain.LimitGrant{"users": {Hard: 10}}, EffectiveAt: now.Add(-time.Hour),
		ExpiresAt: now.Add(time.Hour), GeneratedAt: now,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "tenant-1-client" || secret != "secret" {
				t.Error("wrong configured OAuth binding")
			}
			if err := request.ParseForm(); err != nil || request.Form.Get("resource") != billingResource {
				t.Error("token request did not use the exact Billing resource")
			}
			scope := request.Form.Get("scope")
			if scope != ScopeEntitlementRead && scope != ScopeMeteringWrite {
				t.Errorf("unexpected token scope %q", scope)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"access_token": "token-" + scope, "token_type": "Bearer", "expires_in": 300,
			})
		case "/api/v1/metering/entitlement":
			if request.URL.RawQuery != "" || request.Header.Get("Authorization") != "Bearer token-"+ScopeEntitlementRead {
				t.Error("entitlement request contains caller-selected routing or wrong token")
			}
			writeJSON(t, w, http.StatusOK, map[string]any{"entitlement": entitlement})
		case "/api/v1/metering/reservations":
			if request.Header.Get("Authorization") != "Bearer token-"+ScopeMeteringWrite {
				t.Error("reservation request did not use the narrow metering token")
			}
			assertReserveRequest(t, request)
			writeJSON(t, w, http.StatusCreated, map[string]any{"reservation": map[string]any{
				"id": "usr-res-1", "tenant_id": "tenant-1", "dimension": "users", "quantity": 1, "status": "pending",
			}})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	client, err := NewClient(Config{Endpoint: server.URL, AllowInsecureLoopback: true, Bindings: map[string]BindingConfig{
		"tenant-1": {TokenURL: server.URL + "/token", ClientID: "tenant-1-client", ClientSecret: "secret",
			Scope: ScopeEntitlementRead + " " + ScopeMeteringWrite, Resources: []string{billingResource}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CurrentEntitlement(t.Context(), "tenant-1"); err != nil {
		t.Fatal(err)
	}
	_, err = client.Reserve(t.Context(), "tenant-1", ReserveCommand{
		ID: "usr-res-1", Dimension: "users", Quantity: 1, TTLSeconds: 300, IdempotencyKey: "stable-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CurrentEntitlement(t.Context(), "tenant-unconfigured"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unconfigured tenant error = %v", err)
	}
}

func TestClientMapsOnlyQuotaErrorCodeToQuotaExceeded(t *testing.T) {
	client := &Client{maxBody: 1024}
	tests := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusConflict, `{"error":"metering_quota_exceeded"}`, ErrQuotaExceeded},
		{http.StatusConflict, `{"error":"metering_idempotency_conflict"}`, ErrUnavailable},
		{http.StatusForbidden, `{"error":"metering_entitlement_missing"}`, ErrEntitlement},
		{http.StatusForbidden, `{"error":"metering_source_unauthorized"}`, ErrUnauthorized},
	}
	for _, test := range tests {
		response := &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body)), Header: http.Header{}}
		if err := client.decode(response, nil); !errors.Is(err, test.want) {
			t.Errorf("decode(%d, %s) error = %v, want %v", test.status, test.body, err, test.want)
		}
	}
}

func TestClientRejectsCredentialReuseAcrossTenants(t *testing.T) {
	binding := BindingConfig{TokenURL: "http://127.0.0.1/token", ClientID: "shared-client", ClientSecret: "shared-secret",
		Scope: ScopeEntitlementRead + " " + ScopeMeteringWrite, Resources: []string{billingResource}}
	_, err := NewClient(Config{Endpoint: "http://127.0.0.1", AllowInsecureLoopback: true,
		Bindings: map[string]BindingConfig{"tenant-one": binding, "tenant-two": binding}}, nil)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("duplicate credential error = %v", err)
	}
}

func assertReserveRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Header.Get("Idempotency-Key") != "stable-key" {
		t.Error("missing stable idempotency key")
	}
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, supplied := body["tenant_id"]; supplied {
		t.Error("tenant_id must not be sent in the metering request")
	}
	if body["dimension"] != "users" {
		t.Errorf("dimension = %v", body["dimension"])
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}
