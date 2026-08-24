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

// wellKnownPlaceholderSecrets are signing-key literals that were shipped in
// configs/config.yaml and are therefore public forever. They must never boot
// anywhere: the audit ledger signs every record_hash with its HMAC key and the
// root key signs presigned download URLs, so a known key would let an attacker
// forge tamper-evidence signatures or download links. The deny-list is
// compiled-in and cannot be overridden by env (Load merges env before
// validate, so an env-supplied placeholder still fails).
var wellKnownPlaceholderSecrets = map[string]struct{}{
	"change-me-please-very-long-audit-key": {},
	"change-me-please-very-long-random":    {},
}

// placeholderSecretPrefixes covers the conventional placeholder families for
// both signing keys; the deny-before-length ordering in validateAuditLedger
// makes the deny error deterministic even for 32+ byte placeholder-looking
// values.
var placeholderSecretPrefixes = []string{"change-me", "changeme", "replace-me"}

// rejectPlaceholderSecret denies well-known placeholder signing keys. Both
// signing-key validators call it; the pinned substring "signing_key is a
// known placeholder" is asserted by TestLoadRejectsPlaceholderSecrets — do
// not reword without updating that test.
func rejectPlaceholderSecret(key, field string) error {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if _, known := wellKnownPlaceholderSecrets[normalized]; known {
		return fmt.Errorf("%s is a known placeholder — secrets must come from environment or an approved secret provider", field)
	}
	for _, prefix := range placeholderSecretPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return fmt.Errorf("%s is a known placeholder — secrets must come from environment or an approved secret provider", field)
		}
	}
	return nil
}
