package middleware

import (
	"context"
	stderrors "errors"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// ControlPlaneTokenValidator is the stronger authentication capability required
// by account, aggregation, and source-ingest HTTP routes. Legacy validators may
// intentionally return only a UID and must not satisfy this interface.
type ControlPlaneTokenValidator interface {
	TokenValidator
	ValidateControlPlaneIdentity(context.Context, string) (*domain.TokenIdentity, error)
}

// TokenInvalidator is an optional cache-eviction capability exposed by token
// validators that retain validation results. It is used only for business
// rejections; dependency outages must not evict otherwise usable credentials.
type TokenInvalidator interface {
	InvalidateToken(token string)
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
		if isNilValidator(v) {
			httpx.WriteError(c, apperr.Unauthorized("auth.invalid_token", "invalid or expired token"))
			c.Abort()
			return
		}
		identity, uid, err := validateIdentity(c.Request.Context(), v, token)
		if err != nil || uid == "" {
			InvalidateTokenOnValidationFailure(v, token, err)
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

// ControlPlaneAuth authenticates routes that require the complete, verified
// Snaplink identity. It deliberately does not fall back to UID-only validators.
func ControlPlaneAuth(v TokenValidator) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			httpx.WriteError(c, apperr.Unauthorized("auth.missing_token", "missing bearer token"))
			c.Abort()
			return
		}
		validator, ok := v.(ControlPlaneTokenValidator)
		if !ok || isNilValidator(v) || isNilValidator(validator) {
			httpx.WriteError(c, apperr.Unauthorized("auth.invalid_token", "invalid or expired token"))
			c.Abort()
			return
		}
		identity, err := validator.ValidateControlPlaneIdentity(c.Request.Context(), token)
		if err != nil || !completeIdentity(identity) {
			InvalidateTokenOnValidationFailure(v, token, err)
			httpx.WriteError(c, apperr.Unauthorized("auth.invalid_token", "invalid or expired token"))
			c.Abort()
			return
		}
		c.Set("actor_uid", identity.UID)
		ctx := logger.WithActor(c.Request.Context(), identity.UID)
		c.Request = c.Request.WithContext(service.WithIdentity(ctx, identity))
		c.Next()
	}
}

// IsNilTokenValidator reports nil and typed-nil validator interfaces without
// invoking their methods. Composition roots and both HTTP/gRPC auth paths use
// it to fail closed instead of turning a typed nil into a panic.
func IsNilTokenValidator(value any) bool {
	return isNilValidator(value)
}

func isNilValidator(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func completeIdentity(identity *domain.TokenIdentity) bool {
	return identity != nil && validIdentityField(identity.UID) &&
		validIdentityField(identity.Issuer) && validIdentityField(identity.Subject) &&
		validIdentityField(identity.TenantID) && validIdentityField(identity.ClientID)
}

func validIdentityField(value string) bool {
	return utf8.ValidString(value) && len(value) <= 256 && value == strings.TrimSpace(value) &&
		value != "" && !strings.ContainsFunc(value, unicode.IsControl)
}

// InvalidateTokenOnValidationFailure evicts a cached token only when the
// validator produced a non-availability rejection. Context cancellation and
// upstream unavailable/deadline errors are transient and retain the cache.
func InvalidateTokenOnValidationFailure(v TokenValidator, token string, err error) {
	if err == nil || !shouldInvalidateToken(err) {
		return
	}
	if invalidator, ok := v.(TokenInvalidator); ok && !isNilValidator(invalidator) {
		invalidator.InvalidateToken(token)
	}
}

func shouldInvalidateToken(err error) bool {
	if stderrors.Is(err, context.Canceled) || stderrors.Is(err, context.DeadlineExceeded) {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return false
	default:
		return true
	}
}

func validateIdentity(ctx context.Context, validator TokenValidator, token string) (*domain.TokenIdentity, string, error) {
	if isNilValidator(validator) {
		return nil, "", stderrors.New("token validator is unavailable")
	}
	if identityValidator, ok := validator.(IdentityTokenValidator); ok && !isNilValidator(identityValidator) {
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
