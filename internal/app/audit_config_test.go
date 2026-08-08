package app

import (
	"testing"

	"github.com/aero/aero-id/internal/config"
)

// TestCompositionRootValidatesAuditSigningKey (FM-8): an empty ledger signing
// key must fail startup instead of turning every audit append (and every
// converted mutation) into a runtime failure. The dev placeholder in
// configs/config.yaml is non-empty, so the out-of-box config still boots.
func TestCompositionRootValidatesAuditSigningKey(t *testing.T) {
	if err := validateAuditLedgerConfig(&config.Config{AuditLedger: config.AuditLedgerConfig{SigningKey: ""}}); err == nil {
		t.Fatal("empty signing key must fail startup validation")
	}
	if err := validateAuditLedgerConfig(&config.Config{AuditLedger: config.AuditLedgerConfig{SigningKey: "   "}}); err == nil {
		t.Fatal("blank signing key must fail startup validation")
	}
	if err := validateAuditLedgerConfig(&config.Config{AuditLedger: config.AuditLedgerConfig{SigningKey: "change-me-please-very-long-audit-key"}}); err != nil {
		t.Fatalf("non-empty signing key rejected: %v", err)
	}
}
