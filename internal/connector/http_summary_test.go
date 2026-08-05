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
		if r.URL.Path != "/summary" || r.URL.Query().Get("dataset") != string(domain.DatasetAeroIMProfile) {
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
	summary, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, RequestID: "request-1", Datasets: []domain.AccountDataset{domain.DatasetAeroIMProfile}})
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
