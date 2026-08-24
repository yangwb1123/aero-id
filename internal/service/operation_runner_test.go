package service

import (
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
)

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
