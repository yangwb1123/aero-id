package service

import (
	"context"
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
	if err := s.holds.PutHold(ctx, hold); err != nil {
		return nil, apperr.Internal("audit_hold.save_failed", "failed to record the legal hold").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, AuditFactInput{Source: "aero-id", EventID: idgen.NewID(), ActorUID: actor,
			TenantID: tenantID, Action: "audit_hold.set", ResourceType: string(scopeType), ResourceID: scopeID,
			Payload: map[string]any{"reason": reason, "expires_at": expiresAt}})
	}
	return hold, nil
}

func (s *auditHoldService) Remove(ctx context.Context, tenantID string, scopeType repository.AuditHoldScope, scopeID string) error {
	if strings.TrimSpace(scopeID) == "" {
		return apperr.BadRequest("audit_hold.invalid_input", "scope_id is required")
	}
	if err := s.holds.RemoveHold(ctx, tenantID, scopeType, scopeID); err != nil {
		return apperr.Internal("audit_hold.remove_failed", "failed to lift the legal hold").Wrap(err)
	}
	if s.audit != nil {
		_ = s.audit.Record(ctx, AuditFactInput{Source: "aero-id", EventID: idgen.NewID(),
			ActorUID: actorFromContext(ctx), TenantID: tenantID, Action: "audit_hold.removed",
			ResourceType: string(scopeType), ResourceID: scopeID})
	}
	return nil
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


