package config

import (
	"strings"
	"testing"
)

func TestCommercialCredentialsAreDistinctAcrossDestinations(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "billing-secret-value-0000000000000001")
	config := commercialTestConfig()
	if err := config.validateCommercialIntegrations(); err == nil {
		t.Fatal("shared Billing/Audit secret was accepted")
	}

	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	config = commercialTestConfig()
	config.AuditGovernance.Bindings["tenant-one"] = AuditGovernanceBindingConfig{
		TokenURL: "https://sso.example/token", ClientID: "billing-client",
		ClientSecretEnv: "TEST_AUDIT_SECRET", Scope: auditWriteScope, Resources: []string{auditResource},
	}
	if err := config.validateCommercialIntegrations(); err == nil {
		t.Fatal("shared Billing/Audit client ID was accepted")
	}
}

func TestCommercialBindingsRequireExactScopesResourcesAndTenantSets(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	config := commercialTestConfig()
	if err := config.validateCommercialIntegrations(); err != nil {
		t.Fatal(err)
	}
	binding := config.Billing.Bindings["tenant-one"]
	binding.Scope += " admin:write"
	config.Billing.Bindings["tenant-one"] = binding
	if err := config.validateCommercialIntegrations(); err == nil {
		t.Fatal("over-scoped Billing client was accepted")
	}

	config = commercialTestConfig()
	delete(config.AuditGovernance.Bindings, "tenant-one")
	if err := config.validateCommercialIntegrations(); err == nil {
		t.Fatal("different Billing/Audit tenant sets were accepted")
	}
}

func TestCommercialValidationErrorsDoNotDiscloseBindingMaterial(t *testing.T) {
	t.Setenv("SENSITIVE_SECRET_ENV", "short")
	clients := map[string]struct{}{}
	secrets := map[string]struct{}{}
	err := registerCredential("sensitive-client-id", "SENSITIVE_SECRET_ENV", clients, secrets)
	if err == nil {
		t.Fatal("invalid secret was accepted")
	}
	for _, sensitive := range []string{"sensitive-client-id", "SENSITIVE_SECRET_ENV", "short"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("validation error disclosed %q: %v", sensitive, err)
		}
	}
}

func TestCommercialCredentialsRejectEmbeddedControlCharacters(t *testing.T) {
	t.Setenv("TEST_CONTROL_SECRET", "commercial-secret-value-000000000\t1")
	if _, err := ResolveClientSecretEnv("TEST_CONTROL_SECRET"); err == nil {
		t.Fatal("secret containing a control character was accepted")
	}
	if canonicalIdentity("tenant\ttwo", 256) {
		t.Fatal("tenant containing a control character was accepted")
	}
}

func setCommercialSecrets(t *testing.T, billing, audit string) {
	t.Helper()
	t.Setenv("TEST_BILLING_SECRET", billing)
	t.Setenv("TEST_AUDIT_SECRET", audit)
}

func commercialTestConfig() *Config {
	return &Config{
		Billing: BillingConfig{Enabled: true, Endpoint: "https://billing.example", Bindings: map[string]BillingBindingConfig{
			"tenant-one": {TokenURL: "https://sso.example/token", ClientID: "billing-client",
				ClientSecretEnv: "TEST_BILLING_SECRET", Scope: billingMeteringScope + " " + billingEntitlementScope,
				Resources: []string{billingResource}},
		}},
		AuditGovernance: AuditGovernanceConfig{Enabled: true, Endpoint: "https://audit.example", Bindings: map[string]AuditGovernanceBindingConfig{
			"tenant-one": {TokenURL: "https://sso.example/token", ClientID: "audit-client",
				ClientSecretEnv: "TEST_AUDIT_SECRET", Scope: auditWriteScope, Resources: []string{auditResource}},
		}},
	}
}
