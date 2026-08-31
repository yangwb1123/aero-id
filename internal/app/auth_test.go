package app

import (
	"context"
	"testing"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/domain"
	grpcsrv "github.com/aero/aero-id/internal/handler/grpc"
)

type appTokenValidator struct{}

func (appTokenValidator) ValidateToken(context.Context, string) (string, error) { return "u1", nil }

type appClaimsValidator struct{}

func (appClaimsValidator) ValidateToken(context.Context, string) (string, error) { return "u1", nil }
func (appClaimsValidator) ValidateControlPlaneIdentity(context.Context, string) (*domain.TokenIdentity, error) {
	return &domain.TokenIdentity{UID: "u1", Issuer: "issuer", Subject: "u1", TenantID: "tenant", ClientID: "client"}, nil
}

var _ grpcsrv.TokenValidator = appTokenValidator{}
var _ grpcsrv.TokenValidator = appClaimsValidator{}

func TestValidateControlPlaneAuthentication(t *testing.T) {
	validConfig := &config.Config{}
	validConfig.SSO.JWKSURL = "https://snaplink.example.test/jwks"
	validConfig.SSO.Issuer = "issuer"
	validConfig.SSO.Audience = "aero-id"

	tests := []struct {
		name      string
		cfg       *config.Config
		validator grpcsrv.TokenValidator
		routes    bool
		wantError bool
	}{
		{name: "legacy only permits uid validator", cfg: &config.Config{}, validator: appTokenValidator{}, routes: false},
		{name: "nil validator", cfg: validConfig, routes: true, wantError: true},
		{name: "uid only validator", cfg: validConfig, validator: appTokenValidator{}, routes: true, wantError: true},
		{name: "missing jwks", cfg: &config.Config{}, validator: appClaimsValidator{}, routes: true, wantError: true},
		{name: "claims capable validator", cfg: validConfig, validator: appClaimsValidator{}, routes: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateControlPlaneAuthentication(tt.cfg, tt.validator, tt.routes); (err != nil) != tt.wantError {
				t.Fatalf("error = %v, wantError = %v", err, tt.wantError)
			}
		})
	}
}
