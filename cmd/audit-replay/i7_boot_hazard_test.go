//go:build integration

// I7-boot-hazard behavior tests (post-remediation): the two hazards the file
// originally pinned — (a) an un-audited default invocation and (b) requeue
// facts mis-attributed to an operator-chosen tenant that the next server boot
// would reject — are CLOSED by the remediation plan
// (docs/plans/2026-08-08-dead-letter-replay-remediation-plan.md §2/T2):
//
//   - -actor is REQUIRED in mutating mode (the un-audited default is deleted);
//     a run without it exits 1 with zero mutation and zero facts.
//   - -tenant is a SELECTION FILTER; the fact tenant derives from the rows via
//     the ledger/subscription join, so mis-attribution is structurally
//     impossible. An unbound tenant is refused pre-mutation (friendly
//     pre-flight) and in-tx (authoritative); the boot coverage gate's
//     backstop for requeue facts is unreachable by construction.
//
// The boot-coverage query replication (bootCoverageTenants) is kept for the
// filter-semantics assertions: a filtered run mints no facts, so it cannot
// introduce any tenant into the coverage set.

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// bootCoverageTenants replicates verbatim the coverage query in
// internal/app/audit.go validateAuditBindingCoverage: the tenants the next
// server boot (governance enabled) would require bindings for.
func bootCoverageTenants(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var tenants []string
	err := db.Raw(`
        SELECT tenant_id FROM accounts WHERE tenant_id <> '' AND tenant_id <> 'legacy'
        UNION
        SELECT ledger.tenant_id
          FROM audit_publications publication
          JOIN audit_ledger ledger ON ledger.id = publication.fact_id
         WHERE publication.status IN ('pending', 'delivering', 'dead_letter')
           AND ledger.tenant_id <> '' AND ledger.tenant_id <> 'legacy'`).Scan(&tenants).Error
	if err != nil {
		t.Fatalf("coverage query: %v", err)
	}
	return tenants
}

func bootCoverageFails(t *testing.T, db *gorm.DB, bound map[string]bool) error {
	t.Helper()
	for _, tenantID := range bootCoverageTenants(t, db) {
		if !bound[tenantID] {
			return fmt.Errorf("audit governance bindings do not cover durable tenant state (tenant %q uncovered)", tenantID)
		}
	}
	return nil
}

func seedDeadDelivery(t *testing.T, db *gorm.DB, callbacks repository.CallbackRepository, tenantID string) {
	t.Helper()
	ctx := context.Background()
	sub := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: tenantID,
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{"account.provisioned"},
		SecretCiphertext: "cipher", Enabled: true, Verified: true}
	if err := callbacks.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	d := &domain.CallbackDelivery{SubscriptionID: sub.ID, EventID: idgen.NewID(),
		Action: "account.provisioned", Payload: map[string]any{}}
	if err := callbacks.EnqueueDelivery(ctx, d); err != nil {
		t.Fatal(err)
	}
	fetched, err := callbacks.FetchDueDeliveries(ctx, 10, "probe-worker", 5*time.Minute)
	if err != nil || len(fetched) != 1 {
		t.Fatalf("fetch = %v, %v; want 1", fetched, err)
	}
	if err := callbacks.MarkDelivery(ctx, fetched[0].ID, "probe-worker", "dead_letter", 0, "", "delivery.failed",
		time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// TestAuditReplayDefaultInvocationRequiresActor (I7 inverted): with no
// -actor the tool fails closed BEFORE any mutation — exit 1, zero rows
// requeued, zero audit facts. This is the direct inverse of the landed
// contract (which committed an un-audited requeue).
func TestAuditReplayDefaultInvocationRequiresActor(t *testing.T) {
	db, cfg, publications, _ := startReplayEnv(t)
	seedDeadLetterPublication(t, db, publications, "tenant-a")

	_, err := runCapture(t, db, cfg, "-kind", "publication", "-limit", "10")
	if err == nil {
		t.Fatal("default invocation without -actor must fail (exit 1)")
	}
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 1 {
		t.Fatalf("dead-letter rows after refused run = %d, want 1 (zero mutation)", dead)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 0 {
		t.Fatalf("requeue facts after refused run = %d, want 0 (fail-closed)", facts)
	}
}

// TestAuditReplayUnboundTenantActsAsFilter (I7 publication leg, rewritten):
// -tenant is a selection filter, not an attribution flag. Requeuing
// tenant-a rows with -tenant tenant-unbound selects nothing: requeued 0, no
// facts, and the coverage set is untouched (no mis-attributed fact can enter
// it). A tenant filter that DOES match still derives the fact tenant from the
// rows.
func TestAuditReplayUnboundTenantActsAsFilter(t *testing.T) {
	db, cfg, publications, _ := startReplayEnv(t)
	seedDeadLetterPublication(t, db, publications, "tenant-a")

	// Filter with no matching rows: empty selection, no facts, no mutation.
	out, err := runCapture(t, db, cfg, "-kind", "publication", "-limit", "10",
		"-tenant", "tenant-unbound", "-actor", "ops-bot")
	if err != nil {
		t.Fatalf("tool run: %v", err)
	}
	if !strings.Contains(out, "requeued 0 publication(s)") {
		t.Fatalf("filtered run output = %q, want requeued 0", out)
	}
	var factTenantCount int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&factTenantCount).Error; err != nil {
		t.Fatal(err)
	}
	if factTenantCount != 0 {
		t.Fatalf("requeue facts after filtered run = %d, want 0", factTenantCount)
	}
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 1 {
		t.Fatalf("dead-letter rows after filtered run = %d, want 1 (unchanged)", dead)
	}
	// The boot coverage set is untouched: no tenant was introduced.
	if err := bootCoverageFails(t, db, map[string]bool{"tenant-a": true}); err != nil {
		t.Fatalf("boot should pass with the seeded tenant bound: %v", err)
	}

	// A matching filter requeues the row; the fact tenant derives from the
	// ROW (tenant-a), never from the filter value.
	out, err = runCapture(t, db, cfg, "-kind", "publication", "-limit", "10",
		"-tenant", "tenant-a", "-actor", "ops-bot")
	if err != nil {
		t.Fatalf("matching filter run: %v", err)
	}
	if !strings.Contains(out, "requeued 1 publication(s)") {
		t.Fatalf("matching filter output = %q, want requeued 1", out)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued' AND tenant_id = 'tenant-a'`).Scan(&factTenantCount).Error; err != nil {
		t.Fatal(err)
	}
	if factTenantCount != 1 {
		t.Fatalf("requeue facts with row-derived tenant = %d, want 1", factTenantCount)
	}
}

// TestAuditReplayUnboundTenantRowsRefused (I7 binding enforcement): with
// governance enabled (AllowedTenants = {"tenant-a"}), rows of an unbound
// tenant are refused pre-mutation — the requeue never runs, zero rows, zero
// facts, and the coverage gate never sees the tenant.
func TestAuditReplayUnboundTenantRowsRefused(t *testing.T) {
	db, cfg, publications, _ := startReplayEnv(t)
	seedDeadLetterPublication(t, db, publications, "tenant-a")
	seedDeadLetterPublication(t, db, publications, "tenant-unbound")

	// Hand-built config with governance enabled and tenant-a bound.
	cfg.AuditGovernance.Enabled = true
	cfg.AuditGovernance.Bindings = map[string]config.AuditGovernanceBindingConfig{
		"tenant-a": {TokenURL: "https://x", ClientID: "c", ClientSecretEnv: "S", Scope: "s"},
	}

	out, err := runCapture(t, db, cfg, "-kind", "publication", "-limit", "10", "-actor", "ops-bot")
	if err == nil {
		t.Fatalf("run over an unbound tenant must fail; output = %q", out)
	}
	var requeuedRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'pending'`).Scan(&requeuedRows).Error; err != nil {
		t.Fatal(err)
	}
	if requeuedRows != 0 {
		t.Fatalf("rows requeued before the binding check = %d, want 0 (zero mutation)", requeuedRows)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 0 {
		t.Fatalf("requeue facts after refused run = %d, want 0", facts)
	}
}

// TestAuditReplayDeliveryFilteredRunIntroducesNothing (I7 delivery leg,
// rewritten): the delivery leg's -tenant filter has the same selection
// semantics — a non-matching filter requeues nothing and mints no facts, so
// the boot coverage set is never extended by a mis-attributed delivery fact.
func TestAuditReplayDeliveryFilteredRunIntroducesNothing(t *testing.T) {
	db, cfg, _, callbacks := startReplayEnv(t)
	seedDeadDelivery(t, db, callbacks, "tenant-b")

	if err := bootCoverageFails(t, db, map[string]bool{"tenant-b": true}); err != nil {
		t.Fatalf("pre-requeue boot should pass (delivery rows are not scanned): %v", err)
	}

	out, err := runCapture(t, db, cfg, "-kind", "delivery", "-limit", "10",
		"-tenant", "tenant-unbound", "-actor", "ops-bot")
	if err != nil {
		t.Fatalf("tool run: %v", err)
	}
	if !strings.Contains(out, "requeued 0 delivery(ies)") {
		t.Fatalf("filtered delivery run output = %q, want requeued 0", out)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'callback.delivery.requeued'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 0 {
		t.Fatalf("delivery requeue facts after filtered run = %d, want 0", facts)
	}
	if err := bootCoverageFails(t, db, map[string]bool{"tenant-b": true}); err != nil {
		t.Fatalf("filtered run must not extend the coverage set: %v", err)
	}
}
