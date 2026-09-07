package config

import (
	"fmt"
	"strings"

	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

// validateSourceCredentialSeparation prevents one Snaplink client or secret
// from silently becoming a credential for multiple downstream authorities.
// Source clients are security boundaries even when they use the same token
// authority, and Vault additionally binds its client to one tenant.
func (c *Config) validateSourceCredentialSeparation() error {
	seenClients := make(map[string]struct{})
	seenSecrets := make(map[string]struct{})
	seenBearers := make(map[string]struct{})
	for _, item := range []struct {
		name  string
		value SourceEndpointConfig
	}{
		{"snaplink", c.Sources.Snaplink},
		{"aero-im", c.Sources.AeroIM},
		{"aero-vault", c.Sources.AeroVault},
	} {
		if strings.TrimSpace(item.value.Endpoint) == "" {
			continue
		}
		if configured, complete := sourceOAuthCredentialState(item.value); complete && configured {
			clientID := strings.TrimSpace(item.value.OAuthClientID)
			secret := item.value.OAuthSecret
			if _, duplicate := seenClients[clientID]; duplicate {
				return fmt.Errorf("sources OAuth client credentials must be distinct per source")
			}
			if _, duplicate := seenSecrets[secret]; duplicate {
				return fmt.Errorf("sources OAuth client secrets must be distinct per source")
			}
			seenClients[clientID] = struct{}{}
			seenSecrets[secret] = struct{}{}
		}
		bearer, err := sourceauth.NormalizeBearerToken(item.value.BearerToken)
		if err != nil || bearer == "" {
			continue
		}
		if _, duplicate := seenBearers[bearer]; duplicate {
			return fmt.Errorf("sources bearer credentials must be distinct per source")
		}
		seenBearers[bearer] = struct{}{}
	}
	return nil
}
