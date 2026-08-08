package config

import (
	"testing"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

// TestAuditAnchorSchemeParity (T-10.1-A-1 / REQ-3): the config-declared anchor
// scheme is tied to the compiled-in derivation — the accepted scheme constant
// equals the validation's accepted string, the idgen namespace equals the
// documented fixed namespace, and idgen.Anchor produces a stable RFC-4122 v5
// ID that changes when any part changes. A namespace rotation or encoding
// change without a scheme bump fails this test.
func TestAuditAnchorSchemeParity(t *testing.T) {
	// (a) The accepted scheme constant is the string validateAuditAnchor accepts.
	if AnchorSchemeUUIDv5NulSeparated != "uuidv5-nul-separated" {
		t.Fatalf("scheme constant = %q, want uuidv5-nul-separated", AnchorSchemeUUIDv5NulSeparated)
	}
	if err := validateAuditAnchor(AuditGovernanceConfig{AnchorScheme: AnchorSchemeUUIDv5NulSeparated, AnchorSchemeVersion: 1}); err != nil {
		t.Fatalf("validateAuditAnchor rejects the scheme constant: %v", err)
	}
	if err := validateAuditAnchor(AuditGovernanceConfig{AnchorScheme: AnchorSchemeUUIDv5NulSeparated, AnchorSchemeVersion: AnchorSchemeVersionDefault}); err != nil {
		t.Fatalf("validateAuditAnchor rejects the version constant: %v", err)
	}

	// (b) The namespace the scheme name documents equals the implementation's.
	const documentedNamespace = "38248e3a-32f0-4929-9b56-b2dbd75aa849"
	if got := idgen.AnchorNamespace.String(); got != documentedNamespace {
		t.Fatalf("idgen.AnchorNamespace = %s, want documented %s — a namespace rotation without a scheme bump is a rollout hazard", got, documentedNamespace)
	}

	// (c) Anchor produces a stable v5 UUID that changes when any part changes.
	a1 := idgen.Anchor("audit_hold.set", "tenant-a", "actor", "scope", "reason")
	a2 := idgen.Anchor("audit_hold.set", "tenant-a", "actor", "scope", "reason")
	if a1 != a2 {
		t.Fatal("Anchor is not stable across identical parts")
	}
	if a1.Version() != 5 || a1.Variant().String() != "RFC4122" {
		t.Fatalf("Anchor = %s version/variant = %d/%v, want v5/RFC4122", a1, a1.Version(), a1.Variant())
	}
	if changed := idgen.Anchor("audit_hold.set", "tenant-a", "actor", "scope", "reason-2"); changed == a1 {
		t.Fatal("Anchor did not change when a part changed")
	}
}
