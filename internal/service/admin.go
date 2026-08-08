package service

import (
	"context"
	stderrors "errors"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
)

type adminService struct {
	adminRepo repository.AdminRepository
	audit     AuditLogger // nil => audit inactive
	ledger    AuditLedgerService
}

func NewAdminService(adminRepo repository.AdminRepository, opts ...AdminOption) AdminService {
	s := &adminService{adminRepo: adminRepo}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// AdminOption configures optional adminService behaviour.
type AdminOption func(*adminService)

// WithAdminAuditLogger wires an AuditLogger into the admin service.
func WithAdminAuditLogger(a AuditLogger) AdminOption {
	return func(s *adminService) { s.audit = a }
}

func WithAdminAuditLedger(ledger AuditLedgerService) AdminOption {
	return func(s *adminService) { s.ledger = ledger }
}

func (s *adminService) HasPermission(ctx context.Context, uid, perm string) (bool, error) {
	if uid == "" {
		return false, nil
	}
	ok, err := s.adminRepo.HasPermission(ctx, uid, perm)
	if err != nil {
		return false, apperr.Internal("admin.permission_check_failed", "failed to verify permission").Wrap(err)
	}
	return ok, nil
}

func (s *adminService) ListAudit(ctx context.Context, action string, page domain.Page) ([]*domain.AuditEvent, string, error) {
	if s.ledger == nil {
		return nil, "", apperr.Unavailable("admin.audit_unavailable", "tenant audit ledger is unavailable")
	}
	if _, err := mustTenant(ctx); err != nil {
		return nil, "", err
	}
	facts, next, err := s.ledger.List(ctx, domain.AuditFactFilter{Action: action, Page: page})
	if err != nil {
		return nil, "", apperr.Internal("admin.audit_list_failed", "failed to list audit events").Wrap(err)
	}
	events := make([]*domain.AuditEvent, len(facts))
	for i, fact := range facts {
		events[i] = auditEventFromFact(fact)
	}
	return events, next, nil
}

func auditEventFromFact(fact *domain.AuditFact) *domain.AuditEvent {
	return &domain.AuditEvent{ID: fact.ID, ActorUID: fact.ActorUID, Action: fact.Action,
		SubjectType: fact.ResourceType, SubjectID: fact.ResourceID, CreatedAt: fact.OccurredAt}
}

func (s *adminService) GrantRole(ctx context.Context, uid, roleCode string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if uid == "" {
		return apperr.BadRequest("admin.invalid_uid", "uid must not be blank")
	}
	if roleCode == "" {
		return apperr.BadRequest("admin.invalid_role", "role code must not be blank")
	}
	if uid == actor {
		return apperr.BadRequest("admin.self_grant", "cannot grant a role to yourself")
	}
	if s.audit != nil {
		repo, ok := s.adminRepo.(repository.AdminAuditRepository)
		if !ok {
			return apperr.Internal("admin.audit_unavailable", "admin audit persistence is unavailable")
		}
		if err := repo.GrantRoleWithAudit(ctx, uid, roleCode, actor,
			buildLegacyAuditFact(ctx, actor, domain.AuditAdminGrantRole, "user", uid, uid,
				map[string]any{"role": roleCode}, domain.ID{})); err != nil {
			return apperr.Internal("admin.grant_role_failed", "failed to grant role").Wrap(err)
		}
		return nil
	}
	if err := s.adminRepo.GrantRole(ctx, uid, roleCode, actor); err != nil {
		if stderrors.Is(err, repository.ErrNotFound) {
			return apperr.NotFound("admin.role_not_found", "role not found")
		}
		return apperr.Internal("admin.grant_role_failed", "failed to grant role").Wrap(err)
	}
	return nil
}

func (s *adminService) RevokeRole(ctx context.Context, uid, roleCode string) error {
	actor, err := mustActor(ctx)
	if err != nil {
		return err
	}
	if uid == "" {
		return apperr.BadRequest("admin.invalid_uid", "uid must not be blank")
	}
	if roleCode == "" {
		return apperr.BadRequest("admin.invalid_role", "role code must not be blank")
	}
	if s.audit != nil {
		repo, ok := s.adminRepo.(repository.AdminAuditRepository)
		if !ok {
			return apperr.Internal("admin.audit_unavailable", "admin audit persistence is unavailable")
		}
		if err := repo.RevokeRoleWithAudit(ctx, uid, roleCode,
			buildLegacyAuditFact(ctx, actor, domain.AuditAdminRevokeRole, "user", uid, uid,
				map[string]any{"role": roleCode}, domain.ID{})); err != nil {
			return apperr.Internal("admin.revoke_role_failed", "failed to revoke role").Wrap(err)
		}
		return nil
	}
	if err := s.adminRepo.RevokeRole(ctx, uid, roleCode); err != nil {
		return apperr.Internal("admin.revoke_role_failed", "failed to revoke role").Wrap(err)
	}
	return nil
}

func (s *adminService) ListRoles(ctx context.Context) ([]*domain.Role, error) {
	if _, err := mustActor(ctx); err != nil {
		return nil, err
	}
	roles, err := s.adminRepo.ListRoles(ctx)
	if err != nil {
		return nil, apperr.Internal("admin.list_roles_failed", "failed to list roles").Wrap(err)
	}
	return roles, nil
}

func (s *adminService) ListUserRoles(ctx context.Context, uid string) ([]*domain.UserRole, error) {
	if _, err := mustActor(ctx); err != nil {
		return nil, err
	}
	if uid == "" {
		return nil, apperr.BadRequest("admin.invalid_uid", "uid must not be blank")
	}
	roles, err := s.adminRepo.ListUserRoles(ctx, uid)
	if err != nil {
		return nil, apperr.Internal("admin.list_user_roles_failed", "failed to list user roles").Wrap(err)
	}
	return roles, nil
}
