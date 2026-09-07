package sourceauth

import (
	"strings"
	"testing"
)

func TestEvaluateAeroIMAuthenticationMatrix(t *testing.T) {
	cases := []struct {
		name        string
		endpoint    string
		environment string
		allow       bool
		bearer      string
		cert        string
		key         string
		wantMode    Mode
		wantErr     string
	}{
		{name: "missing auth", endpoint: "https://im.example", wantErr: "service authentication is required"},
		{name: "loopback opt in", endpoint: "http://localhost", environment: "test", allow: true, wantMode: ModeLoopback},
		{name: "loopback opt in ipv4", endpoint: "http://127.0.0.1:8080", environment: "dev", allow: true, wantMode: ModeLoopback},
		{name: "loopback opt in ipv6", endpoint: "http://[::1]:8080", environment: "test", allow: true, wantMode: ModeLoopback},
		{name: "loopback flag missing", endpoint: "http://127.0.0.1", wantErr: "service authentication is required"},
		{name: "non loopback flag", endpoint: "https://im.example", environment: "test", allow: true, wantErr: "exact loopback"},
		{name: "production flag", endpoint: "http://localhost", environment: "staging", allow: true, wantErr: "not allowed"},
		{name: "static bearer", endpoint: "https://im.example", bearer: "  static-token  ", wantMode: ModeStaticBearer},
		{name: "blank bearer", endpoint: "https://im.example", bearer: "   ", wantErr: "service authentication is required"},
		{name: "control bearer", endpoint: "https://im.example", bearer: "static\ntoken", wantErr: "bearer token is invalid"},
		{name: "mtls", endpoint: "https://im.example", cert: "/cert", key: "/key", wantMode: ModeMTLS},
		{name: "mtls http", endpoint: "http://localhost", cert: "/cert", key: "/key", wantErr: "mTLS requires an HTTPS endpoint"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := Evaluate(Config{Source: aeroIMSource, Endpoint: tc.endpoint, Environment: tc.environment,
				AllowInsecureLoopback: tc.allow, BearerToken: tc.bearer,
				TLSCertFile: tc.cert, TLSKeyFile: tc.key})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if decision.Mode != tc.wantMode {
				t.Fatalf("mode = %q, want %q", decision.Mode, tc.wantMode)
			}
		})
	}
}

func TestIsExplicitNonProductionLoopbackMatrix(t *testing.T) {
	cases := []struct {
		name        string
		endpoint    string
		environment string
		allow       bool
		want        bool
	}{
		{name: "localhost", endpoint: "http://localhost:8080", environment: "test", allow: true, want: true},
		{name: "ipv4", endpoint: "http://127.0.0.1:8080", environment: "dev", allow: true, want: true},
		{name: "ipv6", endpoint: "http://[::1]:8080", environment: "test", allow: true, want: true},
		{name: "https loopback", endpoint: "https://localhost:8443", environment: "test", allow: true, want: true},
		{name: "flag disabled", endpoint: "http://localhost:8080", environment: "test", want: false},
		{name: "environment missing", endpoint: "http://localhost:8080", allow: true, want: false},
		{name: "prod", endpoint: "http://localhost:8080", environment: "prod", allow: true, want: false},
		{name: "production", endpoint: "http://localhost:8080", environment: "production", allow: true, want: false},
		{name: "staging", endpoint: "http://localhost:8080", environment: "staging", allow: true, want: false},
		{name: "unknown environment", endpoint: "http://localhost:8080", environment: "qa", allow: true, want: false},
		{name: "preproduction environment", endpoint: "http://localhost:8080", environment: "preprod", allow: true, want: false},
		{name: "confusable environment", endpoint: "http://localhost:8080", environment: "prod\u0430", allow: true, want: false},
		{name: "control environment", endpoint: "http://localhost:8080", environment: "test\x00", allow: true, want: false},
		{name: "private ipv4", endpoint: "https://192.168.1.10:8443", environment: "test", allow: true, want: false},
		{name: "loopback alias", endpoint: "http://localhost.localdomain:8080", environment: "test", allow: true, want: false},
		{name: "non-loopback https", endpoint: "https://im.example", environment: "test", allow: true, want: false},
		{name: "malformed", endpoint: "not-an-endpoint", environment: "test", allow: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsExplicitNonProductionLoopback(tc.endpoint, tc.environment, tc.allow); got != tc.want {
				t.Fatalf("IsExplicitNonProductionLoopback() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApprovedNonProductionEnvironmentIsExplicit(t *testing.T) {
	for _, environment := range []string{"local", "dev", "development", "test", "testing", "ci", " DEV "} {
		if !ApprovedNonProductionEnvironment(environment) {
			t.Errorf("ApprovedNonProductionEnvironment(%q) = false, want true", environment)
		}
	}
	for _, environment := range []string{"", "qa", "uat", "preprod", "live", "prod-blue", "production", "staging", "test\\x00", "prodа"} {
		if ApprovedNonProductionEnvironment(environment) {
			t.Errorf("ApprovedNonProductionEnvironment(%q) = true, want false", environment)
		}
	}
}

func TestEvaluateRequiresAuthenticationForSnaplink(t *testing.T) {
	_, err := Evaluate(Config{Source: "snaplink", Endpoint: "https://source.example"})
	if err == nil || !strings.Contains(err.Error(), "service authentication is required") {
		t.Fatalf("Snaplink without authentication error = %v, want service authentication required", err)
	}
}

func TestEvaluateRequiresAuthenticationForAeroVault(t *testing.T) {
	_, err := Evaluate(Config{Source: "aero-vault", Endpoint: "https://source.example"})
	if err == nil || !strings.Contains(err.Error(), "requires Snaplink OAuth client credentials") {
		t.Fatalf("Aero Vault without authentication error = %v, want source-contract authentication error", err)
	}
}

func TestEvaluateAeroVaultRequiresBoundOAuthClientCredentials(t *testing.T) {
	cases := []struct {
		name   string
		config Config
	}{
		{name: "static bearer", config: Config{BearerToken: "vault-token"}},
		{name: "mTLS only", config: Config{TLSCertFile: "/cert", TLSKeyFile: "/key"}},
		{name: "loopback compatibility", config: Config{AllowInsecureLoopback: true, Environment: "test"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.config.Source = aeroVaultSource
			tc.config.Endpoint = "https://vault.example"
			if tc.config.AllowInsecureLoopback {
				tc.config.Endpoint = "http://127.0.0.1:8080"
			}
			if _, err := Evaluate(tc.config); err == nil || !strings.Contains(err.Error(), "Snaplink OAuth") {
				t.Fatalf("Evaluate() error = %v, want Vault OAuth requirement", err)
			}
		})
	}

	decision, err := Evaluate(Config{Source: aeroVaultSource, Endpoint: "https://vault.example",
		OAuth: OAuthConfig{TokenURL: "https://token.example/token", ClientID: "vault-client",
			ClientSecret: "secret", Scope: VaultOAuthScope, Resource: VaultOAuthResource,
			AllowedHosts: []string{"token.example"}}})
	if err != nil || decision.Mode != ModeOAuth {
		t.Fatalf("valid Vault OAuth decision = %#v/%v, want oauth", decision, err)
	}
}
