package connector

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

// TestJSONSummaryConnectorSurfacesSkippedDatasets: a payload carrying datasets
// that are unregistered or registered to a different source must surface them
// in SkippedDatasets (raw, sorted names), project zero snapshots for them, and
// downgrade Complete to false so reconcile marking is suppressed downstream.
func TestJSONSummaryConnectorSurfacesSkippedDatasets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"source_region":"west","version":5,"generated_at":"2026-08-16T00:00:00Z","complete":true,` +
			`"datasets":{"aero-vault.new_thing":{"count":1},"aero-im.profile":{"display_name":"Ada"}}}`))
	}))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{SummaryPath: "/summary", Client: server.Client()})
	summary, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{7}, TenantID: vaultTestTenant,
		Datasets: []domain.AccountDataset{domain.AccountDataset("aero-vault.tenants")}})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Complete {
		t.Fatal("Complete must be downgraded to false when any received dataset was skipped")
	}
	want := []string{"aero-im.profile", "aero-vault.new_thing"}
	if !reflect.DeepEqual(summary.SkippedDatasets, want) {
		t.Fatalf("SkippedDatasets = %v, want %v (raw, ascending)", summary.SkippedDatasets, want)
	}
	if len(summary.Snapshots) != 0 {
		t.Fatalf("snapshots = %d, want 0 (skipped datasets must not be projected): %#v", len(summary.Snapshots), summary.Snapshots)
	}
	if summary.Source != SourceAeroVault || summary.Version != 5 || summary.SourceRegion != "west" {
		t.Fatalf("non-dataset fields changed: source=%s version=%d region=%s", summary.Source, summary.Version, summary.SourceRegion)
	}
}

// TestJSONSummaryConnectorNoFalseDowngradeForRegisteredVaultDatasets: a payload
// whose datasets are all registered to the connector's source must keep
// Complete == payload.Complete and report no skips.
func TestJSONSummaryConnectorNoFalseDowngradeForRegisteredVaultDatasets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"source_region":"west","version":4,"generated_at":"2026-08-20T00:00:00Z","complete":true,"datasets":{` +
			`"aero-vault.tenants":{"count":1},"aero-vault.buckets":{"count":2},"aero-vault.usage":{"bytes":100}}}`))
	}))
	defer server.Close()
	client := newVaultTestConnector(t, server.URL, HTTPConfig{SummaryPath: "/summary", Client: server.Client()})
	summary, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{8}, TenantID: vaultTestTenant,
		Datasets: []domain.AccountDataset{domain.AccountDataset("aero-vault.tenants"), domain.AccountDataset("aero-vault.buckets"), domain.AccountDataset("aero-vault.usage")}})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Complete {
		t.Fatal("Complete must stay true when every received dataset is registered to the source")
	}
	if len(summary.SkippedDatasets) != 0 {
		t.Fatalf("SkippedDatasets = %v, want empty", summary.SkippedDatasets)
	}
	if len(summary.Snapshots) != 3 {
		t.Fatalf("snapshots = %d, want 3", len(summary.Snapshots))
	}
	for _, snapshot := range summary.Snapshots {
		if snapshot.Source != string(SourceAeroVault) {
			t.Fatalf("snapshot source = %q, want %q", snapshot.Source, string(SourceAeroVault))
		}
		switch snapshot.Dataset {
		case domain.AccountDataset("aero-vault.tenants"), domain.AccountDataset("aero-vault.buckets"), domain.AccountDataset("aero-vault.usage"):
		default:
			t.Fatalf("unexpected dataset %q", snapshot.Dataset)
		}
	}
}
