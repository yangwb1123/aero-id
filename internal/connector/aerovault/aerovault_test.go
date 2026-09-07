package aerovault

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

func TestNewUsesDeployedVaultSummaryPath(t *testing.T) {
	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeVaultOAuthToken(w, r) {
			return
		}
		paths <- r.URL.Path
		_, _ = w.Write([]byte(`{"source_region":"local","version":1,"generated_at":"2026-08-30T00:00:00Z","complete":true,"datasets":{"aero-vault.buckets":{}}}`))
	}))
	defer server.Close()

	client, err := New(vaultTestHTTPConfig(t, server.URL, "tenant-a", server.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Summary(t.Context(), connector.SummaryRequest{AccountID: domain.ID{1}, TenantID: "tenant-a",
		Datasets: []domain.AccountDataset{"aero-vault.buckets"}}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got := <-paths; got != DefaultSummaryPath {
		t.Fatalf("summary path = %q, want %q", got, DefaultSummaryPath)
	}
}

func TestNewUsesExplicitSummaryPath(t *testing.T) {
	paths := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeVaultOAuthToken(w, r) {
			return
		}
		paths <- r.URL.Path
		_, _ = w.Write([]byte(`{"source_region":"local","version":1,"generated_at":"2026-08-30T00:00:00Z","complete":true,"datasets":{"aero-vault.buckets":{}}}`))
	}))
	defer server.Close()

	const path = "/compat/account-summary"
	cfg := vaultTestHTTPConfig(t, server.URL, "tenant-a", server.Client())
	cfg.SummaryPath = path
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Summary(t.Context(), connector.SummaryRequest{AccountID: domain.ID{1}, TenantID: "tenant-a",
		Datasets: []domain.AccountDataset{"aero-vault.buckets"}}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got := <-paths; got != path {
		t.Fatalf("summary path = %q, want %q", got, path)
	}
}

func TestNewPropagatesOAuthToSummaryAndHealth(t *testing.T) {
	authHeaders := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeVaultOAuthToken(w, r) {
			return
		}
		authHeaders <- r.Header.Get("Authorization")
		if r.URL.Path == DefaultSummaryPath {
			_, _ = w.Write([]byte(`{"source_region":"local","version":1,"generated_at":"2026-08-30T00:00:00Z","complete":true,"datasets":{"aero-vault.buckets":{}}}`))
			return
		}
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := New(vaultTestHTTPConfig(t, server.URL, "tenant-a", server.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Summary(t.Context(), connector.SummaryRequest{AccountID: domain.ID{1}, TenantID: "tenant-a",
		Datasets: []domain.AccountDataset{"aero-vault.buckets"}}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if err := client.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	for i := 0; i < 2; i++ {
		if got := <-authHeaders; got != "Bearer "+vaultTestAccessToken {
			t.Fatalf("request %d Authorization = %q, want OAuth access token", i, got)
		}
	}
}

func TestNewUsesLazyOAuthAndOnlySendsAccessTokenToVault(t *testing.T) {
	calls := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			calls <- "token " + r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"` + vaultTestAccessToken + `","token_type":"Bearer","expires_in":3600}`))
			return
		}
		calls <- "source " + r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"source_region":"local","version":1,"generated_at":"2026-08-30T00:00:00Z","complete":true,"datasets":{"aero-vault.buckets":{}}}`))
	}))
	defer server.Close()

	cfg := vaultTestHTTPConfig(t, server.URL, "tenant-a", server.Client())
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	select {
	case got := <-calls:
		t.Fatalf("constructor made network call: %q", got)
	default:
	}
	if _, err := client.Summary(t.Context(), connector.SummaryRequest{AccountID: domain.ID{1}, TenantID: "tenant-a",
		Datasets: []domain.AccountDataset{"aero-vault.buckets"}}); err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if got := <-calls; got != "token Basic dmF1bHQtY2xpZW50OmNsaWVudC1zZWNyZXQ=" {
		t.Fatalf("token request = %q", got)
	}
	if got := <-calls; got != "source Bearer "+vaultTestAccessToken {
		t.Fatalf("source request = %q", got)
	}
}

func TestNewWithCapabilitiesPropagatesCommandContract(t *testing.T) {
	seen := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeVaultOAuthToken(w, r) {
			return
		}
		seen <- r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization") + " " + r.Header.Get("Idempotency-Key")
		if r.URL.Path == "/erase" {
			_, _ = w.Write([]byte(`{"operation_id":"logical-1","source_operation_id":"vault-op-1","status":"accepted"}`))
			return
		}
		_, _ = w.Write([]byte(`{"operation_id":"logical-1","source_operation_id":"vault-op-1","status":"completed"}`))
	}))
	defer server.Close()

	cfg := vaultTestHTTPConfig(t, server.URL, "tenant-a", server.Client())
	cfg.ErasePath = "/erase"
	cfg.OperationStatusPath = "/status"
	client, err := NewWithCapabilities(cfg, Capabilities{Erase: true, OperationStatus: true})
	if err != nil {
		t.Fatalf("NewWithCapabilities: %v", err)
	}
	command, ok := client.(connector.CommandConnector)
	if !ok {
		t.Fatal("enabled erase capability is not exposed")
	}
	status, ok := client.(connector.OperationStatusConnector)
	if !ok {
		t.Fatal("enabled operation-status capability is not exposed")
	}
	if _, err := command.Erase(t.Context(), connector.EraseRequest{AccountID: domain.ID{1}, TenantID: "tenant-a",
		OperationID: "logical-1", Datasets: []domain.AccountDataset{"aero-vault.buckets"}, IdempotencyKey: "erase-1"}); err != nil {
		t.Fatalf("Erase: %v", err)
	}
	result, err := status.OperationStatus(t.Context(), connector.OperationStatusRequest{AccountID: domain.ID{1}, TenantID: "tenant-a",
		OperationID: "logical-1", SourceOperationID: "vault-op-1", IdempotencyKey: "erase-1"})
	if err != nil || result.Status != "completed" {
		t.Fatalf("OperationStatus = %#v, %v", result, err)
	}
	if got := <-seen; got != "POST /erase Bearer "+vaultTestAccessToken+" erase-1" {
		t.Fatalf("erase request = %q", got)
	}
	if got := <-seen; got != "GET /status Bearer "+vaultTestAccessToken+" erase-1" {
		t.Fatalf("status request = %q", got)
	}
}

func TestNewIsReadOnlyByDefault(t *testing.T) {
	client, err := New(vaultTestHTTPConfig(t, "http://127.0.0.1:8080", "tenant-a", nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := client.(connector.CommandConnector); ok {
		t.Fatal("read-only Vault connector unexpectedly implements CommandConnector")
	}
	if _, ok := client.(connector.OperationStatusConnector); ok {
		t.Fatal("read-only Vault connector unexpectedly implements OperationStatusConnector")
	}
}

func TestNewWithCapabilitiesExposesOnlyEnabledInterfaces(t *testing.T) {
	cases := []struct {
		name       string
		capability Capabilities
		erasePath  string
		statusPath string
		wantErase  bool
		wantStatus bool
	}{
		{name: "none"},
		{name: "erase", capability: Capabilities{Erase: true}, erasePath: "/erase", wantErase: true},
		{name: "status", capability: Capabilities{OperationStatus: true}, statusPath: "/status", wantStatus: true},
		{name: "both", capability: Capabilities{Erase: true, OperationStatus: true}, erasePath: "/erase", statusPath: "/status", wantErase: true, wantStatus: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := vaultTestHTTPConfig(t, "http://127.0.0.1:8080", "tenant-a", nil)
			cfg.ErasePath = tc.erasePath
			cfg.OperationStatusPath = tc.statusPath
			client, err := NewWithCapabilities(cfg, tc.capability)
			if err != nil {
				t.Fatalf("NewWithCapabilities: %v", err)
			}
			_, gotErase := client.(connector.CommandConnector)
			_, gotStatus := client.(connector.OperationStatusConnector)
			if gotErase != tc.wantErase || gotStatus != tc.wantStatus {
				t.Fatalf("optional interfaces = erase:%t/status:%t, want erase:%t/status:%t", gotErase, gotStatus, tc.wantErase, tc.wantStatus)
			}
		})
	}
}

func TestNewWithCapabilitiesRejectsPathMismatchesBeforeConstruction(t *testing.T) {
	cases := []struct {
		name       string
		capability Capabilities
		erasePath  string
		statusPath string
	}{
		{name: "erase path without capability", erasePath: "/erase"},
		{name: "erase capability without path", capability: Capabilities{Erase: true}},
		{name: "status path without capability", statusPath: "/status"},
		{name: "status capability without path", capability: Capabilities{OperationStatus: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewWithCapabilities(connector.HTTPConfig{Endpoint: "https://vault.example",
				ErasePath: tc.erasePath, OperationStatusPath: tc.statusPath}, tc.capability)
			if err == nil {
				t.Fatal("path/capability mismatch was accepted")
			}
		})
	}
}

func TestNewRejectsAmbiguousVaultPaths(t *testing.T) {
	for _, path := range []string{"relative", "/../erase", "/%2e%2e/erase", "/%2525252e%2525252e/erase", "/erase/%00"} {
		t.Run(path, func(t *testing.T) {
			cfg := vaultTestHTTPConfig(t, "http://127.0.0.1:8080", "tenant-a", nil)
			cfg.SummaryPath = path
			if _, err := New(cfg); err == nil {
				t.Fatal("ambiguous Vault path was accepted")
			}
		})
	}
}

//nolint:gosec // structurally valid, non-secret JWT fixture used only by tests.
const vaultTestAccessToken = "eyJhbGciOiJSUzI1NiIsInR5cCI6ImF0K2p3dCJ9.eyJpc3MiOiJodHRwczovL3NuYXBsaW5rLmV4YW1wbGUiLCJzdWIiOiJ2YXVsdC1jbGllbnQiLCJhdWQiOiJhZXJvLXZhdWx0IiwiY2xpZW50X2lkIjoidmF1bHQtY2xpZW50IiwianRpIjoidmF1bHQtdGVzdC0xIiwic2NvcGUiOiJyZWFkIiwiZXhwIjo0MTAyNDQ0ODAwfQ.signature"

func vaultTestHTTPConfig(t *testing.T, endpoint, tenant string, client *http.Client) connector.HTTPConfig {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse Vault endpoint: %v", err)
	}
	return connector.HTTPConfig{
		Endpoint: endpoint, AllowInsecureLoopback: true, Environment: "test", Client: client,
		OAuthTenantID: tenant, OAuthAllowedHosts: []string{parsed.Scheme + "://" + parsed.Host},
		OAuth: connector.OAuthClientCredentialsConfig{TokenURL: strings.TrimRight(endpoint, "/") + "/token",
			ClientID: "vault-client", ClientSecret: "client-secret", Scope: "read", Resource: "aero-vault"},
		MaxAttempts: 1,
	}
}

func writeVaultOAuthToken(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/token" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": vaultTestAccessToken, "token_type": "Bearer", "expires_in": 3600,
	})
	return true
}
