package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/connector/aeroim"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/repository"
)

type operationStatusAccountRepository struct {
	repository.AccountRepository
	account *domain.Account
}

func (r *operationStatusAccountRepository) GetByID(_ context.Context, tenantID string, id domain.ID) (*domain.Account, error) {
	if r.account == nil || tenantID != r.account.TenantID || id != r.account.ID {
		return nil, repository.ErrNotFound
	}
	return r.account, nil
}

func TestConnectorOperationRunnerReconcileRejectsAeroIMCorrelationMismatches(t *testing.T) {
	cases := []struct {
		name          string
		responseIDs   string
		expectedID    func(*domain.Operation) string
		leakedSecrets []string
	}{
		{name: "source operation ID", responseIDs: `"operation_id":"%s","source_operation_id":"wrong-source-secret"`, expectedID: func(operation *domain.Operation) string { return operation.ID.String() }, leakedSecrets: []string{"wrong-source-secret"}},
		{name: "logical operation ID", responseIDs: `"operation_id":"wrong-logical-secret","source_operation_id":"%s"`, expectedID: func(_ *domain.Operation) string { return "source-operation-1" }, leakedSecrets: []string{"wrong-logical-secret"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			operation := &domain.Operation{ID: domain.ID{1}, AccountID: domain.ID{2}, TenantID: "tenant-1", ActorUID: "actor-1"}
			const sourceOperationID = "source-operation-1"
			response := fmt.Sprintf(`{%s,"status":"completed","result":{"token":"response-secret"}}`,
				fmt.Sprintf(tc.responseIDs, tc.expectedID(operation)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("operation_id") != operation.ID.String() || r.URL.Query().Get("source_operation_id") != sourceOperationID {
					t.Fatalf("request IDs = operation %q/source %q", r.URL.Query().Get("operation_id"), r.URL.Query().Get("source_operation_id"))
				}
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()

			client, err := aeroim.New(connector.HTTPConfig{Endpoint: server.URL, OperationStatusPath: "/status",
				BearerToken: "source-token", AllowInsecureLoopback: true, Environment: "test", Client: server.Client(), MaxAttempts: 1})
			if err != nil {
				t.Fatalf("aeroim.New: %v", err)
			}
			runner := NewConnectorOperationRunner(&operationStatusAccountRepository{account: &domain.Account{
				ID: operation.AccountID, TenantID: operation.TenantID, CanonicalUID: "canonical-1", HomeRegion: "west",
			}}, nil, client)
			reconciler := runner.(OperationOutcomeReconciler)
			status, _, result, err := reconciler.Reconcile(t.Context(), operation, &domain.OperationStep{
				ID: domain.ID{3}, OperationID: operation.ID, Source: string(connector.SourceAeroIM), Action: "erase",
				SourceOperationID: sourceOperationID, RequestPayload: map[string]any{"datasets": []any{"aero-im.profile"}},
			})
			if status == domain.StepSucceeded {
				t.Fatal("correlation mismatch yielded succeeded step")
			}
			if result != nil {
				t.Fatalf("result = %#v, want nil", result)
			}
			var remote *connector.RemoteError
			if !errors.As(err, &remote) {
				t.Fatalf("error = %v, want RemoteError", err)
			}
			if _, ok := connector.IsRemote(err); !ok || remote.Code != "source.operation_id_mismatch" ||
				remote.Kind != connector.ErrorInvalid || remote.Temporary || remote.Cause != nil {
				t.Fatalf("remote = %+v, want invalid/non-temporary mismatch without cause", remote)
			}
			text := fmt.Sprintf("%v %+v", err, remote)
			for _, secret := range append(tc.leakedSecrets, "response-secret", sourceOperationID, operation.ID.String()) {
				if strings.Contains(text, secret) {
					t.Fatalf("reconciliation error/log output leaked %q: %q", secret, text)
				}
			}
		})
	}
}
