package config

import (
	"strings"
	"testing"
)

const aeroIMTargetAssertionRequiredError = "sources.aero_im target assertion is required for an enabled non-loopback or production-like endpoint"

func loadAeroIMRequirementConfig(t *testing.T, environment, source string) (*Config, error) {
	t.Helper()
	t.Setenv("AERO_ID_SOURCES_AERO_IM_BEARER_TOKEN", "")
	t.Setenv("AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_SECRET", "")
	if strings.Contains(source, `bearer_token: "config-secret-bearer"`) {
		t.Setenv("AERO_ID_SOURCES_AERO_IM_BEARER_TOKEN", "config-secret-bearer")
	}
	if strings.Contains(source, `bearer_token: "source-bearer"`) {
		t.Setenv("AERO_ID_SOURCES_AERO_IM_BEARER_TOKEN", "source-bearer")
	}
	if strings.Contains(source, `oauth_client_secret: oauth-secret`) {
		t.Setenv("AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_SECRET", "oauth-secret")
	}
	source = strings.ReplaceAll(source, `bearer_token: "config-secret-bearer"`, `bearer_token: ""`)
	source = strings.ReplaceAll(source, `bearer_token: "source-bearer"`, `bearer_token: ""`)
	source = strings.ReplaceAll(source, `oauth_client_secret: oauth-secret`, `oauth_client_secret: ""`)
	content := sample
	if environment != "test" {
		content = strings.Replace(content, "env: test", "env: "+environment, 1)
	}
	if productionLikeEnvironment(environment) {
		t.Setenv("AERO_ID_CALLBACK_MASTER_KEY", testCallbackMasterKey)
		content = strings.Replace(content, `server:
  http: {host: 0.0.0.0, port: 8080, read_timeout: 15s, write_timeout: 15s, shutdown_timeout: 10s}`, `server:
  http: {host: 0.0.0.0, port: 8080, cors_origins: ["https://console.example"], read_timeout: 15s, write_timeout: 15s, shutdown_timeout: 10s}`, 1)
		content = strings.Replace(content, "sslmode: disable", "sslmode: require", 1)
		content = strings.Replace(content, `sso: {endpoint: "sso:9000", timeout: 5s, insecure: true, token_cache_ttl: 60s, user_cache_ttl: 300s}`, `sso: {endpoint: "sso:9000", timeout: 5s, insecure: false, authz_required: true, authz_legacy_fallback: false, token_cache_ttl: 60s, user_cache_ttl: 300s}`, 1)
	}
	return Load(writeTemp(t, content+"\nsources:\n  aero_im:\n"+source))
}

func TestLoadAeroIMTargetAssertionRequirementPreservesValidationPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		wantErr string
	}{
		{
			name: "missing source authentication wins",
			source: `
    endpoint: "https://im.example"
    region: local
`,
			wantErr: "sources.aero-im service authentication is required",
		},
		{
			name: "valid bearer reaches target assertion requirement",
			source: `
    endpoint: "https://im.example"
    region: local
    bearer_token: "config-secret-bearer"
`,
			wantErr: aeroIMTargetAssertionRequiredError,
		},
		{
			name: "valid OAuth reaches target assertion requirement",
			source: `
    endpoint: "https://im.example"
    region: local
    oauth_token_url: "https://token.example/token"
    oauth_allowed_hosts: [token.example]
    oauth_client_id: im-client
    oauth_client_secret: oauth-secret
    oauth_scope: source:read
    oauth_resource: im
`,
			wantErr: aeroIMTargetAssertionRequiredError,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadAeroIMRequirementConfig(t, "test", tc.source)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load error = %v, want containing %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "config-secret-bearer") || strings.Contains(err.Error(), "oauth-secret") {
				t.Fatalf("Load error disclosed a credential: %v", err)
			}
		})
	}
}

func TestLoadAeroIMTargetAssertionRequirementMatrix(t *testing.T) {
	cases := []struct {
		name        string
		environment string
		source      string
		wantErr     string
	}{
		{name: "localhost exception", environment: "test", source: `
    endpoint: "http://localhost:8080"
    allow_insecure_loopback: true
`},
		{name: "ipv4 exception", environment: "dev", source: `
    endpoint: "http://127.0.0.1:8080"
    allow_insecure_loopback: true
`},
		{name: "ipv6 exception", environment: "test", source: `
    endpoint: "http://[::1]:8080"
    allow_insecure_loopback: true
`},
		{name: "loopback flag disabled", environment: "test", source: `
    endpoint: "https://localhost:8443"
    bearer_token: "source-bearer"
`, wantErr: aeroIMTargetAssertionRequiredError},
		{name: "non-loopback bearer", environment: "test", source: `
    endpoint: "https://im.example"
    bearer_token: "source-bearer"
`, wantErr: aeroIMTargetAssertionRequiredError},
		{name: "prod loopback", environment: "prod", source: `
    endpoint: "https://localhost:8443"
    bearer_token: "source-bearer"
`, wantErr: aeroIMTargetAssertionRequiredError},
		{name: "production loopback", environment: "production", source: `
    endpoint: "https://localhost:8443"
    bearer_token: "source-bearer"
`, wantErr: aeroIMTargetAssertionRequiredError},
		{name: "staging loopback", environment: "staging", source: `
    endpoint: "https://localhost:8443"
    bearer_token: "source-bearer"
`, wantErr: aeroIMTargetAssertionRequiredError},
		{name: "partial assertion remains invalid on loopback", environment: "test", source: `
    endpoint: "http://127.0.0.1:8080"
    allow_insecure_loopback: true
    target_assertion_key_file: /run/secrets/target-key.pem
`, wantErr: "sources.aero_im.target_assertion_"},
		{name: "empty endpoint remains disabled", environment: "production", source: `
    endpoint: ""
    target_assertion_key_id: partial-only
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadAeroIMRequirementConfig(t, tc.environment, tc.source)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Load error = %v, want disabled/accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load error = %v, want containing %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "source-bearer") || strings.Contains(err.Error(), "/run/secrets/target-key.pem") {
				t.Fatalf("Load error disclosed sensitive configuration: %v", err)
			}
		})
	}
}
