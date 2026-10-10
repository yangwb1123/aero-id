package app

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aero/aero-id/internal/config"
	"go.uber.org/zap"
)

func TestAuthenticationPreflightMatchesStandardStartupGuard(t *testing.T) {
	for _, missing := range []string{"none", "jwks", "issuer", "audience", "invalid-url", "nil"} {
		t.Run(missing, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.SSO.JWKSURL, cfg.SSO.Issuer, cfg.SSO.Audience = "https://snaplink.example/jwks", "snaplink", "aero-id"
			switch missing {
			case "jwks":
				cfg.SSO.JWKSURL = ""
			case "issuer":
				cfg.SSO.Issuer = " "
			case "audience":
				cfg.SSO.Audience = " "
			case "invalid-url":
				cfg.SSO.JWKSURL = "file:///tmp/jwks"
			case "nil":
				cfg = nil
			}
			preflightErr := PreflightControlPlaneAuthentication(cfg)
			if (preflightErr != nil) != (missing != "none") {
				t.Fatalf("preflight = %v", preflightErr)
			}
			if cfg == nil {
				return
			}
			validator, startupErr := buildTokenValidator(cfg, nil, zap.NewNop())
			if startupErr == nil {
				startupErr = validateControlPlaneAuthentication(cfg, validator, true)
			}
			if (preflightErr == nil) != (startupErr == nil) {
				t.Fatalf("preflight/startup diverged: %v / %v", preflightErr, startupErr)
			}
		})
	}
}

func TestAuthenticationPreflightDoesNotFetchJWKS(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	cfg := &config.Config{}
	cfg.SSO.JWKSURL, cfg.SSO.Issuer, cfg.SSO.Audience = server.URL+"/jwks", "snaplink", "aero-id"
	if err := PreflightControlPlaneAuthentication(cfg); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("static authentication preflight probed live trust")
	}
}
