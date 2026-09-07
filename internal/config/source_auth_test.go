package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsUnauthenticatedAeroIMWithoutSecrets(t *testing.T) {
	content := sample + `
sources:
  aero_im:
    endpoint: "https://im.example"
    region: local
    bearer_token: "do-not-echo-this-token"
`
	content = strings.Replace(content, `bearer_token: "do-not-echo-this-token"`, `bearer_token: ""`, 1)
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "sources.aero-im service authentication is required") {
		t.Fatalf("Load error = %v, want fail-closed Aero IM authentication error", err)
	}
	if strings.Contains(err.Error(), "do-not-echo-this-token") {
		t.Fatalf("Load error disclosed an authentication secret: %v", err)
	}
}

func TestLoadAeroIMOAuthEnvironmentOverridesForTargetAssertion(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_AERO_IM_OAUTH_TOKEN_URL", "https://snaplink.example/token")
	t.Setenv("AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_ID", "aero-id-im-source")
	t.Setenv("AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_SECRET", "oauth-client-secret-from-secret")
	t.Setenv("AERO_ID_SOURCES_AERO_IM_OAUTH_SCOPE", "aero.account.summary.read")
	t.Setenv("AERO_ID_SOURCES_AERO_IM_OAUTH_RESOURCE", "aero-im")
	content := sample + `
sources:
  aero_im:
    endpoint: "https://aero-im.example"
    summary_path: /internal/account-summary
    region: local
    bearer_token: ""
    oauth_token_url: ""
    oauth_client_id: ""
    oauth_client_secret: ""
    oauth_scope: ""
    oauth_resource: ""
    oauth_allowed_hosts: [snaplink.example]
    target_assertion_key_file: ""
    target_assertion_key_env: AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_PRIVATE_KEY
    target_assertion_key_id: target-key-2026-08
    target_assertion_issuer: https://aero-id.example/assertions
    target_assertion_audience: aero-im-account-summary
    target_assertion_subject: aero-id-sync
    target_assertion_client_id: aero-id-im-source
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	got := cfg.Sources.AeroIM
	if got.OAuthTokenURL != "https://snaplink.example/token" || got.OAuthClientID != "aero-id-im-source" ||
		got.OAuthSecret != "oauth-client-secret-from-secret" || got.OAuthScope != "aero.account.summary.read" ||
		got.OAuthResource != "aero-im" {
		t.Fatal("AeroIM OAuth environment overrides did not land")
	}
}

func TestLoadRejectsInvalidAeroIMTargetAssertion(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_SECRET", "oauth-client-secret")
	const validConfig = sample + `
sources:
  aero_im:
    endpoint: "https://aero-im.example"
    summary_path: /internal/account-summary
    region: local
    oauth_token_url: "https://snaplink.example/token"
    oauth_allowed_hosts: [snaplink.example]
    oauth_client_id: aero-id-im-source
    oauth_client_secret: ""
    oauth_scope: aero.account.summary.read
    oauth_resource: aero-im
    target_assertion_key_file: /run/secrets/aero-id-target-key.pem
    target_assertion_key_id: target-key-2026-08
    target_assertion_issuer: "https://aero-id.example/assertions"
    target_assertion_audience: aero-im-account-summary
    target_assertion_subject: aero-id-sync
    target_assertion_client_id: aero-id-im-source
`
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "invalid audience",
			content: strings.Replace(validConfig, "target_assertion_audience: aero-im-account-summary", "target_assertion_audience: wrong-audience", 1),
			wantErr: "sources.aero_im.target_assertion_audience must be",
		},
		{
			name:    "missing issuer",
			content: strings.Replace(validConfig, `target_assertion_issuer: "https://aero-id.example/assertions"`, `target_assertion_issuer: ""`, 1),
			wantErr: "sources.aero_im.target_assertion_issuer is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.content))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadSourceWorkloadConcurrencyEnvironmentOverrides(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_SNAPLINK_QUERY_MAX_CONCURRENCY", "7")
	t.Setenv("AERO_ID_SOURCES_AERO_IM_COMMAND_MAX_CONCURRENCY", "5")
	content := sample + `
sources:
  snaplink:
    query_max_concurrency: 1
  aero_im:
    command_max_concurrency: 1
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if cfg.Sources.Snaplink.QueryMaxConcurrency != 7 {
		t.Fatalf("Snaplink.QueryMaxConcurrency = %d, want 7", cfg.Sources.Snaplink.QueryMaxConcurrency)
	}
	if cfg.Sources.AeroIM.CommandMaxConcurrency != 5 {
		t.Fatalf("AeroIM.CommandMaxConcurrency = %d, want 5", cfg.Sources.AeroIM.CommandMaxConcurrency)
	}
}

func TestLoadRejectsUnauthenticatedSnaplinkWithoutSecrets(t *testing.T) {
	content := sample + `
sources:
  snaplink:
    endpoint: "https://snaplink.example"
    region: local
    capabilities: {summary: true}
`
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "sources.snaplink service authentication is required") {
		t.Fatalf("Load error = %v, want fail-closed Snaplink authentication error", err)
	}
}

func TestLoadSnaplinkLoopbackCompatibilityUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_SNAPLINK_ALLOW_INSECURE_LOOPBACK", "true")
	content := sample + `
sources:
  snaplink:
    endpoint: "http://127.0.0.1:8080"
    region: local
    capabilities: {summary: true}
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load error = %v, want loopback compatibility to pass", err)
	}
	if !cfg.Sources.Snaplink.AllowInsecureLoopback {
		t.Fatal("Snaplink.AllowInsecureLoopback = false, want environment override true")
	}
}

func TestLoadAeroIMLoopbackCompatibilityUsesEnvironmentOverride(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_AERO_IM_ALLOW_INSECURE_LOOPBACK", "true")
	content := sample + `
sources:
  aero_im:
    endpoint: "http://127.0.0.1:8080"
    region: local
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load error = %v, want loopback compatibility override to pass", err)
	}
	if !cfg.Sources.AeroIM.AllowInsecureLoopback {
		t.Fatal("AeroIM.AllowInsecureLoopback = false, want environment override true")
	}
}

func TestLoadRequiresAeroVaultAuthenticationWhenEnabled(t *testing.T) {
	content := sample + `
sources:
  aero_vault:
    endpoint: "https://vault.example"
    region: local
`
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "sources.aero-vault service authentication is required") {
		t.Fatalf("Load error = %v, want fail-closed Aero Vault authentication error", err)
	}
}

func TestLoadAeroVaultAuthAndCapabilitiesEnvironmentOverrides(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_AERO_VAULT_ALLOW_INSECURE_LOOPBACK", "true")
	t.Setenv("AERO_ID_SOURCES_AERO_VAULT_OAUTH_CLIENT_SECRET", "vault-oauth-client-secret")
	t.Setenv("AERO_ID_SOURCES_AERO_VAULT_CAPABILITIES_ERASE", "true")
	t.Setenv("AERO_ID_SOURCES_AERO_VAULT_CAPABILITIES_OPERATION_STATUS", "true")
	content := sample + `
sources:
  aero_vault:
    endpoint: "http://127.0.0.1:8080"
    region: local
    erase_path: /erase
    status_path: /status
    oauth_token_url: "http://127.0.0.1:8080/token"
    oauth_allowed_hosts: ["http://127.0.0.1:8080"]
    oauth_client_id: vault-client
    oauth_client_secret: ""
    oauth_scope: read
    oauth_resource: aero-vault
    oauth_tenant_id: tenant-a
`
	cfg, err := Load(writeTemp(t, content))
	if err != nil {
		t.Fatalf("Load error = %v, want environment-bound Vault settings to pass", err)
	}
	got := cfg.Sources.AeroVault
	if !got.AllowInsecureLoopback || !got.Capabilities.Erase || !got.Capabilities.OperationStatus {
		t.Fatalf("Aero Vault environment settings = %#v, want loopback and both capabilities enabled", got)
	}
}
