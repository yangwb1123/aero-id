package idgen

import (
	"testing"

	"github.com/google/uuid"
)

// documentedAnchorNamespace pins the namespace the derivation contract names.
// internal/config's TestAuditAnchorSchemeParity pins the same value from the
// documentation side; this file pins it from the minting side, so a rotation
// without a scheme bump turns both suites red instead of one.
const documentedAnchorNamespace = "38248e3a-32f0-4929-9b56-b2dbd75aa849"

// goldenSeparatorA is the golden S1 row from TestAnchor_GoldenConstants:
// Anchor("ab", "c"). Duplicated as a constant here so the inertness test fails
// with a single, unambiguous message; the golden table remains the authority
// and is never edited.
const goldenSeparatorA = "251d9788-4b10-57f0-a935-e4ca289d517e"

// hostileNamespace is a well-formed v5-shaped UUID that is deliberately not the
// documented namespace. Any derived ID that moves when this value is installed
// on the exported mirror proves derivation still reads that mirror.
var hostileNamespace = uuid.MustParse("11111111-2222-4333-8444-555555555555")

// TestAnchorNamespace_ExportedMirrorMatchesPrivateCopy is the drift detector:
// the exported mirror is initialized from the unexported derivation source, so
// this also catches any same-package reassignment of the private copy.
func TestAnchorNamespace_ExportedMirrorMatchesPrivateCopy(t *testing.T) {
	if got := AnchorNamespace.String(); got != documentedAnchorNamespace {
		t.Fatalf("exported mirror = %s, want documented %s — a namespace rotation without a scheme bump is a rollout hazard", got, documentedAnchorNamespace)
	}
	if AnchorNamespace != anchorNamespace {
		t.Fatalf("exported mirror %s drifted from the derivation namespace %s — Anchor derives from the private copy, the mirror is documentation only", AnchorNamespace, anchorNamespace)
	}
}

// TestAnchorNamespace_ReassignmentIsInert is the acceptance test for the
// never-rotate contract: writing the exported idgen.AnchorNamespace must leave
// every derived ID byte-identical. It is deliberately NOT parallel — the package
// runs tests sequentially so this mutation cannot race the golden table, and the
// t.Cleanup restore leaves TestAnchor_GoldenConstants green in the same binary.
func TestAnchorNamespace_ReassignmentIsInert(t *testing.T) {
	orig := AnchorNamespace
	t.Cleanup(func() { AnchorNamespace = orig })

	if got := Anchor("ab", "c").String(); got != goldenSeparatorA {
		t.Fatalf("pre-mutation Anchor(ab, c) = %s, want golden S1 %s", got, goldenSeparatorA)
	}

	AnchorNamespace = hostileNamespace
	if AnchorNamespace != hostileNamespace {
		t.Fatalf("fixture invalid: assigning idgen.AnchorNamespace did not take effect, so every assertion below would pass vacuously")
	}

	if got := Anchor("ab", "c").String(); got != goldenSeparatorA {
		t.Errorf("Anchor(ab, c) = %s after idgen.AnchorNamespace was set to %s, want golden S1 %s — Anchor must derive from the unexported namespace, not the exported mirror", got, hostileNamespace, goldenSeparatorA)
	}
	// The handler's poison-event quarantine shape (internal/handler/grpc
	// event_ingest.go) is the named beneficiary of this contract; assert it
	// stays a pure function of its parts after the mutation.
	quarantine := Anchor("event.quarantine.v1", "wire-fingerprint")
	if again := Anchor("event.quarantine.v1", "wire-fingerprint"); again != quarantine {
		t.Errorf("Anchor is not stable across identical calls after the mirror was reassigned: %s vs %s", quarantine, again)
	}
	if quarantine.Version() != 5 {
		t.Errorf("quarantine anchor = %s version %d, want 5", quarantine, quarantine.Version())
	}
	if got := AnchorNamespace; got != hostileNamespace {
		t.Errorf("exported mirror = %s, want the injected %s — the export must stay an assignable read surface", got, hostileNamespace)
	}
	if anchorNamespace != orig {
		t.Errorf("private anchorNamespace = %s, want %s — the derivation source must be untouched by a write to the exported mirror", anchorNamespace, orig)
	}
	if got := anchorNamespace.String(); got != documentedAnchorNamespace {
		t.Errorf("private anchorNamespace = %s, want documented %s", got, documentedAnchorNamespace)
	}
}
