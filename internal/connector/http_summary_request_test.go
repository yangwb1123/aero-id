package connector

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

func TestConnector_EraseTruncatedBodyIsTemporaryReadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000") // declared, then...
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase", MaxAttempts: 1})
	_, err := client.Erase(t.Context(), EraseRequest{
		AccountID: domain.ID{1}, IdempotencyKey: "idem-1", OperationID: "op-1",
	})
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("err = %v, want RemoteError", err)
	}
	if remote.Kind != ErrorUnavailable || !remote.Temporary || remote.Code != "source.response_read_failed" {
		t.Fatalf("remote = %+v, want unavailable/temporary/source.response_read_failed", remote)
	}
	if remote.Cause == nil {
		t.Fatalf("remote = %+v, want wrapped read cause", remote)
	}
}

func TestConnector_EraseRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 128)))
	}))
	defer server.Close()
	client := newTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase", MaxBodyBytes: 16, MaxAttempts: 1})
	_, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{1}, IdempotencyKey: "erase-oversized"})
	var remote *RemoteError
	if !errors.As(err, &remote) {
		t.Fatalf("err = %v, want RemoteError", err)
	}
	if remote.Kind != ErrorInvalid || remote.Temporary || remote.Code != "source.response_too_large" {
		t.Fatalf("remote = %+v, want invalid/non-temporary/source.response_too_large", remote)
	}
}

func TestNewJSONSummaryConnectorRejectsUnboundedConfiguration(t *testing.T) {
	cases := []struct {
		name string
		cfg  HTTPConfig
	}{
		{name: "body", cfg: HTTPConfig{MaxBodyBytes: maxSourceBodyBytes + 1}},
		{name: "concurrency", cfg: HTTPConfig{MaxConcurrency: maxSourceConcurrency + 1}},
		{name: "attempts", cfg: HTTPConfig{MaxAttempts: maxSourceAttempts + 1}},
		{name: "timeout", cfg: HTTPConfig{Timeout: maxSourceTimeout + time.Second}},
		{name: "backoff", cfg: HTTPConfig{RetryBackoff: maxSourceRetryBackoff + time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Endpoint = "http://127.0.0.1"
			if _, err := NewJSONSummaryConnector(SourceAeroIM, tc.cfg); err == nil {
				t.Fatal("unbounded connector configuration was accepted")
			}
		})
	}
}
