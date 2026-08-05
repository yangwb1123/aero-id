package domain

import "testing"

func TestLimitGrantRequiresExplicitUnlimited(t *testing.T) {
	if err := (LimitGrant{}).Validate(); err != nil {
		t.Fatalf("finite hard zero should be valid zero capacity: %v", err)
	}
	if err := (LimitGrant{Unlimited: true, Hard: 1}).Validate(); err == nil {
		t.Fatal("ambiguous unlimited grant was accepted")
	}
}
