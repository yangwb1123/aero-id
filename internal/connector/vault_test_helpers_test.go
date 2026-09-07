package connector

import (
	"net/url"
	"testing"
	"time"
)

const vaultTestTenant = "tenant-a"

// newVaultTestConnector exercises the strict Vault constructor without making
// the unit suite depend on a token endpoint. The OAuth client is constructed
// normally; its cached contract-shaped token is installed only after
// construction so the source handler can focus on the request contract.
func newVaultTestConnector(t *testing.T, endpoint string, cfg HTTPConfig) *JSONSummaryConnector {
	t.Helper()
	cfg.Endpoint = endpoint
	cfg.AllowInsecureLoopback = true
	cfg.Environment = "test"
	if cfg.OAuthTenantID == "" {
		cfg.OAuthTenantID = vaultTestTenant
	}
	if cfg.OAuth.TokenURL == "" {
		cfg.OAuth.TokenURL = endpoint + "/token"
	}
	if len(cfg.OAuthAllowedHosts) == 0 {
		cfg.OAuthAllowedHosts = []string{oauthTestOrigin(t, endpoint)}
	}
	cfg.OAuth.AllowedHosts = append([]string(nil), cfg.OAuthAllowedHosts...)
	if cfg.OAuth.ClientID == "" {
		cfg.OAuth.ClientID = "vault-client"
	}
	if cfg.OAuth.ClientSecret == "" {
		cfg.OAuth.ClientSecret = "vault-client-secret"
	}
	if cfg.OAuth.Scope == "" {
		cfg.OAuth.Scope = "read"
	}
	if cfg.OAuth.Resource == "" {
		cfg.OAuth.Resource = "aero-vault"
	}
	client, err := NewJSONSummaryConnector(SourceAeroVault, cfg)
	if err != nil {
		t.Fatalf("new Vault test connector: %v", err)
	}
	if client.tokens == nil {
		t.Fatal("Vault test connector did not construct an OAuth client")
	}
	client.tokens.mu.Lock()
	client.tokens.token = vaultTestAccessToken
	client.tokens.refreshAt = time.Now().UTC().Add(time.Hour)
	client.tokens.generation++
	client.tokens.mu.Unlock()
	return client
}

func oauthTestOrigin(t *testing.T, endpoint string) string {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse test endpoint: %v", err)
	}
	return parsed.Scheme + "://" + parsed.Host
}

// validOAuthAccessToken accepts the token's token68 characters. The Vault
// constructor additionally requires its JWT payload to identify the client and
// audience; the signature is intentionally irrelevant to this adapter test.
//
//nolint:gosec // structurally valid, non-secret JWT fixture used only by tests.
const vaultTestAccessToken = "eyJhbGciOiJSUzI1NiIsInR5cCI6ImF0K2p3dCJ9.eyJpc3MiOiJodHRwczovL3NuYXBsaW5rLmV4YW1wbGUiLCJzdWIiOiJ2YXVsdC1jbGllbnQiLCJhdWQiOiJhZXJvLXZhdWx0IiwiY2xpZW50X2lkIjoidmF1bHQtY2xpZW50IiwianRpIjoidmF1bHQtdGVzdC0xIiwic2NvcGUiOiJyZWFkIiwiZXhwIjo0MTAyNDQ0ODAwfQ.signature"
