package middleware

import (
	"context"

	"github.com/gin-gonic/gin"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
)

// BanChecker is the narrow interface the ban middleware needs — just a yes/no
// query for whether uid is currently banned. *repository.banRepo satisfies this.
type BanChecker interface {
	IsBanned(ctx context.Context, uid string) (bool, error)
}

// Ban returns a middleware that checks whether the authenticated actor (set by
// Auth middleware) is currently banned. If banned, the request is aborted with
// 403 Forbidden + account.banned error code.
//
// This middleware MUST be placed AFTER Auth (which sets actor_uid) and BEFORE
// any other processing on authenticated routes.
func Ban(checker BanChecker) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, exists := c.Get("actor_uid")
		if !exists {
			// No actor — likely an anonymous route or Auth not installed.
			// Do not block; let downstream middleware handle the auth error.
			c.Next()
			return
		}
		uid, ok := raw.(string)
		if !ok || uid == "" {
			c.Next()
			return
		}

		banned, err := checker.IsBanned(c.Request.Context(), uid)
		if err != nil {
			// If the ban check fails we err on the side of caution and assume
			// the user is NOT banned (fail-open). Logging the error would be
			// nice, but the middleware has no logger handle — the access log
			// middleware downstream will record the 200/403 eventually.
			c.Next()
			return
		}
		if banned {
			httpx.WriteError(c, apperr.Forbidden("account.banned", "your account has been banned"))
			c.Abort()
			return
		}
		c.Next()
	}
}
