package billing

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestClientRetriesUnauthorizedWithFreshTokenAndPreservesRequest(t *testing.T) {
	command := ReserveCommand{ID: "res-1", Dimension: "users", Quantity: 2, TTLSeconds: 300, IdempotencyKey: "stable-key"}
	expectedBody, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	var tokenCalls, resourceCalls atomic.Int32
	captures := make(chan billingRequestCapture, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			call := tokenCalls.Add(1)
			clientID, secret, ok := request.BasicAuth()
			if !ok || clientID != "tenant-1-client" || secret != "secret" {
				t.Errorf("token credentials = %q/%q/%v", clientID, secret, ok)
			}
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			if request.Form.Get("scope") != ScopeMeteringWrite || request.Form.Get("resource") != billingResource {
				t.Errorf("token form scope/resource = %q/%q", request.Form.Get("scope"), request.Form.Get("resource"))
			}
			token := "token-1"
			if call == 2 {
				token = "token-2"
			}
			writeJSON(t, w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300})
		case "/" + reservationPath:
			body, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Errorf("read reservation body: %v", readErr)
			}
			captures <- billingRequestCapture{method: request.Method, path: request.URL.Path,
				authorization: request.Header.Get("Authorization"), idempotencyKey: request.Header.Get("Idempotency-Key"), body: string(body)}
			resourceCalls.Add(1)
			if request.Header.Get("Authorization") == "Bearer token-1" {
				writeJSON(t, w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
				return
			}
			writeJSON(t, w, http.StatusCreated, map[string]any{"reservation": map[string]any{
				"id": command.ID, "tenant_id": "tenant-1", "dimension": command.Dimension,
				"quantity": command.Quantity, "status": "pending",
			}})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	client := newBillingTestClient(t, server.URL)
	reservation, err := client.Reserve(t.Context(), "tenant-1", command)
	if err != nil {
		t.Fatalf("reserve after token refresh: %v", err)
	}
	if reservation.ID != command.ID || reservation.TenantID != "tenant-1" || reservation.Quantity != command.Quantity {
		t.Fatalf("reservation = %+v, want validated response", reservation)
	}
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("token calls = %d, want 2", got)
	}
	if got := resourceCalls.Load(); got != 2 {
		t.Fatalf("resource calls = %d, want 2", got)
	}
	first, second := nextBillingCapture(t, captures), nextBillingCapture(t, captures)
	if first.authorization != "Bearer token-1" || second.authorization != "Bearer token-2" {
		t.Fatalf("Authorization headers = %q/%q, want token-1/token-2", first.authorization, second.authorization)
	}
	for index, capture := range []billingRequestCapture{first, second} {
		if capture.method != http.MethodPost || capture.path != "/"+reservationPath || capture.idempotencyKey != command.IdempotencyKey {
			t.Errorf("request %d metadata = %+v, want exact retry metadata", index, capture)
		}
		if capture.body != string(expectedBody) {
			t.Errorf("request %d body = %q, want %q", index, capture.body, expectedBody)
		}
	}
}

func TestClientBoundsUnauthorizedRetry(t *testing.T) {
	var tokenCalls, resourceCalls atomic.Int32
	captures := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			call := tokenCalls.Add(1)
			token := "token-1"
			if call == 2 {
				token = "token-2"
			}
			writeJSON(t, w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 300})
		case "/" + entitlementPath:
			resourceCalls.Add(1)
			captures <- request.Header.Get("Authorization")
			writeJSON(t, w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	client := newBillingTestClient(t, server.URL)
	_, err := client.CurrentEntitlement(t.Context(), "tenant-1")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("second 401 error = %v, want ErrUnauthorized", err)
	}
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("token calls = %d, want 2", got)
	}
	if got := resourceCalls.Load(); got != 2 {
		t.Fatalf("resource calls = %d, want 2", got)
	}
	if first, second := nextBillingHeader(t, captures), nextBillingHeader(t, captures); first != "Bearer token-1" || second != "Bearer token-2" {
		t.Fatalf("Authorization headers = %q/%q, want token-1/token-2", first, second)
	}
}

func TestClientDoesNotRefreshForbiddenOrInsufficientScope(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "forbidden", status: http.StatusForbidden, body: `{"error":"insufficient_scope"}`},
		{name: "insufficient scope", status: http.StatusUnauthorized, body: `{"error":"insufficient_scope"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var tokenCalls, resourceCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/token":
					tokenCalls.Add(1)
					writeJSON(t, w, http.StatusOK, map[string]any{"access_token": "token-1", "token_type": "Bearer", "expires_in": 300})
				case "/" + entitlementPath:
					resourceCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, test.body)
				default:
					http.NotFound(w, request)
				}
			}))
			defer server.Close()

			client := newBillingTestClient(t, server.URL)
			_, err := client.CurrentEntitlement(t.Context(), "tenant-1")
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("error = %v, want ErrUnauthorized", err)
			}
			if got := tokenCalls.Load(); got != 1 {
				t.Fatalf("token calls = %d, want 1", got)
			}
			if got := resourceCalls.Load(); got != 1 {
				t.Fatalf("resource calls = %d, want 1", got)
			}
			if token, ok := client.bindings["tenant-1"].cached(ScopeEntitlementRead); !ok || token != "token-1" {
				t.Fatalf("cached token = %q/%v, want token-1/true", token, ok)
			}
		})
	}
}

func newBillingTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()
	client, err := NewClient(Config{Endpoint: endpoint, Timeout: time.Second, MaxBodyBytes: 1024,
		AllowInsecureLoopback: true, Bindings: map[string]BindingConfig{
			"tenant-1": {TokenURL: endpoint + "/token", ClientID: "tenant-1-client", ClientSecret: "secret",
				Scope: ScopeEntitlementRead + " " + ScopeMeteringWrite, Resources: []string{billingResource}},
		}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type billingRequestCapture struct {
	method         string
	path           string
	authorization  string
	idempotencyKey string
	body           string
}

func nextBillingCapture(t *testing.T, captures <-chan billingRequestCapture) billingRequestCapture {
	t.Helper()
	select {
	case capture := <-captures:
		return capture
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for billing request")
		return billingRequestCapture{}
	}
}

func nextBillingHeader(t *testing.T, captures <-chan string) string {
	t.Helper()
	select {
	case capture := <-captures:
		return capture
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for billing request")
		return ""
	}
}

func TestClientReleaseCarriesStableIdempotencyKeyAndEscapedPath(t *testing.T) {
	const reservationID = "usr-res-needs/escaping"
	const releaseKey = "usr-release-stable"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/token":
			writeJSON(t, writer, http.StatusOK, map[string]any{"access_token": "metering-token", "token_type": "Bearer", "expires_in": 300})
		default:
			if request.Method != http.MethodDelete || request.URL.EscapedPath() != "/"+reservationPath+"/usr-res-needs%2Fescaping" {
				t.Errorf("release request = %s %s", request.Method, request.URL.EscapedPath())
			}
			if request.Header.Get("Idempotency-Key") != releaseKey {
				t.Errorf("release idempotency key = %q, want %q", request.Header.Get("Idempotency-Key"), releaseKey)
			}
			calls.Add(1)
			writeJSON(t, writer, http.StatusOK, map[string]any{"reservation": map[string]any{
				"id": reservationID, "tenant_id": "tenant-1", "status": "released",
			}})
		}
	}))
	defer server.Close()

	client := newBillingTestClient(t, server.URL)
	for range 2 {
		if err := client.Release(t.Context(), "tenant-1", reservationID, releaseKey); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("release calls = %d, want 2", calls.Load())
	}
	if err := client.Release(t.Context(), "tenant-1", reservationID, " "); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty release key error = %v, want ErrInvalidConfig", err)
	}
}

func TestClientReleaseRejectsMismatchedResponse(t *testing.T) {
	for _, test := range []struct {
		name string
		body map[string]any
	}{
		{name: "reservation id", body: map[string]any{"id": "other", "tenant_id": "tenant-1", "status": "released"}},
		{name: "tenant", body: map[string]any{"id": "usr-res-1", "tenant_id": "tenant-2", "status": "released"}},
		{name: "committed state", body: map[string]any{"id": "usr-res-1", "tenant_id": "tenant-1", "status": "committed"}},
		{name: "missing state", body: map[string]any{"id": "usr-res-1", "tenant_id": "tenant-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/token" {
					writeJSON(t, writer, http.StatusOK, map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 300})
					return
				}
				writeJSON(t, writer, http.StatusOK, map[string]any{"reservation": test.body})
			}))
			defer server.Close()
			client := newBillingTestClient(t, server.URL)
			if err := client.Release(t.Context(), "tenant-1", "usr-res-1", "usr-release-1"); !errors.Is(err, ErrInvalidReply) {
				t.Fatalf("release error = %v, want ErrInvalidReply", err)
			}
		})
	}
}

func TestClientAcceptsExpiredReleaseReplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writeJSON(t, writer, http.StatusOK, map[string]any{"access_token": "token", "token_type": "Bearer", "expires_in": 300})
			return
		}
		writeJSON(t, writer, http.StatusOK, map[string]any{"reservation": map[string]any{
			"id": "usr-res-1", "tenant_id": "tenant-1", "status": "expired",
		}})
	}))
	defer server.Close()
	client := newBillingTestClient(t, server.URL)
	if err := client.Release(t.Context(), "tenant-1", "usr-res-1", "usr-release-1"); err != nil {
		t.Fatalf("expired release replay = %v", err)
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
