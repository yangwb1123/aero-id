package snaplink

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

func TestSnaplinkEraseRetriesWithStableIdempotency(t *testing.T) {
	installEraseContract(t, "test-v1")
	var bodies, keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"operation_id":"logical-1","source_operation_id":"source-1","status":"completed"}`)
	}))
	defer server.Close()
	cfg := testHTTPConfig(server.URL)
	cfg.ErasePath, cfg.MaxAttempts, cfg.RetryBackoff = "/erase", 2, time.Millisecond
	client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, Erase: true, EraseContractVersion: "test-v1"})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	_, err = client.(connector.CommandConnector).Erase(t.Context(), connector.EraseRequest{AccountID: domain.ID{1},
		OperationID: "logical-1", IdempotencyKey: "idem-1", Datasets: []domain.AccountDataset{"snaplink.identity"}})
	if err != nil || len(bodies) != 2 || bodies[0] != bodies[1] || len(keys) != 2 || keys[0] != "idem-1" || keys[1] != "idem-1" {
		t.Fatalf("retry err=%v bodies=%q keys=%q", err, bodies, keys)
	}
}

func TestSnaplinkMalformedAndOversizedEraseResponsesAreUnknown(t *testing.T) {
	installEraseContract(t, "test-v1")
	cases := []struct {
		name string
		body string
		max  int64
	}{
		{name: "malformed", body: `{"operation_id":`},
		{name: "oversized", body: strings.Repeat("x", 65), max: 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			cfg := testHTTPConfig(server.URL)
			cfg.ErasePath, cfg.MaxBodyBytes = "/erase", tc.max
			client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, Erase: true, EraseContractVersion: "test-v1"})
			if err != nil {
				t.Fatalf("constructor: %v", err)
			}
			_, err = client.(connector.CommandConnector).Erase(t.Context(), connector.EraseRequest{AccountID: domain.ID{1},
				OperationID: "logical-1", IdempotencyKey: "idem-1", Datasets: []domain.AccountDataset{"snaplink.identity"}})
			assertSnaplinkUnknown(t, err)
		})
	}
}

func TestSnaplinkEraseTimeoutIsUnknown(t *testing.T) {
	installEraseContract(t, "test-v1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()
	cfg := testHTTPConfig(server.URL)
	cfg.ErasePath, cfg.Timeout = "/erase", 10*time.Millisecond
	client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, Erase: true, EraseContractVersion: "test-v1"})
	if err != nil {
		t.Fatalf("constructor: %v", err)
	}
	_, err = client.(connector.CommandConnector).Erase(t.Context(), connector.EraseRequest{AccountID: domain.ID{1},
		OperationID: "logical-1", IdempotencyKey: "idem-1", Datasets: []domain.AccountDataset{"snaplink.identity"}})
	assertSnaplinkUnknown(t, err)
}

func TestSnaplinkStatusFailuresAndMismatchesAreUnknown(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
	}{
		{name: "forbidden", code: http.StatusForbidden},
		{name: "malformed", body: `{"operation_id":`},
		{name: "mismatch", body: `{"operation_id":"other","source_operation_id":"source-1","status":"completed"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.code != 0 {
					w.WriteHeader(tc.code)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			cfg := testHTTPConfig(server.URL)
			cfg.OperationStatusPath = "/status"
			client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, OperationStatus: true})
			if err != nil {
				t.Fatalf("constructor: %v", err)
			}
			_, err = client.(connector.OperationStatusConnector).OperationStatus(t.Context(), connector.OperationStatusRequest{
				AccountID: domain.ID{1}, OperationID: "logical-1", SourceOperationID: "source-1"})
			assertSnaplinkUnknown(t, err)
		})
	}
}

func assertSnaplinkUnknown(t *testing.T, err error) {
	t.Helper()
	var remote *connector.RemoteError
	if !errors.As(err, &remote) || remote.Kind != connector.ErrorUnknown || !remote.Temporary {
		t.Fatalf("error = %+v, want temporary unknown RemoteError", err)
	}
}
