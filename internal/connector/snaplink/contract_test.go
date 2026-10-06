package snaplink

import (
	"encoding/json"
	"errors"
	"fmt"
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

// snaplinkCorrelationID is a UUID whose canonical rendering contains hex
// letters, so every alternate rendering below is an observably different
// string for the same 128-bit identity.
const (
	snaplinkCorrelationID = "0195b2c6-9f40-7c01-8000-000000000001"
	snaplinkAltID         = "ffff0000-0000-0000-0000-000000000000"
	snaplinkCorrelation32 = "0195b2c69f407c018000000000000001"
)

func snaplinkCorrelationRenderings() []struct{ name, text string } {
	// Whitespace-padded and control-character forms are deliberately absent:
	// validCorrelationID rejects them as illegal correlation ids before any
	// identity comparison runs, which is a stricter boundary than identity
	// and is pinned by TestSnaplinkConnectorsRejectNonIdentityCorrelation.
	return []struct{ name, text string }{
		{name: "canonical", text: snaplinkCorrelationID},
		{name: "upper-case", text: "0195B2C6-9F40-7C01-8000-000000000001"},
		{name: "mixed-case", text: "0195b2C6-9F40-7c01-8000-000000000001"},
		{name: "urn prefix", text: "urn:uuid:" + snaplinkCorrelationID},
		{name: "urn prefix upper-case", text: "URN:UUID:0195B2C6-9F40-7C01-8000-000000000001"},
		{name: "32 hex", text: snaplinkCorrelation32},
		{name: "braced", text: "{" + snaplinkCorrelationID + "}"},
	}
}

func snaplinkBody(t *testing.T, operationID, sourceOperationID string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"operation_id":`+jsonString(t, operationID)+
			`,"source_operation_id":`+jsonString(t, sourceOperationID)+`,"status":"completed"}`)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %q: %v", value, err)
	}
	return string(encoded)
}

// TestSnaplinkCommandConnectorAcceptsAlternateCorrelationRenderings is the
// production half of AC-2: the Snaplink anti-corruption adapter validates every
// erase response before the service sees it, so it must accept a re-rendering of
// the correlation id aero-id sent instead of reporting a temporary unknown.
func TestSnaplinkCommandConnectorAcceptsAlternateCorrelationRenderings(t *testing.T) {
	installEraseContract(t, "test-v1")
	for _, rendering := range snaplinkCorrelationRenderings() {
		t.Run(rendering.name, func(t *testing.T) {
			cfg := testHTTPConfig(snaplinkBody(t, rendering.text, "snaplink-op-1"))
			cfg.ErasePath = "/erase"
			client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, Erase: true, EraseContractVersion: "test-v1"})
			if err != nil {
				t.Fatalf("NewWithCapabilities: %v", err)
			}
			result, err := client.(connector.CommandConnector).Erase(t.Context(), connector.EraseRequest{
				AccountID: domain.ID{1}, OperationID: snaplinkCorrelationID,
				Datasets: []domain.AccountDataset{"snaplink.identity"}, IdempotencyKey: "erase-1"})
			if err != nil {
				t.Fatalf("Erase with %s echo: %v", rendering.name, err)
			}
			if result == nil || result.Status != "completed" || result.SourceOperationID != "snaplink-op-1" {
				t.Fatalf("result = %#v, want completed/snaplink-op-1", result)
			}
		})
	}
}

// TestSnaplinkStatusConnectorAcceptsAlternateCorrelationRenderings covers the
// reconcile half of the adapter: an alternate rendering of the correlation id
// on a status lookup must not turn into a retrying unknown outcome.
func TestSnaplinkStatusConnectorAcceptsAlternateCorrelationRenderings(t *testing.T) {
	for _, rendering := range snaplinkCorrelationRenderings() {
		t.Run(rendering.name, func(t *testing.T) {
			cfg := testHTTPConfig(snaplinkBody(t, rendering.text, "snaplink-op-1"))
			cfg.OperationStatusPath = "/status"
			client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, OperationStatus: true})
			if err != nil {
				t.Fatalf("NewWithCapabilities: %v", err)
			}
			result, err := client.(connector.OperationStatusConnector).OperationStatus(t.Context(),
				connector.OperationStatusRequest{AccountID: domain.ID{1}, OperationID: snaplinkCorrelationID,
					SourceOperationID: "snaplink-op-1", IdempotencyKey: "erase-1"})
			if err != nil {
				t.Fatalf("OperationStatus with %s echo: %v", rendering.name, err)
			}
			if result == nil || result.Status != "completed" || result.SourceOperationID != "snaplink-op-1" {
				t.Fatalf("result = %#v, want completed/snaplink-op-1", result)
			}
		})
	}
}

// TestSnaplinkConnectorsRejectNonIdentityCorrelation is the adapter negative
// set: the aero-id-owned correlation id still requires the same identity, and
// the source-owned source_operation_id stays exact-string even when both sides
// are UUID-shaped and denote the same identity.
func TestSnaplinkConnectorsRejectNonIdentityCorrelation(t *testing.T) {
	t.Run("erase with a different correlation", func(t *testing.T) {
		installEraseContract(t, "test-v1")
		cfg := testHTTPConfig(snaplinkBody(t, snaplinkAltID, "snaplink-op-1"))
		cfg.ErasePath = "/erase"
		client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, Erase: true, EraseContractVersion: "test-v1"})
		if err != nil {
			t.Fatalf("NewWithCapabilities: %v", err)
		}
		_, err = client.(connector.CommandConnector).Erase(t.Context(), connector.EraseRequest{
			AccountID: domain.ID{1}, OperationID: snaplinkCorrelationID,
			Datasets: []domain.AccountDataset{"snaplink.identity"}, IdempotencyKey: "erase-1"})
		assertSnaplinkUnknownCode(t, err, "source.operation_id_mismatch")
	})
	t.Run("status with a different correlation", func(t *testing.T) {
		cfg := testHTTPConfig(snaplinkBody(t, "urn:uuid:"+snaplinkAltID, "snaplink-op-1"))
		cfg.OperationStatusPath = "/status"
		client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, OperationStatus: true})
		if err != nil {
			t.Fatalf("NewWithCapabilities: %v", err)
		}
		_, err = client.(connector.OperationStatusConnector).OperationStatus(t.Context(),
			connector.OperationStatusRequest{AccountID: domain.ID{1}, OperationID: snaplinkCorrelationID,
				SourceOperationID: "snaplink-op-1", IdempotencyKey: "erase-1"})
		assertSnaplinkUnknownCode(t, err, "source.operation_id_mismatch")
	})
	t.Run("status with a whitespace-padded correlation", func(t *testing.T) {
		cfg := testHTTPConfig(snaplinkBody(t, " "+snaplinkCorrelationID+"\n", "snaplink-op-1"))
		cfg.OperationStatusPath = "/status"
		client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, OperationStatus: true})
		if err != nil {
			t.Fatalf("NewWithCapabilities: %v", err)
		}
		_, err = client.(connector.OperationStatusConnector).OperationStatus(t.Context(),
			connector.OperationStatusRequest{AccountID: domain.ID{1}, OperationID: snaplinkCorrelationID,
				SourceOperationID: "snaplink-op-1", IdempotencyKey: "erase-1"})
		assertSnaplinkUnknownCode(t, err, "source.operation_id_mismatch")
	})
	t.Run("status with a re-rendered source operation id", func(t *testing.T) {
		cfg := testHTTPConfig(snaplinkBody(t, snaplinkCorrelationID, strings.ToUpper(snaplinkCorrelationID)))
		cfg.OperationStatusPath = "/status"
		client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, OperationStatus: true})
		if err != nil {
			t.Fatalf("NewWithCapabilities: %v", err)
		}
		_, err = client.(connector.OperationStatusConnector).OperationStatus(t.Context(),
			connector.OperationStatusRequest{AccountID: domain.ID{1}, OperationID: snaplinkCorrelationID,
				SourceOperationID: snaplinkCorrelationID, IdempotencyKey: "erase-1"})
		assertSnaplinkUnknownCode(t, err, "source.source_operation_id_mismatch")
	})
}

func assertSnaplinkUnknownCode(t *testing.T, err error, code string) {
	t.Helper()
	assertSnaplinkUnknown(t, err)
	var remote *connector.RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("error = %+v, want RemoteError", err)
	}
	if remote.Source != connector.SourceSnaplink || remote.Code != code {
		t.Fatalf("remote = %+v, want Snaplink %s", remote, code)
	}
	if strings.Contains(fmt.Sprintf("%v %+v", err, remote), snaplinkCorrelationID) {
		t.Fatalf("mismatch error leaked the correlation id: %v", err)
	}
}
