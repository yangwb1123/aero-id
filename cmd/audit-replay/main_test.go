//go:build integration

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/domain"
	dbpkg "github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
)

const auditReplayTestKey = "audit-replay-test-signing-key-0123456789abcdef"

func startReplayEnv(t *testing.T) (*gorm.DB, *config.Config, repository.AuditPublicationRepository, repository.CallbackRepository) {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	cfg := &config.Config{}
	cfg.AuditLedger.SigningKey = auditReplayTestKey
	ledger := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: auditReplayTestKey})
	publications, ok := ledger.(repository.AuditPublicationRepository)
	if !ok {
		t.Fatal("ledger repo must implement AuditPublicationRepository")
	}
	callbacks := repository.NewCallbackRepository(db, ledger)
	return db, cfg, publications, callbacks
}

func seedDeadLetterPublication(t *testing.T, db *gorm.DB, publications repository.AuditPublicationRepository,
	tenantID string) {
	t.Helper()
	ctx := context.Background()
	appendInTx, ok := publications.(repository.AuditLedgerTransactionalRepository)
	if !ok {
		t.Fatal("publication repo must implement AuditLedgerTransactionalRepository")
	}
	fact := &domain.AuditFact{Source: "aero-id", SourceRegion: "local", PartitionKey: tenantID + ":e2e",
		EventID: idgen.NewID(), TenantID: tenantID, ActorUID: "u-1", Action: "account.provisioned",
		ResourceType: "account", ResourceID: "account-1", OccurredAt: time.Now().UTC(), Payload: map[string]any{}}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return appendInTx.AppendInTx(ctx, tx, fact)
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := publications.ClaimPublication(ctx, "seed-worker", 5*time.Minute, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := publications.FinishPublication(ctx, claimed[0].ID, "seed-worker", claimed[0].LeaseGeneration, 1,
		"dead_letter", "audit.publication_failed", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// runCapture executes the replay core with captured stdout (the CLI prints
// the requeued counts; tests assert the exact text). No -report journal is
// used unless a test asks for one.
func runCapture(t *testing.T, gdb *gorm.DB, cfg *config.Config, args ...string) (string, error) {
	t.Helper()
	ids, kind, limit, tenant, actor, dryRun, reportPath, err := parseFlags(args)
	if err != nil {
		return "", err
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := replay(gdb, cfg, ids, kind, limit, tenant, actor, dryRun, reportPath)
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	_ = r.Close()
	return string(out), runErr
}

// parseFlags mirrors run()'s flag parsing so tests can reuse the same surface
// without re-loading the config file or re-opening a database.
func parseFlags(args []string) ([]domain.ID, string, int, string, string, bool, string, error) {
	kind, limit, rawIDs, tenant, actor, dryRun, reportPath := "both", 100, "", "", "", false, ""
	rest := args
	for len(rest) > 0 {
		switch rest[0] {
		case "-kind":
			kind = rest[1]
			rest = rest[2:]
		case "-limit":
			n, err := strconv.Atoi(rest[1])
			if err != nil {
				return nil, "", 0, "", "", false, "", fmt.Errorf("invalid -limit: %w", err)
			}
			limit = n
			rest = rest[2:]
		case "-id":
			rawIDs = rest[1]
			rest = rest[2:]
		case "-tenant":
			tenant = rest[1]
			rest = rest[2:]
		case "-actor":
			actor = rest[1]
			rest = rest[2:]
		case "-report":
			reportPath = rest[1]
			rest = rest[2:]
		case "-dry-run":
			dryRun = true
			rest = rest[1:]
		default:
			return nil, "", 0, "", "", false, "", errors.New("unknown flag " + rest[0])
		}
	}
	ids, err := parseIDs(rawIDs)
	return ids, kind, limit, tenant, actor, dryRun, reportPath, err
}

// TestAuditReplayCLIRunTwiceIsIdempotent (T-4.2): run twice requeues the
// bounded set once and 0 the second time; -id requeues exactly that set;
// -dry-run changes nothing (rows stay dead_letter, no audit facts appended).
func TestAuditReplayCLIRunTwiceIsIdempotent(t *testing.T) {
	db, cfg, publications, _ := startReplayEnv(t)
	for i := 0; i < 3; i++ {
		seedDeadLetterPublication(t, db, publications, "tenant-a")
	}
	var factsBefore int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger`).Scan(&factsBefore).Error; err != nil {
		t.Fatal(err)
	}

	// Dry run first: no mutation, count reported (bounded at -limit).
	out, err := runCapture(t, db, cfg, "-kind", "publication", "-dry-run")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "3 (dry run; capped at -limit 100)") {
		t.Fatalf("dry run output = %q, want bounded count marker", out)
	}
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 3 {
		t.Fatalf("dead-letter rows after dry run = %d, want 3 (unchanged)", dead)
	}
	var factsAfter int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger`).Scan(&factsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if factsAfter != factsBefore {
		t.Fatalf("ledger facts changed by dry run: %d → %d", factsBefore, factsAfter)
	}

	// Bounded run 1 (limit covers the whole set): requeues all 3.
	out, err = runCapture(t, db, cfg, "-kind", "publication", "-limit", "10", "-actor", "ops-run")
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if !strings.Contains(out, "requeued 3 publication(s)") {
		t.Fatalf("run 1 output = %q, want requeued 3", out)
	}
	// Run 2 with identical arguments: idempotent — 0 additional rows (the
	// dead_letter status predicate is the idempotency key, so the bounded set
	// is requeued exactly once).
	out, err = runCapture(t, db, cfg, "-kind", "publication", "-limit", "10", "-actor", "ops-run")
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if !strings.Contains(out, "requeued 0 publication(s)") {
		t.Fatalf("run 2 output = %q, want requeued 0", out)
	}

	// -id requeues exactly the given row: re-drive one row to dead_letter
	// through the public surface, then requeue only that row by id.
	redrive, err := publications.ClaimPublication(context.Background(), "redrive-worker", 5*time.Minute, 1)
	if err != nil || len(redrive) != 1 {
		t.Fatalf("claim for re-drive = %d, %v; want 1, nil", len(redrive), err)
	}
	target := redrive[0].ID
	if err := publications.FinishPublication(context.Background(), target, "redrive-worker", redrive[0].LeaseGeneration, 1,
		"dead_letter", "audit.publication_failed", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	out, err = runCapture(t, db, cfg, "-kind", "publication", "-id", target.String(), "-actor", "ops-run")
	if err != nil {
		t.Fatalf("id run: %v", err)
	}
	if !strings.Contains(out, "requeued 1 publication(s)") {
		t.Fatalf("id run output = %q, want requeued 1", out)
	}
	// The requeue mints one audit.publication.requeued fact per tenant, and
	// each fact's own pending publication row drains to governance — so count
	// only the ORIGINAL account.provisioned rows for the business assertion.
	var pending int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id
		WHERE ap.status = 'pending' AND l.action = 'account.provisioned'`).Scan(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if pending != 3 {
		t.Fatalf("pending business rows = %d, want 3 (all requeued, none double-applied)", pending)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 0 {
		t.Fatalf("dead-letter rows after id run = %d, want 0", dead)
	}

	// Requeued business rows are claimable by the drain worker (the delivery
	// path). The requeue facts' own publication rows are also pending and
	// claimable — filter to the original account.provisioned rows.
	claimed, err := publications.ClaimPublication(context.Background(), "drain-worker", 5*time.Minute, 20)
	if err != nil {
		t.Fatalf("claim after replay: %v", err)
	}
	businessClaims := 0
	for _, item := range claimed {
		if item.Fact != nil && item.Fact.Action == "account.provisioned" {
			businessClaims++
		}
	}
	if businessClaims != 3 {
		t.Fatalf("business claims after replay = %d, want 3", businessClaims)
	}
}

// TestAuditReplayCLIRequeueFactWithTenant (AUD-1): the requeue appends one
// audit fact per affected tenant in the same transaction; -tenant is a
// SELECTION FILTER (the fact tenant derives from the rows), -actor is the
// operator identity on the fact's actor_uid column, and the payload is the
// allow-listed five-key construction (no operator input inside).
func TestAuditReplayCLIRequeueFactWithTenant(t *testing.T) {
	db, cfg, publications, _ := startReplayEnv(t)
	seedDeadLetterPublication(t, db, publications, "tenant-a")

	out, err := runCapture(t, db, cfg, "-kind", "publication", "-limit", "10",
		"-tenant", "tenant-a", "-actor", "ops-bot")
	if err != nil {
		t.Fatalf("tenant run: %v", err)
	}
	if !strings.Contains(out, "requeued 1 publication(s)") {
		t.Fatalf("output = %q, want requeued 1", out)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued' AND tenant_id = 'tenant-a'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("requeue facts = %d, want 1", facts)
	}
	// The actor lands on the envelope column, never in the payload; the
	// payload is the construction-only five-key allow-list.
	var actorUID string
	if err := db.Raw(`SELECT actor_uid FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&actorUID).Error; err != nil {
		t.Fatal(err)
	}
	if actorUID != "ops-bot" {
		t.Fatalf("fact actor_uid = %q, want ops-bot", actorUID)
	}
	var payload string
	if err := db.Raw(`SELECT payload::text FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&payload).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "ops-bot") {
		t.Fatalf("fact payload = %q, must not carry operator input", payload)
	}
	for _, key := range []string{"\"count\": 1", "\"publication_ids\"", "\"scope\"", "\"prior_error_code\"", "\"prior_attempt\""} {
		if !strings.Contains(payload, key) {
			t.Fatalf("fact payload = %q, want allow-listed key %s", payload, key)
		}
	}
	var opIDCount int64
	if err := db.Raw(`SELECT count(DISTINCT operation_id) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&opIDCount).Error; err != nil {
		t.Fatal(err)
	}
	if opIDCount != 1 {
		t.Fatalf("distinct operation ids = %d, want 1 (shared per run)", opIDCount)
	}
}

// replayConfigYAML is a minimal config that passes config.Load validation
// (mirrors internal/config/config_test.go); tests override database host/port
// and audit_ledger.signing_key per case.
func replayConfigYAML(host string, port int, signingKey string) string {
	return fmt.Sprintf(`
database:
  host: %s
  port: %d
  user: aero
  password: aero
  dbname: aero_id
  sslmode: disable
  max_open_conns: 10
  max_idle_conns: 2
  conn_max_lifetime: 30m
  log_level: warn
sso: {endpoint: "sso:9000", timeout: 5s, insecure: true, token_cache_ttl: 60s, user_cache_ttl: 300s}
storage:
  type: local
  local: {root: /tmp/files, public_base_url: "http://x"}
audit_ledger: {signing_key: %s}
signing_key: testkey
log: {level: info, encoding: json}
`, host, port, strconv.Quote(signingKey))
}

func writeReplayConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestAuditReplaySigningKeyPreFlight (F7): the tool refuses to run before any
// mutation when the ledger signing key is missing or too short — the config
// layer enforces the >= 32 byte rule (validateAuditLedger) at load time, and
// the replay core fails closed with zero mutation even when a hand-built
// config bypasses load-time validation.
func TestAuditReplaySigningKeyPreFlight(t *testing.T) {
	// Config-layer rejections fire before the DB is ever reached: point the
	// DSN at a dead endpoint — the error must be the key error, not a
	// connection error.
	deadHost := "127.0.0.1"
	for name, tc := range map[string]struct {
		key     string
		wantErr string
	}{
		"missing key":    {"", "audit_ledger.signing_key is required"},
		"whitespace key": {"   \t  ", "audit_ledger.signing_key is required"},
		"short key":      {strings.Repeat("a", 31), "audit_ledger.signing_key must be at least 32 bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			cfgPath := writeReplayConfig(t, replayConfigYAML(deadHost, 1, tc.key))
			err := run([]string{"-config", cfgPath, "-actor", "ops-bot"})
			if err == nil {
				t.Fatal("run() with an invalid signing key must fail")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("run() error = %v, want mention of %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "open db") || strings.Contains(err.Error(), "connection") {
				t.Fatalf("run() touched the DB before rejecting the key: %v", err)
			}
		})
	}

	// Replay core with a hand-built empty-key config (bypasses config.Load):
	// AppendInTx fails in-tx, the reset rolls back, zero mutation.
	db, cfg, publications, _ := startReplayEnv(t)
	seedDeadLetterPublication(t, db, publications, "tenant-a")
	cfg.AuditLedger.SigningKey = ""
	if err := replay(db, cfg, nil, "publication", 10, "tenant-a", "ops-bot", false, ""); err == nil {
		t.Fatal("replay with an empty signing key must fail")
	}
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 1 {
		t.Fatalf("dead-letter rows after failed replay = %d, want 1 (zero mutation)", dead)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 0 {
		t.Fatalf("requeue facts after failed replay = %d, want 0", facts)
	}
}

// TestAuditReplayDBOutageFailsClosed (F6): with an unreachable or closed
// database the tool errors out (run() propagates → main exits non-zero) and
// mutates nothing — dead-letter rows and the ledger stay byte-identical.
func TestAuditReplayDBOutageFailsClosed(t *testing.T) {
	// Leg 1 — run() against an unreachable endpoint: error, no partial work.
	cfgPath := writeReplayConfig(t, replayConfigYAML("127.0.0.1", 1, auditReplayTestKey))
	if err := run([]string{"-config", cfgPath, "-actor", "ops-bot"}); err == nil {
		t.Fatal("run() against an unreachable database must fail (main exits non-zero)")
	}

	// Leg 2 — replay core against a real database whose pool is closed: the
	// first query fails, nothing is requeued, and the live handle proves zero
	// mutation.
	db, cfg, publications, _ := startReplayEnv(t)
	seedDeadLetterPublication(t, db, publications, "tenant-a")
	seedDeadLetterPublication(t, db, publications, "tenant-a")

	dialector, ok := db.Dialector.(*postgres.Dialector)
	if !ok || dialector.DSN == "" {
		t.Fatal("testpg handle must carry its DSN for the outage leg")
	}
	outageDB, err := dbpkg.Open(dbpkg.Options{DSN: dialector.DSN, MaxOpenConns: 1, LogLevel: "silent"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := outageDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	if err := replay(outageDB, cfg, nil, "publication", 10, "tenant-a", "ops-bot", false, ""); err == nil {
		t.Fatal("replay against a closed database must fail")
	}
	var dead int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status = 'dead_letter'`).Scan(&dead).Error; err != nil {
		t.Fatal(err)
	}
	if dead != 2 {
		t.Fatalf("dead-letter rows after outage = %d, want 2 (zero mutation)", dead)
	}
	var facts int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE action = 'audit.publication.requeued'`).Scan(&facts).Error; err != nil {
		t.Fatal(err)
	}
	if facts != 0 {
		t.Fatalf("requeue facts after outage = %d, want 0", facts)
	}
}

// TestAuditReplayGovernanceActivationGatePreFlight (T-10.2, L10.2.4): the
// replay CLI must inherit the Load-level activation gate — an
// enabled+zero-bindings governance section exits before the DB is ever
// reached, with the byte-identical documented error. The Load gate is the
// binary-agnostic enforcement point for every root that can mint durable
// facts (cmd/server, cmd/audit-replay, cmd/migrate); the composition-root
// re-check in internal/app is the server-root second line only, and the CLI
// must NOT gain an internal/app dependency. Mirrors
// TestAuditReplaySigningKeyPreFlight: dead DSN proves the refusal is
// config-layer, not a connection error.
func TestAuditReplayGovernanceActivationGatePreFlight(t *testing.T) {
	const wantErr = "audit_governance.enabled requires at least one tenant binding (activation gate)"
	deadHost := "127.0.0.1"
	cfgPath := writeReplayConfig(t,
		replayConfigYAML(deadHost, 1, auditReplayTestKey)+"\naudit_governance: {enabled: true}\n")
	err := run([]string{"-config", cfgPath, "-dry-run"})
	if err == nil {
		t.Fatal("run() with enabled+zero-bindings governance must fail at Load")
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("run() error = %v, want containing %q", err, wantErr)
	}
	if strings.Contains(err.Error(), "open db") || strings.Contains(err.Error(), "connection") {
		t.Fatalf("run() touched the DB before rejecting the gate: %v", err)
	}
}
