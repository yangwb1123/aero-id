package repository

import (
	"bytes"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"gorm.io/datatypes"
)

func TestSyncJobFinishUpdatesPreservesCompletionAndReconcileLeaseRules(t *testing.T) {
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	result := []byte(`{"sources":{}}`)
	cases := []struct {
		name      string
		status    domain.SyncJobStatus
		code      string
		completed bool
		reconcile bool
	}{
		{name: "completed", status: domain.SyncJobCompleted, completed: true},
		{name: "failed", status: domain.SyncJobFailed, completed: true},
		{name: "partial source failure", status: domain.SyncJobPartiallyComplete, code: "erase.partial_failure", completed: true},
		{name: "local erase projection failure", status: domain.SyncJobPartiallyComplete, code: "erase.local_projection_failed", reconcile: true},
		{name: "unknown", status: domain.SyncJobUnknown, code: "erase.unknown_outcome", reconcile: true},
		{name: "running", status: domain.SyncJobRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			updates := syncJobFinishUpdates(tc.status, tc.code, "message", result, now)
			if updates["status"] != string(tc.status) || updates["error_code"] != tc.code || updates["error_message"] != "message" {
				t.Fatalf("status/error fields = %#v, want %q/%q/message", updates, tc.status, tc.code)
			}
			if !bytes.Equal(updates["result"].(datatypes.JSON), result) {
				t.Fatalf("result = %s, want %s", updates["result"], result)
			}
			if tc.completed {
				if got := updates["completed_at"]; got != now {
					t.Errorf("completed_at = %v, want %v", got, now)
				}
			} else if updates["completed_at"] != nil {
				t.Errorf("completed_at = %v, want nil", updates["completed_at"])
			}
			if tc.reconcile {
				if updates["lease_kind"] != "reconcile" || updates["lease_until"] == nil {
					t.Errorf("reconcile lease = %q/%v, want reconcile/non-nil", updates["lease_kind"], updates["lease_until"])
				}
			} else if updates["lease_kind"] != "" || updates["lease_until"] != nil {
				t.Errorf("lease fields = %q/%v, want empty/nil", updates["lease_kind"], updates["lease_until"])
			}
		})
	}
}
