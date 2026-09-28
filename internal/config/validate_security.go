package config

import (
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

// validateEnvironmentSecurity applies the production safety contract at the
// configuration boundary. Development and test configurations intentionally
// retain the local/insecure compatibility defaults, but a production-like
// process must never silently start with those settings.
func (c *Config) validateEnvironmentSecurity() error {
	if !productionLikeEnvironment(c.App.Env) {
		return nil
	}
	if c.SSO.Insecure {
		return fmt.Errorf("sso.insecure must be false in production-like environments")
	}
	if !c.SSO.AuthZRequired {
		return fmt.Errorf("sso.authz_required must be true in production-like environments")
	}
	if c.SSO.AuthZLegacyFallback {
		return fmt.Errorf("sso.authz_legacy_fallback must be false in production-like environments")
	}
	if strings.TrimSpace(c.Database.SSLMode) == "" || strings.EqualFold(strings.TrimSpace(c.Database.SSLMode), "disable") {
		return fmt.Errorf("database.sslmode must enable TLS in production-like environments")
	}
	if len(c.Server.HTTP.CORSOrigins) == 0 {
		return fmt.Errorf("server.http.cors_origins must be configured in production-like environments")
	}
	if eventIngestHMACConfigured(c.EventIngest) && !c.EventIngest.RequireTimestamp {
		return fmt.Errorf("event_ingest.require_timestamp must be true when signed event ingestion is configured in production-like environments")
	}
	return nil
}

func eventIngestHMACConfigured(value EventIngestConfig) bool {
	return strings.TrimSpace(value.SigningKey) != "" || len(value.SourceKeys) > 0
}

func productionLikeEnvironment(value string) bool {
	return sourceauth.ProductionLikeEnvironment(value)
}
