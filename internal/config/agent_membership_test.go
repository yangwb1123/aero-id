package config

import (
	"strings"
	"testing"
	"time"
)

func TestAgentMembershipConfigIsDisabledUnlessBound(t *testing.T) {
	if err := validateAgentMembershipConfig(AgentMembershipConfig{}); err != nil {
		t.Fatalf("empty config must disable the route: %v", err)
	}
	value := AgentMembershipConfig{Bindings: []AgentMembershipBindingConfig{{
		Issuer: "https://issuer.example", Subject: "hub-checker", ClientID: "agent-hub",
		Tenants: []AgentMembershipTenantConfig{{TenantID: "team-a", Source: "snaplink",
			ScopeType: "tenant", ScopeID: "workspace-a", MaxAge: 10 * time.Minute}},
	}}}
	if err := validateAgentMembershipConfig(value); err != nil {
		t.Fatalf("valid exact binding rejected: %v", err)
	}
}

func TestAgentMembershipConfigRejectsAmbiguousOrUnboundedBindings(t *testing.T) {
	base := AgentMembershipBindingConfig{Issuer: "issuer", Subject: "subject", ClientID: "client",
		Tenants: []AgentMembershipTenantConfig{{TenantID: "team-a", Source: "snaplink",
			ScopeType: "tenant", ScopeID: "scope-a", MaxAge: time.Minute}}}
	cases := map[string]AgentMembershipConfig{
		"missing tenant": {Bindings: []AgentMembershipBindingConfig{{Issuer: "issuer", Subject: "subject", ClientID: "client"}}},
		"missing scope": {Bindings: []AgentMembershipBindingConfig{{Issuer: "issuer", Subject: "subject", ClientID: "client",
			Tenants: []AgentMembershipTenantConfig{{TenantID: "team-a", Source: "snaplink", ScopeType: "tenant", MaxAge: time.Minute}}}}},
		"missing freshness": {Bindings: []AgentMembershipBindingConfig{{Issuer: "issuer", Subject: "subject", ClientID: "client",
			Tenants: []AgentMembershipTenantConfig{{TenantID: "team-a", Source: "snaplink", ScopeType: "tenant", ScopeID: "scope-a"}}}}},
		"duplicate principal": {Bindings: []AgentMembershipBindingConfig{base, base}},
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateAgentMembershipConfig(value); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestLoadAgentMembershipBindingParsesMaxAge(t *testing.T) {
	content := sample + `
agent_membership:
  bindings:
    - issuer: https://issuer.example
      subject: hub-checker
      client_id: agent-hub
      tenants:
        - tenant_id: team-a
          source: snaplink
          scope_type: tenant
          scope_id: workspace-a
          max_age: 10m
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AgentMembership.Bindings) != 1 || cfg.AgentMembership.Bindings[0].Tenants[0].MaxAge != 10*time.Minute {
		t.Fatalf("agent membership config was not decoded: %+v", cfg.AgentMembership)
	}
	if _, err := Load(writeTemp(t, strings.Replace(content, "max_age: 10m", "max_age: 0s", 1))); err == nil {
		t.Fatal("zero freshness was accepted")
	}
}
