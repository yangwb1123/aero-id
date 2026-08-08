// Command audit-replay is the operator recovery surface for dead-lettered
// audit publications and callback deliveries (B2-5): it moves dead_letter
// rows back to pending with a full fresh retry budget (attempt reset to 0,
// lease cleared, error_code manual_replay), in bounded idempotent batches.
//
// Every mutating run REQUIRES -actor and appends one durable audit fact per
// affected tenant in the same transaction (the fact tenant derives from the
// rows, never from a flag); -tenant narrows the SELECTION instead of
// attributing. Delivery itself stays with the server drain worker
// (ClaimPublication / FetchDueDeliveries pick the requeued rows up on the
// next tick); the CLI never publishes. Re-running with identical arguments
// requeues 0 additional rows (the dead_letter status predicate is the
// idempotency key) and mints no facts. Dry-run counts are bounded at -limit
// and equal exactly what the next mutating run will affect — replay until
// both dry-run lines print 0.
//
// Usage:
//
//	audit-replay [-config path] [-kind publication|delivery|both] [-limit N]
//	             [-id id[,id...]] [-tenant T] [-actor NAME] [-dry-run]
//	             [-report path]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/db"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

// replayLimitDefault is the CLI's effective per-kind batch bound (mirrors the
// repository default; -limit 0 falls back here — there is no unbounded
// escape hatch by design).
const replayLimitDefault = 100

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "audit-replay: "+err.Error())
		os.Exit(1)
	}
}

func usage() string {
	return "usage: audit-replay [-config path] [-kind publication|delivery|both] [-limit N] " +
		"[-id id[,id...]] [-tenant tenant] [-actor actor] [-dry-run] [-report path]\n\n" +
		"flags:\n" +
		"  -config   path to config file (default configs/config.yaml)\n" +
		"  -kind     which dead-letter rows to requeue: publication | delivery | both (default both)\n" +
		"  -limit    max rows requeued per kind (default 100; -id overrides)\n" +
		"  -id       exact publication/delivery row IDs, comma-separated (overrides -limit)\n" +
		"  -tenant   SELECTION FILTER: requeue only rows whose ledger/subscription\n" +
		"            tenant equals this value (default: all tenants). Audit facts\n" +
		"            always derive their tenant from the rows, never from this flag.\n" +
		"  -actor    operator identity recorded on the requeue audit facts — REQUIRED\n" +
		"            in mutating mode (no default); trimmed, at most 256 bytes\n" +
		"  -report   append-only journal path: one line per leg after commit\n" +
		"            (ts, operation_id, kind, affected, fact_ids), fsynced\n" +
		"  -dry-run  print the bounded count and affected tenants of the next mutating\n" +
		"            run (capped at -limit); mutate nothing, mint nothing"
}

//nolint:cyclop,funlen // the replay CLI intentionally validates flags, opens the DB, and dispatches both kinds here.
func run(args []string) error {
	fs := flag.NewFlagSet("audit-replay", flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/config.yaml", "path to config file")
	kind := fs.String("kind", "both", "publication | delivery | both")
	limit := fs.Int("limit", replayLimitDefault, "max rows requeued per kind")
	rawIDs := fs.String("id", "", "comma-separated explicit row IDs (overrides -limit)")
	tenant := fs.String("tenant", "", "tenant selection filter ('' = all tenants)")
	actor := fs.String("actor", "", "operator identity on requeue facts — REQUIRED in mutating mode")
	dryRun := fs.Bool("dry-run", false, "count rows that would be requeued without mutating anything")
	reportPath := fs.String("report", "", "append-only journal path (one line per leg after commit)")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usage()) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	ids, err := parseIDs(*rawIDs)
	if err != nil {
		return err
	}
	// Fail closed before any mutation: -actor is mandatory in mutating mode
	// (the un-audited default is deleted — every state-changing replay run
	// produces a durable audit fact per affected tenant).
	if !*dryRun {
		if strings.TrimSpace(*actor) == "" {
			return fmt.Errorf("-actor is required in mutating mode (every requeue commits an audit fact per affected tenant)")
		}
		if len(strings.TrimSpace(*actor)) > 256 {
			return fmt.Errorf("-actor must be at most 256 bytes")
		}
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Fail closed before any mutation (mirror validateAuditLedgerConfig in
	// internal/app/audit.go): a missing signing key turns every in-tx audit
	// append into a runtime failure.
	if strings.TrimSpace(cfg.AuditLedger.SigningKey) == "" {
		return fmt.Errorf("audit ledger signing key is not configured")
	}

	gdb, err := db.Open(db.Options{
		DSN:             cfg.Database.DSN(),
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		LogLevel:        cfg.Database.LogLevel,
	})
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return fmt.Errorf("sql db handle: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	return replay(gdb, cfg, ids, *kind, *limit, *tenant, *actor, *dryRun, *reportPath)
}

// replay runs the requeue against an already-open database (extracted from run
// so integration tests can drive the exact CLI semantics against testpg).
// One fresh v7 OperationID is minted per mutating run and shared by every leg
// and tenant (controlled-replay doctrine); dry runs build no intent, mutate
// nothing, and mint nothing.
func replay(gdb *gorm.DB, cfg *config.Config, ids []domain.ID, kind string, limit int,
	tenant, actor string, dryRun bool, reportPath string) error {
	if limit <= 0 {
		limit = replayLimitDefault
	}
	ledger := repository.NewAuditLedgerRepository(gdb, repository.AuditLedgerConfig{SigningKey: cfg.AuditLedger.SigningKey})
	callbacks := repository.NewCallbackRepository(gdb, ledger)
	publications, ok := ledger.(repository.AuditPublicationRepository)
	if !ok {
		return fmt.Errorf("audit ledger repository does not implement the publication replay surface")
	}
	pubIntent, delIntent, err := buildReplayIntents(dryRun, actor, tenant, allowedTenants(cfg))
	if err != nil {
		return err
	}
	switch strings.ToLower(kind) {
	case "publication":
		return replayPublications(publications, ids, limit, tenant, pubIntent, dryRun, reportPath)
	case "delivery":
		return replayDeliveries(callbacks, ids, limit, tenant, delIntent, dryRun, reportPath)
	case "both":
		if err := replayPublications(publications, ids, limit, tenant, pubIntent, dryRun, reportPath); err != nil {
			return err
		}
		return replayDeliveries(callbacks, ids, limit, tenant, delIntent, dryRun, reportPath)
	default:
		return fmt.Errorf("invalid -kind %q (want publication | delivery | both)", kind)
	}
}

// buildReplayIntents builds the per-leg operator intents for one mutating run:
// ONE fresh v7 OperationID is minted per run and shared by every leg and
// tenant (controlled-replay doctrine). Dry runs build no intent — they mutate
// nothing and mint nothing.
func buildReplayIntents(dryRun bool, actor, tenant string, allowed []string) (repository.ReplayIntent, repository.ReplayIntent, error) {
	if dryRun {
		return repository.ReplayIntent{}, repository.ReplayIntent{}, nil
	}
	pubIntent, err := service.BuildAuditReplayIntent(context.Background(), "publication", actor, tenant, allowed, domain.ID{})
	if err != nil {
		return repository.ReplayIntent{}, repository.ReplayIntent{}, err
	}
	delIntent, err := service.BuildAuditReplayIntent(context.Background(), "delivery", actor, tenant, allowed, pubIntent.OperationID)
	if err != nil {
		return repository.ReplayIntent{}, repository.ReplayIntent{}, err
	}
	return pubIntent, delIntent, nil
}

// replayPublications runs the publication leg: bounded dry-run count plus
// affected-tenant report, or the mutating requeue with its in-tx per-tenant
// facts, stderr logfmt run record, and optional fsynced journal line.
func replayPublications(ledger repository.AuditPublicationRepository, ids []domain.ID, limit int,
	tenantFilter string, intent repository.ReplayIntent, dryRun bool, reportPath string) error {
	ctx := context.Background()
	if dryRun {
		count, err := ledger.CountDeadLetterPublications(ctx, ids, limit, tenantFilter)
		if err != nil {
			return fmt.Errorf("count dead-letter publications: %w", err)
		}
		tenants, err := ledger.DeadLetterTenants(ctx, ids, limit, tenantFilter)
		if err != nil {
			return fmt.Errorf("list dead-letter tenants: %w", err)
		}
		fmt.Printf("publications to requeue: %d (dry run; capped at -limit %d)\n", count, limit)
		if len(tenants) > 0 {
			fmt.Printf("affected tenants: %s\n", strings.Join(tenants, ", "))
		}
		return nil
	}
	if err := replayTenantPreflight(ctx, ledger.DeadLetterTenants, ids, limit, intent); err != nil {
		return err
	}
	start := time.Now()
	requeued, facts, err := ledger.RequeuePublication(ctx, ids, limit, intent)
	if err != nil {
		return fmt.Errorf("requeue publications: %w", err)
	}
	fmt.Printf("requeued %d publication(s)\n", requeued)
	logReplayRun(intent, "publication", limit, requeued, facts, time.Since(start))
	return writeJournal(reportPath, intent.OperationID, "publication", requeued, facts)
}

// replayDeliveries runs the delivery leg; semantics mirror
// replayPublications with the callback repository surface.
func replayDeliveries(callbacks repository.CallbackRepository, ids []domain.ID, limit int,
	tenantFilter string, intent repository.ReplayIntent, dryRun bool, reportPath string) error {
	ctx := context.Background()
	if dryRun {
		count, err := callbacks.CountDeadLetterDeliveries(ctx, ids, limit, tenantFilter)
		if err != nil {
			return fmt.Errorf("count dead-letter deliveries: %w", err)
		}
		tenants, err := callbacks.DeadLetterTenants(ctx, ids, limit, tenantFilter)
		if err != nil {
			return fmt.Errorf("list dead-letter tenants: %w", err)
		}
		fmt.Printf("deliveries to requeue: %d (dry run; capped at -limit %d)\n", count, limit)
		if len(tenants) > 0 {
			fmt.Printf("affected tenants: %s\n", strings.Join(tenants, ", "))
		}
		return nil
	}
	if err := replayTenantPreflight(ctx, callbacks.DeadLetterTenants, ids, limit, intent); err != nil {
		return err
	}
	start := time.Now()
	requeued, facts, err := callbacks.RequeueDead(ctx, ids, limit, intent)
	if err != nil {
		return fmt.Errorf("requeue deliveries: %w", err)
	}
	fmt.Printf("requeued %d delivery(ies)\n", requeued)
	logReplayRun(intent, "delivery", limit, requeued, facts, time.Since(start))
	return writeJournal(reportPath, intent.OperationID, "delivery", requeued, facts)
}

// replayTenantPreflight is the friendly early attribution/binding check on
// the would-be selection. It is NOT load-bearing — the authoritative check
// runs in-tx on the actual locked selection (checkReplayTenants); this only
// saves the operator a doomed run with a clear error and exit 1.
func replayTenantPreflight(ctx context.Context,
	list func(context.Context, []domain.ID, int, string) ([]string, error),
	ids []domain.ID, limit int, intent repository.ReplayIntent) error {
	tenants, err := list(ctx, ids, limit, intent.TenantFilter)
	if err != nil {
		return fmt.Errorf("list dead-letter tenants: %w", err)
	}
	for _, tenantID := range tenants {
		if strings.TrimSpace(tenantID) == "" {
			return fmt.Errorf("%w: rows without a tenant cannot be attributed", repository.ErrAuditReplayUnattributableTenant)
		}
		if tenantID != "legacy" && intent.AllowedTenants != nil && !slices.Contains(intent.AllowedTenants, tenantID) {
			return fmt.Errorf("%w: %q", repository.ErrAuditReplayTenantNotBound, tenantID)
		}
	}
	return nil
}

// logReplayRun emits the per-leg logfmt run record on stderr — the operator
// observability for a run: operator, shared operation_id, kind, bounds,
// affected count, and the fact ids the ledger minted.
func logReplayRun(intent repository.ReplayIntent, kind string, limit, affected int,
	facts []*domain.AuditFact, elapsed time.Duration) {
	fmt.Fprintf(os.Stderr,
		"ts=%s level=info cmd=audit-replay operator=%s operation_id=%s kind=%s dry_run=false limit=%d affected=%d fact_ids=%s duration_ms=%d exit=0\n",
		time.Now().UTC().Format(time.RFC3339Nano), intent.ActorUID, intent.OperationID, kind, limit, affected,
		joinFactIDs(facts), elapsed.Milliseconds())
}

// writeJournal appends one line per leg after commit and fsyncs — the durable
// cross-kind partial-state marker: a kill between the two legs of -kind both
// leaves leg A's line (and its facts) and no leg B line; re-running -kind both
// completes the other leg idempotently (a 0-row leg mints nothing).
func writeJournal(path string, operationID domain.ID, kind string, affected int, facts []*domain.AuditFact) error {
	if path == "" {
		return nil
	}
	line := fmt.Sprintf("ts=%s operation_id=%s kind=%s affected=%d fact_ids=%s\n",
		time.Now().UTC().Format(time.RFC3339Nano), operationID, kind, affected, joinFactIDs(facts))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line); err != nil {
		return err
	}
	return f.Sync()
}

func joinFactIDs(facts []*domain.AuditFact) string {
	ids := make([]string, 0, len(facts))
	for _, fact := range facts {
		ids = append(ids, fact.ID.String())
	}
	return strings.Join(ids, ",")
}

// allowedTenants derives the binding allow-list from the config: the keys of
// audit_governance.bindings when governance is enabled, else nil (no
// constraint — the in-tx binding check is a no-op). 'legacy' is exempt in the
// repository check, mirroring the boot coverage gate's exclusions.
func allowedTenants(cfg *config.Config) []string {
	if !cfg.AuditGovernance.Enabled {
		return nil
	}
	tenants := make([]string, 0, len(cfg.AuditGovernance.Bindings))
	for tenantID := range cfg.AuditGovernance.Bindings {
		tenants = append(tenants, tenantID)
	}
	sort.Strings(tenants)
	return tenants
}

// parseIDs converts the comma-separated -id flag into domain IDs. Empty input
// yields nil (bounded by -limit); a malformed ID is a hard error so the
// operator cannot silently requeue the wrong set.
func parseIDs(raw string) ([]domain.ID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]domain.ID, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := idgen.Parse(part)
		if err != nil {
			return nil, fmt.Errorf("invalid -id %q: %w", part, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
