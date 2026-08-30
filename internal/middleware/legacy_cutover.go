package middleware

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/service"
)

// CutoverChecker answers whether a tenant's legacy compatibility surface has
// been cut over to the new authority (P8-04).
type CutoverChecker interface {
	IsCutOver(ctx context.Context, tenantID string) (bool, error)
}

// LegacyCutoverGuard answers explicitly for cut-over tenants on the legacy
// surface: HTTP 410 with a stable code instead of serving stale data or
// failing generically. Tenants without a decision keep serving (default
// 'legacy'); a checker outage fails open on this read path (the registry is an
// admin marker, not an authorization control).
//
// Usage:
//
//	authMW = append(authMW, middleware.LegacyCutoverGuard(checker))
func LegacyCutoverGuard(checker CutoverChecker) gin.HandlerFunc {
	if checker == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if !isLegacyPath(c.FullPath()) {
			c.Next()
			return
		}
		tenantID := tenantFromRequest(c)
		if tenantID == "" {
			c.Next()
			return
		}
		cutOver, err := checker.IsCutOver(c.Request.Context(), tenantID)
		if err != nil || !cutOver {
			c.Next()
			return
		}
		c.Header("Link", `</docs/plans/2026-08-04-distributed-account-aggregation.md>; rel="deprecation"`)
		c.AbortWithStatusJSON(http.StatusGone, gin.H{
			"error": gin.H{
				"code":       "legacy.cut_over",
				"message":    "this tenant's legacy API has moved to the new authority",
				"request_id": c.GetString("request_id"),
			},
		})
	}
}

// tenantFromRequest extracts the verified tenant claim if present.
func tenantFromRequest(c *gin.Context) string {
	identity, ok := service.IdentityFrom(c.Request.Context())
	if !ok || identity == nil {
		return ""
	}
	return identity.TenantID
}
