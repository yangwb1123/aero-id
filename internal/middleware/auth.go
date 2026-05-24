package middleware

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/service"
)

// TokenValidator is the SSO capability the auth middleware needs. *sso.Client
// satisfies it.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (string, error)
}

// Auth extracts the Bearer token, validates it via SSO, and injects the resulting
// uid as the request actor (both on the gin context for logging and the request
// context for the service layer).
func Auth(v TokenValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			httpx.WriteError(c, apperr.Unauthorized("auth.missing_token", "missing bearer token"))
			c.Abort()
			return
		}
		uid, err := v.ValidateToken(c.Request.Context(), token)
		if err != nil {
			httpx.WriteError(c, apperr.Unauthorized("auth.invalid_token", "invalid or expired token"))
			c.Abort()
			return
		}
		c.Set("actor_uid", uid)
		c.Request = c.Request.WithContext(service.WithActor(c.Request.Context(), uid))
		c.Next()
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}
