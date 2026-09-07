package snaplink

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

func TestNewGatesOptionalInterfaces(t *testing.T) {
	installEraseContract(t, "test-v1")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	base := testHTTPConfig(server.URL)

	cases := []struct {
		name       string
		capability Capabilities
		erasePath  string
		statusPath string
		wantErase  bool
		wantStatus bool
	}{
		{name: "summary only", capability: Capabilities{Summary: true}},
		{name: "erase", capability: Capabilities{Summary: true, Erase: true, EraseContractVersion: "test-v1"}, erasePath: "/erase", wantErase: true},
		{name: "status", capability: Capabilities{Summary: true, OperationStatus: true}, statusPath: "/status", wantStatus: true},
		{name: "both", capability: Capabilities{Summary: true, Erase: true, OperationStatus: true, EraseContractVersion: "test-v1"}, erasePath: "/erase", statusPath: "/status", wantErase: true, wantStatus: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.ErasePath, cfg.OperationStatusPath = tc.erasePath, tc.statusPath
			client, err := NewWithCapabilities(cfg, tc.capability)
			if err != nil {
				t.Fatalf("NewWithCapabilities: %v", err)
			}
			_, gotErase := client.(connector.CommandConnector)
			_, gotStatus := client.(connector.OperationStatusConnector)
			if gotErase != tc.wantErase || gotStatus != tc.wantStatus {
				t.Fatalf("interfaces = erase:%t/status:%t, want erase:%t/status:%t", gotErase, gotStatus, tc.wantErase, tc.wantStatus)
			}
		})
	}

	if _, err := New(base); err != nil {
		t.Fatalf("summary-only New: %v", err)
	}
	if _, err := NewWithCapabilities(base, Capabilities{Summary: true, EraseContractVersion: "test-v1"}); err == nil {
		t.Fatal("erase contract version without erase capability was accepted")
	}
	bad := base
	bad.ErasePath = "/erase"
	if _, err := New(bad); err == nil || !strings.Contains(err.Error(), "sources.snaplink.erase_path") {
		t.Fatalf("New with dormant erase path error = %v", err)
	}
}

func TestValidateCapabilitiesDisabledAndEndpointRules(t *testing.T) {
	if err := ValidateCapabilities(connector.HTTPConfig{}, Capabilities{}); err != nil {
		t.Fatalf("disabled source: %v", err)
	}
	cases := []struct {
		name string
		cfg  connector.HTTPConfig
		cap  Capabilities
		want string
	}{
		{name: "erase path without capability", cfg: connector.HTTPConfig{ErasePath: "/erase"}, want: "erase_path"},
		{name: "status path without capability", cfg: connector.HTTPConfig{OperationStatusPath: "/status"}, want: "status_path"},
		{name: "erase without path", cfg: connector.HTTPConfig{Endpoint: "https://snaplink.example"}, cap: Capabilities{Summary: true, Erase: true, EraseContractVersion: "test-v1"}, want: "capabilities.erase"},
		{name: "status without path", cfg: connector.HTTPConfig{Endpoint: "https://snaplink.example"}, cap: Capabilities{Summary: true, OperationStatus: true}, want: "operation_status"},
		{name: "enabled endpoint without summary", cfg: connector.HTTPConfig{Endpoint: "https://snaplink.example"}, want: "capabilities.summary"},
		{name: "enabled capability without endpoint", cap: Capabilities{Summary: true}, want: "non-empty"},
		{name: "unknown erase version", cfg: connector.HTTPConfig{Endpoint: "https://snaplink.example", ErasePath: "/erase"}, cap: Capabilities{Summary: true, Erase: true, EraseContractVersion: "unknown"}, want: "erase_contract_version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCapabilities(tc.cfg, tc.cap)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestSnaplinkEraseUnknownOutcomesAndAuthorization(t *testing.T) {
	installEraseContract(t, "test-v1")
	cases := []struct {
		name       string
		statusCode int
		body       string
		wantKind   connector.ErrorKind
		wantStatus string
	}{
		{name: "malformed", statusCode: http.StatusOK, body: `{"operation_id":`, wantKind: connector.ErrorUnknown},
		{name: "missing correlation", statusCode: http.StatusOK, body: `{"status":"completed","source_operation_id":"source-1"}`, wantKind: connector.ErrorUnknown},
		{name: "mismatched operation", statusCode: http.StatusOK, body: `{"operation_id":"other","source_operation_id":"source-1","status":"completed"}`, wantKind: connector.ErrorUnknown},
		{name: "pending", statusCode: http.StatusOK, body: `{"operation_id":"logical-1","source_operation_id":"source-1","status":"pending"}`, wantKind: "", wantStatus: "pending"},
		{name: "completed", statusCode: http.StatusOK, body: `{"operation_id":"logical-1","source_operation_id":"source-1","status":"completed"}`, wantKind: "", wantStatus: "completed"},
		{name: "unauthorized", statusCode: http.StatusUnauthorized, wantKind: connector.ErrorUnauthorized},
		{name: "forbidden", statusCode: http.StatusForbidden, wantKind: connector.ErrorForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.statusCode != 0 {
					w.WriteHeader(tc.statusCode)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			cfg := testHTTPConfig(server.URL)
			cfg.ErasePath = "/erase"
			client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, Erase: true, EraseContractVersion: "test-v1"})
			if err != nil {
				t.Fatalf("NewWithCapabilities: %v", err)
			}
			command := client.(connector.CommandConnector)
			result, err := command.Erase(t.Context(), connector.EraseRequest{AccountID: domain.ID{1}, OperationID: "logical-1",
				Datasets: []domain.AccountDataset{"snaplink.identity"}, IdempotencyKey: "erase-1"})
			if tc.wantKind == "" {
				if err != nil {
					t.Fatalf("Erase: %v", err)
				}
				if result == nil || result.Status != tc.wantStatus || result.SourceOperationID != "source-1" {
					t.Fatalf("result = %#v, want %s/source-1", result, tc.wantStatus)
				}
				return
			}
			var remote *connector.RemoteError
			if !errors.As(err, &remote) || remote.Kind != tc.wantKind || !remote.Temporary && tc.wantKind == connector.ErrorUnknown {
				t.Fatalf("result/error = %#v/%+v, want %s", result, err, tc.wantKind)
			}
		})
	}
}

func TestSnaplinkStatusRequiresAndPreservesCorrelation(t *testing.T) {
	installEraseContract(t, "test-v1")
	var seenSource atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSource.Store(r.URL.Query().Get("source_operation_id"))
		_, _ = io.WriteString(w, `{"operation_id":"logical-1","source_operation_id":"source-1","status":"completed"}`)
	}))
	defer server.Close()
	cfg := testHTTPConfig(server.URL)
	cfg.OperationStatusPath = "/status"
	client, err := NewWithCapabilities(cfg, Capabilities{Summary: true, OperationStatus: true})
	if err != nil {
		t.Fatalf("NewWithCapabilities: %v", err)
	}
	status := client.(connector.OperationStatusConnector)
	result, err := status.OperationStatus(t.Context(), connector.OperationStatusRequest{AccountID: domain.ID{1},
		OperationID: "logical-1", SourceOperationID: "source-1", IdempotencyKey: "erase-1"})
	if err != nil || result == nil || result.SourceOperationID != "source-1" {
		t.Fatalf("OperationStatus = %#v/%v, want matching source ID", result, err)
	}
	if got := seenSource.Load(); got != "source-1" {
		t.Fatalf("source operation query = %q, want source-1", got)
	}
}

func testHTTPConfig(endpoint string) connector.HTTPConfig {
	return connector.HTTPConfig{Endpoint: endpoint, SummaryPath: "/summary", BearerToken: "source-token",
		AllowInsecureLoopback: true, Environment: "test", Client: http.DefaultClient, MaxAttempts: 1, Timeout: time.Second}
}

func installEraseContract(t *testing.T, version string) {
	t.Helper()
	previous := eraseContractVersions
	eraseContractVersions = map[string]struct{}{version: {}}
	t.Cleanup(func() { eraseContractVersions = previous })
}
