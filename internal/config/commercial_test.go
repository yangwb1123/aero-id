package config

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector/auditgovernance"
	billingconnector "github.com/aero/aero-id/internal/connector/billing"
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

// TestAuditGovernanceGateMatrix (T-10.1.1): every activation-gate cell of
// validateAuditBindings — enabled+empty bindings, enabled+missing/whitespace
// endpoint, disabled+empty, disabled+invalid TokenURL. Each cell varies
// exactly one dimension; the enabled fixture is otherwise fully valid.
func TestAuditGovernanceGateMatrix(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	const missing = "audit governance requires an endpoint and tenant bindings"
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"enabled empty bindings", func(c *Config) {
			c.AuditGovernance.Bindings = map[string]AuditGovernanceBindingConfig{}
		}, missing},
		{"enabled empty endpoint", func(c *Config) { c.AuditGovernance.Endpoint = "" }, missing},
		{"enabled whitespace endpoint", func(c *Config) { c.AuditGovernance.Endpoint = "   " }, missing},
		{"disabled empty", func(c *Config) {
			c.AuditGovernance.Enabled = false
			c.AuditGovernance.Endpoint = ""
			c.AuditGovernance.Bindings = map[string]AuditGovernanceBindingConfig{}
		}, ""},
		{"disabled invalid token url", func(c *Config) {
			c.AuditGovernance.Enabled = false
			binding := c.AuditGovernance.Bindings["tenant-one"]
			binding.TokenURL = "not-a-url"
			c.AuditGovernance.Bindings["tenant-one"] = binding
		}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := commercialTestConfig()
			tc.mutate(cfg)
			err := cfg.validateCommercialIntegrations()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// TestAuditGovernanceKnobGateMatrix (T-10.1.7): B2-5 knob cells of the
// activation gate matrix. validateAuditBindings (the commercial gate) must
// never read purge_pause or dead_letter_retention — a pause or a positive
// retention is never a gate rejection — while validateAuditGovernance
// rejects a negative retention regardless of Enabled. The entry-point split
// is load-bearing: validateAuditGovernance sits in validateRuntimeLimits
// (validate.go:130), not in validateCommercialIntegrations (commercial.go),
// so the rejection cells call it directly. Only the DISABLED -1h cell pins
// Enabled-independence: it must error even when the relay is off (an
// Enabled-gated regression goes red here while the enabled leg would stay
// green), so both -1h cells are kept. The defaults cell pins the
// zero-value contract the parity pin (T-10.1.8) relies on.
func TestAuditGovernanceKnobGateMatrix(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	const wantErr = "audit_governance.dead_letter_retention must not be negative"
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
		check  func(*Config) error
		want   string
	}{
		{"enabled purge_pause passes", func(c *Config) { c.AuditGovernance.PurgePause = true },
			func(c *Config) error { return c.validateCommercialIntegrations() }, ""},
		{"enabled positive retention passes", func(c *Config) { c.AuditGovernance.DeadLetterRetention = 168 * time.Hour },
			func(c *Config) error { return c.validateCommercialIntegrations() }, ""},
		{"enabled negative retention rejected", func(c *Config) { c.AuditGovernance.DeadLetterRetention = -time.Hour },
			func(c *Config) error { return validateAuditGovernance(c.AuditGovernance) }, wantErr},
		{"disabled purge_pause passes", func(c *Config) {
			c.AuditGovernance.Enabled = false
			c.AuditGovernance.PurgePause = true
		}, func(c *Config) error { return c.validateCommercialIntegrations() }, ""},
		// Load-bearing cell: a negative retention is rejected even when the
		// relay is disabled — the check is Enabled-independent.
		{"disabled negative retention rejected", func(c *Config) {
			c.AuditGovernance.Enabled = false
			c.AuditGovernance.DeadLetterRetention = -time.Hour
		}, func(c *Config) error { return validateAuditGovernance(c.AuditGovernance) }, wantErr},
		{"defaults are false and zero", func(c *Config) {}, func(c *Config) error {
			if c.AuditGovernance.PurgePause || c.AuditGovernance.DeadLetterRetention != 0 {
				return fmt.Errorf("knob defaults = %v/%v, want false/0",
					c.AuditGovernance.PurgePause, c.AuditGovernance.DeadLetterRetention)
			}
			return nil
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := commercialTestConfig()
			tc.mutate(cfg)
			if tc.want == "" {
				if err := tc.check(cfg); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			err := tc.check(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// TestAuditGovernanceRejectsInvalidTokenURLBeforeCredentials (T-10.1.2): an
// empty/whitespace/invalid TokenURL fails with the stable non-disclosing
// message both with and without valid secrets — the URL check runs before
// registerCredential (D2 ordering), so a missing/short secret env can never
// mask the URL error (the no-secrets leg disables billing, whose credential
// check runs first). The raw URL value never appears in the error.
func TestAuditGovernanceRejectsInvalidTokenURLBeforeCredentials(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	const want = "audit governance binding contains an invalid token URL"
	for _, tc := range []struct {
		name    string
		url     string
		secrets bool
	}{
		{"empty url with secrets", "", true},
		{"whitespace url with secrets", "   \t ", true},
		{"userinfo url with secrets", "https://user:pass@sso.example/token", true},
		{"empty url without secrets", "", false},
		{"whitespace url without secrets", "  ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := commercialTestConfig()
			if !tc.secrets {
				cfg.Billing.Enabled = false
				t.Setenv("TEST_AUDIT_SECRET", "short")
			}
			binding := cfg.AuditGovernance.Bindings["tenant-one"]
			binding.TokenURL = tc.url
			cfg.AuditGovernance.Bindings["tenant-one"] = binding
			err := cfg.validateCommercialIntegrations()
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %v, want containing %q", err, want)
			}
			if tc.url != "" && strings.Contains(err.Error(), tc.url) {
				t.Fatalf("error disclosed the raw URL %q: %v", tc.url, err)
			}
		})
	}
}

// TestAuditGovernanceTokenURLErrorDoesNotDiscloseValue (T-10.1.2 F4 pin): the
// error text never echoes the raw URL, even a distinctive one.
func TestAuditGovernanceTokenURLErrorDoesNotDiscloseValue(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	const sensitive = "https://sensitive-audit-token-url-0000000:secret@sso.example/token"
	cfg := commercialTestConfig()
	binding := cfg.AuditGovernance.Bindings["tenant-one"]
	binding.TokenURL = sensitive
	cfg.AuditGovernance.Bindings["tenant-one"] = binding
	err := cfg.validateCommercialIntegrations()
	if err == nil {
		t.Fatal("userinfo token URL was accepted")
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatalf("validation error disclosed the token URL %q: %v", sensitive, err)
	}
}

// auditTokenURLCases is the shared T-10.1.3 scheme/loopback matrix, reused by
// the parity pin (T-10.1.6). The bare-? row pins ForceQuery/RawQuery==""
// parity so a future cleanup cannot break it asymmetrically.
func auditTokenURLCases() []struct {
	name          string
	tokenURL      string
	allowLoopback bool
	wantValid     bool
} {
	return []struct {
		name          string
		tokenURL      string
		allowLoopback bool
		wantValid     bool
	}{
		{"http non-loopback", "http://sso.example/token", false, false},
		{"http non-loopback loopback allowed", "http://sso.example/token", true, false},
		{"http localhost", "http://localhost/token", false, false},
		{"http localhost loopback allowed", "http://localhost/token", true, true},
		{"http 127.0.0.1", "http://127.0.0.1/token", false, false},
		{"http 127.0.0.1 loopback allowed", "http://127.0.0.1/token", true, true},
		{"http [::1] loopback allowed", "http://[::1]/token", true, true},
		{"https", "https://sso.example/token", false, true},
		{"https loopback allowed", "https://sso.example/token", true, true},
		{"https bare query", "https://sso.example/token?", false, true},
		{"not a url", "not-a-url", false, false},
		{"ftp scheme", "ftp://sso.example/token", false, false},
		{"userinfo", "https://u:p@sso.example/token", false, false},
		{"fragment", "https://sso.example/token#frag", false, false},
		{"raw query", "https://sso.example/token?x=1", false, false},
		{"trimmed https", "  https://sso.example/token  ", false, true},
	}
}

// TestAuditGovernanceTokenURLSchemeMatrix (T-10.1.3): accept/reject per
// REQ-1.1 for every matrix cell at both AllowInsecureLoopback values.
func TestAuditGovernanceTokenURLSchemeMatrix(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	for _, tc := range auditTokenURLCases() {
		t.Run(tc.name, func(t *testing.T) {
			cfg := commercialTestConfig()
			cfg.AuditGovernance.AllowInsecureLoopback = tc.allowLoopback
			binding := cfg.AuditGovernance.Bindings["tenant-one"]
			binding.TokenURL = tc.tokenURL
			cfg.AuditGovernance.Bindings["tenant-one"] = binding
			err := cfg.validateCommercialIntegrations()
			if (err == nil) != tc.wantValid {
				t.Fatalf("TokenURL %q (loopback %v) accepted=%v, want %v (err %v)",
					tc.tokenURL, tc.allowLoopback, err == nil, tc.wantValid, err)
			}
		})
	}
}

// TestAuditTokenURLGateParityWithConnector (T-10.1.6): for every matrix cell,
// the config gate and auditgovernance.NewOAuthTokenSource must agree — a
// config that passes validate() can never fail connector construction on the
// TokenURL dimension, and vice versa. The connector does not import config,
// so the pin lives in the config test package without a cycle.
func TestAuditTokenURLGateParityWithConnector(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	for _, tc := range auditTokenURLCases() {
		t.Run(tc.name, func(t *testing.T) {
			cfg := commercialTestConfig()
			cfg.AuditGovernance.AllowInsecureLoopback = tc.allowLoopback
			binding := cfg.AuditGovernance.Bindings["tenant-one"]
			binding.TokenURL = tc.tokenURL
			cfg.AuditGovernance.Bindings["tenant-one"] = binding
			gateErr := cfg.validateCommercialIntegrations()

			_, connectorErr := auditgovernance.NewOAuthTokenSource(auditgovernance.OAuthConfig{
				SourcePrefix: "aero-id", AllowInsecureLoopback: tc.allowLoopback,
				Bindings: map[string]auditgovernance.OAuthBindingConfig{
					"tenant-one": {TokenURL: tc.tokenURL, ClientID: "audit-client",
						ClientSecret: "audit-secret-value-000000000000000001", Scope: auditWriteScope,
						Resources: []string{auditResource}},
				},
			}, nil)
			if (gateErr == nil) != (connectorErr == nil) {
				t.Fatalf("parity drift for %q (loopback %v): gate accepts=%v connector accepts=%v",
					tc.tokenURL, tc.allowLoopback, gateErr == nil, connectorErr == nil)
			}
		})
	}
}

// TestBillingTokenURLGateParityWithConnector: for every cell of the shared
// T-10.1.3 matrix at both AllowInsecureLoopback values, the config gate and
// billingconnector.NewClient must agree on the TokenURL dimension — a config
// that passes validate() can never fail connector construction on the
// TokenURL dimension, and vice versa. The connector does not import config,
// so the pin lives in the config test package without a cycle.
func TestBillingTokenURLGateParityWithConnector(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	for _, tc := range auditTokenURLCases() {
		t.Run(tc.name, func(t *testing.T) {
			cfg := commercialTestConfig()
			cfg.Billing.AllowInsecureLoopback = tc.allowLoopback
			binding := cfg.Billing.Bindings["tenant-one"]
			binding.TokenURL = tc.tokenURL
			cfg.Billing.Bindings["tenant-one"] = binding
			gateErr := cfg.validateCommercialIntegrations()

			_, connectorErr := billingconnector.NewClient(billingconnector.Config{
				Endpoint: "https://billing.example", Timeout: 5 * time.Second, MaxBodyBytes: 256 << 10,
				AllowInsecureLoopback: tc.allowLoopback,
				Bindings: map[string]billingconnector.BindingConfig{
					"tenant-one": {TokenURL: tc.tokenURL, ClientID: "billing-client",
						ClientSecret: "billing-secret-value-0000000000000001",
						Scope:        billingEntitlementScope + " " + billingMeteringScope,
						Resources:    []string{billingResource}},
				},
			}, nil)
			if (gateErr == nil) != (connectorErr == nil) {
				t.Fatalf("parity drift for %q (loopback %v): gate accepts=%v connector accepts=%v",
					tc.tokenURL, tc.allowLoopback, gateErr == nil, connectorErr == nil)
			}
		})
	}
}

// TestBillingEndpointGateParityWithConnector: the Endpoint dimension of the
// billing gate must agree with billingconnector.NewClient on every cell at
// both AllowInsecureLoopback values, closing the second half of the
// Load-vs-boot asymmetry. Empty/whitespace endpoints are rejected by the
// existing envelope check on the gate side and by secureEndpoint on the
// connector side; the bare-? row pins ForceQuery/RawQuery=="" parity.
func TestBillingEndpointGateParityWithConnector(t *testing.T) {
	setCommercialSecrets(t, "billing-secret-value-0000000000000001", "audit-secret-value-000000000000000001")
	cells := []struct {
		name          string
		endpoint      string
		allowLoopback bool
	}{
		{"https valid", "https://billing.example", false},
		{"https valid loopback allowed", "https://billing.example", true},
		{"http non-loopback", "http://billing.example/token", false},
		{"http non-loopback loopback allowed", "http://billing.example/token", true},
		{"http localhost", "http://localhost:9090", false},
		{"http localhost loopback allowed", "http://localhost:9090", true},
		{"http 127.0.0.1", "http://127.0.0.1:9090", false},
		{"http 127.0.0.1 loopback allowed", "http://127.0.0.1:9090", true},
		{"userinfo", "https://u:p@billing.example", false},
		{"fragment", "https://billing.example#frag", false},
		{"raw query", "https://billing.example?x=1", false},
		{"bare query", "https://billing.example?", false},
		{"empty", "", false},
		{"whitespace", "   ", false},
	}
	for _, tc := range cells {
		t.Run(tc.name, func(t *testing.T) {
			cfg := commercialTestConfig()
			cfg.Billing.AllowInsecureLoopback = tc.allowLoopback
			cfg.Billing.Endpoint = tc.endpoint
			gateErr := cfg.validateCommercialIntegrations()

			_, connectorErr := billingconnector.NewClient(billingconnector.Config{
				Endpoint: tc.endpoint, Timeout: 5 * time.Second, MaxBodyBytes: 256 << 10,
				AllowInsecureLoopback: tc.allowLoopback,
				Bindings: map[string]billingconnector.BindingConfig{
					"tenant-one": {TokenURL: "https://sso.example/token", ClientID: "billing-client",
						ClientSecret: "billing-secret-value-0000000000000001",
						Scope:        billingEntitlementScope + " " + billingMeteringScope,
						Resources:    []string{billingResource}},
				},
			}, nil)
			if (gateErr == nil) != (connectorErr == nil) {
				t.Fatalf("parity drift for endpoint %q (loopback %v): gate accepts=%v connector accepts=%v",
					tc.endpoint, tc.allowLoopback, gateErr == nil, connectorErr == nil)
			}
		})
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
