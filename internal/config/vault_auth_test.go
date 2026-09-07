package config

import (
	"strings"
	"testing"
)

func TestLoadVaultRequiresAuthenticationWhenEnabled(t *testing.T) {
	content := sample + `
sources:
  aero_vault:
    endpoint: "https://vault.example"
    region: local
`
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "sources.aero-vault service authentication is required") {
		t.Fatalf("Load error = %v, want fail-closed Vault authentication", err)
	}
}

func TestLoadVaultLoopbackWithoutPrincipalIsRejected(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_AERO_VAULT_ALLOW_INSECURE_LOOPBACK", "true")
	content := sample + `
sources:
  aero_vault:
    endpoint: "http://127.0.0.1:8080"
    region: local
`
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "Snaplink OAuth") {
		t.Fatalf("Load error = %v, want PrincipalService authentication rejection", err)
	}
}

func TestLoadVaultCapabilityPathRequiresMatchingOptIn(t *testing.T) {
	t.Setenv("AERO_ID_SOURCES_AERO_VAULT_OAUTH_CLIENT_SECRET", "vault-oauth-client-secret")
	content := sample + `
sources:
  aero_vault:
    endpoint: "https://vault.example"
    region: local
    oauth_token_url: "https://token.example/token"
    oauth_allowed_hosts: [token.example]
    oauth_client_id: vault-client
    oauth_client_secret: ""
    oauth_scope: read
    oauth_resource: aero-vault
    oauth_tenant_id: tenant-a
    erase_path: /erase
`
	_, err := Load(writeTemp(t, content))
	if err == nil || !strings.Contains(err.Error(), "erase_path requires capabilities.erase") {
		t.Fatalf("Load error = %v, want capability/path mismatch", err)
	}
	if strings.Contains(err.Error(), "vault-token") {
		t.Fatalf("Load error disclosed bearer token: %v", err)
	}
}
