package app

import (
	"testing"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/service"
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
	if err := validateAuditLedgerConfig(&config.Config{AuditLedger: config.AuditLedgerConfig{SigningKey: "5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d"}}); err != nil {
		t.Fatalf("non-empty signing key rejected: %v", err)
	}
}

// TestValidateAuditGovernanceGateMatrix (T-10.2, L10.2.1): the composition
// root's activation-gate re-check is the full 2×2 — enabled+0-bindings fails
// with the exact full documented error, disabled+0 and enabled+1 succeed, and
// disabled+1 succeeds. A failure-cell-only matrix would let a reversed
// condition (gate firing while disabled) pass, which is why the disabled
// success cells are asserted here and the real-file Load leg stays green.
func TestValidateAuditGovernanceGateMatrix(t *testing.T) {
	const wantErr = auditGovernanceActivationGateError
	binding := map[string]config.AuditGovernanceBindingConfig{"tenant-one": {}}

	t.Run("enabled+zero bindings fails (exact full string)", func(t *testing.T) {
		err := validateAuditGovernanceGate(&config.Config{AuditGovernance: config.AuditGovernanceConfig{Enabled: true}})
		if err == nil {
			t.Fatal("enabled+0-bindings must fail the gate")
		}
		if err.Error() != wantErr {
			t.Fatalf("gate error = %q, want byte-identical %q (wrong constant or drifted message)", err.Error(), wantErr)
		}
	})
	t.Run("disabled+zero bindings succeeds (kill switch)", func(t *testing.T) {
		if err := validateAuditGovernanceGate(&config.Config{AuditGovernance: config.AuditGovernanceConfig{}}); err != nil {
			t.Fatalf("disabled+0-bindings rejected: %v", err)
		}
	})
	t.Run("enabled+binding succeeds", func(t *testing.T) {
		if err := validateAuditGovernanceGate(&config.Config{AuditGovernance: config.AuditGovernanceConfig{
			Enabled: true, Bindings: binding,
		}}); err != nil {
			t.Fatalf("enabled+1-binding rejected: %v", err)
		}
	})
	t.Run("disabled+binding succeeds", func(t *testing.T) {
		if err := validateAuditGovernanceGate(&config.Config{AuditGovernance: config.AuditGovernanceConfig{
			Bindings: binding,
		}}); err != nil {
			t.Fatalf("disabled+1-binding rejected: %v", err)
		}
	})
	t.Run("nil config succeeds (no panic)", func(t *testing.T) {
		if err := validateAuditGovernanceGate(nil); err != nil {
			t.Fatalf("nil config rejected: %v", err)
		}
	})
}

// TestBuildAuditStackRefusesEnabledZeroBindings (T-10.2, L10.2.3): the gate
// must refuse at the composition root with the exact full documented error
// BEFORE any DB or connector use. The nil gdb is load-bearing: with a real
// gdb the incidental connector refusal ("audit governance: invalid
// configuration", oauth.go) would make an `err != nil` assertion pass with
// the gate completely absent, and gate-before-DB ordering would become
// untestable (validateAuditBindingCoverage on a nil gdb panics, so a nil
// pointer here proves the gate fired first). The SigningKey must be present:
// validateAuditLedgerConfig runs before the gate.
func TestBuildAuditStackRefusesEnabledZeroBindings(t *testing.T) {
	cfg := &config.Config{
		AuditLedger: config.AuditLedgerConfig{SigningKey: "5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d"},
		AuditGovernance: config.AuditGovernanceConfig{
			Enabled: true, SourcePrefix: "aero-id", Endpoint: "https://audit-governance.internal",
		},
	}
	callbackSvc, auditLedger, ledgerRepo, err := buildAuditStack(cfg, nil,
		service.NewRegistryControlPlaneMetrics(metrics.NewRegistry()))
	if err == nil {
		t.Fatal("buildAuditStack with enabled+0-bindings must refuse")
	}
	if err.Error() != auditGovernanceActivationGateError {
		t.Fatalf("buildAuditStack error = %q, want byte-identical %q (incidental connector refusal is a false green)",
			err.Error(), auditGovernanceActivationGateError)
	}
	if callbackSvc != nil || auditLedger != nil || ledgerRepo != nil {
		t.Fatal("refused buildAuditStack must not return partial stack values")
	}
}
