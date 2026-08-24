package connector

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestConnectorClientCredentialsCachesAudienceBoundToken(t *testing.T) {
	var mu sync.Mutex
	tokenCalls := 0
	sourceCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/token":
			tokenCalls++
			clientID, clientSecret, ok := r.BasicAuth()
			if !ok || clientID != "aero-id" || clientSecret != "source-secret" {
				t.Fatalf("token endpoint basic auth = %q/%q/%v", clientID, clientSecret, ok)
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			wantForm := url.Values{"grant_type": {"client_credentials"}, "scope": {"account:summary:read"}, "resource": {"aero-im"}}
			if r.Form.Encode() != wantForm.Encode() {
				t.Fatalf("token form = %q, want %q", r.Form.Encode(), wantForm.Encode())
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"source-token","token_type":"Bearer","expires_in":300}`))
		case "/summary":
			sourceCalls++
			if got := r.Header.Get("Authorization"); got != "Bearer source-token" {
				t.Fatalf("Authorization = %q", got)
			}
			writeOAuthSummary(t, w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 1, OAuth: OAuthClientCredentialsConfig{
		TokenURL: server.URL + "/token", ClientID: "aero-id", ClientSecret: "source-secret",
		Scope: "account:summary:read", Resource: "aero-im",
	}})
	for range 2 {
		if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}}); err != nil {
			t.Fatalf("summary: %v", err)
		}
	}
	if tokenCalls != 1 || sourceCalls != 2 {
		t.Fatalf("calls = token %d/source %d, want 1/2", tokenCalls, sourceCalls)
	}
}

func TestConnectorClientCredentialsPropagatesAcrossAllConnectorSurfaces(t *testing.T) {
	var mu sync.Mutex
	tokenCalls := 0
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/token":
			tokenCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"source-token","token_type":"Bearer","expires_in":300}`))
		case "/summary", "/erase", "/status":
			paths = append(paths, r.URL.Path+" "+r.Header.Get("Authorization"))
			if r.Header.Get("Authorization") != "Bearer source-token" {
				t.Fatalf("%s Authorization = %q", r.URL.Path, r.Header.Get("Authorization"))
			}
			if r.URL.Path == "/summary" {
				writeOAuthSummary(t, w)
				return
			}
			_, _ = w.Write([]byte(`{"status":"completed","source_operation_id":"source-op"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestConnector(t, server.URL, HTTPConfig{
		ErasePath: "/erase", OperationStatusPath: "/status", MaxAttempts: 1,
		OAuth: OAuthClientCredentialsConfig{TokenURL: server.URL + "/token", ClientID: "aero-id",
			ClientSecret: "source-secret", Scope: "account:summary:read", Resource: "aero-im"},
	})
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}}); err != nil {
		t.Fatalf("summary: %v", err)
	}
	if _, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{1}, IdempotencyKey: "idem-1"}); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if _, err := client.OperationStatus(t.Context(), OperationStatusRequest{AccountID: domain.ID{1}, SourceOperationID: "source-op"}); err != nil {
		t.Fatalf("operation status: %v", err)
	}
	if tokenCalls != 1 || len(paths) != 3 {
		t.Fatalf("token/source calls = %d/%d, want 1/3", tokenCalls, len(paths))
	}
}

func TestConnectorClientCredentialsRefreshesOnceAfterUnauthorized(t *testing.T) {
	var mu sync.Mutex
	tokenCalls := 0
	sourceCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/token":
			tokenCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token-` + string(rune('0'+tokenCalls)) + `","token_type":"Bearer","expires_in":300}`))
		case "/summary":
			sourceCalls++
			if r.Header.Get("Authorization") == "Bearer token-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer token-2" {
				t.Fatalf("refreshed Authorization = %q", got)
			}
			writeOAuthSummary(t, w)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 1, OAuth: OAuthClientCredentialsConfig{
		TokenURL: server.URL + "/token", ClientID: "aero-id", ClientSecret: "source-secret",
		Scope: "account:summary:read", Resource: "aero-im",
	}})
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}}); err != nil {
		t.Fatalf("summary after token refresh: %v", err)
	}
	if tokenCalls != 2 || sourceCalls != 2 {
		t.Fatalf("calls = token %d/source %d, want 2/2", tokenCalls, sourceCalls)
	}
}

func TestConnectorRejectsMixedOrPartialOAuthCredentials(t *testing.T) {
	tests := []HTTPConfig{
		{BearerToken: "legacy", OAuth: OAuthClientCredentialsConfig{TokenURL: "http://127.0.0.1/token", ClientID: "id", ClientSecret: "secret", Scope: "read", Resource: "source"}},
		{OAuth: OAuthClientCredentialsConfig{TokenURL: "http://127.0.0.1/token", ClientID: "id"}},
		{OAuth: OAuthClientCredentialsConfig{TokenURL: "http://source.internal/token", ClientID: "id", ClientSecret: "secret", Scope: "read", Resource: "source"}},
	}
	for i, cfg := range tests {
		cfg.Endpoint = "https://source.example"
		if _, err := NewJSONSummaryConnector(SourceAeroIM, cfg); err == nil {
			t.Fatalf("case %d unexpectedly accepted", i)
		}
	}
}

func writeOAuthSummary(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"source_region": "west", "version": 1, "generated_at": "2026-08-20T00:00:00Z", "datasets": map[string]any{},
	}); err != nil {
		t.Fatalf("encode source response: %v", err)
	}
}
