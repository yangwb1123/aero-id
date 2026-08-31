package app

import (
	"fmt"
	"reflect"
	"strings"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/sso"
)

const controlPlaneAuthenticationError = "control-plane HTTP routes require claims-capable authentication:\n" +
	"validator must verify issuer, subject, tenant, and client_id;\n" +
	"configure sso.jwks_url, sso.issuer, and sso.audience"

// validateControlPlaneAuthentication is a composition-root guard. Legacy-only
// deployments may omit the control-plane registrars, but the standard HTTP
// surface must never start with UID-only or fallback authentication.
func validateControlPlaneAuthentication(cfg *config.Config, validator grpcsrv.TokenValidator, routesRegistered bool) error {
	if !routesRegistered {
		return nil
	}
	if validator == nil {
		return fmt.Errorf("%s", controlPlaneAuthenticationError)
	}
	capability, ok := validator.(middleware.ControlPlaneTokenValidator)
	if !ok || isNilAuthenticationCapability(capability) {
		return fmt.Errorf("%s", controlPlaneAuthenticationError)
	}
	if cfg == nil || strings.TrimSpace(cfg.SSO.JWKSURL) == "" ||
		strings.TrimSpace(cfg.SSO.Issuer) == "" || strings.TrimSpace(cfg.SSO.Audience) == "" {
		return fmt.Errorf("%s", controlPlaneAuthenticationError)
	}
	return nil
}

func isNilAuthenticationCapability(value any) bool {
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

func buildTokenValidator(cfg *config.Config, upstream *sso.Client, log *zap.Logger) (grpcsrv.TokenValidator, error) {
	if cfg.SSO.JWKSURL == "" {
		return upstream, nil
	}
	validator, err := sso.NewJWKSValidator(sso.JWKSConfig{
		URL: cfg.SSO.JWKSURL, Issuer: cfg.SSO.Issuer, Audience: cfg.SSO.Audience,
		CacheTTL: cfg.SSO.JWKSCacheTTL, MaxBodyBytes: cfg.SSO.JWKSMaxBodyBytes, RequireTenant: true,
	})
	if err != nil {
		return nil, fmt.Errorf("jwks validator: %w", err)
	}
	log.Info("snaplink local jwks token verification enabled", zap.String("jwks_url", cfg.SSO.JWKSURL))
	return validator, nil
}
