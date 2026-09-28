package config

import (
	"fmt"
	"strings"
)

const (
	shippedSigningKeyPlaceholder      = "change-me-please-very-long-random"
	shippedAuditSigningKeyPlaceholder = "change-me-please-very-long-audit-key"
)

// wellKnownPlaceholderSecrets are values that have been committed in shipped
// configuration or deployment fixtures. They are public forever and must not
// be accepted by a production-like process.
var wellKnownPlaceholderSecrets = map[string]struct{}{
	shippedSigningKeyPlaceholder:      {},
	shippedAuditSigningKeyPlaceholder: {},
}

// placeholderSecretPrefixes covers the conventional placeholder families,
// including their underscore spelling after normalization. Keep this check in
// the production-only gate so development and test fixtures remain compatible.
var placeholderSecretPrefixes = []string{"change-me", "changeme", "replace-me"}

func isPlaceholderSecret(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	if _, known := wellKnownPlaceholderSecrets[normalized]; known {
		return true
	}
	for _, prefix := range placeholderSecretPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

// validateEnvHardening rejects committed development credentials and insecure
// SSO defaults only for production-like environments. Other environments
// retain their existing compatibility behavior.
func (c *Config) validateEnvHardening() error {
	if !productionLikeEnvironment(c.App.Env) {
		return nil
	}
	if isPlaceholderSecret(c.SigningKey) {
		return fmt.Errorf("signing_key must not be the shipped placeholder when app.env=prod (override with AERO_ID_SIGNING_KEY)")
	}
	if isPlaceholderSecret(c.AuditLedger.SigningKey) {
		return fmt.Errorf("audit_ledger.signing_key must not be the shipped placeholder when app.env=prod (override with AERO_ID_AUDIT_LEDGER_SIGNING_KEY)")
	}
	if c.SSO.Insecure {
		return fmt.Errorf("sso.insecure must be false when app.env=prod")
	}
	if !c.SSO.AuthZRequired {
		return fmt.Errorf("sso.authz_required must be true when app.env=prod")
	}
	return nil
}
