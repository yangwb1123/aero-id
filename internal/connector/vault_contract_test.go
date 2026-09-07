package connector

import (
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

const vaultCanonicalSummaryPath = "/v1/internal/account-summary"

func TestVaultSummaryWireBindsCanonicalPathAndTenant(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != vaultCanonicalSummaryPath {
			t.Errorf("path = %q, want %q", r.URL.Path, vaultCanonicalSummaryPath)
		}
		if r.URL.Query().Get("reconcile") != "" {
			t.Errorf("Vault summary carried unsupported reconcile query: %q", r.URL.Query().Get("reconcile"))
		}
		for name, want := range map[string]string{
			"Authorization":    "Bearer " + vaultTestAccessToken,
			"X-Aero-Tenant":    vaultTestTenant,
			"X-Aero-Tenant-ID": vaultTestTenant,
		} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		_, _ = io.WriteString(w, `{"source_region":"local","version":7,"generated_at":"2026-08-30T00:00:00Z","complete":true,"datasets":{"aero-vault.tenants":{}}}`)
	}))
	defer server.Close()

	client := newVaultTestConnector(t, server.URL, HTTPConfig{SummaryPath: vaultCanonicalSummaryPath,
		MaxAttempts: 1, Client: server.Client()})
	summary, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, TenantID: vaultTestTenant,
		CanonicalUID: "vault-account", Region: "local", Datasets: []domain.AccountDataset{"aero-vault.tenants"}})
	if err != nil {
		t.Fatalf("Vault summary: %v", err)
	}
	if summary.Source != SourceAeroVault || summary.Version != 7 || calls.Load() != 1 {
		t.Fatalf("summary = %#v, calls = %d, want Vault/version 7/one call", summary, calls.Load())
	}
}

func TestVaultBindsOmittedRegionToItsConfiguredCell(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.URL.Query().Get("region"); got != "local" || r.Header.Get("X-Aero-Region") != "local" {
			t.Errorf("region query/header = %q/%q, want local/local", got, r.Header.Get("X-Aero-Region"))
		}
		_, _ = io.WriteString(w, `{"source_region":"local","version":1,"generated_at":"2026-08-30T00:00:00Z","complete":true,"datasets":{"aero-vault.tenants":{}}}`)
	}))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{Region: "local", Client: server.Client(), MaxAttempts: 1})
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{9}, TenantID: vaultTestTenant,
		Datasets: []domain.AccountDataset{"aero-vault.tenants"}}); err != nil {
		t.Fatalf("region-bound summary: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("source calls = %d, want one", calls.Load())
	}
}

func TestVaultRejectsCrossRegionRequestBeforeTokenOrSourceIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{Region: "local", Client: server.Client()})
	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{10}, TenantID: vaultTestTenant,
		Region: "eu-west-1", Datasets: []domain.AccountDataset{"aero-vault.tenants"}})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Kind != ErrorForbidden || remote.Code != "source.region_credential_unbound" {
		t.Fatalf("cross-region error = %+v, want forbidden/source.region_credential_unbound", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("source calls = %d, want zero for cross-region request", calls.Load())
	}
}

func TestVaultRejectsASecondTenantBeforeTokenOrSourceIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{SummaryPath: vaultCanonicalSummaryPath,
		Client: server.Client()})

	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, TenantID: "tenant-b",
		Datasets: []domain.AccountDataset{"aero-vault.tenants"}})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Kind != ErrorForbidden || remote.Code != "source.tenant_credential_unbound" {
		t.Fatalf("tenant mismatch error = %+v, want forbidden/source.tenant_credential_unbound", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("source calls = %d, want zero for an unbound tenant", calls.Load())
	}
}

func TestVaultSummaryRetries429And503WithinBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := calls.Add(1)
		if r.Header.Get("X-Aero-Tenant") != vaultTestTenant {
			t.Errorf("attempt %d tenant = %q, want %q", attempt, r.Header.Get("X-Aero-Tenant"), vaultTestTenant)
		}
		switch attempt {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = io.WriteString(w, `{"source_region":"local","version":8,"generated_at":"2026-08-30T00:00:00Z","complete":true,"datasets":{"aero-vault.tenants":{}}}`)
		}
	}))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{SummaryPath: vaultCanonicalSummaryPath,
		MaxAttempts: 3, RetryBackoff: time.Millisecond, Client: server.Client()})

	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{2}, TenantID: vaultTestTenant,
		Datasets: []domain.AccountDataset{"aero-vault.tenants"}}); err != nil {
		t.Fatalf("retrying Vault summary: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("source calls = %d, want exactly three attempts", got)
	}
}

func TestVaultSummaryBodyTimeoutMapsToTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{SummaryPath: vaultCanonicalSummaryPath,
		Timeout: 40 * time.Millisecond, MaxAttempts: 1, Client: server.Client()})

	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{3}, TenantID: vaultTestTenant,
		Datasets: []domain.AccountDataset{"aero-vault.tenants"}})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Kind != ErrorTimeout || !remote.Temporary {
		t.Fatalf("body timeout = %+v, want temporary timeout RemoteError", err)
	}
}

func TestVaultOAuthBodyTimeoutDoesNotCallVault(t *testing.T) {
	var sourceCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
			return
		}
		sourceCalls.Add(1)
	}))
	defer server.Close()
	cfg := HTTPConfig{Endpoint: server.URL, SummaryPath: vaultCanonicalSummaryPath, AllowInsecureLoopback: true,
		Environment: "test", OAuthTenantID: vaultTestTenant, OAuthAllowedHosts: []string{oauthTestOrigin(t, server.URL)},
		OAuth: OAuthClientCredentialsConfig{TokenURL: server.URL + "/token", ClientID: "vault-client",
			ClientSecret: "vault-client-secret", Scope: "read", Resource: "aero-vault"},
		Timeout: 40 * time.Millisecond, MaxAttempts: 1, Client: server.Client()}
	client, err := NewJSONSummaryConnector(SourceAeroVault, cfg)
	if err != nil {
		t.Fatalf("Vault OAuth constructor: %v", err)
	}
	_, err = client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{4}, TenantID: vaultTestTenant,
		Datasets: []domain.AccountDataset{"aero-vault.tenants"}})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Kind != ErrorTimeout || remote.Code != "source.oauth_token_unavailable" {
		if remote != nil {
			t.Logf("remote kind=%q code=%q cause=%T %v", remote.Kind, remote.Code, remote.Cause, remote.Cause)
		}
		t.Fatalf("OAuth body timeout = %+v, want source.oauth_token_unavailable timeout", err)
	}
	if sourceCalls.Load() != 0 {
		t.Fatalf("Vault source calls = %d, want zero after token timeout", sourceCalls.Load())
	}
}

func TestVaultEraseRetriesWithStableIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Aero-Tenant") != vaultTestTenant || r.Header.Get("Idempotency-Key") != "erase-vault-1" {
			t.Errorf("command context tenant/key = %q/%q", r.Header.Get("X-Aero-Tenant"), r.Header.Get("Idempotency-Key"))
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"status":"completed","source_operation_id":"vault-op-1"}`)
	}))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase",
		MaxAttempts: 2, RetryBackoff: time.Millisecond, Client: server.Client()})

	result, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{5}, TenantID: vaultTestTenant,
		OperationID: "logical-erase-1", Datasets: []domain.AccountDataset{"aero-vault.tenants"}, IdempotencyKey: "erase-vault-1"})
	if err != nil || result == nil || result.Status != "completed" || calls.Load() != 2 {
		t.Fatalf("Vault erase = %#v/%v after %d calls, want completed/2", result, err, calls.Load())
	}
}

func TestVaultEraseTimeoutIsUnknownOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase",
		Timeout: 40 * time.Millisecond, MaxAttempts: 1, Client: server.Client()})

	_, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{6}, TenantID: vaultTestTenant,
		Datasets: []domain.AccountDataset{"aero-vault.tenants"}, IdempotencyKey: "erase-timeout"})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Kind != ErrorTimeout || !remote.Temporary {
		t.Fatalf("Vault erase timeout = %+v, want temporary timeout/unknown outcome", err)
	}
}

func TestVaultStatusMalformedOrUncorrelated2xxRemainsUnknown(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing operation IDs", body: `{"status":"completed"}`},
		{name: "mismatched operation ID", body: `{"operation_id":"other","source_operation_id":"vault-op-1","status":"completed"}`},
		{name: "mismatched source operation ID", body: `{"operation_id":"logical-1","source_operation_id":"other","status":"completed"}`},
		{name: "malformed JSON", body: `{"status":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := newVaultTestConnector(t, server.URL, HTTPConfig{OperationStatusPath: "/status", Client: server.Client()})
			result, err := client.OperationStatus(t.Context(), OperationStatusRequest{AccountID: domain.ID{7}, TenantID: vaultTestTenant,
				OperationID: "logical-1", SourceOperationID: "vault-op-1", IdempotencyKey: "erase-1"})
			var remote *RemoteError
			if result != nil || !errors.As(err, &remote) || remote.Kind != ErrorUnknown || !remote.Temporary {
				t.Fatalf("status result/error = %#v/%+v, want temporary unknown", result, err)
			}
		})
	}
}

func TestVaultOversizedEraseResponseRemainsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 32))
	}))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase", MaxBodyBytes: 16,
		Client: server.Client()})
	_, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{8}, TenantID: vaultTestTenant,
		Datasets: []domain.AccountDataset{"aero-vault.tenants"}, IdempotencyKey: "erase-large"})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Kind != ErrorUnknown || remote.Code != "source.response_too_large" {
		t.Fatalf("oversized erase = %+v, want unknown/source.response_too_large", err)
	}
}
