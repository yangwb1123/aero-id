package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// LegacyCutoverService drives the per-tenant legacy cutover registry (P8-04)
// and the cleanup-readiness checklist (P8-05). Default is 'legacy': the
// compatibility surface keeps serving until an admin explicitly flips a tenant.
type LegacyCutoverService interface {
	// Get returns the tenant's status; tenants with no decision are 'legacy'.
	Get(ctx context.Context, tenantID string) (domain.LegacyCutoverStatus, error)
	// Set applies a validated transition (shadow -> cut_over -> rolled_back).
	Set(ctx context.Context, tenantID string, status domain.LegacyCutoverStatus, note string) (*repository.LegacyCutover, error)
	// CleanupReadiness returns the P8-05 checklist for removing the legacy
	// write path. Read-only; never deletes.
	CleanupReadiness(ctx context.Context, tenantID string) ([]repository.CleanupReadinessItem, bool, error)
	// IsCutOver implements the facade guard contract (P8-04): true when the
	// tenant's legacy surface must answer "moved". Checker outages report an
	// error and the guard fails open.
	IsCutOver(ctx context.Context, tenantID string) (bool, error)
}

type legacyCutoverService struct {
	cutover        repository.LegacyCutoverRepository
	cleanup        repository.LegacyCleanupRepository
	audit          AuditLedgerService
	actor          func(ctx context.Context) string
	rollbackWindow time.Duration
}

func NewLegacyCutoverService(cutover repository.LegacyCutoverRepository,
	cleanup repository.LegacyCleanupRepository, audit AuditLedgerService) LegacyCutoverService {
	return &legacyCutoverService{cutover: cutover, cleanup: cleanup, audit: audit,
		actor: func(ctx context.Context) string {
			actor, ok := ActorFrom(ctx)
			if !ok {
				return ""
			}
			return actor
		}, rollbackWindow: 30 * 24 * time.Hour}
}

func (s *legacyCutoverService) IsCutOver(ctx context.Context, tenantID string) (bool, error) {
	status, err := s.Get(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return status == domain.LegacyCutoverStatusCutOver, nil
}

func (s *legacyCutoverService) Get(ctx context.Context, tenantID string) (domain.LegacyCutoverStatus, error) {
	entry, err := s.cutover.GetCutover(ctx, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return domain.LegacyCutoverStatusLegacy, nil
		}
		return "", apperr.Internal("cutover.lookup_failed", "failed to read the cutover registry").Wrap(err)
	}
	return entry.Status, nil
}

func (s *legacyCutoverService) Set(ctx context.Context, tenantID string,
	status domain.LegacyCutoverStatus, note string) (*repository.LegacyCutover, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, apperr.BadRequest("cutover.tenant_required", "tenant_id is required")
	}
	current, err := s.Get(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if !domain.ValidCutoverTransition(current, status) {
		return nil, apperr.Conflict("cutover.invalid_transition",
			"cannot move tenant from "+string(current)+" to "+string(status))
	}
	entry := &repository.LegacyCutover{TenantID: tenantID, Status: status, DecidedBy: s.actor(ctx), Note: note}
	input := cutoverSetFactInput(ctx, tenantID, status, current, entry.DecidedBy, note)
	atomicAudit, err := s.setWithAudit(ctx, entry, current, input)
	if err != nil {
		if errors.Is(err, repository.ErrInvalidCutoverTransition) {
			return nil, apperr.Conflict("cutover.concurrent_transition",
				"cutover status changed concurrently; reload before retrying").Wrap(err)
		}
		// With the WithAudit surface this is an ATOMIC failure: the ledger
		// append rolled the whole decision back, so neither the cutover nor
		// its fact committed. save_failed is truthful and fail-closed — a
		// keyless or down ledger never lets a decision commit without its
		// durable fact (sync createWithAudit precedent). It must NOT be read
		// as "audit skipped, mutation committed".
		return nil, apperr.Internal("cutover.save_failed", "failed to record the cutover decision").Wrap(err)
	}
	if s.audit != nil && !atomicAudit {
		// audit-site: with-audit-fallback (sibling SetCutoverWithAudit) —
		// fallback tail reachable only for repositories without the WithAudit
		// surface (unit-test fakes). The decision committed and the fact is
		// appended now via the same pipeline; a failure surfaces
		// cutover.audit_failed and is never swallowed. Concrete repos always
		// take the atomic path, so this tail is unreachable in the
		// composition root.
		if err := s.audit.Record(ctx, input); err != nil {
			return nil, apperr.Internal("cutover.audit_failed", "failed to record the cutover audit").Wrap(err)
		}
	}
	return entry, nil
}

// setWithAudit performs the cutover write with its audit fact: atomically
// when the repository exposes the WithAudit surface (production), otherwise as
// a plain write whose fact the caller's post-commit Record fallback appends.
// It returns atomicAudit=true when the fact was appended in the same
// transaction as the decision write.
func (s *legacyCutoverService) setWithAudit(ctx context.Context, entry *repository.LegacyCutover,
	expected domain.LegacyCutoverStatus, input AuditFactInput) (bool, error) {
	if s.audit == nil {
		return false, s.cutover.SetCutover(ctx, entry) // 0 = disabled convention
	}
	if repo, ok := s.cutover.(repository.LegacyCutoverCASAuditRepository); ok {
		return true, repo.CompareAndSetCutoverWithAudit(ctx, entry, expected, buildAuditFact(ctx, input))
	}
	repo, ok := s.cutover.(repository.LegacyCutoverAuditRepository)
	if !ok {
		// Non-audit repository (test fake): the fact is appended by the
		// caller's checked Record fallback — never silently dropped.
		return false, s.cutover.SetCutover(ctx, entry)
	}
	return true, repo.SetCutoverWithAudit(ctx, entry, buildAuditFact(ctx, input))
}

// cutoverSetFactInput builds the anchored fact input for a cutover decision.
// Anchored event_id (contract §2.3 / D-4): [legacy.cutover.set, tenant,
// status, previous]. previous is the status read by Get before the
// transition, so cycle returns (Legacy→…→RolledBack→Legacy) and same-status
// re-assertions record distinct facts (G11≠G12≠G13) while racing duplicate
// executions that read the same previous dedup exactly. Every input is known
// before the transaction starts, so the in-tx append is byte-identical to the
// post-commit Record it replaces.
func cutoverSetFactInput(ctx context.Context, tenantID string, status, previous domain.LegacyCutoverStatus,
	decidedBy, note string) AuditFactInput {
	tenant := tenantID
	if fromCtx := TenantFrom(ctx); fromCtx != "" {
		tenant = fromCtx
	}
	return AuditFactInput{Source: "aero-id",
		EventID:      idgen.Anchor("legacy.cutover.set", tenant, string(status), string(previous)),
		ActorUID:     decidedBy,
		TenantID:     tenant,
		Action:       "legacy.cutover.set",
		ResourceType: "tenant",
		ResourceID:   tenantID,
		Payload:      map[string]any{"status": string(status), "previous": string(previous), "note": note}}
}

func (s *legacyCutoverService) CleanupReadiness(ctx context.Context, tenantID string) ([]repository.CleanupReadinessItem, bool, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, false, apperr.BadRequest("cutover.tenant_required", "tenant_id is required")
	}
	items, err := s.cleanup.CleanupReadiness(ctx, tenantID)
	if err != nil {
		return nil, false, apperr.Internal("cutover.readiness_failed", "failed to evaluate cleanup readiness").Wrap(err)
	}
	// Evaluate the rollback-window item against the service policy (P8-05). If
	// the repository did not include the item (fakes or older deployments),
	// append the evaluated decision so the checklist is always complete.
	windowReady, windowDetails := s.evaluateRollbackWindow(ctx, tenantID)
	foundWindow := false
	ready := true
	for i := range items {
		if items[i].Check == "rollback_window_elapsed" {
			foundWindow = true
			items[i].Ready = windowReady
			items[i].Details = windowDetails
		}
		if !items[i].Ready {
			ready = false
		}
	}
	if !foundWindow {
		items = append(items, repository.CleanupReadinessItem{
			Check: "rollback_window_elapsed", Ready: windowReady, Details: windowDetails,
		})
	}
	if !windowReady {
		ready = false
	}
	return items, ready, nil
}

// evaluateRollbackWindow reports whether the tenant's cut-over decision is old
// enough that the rollback window has elapsed. Tenants without a cut-over
// decision never satisfy the window (they have nothing to roll back from).
func (s *legacyCutoverService) evaluateRollbackWindow(ctx context.Context, tenantID string) (bool, string) {
	entry, getErr := s.cutover.GetCutover(ctx, tenantID)
	if getErr == nil && entry != nil && entry.Status == domain.LegacyCutoverStatusCutOver {
		elapsed := time.Since(entry.DecidedAt)
		return elapsed >= s.rollbackWindow,
			"cut_over " + elapsed.Round(time.Hour).String() + " ago; window " + s.rollbackWindow.String()
	}
	return false, "no cutover decision recorded for tenant"
}
