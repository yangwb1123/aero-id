package config

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	maxAgentMembershipBindings = 100
	maxAgentMembershipTenants  = 100
	maxAgentMembershipAge      = 30 * 24 * time.Hour
)

// AgentMembershipConfig enables the read-only service membership projection
// endpoint. An empty binding list keeps the endpoint disabled.
type AgentMembershipConfig struct {
	Bindings []AgentMembershipBindingConfig `mapstructure:"bindings"`
}

// AgentMembershipBindingConfig binds one verified machine identity to an
// explicit set of tenants and their fixed membership scopes.
type AgentMembershipBindingConfig struct {
	Issuer   string                        `mapstructure:"issuer"`
	Subject  string                        `mapstructure:"subject"`
	ClientID string                        `mapstructure:"client_id"`
	Tenants  []AgentMembershipTenantConfig `mapstructure:"tenants"`
}

// AgentMembershipTenantConfig limits a principal to one configured projection
// scope per tenant. The caller cannot choose any of these values.
type AgentMembershipTenantConfig struct {
	TenantID  string        `mapstructure:"tenant_id"`
	Source    string        `mapstructure:"source"`
	ScopeType string        `mapstructure:"scope_type"`
	ScopeID   string        `mapstructure:"scope_id"`
	MaxAge    time.Duration `mapstructure:"max_age"`
}

func validateAgentMembershipConfig(value AgentMembershipConfig) error {
	if len(value.Bindings) > maxAgentMembershipBindings {
		return fmt.Errorf("agent_membership.bindings exceeds %d entries", maxAgentMembershipBindings)
	}
	principals := make(map[string]struct{}, len(value.Bindings))
	for i, binding := range value.Bindings {
		if err := validateAgentMembershipBinding(binding, i, principals); err != nil {
			return err
		}
	}
	return nil
}

func validateAgentMembershipBinding(binding AgentMembershipBindingConfig, index int,
	principals map[string]struct{}) error {
	prefix := fmt.Sprintf("agent_membership.bindings[%d]", index)
	for name, field := range map[string]string{
		"issuer": binding.Issuer, "subject": binding.Subject, "client_id": binding.ClientID,
	} {
		if !validAgentMembershipText(field, 256) {
			return fmt.Errorf("%s.%s must be a nonempty exact value", prefix, name)
		}
	}
	principal := binding.Issuer + "\x00" + binding.Subject + "\x00" + binding.ClientID
	if _, exists := principals[principal]; exists {
		return fmt.Errorf("%s duplicates an agent membership principal", prefix)
	}
	principals[principal] = struct{}{}
	if len(binding.Tenants) == 0 || len(binding.Tenants) > maxAgentMembershipTenants {
		return fmt.Errorf("%s.tenants must contain 1..%d entries", prefix, maxAgentMembershipTenants)
	}
	return validateAgentMembershipTenants(binding.Tenants, prefix)
}

func validateAgentMembershipTenants(tenants []AgentMembershipTenantConfig, prefix string) error {
	seen := make(map[string]struct{}, len(tenants))
	for index, tenant := range tenants {
		tenantPrefix := fmt.Sprintf("%s.tenants[%d]", prefix, index)
		if err := validateAgentMembershipTenant(tenant, tenantPrefix); err != nil {
			return err
		}
		if _, exists := seen[tenant.TenantID]; exists {
			return fmt.Errorf("%s duplicates tenant_id", tenantPrefix)
		}
		seen[tenant.TenantID] = struct{}{}
	}
	return nil
}

func validateAgentMembershipTenant(tenant AgentMembershipTenantConfig, prefix string) error {
	for name, field := range map[string]string{
		"tenant_id": tenant.TenantID, "source": tenant.Source,
		"scope_type": tenant.ScopeType, "scope_id": tenant.ScopeID,
	} {
		if !validAgentMembershipText(field, 256) {
			return fmt.Errorf("%s.%s must be a nonempty exact value", prefix, name)
		}
	}
	if tenant.MaxAge < time.Second || tenant.MaxAge > maxAgentMembershipAge {
		return fmt.Errorf("%s.max_age must be between 1s and 30d", prefix)
	}
	return nil
}

func validAgentMembershipText(value string, limit int) bool {
	return value != "" && len(value) <= limit && value == strings.TrimSpace(value) &&
		!strings.ContainsFunc(value, unicode.IsControl)
}
