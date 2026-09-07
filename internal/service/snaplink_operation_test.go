package service

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/connector/snaplink"
	"github.com/aero/aero-id/internal/domain"
)

func TestSnaplinkSummaryOnlyEraseHasNoSourceOrTokenIO(t *testing.T) {
	var sourceCalls, tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokenCalls.Add(1)
			return
		}
		sourceCalls.Add(1)
	}))
	defer server.Close()
	client, err := snaplink.New(connector.HTTPConfig{Endpoint: server.URL, AllowInsecureLoopback: true,
		Environment: "test", Client: server.Client(), OAuth: connector.OAuthClientCredentialsConfig{
			TokenURL: server.URL + "/token", ClientID: "source-client", ClientSecret: "secret",
			Scope: "source.read", Resource: "snaplink"}, MaxAttempts: 1})
	if err != nil {
		t.Fatalf("snaplink.New: %v", err)
	}
	runner := NewConnectorOperationRunner(&operationStatusAccountRepository{account: testOperationAccount()}, nil, client)
	_, _, err = runner.Run(t.Context(), testSnaplinkOperation(), testSnaplinkEraseStep())
	if err == nil || err.Error() != "source erase command is not configured" {
		t.Fatalf("Run error = %v, want local command-not-configured error", err)
	}
	if sourceCalls.Load() != 0 || tokenCalls.Load() != 0 {
		t.Fatalf("source/token calls = %d/%d, want zero", sourceCalls.Load(), tokenCalls.Load())
	}
}

func TestSnaplinkSummaryOnlyReconcileRemainsUnknownWithoutStatusIO(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client, err := snaplink.New(connector.HTTPConfig{Endpoint: server.URL, AllowInsecureLoopback: true,
		Environment: "test", Client: server.Client(), MaxAttempts: 1})
	if err != nil {
		t.Fatalf("snaplink.New: %v", err)
	}
	runner := NewConnectorOperationRunner(&operationStatusAccountRepository{account: testOperationAccount()}, nil, client)
	reconciler := runner.(OperationOutcomeReconciler)
	status, sourceID, _, err := reconciler.Reconcile(t.Context(), testSnaplinkOperation(), testSnaplinkEraseStep())
	if err != nil || status != domain.StepUnknown || sourceID != "" {
		t.Fatalf("Reconcile = %s/%q/%v, want unknown/empty/nil", status, sourceID, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("status calls = %d, want zero without status interface", calls.Load())
	}
}

func testOperationAccount() *domain.Account {
	return &domain.Account{ID: domain.ID{1}, TenantID: "tenant-1", CanonicalUID: "uid-1", HomeRegion: "local"}
}

func testSnaplinkOperation() *domain.Operation {
	return &domain.Operation{ID: domain.ID{2}, AccountID: domain.ID{1}, TenantID: "tenant-1", ActorUID: "actor-1", RequestID: "request-1"}
}

func testSnaplinkEraseStep() *domain.OperationStep {
	return &domain.OperationStep{ID: domain.ID{3}, Source: string(connector.SourceSnaplink), Action: "erase",
		RequestPayload: map[string]any{"datasets": []any{"snaplink.identity"}}}
}
