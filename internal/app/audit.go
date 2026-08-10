package app

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector/auditgovernance"
	httpapi "github.com/aero/aero-id/internal/handler/http"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
	"github.com/aero/aero-id/internal/storage"
)

// validateAuditLedgerConfig fails startup when the ledger signing key is
// missing: an empty key turns every audit append (and every converted
// mutation) into a runtime failure with no startup error (FM-8). The dev
// placeholder in configs/config.yaml is non-empty, so the out-of-box config
// still boots.
func validateAuditLedgerConfig(cfg *config.Config) error {
	if cfg == nil || strings.TrimSpace(cfg.AuditLedger.SigningKey) == "" {
		return fmt.Errorf("audit ledger signing key is not configured")
	}
	return nil
}

// auditGovernanceActivationGateError is the byte-identical activation-gate
// error from internal/config/validate.go (validateAuditGovernance): an
// enabled relay with zero tenant bindings silently drops every publication
// (no tenant is captured) — the exact silent-loss class the outbox contract
// forbids. config.Load is the primary, binary-agnostic enforcement point
// (cmd/server, cmd/audit-replay and cmd/migrate all Load the config before
// any DB handle opens); this re-check is the server composition root's
// second line for in-code configs that bypass Load. Without it an
// enabled+zero-bindings config reaches the connector and refuses with the
// incidental oauth ErrInvalidConfig message after a DB-touching coverage
// check (FM-13).
const auditGovernanceActivationGateError = "audit_governance.enabled requires at least one tenant binding (activation gate)"

// validateAuditGovernanceGate re-checks the activation gate at the
// composition root, before any DB or connector use (nil-gdb testable). It
// returns the byte-identical documented error; any other message is drift
// (FM-02). Disabling the relay (the kill switch) accepts empty bindings.
func validateAuditGovernanceGate(cfg *config.Config) error {
	if cfg != nil && cfg.AuditGovernance.Enabled && len(cfg.AuditGovernance.Bindings) == 0 {
		return errors.New(auditGovernanceActivationGateError)
	}
	return nil
}

// buildAuditStack constructs the durable audit stack in dependency order:
// ledger repo (fail-closed, wired into the callback repo) → callback service →
// standalone-append audit service (single delivery: the drain worker) →
// governance publishers (flag-gated) → drain wiring.
func buildAuditStack(cfg *config.Config, gdb *gorm.DB, controlMetrics service.ControlPlaneMetrics) (
	service.CallbackService, service.AuditLedgerService, repository.FullAuditLedgerRepository, error) {
	if err := validateAuditLedgerConfig(cfg); err != nil {
		return nil, nil, nil, err
	}
	// B2-6 activation gate (second line): refuse enabled+zero-bindings before
	// any repository or connector is constructed. Load() already enforces the
	// same error for every binary that Loads the config; this pin keeps the
	// documented error on the boot path for in-code configs.
	if err := validateAuditGovernanceGate(cfg); err != nil {
		return nil, nil, nil, err
	}
	auditLedgerRepo := repository.NewAuditLedgerRepository(gdb, repository.AuditLedgerConfig{SigningKey: cfg.AuditLedger.SigningKey})
	callbackRepo := repository.NewCallbackRepository(gdb, auditLedgerRepo)
	callbackSvc := service.NewCallbackService(callbackRepo, service.CallbackConfig{
		MasterKey: cfg.Callback.MasterKey, RequestTimeout: cfg.Callback.RequestTimeout,
		MaxResponseBytes: cfg.Callback.MaxResponseBytes, MaxAttempts: cfg.Callback.MaxAttempts,
		RetryBase: cfg.Callback.RetryBase, AllowPrivateTarget: cfg.Callback.AllowPrivateTarget,
		WorkerConcurrency: cfg.Callback.WorkerConcurrency,
		// M-5: the drain lease must exceed the worst-case batch latency; the
		// per-publish bound is the governance publisher timeout (0 when the
		// flag-gated publisher is disabled — publicationLease defaults it).
		PublicationTimeout: cfg.AuditGovernance.Timeout,
		// B2-5 WORKER-1: the dead-letter purge scan in the drain loop is
		// bounded by dead_letter_retention and suspended by purge_pause.
		PurgePause:          cfg.AuditGovernance.PurgePause,
		DeadLetterRetention: cfg.AuditGovernance.DeadLetterRetention,
	})
	auditPublishers, err := buildAuditPublishers(cfg, controlMetrics, gormAuditBindingCoverage{gdb})
	if err != nil {
		return nil, nil, nil, err
	}
	auditLedger := service.NewAuditLedgerService(auditLedgerRepo)
	if setter, ok := auditLedger.(service.ControlPlaneMetricsSetter); ok {
		setter.SetControlPlaneMetrics(controlMetrics)
	}
	// F-1: the drain worker's publication observability (claim-failed,
	// finish-failure, outcome, drain-skipped counters) is attached to the
	// callback service, not the ledger. Without this the drain metrics would
	// fall back to noopControlPlaneMetrics and the §5 runbook alerts would
	// match nothing.
	if setter, ok := callbackSvc.(service.ControlPlaneMetricsSetter); ok {
		setter.SetControlPlaneMetrics(controlMetrics)
	}
	wireAuditServices(callbackSvc, auditLedgerRepo, auditPublishers...)
	return callbackSvc, auditLedger, auditLedgerRepo, nil
}

// wireAuditServices fails closed at composition time (FM-8): the drain worker
// is the only audit delivery path, so an outbox repository that cannot be
// attached — or a callback service that cannot accept it — is a programmer
// error and must stop the process at startup, not pend facts silently. With
// the shipped concrete types both assertions always hold; the panics are
// unreachable insurance against future wiring regressions.
func wireAuditServices(callbackSvc service.CallbackService, auditLedgerRepo repository.AuditLedgerRepository,
	publishers ...service.AuditPublisher) {
	setter, ok := callbackSvc.(interface {
		SetAuditPublicationRepository(repository.AuditPublicationRepository)
	})
	if !ok {
		panic("app: callback service must implement SetAuditPublicationRepository (audit outbox drain is the only delivery path)")
	}
	publicationRepo, ok := auditLedgerRepo.(repository.AuditPublicationRepository)
	if !ok {
		panic("app: audit ledger repository must implement AuditPublicationRepository (audit outbox drain is the only delivery path)")
	}
	setter.SetAuditPublicationRepository(publicationRepo)
	if setter, ok := callbackSvc.(interface {
		SetAuditPublishers(...service.AuditPublisher)
	}); !ok {
		panic("app: callback service must implement SetAuditPublishers (audit outbox drain is the only delivery path)")
	} else {
		setter.SetAuditPublishers(publishers...)
	}
}

// auditBindingCoverageSeamError is the byte-identical fail-fast error for a
// nil binding-coverage seam (FM-9): a nil seam would silently skip the
// coverage gate — the exact silent-loss class the outbox contract forbids.
const auditBindingCoverageSeamError = "audit governance binding coverage seam is not wired"

// auditBindingCoverage validates that every durable tenant with pending audit
// state has a governance binding. The gorm implementation issues the UNION
// query; tests inject a fake so buildAuditPublishers is unit-testable without
// a database. This is the single shared seam also consumed by the sibling
// direction's T-5 ("fake binding-coverage querier"): never fork a second
// coverage mechanism.
type auditBindingCoverage interface {
	CheckAuditBindingCoverage(bindings map[string]config.AuditGovernanceBindingConfig) error
}

// gormAuditBindingCoverage is the production implementation of
// auditBindingCoverage, wiring the existing UNION query into the seam.
type gormAuditBindingCoverage struct{ db *gorm.DB }

func (g gormAuditBindingCoverage) CheckAuditBindingCoverage(bindings map[string]config.AuditGovernanceBindingConfig) error {
	return validateAuditBindingCoverage(g.db, bindings)
}

// buildAuditScopeMissingHook returns the OnScopeRejected closure wired to the
// shared control-plane registry. The label is the normalized (TrimSpace'd)
// configured source prefix; the connector contract (token_invalidation_test.go
// :643 family) guarantees one invocation per token-endpoint scope rejection,
// so one hook call must produce exactly one increment.
func buildAuditScopeMissingHook(controlMetrics service.ControlPlaneMetrics, sourcePrefix string) func(string) {
	return func(string) {
		controlMetrics.ObserveAuditScopeMissing(strings.TrimSpace(sourcePrefix))
	}
}

func buildAuditPublishers(cfg *config.Config, controlMetrics service.ControlPlaneMetrics, coverage auditBindingCoverage) ([]service.AuditPublisher, error) {
	if !cfg.AuditGovernance.Enabled {
		return nil, nil
	}
	// FM-9: a nil seam would silently skip the coverage gate. The guard sits
	// after the Enabled early-return so the disabled path (the kill switch)
	// stays behaviorally identical and never requires a seam.
	if coverage == nil {
		return nil, errors.New(auditBindingCoverageSeamError)
	}
	settings := cfg.AuditGovernance
	// Label-seam guard (merged plan §5 delta 4): governance facts are stamped
	// Source: "aero-id" (audit_hold.go), so the hook closure and the
	// service-side counter must emit the same normalized label. Any other
	// source_prefix would split aero_control_audit_scope_missing_total into
	// two label sets for one condition — fail fast at wiring time.
	if strings.TrimSpace(settings.SourcePrefix) != "aero-id" {
		return nil, fmt.Errorf("audit governance source_prefix must be %q, got %q", "aero-id", settings.SourcePrefix)
	}
	bindings, err := auditOAuthBindings(settings.Bindings)
	if err != nil {
		return nil, err
	}
	if err := coverage.CheckAuditBindingCoverage(settings.Bindings); err != nil {
		return nil, err
	}
	tokens, err := auditgovernance.NewOAuthTokenSource(auditgovernance.OAuthConfig{
		SourcePrefix: settings.SourcePrefix, Bindings: bindings,
		Timeout: settings.Timeout, RefreshSkew: settings.RefreshSkew,
		AllowInsecureLoopback: settings.AllowInsecureLoopback,
		// C-C: no double counting. A token-endpoint scope rejection (400-gate
		// or scope-claim validation) fires only this hook — the drain counter
		// skips it via the ErrTokenEndpointScopeMissing marker
		// (callback_audit.go publishAuditFact). An events-endpoint
		// classification fires only the service-side counter (the hook is
		// structurally unreachable there). Exactly one increment per condition,
		// on the single label set {source="aero-id"}.
		OnScopeRejected: buildAuditScopeMissingHook(controlMetrics, settings.SourcePrefix),
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("audit governance oauth: %w", err)
	}
	publisher, err := auditgovernance.NewPublisher(auditgovernance.Config{
		Endpoint: settings.Endpoint, SourcePrefix: settings.SourcePrefix, ActorID: settings.ActorID,
		SchemaID: settings.SchemaID, SchemaVersion: settings.SchemaVersion,
		DataClassification: settings.DataClassification, RetentionClass: settings.RetentionClass,
		Timeout: settings.Timeout, AllowInsecureLoopback: settings.AllowInsecureLoopback,
	}, tokens, nil)
	if err != nil {
		return nil, fmt.Errorf("audit governance publisher: %w", err)
	}
	return []service.AuditPublisher{publisher}, nil
}

func auditOAuthBindings(values map[string]config.AuditGovernanceBindingConfig) (map[string]auditgovernance.OAuthBindingConfig, error) {
	result := make(map[string]auditgovernance.OAuthBindingConfig, len(values))
	for tenantID, value := range values {
		secret, err := config.ResolveClientSecretEnv(value.ClientSecretEnv)
		if err != nil {
			return nil, fmt.Errorf("audit governance credentials are unavailable")
		}
		result[tenantID] = auditgovernance.OAuthBindingConfig{
			TokenURL: value.TokenURL, ClientID: value.ClientID, ClientSecret: secret,
			Scope: value.Scope, Resources: value.Resources,
		}
	}
	return result, nil
}

func validateAuditBindingCoverage(db *gorm.DB, bindings map[string]config.AuditGovernanceBindingConfig) error {
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
		return fmt.Errorf("audit governance binding coverage check failed: %w", err)
	}
	for _, tenantID := range tenants {
		if _, found := bindings[tenantID]; !found {
			return fmt.Errorf("audit governance bindings do not cover durable tenant state")
		}
	}
	return nil
}

// auditWiredService pairs a named service with the value the composition root
// built, so a failed wiring assertion reports which service is broken.
type auditWiredService struct {
	name string
	svc  any
}

// assertAuditWiring fails fast at startup if any of the six legacy services
// that own the `if s.audit != nil` audit-write skip guards (the 10 fail-closed
// Log sites) was constructed without a non-nil audit logger. Those guards exist
// only so unit tests may build the services auditor-free; in a production
// composition root they must be unreachable. Like wireAuditServices, the
// shipped concrete types always satisfy this — the panic is unreachable
// insurance against a future composition-root regression that forgets an
// audit option.
func assertAuditWiring(services ...auditWiredService) {
	for _, ws := range services {
		w, ok := ws.svc.(interface{ AuditLoggerWired() bool })
		if !ok {
			panic("app: " + ws.name + " service must expose AuditLoggerWired() (fail-closed audit contract)")
		}
		if !w.AuditLoggerWired() {
			panic("app: " + ws.name + " service constructed without a non-nil audit logger: " +
				"its `if s.audit != nil` skip guards would silently drop audit facts in production (fail-closed audit contract)")
		}
	}
}

// buildAuditComplianceServices constructs the legal-hold, WORM-anchor, and
// cutover-facade services (P4-03/P4-07/P8-04) sharing the ledger, database and
// export storage. The cutover facade is the instance wired into the account
// handler's legacy-migration surface.
func buildAuditComplianceServices(gdb *gorm.DB, auditLedger service.AuditLedgerService,
	ledgerRepo repository.FullAuditLedgerRepository, store storage.FileStorage) (
	service.AuditHoldService, service.AuditAnchorService, service.LegacyCutoverService) {
	holdSvc := service.NewAuditHoldService(repository.NewAuditHoldRepository(gdb, ledgerRepo), auditLedger)
	return holdSvc, service.NewAuditAnchorService(ledgerRepo, store),
		service.NewLegacyCutoverService(repository.NewLegacyCutoverRepository(gdb, ledgerRepo),
			repository.NewLegacyCleanupRepository(gdb), auditLedger)
}

// assertComplianceAuditWiring fails fast at startup when the hold or cutover
// services were constructed without the audit service: their `if s.audit !=
// nil` skip guards would otherwise let hold/cutover mutations commit without
// their durable facts (REQ-4/5, pattern: assertAuditWiring).
func assertComplianceAuditWiring(holdSvc service.AuditHoldService, cutoverSvc, cutoverFacadeSvc service.LegacyCutoverService) {
	assertAuditWiring(
		auditWiredService{name: "audit_hold", svc: holdSvc},
		auditWiredService{name: "legacy_cutover", svc: cutoverSvc},
		auditWiredService{name: "legacy_cutover_facade", svc: cutoverFacadeSvc},
	)
}

// buildAuditLedgerHandler assembles the audit surface: ledger query, legal
// holds, WORM anchoring and async export.
func buildAuditLedgerHandler(auditLedger service.AuditLedgerService, holdSvc service.AuditHoldService,
	anchorSvc service.AuditAnchorService, syncJobSvc service.SyncJobService,
	permissionChecker middleware.PermissionChecker) *httpapi.AuditLedgerHandler {
	return httpapi.NewAuditLedgerHandlerWithExtensions(auditLedger, holdSvc, anchorSvc, syncJobSvc, permissionChecker)
}
