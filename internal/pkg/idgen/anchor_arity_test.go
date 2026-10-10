package idgen

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// This file holds the arity policy in one place: the enforced floor, the one
// grandfathered shape, and the fail-fast guard both imply. It is a sibling of
// idgen_test.go (golden derivation bytes) rather than a section of it, so the
// encoding contract and the arity contract stay separately readable.

// TestAnchor_MinimumArity (D-1 delta): a site whose parts were dropped must
// fail fast rather than derive. Arity is part of the encoding, so a short
// anchor yields a valid, perfectly stable UUIDv5 that the audit ledger then
// dedups on (tenant_id, source, event_id) — folding a later execution of that
// truncated shape onto the first row with no error and no log line. The
// enforced floor is minAnchorArity (4) for every discriminator; the
// event.quarantine.v1 + wire-fingerprint surrogate is the single named
// exception and is pinned separately by
// TestAnchor_QuarantineSurrogateGrandfathered.
func TestAnchor_MinimumArity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []string
	}{
		{"zero_parts", nil},
		{"one_part", []string{"x"}},
		// The class this policy exists to close: two parts was legal before
		// the floor was raised, and every non-surrogate site is >= 4.
		{"two_parts_non_surrogate", []string{"x", "y"}},
		// The concrete dropped-slot shape: audit_hold.removed without its
		// scope_type slot. Derives a stable, silently folding ID if allowed.
		{"three_parts_audit_hold_removed", []string{"audit_hold.removed", goldenTenant, "acct-1024"}},
		// The exemption is arity-exact, so a dropped fingerprint on the
		// surrogate is a dropped slot like any other.
		{"surrogate_one_part", []string{"event.quarantine.v1"}},
		{"surrogate_three_parts", []string{"event.quarantine.v1", "fp", "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := panicText(func() { Anchor(tc.parts...) })
			if !ok {
				t.Fatalf("Anchor(%q): %d parts is below the enforced floor and must panic", tc.parts, len(tc.parts))
			}
			if !strings.Contains(msg, "minAnchorArity") {
				t.Fatalf("panic %q must name the enforced floor minAnchorArity", msg)
			}
		})
	}

	// The arity guard runs before the NUL guard: an under-arity call whose part
	// also carries U+0000 must report the arity error, not the NUL error.
	if msg, ok := panicText(func() { Anchor("x\x00") }); !ok || !strings.Contains(msg, "minAnchorArity") {
		t.Fatalf(`Anchor("x\x00") = (%q, panicked=%v), want the minAnchorArity panic`, msg, ok)
	}

	// The floor itself derives: arity exactly minAnchorArity is a legal anchor,
	// not a degenerate one, and must stay byte-identical for repeat callers.
	id := Anchor("a", "b", "c", "d")
	if id.Version() != 5 || id.Variant() != uuid.RFC4122 {
		t.Fatalf("floor-arity anchor %s is not a well-formed RFC4122 v5", id)
	}
	if id != Anchor("a", "b", "c", "d") {
		t.Fatalf("floor-arity anchor is not deterministic: %s != %s", id, Anchor("a", "b", "c", "d"))
	}
}

// TestAnchor_QuarantineSurrogateGrandfathered pins the one shape that is
// deliberately below the floor. It derives byte-identically to the shipped
// golden G17 (and therefore to every event_id already stored for a quarantined
// source event), the exemption is exact in both directions, and the exemption
// does not bypass the NUL guard.
func TestAnchor_QuarantineSurrogateGrandfathered(t *testing.T) {
	// G17's shipped bytes: the surrogate shape must not re-key stored rows.
	if got := Anchor("event.quarantine.v1", "example-fingerprint").String(); got != "68c8ca5a-323b-53c0-ab6b-af5876dad94a" {
		t.Fatalf(`Anchor("event.quarantine.v1","example-fingerprint") = %s, want the shipped golden G17 68c8ca5a-323b-53c0-ab6b-af5876dad94a`, got)
	}
	// A different fingerprint still derives (and derives differently): the
	// policy keys on arity plus discriminator, never on fingerprint content.
	const fp = "fp"
	derived := Anchor("event.quarantine.v1", fp)
	if derived.String() != "62c2b902-f8db-509b-a7a5-157d6dd7d599" {
		t.Fatalf(`Anchor("event.quarantine.v1","fp") = %s, want 62c2b902-f8db-509b-a7a5-157d6dd7d599`, derived)
	}
	if again := Anchor("event.quarantine.v1", fp); again != derived {
		t.Fatalf("surrogate anchor is not deterministic: %s != %s", derived, again)
	}
	if derived == Anchor("event.quarantine.v1", "fp2") {
		t.Fatal("the fingerprint part must participate in the surrogate's encoding")
	}

	// Arity-exact in the other direction too: one part and three parts are
	// both rejected (asserted by TestAnchor_MinimumArity), so the exemption
	// cannot be widened to "two parts is fine" without turning those red.
	if Anchor("event.quarantine.v1", "fp") == Anchor("x", "y", "z", "w") {
		t.Fatal("the grandfathered surrogate must not share a derived ID with a floor-arity shape")
	}

	// The exemption bypasses the arity guard only; a grandfathered call whose
	// fingerprint carries U+0000 still trips the NUL guard.
	msg, ok := panicText(func() { Anchor("event.quarantine.v1", "fp\x00") })
	if !ok || !strings.Contains(msg, "NUL byte") {
		t.Fatalf(`Anchor("event.quarantine.v1","fp\x00") = (%q, panicked=%v), want the NUL-byte panic — the exemption must not bypass the ambiguity guard`, msg, ok)
	}

	// An empty fingerprint is outside this policy: constraining content here
	// would be a separate decision, and the live non-empty precondition is
	// enforced by the callers that build WireFingerprint.
	if Anchor("event.quarantine.v1", "") == (ID{}) {
		t.Fatal("test premise broken: the surrogate must still derive for an empty fingerprint")
	}
}

// TestAnchor_SubFloorNonSurrogatePanics is the direction's named example
// alongside a control pair proving the guard does not over-reject the house
// shapes it is meant to protect: the 4-part audit_hold.removed (G10) and the
// 6-part region.promote (G14) must keep deriving their stored bytes.
func TestAnchor_SubFloorNonSurrogatePanics(t *testing.T) {
	dropped := []string{"audit_hold.removed", goldenTenant, "acct-1024"}
	msg, ok := panicText(func() { Anchor(dropped...) })
	if !ok {
		t.Fatalf("Anchor(%q) derived; dropping the scope_type slot must fail fast, not derive a ledger-dedupable ID", dropped)
	}
	if !strings.Contains(msg, "minAnchorArity=4") {
		t.Fatalf("panic %q must name the enforced floor minAnchorArity=4", msg)
	}

	if got := Anchor("audit_hold.removed", goldenTenant, "account", "acct-1024").String(); got != "5d349dda-a820-5abf-a9f0-efe421a58f0f" {
		t.Fatalf("4-part audit_hold.removed anchor = %s, want the golden G10 5d349dda-a820-5abf-a9f0-efe421a58f0f", got)
	}
	if got := Anchor("audit", "region.promote", goldenTenant, goldenOP, "us-east", "0").String(); got != "0c9b9631-6da3-5c20-813d-4ec76e5dc961" {
		t.Fatalf("6-part region.promote anchor = %s, want the golden G14 0c9b9631-6da3-5c20-813d-4ec76e5dc961", got)
	}
}
