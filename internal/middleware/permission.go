package middleware

import (
	"context"

	"github.com/gin-gonic/gin"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

// PermissionChecker checks whether a uid holds a permission.
type PermissionChecker interface {
	HasPermission(ctx context.Context, uid, perm string) (bool, error)
}

// RequirePermission returns middleware that checks the authenticated actor has
// the given permission. It MUST run AFTER Auth middleware so the actor_uid is
// present on the context.
func RequirePermission(perm string, checker PermissionChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		uid, exists := c.Get("actor_uid")
		if !exists || uid == "" {
			httpx.WriteError(c, apperr.Unauthorized("auth.unauthorized", "missing authenticated actor"))
			c.Abort()
			return
		}
		uidStr, _ := uid.(string)
		ok, err := checker.HasPermission(c.Request.Context(), uidStr, perm)
		if err != nil {
			httpx.WriteError(c, apperr.Internal("auth.permission_check_failed", "failed to verify permission"))
			c.Abort()
			return
		}
		if !ok {
			httpx.WriteError(c, apperr.Forbidden("auth.forbidden", "insufficient permissions"))
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequirePermissionIfConfigured keeps feature registration concise while
// preserving the legacy development mode where no local permission checker is
// wired. Production account/admin deployments should always provide one.
func RequirePermissionIfConfigured(perm string, checker PermissionChecker) gin.HandlerFunc {
	if checker == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return RequirePermission(perm, checker)
}
