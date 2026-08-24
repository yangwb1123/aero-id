package connector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

// Contract tests (AGENTS.md): auth context, deadlines, and partial responses
// are part of the connector conformance suite. These three cover the seams
// the fault-injection suite does not: bearer-token propagation on every
// command surface, client-side context deadline enforcement, and the
// operation-status/idempotency path.

// TestConnector_PropagatesBearerTokenOnEverySurface: the configured bearer
// token must be attached to Summary, Erase, and OperationStatus requests —
// auth context is part of the connector contract.
func TestConnector_PropagatesBearerTokenOnEverySurface(t *testing.T) {
	const token = "source-secret-token"
	var sawAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = append(sawAuth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/summary":
			_, _ = w.Write([]byte(`{"source_region":"west","version":1,"generated_at":"2026-08-20T00:00:00Z","datasets":{}}`))
		case "/erase":
			_, _ = w.Write([]byte(`{"status":"completed","source_operation_id":"op-1"}`))
		case "/status":
			_, _ = w.Write([]byte(`{"status":"completed","source_operation_id":"op-1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newTestConnector(t, server.URL, HTTPConfig{
		BearerToken:         token,
		ErasePath:           "/erase",
		OperationStatusPath: "/status",
		MaxAttempts:         1,
	})

	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}}); err != nil {
		t.Fatalf("summary: %v", err)
	}
	if _, err := client.Erase(t.Context(), EraseRequest{
		AccountID: domain.ID{1}, CanonicalUID: "u-1", TenantID: "t-1",
		OperationID: "op-id-1", IdempotencyKey: "idem-1",
	}); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if _, err := client.OperationStatus(t.Context(), OperationStatusRequest{
		AccountID: domain.ID{1}, SourceOperationID: "op-1",
	}); err != nil {
		t.Fatalf("operation status: %v", err)
	}
	if len(sawAuth) != 3 {
		t.Fatalf("saw %d authenticated requests, want 3", len(sawAuth))
	}
	for i, got := range sawAuth {
		if got != "Bearer "+token {
			t.Fatalf("request %d Authorization = %q, want Bearer %s", i, got, token)
		}
	}
}

// TestConnector_PropagatesRegionOnCommandSurfaces: account-affecting source
// commands must carry the account home region on both the transport header and
// query string so a source can route the request to the correct authority.
func TestConnector_PropagatesRegionOnCommandSurfaces(t *testing.T) {
	type requestObservation struct {
		path   string
		header string
		query  string
	}
	var observations []requestObservation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observations = append(observations, requestObservation{path: r.URL.Path,
			header: r.Header.Get("X-Aero-Region"), query: r.URL.Query().Get("region")})
		switch r.URL.Path {
		case "/erase", "/status":
			_, _ = w.Write([]byte(`{"status":"completed","source_operation_id":"op-1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{
		ErasePath:           "/erase",
		OperationStatusPath: "/status",
		MaxAttempts:         1,
	})
	const region = "us-west-2"
	if _, err := client.Erase(t.Context(), EraseRequest{
		AccountID: domain.ID{1}, Region: region, OperationID: "op-id-1", IdempotencyKey: "idem-1",
	}); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if _, err := client.OperationStatus(t.Context(), OperationStatusRequest{
		AccountID: domain.ID{1}, Region: region, OperationID: "op-id-1", SourceOperationID: "op-1",
	}); err != nil {
		t.Fatalf("operation status: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(observations))
	}
	for _, observation := range observations {
		if observation.header != region || observation.query != region {
			t.Errorf("%s region = header %q/query %q, want %q/%q", observation.path,
				observation.header, observation.query, region, region)
		}
	}
}

// TestConnector_ContextDeadlineCancelsInFlightCall: a client-side context
// deadline must abort the call promptly (bounded by the deadline, not the
// server's stall) and map to a stable timeout error, never a hang.
func TestConnector_ContextDeadlineCancelsInFlightCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second) // server stalls well past the deadline
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{Timeout: 2 * time.Second, MaxAttempts: 1})

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := client.Summary(ctx, SummaryRequest{AccountID: domain.ID{1}})
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("call took %v, want prompt cancellation by the client deadline", elapsed)
	}
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("err = %v, want RemoteError", err)
	}
	if remote.Kind != ErrorTimeout || !remote.Temporary {
		t.Fatalf("remote = %+v, want timeout/temporary", remote)
	}
}

// TestConnector_OperationStatusMapsRemoteOutcome: the operation-status seam
// decodes source outcomes (completed/failed/unknown) into CommandResult
// without leaking transport internals — the P5-04 reconciliation input.
func TestConnector_OperationStatusMapsRemoteOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "failed", "source_operation_id": "op-7", "error_code": "source.rejected",
			"result": map[string]any{"deleted_count": 3, "accessKey": "source-secret", "content": "message-body"},
		})
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{
		OperationStatusPath: "/status",
		MaxAttempts:         1,
	})
	result, err := client.OperationStatus(t.Context(), OperationStatusRequest{
		AccountID: domain.ID{1}, SourceOperationID: "op-7",
	})
	if err != nil {
		t.Fatalf("operation status: %v", err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %s, want failed", result.Status)
	}
	if result.SourceOperationID != "op-7" {
		t.Fatalf("result = %+v, want source operation op-7", result)
	}
	if result.Result["deleted_count"] != float64(3) {
		t.Fatalf("safe result = %#v, want deleted_count", result.Result)
	}
	if _, leaked := result.Result["accessKey"]; leaked {
		t.Fatalf("command result leaked accessKey: %#v", result.Result)
	}
	if _, leaked := result.Result["content"]; leaked {
		t.Fatalf("command result leaked content: %#v", result.Result)
	}
}
