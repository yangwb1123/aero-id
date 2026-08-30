package service

import (
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

func TestOperationCommandKeysAreStableAndDistinctForCompensation(t *testing.T) {
	operation := &domain.Operation{ID: domain.ID{1}}
	step := &domain.OperationStep{ID: domain.ID{2}}
	forward := operationEraseIdempotencyKey(operation, step, "")
	compensation := operationEraseIdempotencyKey(operation, step, ":compensation")
	reconcile := operationReconcileIdempotencyKey(operation, step)
	compensationReconcile := operationCompensationReconcileIdempotencyKey(operation, step)
	if forward == "" || reconcile == "" || compensation == "" || compensationReconcile == "" {
		t.Fatal("operation command keys must be non-empty")
	}
	if forward != reconcile {
		t.Fatalf("reconciliation key = %q, want forward key %q", reconcile, forward)
	}
	if compensation == forward || compensationReconcile == forward {
		t.Fatal("compensation keys must be distinct from the forward key")
	}
	if compensationReconcile != compensation {
		t.Fatalf("compensation reconciliation key = %q, want compensation key %q", compensationReconcile, compensation)
	}
	if retry := operationReconcileIdempotencyKey(operation, step); retry != reconcile {
		t.Fatalf("reconciliation retry key = %q, want %q", retry, reconcile)
	}
}

func TestCommandResultOutcome(t *testing.T) {
	cases := []struct {
		name      string
		status    string
		kind      connector.ErrorKind
		code      string
		wantError bool
	}{
		{name: "completed", status: "completed"},
		{name: "failed", status: "failed", kind: connector.ErrorInvalid, code: "source.operation_failed", wantError: true},
		{name: "accepted", status: "accepted", kind: connector.ErrorUnknown, code: "source.operation_pending", wantError: true},
		{name: "empty", status: "", kind: connector.ErrorUnknown, code: "source.operation_pending", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := commandResultOutcome(connector.SourceAeroVault, &connector.CommandResult{Status: tc.status})
			if !tc.wantError {
				if err != nil {
					t.Fatalf("error = %v, want nil", err)
				}
				return
			}
			var remote *connector.RemoteError
			if !errors.As(err, &remote) {
				t.Fatalf("error = %v, want RemoteError", err)
			}
			if remote.Kind != tc.kind || remote.Code != tc.code {
				t.Fatalf("remote = %+v, want %s/%s", remote, tc.kind, tc.code)
			}
		})
	}
}

func TestOperationRunStatusTreatsIdentifierMismatchAsFailed(t *testing.T) {
	status, code, _ := operationRunStatus(&connector.RemoteError{
		Source: connector.SourceAeroIM, Kind: connector.ErrorInvalid,
		Code: "source.operation_id_mismatch",
	})
	if status != domain.StepFailed {
		t.Fatalf("status = %s, want failed durable step", status)
	}
	if code != "operation.step_failed" {
		t.Fatalf("code = %q, want operation.step_failed", code)
	}
}

func TestSyncOperationResultPreservesPartialSummary(t *testing.T) {
	result := syncOperationResult(&connector.Summary{
		Complete:        false,
		SkippedDatasets: []string{"aero-vault.objects"},
		Snapshots:       []*domain.AccountSnapshot{{}},
	}, domain.SummaryApplicationResult{Outcome: domain.SummaryIncomplete})
	if got := result["status"]; got != string(domain.OperationPartiallyCompleted) {
		t.Fatalf("status = %v, want %q", got, domain.OperationPartiallyCompleted)
	}
	if got := result["error_code"]; got != "source.summary_incomplete" {
		t.Fatalf("error_code = %v, want source.summary_incomplete", got)
	}
	if got, ok := result["skipped_datasets"].([]string); !ok || len(got) != 1 || got[0] != "aero-vault.objects" {
		t.Fatalf("skipped_datasets = %#v, want one skipped dataset", result["skipped_datasets"])
	}
}
