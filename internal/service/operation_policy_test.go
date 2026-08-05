package service

import "testing"

func TestOperationPolicy_AllowsOnlyImplementedTypesAndActions(t *testing.T) {
	for _, value := range []string{"sync", "erase", "source.command", "replay"} {
		if !allowedOperationType(value) {
			t.Errorf("operation type %q should be allowed", value)
		}
	}
	for _, value := range []string{"export", "profile.update", "unknown"} {
		if allowedOperationType(value) {
			t.Errorf("operation type %q should be rejected", value)
		}
	}
	cases := []struct {
		typeName string
		action   string
		allowed  bool
	}{
		{typeName: "sync", action: "summary", allowed: true},
		{typeName: "sync", action: "erase", allowed: false},
		{typeName: "erase", action: "source.erase", allowed: true},
		{typeName: "erase", action: "summary", allowed: false},
		{typeName: "source.command", action: "summary", allowed: true},
		{typeName: "replay", action: "delete", allowed: true},
	}
	for _, tc := range cases {
		if got := operationActionMatchesType(tc.typeName, tc.action); got != tc.allowed {
			t.Errorf("%s/%s = %v, want %v", tc.typeName, tc.action, got, tc.allowed)
		}
	}
}
