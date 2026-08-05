package service

import (
	"context"
	"errors"
)

// DelegatingPermissionChecker keeps Snaplink as the authorization authority.
// The local checker is only a deliberate compatibility fallback while older
// Snaplink deployments do not expose Authorizer.Check.
type DelegatingPermissionChecker struct {
	remote   PermissionChecker
	local    PermissionChecker
	fallback bool
}

type PermissionChecker interface {
	HasPermission(context.Context, string, string) (bool, error)
}

func NewDelegatingPermissionChecker(remote, local PermissionChecker, legacyFallback bool) PermissionChecker {
	return &DelegatingPermissionChecker{remote: remote, local: local, fallback: legacyFallback}
}

func (c *DelegatingPermissionChecker) HasPermission(ctx context.Context, uid, permission string) (bool, error) {
	if c.remote != nil {
		allowed, err := c.remote.HasPermission(ctx, uid, permission)
		if err == nil || !c.fallback {
			return allowed, err
		}
	}
	if c.local == nil {
		return false, errors.New("permission checker is not configured")
	}
	return c.local.HasPermission(ctx, uid, permission)
}

var _ PermissionChecker = (*DelegatingPermissionChecker)(nil)
