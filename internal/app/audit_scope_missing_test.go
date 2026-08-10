package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector/auditgovernance"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/service"
)

// assertScopeMissingExposition pins the family exposition on the registry:
// exactly one series (the single label set {source="aero-id"}) whose value is
// exactly want. A substring match would be a false green (" 1" is a substring
// of " 12"), so the whole line is compared. want == 0 asserts the family is
// absent entirely (the noop-sink leg of T-10.1b).
func assertScopeMissingExposition(t *testing.T, reg *metrics.Registry, want int) {
	t.Helper()
	var buf strings.Builder
	reg.WriteText(&buf)
	var lines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, "aero_control_audit_scope_missing_total") {
			lines = append(lines, line)
		}
	}
	if want == 0 {
		if len(lines) != 0 {
			t.Fatalf("scope_missing exposition = %v, want no series (noop sink must leave the family absent)", lines)
		}
		return
	}
	wantLine := fmt.Sprintf("aero_control_audit_scope_missing_total{source=%q} %d", "aero-id", want)
	if len(lines) != 1 {
		t.Fatalf("scope_missing series count = %d (%v), want exactly 1 (single label set)", len(lines), lines)
	}
	if lines[0] != wantLine {
		t.Fatalf("scope_missing exposition = %q, want %q", lines[0], wantLine)
	}
}

// TestBuildAuditScopeMissingHookIncrementsExactlyOncePerCall (T-10.1a): the
// extracted hook-builder seam increments the shared registry family exactly
// once per invocation — not zero, not twice. The hook ignores its tenantID
// argument (the label is the configured source prefix, per the connector
// contract at token_invalidation_test.go:643).
func TestBuildAuditScopeMissingHookIncrementsExactlyOncePerCall(t *testing.T) {
	reg := metrics.NewRegistry()
	hook := buildAuditScopeMissingHook(service.NewRegistryControlPlaneMetrics(reg), "aero-id")

	hook("tenant-one")
	assertScopeMissingExposition(t, reg, 1)

	hook("tenant-one")
	assertScopeMissingExposition(t, reg, 2)
}

// TestBuildAuditScopeMissingHookNormalizesLabelAndNoop (T-10.1b): the label
// is the TrimSpace'd configured source prefix (a whitespace-padded prefix
// still emits {source="aero-id"}), and a noop sink leaves the family absent —
// the noop fallback is explicit and never the hook's accidental sink.
func TestBuildAuditScopeMissingHookNormalizesLabelAndNoop(t *testing.T) {
	reg := metrics.NewRegistry()
	hook := buildAuditScopeMissingHook(service.NewRegistryControlPlaneMetrics(reg), " aero-id ")
	hook("tenant-one")
	assertScopeMissingExposition(t, reg, 1)

	noopReg := metrics.NewRegistry()
	hook = buildAuditScopeMissingHook(service.NewRegistryControlPlaneMetrics(nil), "aero-id")
	hook("tenant-one")
	assertScopeMissingExposition(t, noopReg, 0)
}

// recordingBindingCoverage is the FM-4 non-vacuity fake: it records every
// invocation so T-10.1c can prove the coverage seam actually ran during
// buildAuditPublishers. A fake that merely returns nil proves nothing about
// the gate being wired — the call count makes the assertion non-vacuous.
type recordingBindingCoverage struct {
	calls int
}

func (r *recordingBindingCoverage) CheckAuditBindingCoverage(map[string]config.AuditGovernanceBindingConfig) error {
	r.calls++
	return nil
}

// TestBuildAuditPublishersWiresScopeHookThroughRealConnector (T-10.1c): the
// full seam path with no DB — fake coverage + httptest /token answering
// 400 {"error":"invalid_scope"} + registry-backed metrics + one binding
// (secret via t.Setenv, mirroring auditGovernanceTestConfig). Publish must
// increment aero_control_audit_scope_missing_total{source="aero-id"} exactly
// once per rejection: not zero (proves the provided controlMetrics reached
// the hook — non-noop wiring) and not twice (no per-publish double increment
// at the seam). The returned error must carry the scope-missing
// classification, the token-endpoint marker (A2 at the 400-gate site), and
// must stay mutually exclusive with ErrTokenRejected. The coverage fake must
// record >= 1 call — the seam runs during construction (FM-4).
func TestBuildAuditPublishersWiresScopeHookThroughRealConnector(t *testing.T) {
	t.Setenv("TEST_AUDIT_SECRET", "audit-secret-value-000000000000000001")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
	}))
	defer server.Close()
	cfg := &config.Config{AuditGovernance: config.AuditGovernanceConfig{
		Enabled: true, Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]config.AuditGovernanceBindingConfig{
			"tenant-a": {TokenURL: server.URL + "/token", ClientID: "audit-client",
				ClientSecretEnv: "TEST_AUDIT_SECRET", Scope: "audit:event:write",
				Resources: []string{"audit-governance"}},
		},
	}}
	reg := metrics.NewRegistry()
	coverage := &recordingBindingCoverage{}

	publishers, err := buildAuditPublishers(cfg, service.NewRegistryControlPlaneMetrics(reg), coverage)
	if err != nil {
		t.Fatal(err)
	}
	if len(publishers) != 1 {
		t.Fatalf("publishers = %d, want 1", len(publishers))
	}
	if coverage.calls < 1 {
		t.Fatalf("binding-coverage seam calls = %d, want >= 1 (the gate must run during construction — FM-4 non-vacuity)", coverage.calls)
	}

	fact := &domain.AuditFact{ID: idgen.NewID(), TenantID: "tenant-a", ActorUID: "user-1",
		Action: "account.profile.updated", OccurredAt: time.Now().UTC()}
	for want := 1; want <= 2; want++ {
		publishErr := publishers[0].Publish(t.Context(), fact)
		if !errors.Is(publishErr, auditgovernance.ErrTokenScopeMissing) {
			t.Fatalf("publish error = %v, want ErrTokenScopeMissing", publishErr)
		}
		if !errors.Is(publishErr, auditgovernance.ErrTokenEndpointScopeMissing) {
			t.Fatalf("publish error = %v, want the ErrTokenEndpointScopeMissing marker (A2 400-gate site)", publishErr)
		}
		if errors.Is(publishErr, auditgovernance.ErrTokenRejected) {
			t.Fatalf("publish error = %v, must stay mutually exclusive with ErrTokenRejected", publishErr)
		}
		assertScopeMissingExposition(t, reg, want)
	}
}

// TestBuildAuditPublishersNilCoverageSeamFailsClosed (FM-9): a nil
// auditBindingCoverage seam must fail wiring, never silently skip the
// coverage gate (the exact silent-loss class the outbox contract forbids).
// The guard sits after the Enabled early-return, so the kill-switch path
// (Enabled: false) stays behaviorally identical and returns nil, nil even
// with a nil seam.
func TestBuildAuditPublishersNilCoverageSeamFailsClosed(t *testing.T) {
	sink := service.NewRegistryControlPlaneMetrics(metrics.NewRegistry())
	cfg := &config.Config{AuditGovernance: config.AuditGovernanceConfig{
		Enabled: true, SourcePrefix: "aero-id",
		Bindings: map[string]config.AuditGovernanceBindingConfig{"tenant-a": {}},
	}}
	publishers, err := buildAuditPublishers(cfg, sink, nil)
	if err == nil {
		t.Fatal("nil binding-coverage seam must fail closed at wiring time")
	}
	if err.Error() != auditBindingCoverageSeamError {
		t.Fatalf("nil-seam error = %q, want byte-identical %q", err.Error(), auditBindingCoverageSeamError)
	}
	if publishers != nil {
		t.Fatal("failed buildAuditPublishers must not return partial publishers")
	}

	cfg.AuditGovernance.Enabled = false
	publishers, err = buildAuditPublishers(cfg, sink, nil)
	if err != nil || publishers != nil {
		t.Fatalf("disabled + nil seam = (%v, %v), want (nil, nil) (the kill switch must not require the seam)", publishers, err)
	}
}
