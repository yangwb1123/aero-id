package config

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

const (
	billingEntitlementScope = "billing:entitlement:read"
	billingMeteringScope    = "metering:write"
	billingResource         = "billing-api"
	auditWriteScope         = "audit:event:write"
	auditResource           = "audit-governance"
)

func (c *Config) validateCommercialIntegrations() error {
	clients := make(map[string]struct{})
	secrets := make(map[string]struct{})
	if err := validateBillingBindings(c.Billing, clients, secrets); err != nil {
		return err
	}
	if err := validateAuditBindings(c.AuditGovernance, clients, secrets); err != nil {
		return err
	}
	if c.Billing.Enabled && c.AuditGovernance.Enabled &&
		!sameTenantSet(c.Billing.Bindings, c.AuditGovernance.Bindings) {
		return fmt.Errorf("billing and audit governance must bind the same tenant set")
	}
	return nil
}

func validateBillingBindings(value BillingConfig, clients, secrets map[string]struct{}) error {
	if !value.Enabled {
		return nil
	}
	if strings.TrimSpace(value.Endpoint) == "" || len(value.Bindings) == 0 {
		return fmt.Errorf("billing requires an endpoint and tenant bindings")
	}
	for tenant, binding := range value.Bindings {
		if !canonicalIdentity(tenant, 256) ||
			!exactScopes(binding.Scope, billingEntitlementScope, billingMeteringScope) ||
			!exactResources(binding.Resources, billingResource) {
			return fmt.Errorf("billing contains an invalid tenant, scope, or resource binding")
		}
		if err := registerCredential(binding.ClientID, binding.ClientSecretEnv, clients, secrets); err != nil {
			return err
		}
	}
	return nil
}

func validateAuditBindings(value AuditGovernanceConfig, clients, secrets map[string]struct{}) error {
	if !value.Enabled {
		return nil
	}
	if strings.TrimSpace(value.Endpoint) == "" || len(value.Bindings) == 0 {
		return fmt.Errorf("audit governance requires an endpoint and tenant bindings")
	}
	for tenant, binding := range value.Bindings {
		if !canonicalIdentity(tenant, 256) || !exactScopes(binding.Scope, auditWriteScope) ||
			!exactResources(binding.Resources, auditResource) {
			return fmt.Errorf("audit governance contains an invalid tenant, scope, or resource binding")
		}
		if err := registerCredential(binding.ClientID, binding.ClientSecretEnv, clients, secrets); err != nil {
			return err
		}
	}
	return nil
}

func registerCredential(clientID, secretEnv string, clients, secrets map[string]struct{}) error {
	secret, err := ResolveClientSecretEnv(secretEnv)
	if err != nil || !canonicalIdentity(clientID, 512) {
		return fmt.Errorf("commercial OAuth binding contains invalid credentials")
	}
	if _, duplicate := clients[clientID]; duplicate {
		return fmt.Errorf("commercial OAuth client IDs must be globally distinct")
	}
	if _, duplicate := secrets[secret]; duplicate {
		return fmt.Errorf("commercial OAuth client secrets must be globally distinct")
	}
	clients[clientID] = struct{}{}
	secrets[secret] = struct{}{}
	return nil
}

// ResolveClientSecretEnv reads an operator-selected secret without trimming it.
// Error text deliberately omits the locator and secret value.
func ResolveClientSecretEnv(name string) (string, error) {
	if !validSecretEnvName(name) {
		return "", fmt.Errorf("commercial OAuth secret locator is invalid")
	}
	value, found := os.LookupEnv(name)
	if !found || len(value) < 32 || value != strings.TrimSpace(value) || containsControl(value) {
		return "", fmt.Errorf("commercial OAuth secret is unavailable or invalid")
	}
	return value, nil
}

func validSecretEnvName(value string) bool {
	if value == "" || !secretEnvStart(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !secretEnvCharacter(value[index]) {
			return false
		}
	}
	return true
}

func secretEnvStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z'
}

func secretEnvCharacter(value byte) bool {
	return secretEnvStart(value) || value >= '0' && value <= '9'
}

func canonicalIdentity(value string, max int) bool {
	return value != "" && len(value) <= max && value == strings.TrimSpace(value) && !containsControl(value)
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func exactScopes(value string, expected ...string) bool {
	actual := strings.Fields(value)
	if len(actual) != len(expected) {
		return false
	}
	set := make(map[string]struct{}, len(actual))
	for _, scope := range actual {
		set[scope] = struct{}{}
	}
	for _, scope := range expected {
		if _, found := set[scope]; !found {
			return false
		}
	}
	return len(set) == len(expected)
}

func exactResources(value []string, expected string) bool {
	return len(value) == 1 && value[0] == expected
}

func sameTenantSet(left map[string]BillingBindingConfig, right map[string]AuditGovernanceBindingConfig) bool {
	if len(left) != len(right) {
		return false
	}
	for tenant := range left {
		if _, found := right[tenant]; !found {
			return false
		}
	}
	return true
}
