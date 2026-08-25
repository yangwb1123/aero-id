package connector

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Source != SourceAeroIM || remote.Kind != ErrorInvalid ||
		remote.Code != "source.dataset_scope_invalid" || remote.Temporary {
		t.Fatalf("error = %+v, want non-temporary invalid dataset scope", err)
	}
	if strings.Contains(err.Error(), "profile") || strings.Contains(err.Error(), "vault") {
		t.Fatalf("dataset value leaked through the error: %v", err)
	}
}
