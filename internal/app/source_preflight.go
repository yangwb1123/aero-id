package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/pkg/metrics"
	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

// PreflightAeroIMTargetAssertion verifies the configured signing key against
// Aero IM's dedicated JWKS. It is intentionally separate from New: normal
// startup remains independent of a remote trust-document fetch, while a
// release/init preflight can use the exact image, config, and secret context
// that will run the connector.
func PreflightAeroIMTargetAssertion(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("aero-im target assertion preflight requires configuration")
	}
	value := cfg.Sources.AeroIM
	if strings.TrimSpace(value.Endpoint) == "" ||
		(sourceauth.IsExplicitNonProductionLoopback(value.Endpoint, cfg.App.Env, value.AllowInsecureLoopback) &&
			!sourceTargetAssertionConfigured(value)) {
		return nil
	}
	if err := connector.VerifyAeroIMTargetAssertionTrust(sourceHTTPConfig(value, cfg.App.Env, metrics.NewRegistry())); err != nil {
		return fmt.Errorf("aero-im target assertion preflight failed: %w", err)
	}
	return nil
}

func sourceTargetAssertionConfigured(value config.SourceEndpointConfig) bool {
	for _, field := range []string{value.TargetAssertionKeyFile, value.TargetAssertionKeyEnv,
		value.TargetAssertionJWKSURL, value.TargetAssertionTrustDomain, value.TargetAssertionKeyID,
		value.TargetAssertionIssuer, value.TargetAssertionAudience, value.TargetAssertionSubject,
		value.TargetAssertionClientID} {
		if strings.TrimSpace(field) != "" {
			return true
		}
	}
	return value.TargetAssertionLifetime != 0
}
