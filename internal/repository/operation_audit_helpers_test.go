package repository

import (
	"reflect"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestOperationStatusTransitionGuardMatchesTerminalBoundary(t *testing.T) {
	wantTerminal := []string{"unknown", "completed", "failed", "partially_completed"}
	cases := []struct {
		status domain.OperationStatus
		want   bool
	}{
		{status: domain.OperationRequested},
		{status: domain.OperationRunning},
		{status: domain.OperationWaiting},
		{status: domain.OperationUnknown, want: true},
		{status: domain.OperationPartiallyCompleted, want: true},
		{status: domain.OperationCompleted, want: true},
		{status: domain.OperationFailed, want: true},
		{status: domain.OperationCompensating},
	}
	for _, tc := range cases {
		terminal, isTerminal := operationStatusTransitionGuard(tc.status)
		if isTerminal != tc.want {
			t.Errorf("status %q terminal = %v, want %v", tc.status, isTerminal, tc.want)
		}
		if !reflect.DeepEqual(terminal, wantTerminal) {
			t.Errorf("status %q guard set = %v, want %v", tc.status, terminal, wantTerminal)
		}
	}
}
