package connector

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

func TestOperationStatusAeroIMCorrelationAndValidation(t *testing.T) {
	const logicalID = "logical-opaque-1"
	const sourceID = "source-opaque-1"
	request := OperationStatusRequest{AccountID: domain.ID{1}, OperationID: logicalID, SourceOperationID: sourceID}

	cases := []struct {
		name       string
		body       string
		wantSource string
		wantStatus string
	}{
		{name: "matching IDs", body: `{"operation_id":"logical-opaque-1","source_operation_id":"source-opaque-1","status":"completed"}`, wantSource: sourceID, wantStatus: "completed"},
		{name: "omitted logical ID", body: `{"source_operation_id":"source-opaque-1","status":"completed"}`, wantSource: sourceID, wantStatus: "completed"},
		{name: "omitted source ID falls back", body: `{"operation_id":"logical-opaque-1","status":"completed"}`, wantSource: logicalID, wantStatus: "completed"},
		{name: "both IDs omitted", body: `{"status":"completed"}`, wantStatus: "completed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := operationStatusTestConnector(t, SourceAeroIM, tc.body, 0)
			result, err := client.OperationStatus(t.Context(), request)
			if err != nil {
				t.Fatalf("OperationStatus: %v", err)
			}
			if result.SourceOperationID != tc.wantSource || result.Status != tc.wantStatus {
				t.Fatalf("result = %+v, want source=%q/status=%q", result, tc.wantSource, tc.wantStatus)
			}
		})
	}
}

func TestOperationStatusAeroIMMismatchIsNotRetried(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"source_operation_id":"wrong-source","status":"completed"}`))
	}))
	defer server.Close()
	cfg := HTTPConfig{Endpoint: server.URL, OperationStatusPath: "/status", MaxAttempts: 3,
		RetryBackoff: time.Millisecond, Client: server.Client()}
	cfg = addTestTargetAssertion(cfg)
	cfg.AllowInsecureLoopback = true
	cfg.Environment = "test"
	client, err := NewJSONSummaryConnector(SourceAeroIM, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.OperationStatus(t.Context(), OperationStatusRequest{
		AccountID: domain.ID{1}, SourceOperationID: "requested-source",
	})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != "source.operation_id_mismatch" || remote.Temporary {
		t.Fatalf("error = %v, want non-temporary identifier mismatch", err)
	}
	if calls != 1 {
		t.Fatalf("status calls = %d, want one call without retry", calls)
	}
}

func TestOperationStatusAeroIMRejectsCorrelationMismatchBeforeStatus(t *testing.T) {
	const logicalID = "logical-secret-value"
	const sourceID = "source-secret-value"
	request := OperationStatusRequest{AccountID: domain.ID{1}, OperationID: logicalID, SourceOperationID: sourceID}

	cases := []struct {
		name string
		body string
	}{
		{name: "source ID", body: `{"operation_id":"logical-secret-value","source_operation_id":"wrong-source-secret","status":"completed","result":{"token":"response-secret"}}`},
		{name: "logical ID", body: `{"operation_id":"wrong-logical-secret","source_operation_id":"source-secret-value","status":"completed"}`},
		{name: "both IDs", body: `{"operation_id":"wrong-logical-secret","source_operation_id":"wrong-source-secret","status":"completed"}`},
		{name: "empty status still checks IDs first", body: `{"operation_id":"wrong-logical-secret","source_operation_id":"source-secret-value"}`},
		{name: "failed status still checks IDs first", body: `{"operation_id":"logical-secret-value","source_operation_id":"wrong-source-secret","status":"failed"}`},
		{name: "pending status still checks IDs first", body: `{"operation_id":"logical-secret-value","source_operation_id":"wrong-source-secret","status":"pending"}`},
		{name: "unknown status still checks IDs first", body: `{"operation_id":"logical-secret-value","source_operation_id":"wrong-source-secret","status":"not-a-status"}`},
		{name: "case difference is opaque mismatch", body: `{"operation_id":"logical-secret-value","source_operation_id":"SOURCE-SECRET-VALUE","status":"completed"}`},
		{name: "whitespace difference is opaque mismatch", body: `{"operation_id":"logical-secret-value","source_operation_id":"source-secret-value ","status":"completed"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := operationStatusTestConnector(t, SourceAeroIM, tc.body, 0)
			result, err := client.OperationStatus(t.Context(), request)
			assertOperationIDMismatch(t, result, err, logicalID, sourceID)
		})
	}
}

func TestOperationStatusValidationErrorsRemainMalformedAndBounded(t *testing.T) {
	request := OperationStatusRequest{AccountID: domain.ID{1}, OperationID: "logical-1", SourceOperationID: "source-1"}
	cases := []struct {
		name      string
		body      string
		maxBytes  int64
		wantCode  string
		wantCause bool
	}{
		{name: "malformed JSON", body: `{"operation_id":`, wantCode: "source.response_invalid", wantCause: true},
		{name: "missing status", body: `{"operation_id":"logical-1","source_operation_id":"source-1"}`, wantCode: "source.operation_status_missing"},
		{name: "oversized response", body: strings.Repeat("x", 65), maxBytes: 64, wantCode: "source.response_too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := operationStatusTestConnector(t, SourceAeroIM, tc.body, tc.maxBytes)
			result, err := client.OperationStatus(t.Context(), request)
			if result != nil {
				t.Fatalf("result = %#v, want nil", result)
			}
			var remote *RemoteError
			_, remoteOK := IsRemote(err)
			if !errors.As(err, &remote) || !remoteOK {
				t.Fatalf("error = %v, want discoverable RemoteError", err)
			}
			if remote.Code != tc.wantCode || remote.Kind != ErrorInvalid || remote.Temporary {
				t.Fatalf("remote = %+v, want invalid/non-temporary/%s", remote, tc.wantCode)
			}
			if (remote.Cause != nil) != tc.wantCause {
				t.Fatalf("Cause present = %t, want %t", remote.Cause != nil, tc.wantCause)
			}
		})
	}
}

func TestOperationStatusCorrelationIsAeroIMOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"operation_id":"different-logical","source_operation_id":"source-1","status":"completed"}`))
	}))
	defer server.Close()

	client, err := NewJSONSummaryConnector(SourceSnaplink, HTTPConfig{
		Endpoint: server.URL, OperationStatusPath: "/status", BearerToken: "source-token",
		AllowInsecureLoopback: true, Environment: "test", Client: server.Client(), MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.OperationStatus(t.Context(), OperationStatusRequest{
		AccountID: domain.ID{1}, OperationID: "logical-1", SourceOperationID: "source-1",
	})
	if err != nil {
		t.Fatalf("non-Aero IM status: %v", err)
	}
	if result == nil || result.Status != "completed" || result.SourceOperationID != "source-1" {
		t.Fatalf("result = %#v, want existing non-Aero IM behavior", result)
	}
}

func operationStatusTestConnector(t *testing.T, source Source, body string, maxBytes int64) *JSONSummaryConnector {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	cfg := HTTPConfig{Endpoint: server.URL, OperationStatusPath: "/status", MaxAttempts: 1,
		MaxBodyBytes: maxBytes, Client: server.Client()}
	if source == SourceAeroIM {
		cfg = addTestTargetAssertion(cfg)
		cfg.AllowInsecureLoopback = true
		cfg.Environment = "test"
	} else {
		cfg.BearerToken = "source-token"
		cfg.AllowInsecureLoopback = true
		cfg.Environment = "test"
	}
	client, err := NewJSONSummaryConnector(source, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertOperationIDMismatch(t *testing.T, result *CommandResult, err error, logicalID, sourceID string) {
	t.Helper()
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("error = %v, want RemoteError", err)
	}
	if _, ok := IsRemote(err); !ok || remote.Source != SourceAeroIM || remote.Kind != ErrorInvalid ||
		remote.Code != "source.operation_id_mismatch" || remote.Temporary || remote.Cause != nil {
		t.Fatalf("remote = %+v, want Aero IM invalid non-temporary mismatch without cause", remote)
	}
	text := fmt.Sprintf("%v %+v", err, remote)
	for _, secret := range []string{logicalID, sourceID, "wrong-logical-secret", "wrong-source-secret", "response-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("mismatch error/log output leaked %q: %q", secret, text)
		}
	}
}
