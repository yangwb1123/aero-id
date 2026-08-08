//go:build integration

package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

const replayTestKey = "replay-test-signing-key-0123456789abcdef"

func startReplayLedger(t *testing.T, key string) (*gorm.DB, repository.AuditPublicationRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	repo := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: key})
	publications, ok := repo.(repository.AuditPublicationRepository)
	if !ok {
		t.Fatal("audit ledger repo must implement AuditPublicationRepository")
	}
	return db, publications
}

func replayFact(eventID domain.ID, tenantID string) *domain.AuditFact {
	return &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: tenantID + ":replay",
		EventID: eventID, TenantID: tenantID, ActorUID: "u-1", Action: "account.provisioned",
		ResourceType: "account", ResourceID: "account-1", OccurredAt: time.Now().UTC(),
		Payload: map[string]any{}}
}

// replayIntent builds the operator intent the repo tests drive, shaped
// exactly as the CLI's service-built intent: one fresh OperationID per
// intent, an envelope with the publication action, and the given tenant
// filter / binding allow-list.
func replayIntent(tenantFilter string, allowed []string) repository.ReplayIntent {
	operationID := idgen.NewID()
	return repository.ReplayIntent{
		OperationID: operationID, ActorUID: "ops-test", TenantFilter: tenantFilter, AllowedTenants: allowed,
		Envelope: domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "requeue",
			OperationID: operationID, CorrelationID: operationID, ActorUID: "ops-test",
			Action: "audit.publication.requeued", ResourceType: "audit_publication",
			OccurredAt: time.Now().UTC(), Payload: map[string]any{}},
	}
}

func deliveryReplayIntent(tenantFilter string, allowed []string) repository.ReplayIntent {
	operationID := idgen.NewID()
	return repository.ReplayIntent{
		OperationID: operationID, ActorUID: "ops-test", TenantFilter: tenantFilter, AllowedTenants: allowed,
		Envelope: domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: "requeue",
			OperationID: operationID, CorrelationID: operationID, ActorUID: "ops-test",
			Action: "callback.delivery.requeued", ResourceType: "callback_delivery",
			OccurredAt: time.Now().UTC(), Payload: map[string]any{}},
	}
}

// drivePublicationDeadLetter drives one fact through the public surface into
// the terminal dead_letter state: AppendInTx → ClaimPublication →
// FinishPublication(dead_letter). Returns the publication row ID.
func drivePublicationDeadLetter(t *testing.T, db *gorm.DB, publications repository.AuditPublicationRepository,
	tenantID string) domain.ID {
	t.Helper()
	ctx := context.Background()
	appendInTx, ok := publications.(repository.AuditLedgerTransactionalRepository)
	if !ok {
		t.Fatal("publication repo must implement AuditLedgerTransactionalRepository")
	}
	eventID := idgen.NewID()
	if err := db.Transaction(func(tx *gorm.DB) error {
		return appendInTx.AppendInTx(ctx, tx, replayFact(eventID, tenantID))
	}); err != nil {
		t.Fatalf("AppendInTx: %v", err)
	}
	claimed, err := publications.ClaimPublication(ctx, "test-worker", 5*time.Minute, 10)
	if err != nil {
		t.Fatalf("ClaimPublication: %v", err)
	}
	// Earlier requeues may have left other rows pending; the claim batch can
	// therefore contain rows that are not ours. Only the row backing THIS fact
	// is driven to dead_letter; every other claimed row is settled back to
	// pending so the fixture stays exactly one dead-letter row.
	var target *domain.AuditPublication
	for _, item := range claimed {
		if item.Fact != nil && item.Fact.EventID == eventID {
			target = item
			continue
		}
		if err := publications.FinishPublication(ctx, item.ID, "test-worker", item.Attempt,
			"pending", "", time.Now().UTC()); err != nil {
			t.Fatalf("restore foreign claim: %v", err)
		}
	}
	if target == nil {
		t.Fatalf("claimed batch %d did not include the seeded fact", len(claimed))
	}
	if err := publications.FinishPublication(ctx, target.ID, "test-worker", 1,
		"dead_letter", "audit.publication_failed", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("FinishPublication: %v", err)
	}
	return target.ID
}

func publicationState(t *testing.T, db *gorm.DB, id domain.ID) (status, errorCode string, attempt int, leaseOwner string, leaseUntil *time.Time) {
	t.Helper()
	var row struct {
		Status     string
		ErrorCode  string
		Attempt    int
		LeaseOwner string
		LeaseUntil *time.Time
	}
	if err := db.Raw(`SELECT status, error_code, attempt, coalesce(lease_owner, '') AS lease_owner, lease_until FROM audit_publications WHERE id = ?`, id).
		Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row.Status, row.ErrorCode, row.Attempt, row.LeaseOwner, row.LeaseUntil
}

// TestRequeuePublicationResetsDeadLetterRows (T-4.1): dead_letter → pending
// with attempt reset to 0, fresh next_attempt_at, lease cleared, error_code
// manual_replay; idempotent on re-run; one row per fact; other statuses
// untouched. The intent-based run mints one fact for the affected tenant.
func TestRequeuePublicationResetsDeadLetterRows(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)

	id1 := drivePublicationDeadLetter(t, db, publications, "tenant-a")
	id2 := drivePublicationDeadLetter(t, db, publications, "tenant-a")

	// A completed row must be untouched by the requeue: drive a fact to
	// pending via AppendInTx, then claim and settle it completed.
	appendInTx, ok := publications.(repository.AuditLedgerTransactionalRepository)
	if !ok {
		t.Fatal("publication repo must implement AuditLedgerTransactionalRepository")
	}
	completedEvent := idgen.NewID()
	if err := db.Transaction(func(tx *gorm.DB) error {
		return appendInTx.AppendInTx(ctx, tx, replayFact(completedEvent, "tenant-a"))
	}); err != nil {
		t.Fatal(err)
	}
	claimedCompleted, err := publications.ClaimPublication(ctx, "test-worker", 5*time.Minute, 10)
	if err != nil || len(claimedCompleted) != 1 {
		t.Fatalf("claim completed = %v, %v", claimedCompleted, err)
	}
	completedID := claimedCompleted[0].ID
	if err := publications.FinishPublication(ctx, completedID, "test-worker", 1, "completed", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	requeued, facts, err := publications.RequeuePublication(ctx, nil, 0, replayIntent("", nil))
	if err != nil {
		t.Fatalf("RequeuePublication: %v", err)
	}
	if requeued != 2 {
		t.Fatalf("requeued = %d, want 2 (dead-letter only)", requeued)
	}
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1 (one per affected tenant)", len(facts))
	}
	if facts[0].TenantID != "tenant-a" || facts[0].ActorUID != "ops-test" {
		t.Fatalf("fact = tenant %q actor %q, want tenant-a/ops-test (derived from the rows)", facts[0].TenantID, facts[0].ActorUID)
	}
	for _, id := range []domain.ID{id1, id2} {
		status, errorCode, attempt, leaseOwner, leaseUntil := publicationState(t, db, id)
		if status != "pending" || errorCode != "manual_replay" || attempt != 0 {
			t.Fatalf("row %s = %s/%s/%d, want pending/manual_replay/0", id, status, errorCode, attempt)
		}
		if leaseOwner != "" || leaseUntil != nil {
			t.Fatalf("row %s lease = %q/%v, want cleared", id, leaseOwner, leaseUntil)
		}
		var next time.Time
		if err := db.Raw(`SELECT next_attempt_at FROM audit_publications WHERE id = ?`, id).Scan(&next).Error; err != nil {
			t.Fatal(err)
		}
		if delta := time.Since(next); delta > 2*time.Minute || delta < -2*time.Minute {
			t.Fatalf("next_attempt_at = %v, want ≈ now", next)
		}
	}

	// Idempotency: a second run over the same rows requeues 0 and mints no
	// facts (no state change, no fact).
	requeued, facts, err = publications.RequeuePublication(ctx, nil, 0, replayIntent("", nil))
	if err != nil || requeued != 0 {
		t.Fatalf("second requeue = %d, %v; want 0, nil", requeued, err)
	}
	if len(facts) != 0 {
		t.Fatalf("second-run facts = %d, want 0 (0-row re-runs are fact-free)", len(facts))
	}

	// Exactly one publication row per fact.
	var perFact int64
	if err := db.Raw(`SELECT count(*) FROM (SELECT fact_id FROM audit_publications GROUP BY fact_id HAVING count(*) > 1) x`).Scan(&perFact).Error; err != nil {
		t.Fatal(err)
	}
	if perFact != 0 {
		t.Fatalf("duplicate publication rows per fact = %d, want 0", perFact)
	}

	// The completed row is untouched.
	status, _, attempt, _, _ := publicationState(t, db, completedID)
	if status != "completed" || attempt != 1 {
		t.Fatalf("completed row = %s/%d, want completed/1", status, attempt)
	}

	// Explicit ids bound the set and override the limit.
	id3 := drivePublicationDeadLetter(t, db, publications, "tenant-a")
	requeued, facts, err = publications.RequeuePublication(ctx, []domain.ID{id3}, 0, replayIntent("", nil))
	if err != nil || requeued != 1 {
		t.Fatalf("id-scoped requeue = %d, %v; want 1, nil", requeued, err)
	}
	if len(facts) != 1 {
		t.Fatalf("id-scoped facts = %d, want 1", len(facts))
	}
}

// TestRequeuePublicationFactAppendsInTx (AUD-1 / T1): the intent-based
// requeue commits the reset and one fact per affected tenant atomically; the
// fact carries the shared OperationID, the row-derived tenant, and the
// allow-listed five-key payload whose ids partition the affected set; the
// RowsAffected==selection invariant holds. An append failure (no signing
// key) rolls the whole requeue back.
func TestRequeuePublicationFactAppendsInTx(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)
	id := drivePublicationDeadLetter(t, db, publications, "tenant-a")

	intent := replayIntent("", nil)
	requeued, facts, err := publications.RequeuePublication(ctx, []domain.ID{id}, 0, intent)
	if err != nil || requeued != 1 {
		t.Fatalf("requeue with intent = %d, %v", requeued, err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(facts))
	}
	fact := facts[0]
	if fact.OperationID != intent.OperationID {
		t.Fatalf("fact operation_id = %v, want the run's %v", fact.OperationID, intent.OperationID)
	}
	if fact.CorrelationID != intent.OperationID {
		t.Fatalf("fact correlation_id = %v, want the run's operation id", fact.CorrelationID)
	}
	if fact.TenantID != "tenant-a" {
		t.Fatalf("fact tenant = %q, want tenant-a (derived from the row)", fact.TenantID)
	}
	if fact.PartitionKey != "tenant-a:requeue" {
		t.Fatalf("fact partition = %q, want tenant-a:requeue", fact.PartitionKey)
	}
	if fact.CausationID == (domain.ID{}) || fact.ResourceID == "" {
		t.Fatalf("single-row fact must carry the causation/resource link (causation %v resource %q)", fact.CausationID, fact.ResourceID)
	}
	payload, err := json.Marshal(fact.Payload)
	if err != nil {
		t.Fatal(err)
	}
	payloadText := string(payload)
	for _, key := range []string{"count", "publication_ids", "scope", "prior_error_code", "prior_attempt"} {
		if !strings.Contains(payloadText, `"`+key+`"`) {
			t.Fatalf("fact payload = %s, want allow-listed key %q", payloadText, key)
		}
	}
	if !strings.Contains(payloadText, id.String()) {
		t.Fatalf("fact payload = %s, want the affected row id %s", payloadText, id)
	}
	if strings.Contains(payloadText, "ops-test") {
		t.Fatalf("fact payload = %s, must not carry operator input (construction-only)", payloadText)
	}
	var ledgerRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE event_id = ?`, fact.EventID).Scan(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 1 {
		t.Fatalf("requeue fact ledger rows = %d, want 1", ledgerRows)
	}

	// Append failure rolls the requeue back: use an unkeyed repo over the SAME
	// database (fail-closed AppendInTx) — the reset must not survive.
	unkeyedRepo := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	unkeyed, ok := unkeyedRepo.(repository.AuditPublicationRepository)
	if !ok {
		t.Fatal("unkeyed ledger repo must implement AuditPublicationRepository")
	}
	id3 := drivePublicationDeadLetter(t, db, publications, "tenant-a")
	_, _, err = unkeyed.RequeuePublication(ctx, []domain.ID{id3}, 0, replayIntent("", nil))
	if err == nil {
		t.Fatal("unkeyed requeue with intent must fail")
	}
	status, _, _, _, _ := publicationState(t, db, id3)
	if status != "dead_letter" {
		t.Fatalf("row after rolled-back requeue = %s, want dead_letter", status)
	}
}

// TestRequeuePublicationPerTenantFacts (T2): one batch spanning two tenants
// mints ONE fact per tenant with distinct fresh v7 EventIDs, a shared
// OperationID, and payload id sets partitioned correctly — all in the same
// transaction.
func TestRequeuePublicationPerTenantFacts(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)
	idA1 := drivePublicationDeadLetter(t, db, publications, "tenant-a")
	idA2 := drivePublicationDeadLetter(t, db, publications, "tenant-a")
	idB := drivePublicationDeadLetter(t, db, publications, "tenant-b")

	intent := replayIntent("", nil)
	requeued, facts, err := publications.RequeuePublication(ctx, nil, 0, intent)
	if err != nil || requeued != 3 {
		t.Fatalf("requeue = %d, %v; want 3, nil", requeued, err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want 2 (one per tenant)", len(facts))
	}
	byTenant := map[string]*domain.AuditFact{}
	for _, fact := range facts {
		byTenant[fact.TenantID] = fact
	}
	for _, tenantID := range []string{"tenant-a", "tenant-b"} {
		fact, ok := byTenant[tenantID]
		if !ok {
			t.Fatalf("no fact for tenant %q", tenantID)
		}
		if fact.OperationID != intent.OperationID {
			t.Fatalf("tenant %q fact operation_id = %v, want shared %v", tenantID, fact.OperationID, intent.OperationID)
		}
		if fact.EventID.Version() != 7 {
			t.Fatalf("tenant %q fact event id version = %d, want v7 (fresh per run+leg+tenant)", tenantID, fact.EventID.Version())
		}
		ids, _ := fact.Payload["publication_ids"].([]string)
		if tenantID == "tenant-a" && len(ids) != 2 {
			t.Fatalf("tenant-a payload ids = %d, want 2", len(ids))
		}
		if tenantID == "tenant-b" && (len(ids) != 1 || !strings.Contains(fmt.Sprint(ids), idB.String())) {
			t.Fatalf("tenant-b payload ids = %v, want exactly the tenant-b row %s", ids, idB)
		}
	}
	if byTenant["tenant-a"].EventID == byTenant["tenant-b"].EventID {
		t.Fatal("tenant facts share an event id — fresh per (run, leg, tenant) is broken")
	}
	// Both ids of the tenant-a fact are present in its payload.
	payloadA, _ := json.Marshal(byTenant["tenant-a"].Payload)
	for _, id := range []domain.ID{idA1, idA2} {
		if !strings.Contains(string(payloadA), id.String()) {
			t.Fatalf("tenant-a payload = %s, missing affected row %s", payloadA, id)
		}
	}
}

// TestRequeuePublicationEmptyTenantFailsClosed (audit 2a): a dead_letter row
// whose ledger fact carries an empty tenant cannot be attributed — the whole
// batch rolls back with ErrAuditReplayUnattributableTenant, zero rows, zero
// facts. (Fail-closed: the direct inverse of the landed silent nil-fact path.)
func TestRequeuePublicationEmptyTenantFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)
	drivePublicationDeadLetter(t, db, publications, "tenant-a")
	// Simulate the legacy/corrupt row: a ledger fact with an empty tenant (the
	// append boundary validates tenant, so such rows can only predate it or
	// arrive via a maintenance path). The append-only trigger forbids UPDATE,
	// so the row is constructed by INSERT like a pre-validation fact.
	badFactID := idgen.NewID()
	if err := db.Exec(`INSERT INTO audit_ledger (id, chain_sequence, source, source_region, partition_key,
			event_id, tenant_id, action, occurred_at, payload_hash, record_hash)
		VALUES (?, 0, 'aero-id', 'local', 'legacy:corrupt', ?, '', 'account.provisioned',
			now(), 'h', 'h')`, badFactID, idgen.NewID()).Error; err != nil {
		t.Fatal(err)
	}
	badID := idgen.NewID()
	if err := db.Exec(`INSERT INTO audit_publications (id, fact_id, status) VALUES (?, ?, 'dead_letter')`, badID, badFactID).Error; err != nil {
		t.Fatal(err)
	}

	_, _, err := publications.RequeuePublication(ctx, nil, 0, replayIntent("", nil))
	if err == nil {
		t.Fatal("requeue over an unattributable row must fail closed")
	}
	if !errors.Is(err, repository.ErrAuditReplayUnattributableTenant) {
		t.Fatalf("error = %v, want ErrAuditReplayUnattributableTenant", err)
	}
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 2 {
		t.Fatalf("dead-letter rows after refused requeue = %d, want 2 (zero mutation)", dead)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 0 {
		t.Fatalf("requeue facts after refused requeue = %d, want 0", facts)
	}
}

// TestRequeuePublicationUnboundTenantRollsBack (audit 2b/3): the in-tx
// binding check refuses a batch whose locked selection contains a tenant
// outside AllowedTenants (and not 'legacy') — zero rows, zero facts; the
// 'legacy' exclusion mirrors the boot coverage gate.
func TestRequeuePublicationUnboundTenantRollsBack(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)
	drivePublicationDeadLetter(t, db, publications, "tenant-a")
	drivePublicationDeadLetter(t, db, publications, "tenant-unbound")

	_, _, err := publications.RequeuePublication(ctx, nil, 0, replayIntent("", []string{"tenant-a"}))
	if err == nil {
		t.Fatal("requeue over an unbound tenant must roll back")
	}
	if !errors.Is(err, repository.ErrAuditReplayTenantNotBound) {
		t.Fatalf("error = %v, want ErrAuditReplayTenantNotBound", err)
	}
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 2 {
		t.Fatalf("dead-letter rows after refused requeue = %d, want 2 (zero mutation)", dead)
	}
	var factCount int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&factCount).Error; err != nil {
		t.Fatal(err)
	}
	if factCount != 0 {
		t.Fatalf("requeue facts after refused requeue = %d, want 0", factCount)
	}

	// The tenant filter narrows the selection: with -tenant tenant-a the
	// unbound row is excluded and the bound rows requeue fine.
	requeued, facts, err := publications.RequeuePublication(ctx, nil, 0, replayIntent("tenant-a", []string{"tenant-a"}))
	if err != nil || requeued != 1 {
		t.Fatalf("filtered requeue = %d, %v; want 1, nil", requeued, err)
	}
	if len(facts) != 1 || facts[0].TenantID != "tenant-a" {
		t.Fatalf("filtered facts = %+v, want one tenant-a fact", facts)
	}

	// Clear the still-dead unbound row via its own filter, then prove 'legacy'
	// rows pass without being listed (boot-gate exclusion mirrored).
	requeued, _, err = publications.RequeuePublication(ctx, nil, 0, replayIntent("tenant-unbound", nil))
	if err != nil || requeued != 1 {
		t.Fatalf("unbound-row clear = %d, %v; want 1, nil", requeued, err)
	}
	legacyID := drivePublicationDeadLetter(t, db, publications, "legacy")
	requeued, facts, err = publications.RequeuePublication(ctx, nil, 0, replayIntent("", []string{"tenant-a"}))
	if err != nil || requeued != 1 {
		t.Fatalf("legacy requeue = %d, %v; want 1, nil", requeued, err)
	}
	if len(facts) != 1 || facts[0].TenantID != "legacy" {
		t.Fatalf("legacy facts = %+v, want one legacy fact", facts)
	}
	if status, _, _, _, _ := publicationState(t, db, legacyID); status != "pending" {
		t.Fatalf("legacy row = %s, want pending", status)
	}
}

// TestRequeuePublicationSecondRequeueMintsDistinctFact (T-10.2.5 / R3): a row
// that fails again after a first requeue can be requeued a second time; the
// second requeue commits and the ledger holds TWO distinct v7 facts — the C2
// fresh-event-ID doctrine, not ledger dedup, is the idempotency boundary
// between runs.
func TestRequeuePublicationSecondRequeueMintsDistinctFact(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)
	id := drivePublicationDeadLetter(t, db, publications, "tenant-a")

	firstIntent := replayIntent("", nil)
	n, facts1, err := publications.RequeuePublication(ctx, []domain.ID{id}, 0, firstIntent)
	if err != nil || n != 1 {
		t.Fatalf("first requeue = %d, %v; want 1, nil", n, err)
	}
	if len(facts1) != 1 {
		t.Fatalf("first facts = %d, want 1", len(facts1))
	}
	fact1 := facts1[0]
	if fact1.EventID.Version() != 7 {
		t.Fatalf("fact1 event id version = %d, want v7", fact1.EventID.Version())
	}

	// The requeued row fails again and re-enters dead_letter. The requeue fact
	// itself produced a second pending publication row (it drains to
	// governance too), so the claim batch holds both; only the original
	// account.provisioned row is driven back to dead_letter.
	claimed, err := publications.ClaimPublication(ctx, "worker-2", 5*time.Minute, 10)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("re-claim after first requeue = %d, %v; want 2 (original + requeue fact)", len(claimed), err)
	}
	var target *domain.AuditPublication
	for _, item := range claimed {
		if item.Fact != nil && item.Fact.Action == "account.provisioned" {
			target = item
			break
		}
	}
	if target == nil {
		t.Fatal("original fact row not claimed")
	}
	if err := publications.FinishPublication(ctx, target.ID, "worker-2", 1,
		"dead_letter", "audit.publication_failed", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Second requeue under a NEW run's intent (fresh OperationID + fresh
	// EventIDs): must commit, not fold or roll back.
	secondIntent := replayIntent("", nil)
	if secondIntent.OperationID == firstIntent.OperationID {
		t.Fatal("test intents share an operation id — the run boundary is broken")
	}
	n, facts2, err := publications.RequeuePublication(ctx, []domain.ID{id}, 0, secondIntent)
	if err != nil || n != 1 {
		t.Fatalf("second requeue = %d, %v; want 1, nil (fresh v7 must not collide)", n, err)
	}
	if len(facts2) != 1 {
		t.Fatalf("second facts = %d, want 1", len(facts2))
	}
	fact2 := facts2[0]
	if fact2.EventID.Version() != 7 {
		t.Fatalf("fact2 event id version = %d, want v7", fact2.EventID.Version())
	}
	if fact1.EventID == fact2.EventID {
		t.Fatal("requeue facts share an event id — the C2 fresh-mint doctrine is broken")
	}
	if fact1.OperationID == fact2.OperationID {
		t.Fatal("requeue facts share an operation id — each run mints its own")
	}

	// Both facts are durable, distinct, and the row is pending again.
	for _, eventID := range []domain.ID{fact1.EventID, fact2.EventID} {
		var rows int64
		if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("ledger rows for requeue fact %s = %d, want 1", eventID, rows)
		}
	}
	status, _, _, _, _ := publicationState(t, db, id)
	if status != "pending" {
		t.Fatalf("row after second requeue = %s, want pending", status)
	}
}

// TestConcurrentPurgeAndRequeueRace (F4): the operator requeue tool and the
// server purge tick can run at the same time. Every row transitions at most
// once: the requeue UPDATE matches only dead_letter rows, the purge DELETE
// re-checks the outer status qual via EPQ against the new row version (and
// SKIP LOCKED skips rows locked by an in-flight requeue), so the totals
// partition exactly — purged + requeued == seeded, and every surviving row is
// pending with a cleared lease. (The deterministic proof of the EPQ guard is
// TestPurgeDeleteBlockedByRequeueCommitEPQ; this test is the aggregate
// partition stress.)
func TestConcurrentPurgeAndRequeueRace(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)

	const seeded = 20
	ids := make([]domain.ID, 0, seeded)
	for i := 0; i < seeded; i++ {
		ids = append(ids, drivePublicationDeadLetter(t, db, publications, "tenant-a"))
	}
	cutoff := time.Now().UTC().Add(time.Hour) // every row is past retention

	var (
		wg       sync.WaitGroup
		errCh    = make(chan error, 2)
		purged   atomic.Int64
		requeued atomic.Int64
	)
	wg.Add(2)
	go func() { // server purge tick: bounded scans until nothing is eligible
		defer wg.Done()
		for {
			n, err := publications.PurgeDeadLetterPublications(ctx, cutoff, 4)
			if err != nil {
				errCh <- fmt.Errorf("purge: %w", err)
				return
			}
			purged.Add(n)
			if n == 0 {
				return
			}
		}
	}()
	go func() { // operator requeue tool: bounded batches until nothing is left
		defer wg.Done()
		intent := replayIntent("", []string{"tenant-a"})
		for {
			n, _, err := publications.RequeuePublication(ctx, nil, 4, intent)
			if err != nil {
				errCh <- fmt.Errorf("requeue: %w", err)
				return
			}
			requeued.Add(int64(n))
			if n == 0 {
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	// Exact partition: every seeded row was either purged or requeued, once.
	if got := purged.Load() + requeued.Load(); got != seeded {
		t.Fatalf("purged(%d) + requeued(%d) = %d, want %d (a row must not transition twice)",
			purged.Load(), requeued.Load(), got, seeded)
	}
	// Survivors are pending with a cleared lease; nothing else remains.
	var pending int64
	for _, id := range ids {
		var count int64
		if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE id = ? AND status = 'pending'`, id).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		pending += count
	}
	if pending != requeued.Load() {
		t.Fatalf("pending rows = %d, want %d (== requeued count)", pending, requeued.Load())
	}
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status IN ('dead_letter', 'delivering') AND id IN ?`, ids).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 0 {
		t.Fatalf("rows left in dead_letter/delivering = %d, want 0", dead)
	}
}

// TestRequeuePublicationLeaseInvariant (T-4.3 case 1): a delivering row with a
// live lease is unreachable by RequeuePublication (status predicate) and by a
// purge scan (lease guard).
func TestRequeuePublicationLeaseInvariant(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)
	appendInTx, ok := publications.(repository.AuditLedgerTransactionalRepository)
	if !ok {
		t.Fatal("publication repo must implement AuditLedgerTransactionalRepository")
	}
	eventID := idgen.NewID()
	if err := db.Transaction(func(tx *gorm.DB) error {
		return appendInTx.AppendInTx(ctx, tx, replayFact(eventID, "tenant-a"))
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := publications.ClaimPublication(ctx, "worker-1", 5*time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	// Row is delivering with a live lease: requeue must not touch it (and the
	// 0-row run mints no facts).
	requeued, facts, err := publications.RequeuePublication(ctx, nil, 0, replayIntent("", nil))
	if err != nil || requeued != 0 {
		t.Fatalf("requeue over leased row = %d, %v; want 0, nil", requeued, err)
	}
	if len(facts) != 0 {
		t.Fatalf("facts over leased row = %d, want 0", len(facts))
	}
	// Purge scan with a far-future cutoff must not delete it either.
	purged, err := publications.PurgeDeadLetterPublications(ctx, time.Now().UTC().Add(time.Hour), 10)
	if err != nil || purged != 0 {
		t.Fatalf("purge over leased row = %d, %v; want 0, nil", purged, err)
	}
	status, _, _, leaseOwner, _ := publicationState(t, db, claimed[0].ID)
	if status != "delivering" || leaseOwner != "worker-1" {
		t.Fatalf("leased row = %s/%q, want delivering/worker-1", status, leaseOwner)
	}
}

// TestRequeueDeadResetsDeliveries (T-4.1 delivery leg): dead_letter callback
// deliveries are requeued with attempt reset to 0, a fresh retry budget and
// manual_replay marking; idempotent on re-run; non-dead-letter rows untouched;
// the per-tenant fact commits in the same transaction.
func TestRequeueDeadResetsDeliveries(t *testing.T) {
	ctx := context.Background()
	db, callbacks, _ := startCallbackAuditRepo(t, replayTestKey)

	// Seed a verified subscription and drive two deliveries to dead_letter.
	sub := &domain.CallbackSubscription{ID: idgen.NewID(), OwnerUID: "u-1", TenantID: "tenant-a",
		Endpoint: "https://cb.example.test/hook", EventTypes: []string{"account.provisioned"},
		SecretCiphertext: "cipher", Enabled: true, Verified: true}
	if err := callbacks.CreateSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	var deliveryIDs []domain.ID
	for i := 0; i < 2; i++ {
		d := &domain.CallbackDelivery{SubscriptionID: sub.ID, EventID: idgen.NewID(),
			Action: "account.provisioned", Payload: map[string]any{"n": i}}
		if err := callbacks.EnqueueDelivery(ctx, d); err != nil {
			t.Fatal(err)
		}
		fetched, err := callbacks.FetchDueDeliveries(ctx, 10, "test-worker", 5*time.Minute)
		if err != nil || len(fetched) != 1 {
			t.Fatalf("fetch = %v, %v; want 1", fetched, err)
		}
		if err := callbacks.MarkDelivery(ctx, fetched[0].ID, "test-worker", "dead_letter", 0, "", "delivery.failed",
			time.Now().UTC().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		deliveryIDs = append(deliveryIDs, fetched[0].ID)
	}

	requeued, facts, err := callbacks.RequeueDead(ctx, nil, 0, deliveryReplayIntent("", nil))
	if err != nil || requeued != 2 {
		t.Fatalf("RequeueDead = %d, %v; want 2, nil", requeued, err)
	}
	if len(facts) != 1 || facts[0].TenantID != "tenant-a" {
		t.Fatalf("facts = %+v, want one tenant-a fact (derived via the subscription)", facts)
	}
	for _, id := range deliveryIDs {
		var row struct {
			Status     string
			ErrorCode  string
			Attempt    int
			LeaseOwner any
		}
		if err := db.Raw(`SELECT status, error_code, attempt, lease_owner FROM callback_deliveries WHERE id = ?`, id).Scan(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Status != "pending" || row.ErrorCode != "manual_replay" || row.Attempt != 0 || row.LeaseOwner != nil {
			t.Fatalf("delivery %s = %s/%s/%d/%v, want pending/manual_replay/0/cleared",
				id, row.Status, row.ErrorCode, row.Attempt, row.LeaseOwner)
		}
	}

	// Idempotent re-run (0 rows, no facts); requeued rows are claimable again.
	if n, facts, err := callbacks.RequeueDead(ctx, nil, 0, deliveryReplayIntent("", nil)); err != nil || n != 0 || len(facts) != 0 {
		t.Fatalf("second RequeueDead = %d, facts %d, %v; want 0/0/nil", n, len(facts), err)
	}
	fetched, err := callbacks.FetchDueDeliveries(ctx, 10, "test-worker", 5*time.Minute)
	if err != nil || len(fetched) != 2 {
		t.Fatalf("re-claim after requeue = %d, %v; want 2, nil", len(fetched), err)
	}
}

// TestRequeueVsPurgeInterleaving (T-4.3 case 2): both orders of
// requeue/purge on the same dead-letter rows are safe — requeue never errors
// on already-purged rows, purge never deletes a requeued row, and the audit
// fact always survives in the ledger. The in-flight order (requeue holds the
// row lock while the purge DELETE blocks) is proven deterministically by
// TestPurgeDeleteBlockedByRequeueCommitEPQ.
func TestRequeueVsPurgeInterleaving(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)

	seed := func() (domain.ID, domain.ID) {
		id1 := drivePublicationDeadLetter(t, db, publications, "tenant-a")
		id2 := drivePublicationDeadLetter(t, db, publications, "tenant-a")
		return id1, id2
	}
	cutoff := time.Now().UTC().Add(time.Hour) // everything is past retention

	// Order (a): requeue then purge → requeue affects N, purge deletes 0.
	idA1, idA2 := seed()
	if n, _, err := publications.RequeuePublication(ctx, nil, 0, replayIntent("", nil)); err != nil || n != 2 {
		t.Fatalf("requeue-first: %d, %v; want 2, nil", n, err)
	}
	if n, err := publications.PurgeDeadLetterPublications(ctx, cutoff, 10); err != nil || n != 0 {
		t.Fatalf("purge after requeue = %d, %v; want 0, nil", n, err)
	}
	for _, id := range []domain.ID{idA1, idA2} {
		if status, _, _, _, _ := publicationState(t, db, id); status != "pending" {
			t.Fatalf("requeue-first row = %s, want pending", status)
		}
	}

	// Order (b): purge then requeue → purge deletes N, requeue affects 0 and
	// succeeds (no error on the vanished rows).
	idB1, idB2 := seed()
	if n, err := publications.PurgeDeadLetterPublications(ctx, cutoff, 10); err != nil || n != 2 {
		t.Fatalf("purge-first: %d, %v; want 2, nil", n, err)
	}
	if n, _, err := publications.RequeuePublication(ctx, []domain.ID{idB1, idB2}, 0, replayIntent("", nil)); err != nil || n != 0 {
		t.Fatalf("requeue after purge = %d, %v; want 0, nil", n, err)
	}

	// The ledger facts survive every publication delete.
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE tenant_id = 'tenant-a' AND action = 'account.provisioned'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 4 {
		t.Fatalf("ledger facts = %d, want 4 (publication deletes never cascade to the fact)", facts)
	}
}

// TestPurgeDeadLetterPublicationsPredicate (T-4.3): only dead_letter rows past
// the cutoff with a cleared lease are purged; in-retention and leased rows
// survive. The scan is ordered by (updated_at, id) — the purge index shape.
func TestPurgeDeadLetterPublicationsPredicate(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)

	old1 := drivePublicationDeadLetter(t, db, publications, "tenant-a")
	old2 := drivePublicationDeadLetter(t, db, publications, "tenant-a")
	if err := db.Exec(`UPDATE audit_publications SET updated_at = now() - interval '10 days' WHERE id IN (?, ?)`, old1, old2).Error; err != nil {
		t.Fatal(err)
	}
	// In-retention row: keep updated_at fresh.
	fresh := drivePublicationDeadLetter(t, db, publications, "tenant-a")

	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	purged, err := publications.PurgeDeadLetterPublications(ctx, cutoff, 10)
	if err != nil || purged != 2 {
		t.Fatalf("purge = %d, %v; want 2, nil", purged, err)
	}
	for _, id := range []domain.ID{old1, old2} {
		var count int64
		if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE id = ?`, id).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("purged row %s still present", id)
		}
	}
	if status, _, _, _, _ := publicationState(t, db, fresh); status != "dead_letter" {
		t.Fatalf("in-retention row = %s, want dead_letter (kept)", status)
	}
}

// TestAuditReplayCountIsBounded (db §4 / T3): the dry-run count is bounded by
// the same selection core as the next mutating run — with a 120-row backlog
// and limit 100 the count is 100 (not 120), and after the mutating run it
// drops to 20, then 0. The count is exact for -id selections.
func TestAuditReplayCountIsBounded(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)

	const backlog = 120
	for i := 0; i < backlog; i++ {
		drivePublicationDeadLetter(t, db, publications, "tenant-a")
	}
	count, err := publications.CountDeadLetterPublications(ctx, nil, 100, "")
	if err != nil || count != 100 {
		t.Fatalf("bounded count = %d, %v; want 100 (not the full 120 backlog)", count, err)
	}
	// limit<=0 means the default 100 (identical to the mutating run's
	// selection core — the dry-run estimate can never overstate the run);
	// an explicit big limit reports the full backlog.
	count, err = publications.CountDeadLetterPublications(ctx, nil, 0, "")
	if err != nil || count != 100 {
		t.Fatalf("default-limit count = %d, %v; want 100 (bounded like the mutating run)", count, err)
	}
	count, err = publications.CountDeadLetterPublications(ctx, nil, 1000, "")
	if err != nil || count != backlog {
		t.Fatalf("explicit-limit count = %d, %v; want %d (full backlog)", count, err, backlog)
	}
	// Tenant filter narrows the count.
	count, err = publications.CountDeadLetterPublications(ctx, nil, 100, "tenant-other")
	if err != nil || count != 0 {
		t.Fatalf("filtered count = %d, %v; want 0", count, err)
	}
	// The mutating run affects exactly the bounded count and the next count
	// drops to the residual.
	requeued, _, err := publications.RequeuePublication(ctx, nil, 100, replayIntent("", nil))
	if err != nil || requeued != 100 {
		t.Fatalf("mutating run = %d, %v; want 100", requeued, err)
	}
	count, err = publications.CountDeadLetterPublications(ctx, nil, 100, "")
	if err != nil || count != 20 {
		t.Fatalf("residual count = %d, %v; want 20", count, err)
	}
	// -id is exact.
	ids := make([]domain.ID, 0, backlog)
	var allIDs []domain.ID
	if err := db.Raw(`SELECT id FROM audit_publications WHERE status = 'dead_letter' ORDER BY next_attempt_at, id`).Scan(&allIDs).Error; err != nil {
		t.Fatal(err)
	}
	ids = append(ids, allIDs[:3]...)
	count, err = publications.CountDeadLetterPublications(ctx, ids, 0, "")
	if err != nil || count != 3 {
		t.Fatalf("id-scoped count = %d, %v; want 3", count, err)
	}
}

// TestDeadLetterTenantsReport (T2): the tenant report lists the distinct
// tenants of the would-be selection, sorted.
func TestDeadLetterTenantsReport(t *testing.T) {
	ctx := context.Background()
	db, publications := startReplayLedger(t, replayTestKey)
	drivePublicationDeadLetter(t, db, publications, "tenant-b")
	drivePublicationDeadLetter(t, db, publications, "tenant-a")
	drivePublicationDeadLetter(t, db, publications, "tenant-a")

	tenants, err := publications.DeadLetterTenants(ctx, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tenants) != 2 || tenants[0] != "tenant-a" || tenants[1] != "tenant-b" {
		t.Fatalf("tenants = %v, want [tenant-a tenant-b]", tenants)
	}
	tenants, err = publications.DeadLetterTenants(ctx, nil, 0, "tenant-b")
	if err != nil || len(tenants) != 1 || tenants[0] != "tenant-b" {
		t.Fatalf("filtered tenants = %v, %v; want [tenant-b]", tenants, err)
	}
}
