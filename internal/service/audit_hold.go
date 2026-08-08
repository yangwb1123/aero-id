package service

import (
	"context"
	"errors"
	"strings"
	"time"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

// AuditHoldService manages evidence-preservation orders (P4-07). Holds are
// durable, idempotent (upsert), auditable, and exempt the affected actors from
// the legacy retention purge.
type AuditHoldService interface {
	// List returns active holds for the tenant.
	List(ctx context.Context, tenantID string) ([]*repository.AuditHold, error)
	// Put creates or refreshes a hold.
	Put(ctx context.Context, tenantID string, scopeType repository.AuditHoldScope, scopeID, reason string, expiresAt *time.Time) (*repository.AuditHold, error)
	// Remove lifts a hold.
	Remove(ctx context.Context, tenantID string, scopeType repository.AuditHoldScope, scopeID string) error
	// ActiveScopeIDs returns the held scope ids of one kind (cleaner hook).
	ActiveScopeIDs(ctx context.Context, scopeType repository.AuditHoldScope) ([]string, error)
}

// errAuditHoldAuditUnavailable is the sentinel for a hold repository without
// the WithAudit surface: with a non-nil audit logger the hold must never
// commit without its durable fact, so the write is refused before it starts
// (fail-fast, mirroring registerWithAudit's callback.audit_unavailable).
var errAuditHoldAuditUnavailable = errors.New("audit hold audit persistence is unavailable")

type auditHoldService struct {
	holds repository.AuditHoldRepository
	audit AuditLedgerService
}

func NewAuditHoldService(holds repository.AuditHoldRepository, audit AuditLedgerService) AuditHoldService {
	return &auditHoldService{holds: holds, audit: audit}
}

func (s *auditHoldService) List(ctx context.Context, tenantID string) ([]*repository.AuditHold, error) {
	holds, err := s.holds.ListActiveHolds(ctx, tenantID)
	if err != nil {
		return nil, apperr.Internal("audit_hold.list_failed", "failed to list legal holds").Wrap(err)
	}
	return holds, nil
}

func (s *auditHoldService) Put(ctx context.Context, tenantID string, scopeType repository.AuditHoldScope,
	scopeID, reason string, expiresAt *time.Time) (*repository.AuditHold, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(scopeID) == "" {
		return nil, apperr.BadRequest("audit_hold.invalid_input", "tenant_id and scope_id are required")
	}
	if !validHoldScope(scopeType) {
		return nil, apperr.BadRequest("audit_hold.invalid_scope", "scope_type must be tenant, account or actor")
	}
	actor := actorFromContext(ctx)
	hold := &repository.AuditHold{TenantID: tenantID, ScopeType: scopeType, ScopeID: strings.TrimSpace(scopeID),
		Reason: reason, CreatedBy: actor, ExpiresAt: expiresAt}
	input := auditHoldSetFactInput(ctx, tenantID, scopeType, hold.ScopeID, reason, actor, expiresAt)
	if err := s.putWithAudit(ctx, hold, input); err != nil {
		if errors.Is(err, errAuditHoldAuditUnavailable) {
			// Fail-fast (R-3): a repository without the WithAudit surface is a
			// wiring error — the plain write is refused so no hold can ever
			// commit without its durable fact (pattern: registerWithAudit).
			return nil, apperr.Internal("audit_hold.audit_unavailable", "audit hold persistence is unavailable")
		}
		// With the WithAudit surface this is an ATOMIC failure: the ledger
		// append rolled the whole transaction back, so neither the hold nor
		// its fact committed. save_failed is truthful and fail-closed — a
		// keyless or down ledger never lets the hold commit without its
		// durable fact (sync createWithAudit precedent). It must NOT be read
		// as "audit skipped, mutation committed".
		return nil, apperr.Internal("audit_hold.save_failed", "failed to record the legal hold").Wrap(err)
	}
	return hold, nil
}

// putWithAudit performs the hold write with its audit fact in one transaction
// when the repository exposes the WithAudit surface (production). With a
// non-nil audit logger and no WithAudit surface the write fails fast BEFORE
// the plain write (pattern: registerWithAudit) — the post-commit fallback
// tail is gone, so no hold can commit without its durable fact. The 0 =
// disabled convention applies only when no audit logger is configured
// (unit-test fakes).
func (s *auditHoldService) putWithAudit(ctx context.Context, hold *repository.AuditHold, input AuditFactInput) error {
	if s.audit == nil {
		return s.holds.PutHold(ctx, hold) // 0 = disabled convention
	}
	repo, ok := s.holds.(repository.AuditHoldAuditRepository)
	if !ok {
		return errAuditHoldAuditUnavailable
	}
	return repo.PutHoldWithAudit(ctx, hold, buildAuditFact(ctx, input))
}

func (s *auditHoldService) Remove(ctx context.Context, tenantID string, scopeType repository.AuditHoldScope, scopeID string) error {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return apperr.BadRequest("audit_hold.invalid_input", "scope_id is required")
	}
	input := auditHoldRemovedFactInput(ctx, tenantID, scopeType, scopeID)
	if err := s.removeWithAudit(ctx, tenantID, scopeType, scopeID, input); err != nil {
		if errors.Is(err, errAuditHoldAuditUnavailable) {
			// Fail-fast (R-3): see putWithAudit.
			return apperr.Internal("audit_hold.audit_unavailable", "audit hold persistence is unavailable")
		}
		// Atomic failure: the ledger append rolled the delete back, so no
		// hold was lifted without its durable fact (remove_failed is
		// truthful, never fail-open).
		return apperr.Internal("audit_hold.remove_failed", "failed to lift the legal hold").Wrap(err)
	}
	return nil
}

// removeWithAudit mirrors putWithAudit for the lift path.
func (s *auditHoldService) removeWithAudit(ctx context.Context, tenantID string, scopeType repository.AuditHoldScope,
	scopeID string, input AuditFactInput) error {
	if s.audit == nil {
		return s.holds.RemoveHold(ctx, tenantID, scopeType, scopeID) // 0 = disabled convention
	}
	repo, ok := s.holds.(repository.AuditHoldAuditRepository)
	if !ok {
		return errAuditHoldAuditUnavailable
	}
	return repo.RemoveHoldWithAudit(ctx, tenantID, scopeType, scopeID, buildAuditFact(ctx, input))
}

func (s *auditHoldService) ActiveScopeIDs(ctx context.Context, scopeType repository.AuditHoldScope) ([]string, error) {
	ids, err := s.holds.HoldScopeIDs(ctx, scopeType)
	if err != nil {
		return nil, apperr.Internal("audit_hold.list_failed", "failed to read legal holds").Wrap(err)
	}
	return ids, nil
}

func validHoldScope(scope repository.AuditHoldScope) bool {
	switch scope {
	case repository.AuditHoldScopeTenant, repository.AuditHoldScopeAccount, repository.AuditHoldScopeActor:
		return true
	default:
		return false
	}
}

// auditHoldSetFactInput builds the anchored fact input for a hold put.
// Anchored event_id (contract §2.3 / D-5): the parts are the
// post-normalization values that land in the persisted fact — the
// context-bound tenant (bindAuditContext may override the argument), the
// trimmed scope_id stored in audit_holds.scope_id (R3), the raw reason, and
// the canonical expiry rendering (R7). A crash-retry re-derives the same
// event_id and the ledger dedup swallows it. Every input is known before the
// transaction starts (the RETURNING copy-back never feeds the fact), so the
// in-tx append is byte-identical to the post-commit Record it replaces.
func auditHoldSetFactInput(ctx context.Context, tenantID string, scopeType repository.AuditHoldScope,
	scopeID, reason string, actor string, expiresAt *time.Time) AuditFactInput {
	tenant := tenantID
	if fromCtx := TenantFrom(ctx); fromCtx != "" {
		tenant = fromCtx
	}
	return AuditFactInput{Source: "aero-id",
		EventID:      idgen.Anchor("audit_hold.set", tenant, string(scopeType), scopeID, reason, idgen.RenderAnchorTime(expiresAt)),
		ActorUID:     actor,
		TenantID:     tenant,
		Action:       "audit_hold.set",
		ResourceType: string(scopeType),
		ResourceID:   scopeID,
		Payload:      map[string]any{"reason": reason, "expires_at": expiresAt}}
}

// auditHoldRemovedFactInput builds the anchored fact input for a hold lift.
// Anchored event_id (contract §2.3 / D-5): fixed 4-part shape
// [audit_hold.removed, tenant, scope_type, scope_id-trimmed]. The action
// discriminator keeps put("", nil) (6-part) from ever colliding with removed
// (G9b ≠ G10).
func auditHoldRemovedFactInput(ctx context.Context, tenantID string, scopeType repository.AuditHoldScope, scopeID string) AuditFactInput {
	tenant := tenantID
	if fromCtx := TenantFrom(ctx); fromCtx != "" {
		tenant = fromCtx
	}
	return AuditFactInput{Source: "aero-id",
		EventID:      idgen.Anchor("audit_hold.removed", tenant, string(scopeType), scopeID),
		ActorUID:     actorFromContext(ctx),
		TenantID:     tenant,
		Action:       "audit_hold.removed",
		ResourceType: string(scopeType),
		ResourceID:   scopeID}
}
