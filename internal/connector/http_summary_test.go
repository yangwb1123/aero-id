package connector

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestJSONSummaryConnectorRedactsSensitiveFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/summary" || r.URL.Query().Get("dataset") != string(domain.AccountDataset("aero-im.profile")) {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		if got := r.Header.Get("X-Request-ID"); got != "request-1" {
			t.Fatalf("X-Request-ID = %q, want request-1", got)
		}
		_, _ = w.Write([]byte(`{"source_region":"west","version":4,"datasets":{"aero-im.profile":{"display_name":"Ada","token":"secret","nested":{"body":"message"}}}}`))
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: server.URL, SummaryPath: "/summary", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, RequestID: "request-1", Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Snapshots) != 1 || summary.Snapshots[0].Data["token"] != nil {
		t.Fatalf("sensitive source data was retained: %#v", summary.Snapshots)
	}
	if summary.Snapshots[0].Data["display_name"] != "Ada" {
		t.Fatalf("safe source data was removed: %#v", summary.Snapshots[0].Data)
	}
}

func TestJSONSummaryConnectorRedactsSnaplinkIdentityCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/summary" || r.URL.Query().Get("dataset") != string(domain.AccountDataset("snaplink.identity")) {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"source_region":"west","version":1,"datasets":{"snaplink.identity":{
			"display_name":"Ada","email":"ada@example.com","verified":true,
			"identity":{"api_key":"sk-1","apikey":"sk-2","jwt":"eyJhbGciOi…","passcode":"123456",
			"otp":"654321","verification_code":"abc-123","access_token":"tok-1","mfa_secret":"mfa-1"}}}}`))
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceSnaplink, HTTPConfig{Endpoint: server.URL, SummaryPath: "/summary", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{5}, RequestID: "request-snaplink", Datasets: []domain.AccountDataset{domain.AccountDataset("snaplink.identity")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1: %#v", len(summary.Snapshots), summary.Snapshots)
	}
	data := summary.Snapshots[0].Data
	if data["display_name"] != "Ada" || data["email"] != "ada@example.com" || data["verified"] != true {
		t.Fatalf("safe fields were dropped or rewritten: %#v", data)
	}
	if containsSensitiveSummaryKey(data) {
		t.Fatalf("credential keys leaked into snaplink summary data: %#v", data)
	}
	identity, ok := data["identity"].(map[string]any)
	if !ok || len(identity) != 0 {
		t.Fatalf("nested identity map not emptied: %#v", data["identity"])
	}
}

func TestJSONSummaryConnectorRedactsCamelCaseCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/summary" || r.URL.Query().Get("dataset") != string(domain.AccountDataset("aero-vault.tenants")) {
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"source_region":"west","version":1,"datasets":{"aero-vault.tenants":{"display_name":"Ada","owner":{"accessKey":"AKIA…","privateKey":"-----BEGIN…"}}}}`))
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroVault, HTTPConfig{Endpoint: server.URL, SummaryPath: "/summary", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{4}, RequestID: "request-vault", Datasets: []domain.AccountDataset{domain.AccountDataset("aero-vault.tenants")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1: %#v", len(summary.Snapshots), summary.Snapshots)
	}
	data := summary.Snapshots[0].Data
	if data["display_name"] != "Ada" {
		t.Fatalf("safe source data was removed: %#v", data)
	}
	if containsSensitiveSummaryKey(data) {
		t.Fatalf("credential keys leaked into summary data: %#v", data)
	}
}

func TestJSONSummaryConnectorEraseUsesIdempotency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "erase-1" || r.Header.Get("X-Aero-Operation-ID") != "op-1" || r.Header.Get("X-Request-ID") != "request-2" {
			t.Fatalf("missing command context: method=%s headers=%v", r.Method, r.Header)
		}
		_, _ = w.Write([]byte(`{"operation_id":"remote-1","status":"accepted"}`))
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroVault, HTTPConfig{Endpoint: server.URL, ErasePath: "/erase", Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{2}, RequestID: "request-2", IdempotencyKey: "erase-1", OperationID: "op-1"})
	if err != nil || result.SourceOperationID != "remote-1" {
		t.Fatalf("Erase() = %#v, %v", result, err)
	}
}

func TestJSONSummaryConnectorRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 128))) }))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceSnaplink, HTTPConfig{Endpoint: server.URL, MaxBodyBytes: 16, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{3}}); err == nil {
		t.Fatal("oversized response was accepted")
	}
}
