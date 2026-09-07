package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

const (
	//nolint:gosec // structurally valid, non-secret JWT fixture used only by tests.
	vaultReconcileAccessToken = "eyJhbGciOiJSUzI1NiIsInR5cCI6ImF0K2p3dCJ9.eyJpc3MiOiJodHRwczovL3NuYXBsaW5rLmV4YW1wbGUiLCJzdWIiOiJ2YXVsdC1jbGllbnQiLCJhdWQiOiJhZXJvLXZhdWx0IiwiY2xpZW50X2lkIjoidmF1bHQtY2xpZW50IiwianRpIjoidmF1bHQtdGVzdC0xIiwic2NvcGUiOiJyZWFkIiwiZXhwIjo0MTAyNDQ0ODAwfQ.signature"
	vaultLogicalOperationID   = "02000000-0000-0000-0000-000000000000"
)

func TestVaultOperationReconciliationNeverTurnsUncorrelated2xxIntoSuccess(t *testing.T) {
	cases := []struct {
		name    string
		status  string
		body    string
		want    domain.OperationStepStatus
		wantErr bool
	}{
		{name: "completed with matching IDs", status: "completed", body: `{"operation_id":"02000000-0000-0000-0000-000000000000","source_operation_id":"source-1","status":"completed"}`, want: domain.StepSucceeded},
		{name: "missing IDs", status: "missing", body: `{"status":"completed"}`, want: domain.StepUnknown, wantErr: true},
		{name: "wrong source ID", status: "wrong-source", body: `{"operation_id":"operation-1","source_operation_id":"other","status":"completed"}`, want: domain.StepUnknown, wantErr: true},
		{name: "pending", status: "pending", body: `{"operation_id":"02000000-0000-0000-0000-000000000000","source_operation_id":"source-1","status":"pending"}`, want: domain.StepUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"access_token":"`+vaultReconcileAccessToken+`","token_type":"Bearer","expires_in":3600}`)
					return
				}
				if r.Header.Get("X-Aero-Tenant") != "tenant-a" || r.Header.Get("X-Aero-Tenant-ID") != "tenant-a" {
					t.Errorf("tenant headers = %q/%q, want tenant-a", r.Header.Get("X-Aero-Tenant"), r.Header.Get("X-Aero-Tenant-ID"))
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()

			endpoint, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			client, err := connector.NewJSONSummaryConnector(connector.SourceAeroVault, connector.HTTPConfig{
				Endpoint: server.URL, OperationStatusPath: "/status", AllowInsecureLoopback: true, Environment: "test",
				OAuthTenantID:     "tenant-a",
				OAuthAllowedHosts: []string{endpoint.Scheme + "://" + endpoint.Host},
				OAuth: connector.OAuthClientCredentialsConfig{TokenURL: server.URL + "/token", ClientID: "vault-client",
					ClientSecret: "vault-secret", Scope: "read", Resource: "aero-vault"},
				Client: server.Client(), MaxAttempts: 1,
			})
			if err != nil {
				t.Fatalf("Vault connector: %v", err)
			}
			account := &domain.Account{ID: domain.ID{1}, TenantID: "tenant-a", CanonicalUID: "canonical-a",
				HomeRegion: "local", Status: domain.AccountActive}
			runner := NewConnectorOperationRunner(&operationStatusAccountRepository{account: account}, nil, client)
			reconciler, ok := runner.(OperationOutcomeReconciler)
			if !ok {
				t.Fatal("operation runner does not expose reconciliation")
			}
			operation := &domain.Operation{ID: domain.ID{2}, AccountID: account.ID, TenantID: account.TenantID,
				ActorUID: "actor-a", RequestID: "request-a"}
			step := &domain.OperationStep{ID: domain.ID{3}, OperationID: operation.ID, Source: string(connector.SourceAeroVault),
				Action: "erase", SourceOperationID: "source-1", RequestPayload: map[string]any{"datasets": []any{"aero-vault.tenants"}}}

			got, sourceOperationID, _, reconcileErr := reconciler.Reconcile(t.Context(), operation, step)
			if got != tc.want || sourceOperationID != "" && tc.wantErr {
				t.Fatalf("reconcile = %s/source=%q/error=%v, want status %s", got, sourceOperationID, reconcileErr, tc.want)
			}
			if tc.wantErr {
				var remote *connector.RemoteError
				if !errors.As(reconcileErr, &remote) || remote.Kind != connector.ErrorUnknown {
					t.Fatalf("reconcile error = %+v, want unknown RemoteError", reconcileErr)
				}
			} else if reconcileErr != nil {
				t.Fatalf("reconcile error = %v, want nil", reconcileErr)
			}
		})
	}
}
