package service

import (
	"context"
	"errors"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/repository"
	"gorm.io/gorm"
)

// authorizeAccountTarget protects service and gRPC callers that can provide an
// arbitrary account_id. HTTP route middleware is useful at the edge, but it
// cannot establish that the target account is owned by the authenticated actor.
// The source service still rechecks resource ACLs after this control-plane check.
//
//nolint:cyclop // target authorization combines actor, tenant, ownership, and delegated permission checks.
func authorizeAccountTarget(ctx context.Context, accounts repository.AccountRepository, checker PermissionChecker,
	accountID domain.ID, actor, permission string) error {
	if accountID == (domain.ID{}) || accounts == nil {
		return nil
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return err
	}
	account, err := accounts.GetByID(ctx, tenantID, accountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("account.not_found", "account not found")
		}
		return apperr.Internal("account.lookup_failed", "failed to resolve target account").Wrap(err)
	}
	if account.Status == domain.AccountErased {
		return apperr.Forbidden("account.erased", "account is erased")
	}
	if account.CanonicalUID == actor {
		return nil
	}
	if checker == nil {
		return apperr.Forbidden("account.permission_unavailable", "target account permission is not configured")
	}
	ok, err := checker.HasPermission(ctx, actor, permission)
	if err != nil {
		return apperr.Internal("account.permission_check_failed", "failed to verify target account permission").Wrap(err)
	}
	if !ok {
		return apperr.Forbidden("account.forbidden", "insufficient target account permission")
	}
	return nil
}

func requireHomeRegion(ctx context.Context, accounts repository.AccountRepository, accountID domain.ID, local string) error {
	if accountID == (domain.ID{}) || accounts == nil || local == "" {
		return nil
	}
	tenantID, err := mustTenant(ctx)
	if err != nil {
		return err
	}
	account, err := accounts.GetByID(ctx, tenantID, accountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("account.not_found", "account not found")
		}
		return apperr.Internal("account.lookup_failed", "failed to resolve target account").Wrap(err)
	}
	if account.HomeRegion != local {
		return apperr.Conflict("account.home_region_required", "account write must be routed to its home region")
	}
	return nil
}
