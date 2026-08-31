package event

import (
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/domain"
)

var ErrSourceUnauthorized = errors.New("event source is not authorized")

// SourceBinding is server configuration keyed by the signed Snaplink
// client_id. TenantID may pin a client to one tenant. A wildcard is permitted
// only for an explicitly configured and verified tenant delegation; the event
// body is never itself authority for tenant selection.
// SourceBindingResolver resolves a verified token identity to a configured
// source binding. Keeping this as a port lets handlers test the authentication
// boundary without depending on the concrete binding map.
type SourceBindingResolver interface {
	Resolve(*domain.TokenIdentity) (SourceBinding, error)
}

type SourceBinding struct {
	TenantID           string
	Source             string
	SourceRegion       string
	AllowedTenants     []string
	delegationVerified bool
}

type SourceBindings map[string]SourceBinding

func (bindings SourceBindings) Resolve(identity *domain.TokenIdentity) (SourceBinding, error) {
	if !validBindingIdentity(identity) {
		return SourceBinding{}, ErrSourceUnauthorized
	}
	binding, ok := bindings[identity.ClientID]
	if !ok || !bindingConfigured(binding) {
		return SourceBinding{}, ErrSourceUnauthorized
	}
	if binding.TenantID == "*" {
		return resolveWildcardBinding(binding, identity)
	}
	return resolveFixedBinding(binding, identity)
}

func resolveWildcardBinding(binding SourceBinding, identity *domain.TokenIdentity) (SourceBinding, error) {
	if !identity.Verified || binding.Source != "aero-id" {
		return SourceBinding{}, ErrSourceUnauthorized
	}
	binding.AllowedTenants = intersectDelegatedTenants(binding.AllowedTenants, identity)
	if len(binding.AllowedTenants) == 0 {
		return SourceBinding{}, ErrSourceUnauthorized
	}
	binding.delegationVerified = true
	return binding, nil
}

func resolveFixedBinding(binding SourceBinding, identity *domain.TokenIdentity) (SourceBinding, error) {
	if strings.TrimSpace(identity.TenantID) == "" {
		return SourceBinding{}, ErrSourceUnauthorized
	}
	if binding.TenantID != "" && binding.TenantID != identity.TenantID {
		return SourceBinding{}, ErrSourceUnauthorized
	}
	binding.TenantID = identity.TenantID
	return binding, nil
}

func validBindingIdentity(identity *domain.TokenIdentity) bool {
	return identity != nil && strings.TrimSpace(identity.ClientID) != ""
}

func bindingConfigured(binding SourceBinding) bool {
	return strings.TrimSpace(binding.Source) != "" && strings.TrimSpace(binding.SourceRegion) != ""
}

func (binding SourceBinding) Apply(envelope *Envelope) error {
	if !binding.valid() || envelope == nil {
		return ErrSourceUnauthorized
	}
	if !binding.matches(envelope) {
		return ErrSourceUnauthorized
	}
	if binding.TenantID == "*" {
		if !binding.allowsTenant(envelope.TenantID) {
			return ErrSourceUnauthorized
		}
	} else {
		envelope.TenantID = binding.TenantID
	}
	envelope.Source = binding.Source
	envelope.SourceRegion = binding.SourceRegion
	return nil
}

func (binding SourceBinding) matches(envelope *Envelope) bool {
	if binding.TenantID == "*" && strings.TrimSpace(envelope.TenantID) == "" {
		return false
	}
	tenantMatches := binding.TenantID == "*" || matchesOrEmpty(envelope.TenantID, binding.TenantID)
	return tenantMatches &&
		matchesOrEmpty(envelope.Source, binding.Source) &&
		matchesOrEmpty(envelope.SourceRegion, binding.SourceRegion)
}

func (binding SourceBinding) valid() bool {
	if strings.TrimSpace(binding.TenantID) == "" || strings.TrimSpace(binding.Source) == "" ||
		strings.TrimSpace(binding.SourceRegion) == "" {
		return false
	}
	if binding.TenantID == "*" {
		return binding.Source == "aero-id" && binding.delegationVerified && len(binding.AllowedTenants) > 0
	}
	return true
}

func (binding SourceBinding) allowsTenant(tenant string) bool {
	if tenant == "" || tenant != strings.TrimSpace(tenant) {
		return false
	}
	for _, allowed := range binding.AllowedTenants {
		if tenant == strings.TrimSpace(allowed) {
			return true
		}
	}
	return false
}

func intersectDelegatedTenants(configured []string, identity *domain.TokenIdentity) []string {
	delegated := make(map[string]bool, len(identity.AllowedTenants)+1)
	for _, tenant := range identity.AllowedTenants {
		if tenant = strings.TrimSpace(tenant); tenant != "" {
			delegated[tenant] = true
		}
	}
	if tenant := strings.TrimSpace(identity.TenantID); tenant != "" {
		delegated[tenant] = true
	}
	result := make([]string, 0, len(configured))
	seen := make(map[string]bool, len(configured))
	for _, tenant := range configured {
		tenant = strings.TrimSpace(tenant)
		if tenant != "" && delegated[tenant] && !seen[tenant] {
			result = append(result, tenant)
			seen[tenant] = true
		}
	}
	return result
}

func matchesOrEmpty(value, expected string) bool { return value == "" || value == expected }
