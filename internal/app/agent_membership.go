package app

import (
	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/service"
)

func agentMembershipPolicies(value config.AgentMembershipConfig) []service.AgentMembershipPolicy {
	var result []service.AgentMembershipPolicy
	for _, principal := range value.Bindings {
		for _, tenant := range principal.Tenants {
			result = append(result, service.AgentMembershipPolicy{
				Issuer: principal.Issuer, Subject: principal.Subject, ClientID: principal.ClientID,
				TenantID: tenant.TenantID, Source: tenant.Source, ScopeType: tenant.ScopeType,
				ScopeID: tenant.ScopeID, MaxAge: tenant.MaxAge,
			})
		}
	}
	return result
}
