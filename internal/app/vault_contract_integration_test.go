//go:build integration

package app

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

// TestBuildSourceConnectorsAgainstConfiguredVault drives the application
// connector composition seam against a deployed Vault. The connector package
// test owns the detailed wire assertions; this test proves app.New's source
// mapping selects the Vault adapter, carries the tenant/region settings, and
// does not accidentally expose undeployed command interfaces.
func TestBuildSourceConnectorsAgainstConfiguredVault(t *testing.T) {
	if strings.TrimSpace(os.Getenv("AERO_ID_VAULT_CONTRACT_ENDPOINT")) == "" {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("AERO_ID_VAULT_CONTRACT_REQUIRED")), "true") {
			t.Fatal("AERO_ID_VAULT_CONTRACT_ENDPOINT is required for the Vault wiring gate")
		}
		t.Skip("real Vault contract deployment is not configured")
	}
	value := vaultAppContractSource(t)
	cfg := &config.Config{App: config.AppConfig{Env: contractEnv("AERO_ID_VAULT_CONTRACT_ENVIRONMENT", "ci")}}
	cfg.Sources.AeroVault = value
	connectors, err := buildSourceConnectors(cfg, metrics.NewRegistry())
	if err != nil {
		t.Fatalf("build Vault source connector: %v", err)
	}
	if len(connectors) != 1 || connectors[0].Source() != connector.SourceAeroVault {
		t.Fatalf("source connectors = %#v, want one aero-vault connector", connectors)
	}
	if _, ok := connectors[0].(connector.CommandConnector); ok {
		t.Fatal("application wiring enabled an undeployed Vault command capability")
	}
	if _, ok := connectors[0].(connector.OperationStatusConnector); ok {
		t.Fatal("application wiring enabled an undeployed Vault status capability")
	}
	accountID, err := idgen.Parse(requiredContractEnv(t, "AERO_ID_VAULT_CONTRACT_ACCOUNT_ID"))
	if err != nil {
		t.Fatalf("AERO_ID_VAULT_CONTRACT_ACCOUNT_ID must be a non-zero UUID")
	}
	region := contractEnv("AERO_ID_VAULT_CONTRACT_REGION", "local")
	tenant := requiredContractEnv(t, "AERO_ID_VAULT_CONTRACT_TENANT_ID")
	summary, err := connectors[0].Summary(t.Context(), connector.SummaryRequest{
		AccountID: accountID, CanonicalUID: requiredContractEnv(t, "AERO_ID_VAULT_CONTRACT_CANONICAL_UID"),
		ActorUID: "aero-id-app-contract-test", TenantID: tenant, RequestID: "aero-id-app-vault-contract",
		Datasets: []domain.AccountDataset{"aero-vault.tenants"}, Region: region,
	})
	if err != nil {
		t.Fatalf("wired Vault summary: %v", err)
	}
	if summary == nil || summary.Source != connector.SourceAeroVault || summary.SourceRegion != region || !summary.Complete {
		t.Fatalf("wired Vault summary = %#v, want complete region %q", summary, region)
	}
}

func vaultAppContractSource(t *testing.T) config.SourceEndpointConfig {
	t.Helper()
	return config.SourceEndpointConfig{
		Endpoint:    requiredContractEnv(t, "AERO_ID_VAULT_CONTRACT_ENDPOINT"),
		SummaryPath: "", Region: contractEnv("AERO_ID_VAULT_CONTRACT_REGION", "local"),
		OAuthTokenURL: requiredContractEnv(t, "AERO_ID_VAULT_CONTRACT_TOKEN_URL"),
		OAuthClientID: requiredContractEnv(t, "AERO_ID_VAULT_CONTRACT_CLIENT_ID"),
		OAuthSecret:   requiredContractEnv(t, "AERO_ID_VAULT_CONTRACT_CLIENT_SECRET"), OAuthScope: "read",
		OAuthResource: "aero-vault", OAuthTenantID: requiredContractEnv(t, "AERO_ID_VAULT_CONTRACT_TENANT_ID"),
		OAuthAllowedHosts: csvContractEnv(t, "AERO_ID_VAULT_CONTRACT_OAUTH_ALLOWED_HOSTS"),
		Timeout:           durationContractEnv(t, "AERO_ID_VAULT_CONTRACT_TIMEOUT", 5*time.Second),
		MaxAttempts:       intContractEnv(t, "AERO_ID_VAULT_CONTRACT_MAX_ATTEMPTS", 3), RetryBackoff: 25 * time.Millisecond,
		TLSCAFile:          os.Getenv("AERO_ID_VAULT_CONTRACT_TLS_CA_FILE"),
		TLSCertFile:        os.Getenv("AERO_ID_VAULT_CONTRACT_TLS_CERT_FILE"),
		TLSKeyFile:         os.Getenv("AERO_ID_VAULT_CONTRACT_TLS_KEY_FILE"),
		TLSServerName:      os.Getenv("AERO_ID_VAULT_CONTRACT_TLS_SERVER_NAME"),
		OAuthTLSCAFile:     os.Getenv("AERO_ID_VAULT_CONTRACT_OAUTH_TLS_CA_FILE"),
		OAuthTLSCertFile:   os.Getenv("AERO_ID_VAULT_CONTRACT_OAUTH_TLS_CERT_FILE"),
		OAuthTLSKeyFile:    os.Getenv("AERO_ID_VAULT_CONTRACT_OAUTH_TLS_KEY_FILE"),
		OAuthTLSServerName: os.Getenv("AERO_ID_VAULT_CONTRACT_OAUTH_TLS_SERVER_NAME"),
	}
}

func requiredContractEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required for the Vault wiring gate", name)
	}
	return value
}

func csvContractEnv(t *testing.T, name string) []string {
	t.Helper()
	values := strings.Split(strings.TrimSpace(requiredContractEnv(t, name)), ",")
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			t.Fatalf("%s contains an empty OAuth origin", name)
		}
		result = append(result, value)
	}
	return result
}

func contractEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationContractEnv(t *testing.T, name string, fallback time.Duration) time.Duration {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		t.Fatalf("%s must be a positive duration", name)
	}
	return parsed
}

func intContractEnv(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		t.Fatalf("%s must be a positive integer", name)
	}
	return parsed
}
