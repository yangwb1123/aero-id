//go:build integration

package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/repository/testpg"
	"github.com/aero/aero-id/internal/service"
)

func TestAuditBindingCoverageIncludesAccountsAndDurableBacklog(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledgerRepo := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})
	ledger := service.NewAuditLedgerService(repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "app-integration-audit-ledger-key-0123456789"}))
	if err := ledger.Record(t.Context(), service.AuditFactInput{
		TenantID: "tenant-publication", Action: "account.created", ActorUID: "actor-one",
	}); err != nil {
		t.Fatal(err)
	}
	accounts := repository.NewAccountRepository(db, ledgerRepo)
	if _, err := accounts.EnsureByIdentity(
		t.Context(), "tenant-account", "snaplink", "subject-one", "actor-two", "local",
	); err != nil {
		t.Fatal(err)
	}

	bindings := map[string]config.AuditGovernanceBindingConfig{
		"tenant-publication": {},
	}
	if err := validateAuditBindingCoverage(db, bindings); err == nil {
		t.Fatal("account tenant without an audit binding passed startup coverage")
	}
	bindings["tenant-account"] = config.AuditGovernanceBindingConfig{}
	if err := validateAuditBindingCoverage(db, bindings); err != nil {
		t.Fatalf("complete durable tenant coverage failed: %v", err)
	}
}

// TestAuditBindingCoverageSnapshotUncommittedRowsInvisible (REQ-3.1/3.3,
// T-10.2a): the coverage gate is a single-statement UNION snapshot — an
// in-flight uncommitted unbound write is outside the boot decision (the gate
// passes while the writer is blocked, and a plain SELECT proves MVCC
// invisibility), and the same row fails the gate once committed.
func TestAuditBindingCoverageSnapshotUncommittedRowsInvisible(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	empty := map[string]config.AuditGovernanceBindingConfig{}

	// Unbound leg: committed unbound rows always fail the boot; in-flight
	// uncommitted writes are outside the boot decision (not a partial boot).
	unlock, done := startBlockedAuditWriter(t, db, sqlDB, "gate-p-1", "race-unbound")
	if err := validateAuditBindingCoverage(db, empty); err != nil {
		t.Fatalf("gate failed on an uncommitted unbound row: %v", err)
	}
	var visible int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE tenant_id = 'race-unbound'`).Scan(&visible).Error; err != nil {
		t.Fatal(err)
	}
	if visible != 0 {
		t.Fatalf("uncommitted row visible to a plain SELECT: %d rows", visible)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("writer: %v", err)
	}
	if err := validateAuditBindingCoverage(db, empty); err == nil {
		t.Fatal("committed unbound row passed the coverage gate")
	}

	// Covered leg: a committed row with a matching binding never fails.
	unlock, done = startBlockedAuditWriter(t, db, sqlDB, "gate-p-2", "race-covered")
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("writer: %v", err)
	}
	covered := map[string]config.AuditGovernanceBindingConfig{"race-unbound": {}, "race-covered": {}}
	if err := validateAuditBindingCoverage(db, covered); err != nil {
		t.Fatalf("covered committed row failed the gate: %v", err)
	}
}

// startBlockedAuditWriter runs the T-10.2a handshake: a coordinator session
// conn takes pg_advisory_lock(hashtext('coverage-gate-writer')) before the
// writer spawns; the writer goroutine BEGINs, inserts one audit_ledger row and
// one pending audit_publications row, then blocks on the transaction-scoped
// pg_advisory_xact_lock with the same key (auto-released at COMMIT — a session
// lock taken inside a transaction would survive COMMIT and leak). The test
// fails closed if the writer never blocks (waitForLockPoll times out). Returns
// the coordinator unlock function and the writer's completion channel.
func startBlockedAuditWriter(t *testing.T, db *gorm.DB, sqlDB *sql.DB, partition, tenant string) (func(), <-chan error) {
	t.Helper()
	ctx := context.Background()
	coordinator, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('coverage-gate-writer'))`); err != nil {
		t.Fatal(err)
	}
	writer, err := sqlDB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		defer writer.Close()
		errCh <- writerRun(ctx, writer, partition, tenant)
	}()
	waitForLockPoll(t, db, "advisory", 10*time.Second)
	return func() {
		if _, err := coordinator.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext('coverage-gate-writer'))`); err != nil {
			t.Fatal(err)
		}
		_ = coordinator.Close()
	}, errCh
}

// writerRun is the blocked-writer transaction body: ledger row + pending
// publication row, then the xact lock that only the coordinator can release.
func writerRun(ctx context.Context, writer *sql.Conn, partition, tenant string) error {
	if _, err := writer.ExecContext(ctx, "BEGIN"); err != nil {
		return err
	}
	factID := idgen.NewID()
	if _, err := writer.ExecContext(ctx, `
		INSERT INTO audit_ledger (id, hash_version, chain_sequence, source, source_region, partition_key,
			event_id, tenant_id, action, occurred_at, payload, payload_hash, previous_hash, record_hash, created_at)
		VALUES ($1, 2, 1, 'aero-id', 'local', $2, $3, $4, 'test.gate', now(), '{}'::jsonb, 'ph', '', 'rh', now())`,
		factID, partition, idgen.NewID(), tenant); err != nil {
		return err
	}
	if _, err := writer.ExecContext(ctx, `
		INSERT INTO audit_publications (id, fact_id, status, attempt, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, 'pending', 0, now(), now(), now())`, idgen.NewID(), factID); err != nil {
		return err
	}
	if _, err := writer.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('coverage-gate-writer'))`); err != nil {
		return err
	}
	if _, err := writer.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	return nil
}

// waitForLockPoll polls pg_stat_activity until at least one backend waits on
// the given lock event (advisory / transactionid), or fails the test. Copied
// from internal/repository/audit_consolidation_integration_test.go:18-27.
func waitForLockPoll(t *testing.T, db *gorm.DB, waitEvent string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var n int64
		if err := db.Raw(`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND wait_event = ?`, waitEvent).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for lock event %q", waitEvent)
}

// TestAuditBindingCoverageConcurrentProvisioningStress (REQ-3.2/3.3, T-10.2b):
// bounded stress over concurrent provisioning (Record + EnsureByIdentity with
// distinct (issuer, subject) per writer) racing concurrent gate calls. Tail
// assertions only — no mid-flight state assertions: no fact loss, the gate is
// read-only (publications untouched), the tail gate call with empty bindings
// deterministically fails, and full coverage never fails.
func TestAuditBindingCoverageConcurrentProvisioningStress(t *testing.T) {
	ctx := context.Background()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledgerRepo := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "app-integration-audit-ledger-key-0123456789"})
	ledger := service.NewAuditLedgerService(ledgerRepo)
	accounts := repository.NewAccountRepository(db, ledgerRepo)

	start := make(chan struct{})
	errs := make(chan error, 8)
	eventIDs := make([]domain.ID, 4)
	for i := range 4 {
		go func() {
			<-start
			eventIDs[i] = idgen.NewID()
			errs <- ledger.Record(ctx, service.AuditFactInput{TenantID: fmt.Sprintf("race-%d", i),
				EventID: eventIDs[i], ActorUID: "actor-stress", Action: "account.created"})
		}()
	}
	for i := 4; i < 8; i++ {
		go func() {
			<-start
			_, err := accounts.EnsureByIdentity(ctx, fmt.Sprintf("race-%d", i), "race-issuer",
				fmt.Sprintf("race-%d", i), fmt.Sprintf("canonical-%d", i), "local")
			errs <- err
		}()
	}
	var gates sync.WaitGroup
	for range 4 {
		gates.Add(1)
		go func() {
			defer gates.Done()
			for range 20 {
				_ = validateAuditBindingCoverage(db, map[string]config.AuditGovernanceBindingConfig{})
			}
		}()
	}
	close(start)
	for range 8 {
		if err := <-errs; err != nil {
			t.Fatalf("provisioning writer: %v", err)
		}
	}
	gates.Wait()
	assertCoverageStressTail(t, db, eventIDs)
}

// assertCoverageStressTail is the deterministic post-join assertion set for
// the T-10.2b stress run.
func assertCoverageStressTail(t *testing.T, db *gorm.DB, eventIDs []domain.ID) {
	t.Helper()
	for i, eventID := range eventIDs {
		var rows int64
		if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE tenant_id = ? AND event_id = ?`,
			fmt.Sprintf("race-%d", i), eventID).Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("fact for race-%d = %d rows, want exactly 1 (no loss)", i, rows)
		}
	}
	var publications, touched int64
	if err := db.Raw(`SELECT count(*) FROM audit_publications`).Scan(&publications).Error; err != nil {
		t.Fatal(err)
	}
	if publications != 4 {
		t.Fatalf("publication rows = %d, want 4", publications)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications WHERE status <> 'pending' OR attempt <> 0 OR lease_owner <> ''`).Scan(&touched).Error; err != nil {
		t.Fatal(err)
	}
	if touched != 0 {
		t.Fatalf("gate touched %d publication rows (read-only violation)", touched)
	}
	if err := validateAuditBindingCoverage(db, map[string]config.AuditGovernanceBindingConfig{}); err == nil {
		t.Fatal("tail gate call with empty bindings passed")
	}
	bindings := make(map[string]config.AuditGovernanceBindingConfig, 8)
	for i := range 8 {
		bindings[fmt.Sprintf("race-%d", i)] = config.AuditGovernanceBindingConfig{}
	}
	for range 20 {
		if err := validateAuditBindingCoverage(db, bindings); err != nil {
			t.Fatalf("full-coverage gate call failed: %v", err)
		}
	}
}
func TestAuditLedgerRecordEnqueuesOnlyIntegration(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	ledgerRepo := repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{SigningKey: "app-integration-audit-ledger-key-0123456789"})
	ledger := service.NewAuditLedgerService(ledgerRepo)

	eventID := idgen.NewID()
	if err := ledger.Record(t.Context(), service.AuditFactInput{
		TenantID: "tenant-one", EventID: eventID, ActorUID: "actor-one", Action: "account.viewed",
	}); err != nil {
		t.Fatal(err)
	}
	var ledgerRows, publicationRows int64
	if err := db.Raw(`SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID).Scan(&ledgerRows).Error; err != nil {
		t.Fatal(err)
	}
	if ledgerRows != 1 {
		t.Fatalf("ledger rows = %d, want 1", ledgerRows)
	}
	if err := db.Raw(`SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id
		WHERE l.event_id = ? AND ap.status = 'pending'`, eventID).Scan(&publicationRows).Error; err != nil {
		t.Fatal(err)
	}
	if publicationRows != 1 {
		t.Fatalf("pending publication rows = %d, want 1 (enqueue-only)", publicationRows)
	}
}

// TestBuildAuditStackWiresControlMetricsIntoCallbackDrain (F-1, §5 runbook):
// the composition root must attach the control-plane registry to the callback
// service, not only the ledger service. Without that wiring the drain worker's
// publication observability falls back to the no-op sink and none of the
// `aero_control_audit_publication_*` series ever appears on /metrics — every
// §5 runbook alert written from those names would match nothing. Driving the
// real stack (Record → drain tick → committed FinishPublication) must render
// `aero_control_audit_publication_outcomes_total{status="completed"} 1`.
func TestBuildAuditStackWiresControlMetricsIntoCallbackDrain(t *testing.T) {
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))

	reg := metrics.NewRegistry()
	callbackSvc, auditLedger, _, err := buildAuditStack(&config.Config{
		AuditLedger: config.AuditLedgerConfig{SigningKey: "app-integration-audit-ledger-key-0123456789"},
		Callback:    config.CallbackConfig{MasterKey: "app-integration-callback-master-key", WorkerConcurrency: 2},
	}, db, service.NewRegistryControlPlaneMetrics(reg))
	if err != nil {
		t.Fatal(err)
	}
	if err := auditLedger.Record(t.Context(), service.AuditFactInput{
		TenantID: "tenant-one", ActorUID: "actor-one", Action: "account.viewed",
	}); err != nil {
		t.Fatal(err)
	}

	stop := callbackSvc.Start(t.Context(), 50*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var status string
		if err := db.Raw(`SELECT status FROM audit_publications LIMIT 1`).Scan(&status).Error; err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("publication did not settle to completed within 15s (status=%q)", status)
		}
		time.Sleep(50 * time.Millisecond)
	}

	var buf strings.Builder
	reg.WriteText(&buf)
	out := buf.String()
	for _, want := range []string{
		`aero_control_audit_publication_outcomes_total{status="completed"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics exposition missing %q — control metrics not wired into the callback drain:\n%s", want, out)
		}
	}
}

// governanceTestEndpoint is the httptest governance fake (E11 precedent,
// connector/auditgovernance/publisher_test.go): /token client-credentials
// form → bearer JSON; /api/v1/events → 202 + receipt
// {event_id, tenant_id, status:"ledgered", accepted_at} — a duplicate
// event_id answers with a conflict receipt (the endpoint's dedup
// acknowledgement, which the connector treats as idempotent success). POSTs
// are counted under a mutex so the e2e assertions can pin exactly-once
// delivery; only /api/v1/events POSTs are counted (the token fetch is lazy
// and never appears in the counts).
type governanceTestEndpoint struct {
	url       string
	mu        sync.Mutex
	bodies    []map[string]any
	accepted  map[string]bool
	conflicts int
}

func newGovernanceTestEndpoint(t *testing.T) *governanceTestEndpoint {
	t.Helper()
	e := &governanceTestEndpoint{accepted: map[string]bool{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"service-token","token_type":"Bearer","expires_in":300}`))
		case "/api/v1/events":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode governance event: %v", err)
			}
			e.mu.Lock()
			e.bodies = append(e.bodies, body)
			eventID, _ := body["event_id"].(string)
			conflict := e.accepted[eventID]
			if !conflict {
				e.accepted[eventID] = true
			} else {
				e.conflicts++
			}
			e.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"receipt": map[string]any{
				"event_id": eventID, "tenant_id": "tenant-one", "status": "ledgered",
				"accepted_at": time.Now().UTC(), "conflict": conflict,
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	e.url = server.URL
	return e
}

func (e *governanceTestEndpoint) postCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.bodies)
}

func (e *governanceTestEndpoint) conflictCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.conflicts
}

func (e *governanceTestEndpoint) recordedBodies() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]any(nil), e.bodies...)
}

// auditGovernanceTestConfig is a Load()-valid governance config shape (the
// knob matrix + env legs pin the gate side): the tenant-one binding carries
// the exact scope/resources, the secret env is set by the fixture, and
// AllowInsecureLoopback matches the connector's secureEndpoint so the
// httptest loopback server is accepted. MaxAttempts 8 keeps the fixture away
// from the dead-letter edge (out of T-10.2e/f scope; the failing-publisher
// tests pin that classification).
func auditGovernanceTestConfig(endpoint string, purgePause bool, retention time.Duration) *config.Config {
	return &config.Config{
		AuditLedger: config.AuditLedgerConfig{SigningKey: "app-integration-audit-ledger-key-0123456789"},
		Callback: config.CallbackConfig{
			MasterKey: "app-integration-callback-master-key", RequestTimeout: 2 * time.Second,
			MaxResponseBytes: 4096, MaxAttempts: 8, RetryBase: time.Second, WorkerConcurrency: 2,
		},
		AuditGovernance: config.AuditGovernanceConfig{
			Enabled: true, Endpoint: endpoint, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
			PurgePause: purgePause, DeadLetterRetention: retention,
			Bindings: map[string]config.AuditGovernanceBindingConfig{
				"tenant-one": {TokenURL: endpoint + "/token", ClientID: "audit-client",
					ClientSecretEnv: "TEST_AUDIT_SECRET", Scope: "audit:event:write",
					Resources: []string{"audit-governance"}},
			},
		},
	}
}

// auditGovernanceFixture drives the real composition root: testpg Postgres +
// the httptest governance fake + buildAuditStack (the same constructor the
// server uses). The coverage gate (validateAuditBindingCoverage) passes on
// the fresh empty container before any Record, so the fixture order is
// buildAuditStack → Record → Start (matching TestBuildAuditStackWires
// ControlMetricsIntoCallbackDrain); the ticker is started by the test so
// zero-POSTs-before-drain assertions are possible.
type auditGovernanceFixture struct {
	db          *gorm.DB
	endpoint    *governanceTestEndpoint
	callbackSvc service.CallbackService
	auditLedger service.AuditLedgerService
}

func newAuditGovernanceFixture(t *testing.T, purgePause bool, retention time.Duration) *auditGovernanceFixture {
	t.Helper()
	db := testpg.Start(t)
	testpg.Migrate(t, db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	t.Setenv("TEST_AUDIT_SECRET", "audit-secret-value-000000000000000001")
	fixture := &auditGovernanceFixture{db: db, endpoint: newGovernanceTestEndpoint(t)}
	fixture.buildStack(t, purgePause, retention)
	return fixture
}

// buildStack constructs a fresh audit stack from a fresh config on the
// fixture DB (the B2-5 knobs are static per boot — a second stack exercises
// the composition-root copy at app/audit.go, never cfg mutation).
func (f *auditGovernanceFixture) buildStack(t *testing.T, purgePause bool, retention time.Duration) {
	t.Helper()
	cfg := auditGovernanceTestConfig(f.endpoint.url, purgePause, retention)
	callbackSvc, auditLedger, _, err := buildAuditStack(cfg, f.db, service.NewRegistryControlPlaneMetrics(metrics.NewRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	f.callbackSvc = callbackSvc
	f.auditLedger = auditLedger
}

// pollPublication deadline-polls until the publication row for the fact with
// the given event_id reaches the wanted status (the repo's deadline-poll
// pattern: short bounded sleeps, hard deadline, no unconditional sleeps).
func pollPublication(t *testing.T, db *gorm.DB, eventID domain.ID, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var status string
		if err := db.Raw(`SELECT ap.status FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id
			WHERE l.event_id = ?`, eventID).Scan(&status).Error; err != nil {
			t.Fatal(err)
		}
		if status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("publication for event %s = %q, want %q within %s", eventID, status, want, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ledgerRowID resolves the ledger row id for the caller-known event_id — the
// body pin target of T-10.2e (the known EventID lives only in the dedup
// column and is never carried in the governance payload).
func ledgerRowID(t *testing.T, db *gorm.DB, eventID domain.ID) domain.ID {
	t.Helper()
	var raw string
	if err := db.Raw(`SELECT id::text FROM audit_ledger WHERE event_id = ?`, eventID).Scan(&raw).Error; err != nil {
		t.Fatal(err)
	}
	id, err := idgen.Parse(raw)
	if err != nil {
		t.Fatalf("parse ledger row id %q: %v", raw, err)
	}
	return id
}

func appCountRows(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// TestFirstEventGovernanceE2E (REQ-2, T-10.2e + T-10.2f): the complete
// first-event chain through the real composition root — Record → pending →
// drain ticker → OAuth client-credentials → 202 + receipt → completed — and
// the crash-after-remote-success replay leg. Phase 1 pins exactly 1 ledger
// row + 1 pending row, zero events POSTs before Start (Record never
// publishes inline; the token fetch is lazy), exactly one 202 POST, and the
// body pin target: event_id == idempotency_key == the ledger ROW id (minted
// by buildAuditFact and emitted by publisher.event() as fact.ID.String();
// the caller-known EventID is the dedup key in the event_id column and is
// never what the body carries). Phase 2 resets the row to status='delivering'
// with a backdated DB-clock lease (after phase 1 the row is completed, so
// backdating the lease alone would match nothing), the next tick re-claims
// and re-delivers, the endpoint answers with a conflict receipt (idempotent
// success), and the row settles completed — never dead_letter — with the
// ledger still exactly 1 row (no re-append).
func TestFirstEventGovernanceE2E(t *testing.T) {
	fixture := newAuditGovernanceFixture(t, false, 0)
	eventID := idgen.NewID()
	if err := fixture.auditLedger.Record(t.Context(), service.AuditFactInput{
		TenantID: "tenant-one", EventID: eventID, ActorUID: "actor-one", Action: "account.viewed",
	}); err != nil {
		t.Fatal(err)
	}

	// Phase 1: enqueue-only, exactly one ledger row + one pending row.
	if n := appCountRows(t, fixture.db, `SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID); n != 1 {
		t.Fatalf("ledger rows = %d, want 1", n)
	}
	if n := appCountRows(t, fixture.db, `SELECT count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id
		WHERE l.event_id = ? AND ap.status = 'pending' AND ap.attempt = 0 AND ap.lease_owner = ''`, eventID); n != 1 {
		t.Fatalf("pending publication rows = %d, want 1", n)
	}
	if got := fixture.endpoint.postCount(); got != 0 {
		t.Fatalf("events POSTs before Start = %d, want 0 (drain-only delivery)", got)
	}

	stop := fixture.callbackSvc.Start(t.Context(), 50*time.Millisecond)
	defer stop()
	pollPublication(t, fixture.db, eventID, "completed", 15*time.Second)

	rowID := ledgerRowID(t, fixture.db, eventID)
	if got := fixture.endpoint.postCount(); got != 1 {
		t.Fatalf("events POSTs after first delivery = %d, want exactly 1", got)
	}
	body := fixture.endpoint.recordedBodies()[0]
	if body["event_id"] != rowID.String() || body["idempotency_key"] != rowID.String() {
		t.Fatalf("body event_id/idempotency_key = %v/%v, want %s (ledger row id, not the known EventID)",
			body["event_id"], body["idempotency_key"], rowID)
	}
	if body["tenant_id"] != nil {
		t.Error("tenant_id must not be an event-body authorization input")
	}
	var row struct {
		Status    string
		ErrorCode string
	}
	if err := fixture.db.Raw(`SELECT ap.status, ap.error_code FROM audit_publications ap
		JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Status != "completed" || row.ErrorCode != "" {
		t.Fatalf("publication = %s/%s, want completed/''", row.Status, row.ErrorCode)
	}

	// Phase 2: crash-after-remote-success replay. The explicit status reset
	// plus the DB-clock lease backdate make the row re-claimable; the conflict
	// receipt settles it completed (idempotent success), never dead_letter.
	// attempt becomes 2 on re-settlement; only never-dead_letter is asserted.
	if err := fixture.db.Exec(`UPDATE audit_publications SET status = 'delivering', lease_until = now() - interval '1 minute'
		FROM audit_ledger l WHERE audit_publications.fact_id = l.id AND l.event_id = ?`, eventID).Error; err != nil {
		t.Fatal(err)
	}
	pollPublication(t, fixture.db, eventID, "completed", 15*time.Second)
	if got := fixture.endpoint.conflictCount(); got != 1 {
		t.Fatalf("conflict replies = %d, want exactly 1", got)
	}
	if got := fixture.endpoint.postCount(); got != 2 {
		t.Fatalf("events POSTs after replay = %d, want exactly 2 (one per delivery)", got)
	}
	if n := appCountRows(t, fixture.db, `SELECT count(*) FROM audit_ledger WHERE event_id = ?`, eventID); n != 1 {
		t.Fatalf("ledger rows after replay = %d, want 1 (no re-append)", n)
	}
	var status string
	if err := fixture.db.Raw(`SELECT ap.status FROM audit_publications ap
		JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("publication after conflict replay = %q, want completed (never dead_letter)", status)
	}
}

// TestFirstEventGovernanceE2EConcurrentBatch (REQ-3.2, T-4.2): N first facts
// through the real drain at the composition root with WorkerConcurrency 2.
// Every row settles completed with attempt 1; per-event_id exactly one
// accepted POST and total POSTs == N — a double delivery would either exceed
// N or draw a conflict reply (with distinct event_ids, a re-claim is a
// second accepted POST, caught by the per-event count). With one service
// instance there is exactly one claimer per tick, so claim overlap stays
// T-4.1's job; this test pins the fan-out settlement at the composition root.
func TestFirstEventGovernanceE2EConcurrentBatch(t *testing.T) {
	fixture := newAuditGovernanceFixture(t, false, 0)
	const facts = 6
	for i := 0; i < facts; i++ {
		if err := fixture.auditLedger.Record(t.Context(), service.AuditFactInput{
			TenantID: "tenant-one", EventID: idgen.NewID(), ActorUID: "actor-one",
			Action: "account.viewed", Payload: map[string]any{"n": i},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := fixture.endpoint.postCount(); got != 0 {
		t.Fatalf("events POSTs before Start = %d, want 0", got)
	}
	stop := fixture.callbackSvc.Start(t.Context(), 50*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var remaining int64
		if err := fixture.db.Raw(`SELECT count(*) FROM audit_publications WHERE status <> 'completed'`).Scan(&remaining).Error; err != nil {
			t.Fatal(err)
		}
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("publications did not settle within 15s (%d remaining)", remaining)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := appCountRows(t, fixture.db, `SELECT count(*) FROM audit_publications WHERE attempt <> 1`); n != 0 {
		t.Fatalf("publications with attempt <> 1 = %d, want 0", n)
	}
	if got := fixture.endpoint.postCount(); got != facts {
		t.Fatalf("events POSTs = %d, want %d (one per event_id)", got, facts)
	}
	perEvent := map[string]int{}
	for _, body := range fixture.endpoint.recordedBodies() {
		eventID, _ := body["event_id"].(string)
		perEvent[eventID]++
	}
	for id, n := range perEvent {
		if n != 1 {
			t.Fatalf("event %s delivered %d times, want exactly 1 (per-event_id exactly once)", id, n)
		}
	}
}

// deadLetterRowSnapshot is the bit-for-bit preservation contract of the
// paused purge leg: the drain loop must not SELECT-or-DELETE a dead-letter
// row while purge_pause is set, so id/status/attempt/next_attempt_at/
// updated_at are unchanged after ticks ran.
type deadLetterRowSnapshot struct {
	id            domain.ID
	status        string
	attempt       int
	leaseOwner    string
	nextAttemptAt time.Time
	updatedAt     time.Time
}

func deadLetterSnapshot(t *testing.T, db *gorm.DB, eventID domain.ID) deadLetterRowSnapshot {
	t.Helper()
	var row struct {
		ID            domain.ID
		Status        string
		Attempt       int
		LeaseOwner    string
		NextAttemptAt time.Time
		UpdatedAt     time.Time
	}
	if err := db.Raw(`SELECT ap.id, ap.status, ap.attempt, ap.lease_owner, ap.next_attempt_at, ap.updated_at
		FROM audit_publications ap JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	return deadLetterRowSnapshot{id: row.ID, status: row.Status, attempt: row.Attempt,
		leaseOwner: row.LeaseOwner, nextAttemptAt: row.NextAttemptAt, updatedAt: row.UpdatedAt}
}

// TestBuildAuditStackHonorsPurgeKnobs (REQ-1.2, T-10.1.8): the composition
// root copies PurgePause/DeadLetterRetention from config into the callback
// service (app/audit.go:52-53) and the drain ticker's purge scan honors them.
// Leg 1 (paused): a past-retention dead-letter row (updated_at backdated via
// SQL — the purge predicate is updated_at < cutoff) is preserved bit-for-bit
// while a pending fact's completion proves at least one tick ran (the purge
// metric stays 0 while paused and is unusable as a signal; no sleeps).
// Leg 2 (unpaused boundary value): a SECOND buildAuditStack + Start from a
// PurgePause:false config on the same fixture DB — never cfg mutation, which
// would bypass the composition-root copy — purges exactly the past-retention
// row and keeps the inside-retention row and the ledger fact (the boundary
// value catches a hardcoded-positive retention copy that both single-row
// legs would green-light). The leg-2 coverage gate re-runs against durable
// state (the dead-letter row joins the UNION) and passes only because the
// leg-2 config keeps the tenant-one binding — a binding regression fails
// loudly at buildStack, never silently.
func TestBuildAuditStackHonorsPurgeKnobs(t *testing.T) {
	fixture := newAuditGovernanceFixture(t, true, time.Hour)
	progressID := idgen.NewID()
	if err := fixture.auditLedger.Record(t.Context(), service.AuditFactInput{
		TenantID: "tenant-one", EventID: progressID, ActorUID: "actor-one", Action: "account.viewed",
	}); err != nil {
		t.Fatal(err)
	}
	pastID := idgen.NewID()
	if err := fixture.auditLedger.Record(t.Context(), service.AuditFactInput{
		TenantID: "tenant-one", EventID: pastID, ActorUID: "actor-one", Action: "account.viewed",
	}); err != nil {
		t.Fatal(err)
	}
	// Past-retention dead-letter row: backdate updated_at AFTER the append so
	// the drain cannot pick it up as pending (the append writes now()).
	if err := fixture.db.Exec(`UPDATE audit_publications SET status = 'dead_letter',
		updated_at = now() - interval '10 days'
		FROM audit_ledger l WHERE audit_publications.fact_id = l.id AND l.event_id = ?`, pastID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := deadLetterSnapshot(t, fixture.db, pastID)

	stop := fixture.callbackSvc.Start(t.Context(), 50*time.Millisecond)
	// The pending row's completion proves the ticker ran; the purge scan runs
	// at the end of the same tick iteration, so the preservation assertion
	// below is sound after completion.
	pollPublication(t, fixture.db, progressID, "completed", 15*time.Second)
	after := deadLetterSnapshot(t, fixture.db, pastID)
	if after != snapshot {
		t.Fatalf("paused purge mutated the dead-letter row:\n before %+v\n after  %+v", snapshot, after)
	}
	stop()

	// Leg 2: unpaused boundary — exactly the past-retention row is purged.
	fixture.buildStack(t, false, time.Hour)
	insideID := idgen.NewID()
	if err := fixture.auditLedger.Record(t.Context(), service.AuditFactInput{
		TenantID: "tenant-one", EventID: insideID, ActorUID: "actor-one", Action: "account.viewed",
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Exec(`UPDATE audit_publications SET status = 'dead_letter',
		updated_at = now() - interval '10 minutes'
		FROM audit_ledger l WHERE audit_publications.fact_id = l.id AND l.event_id = ?`, insideID).Error; err != nil {
		t.Fatal(err)
	}
	stop2 := fixture.callbackSvc.Start(t.Context(), 50*time.Millisecond)
	defer stop2()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if n := appCountRows(t, fixture.db, `SELECT count(*) FROM audit_publications WHERE id = ?`, snapshot.id); n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("past-retention dead-letter row was not purged within 15s (purge scan did not honor the retention copy)")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := appCountRows(t, fixture.db, `SELECT count(*) FROM audit_publications
		WHERE fact_id = (SELECT id FROM audit_ledger WHERE event_id = ?)`, insideID); n != 1 {
		t.Fatalf("inside-retention dead-letter row = %d rows, want 1 (retention boundary)", n)
	}
	if n := appCountRows(t, fixture.db, `SELECT count(*) FROM audit_ledger WHERE event_id = ?`, pastID); n != 1 {
		t.Fatalf("ledger fact for purged publication = %d rows, want 1 (purge deletes only the publication)", n)
	}
}
