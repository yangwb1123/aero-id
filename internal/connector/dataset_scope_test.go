package connector

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestAeroIMDatasetScopeRejectsInvalidRequestsBeforeTransport(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: server.URL,
		SummaryPath: "/summary", ErasePath: "/erase", BearerToken: "source-token", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		datasets []domain.AccountDataset
	}{
		{name: "nil"},
		{name: "empty", datasets: []domain.AccountDataset{}},
		{name: "unknown", datasets: []domain.AccountDataset{"aero-im.unknown"}},
		{name: "duplicate", datasets: []domain.AccountDataset{"aero-im.profile", "aero-im.profile"}},
		{name: "snaplink", datasets: []domain.AccountDataset{"snaplink.identity"}},
		{name: "mixed", datasets: []domain.AccountDataset{"aero-im.profile", "aero-vault.tenants"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Summary(t.Context(), SummaryRequest{Datasets: tc.datasets})
			assertInvalidDatasetScope(t, err)
			_, err = client.Erase(t.Context(), EraseRequest{Datasets: tc.datasets})
			assertInvalidDatasetScope(t, err)
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("source calls = %d, want 0 for invalid scopes", got)
	}
}

func TestAeroIMDatasetScopeRejectsInvalidRequestsBeforeOAuth(t *testing.T) {
	var tokenCalls, sourceCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			_, _ = io.WriteString(w, `{"access_token":"source-token","token_type":"Bearer","expires_in":300}`)
		case "/summary", "/erase":
			sourceCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: server.URL,
		SummaryPath: "/summary", ErasePath: "/erase", AllowInsecureLoopback: true, Environment: "test",
		Client: server.Client(), OAuth: OAuthClientCredentialsConfig{TokenURL: server.URL + "/token",
			ClientID: "aero-id", ClientSecret: "source-secret", Scope: "account:summary:read", Resource: "aero-im"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Summary(t.Context(), SummaryRequest{Datasets: []domain.AccountDataset{"aero-im.unknown"}})
	assertInvalidDatasetScope(t, err)
	_, err = client.Erase(t.Context(), EraseRequest{Datasets: []domain.AccountDataset{"aero-im.unknown"}, IdempotencyKey: "erase-1"})
	assertInvalidDatasetScope(t, err)
	if tokenCalls.Load() != 0 || sourceCalls.Load() != 0 {
		t.Fatalf("token/source calls = %d/%d, want 0/0", tokenCalls.Load(), sourceCalls.Load())
	}
}

func TestAeroIMDatasetScopeForwardsExactSelection(t *testing.T) {
	all := []domain.AccountDataset{"aero-im.profile", "aero-im.workspaces", "aero-im.activity_summary", "aero-im.notifications"}
	selections := [][]domain.AccountDataset{all[:1], {all[3], all[0], all[2]}}
	var summaryDatasets, eraseDatasets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/summary" {
			summaryDatasets = append([]string(nil), r.URL.Query()["dataset"]...)
			_, _ = io.WriteString(w, `{"source_region":"west","version":1,"generated_at":"2026-08-20T00:00:00Z","datasets":{}}`)
			return
		}
		if r.URL.Path == "/erase" {
			var body struct {
				Datasets []domain.AccountDataset `json:"datasets"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode erase body: %v", err)
			}
			eraseDatasets = make([]string, len(body.Datasets))
			for i, dataset := range body.Datasets {
				eraseDatasets[i] = string(dataset)
			}
			_, _ = io.WriteString(w, `{"operation_id":"source-op","status":"completed"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: server.URL,
		SummaryPath: "/summary", ErasePath: "/erase", BearerToken: "source-token", MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range selections {
		if _, err := client.Summary(t.Context(), SummaryRequest{Datasets: selection}); err != nil {
			t.Fatalf("summary %v: %v", selection, err)
		}
		want := make([]string, len(selection))
		for i, dataset := range selection {
			want[i] = string(dataset)
		}
		if len(summaryDatasets) != len(want) {
			t.Fatalf("summary datasets = %v, want %v", summaryDatasets, want)
		}
		for i := range want {
			if summaryDatasets[i] != want[i] {
				t.Fatalf("summary datasets = %v, want %v", summaryDatasets, want)
			}
		}
		if _, err := client.Erase(t.Context(), EraseRequest{Datasets: selection, IdempotencyKey: "erase-1"}); err != nil {
			t.Fatalf("erase %v: %v", selection, err)
		}
		if len(eraseDatasets) != len(want) {
			t.Fatalf("erase datasets = %v, want %v", eraseDatasets, want)
		}
		for i := range want {
			if eraseDatasets[i] != want[i] {
				t.Fatalf("erase datasets = %v, want %v", eraseDatasets, want)
			}
		}
	}
}

func assertInvalidDatasetScope(t *testing.T, err error) {
	t.Helper()
	assertInvalidDatasetScopeForSource(t, SourceAeroIM, err)
}

func assertInvalidDatasetScopeForSource(t *testing.T, source Source, err error) {
	t.Helper()
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Source != source || remote.Kind != ErrorInvalid ||
		remote.Code != "source.dataset_scope_invalid" || remote.Temporary {
		t.Fatalf("error = %+v, want %s non-temporary invalid dataset scope", err, source)
	}
	if strings.Contains(err.Error(), "profile") || strings.Contains(err.Error(), "tenants") {
		t.Fatalf("dataset value leaked through the error: %v", err)
	}
}

func TestResolveDatasetScopeModesAndDefensiveCopies(t *testing.T) {
	vault := []domain.AccountDataset{"aero-vault.usage", "aero-vault.tenants"}
	got, err := ResolveDatasetScope(SourceAeroVault, vault, DatasetScopeSummary)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, vault) {
		t.Fatalf("resolved order = %v, want %v", got, vault)
	}
	got[0] = "aero-vault.buckets"
	if vault[0] != "aero-vault.usage" {
		t.Fatal("resolver returned a slice aliased to the caller")
	}

	allVault := []domain.AccountDataset{"aero-vault.tenants", "aero-vault.buckets", "aero-vault.usage"}
	for _, mode := range []DatasetScopeMode{DatasetScopeDestructive, DatasetScopeStored} {
		_, err := ResolveDatasetScope(SourceAeroVault, []domain.AccountDataset{}, mode)
		assertInvalidDatasetScopeForSource(t, SourceAeroVault, err)
	}
	got, err = ResolveDatasetScope(SourceAeroVault, []any{}, DatasetScopeSummary)
	if err != nil || !reflect.DeepEqual(got, allVault) {
		t.Fatalf("empty Vault summary = %v/%v, want registry order %v", got, err, allVault)
	}
	if got, err := ResolveDatasetScope(SourceAeroVault, allVault, DatasetScopeMode("unknown")); err == nil || got != nil {
		t.Fatalf("unknown scope mode = %v/%v, want invalid", got, err)
	}
	oversized := make([]any, len(domain.AccountDatasetRegistry())+1)
	if got, err := ResolveDatasetScope(SourceAeroVault, oversized, DatasetScopeSummary); err == nil || got != nil {
		t.Fatalf("oversized scope = %v/%v, want invalid", got, err)
	}
	if got, err := ResolveDatasetScope(SourceAeroIM, []string{}, DatasetScopeSummary); err == nil || got != nil {
		t.Fatalf("empty Aero IM summary = %v/%v, want invalid", got, err)
	}
	if got, err := ResolveDatasetScope(SourceSnaplink, []domain.AccountDataset(nil), DatasetScopeSummary); err != nil || len(got) != 0 {
		t.Fatalf("empty legacy summary = %v/%v, want accepted empty scope", got, err)
	}

	invalid := []any{
		[]any{"aero-vault.tenants", "aero-vault.tenants"},
		[]any{"aero-vault.tenants", "aero-im.profile"},
		[]any{"aero-vault.unknown"},
		[]any{"aero-vault.tenants", 7},
		[]int{1},
		nil,
	}
	for _, raw := range invalid {
		_, err := ResolveDatasetScope(SourceAeroVault, raw, DatasetScopeSummary)
		if err == nil {
			t.Fatalf("raw %#v was accepted", raw)
		}
		assertInvalidDatasetScopeForSource(t, SourceAeroVault, err)
	}
}

func TestLegacySnaplinkEmptySummaryKeepsSourceWideCoverage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if values := r.URL.Query()["dataset"]; len(values) != 0 {
			t.Fatalf("legacy empty summary sent dataset parameters: %v", values)
		}
		_, _ = io.WriteString(w, `{"source_region":"local","version":1,"generated_at":"2026-08-20T00:00:00Z","datasets":{}}`)
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceSnaplink, HTTPConfig{Endpoint: server.URL,
		SummaryPath: "/summary", AllowInsecureLoopback: true, Environment: "test", Client: server.Client(),
		BearerToken: "snaplink-token"})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := client.Summary(t.Context(), SummaryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.AccountDataset{"snaplink.identity", "snaplink.tenants", "snaplink.permissions", "snaplink.security"}
	if !reflect.DeepEqual(summary.Coverage.Datasets, want) {
		t.Fatalf("legacy coverage = %v, want %v", summary.Coverage.Datasets, want)
	}
}

func TestLegacySnaplinkEmptyEraseKeepsWireCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/erase" {
			t.Fatalf("path = %q, want /erase", r.URL.Path)
		}
		var body struct {
			Datasets []domain.AccountDataset `json:"datasets"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Datasets == nil || len(body.Datasets) != 0 {
			t.Fatalf("legacy datasets = %#v, want an empty array", body.Datasets)
		}
		_, _ = io.WriteString(w, `{"status":"completed"}`)
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceSnaplink, HTTPConfig{Endpoint: server.URL,
		ErasePath: "/erase", AllowInsecureLoopback: true, Environment: "test", BearerToken: "snaplink-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Erase(t.Context(), EraseRequest{IdempotencyKey: "legacy-erase"}); err != nil {
		t.Fatalf("legacy empty erase: %v", err)
	}
}

func TestAeroVaultScopeValidationOrdersOAuthAndHTTPEffects(t *testing.T) {
	var tokenCalls, sourceCalls atomic.Int32
	var summaryDatasets, eraseDatasets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"vault-token","token_type":"Bearer","expires_in":300}`)
		case "/summary":
			sourceCalls.Add(1)
			summaryDatasets = append([]string(nil), r.URL.Query()["dataset"]...)
			_, _ = io.WriteString(w, `{"source_region":"local","version":1,"generated_at":"2026-08-20T00:00:00Z","datasets":{}}`)
		case "/erase":
			sourceCalls.Add(1)
			var body struct {
				Datasets []string `json:"datasets"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode erase body: %v", err)
			}
			eraseDatasets = append([]string(nil), body.Datasets...)
			_, _ = io.WriteString(w, `{"status":"completed","source_operation_id":"source-op"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroVault, HTTPConfig{Endpoint: server.URL,
		SummaryPath: "/summary", ErasePath: "/erase", AllowInsecureLoopback: true, Environment: "test",
		Client: server.Client(), OAuth: OAuthClientCredentialsConfig{TokenURL: server.URL + "/token",
			ClientID: "vault-client", ClientSecret: "vault-secret", Scope: "read", Resource: "aero-vault"}})
	if err != nil {
		t.Fatal(err)
	}
	invalids := [][]domain.AccountDataset{
		{"aero-vault.tenants", "aero-vault.tenants"},
		{"aero-vault.unknown"},
		{"aero-vault.tenants", "aero-im.profile"},
	}
	for _, invalid := range invalids {
		_, err = client.Summary(t.Context(), SummaryRequest{Datasets: invalid})
		assertInvalidDatasetScopeForSource(t, SourceAeroVault, err)
		_, err = client.Erase(t.Context(), EraseRequest{Datasets: invalid, IdempotencyKey: "erase-invalid"})
		assertInvalidDatasetScopeForSource(t, SourceAeroVault, err)
	}
	_, err = client.Erase(t.Context(), EraseRequest{Datasets: nil})
	assertInvalidDatasetScopeForSource(t, SourceAeroVault, err)
	if tokenCalls.Load() != 0 || sourceCalls.Load() != 0 {
		t.Fatalf("invalid scope calls = token:%d source:%d, want 0/0", tokenCalls.Load(), sourceCalls.Load())
	}

	caller := []domain.AccountDataset{"aero-vault.usage", "aero-vault.tenants"}
	before := append([]domain.AccountDataset(nil), caller...)
	if _, err := client.Summary(t.Context(), SummaryRequest{Datasets: nil}); err != nil {
		t.Fatalf("summary: %v (token=%d source=%d)", err, tokenCalls.Load(), sourceCalls.Load())
	}
	if !reflect.DeepEqual(caller, before) {
		t.Fatal("connector changed caller-owned dataset input")
	}
	if want := []string{"aero-vault.tenants", "aero-vault.buckets", "aero-vault.usage"}; !reflect.DeepEqual(summaryDatasets, want) {
		t.Fatalf("empty summary datasets = %v, want registry order %v", summaryDatasets, want)
	}
	if _, err := client.Erase(t.Context(), EraseRequest{Datasets: caller, IdempotencyKey: "erase-1"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(caller, before) {
		t.Fatal("erase connector changed caller-owned dataset input")
	}
	if !reflect.DeepEqual(eraseDatasets, []string{"aero-vault.usage", "aero-vault.tenants"}) {
		t.Fatalf("erase datasets = %v, want caller order", eraseDatasets)
	}
	if tokenCalls.Load() != 1 || sourceCalls.Load() != 2 {
		t.Fatalf("valid calls = token:%d source:%d, want 1/2", tokenCalls.Load(), sourceCalls.Load())
	}
}
