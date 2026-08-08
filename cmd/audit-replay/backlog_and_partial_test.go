//go:build integration

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/service"
)

// TestAuditReplayDryRunCountIsBounded (db §4 / T3): the dry-run count is
// bounded at -limit and equals exactly what the next mutating run will
// affect — with a 250-row backlog and -limit 100 it prints 100, never 250
// (GORM's Count ignores LIMIT on count(*); the bounded form counts the
// derived id-selection table).
func TestAuditReplayDryRunCountIsBounded(t *testing.T) {
	db, cfg, publications, _ := startReplayEnv(t)
	for i := 0; i < 250; i++ {
		seedDeadLetterPublication(t, db, publications, "tenant-a")
	}
	out, err := runCapture(t, db, cfg, "-kind", "publication", "-dry-run", "-limit", "100")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "publications to requeue: 100 (dry run; capped at -limit 100)") {
		t.Fatalf("dry run output = %q, want the bounded count 100 (not the 250-row backlog)", out)
	}
	if !strings.Contains(out, "affected tenants: tenant-a") {
		t.Fatalf("dry run output = %q, want the affected-tenant report", out)
	}
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 250 {
		t.Fatalf("dead-letter rows after dry run = %d, want 250 (unchanged)", dead)
	}
}

// TestAuditReplayCLIBacklogExceedsLimit (R2 / T3): with backlog > limit the
// CLI is a single bounded batch per run — the operator protocol is
// loop-until-zero: runs print 100/100/50/0, the dry-run prints the bounded
// value at each stage, and each mutating run mints exactly ONE fact (per
// tenant), not one per row.
func TestAuditReplayCLIBacklogExceedsLimit(t *testing.T) {
	db, cfg, publications, _ := startReplayEnv(t)
	for i := 0; i < 250; i++ {
		seedDeadLetterPublication(t, db, publications, "tenant-a")
	}
	run := func() string {
		t.Helper()
		out, err := runCapture(t, db, cfg, "-kind", "publication", "-limit", "100", "-actor", "ops-bot")
		if err != nil {
			t.Fatalf("mutating run: %v", err)
		}
		return out
	}
	dry := func() string {
		t.Helper()
		out, err := runCapture(t, db, cfg, "-kind", "publication", "-dry-run", "-limit", "100")
		if err != nil {
			t.Fatalf("dry run: %v", err)
		}
		return out
	}
	for _, want := range []string{
		"publications to requeue: 100 (dry run; capped at -limit 100)",
		"requeued 100 publication(s)",
		"publications to requeue: 100 (dry run; capped at -limit 100)",
		"requeued 100 publication(s)",
		"publications to requeue: 50 (dry run; capped at -limit 100)",
		"requeued 50 publication(s)",
		"publications to requeue: 0 (dry run; capped at -limit 100)",
		"requeued 0 publication(s)",
	} {
		var out string
		if strings.Contains(want, "dry run") {
			out = dry()
		} else {
			out = run()
		}
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want %q", out, want)
		}
	}
	// Exactly one fact per mutating run (3 runs affected rows), never per row.
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 3 {
		t.Fatalf("requeue facts = %d, want 3 (one per mutating run)", facts)
	}
	var pending int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'pending' AND error_code = 'manual_replay'`).Scan(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 250 {
		t.Fatalf("pending manual_replay rows = %d, want 250 (all requeued)", pending)
	}
}

// TestAuditReplayLegResumeAfterPartial (R6 / T-5): a kill between the two
// legs of -kind both leaves publications requeued and deliveries still
// dead-letter — invisible to any single-kind check. The -report journal is
// the durable marker: leg A's line exists, leg B's does not. Re-running
// -kind both completes leg B idempotently (leg A affects 0 rows and mints
// nothing) and the journal gains leg B's line under the NEW run's
// operation_id.
func TestAuditReplayLegResumeAfterPartial(t *testing.T) {
	db, cfg, publications, callbacks := startReplayEnv(t)
	for i := 0; i < 3; i++ {
		seedDeadLetterPublication(t, db, publications, "tenant-a")
	}
	for i := 0; i < 2; i++ {
		seedDeadDelivery(t, db, callbacks, "tenant-b")
	}
	journal := filepath.Join(t.TempDir(), "replay.journal")

	// The kill point: leg 1 (publications) executes, leg 2 never starts — the
	// exact call replay() makes for the publication leg.
	intent, err := service.BuildAuditReplayIntent(context.Background(), "publication", "ops-bot", "", nil, domain.ID{})
	if err != nil {
		t.Fatal(err)
	}
	if err := replayPublications(publications, nil, 100, "", intent, false, journal); err != nil {
		t.Fatalf("leg 1: %v", err)
	}

	// Durable partial state: publications requeued, deliveries untouched.
	var pendingPub, deadDel int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'pending' AND error_code = 'manual_replay'`).Scan(&pendingPub).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM callback_deliveries WHERE status = 'dead_letter'`).Scan(&deadDel).Error; err != nil {
		t.Fatal(err)
	}
	if pendingPub != 3 || deadDel != 2 {
		t.Fatalf("partial state = pubs %d dels %d, want 3/2", pendingPub, deadDel)
	}
	// Journal shows leg A only.
	journalText := readJournal(t, journal)
	if !strings.Contains(journalText, "kind=publication affected=3") {
		t.Fatalf("journal after leg 1 = %q, want the publication line", journalText)
	}
	if strings.Contains(journalText, "kind=delivery") {
		t.Fatalf("journal after leg 1 = %q, must not contain a delivery line", journalText)
	}
	// Invisibility to single-kind checks: publication dry-run prints 0 (looks
	// done), delivery dry-run prints 2.
	out, err := runCapture(t, db, cfg, "-kind", "publication", "-dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "publications to requeue: 0") {
		t.Fatalf("publication dry-run = %q, want 0 (the partial state is invisible here)", out)
	}
	out, err = runCapture(t, db, cfg, "-kind", "delivery", "-dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deliveries to requeue: 2") {
		t.Fatalf("delivery dry-run = %q, want 2", out)
	}

	// Convergence: a full both run completes leg B and re-affects nothing in
	// leg A; the journal gains the delivery line.
	out, err = runCapture(t, db, cfg, "-kind", "both", "-limit", "100", "-actor", "ops-bot", "-report", journal)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if !strings.Contains(out, "requeued 0 publication(s)") || !strings.Contains(out, "requeued 2 delivery(ies)") {
		t.Fatalf("resume output = %q, want 0 publications + 2 deliveries", out)
	}
	journalText = readJournal(t, journal)
	if !strings.Contains(journalText, "kind=publication affected=0") || !strings.Contains(journalText, "kind=delivery affected=2") {
		t.Fatalf("journal after resume = %q, want publication 0 + delivery 2 lines", journalText)
	}
	// Audit accounting: exactly one publication fact and one delivery fact.
	var pubFacts, delFacts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&pubFacts).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'callback.delivery.requeued'`).Scan(&delFacts).Error; err != nil {
		t.Fatal(err)
	}
	if pubFacts != 1 || delFacts != 1 {
		t.Fatalf("facts = pubs %d dels %d, want 1/1 (none double-minted)", pubFacts, delFacts)
	}

	// A second full both run prints 0/0 and mints nothing.
	before := journalText
	out, err = runCapture(t, db, cfg, "-kind", "both", "-limit", "100", "-actor", "ops-bot", "-report", journal)
	if err != nil {
		t.Fatalf("second both run: %v", err)
	}
	if !strings.Contains(out, "requeued 0 publication(s)") || !strings.Contains(out, "requeued 0 delivery(ies)") {
		t.Fatalf("second both output = %q, want 0/0", out)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action IN ('audit.publication.requeued', 'callback.delivery.requeued')`).Scan(&pubFacts).Error; err != nil {
		t.Fatal(err)
	}
	if pubFacts != 2 {
		t.Fatalf("facts after second both run = %d, want 2 (unchanged)", pubFacts)
	}
	journalText = readJournal(t, journal)
	if !strings.Contains(journalText, "kind=delivery affected=0") {
		t.Fatalf("journal after second both run = %q, want the delivery 0 line", journalText)
	}
	if strings.TrimSpace(journalText) == strings.TrimSpace(before) {
		t.Fatalf("journal did not gain the second run's lines:\n%s", journalText)
	}
}

// TestAuditReplayCommitSurvivesMissingReport (T-6 §8.2): the requeue commits
// (row pending, fact durable) and the CLI dies before printing. The durable
// state equals what the report would have claimed, and the operator recovery
// (re-run) is safe and idempotent — the disambiguation queries prove "already
// done" rather than "never happened".
func TestAuditReplayCommitSurvivesMissingReport(t *testing.T) {
	ctx := context.Background()
	db, cfg, publications, _ := startReplayEnv(t)
	seedDeadLetterPublication(t, db, publications, "tenant-a")

	// The requeue commits through the repo; the CLI "dies" before printing.
	intent, err := service.BuildAuditReplayIntent(ctx, "publication", "ops-bot", "", nil, domain.ID{})
	if err != nil {
		t.Fatal(err)
	}
	n, facts, err := publications.RequeuePublication(ctx, nil, 10, intent)
	if err != nil || n != 1 || len(facts) != 1 {
		t.Fatalf("requeue = %d, facts %d, %v; want 1/1/nil", n, len(facts), err)
	}

	// Disambiguation (runbook §5 verify): row-level terminal state + ledger
	// facts in the run window — both non-zero here, proving "already done".
	var pending int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'pending' AND error_code = 'manual_replay'`).Scan(&pending).Error; err != nil {
		t.Fatal(err)
	}
	var ledgerFacts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&ledgerFacts).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 1 || ledgerFacts != 1 {
		t.Fatalf("durable state = pending %d facts %d, want 1/1 (the report would have claimed 1)", pending, ledgerFacts)
	}

	// Operator recovery: re-run through the CLI is idempotent.
	out, err := runCapture(t, db, cfg, "-kind", "publication", "-actor", "ops-bot")
	if err != nil {
		t.Fatalf("recovery run: %v", err)
	}
	if !strings.Contains(out, "requeued 0 publication(s)") {
		t.Fatalf("recovery output = %q, want requeued 0", out)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&ledgerFacts).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerFacts != 1 {
		t.Fatalf("facts after recovery = %d, want 1 (nothing minted)", ledgerFacts)
	}
}

// TestAuditReplayEmptyBacklogPrintsZeroWithoutFact (T-6 §8.3): an empty DLQ
// prints requeued 0 for both kinds, returns success, and mints zero facts —
// indistinguishable on stdout from "crash after full replay"; the ledger /
// manual_replay queries are the only disambiguation (pinned by
// TestAuditReplayCommitSurvivesMissingReport).
func TestAuditReplayEmptyBacklogPrintsZeroWithoutFact(t *testing.T) {
	db, cfg, _, _ := startReplayEnv(t)
	out, err := runCapture(t, db, cfg, "-kind", "both", "-actor", "ops-bot")
	if err != nil {
		t.Fatalf("empty run: %v", err)
	}
	if !strings.Contains(out, "requeued 0 publication(s)") || !strings.Contains(out, "requeued 0 delivery(ies)") {
		t.Fatalf("empty run output = %q, want requeued 0 for both kinds", out)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action IN ('audit.publication.requeued', 'callback.delivery.requeued')`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 0 {
		t.Fatalf("facts after empty run = %d, want 0", facts)
	}
}

// TestAuditReplayPreflightRefusesUnboundTenant (audit 2b): with governance
// enabled and a binding set that excludes the affected rows' tenant, the
// friendly pre-flight refuses the run before any mutation — exit 1, zero
// rows, zero facts.
func TestAuditReplayPreflightRefusesUnboundTenant(t *testing.T) {
	db, cfg, publications, _ := startReplayEnv(t)
	seedDeadLetterPublication(t, db, publications, "tenant-a")
	seedDeadLetterPublication(t, db, publications, "tenant-unbound")
	cfg.AuditGovernance.Enabled = true
	cfg.AuditGovernance.Bindings = map[string]config.AuditGovernanceBindingConfig{
		"tenant-a": {},
	}

	out, err := runCapture(t, db, cfg, "-kind", "publication", "-actor", "ops-bot")
	if err == nil {
		t.Fatalf("run over an unbound tenant must fail; output = %q", out)
	}
	var requeued int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'pending'`).Scan(&requeued).Error; err != nil {
		t.Fatal(err)
	}
	if requeued != 0 {
		t.Fatalf("rows requeued before the pre-flight = %d, want 0", requeued)
	}
}

func readJournal(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	return string(data)
}
