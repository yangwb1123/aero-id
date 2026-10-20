//go:build integration

package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector/auditgovernance"
	"github.com/aero/aero-id/internal/domain"
	dbpkg "github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository/testpg"
)

// bootDatabase couples a fresh testcontainers Postgres with the DatabaseConfig
// the boot e2e hands to app.New. Per-test container is a hard fixture
// invariant (determinism review §1): every exact-POST-count assertion is
// sound only when no other test's rows share the database.
type bootDatabase struct {
	cfg config.DatabaseConfig
	db  *gorm.DB
}

// newBootDatabase launches a throwaway Postgres like testpg.Start but also
// returns the parsed DatabaseConfig so the boot e2e can override the loaded
// config's database block with a live endpoint.
const postgresFixtureTimeout = time.Minute

func newBootDatabase(t *testing.T) bootDatabase {
	t.Helper()
	startCtx, cancelStart := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	var db *gorm.DB
	c, err := testpg.StartContainer(t, startCtx, func() error {
		if db == nil {
			return nil
		}
		sqlDB, err := db.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	})
	cancelStart()
	if err != nil {
		t.Fatalf("start pg container: %v", err)
	}

	hostCtx, cancelHost := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	host, err := c.Host(hostCtx)
	cancelHost()
	if err != nil {
		t.Fatalf("pg host: %v", err)
	}
	portCtx, cancelPort := context.WithTimeout(context.Background(), postgresFixtureTimeout)
	mapped, err := c.MappedPort(portCtx, "5432/tcp")
	cancelPort()
	if err != nil {
		t.Fatalf("pg port: %v", err)
	}
	cfg := config.DatabaseConfig{
		Host: host, Port: mapped.Int(), User: "aero", Password: "aero",
		DBName: "aero_id_test", SSLMode: "disable", LogLevel: "silent",
		MaxOpenConns: 5, MaxIdleConns: 2,
	}
	db, err = dbpkg.Open(dbpkg.Options{DSN: cfg.DSN(), MaxOpenConns: 5, LogLevel: "silent"})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	return bootDatabase{cfg: cfg, db: db}
}

// bootConfigFromShippedFile Loads the real configs/config.yaml (resolved via
// testpg.RepoRoot — `go test` CWD is the package dir, so a relative path
// would fail) and applies only test-runtime overrides: database from a fresh
// container, ephemeral gRPC listener, temp storage root, hermetic SSO
// endpoint, fast drain tick. The AERO_ID_AUDIT_GOVERNANCE_ENABLED pin must
// precede EVERY Load of the real file: viper AutomaticEnv merges ambient CI
// env, and the shipped governance section is disabled with zero bindings by
// design. Migrations run explicitly (auto_migrate is false in the shipped
// file, matching production).
func bootConfigFromShippedFile(t *testing.T) (*config.Config, *gorm.DB) {
	t.Helper()
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "false")
	// The shipped file intentionally contains no secret material. Supply
	// test-only values before Load so this boot fixture exercises the real
	// config path without weakening the production configuration contract.
	t.Setenv("AERO_ID_SIGNING_KEY", "ssssssssssssssssssssssssssssssss")
	t.Setenv("AERO_ID_AUDIT_LEDGER_SIGNING_KEY", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	cfg, err := config.Load(filepath.Join(testpg.RepoRoot(t), "configs", "config.yaml"))
	if err != nil {
		t.Fatalf("load shipped config: %v", err)
	}
	bd := newBootDatabase(t)
	cfg.Database = bd.cfg
	cfg.Server.GRPC.Host = "127.0.0.1"
	cfg.Server.GRPC.Port = 0
	cfg.Storage.Local.Root = t.TempDir()
	cfg.SSO.Endpoint = "" // lazy grpc.NewClient; hermetic (no dial at boot)
	// The shipped config intentionally leaves JWKS unset so a standard
	// production composition fails closed. This fixture opts into a valid,
	// unreachable HTTPS URL because app.New constructs (but does not fetch) the
	// verifier; no token validation is exercised by these boot tests.
	cfg.SSO.JWKSURL = "https://snaplink.example.test/.well-known/jwks.json"
	cfg.SSO.Issuer = "snaplink"
	cfg.SSO.Audience = "aero-id"
	cfg.Callback.WorkerInterval = 50 * time.Millisecond
	testpg.Migrate(t, bd.db, filepath.Join(testpg.RepoRoot(t), "migrations"))
	return cfg, bd.db
}

// insertPendingPublication SQL-inserts one ledger fact + one pending
// publication row (the writerRun shape) so the drain started inside app.New
// claims and delivers it. Returns the dedup event_id for polling.
func insertPendingPublication(t *testing.T, db *gorm.DB, tenant string) domain.ID {
	t.Helper()
	factID := idgen.NewID()
	eventID := idgen.NewID()
	if err := db.Exec(`
		INSERT INTO audit_ledger (id, hash_version, chain_sequence, source, source_region, partition_key,
			event_id, tenant_id, action, occurred_at, payload, payload_hash, previous_hash, record_hash, created_at)
		VALUES ($1, 2, 1, 'aero-id', 'local', $2, $3, $4, 'test.gate', now(), '{}'::jsonb, 'ph', '', 'rh', now())`,
		factID, "boot-leg", eventID, tenant).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`
		INSERT INTO audit_publications (id, fact_id, status, attempt, next_attempt_at, created_at, updated_at)
		VALUES ($1, $2, 'pending', 0, now(), now(), now())`, idgen.NewID(), factID).Error; err != nil {
		t.Fatal(err)
	}
	return eventID
}

// TestCompositionRootBootsGovernanceE2E (REQ-1/REQ-2, T-5, L5.6, Leg A): the
// boot e2e drives the REAL composition root — config.Load of the shipped
// file → surgical test-runtime overrides → app.New → the drain started at
// new.go:163 claims a SQL-inserted pending publication and delivers exactly
// one POST. The body carries the four file-derived envelope fields
// (schema_id/data_classification/retention_class/actor_id from
// configs/config.yaml) plus schema_version 99 — the copy-liveness override:
// the connector defaults are byte-identical to the shipped-file values, so
// without the distinct override a dropped knob copy in buildAuditPublishers
// would pass trivially (value-coincidence false green). source_system is
// recomputed via TenantSourceID, never hardcoded.
func TestCompositionRootBootsGovernanceE2E(t *testing.T) {
	endpoint := newGovernanceTestEndpoint(t)
	cfg, db := bootConfigFromShippedFile(t)
	t.Setenv("TEST_AUDIT_SECRET", "audit-secret-value-000000000000000001")
	cfg.AuditGovernance.Enabled = true
	cfg.AuditGovernance.Endpoint = endpoint.url
	cfg.AuditGovernance.AllowInsecureLoopback = true // shipped file has false; httptest is loopback
	cfg.AuditGovernance.SchemaVersion = 99           // copy-liveness: no connector default can produce this
	cfg.AuditGovernance.Bindings = map[string]config.AuditGovernanceBindingConfig{
		"tenant-one": {TokenURL: endpoint.url + "/token", ClientID: "audit-client",
			ClientSecretEnv: "TEST_AUDIT_SECRET", Scope: "audit:event:write",
			Resources: []string{"audit-governance"}},
	}

	application, err := New(t.Context(), cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("app.New on the loaded shipped config: %v", err)
	}
	if application.stopCallbacks == nil {
		t.Fatal("drain was not started by the composition root (stopCallbacks nil — new.go:163 missing)")
	}
	t.Cleanup(application.shutdown)
	t.Cleanup(application.Close)

	eventID := insertPendingPublication(t, db, "tenant-one")
	// Poll the authoritative DB state first, side-effect counters second.
	pollPublication(t, db, eventID, "completed", 15*time.Second)
	if got := endpoint.postCount(); got != 1 {
		t.Fatalf("events POSTs = %d, want exactly 1 (drain started at new.go:163)", got)
	}
	rowID := ledgerRowID(t, db, eventID)
	body := endpoint.recordedBodies()[0]
	if body["event_id"] != rowID.String() || body["idempotency_key"] != rowID.String() {
		t.Fatalf("body event_id/idempotency_key = %v/%v, want %s (ledger row id)", body["event_id"], body["idempotency_key"], rowID)
	}
	sourceSystem, err := auditgovernance.TenantSourceID("aero-id", "tenant-one")
	if err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]any{
		"schema_id": "aero.id.audit-fact", "data_classification": "personal",
		"retention_class": "account_7y", "schema_version": float64(99),
		"source_system": sourceSystem,
	} {
		if got := body[field]; got != want {
			t.Errorf("body %s = %v, want %v (file-derived through the real root)", field, got, want)
		}
	}
	actor, ok := body["actor"].(map[string]any)
	if !ok || actor["id"] != "aero-id" {
		t.Errorf("body actor = %v, want id aero-id (file-derived)", body["actor"])
	}
}

// TestCompositionRootRefusesEnabledZeroBindings (REQ-3, T-10.2, L5.6, Leg B2):
// the composition root must refuse enabled+zero-bindings with the
// byte-identical documented gate error. Only Enabled is overridden — bindings
// stay {} and source_prefix stays aero-id from the shipped file. Without the
// buildAuditStack re-check this returns the incidental connector error
// ("audit governance: invalid configuration", oauth.go) and an err != nil
// assertion would pass falsely; the byte-exact string pins the gate at the
// root (the Load gate is the primary, binary-agnostic enforcement point; this
// leg makes the composition-root second line a tested line).
func TestCompositionRootRefusesEnabledZeroBindings(t *testing.T) {
	cfg, _ := bootConfigFromShippedFile(t)
	cfg.AuditGovernance.Enabled = true

	_, err := New(t.Context(), cfg, zap.NewNop())
	if err == nil {
		t.Fatal("app.New with enabled+zero-bindings must refuse (activation gate)")
	}
	if err.Error() != auditGovernanceActivationGateError {
		t.Fatalf("app.New error = %q, want byte-identical %q (incidental connector refusal is a false green)",
			err.Error(), auditGovernanceActivationGateError)
	}
}

// TestCompositionRootKillSwitchSettlesWithoutPOSTs (REQ-1, T-5, L5.6, Leg C):
// the unmodified shipped governance section (enabled=false, zero bindings)
// boots the real root; a pending row settles completed with ZERO POSTs — the
// deliberate off-state (relay off = no forwarding; facts stay durable in the
// ledger). Completion is a proven drain side-effect, so this can never pass
// on a drain that never ran, and any regression that reintroduces inline
// publishing makes the row complete with POSTs >= 1 — caught.
func TestCompositionRootKillSwitchSettlesWithoutPOSTs(t *testing.T) {
	endpoint := newGovernanceTestEndpoint(t)
	cfg, db := bootConfigFromShippedFile(t) // no governance overrides

	application, err := New(t.Context(), cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("app.New on the unmodified shipped governance section: %v", err)
	}
	t.Cleanup(application.shutdown)
	t.Cleanup(application.Close)

	eventID := insertPendingPublication(t, db, "tenant-one")
	pollPublication(t, db, eventID, "completed", 15*time.Second)
	if got := endpoint.postCount(); got != 0 {
		t.Fatalf("events POSTs with the kill switch off = %d, want 0 (deliberate off-state)", got)
	}
	var status string
	if err := db.Raw(`SELECT ap.status FROM audit_publications ap
		JOIN audit_ledger l ON l.id = ap.fact_id WHERE l.event_id = ?`, eventID).Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("kill-switch publication = %q, want completed (durable facts still settle)", status)
	}
}
