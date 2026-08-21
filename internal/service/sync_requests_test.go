package service

import (
	"errors"
	"testing"

	"github.com/aero/aero-id/internal/connector"
)

func TestEraseCallOutcomeDoesNotTurnUnknownIntoFailure(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		status      string
		wantUnknown bool
		wantFailed  bool
	}{
		{name: "timeout", err: &connector.RemoteError{Kind: connector.ErrorTimeout}, status: "unknown", wantUnknown: true},
		{name: "unavailable", err: &connector.RemoteError{Kind: connector.ErrorUnavailable}, status: "unknown", wantUnknown: true},
		{name: "pending", err: &connector.RemoteError{Kind: connector.ErrorUnknown}, status: "unknown", wantUnknown: true},
		{name: "rejected", err: &connector.RemoteError{Kind: connector.ErrorInvalid}, status: "failed", wantFailed: true},
		{name: "local", err: errors.New("transport mapping missing"), status: "failed", wantFailed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, unknown, failed := eraseCallOutcome(tc.err)
			if status != tc.status || unknown != tc.wantUnknown || failed != tc.wantFailed {
				t.Fatalf("outcome = %q/%v/%v, want %q/%v/%v", status, unknown, failed,
					tc.status, tc.wantUnknown, tc.wantFailed)
			}
		})
	}
}

func TestEraseSourceOutcomeKeepsPendingRemoteStateUnknown(t *testing.T) {
	cases := map[string]string{
		"completed": "completed",
		"success":   "completed",
		"accepted":  "unknown",
		"":          "unknown",
		"failed":    "failed",
		"cancelled": "failed",
	}
	for input, want := range cases {
		if got := eraseSourceOutcome(input); got != want {
			t.Errorf("eraseSourceOutcome(%q) = %q, want %q", input, got, want)
		}
	}
}
