package app

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/connector/aerovault"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

func TestSourceHTTPConfigCarriesAeroVaultContractSettings(t *testing.T) {
	value := config.SourceEndpointConfig{
		Endpoint: "https://vault.example", SummaryPath: "/v1/internal/account-summary", ErasePath: "/erase", StatusPath: "/status",
		Region: "us-east-1", BearerToken: "legacy-token", OAuthTokenURL: "https://sso.example/token",
		OAuthClientID: "vault-client", OAuthSecret: "vault-secret", OAuthScope: "read", OAuthResource: "aero-vault",
		OAuthAllowedHosts: []string{"sso.example"}, OAuthTLSCAFile: "/run/secrets/sso-ca.pem",
		OAuthTLSCertFile: "/run/secrets/sso-client.pem", OAuthTLSKeyFile: "/run/secrets/sso-client.key",
		OAuthTLSServerName: "sso.example", OAuthTenantID: "tenant-a", Timeout: time.Second,
		MaxBodyBytes: 1024, MaxConcurrency: 8, QueryMaxConcurrency: 5, CommandMaxConcurrency: 2,
		HealthMaxConcurrency: 1, MaxAttempts: 3, RetryBackoff: 10 * time.Millisecond,
	}
	got := sourceHTTPConfig(value, "production", metrics.NewRegistry())
	if got.Endpoint != value.Endpoint || got.SummaryPath != value.SummaryPath || got.ErasePath != value.ErasePath ||
		got.OperationStatusPath != value.StatusPath || got.Region != value.Region || got.BearerToken != value.BearerToken ||
		got.OAuthTenantID != value.OAuthTenantID || got.OAuthAllowedHosts[0] != value.OAuthAllowedHosts[0] {
		t.Fatalf("Vault source mapping = %#v", got)
	}
	if got.OAuth.TokenURL != value.OAuthTokenURL || got.OAuth.ClientID != value.OAuthClientID ||
		got.OAuth.ClientSecret != value.OAuthSecret || got.OAuth.Scope != value.OAuthScope || got.OAuth.Resource != value.OAuthResource ||
		got.OAuthTLSCAFile != value.OAuthTLSCAFile || got.OAuthTLSServerName != value.OAuthTLSServerName ||
		got.OAuthTLSCertFile != value.OAuthTLSCertFile || got.OAuthTLSKeyFile != value.OAuthTLSKeyFile {
		t.Fatalf("Vault OAuth mapping = %#v/%#v", got.OAuth, got)
	}
}

func TestBuildSourceConnectorsWiresVaultCapabilitiesExplicitly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capability config.SourceCapabilitiesConfig
		erasePath  string
		statusPath string
		wantErase  bool
		wantStatus bool
	}{
		{name: "read only"},
		{name: "erase", capability: config.SourceCapabilitiesConfig{Erase: true}, erasePath: "/erase", wantErase: true},
		{name: "status", capability: config.SourceCapabilitiesConfig{OperationStatus: true}, statusPath: "/status", wantStatus: true},
		{name: "both", capability: config.SourceCapabilitiesConfig{Erase: true, OperationStatus: true}, erasePath: "/erase", statusPath: "/status", wantErase: true, wantStatus: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{App: config.AppConfig{Env: "test"}}
			cfg.Sources.AeroVault = config.SourceEndpointConfig{
				Endpoint: "https://vault.example", SummaryPath: aerovault.DefaultSummaryPath,
				ErasePath: tc.erasePath, StatusPath: tc.statusPath, Capabilities: tc.capability,
				OAuthTokenURL: "https://sso.example/token", OAuthClientID: "vault-client", OAuthSecret: "vault-secret",
				OAuthScope: "read", OAuthResource: "aero-vault", OAuthAllowedHosts: []string{"sso.example"}, OAuthTenantID: "tenant-a",
			}
			connectors, err := buildSourceConnectors(cfg, metrics.NewRegistry())
			if err != nil {
				t.Fatalf("buildSourceConnectors: %v", err)
			}
			if len(connectors) != 1 || connectors[0].Source() != connector.SourceAeroVault {
				t.Fatalf("connectors = %#v, want one Vault connector", connectors)
			}
			_, gotErase := connectors[0].(connector.CommandConnector)
			_, gotStatus := connectors[0].(connector.OperationStatusConnector)
			if gotErase != tc.wantErase || gotStatus != tc.wantStatus {
				t.Fatalf("Vault optional interfaces = erase:%t/status:%t, want erase:%t/status:%t", gotErase, gotStatus, tc.wantErase, tc.wantStatus)
			}
		})
	}
}

func TestSourceHTTPConfigCarriesAeroIMTargetAssertionSettings(t *testing.T) {
	value := config.SourceEndpointConfig{
		Endpoint: "https://im.example", AllowInsecureLoopback: true, SummaryPath: "/internal/account-summary",
		OAuthTokenURL: "https://token.example/token", OAuthClientID: "im-client", OAuthSecret: "oauth-secret",
		OAuthScope: "aero.account.summary.read", OAuthResource: "aero-im", OAuthAllowedHosts: []string{"token.example"},
		TargetAssertionKeyFile: "/run/secrets/target-key.pem", TargetAssertionKeyEnv: "AERO_ID_TARGET_KEY",
		TargetAssertionJWKSURL: "https://aero-id.example/jwks", TargetAssertionTrustDomain: "https://aero-id.example/assertions",
		TargetAssertionKeyID: "target-key", TargetAssertionIssuer: "https://aero-id.example/assertions",
		TargetAssertionAudience: "aero-im-account-summary", TargetAssertionSubject: "aero-id-sync",
		TargetAssertionClientID: "im-client", TargetAssertionLifetime: time.Minute,
	}
	got := sourceHTTPConfig(value, "test", metrics.NewRegistry())
	if got.Endpoint != value.Endpoint || got.Environment != "test" || !got.AllowInsecureLoopback {
		t.Fatalf("source endpoint mapping = %#v", got)
	}
	if got.TargetAssertionKeyFile != value.TargetAssertionKeyFile || got.TargetAssertionKeyEnv != value.TargetAssertionKeyEnv ||
		got.TargetAssertionJWKSURL != value.TargetAssertionJWKSURL || got.TargetAssertionTrustDomain != value.TargetAssertionTrustDomain ||
		got.TargetAssertionKeyID != value.TargetAssertionKeyID || got.TargetAssertionIssuer != value.TargetAssertionIssuer ||
		got.TargetAssertionAudience != value.TargetAssertionAudience || got.TargetAssertionSubject != value.TargetAssertionSubject ||
		got.TargetAssertionClientID != value.TargetAssertionClientID || got.TargetAssertionLifetime != value.TargetAssertionLifetime {
		t.Fatalf("target assertion mapping = %#v", got)
	}
	if got.OAuth.TokenURL != value.OAuthTokenURL || got.OAuth.ClientID != value.OAuthClientID ||
		got.OAuth.ClientSecret != value.OAuthSecret || got.OAuth.Scope != value.OAuthScope || got.OAuth.Resource != value.OAuthResource ||
		!reflect.DeepEqual(got.OAuthAllowedHosts, value.OAuthAllowedHosts) {
		t.Fatalf("OAuth mapping = %#v/%#v", got.OAuth, got.OAuthAllowedHosts)
	}
}

func TestBuildSourceConnectorsRegistersExplicitLoopbackAeroIM(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "test"}}
	cfg.Sources.AeroIM = config.SourceEndpointConfig{
		Endpoint: "http://127.0.0.1:8080", AllowInsecureLoopback: true,
	}
	connectors, err := buildSourceConnectors(cfg, metrics.NewRegistry())
	if err != nil {
		t.Fatalf("buildSourceConnectors error = %v", err)
	}
	if len(connectors) != 1 || connectors[0].Source() != connector.SourceAeroIM {
		t.Fatalf("registered connectors = %#v, want one Aero IM connector", connectors)
	}
}

func TestBuildSourceConnectorsDoesNotRegisterRejectedAeroIM(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	cfg := &config.Config{App: config.AppConfig{Env: "test"}}
	cfg.Sources.AeroIM = config.SourceEndpointConfig{
		Endpoint: "https://im.example", OAuthTokenURL: "https://token.example/token", OAuthAllowedHosts: []string{"token.example"},
		OAuthClientID: "im-client", OAuthSecret: "oauth-secret", OAuthScope: "aero.account.summary.read", OAuthResource: "aero-im",
	}
	connectors, err := buildSourceConnectors(cfg, metrics.NewRegistry())
	if err == nil || !strings.Contains(err.Error(), "aero-im connector") || !strings.Contains(err.Error(), "target assertion") {
		t.Fatalf("buildSourceConnectors error = %v, want wrapped target assertion error", err)
	}
	if strings.Contains(err.Error(), "oauth-secret") || len(connectors) != 0 || calls.Load() != 0 {
		t.Fatalf("rejected connector leaked, was registered, or made remote calls: connectors=%v calls=%d error=%v", connectors, calls.Load(), err)
	}
}

func TestBuildSourceConnectorsUsesExplicitSnaplinkCapabilities(t *testing.T) {
	cfg := &config.Config{App: config.AppConfig{Env: "test"}}
	cfg.Sources.Snaplink = config.SourceEndpointConfig{Endpoint: "http://127.0.0.1:8080",
		AllowInsecureLoopback: true, Capabilities: config.SourceCapabilitiesConfig{Summary: true}}
	connectors, err := buildSourceConnectors(cfg, metrics.NewRegistry())
	if err != nil {
		t.Fatalf("summary-only Snaplink construction: %v", err)
	}
	if len(connectors) != 1 || connectors[0].Source() != connector.SourceSnaplink {
		t.Fatalf("connectors = %#v, want one Snaplink connector", connectors)
	}
	if _, ok := connectors[0].(connector.CommandConnector); ok {
		t.Fatal("composition root exposed Snaplink CommandConnector in summary-only mode")
	}
	if _, ok := connectors[0].(connector.OperationStatusConnector); ok {
		t.Fatal("composition root exposed Snaplink OperationStatusConnector in summary-only mode")
	}

	cfg.Sources.Snaplink.Capabilities.Summary = false
	if _, err := buildSourceConnectors(cfg, metrics.NewRegistry()); err == nil || !strings.Contains(err.Error(), "capabilities.summary") {
		t.Fatalf("summary-disabled Snaplink construction error = %v, want explicit capability validation", err)
	}
}
