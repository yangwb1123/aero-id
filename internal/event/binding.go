package event

import (
	"errors"
	"strings"

	"github.com/aero/aero-id/internal/domain"
)

var ErrSourceUnauthorized = errors.New("event source is not authorized")

// SourceBinding is server configuration keyed by the signed Snaplink
// client_id. TenantID may further pin a client to one tenant; the explicit
// "*" tenant is reserved for a trusted aero-id replication service identity
// and lets that identity carry the tenant from each already-authenticated
// projection event. Source and region always replace matching compatibility
// fields in the event wire payload.
type SourceBinding struct {
	TenantID     string
	Source       string
	SourceRegion string
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
		if binding.Source != "aero-id" {
			return SourceBinding{}, ErrSourceUnauthorized
		}
		return binding, nil
	}
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
		if strings.TrimSpace(envelope.TenantID) == "" {
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
	return binding.TenantID != "*" || binding.Source == "aero-id"
}

func matchesOrEmpty(value, expected string) bool { return value == "" || value == expected }
