package connector

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

// Fault-injection suite (P6-09 release gates): source outage, WAN timeout,
// 429/503, malformed responses and partial responses. Every scenario maps to
// a stable RemoteError kind/code without leaking transport internals.

func TestConnector_ServiceUnavailableIsTemporary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 2, RetryBackoff: time.Millisecond})
	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("error = %v, want RemoteError", err)
	}
	if remote.Kind != ErrorUnavailable || !remote.Temporary || remote.Code != "source.http_503" {
		t.Errorf("remote = %+v, want unavailable/temporary/source.http_503", remote)
	}
}

func TestConnector_TooManyRequestsMapsToUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 1})
	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("error = %v, want RemoteError", err)
	}
	if !remote.Temporary || remote.Code != "source.http_429" {
		t.Errorf("remote = %+v, want temporary 429", remote)
	}
}

func TestConnector_TimeoutIsUnknownOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second) // exceed the client timeout
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{Timeout: 30 * time.Millisecond, MaxAttempts: 1})
	start := time.Now()
	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	if time.Since(start) > time.Second {
		t.Fatalf("timeout took too long: %v", time.Since(start))
	}
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("error = %v, want RemoteError", err)
	}
	if remote.Kind != ErrorTimeout || !remote.Temporary {
		t.Errorf("remote = %+v, want timeout/temporary", remote)
	}
}

func TestConnector_KeyedEraseTimeoutRemainsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase", Timeout: 30 * time.Millisecond, MaxAttempts: 1})
	_, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}, IdempotencyKey: "erase-timeout"})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Kind != ErrorTimeout || !remote.Temporary {
		t.Fatalf("err = %+v, want temporary timeout", err)
	}
}

func TestConnector_MalformedBodyIsInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"source_region":`)) // truncated JSON
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 1})
	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("error = %v, want RemoteError", err)
	}
	if remote.Kind != ErrorInvalid || remote.Temporary {
		t.Errorf("remote = %+v, want invalid/non-temporary", remote)
	}
}

// TestConnector_RetryBudgetRespected: a persistent 503 is retried up to
// MaxAttempts and then surfaces the failure (no infinite retry loop).
func TestConnector_RetryBudgetRespected(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 3, RetryBackoff: time.Millisecond})
	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	if err == nil {
		t.Fatal("persistent source failure must surface")
	}
	if calls != 3 {
		t.Errorf("source calls = %d, want 3 (retry budget)", calls)
	}
}

// TestConnector_PartialSummaryAccepted: a summary missing one dataset is still
// returned (partial semantics) — the aggregation layer reports freshness.
func TestConnector_PartialSummaryAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"source_region":"west","version":4,"generated_at":"2026-08-20T00:00:00Z","datasets":{}}`))
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 1})
	summary, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1},
		Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	if err != nil {
		t.Fatalf("empty datasets must be a valid partial summary: %v", err)
	}
	if summary.SourceRegion != "west" || summary.Version != 4 {
		t.Errorf("summary metadata lost: %+v", summary)
	}
	if summary.Complete {
		t.Fatal("summary with a requested dataset missing from the payload must be incomplete")
	}
}

// TestConnector_RetriesTransientThenSucceeds: first attempt 503, retry 200 —
// the connector must succeed and the metrics-free path stays clean.
func TestConnector_RetriesTransientThenSucceeds(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"source_region":"west","version":1,"generated_at":"2026-08-20T00:00:00Z","datasets":{"aero-im.profile":{"display_name":"Ada"}}}`))
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 3, RetryBackoff: time.Millisecond})
	summary, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	if err != nil {
		t.Fatalf("retry should recover: %v", err)
	}
	if len(summary.Snapshots) != 1 {
		t.Errorf("snapshots = %d, want 1", len(summary.Snapshots))
	}
}

func TestConnector_DoesNotRetryEraseWithoutIdempotency(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase", MaxAttempts: 3, RetryBackoff: time.Millisecond})
	_, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}, OperationID: "op-1"})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Kind != ErrorInvalid || remote.Code != "source.idempotency_key_missing" || remote.Temporary {
		t.Fatalf("err = %+v, want non-temporary idempotency_key_missing", err)
	}
	if calls != 0 {
		t.Fatalf("erase calls = %d, want 0 without idempotency key", calls)
	}
}

func TestConnector_RetriesEraseWithIdempotency(t *testing.T) {
	var calls int
	var headers []string
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		headers = append(headers, r.Header.Get("Idempotency-Key"))
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed","source_operation_id":"source-op-1"}`))
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase", MaxAttempts: 3, RetryBackoff: time.Millisecond})
	result, err := client.Erase(t.Context(), EraseRequest{
		AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}, OperationID: "op-1", IdempotencyKey: "erase-1",
	})
	if err != nil {
		t.Fatalf("idempotent erase should recover: %v", err)
	}
	if result.Status != "completed" || calls != 2 {
		t.Fatalf("result=%+v calls=%d, want completed/2", result, calls)
	}
	for i := range headers {
		if headers[i] != "erase-1" || bodies[i] != bodies[0] {
			t.Fatalf("attempt %d key/body = %q/%q, want stable key and body %q", i, headers[i], bodies[i], bodies[0])
		}
	}
}

// TestConnector_CircuitOpensAfterRepeatedFailures: five consecutive temporary
// failures open the breaker; subsequent calls fail fast without HTTP traffic.
func TestConnector_CircuitOpensAfterRepeatedFailures(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{MaxAttempts: 1})
	for i := 0; i < 5; i++ {
		_, _ = client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	}
	before := calls
	_, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	if err == nil {
		t.Fatal("circuit-open call must fail")
	}
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != "source.circuit_open" {
		t.Errorf("error = %v, want circuit_open", err)
	}
	if calls != before {
		t.Errorf("circuit-open call still hit the source (%d -> %d)", before, calls)
	}
}

func newTestConnector(t *testing.T, endpoint string, cfg HTTPConfig) *JSONSummaryConnector {
	t.Helper()
	cfg = addTestTargetAssertion(cfg)
	cfg.Endpoint = endpoint
	cfg.SummaryPath = accountSummaryCanonicalPath
	cfg.AllowInsecureLoopback = true
	cfg.Environment = "test"
	cfg.Client = http.DefaultClient
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	client, err := NewJSONSummaryConnector(SourceAeroIM, cfg)
	if err != nil {
		t.Fatalf("new connector: %v", err)
	}
	return client
}

var _ = strings.Repeat // keep strings import for future assertions
