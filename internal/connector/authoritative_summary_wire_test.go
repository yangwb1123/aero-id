package connector

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

func TestTrustedVaultSummaryWireAndMetrics(t *testing.T) {
	account := wireAccount(8, "tenant-wire", "vault-user", "region-wire")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/internal/account-summary" || query.Get("account_id") != account.ID.String() ||
			query.Get("region") != account.HomeRegion || query.Get("reconcile") != "true" ||
			!equalStrings(query["dataset"], []string{"aero-vault.buckets", "aero-vault.tenants", "aero-vault.usage"}) {
			t.Errorf("Vault query = %s", request.URL.String())
		}
		for header, want := range map[string]string{
			"X-Aero-Account-ID": account.ID.String(), "X-Aero-Canonical-UID": account.CanonicalUID,
			"X-Aero-Actor-UID": "actor-wire", "X-Aero-Tenant-ID": account.TenantID,
			"X-Aero-Region": account.HomeRegion, "X-Request-ID": "request-wire",
			"X-Aero-Operation-ID": "operation-wire",
		} {
			if got := request.Header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		if got := request.Header.Get(accountSummaryTargetAssertionHeader); got != "" {
			t.Errorf("Vault target assertion = %q, want absent", got)
		}
		_, _ = io.WriteString(w, `{"source_region":"region-wire","version":1,"generated_at":"2026-08-30T00:00:00Z","complete":true,"datasets":{"aero-vault.buckets":{},"aero-vault.tenants":{},"aero-vault.usage":{}}}`)
	}))
	defer server.Close()

	registry := metrics.NewRegistry()
	client, err := NewJSONSummaryConnector(SourceAeroVault, HTTPConfig{Endpoint: server.URL,
		AllowInsecureLoopback: true, Environment: "test", Client: server.Client(), Metrics: registry, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewTrustedSummaryRequest(t.Context(), testSummaryAccountLoader{account}, TrustedSummaryRequestInput{
		Source: SourceAeroVault, TenantID: account.TenantID, AccountID: account.ID, ActorUID: "actor-wire",
		RequestID: "request-wire", OperationID: "operation-wire", Reconcile: true,
		Datasets: []domain.AccountDataset{"aero-vault.usage", "aero-vault.tenants", "aero-vault.buckets"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Summary(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	text := metricsText(registry)
	if !strings.Contains(text, `aeroid_source_calls_total{source="aero-vault",operation="summary"} 1`) ||
		strings.Contains(text, `aeroid_source_errors_total{source="aero-vault",operation="summary"} 1`) {
		t.Fatalf("successful Vault metrics = %s", text)
	}
}

func TestTrustedVaultRemoteFailureIsMeasuredButBindingFailureIsNot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	registry := metrics.NewRegistry()
	client, err := NewJSONSummaryConnector(SourceAeroVault, HTTPConfig{Endpoint: server.URL,
		AllowInsecureLoopback: true, Environment: "test", Client: server.Client(), Metrics: registry, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	account := wireAccount(9, "tenant-wire", "vault-user", "region-wire")
	request, err := NewTrustedSummaryRequest(t.Context(), testSummaryAccountLoader{account}, TrustedSummaryRequestInput{
		Source: SourceAeroVault, TenantID: account.TenantID, AccountID: account.ID,
		Datasets: []domain.AccountDataset{"aero-vault.tenants"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Summary(t.Context(), request); err == nil {
		t.Fatal("remote failure was swallowed")
	}
	text := metricsText(registry)
	if !strings.Contains(text, `aeroid_source_calls_total{source="aero-vault",operation="summary"} 1`) ||
		!strings.Contains(text, `aeroid_source_errors_total{source="aero-vault",operation="summary"} 1`) {
		t.Fatalf("remote failure metrics = %s", text)
	}

	beforeBinding := metricsText(registry)
	bad := wireAccount(9, "tenant-wire", "", "region-wire")
	badRequest, err := NewTrustedSummaryRequest(t.Context(), testSummaryAccountLoader{bad}, TrustedSummaryRequestInput{
		Source: SourceAeroVault, TenantID: bad.TenantID, AccountID: bad.ID,
		Datasets: []domain.AccountDataset{"aero-vault.tenants"},
	})
	if err == nil || badRequest.AccountID != (domain.ID{}) {
		t.Fatalf("malformed binding request/error = %#v/%v", badRequest, err)
	}
	if afterBinding := metricsText(registry); afterBinding != beforeBinding {
		t.Fatalf("local binding failure changed source metrics: before=%s after=%s", beforeBinding, afterBinding)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func wireAccount(id byte, tenant, uid, region string) *domain.Account {
	return &domain.Account{ID: domain.ID{id}, TenantID: tenant, CanonicalUID: uid,
		HomeRegion: region, Status: domain.AccountActive}
}

func metricsText(registry *metrics.Registry) string {
	var builder strings.Builder
	registry.WriteText(&builder)
	return builder.String()
}
