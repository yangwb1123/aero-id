package middleware

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/httpx"
	"github.com/aero/aero-id/internal/pkg/logger"
	"github.com/aero/aero-id/internal/service"
)

// TokenValidator is the SSO capability the auth middleware needs. *sso.Client
// satisfies it.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (string, error)
}

type IdentityTokenValidator interface {
	ValidateIdentity(ctx context.Context, token string) (*domain.TokenIdentity, error)
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
		identity, uid, err := validateIdentity(c.Request.Context(), v, token)
		if err != nil || uid == "" {
			httpx.WriteError(c, apperr.Unauthorized("auth.invalid_token", "invalid or expired token"))
			c.Abort()
			return
		}
		c.Set("actor_uid", uid)
		ctx := logger.WithActor(c.Request.Context(), uid)
		if identity != nil {
			ctx = service.WithIdentity(ctx, identity)
		} else {
			ctx = service.WithActor(ctx, uid)
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func validateIdentity(ctx context.Context, validator TokenValidator, token string) (*domain.TokenIdentity, string, error) {
	if identityValidator, ok := validator.(IdentityTokenValidator); ok {
		identity, err := identityValidator.ValidateIdentity(ctx, token)
		if identity == nil {
			return nil, "", err
		}
		return identity, identity.UID, err
	}
	uid, err := validator.ValidateToken(ctx, token)
	return nil, uid, err
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}
