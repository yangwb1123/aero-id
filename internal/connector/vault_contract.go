package connector

import (
	"errors"
	"strings"
	"unicode"

	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

// validateVaultPrincipalConfig binds the one OAuth client credential held by a
// connector to the one Vault tenant configured for that credential. Vault's
// Snaplink resource-server mapping is tenant-specific; a wildcard or an
// unbound client would turn X-Aero-Tenant into an unsafe caller-controlled
// selector.
func validateVaultPrincipalConfig(cfg HTTPConfig, decision sourceauth.Decision) error {
	if decision.Mode != sourceauth.ModeOAuth {
		return errors.New("aero-vault requires a Snaplink OAuth PrincipalService credential")
	}
	if !validVaultTenantID(cfg.OAuthTenantID) {
		return errors.New("aero-vault requires an explicit OAuth principal tenant")
	}
	return nil
}

func validVaultTenantID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "*" && len(value) <= 512 &&
		strings.IndexFunc(value, func(character rune) bool {
			return unicode.IsControl(character) || unicode.IsSpace(character)
		}) < 0
}

func (c *JSONSummaryConnector) validateVaultRequestTenant(tenant string) error {
	if c.source != SourceAeroVault {
		return nil
	}
	if !validVaultTenantID(tenant) || strings.TrimSpace(tenant) != strings.TrimSpace(c.cfg.OAuthTenantID) {
		return &RemoteError{Source: c.source, Kind: ErrorForbidden,
			Code: "source.tenant_credential_unbound", Temporary: false}
	}
	return nil
}

// vaultRequestRegion binds every Vault operation to the region of the
// connector's regional cell. An omitted region is filled from that immutable
// local configuration; a different region is rejected before token or source
// I/O so a local credential cannot be used as a cross-region shortcut.
func (c *JSONSummaryConnector) vaultRequestRegion(region string) (string, error) {
	if c.source != SourceAeroVault {
		return region, nil
	}
	expected := strings.TrimSpace(c.cfg.Region)
	actual := strings.TrimSpace(region)
	if expected == "" {
		return actual, nil
	}
	if actual != "" && actual != expected {
		return "", &RemoteError{Source: c.source, Kind: ErrorForbidden,
			Code: "source.region_credential_unbound", Temporary: false}
	}
	return expected, nil
}
