package app

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/config"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
	"github.com/aero/aero-id/internal/sso"
)

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
