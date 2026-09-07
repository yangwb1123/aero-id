//go:build integration

package aerovault

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/connector"
	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"
)

const (
	vaultContractEndpointEnv     = "AERO_ID_VAULT_CONTRACT_ENDPOINT"
	vaultContractTokenURLEnv     = "AERO_ID_VAULT_CONTRACT_TOKEN_URL"
	vaultContractClientIDEnv     = "AERO_ID_VAULT_CONTRACT_CLIENT_ID"
	vaultContractClientSecretEnv = "AERO_ID_VAULT_CONTRACT_CLIENT_SECRET"
	vaultContractTenantEnv       = "AERO_ID_VAULT_CONTRACT_TENANT_ID"
	vaultContractAccountIDEnv    = "AERO_ID_VAULT_CONTRACT_ACCOUNT_ID"
	vaultContractCanonicalUIDEnv = "AERO_ID_VAULT_CONTRACT_CANONICAL_UID"
	vaultContractAllowedHostsEnv = "AERO_ID_VAULT_CONTRACT_OAUTH_ALLOWED_HOSTS"
	vaultContractRegionEnv       = "AERO_ID_VAULT_CONTRACT_REGION"
	vaultContractEnvironmentEnv  = "AERO_ID_VAULT_CONTRACT_ENVIRONMENT"
	vaultContractDatasetsEnv     = "AERO_ID_VAULT_CONTRACT_DATASETS"
	vaultContractTimeoutEnv      = "AERO_ID_VAULT_CONTRACT_TIMEOUT"
	vaultContractMaxAttemptsEnv  = "AERO_ID_VAULT_CONTRACT_MAX_ATTEMPTS"
	vaultContractRequiredEnv     = "AERO_ID_VAULT_CONTRACT_REQUIRED"
)

type deployedVaultContract struct {
	config  connector.HTTPConfig
	request connector.SummaryRequest
	region  string
	tenant  string
}

// TestVaultContractAgainstDeployment is deliberately an opt-in integration
// test. It talks to a deployed Aero Vault and its real Snaplink token
// authority; it never substitutes an httptest handler for the source service.
// CI runs this test when the contract environment is provisioned, while the
// ordinary Docker-only integration suite skips it when no deployment exists.
func TestVaultContractAgainstDeployment(t *testing.T) {
	contract := loadDeployedVaultContract(t)
	client, err := New(contract.config)
	if err != nil {
		t.Fatalf("construct deployed Vault connector: %v", err)
	}
	assertVaultReadOnly(t, client)

	t.Run("health path and authentication", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), contract.config.Timeout)
		defer cancel()
		if err := client.Health(ctx); err != nil {
			t.Fatalf("GET /healthz through deployed Vault: %v", err)
		}
	})
	t.Run("canonical summary path and tenant context", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), contract.config.Timeout)
		defer cancel()
		summary, err := client.Summary(ctx, contract.request)
		if err != nil {
			t.Fatalf("GET /v1/internal/account-summary through deployed Vault: %v", err)
		}
		assertVaultSummary(t, summary, contract)
	})
}

// TestVaultContractRejectsUnauthenticatedConstruction uses the real deployment
// endpoint only as the target shape. The panic transport proves that missing
// machine authentication is rejected before any source or token request.
func TestVaultContractRejectsUnauthenticatedConstruction(t *testing.T) {
	contract := loadDeployedVaultContract(t)
	bad := contract.config
	bad.OAuth = connector.OAuthClientCredentialsConfig{}
	bad.OAuthAllowedHosts = nil
	bad.Client = &http.Client{Transport: httpPanicRoundTripper{}}
	if _, err := New(bad); err == nil || !strings.Contains(err.Error(), "Snaplink OAuth") {
		t.Fatalf("unauthenticated Vault construction error = %v, want local Snaplink OAuth rejection", err)
	}
}

// TestVaultContractCallerDeadlineIsBounded checks the connector's caller
// deadline path against the configured deployment. The deadline is expired
// before the call, so this test cannot mutate or probe Vault while still
// proving that a stalled remote operation cannot escape its budget.
func TestVaultContractCallerDeadlineIsBounded(t *testing.T) {
	contract := loadDeployedVaultContract(t)
	client, err := New(contract.config)
	if err != nil {
		t.Fatalf("construct deployed Vault connector: %v", err)
	}
	deadline := time.Now().Add(-time.Millisecond)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	started := time.Now()
	_, err = client.Summary(ctx, contract.request)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("expired caller deadline took %s", elapsed)
	}
	var remote *connector.RemoteError
	if !errors.As(err, &remote) || remote.Kind != connector.ErrorTimeout || !remote.Temporary {
		t.Fatalf("expired caller deadline = %+v, want temporary timeout RemoteError", err)
	}
}

func loadDeployedVaultContract(t *testing.T) deployedVaultContract {
	t.Helper()
	endpoint := strings.TrimSpace(os.Getenv(vaultContractEndpointEnv))
	if endpoint == "" {
		if strings.EqualFold(strings.TrimSpace(os.Getenv(vaultContractRequiredEnv)), "true") {
			t.Fatalf("%s is required for the real Vault contract suite", vaultContractEndpointEnv)
		}
		t.Skip("real Vault contract deployment is not configured")
	}
	region := optionalEnv(vaultContractRegionEnv, "local")
	environment := optionalEnv(vaultContractEnvironmentEnv, "ci")
	tenant := requiredVaultContractEnv(t, vaultContractTenantEnv)
	accountID, err := idgen.Parse(requiredVaultContractEnv(t, vaultContractAccountIDEnv))
	if err != nil {
		t.Fatalf("%s must be a non-zero UUID", vaultContractAccountIDEnv)
	}
	canonicalUID := requiredVaultContractEnv(t, vaultContractCanonicalUIDEnv)
	tokenURL := requiredVaultContractEnv(t, vaultContractTokenURLEnv)
	clientID := requiredVaultContractEnv(t, vaultContractClientIDEnv)
	clientSecret := requiredVaultContractEnv(t, vaultContractClientSecretEnv)
	allowedHosts := csvEnv(t, vaultContractAllowedHostsEnv)
	datasets := vaultContractDatasets(t)
	timeout := durationEnv(t, vaultContractTimeoutEnv, 5*time.Second)
	maxAttempts := intEnv(t, vaultContractMaxAttemptsEnv, 3)
	return deployedVaultContract{
		config: connector.HTTPConfig{
			Endpoint: endpoint, Environment: environment, Region: region,
			SummaryPath: "", OAuthTenantID: tenant, OAuthAllowedHosts: allowedHosts,
			OAuth: connector.OAuthClientCredentialsConfig{TokenURL: tokenURL, ClientID: clientID,
				ClientSecret: clientSecret, Scope: "read", Resource: "aero-vault"},
			Timeout: timeout, MaxAttempts: maxAttempts, RetryBackoff: 25 * time.Millisecond,
			TLSCAFile:          os.Getenv("AERO_ID_VAULT_CONTRACT_TLS_CA_FILE"),
			TLSCertFile:        os.Getenv("AERO_ID_VAULT_CONTRACT_TLS_CERT_FILE"),
			TLSKeyFile:         os.Getenv("AERO_ID_VAULT_CONTRACT_TLS_KEY_FILE"),
			TLSServerName:      os.Getenv("AERO_ID_VAULT_CONTRACT_TLS_SERVER_NAME"),
			OAuthTLSCAFile:     os.Getenv("AERO_ID_VAULT_CONTRACT_OAUTH_TLS_CA_FILE"),
			OAuthTLSCertFile:   os.Getenv("AERO_ID_VAULT_CONTRACT_OAUTH_TLS_CERT_FILE"),
			OAuthTLSKeyFile:    os.Getenv("AERO_ID_VAULT_CONTRACT_OAUTH_TLS_KEY_FILE"),
			OAuthTLSServerName: os.Getenv("AERO_ID_VAULT_CONTRACT_OAUTH_TLS_SERVER_NAME"),
		},
		request: connector.SummaryRequest{AccountID: accountID, CanonicalUID: canonicalUID,
			ActorUID: "aero-id-contract-test", TenantID: tenant, RequestID: "aero-id-vault-contract",
			OperationID: "aero-id-vault-contract", Datasets: datasets, Region: region},
		region: region, tenant: tenant,
	}
}

func assertVaultReadOnly(t *testing.T, client connector.Connector) {
	t.Helper()
	if client.Source() != connector.SourceAeroVault {
		t.Fatalf("connector source = %q, want aero-vault", client.Source())
	}
	if _, ok := client.(connector.CommandConnector); ok {
		t.Fatal("deployed Vault connector exposes an undeployed erase command")
	}
	if _, ok := client.(connector.OperationStatusConnector); ok {
		t.Fatal("deployed Vault connector exposes an undeployed status command")
	}
}

func assertVaultSummary(t *testing.T, summary *connector.Summary, contract deployedVaultContract) {
	t.Helper()
	if summary == nil || summary.Source != connector.SourceAeroVault || summary.SourceRegion != contract.region ||
		!summary.Complete || summary.Version < 1 || len(summary.Snapshots) != len(contract.request.Datasets) {
		t.Fatalf("deployed Vault summary metadata = %#v", summary)
	}
	seen := make(map[domain.AccountDataset]bool, len(summary.Snapshots))
	for _, snapshot := range summary.Snapshots {
		if snapshot == nil || snapshot.Source != string(connector.SourceAeroVault) ||
			snapshot.SourceRegion != contract.region || seen[snapshot.Dataset] {
			t.Fatalf("deployed Vault snapshots = %#v", summary.Snapshots)
		}
		seen[snapshot.Dataset] = true
	}
	for _, dataset := range contract.request.Datasets {
		if !seen[dataset] {
			t.Fatalf("deployed Vault omitted requested dataset %q", dataset)
		}
	}
	if len(summary.Sources) != 1 || summary.Sources[0].ScopeID != contract.tenant ||
		summary.Sources[0].ScopeType != "tenant" || len(summary.Memberships) != 1 ||
		summary.Memberships[0].ScopeID != contract.tenant {
		t.Fatalf("deployed Vault tenant references = %#v/%#v", summary.Sources, summary.Memberships)
	}
}

func vaultContractDatasets(t *testing.T) []domain.AccountDataset {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(vaultContractDatasetsEnv))
	if raw == "" {
		return []domain.AccountDataset{"aero-vault.buckets", "aero-vault.tenants", "aero-vault.usage"}
	}
	seen := map[domain.AccountDataset]bool{}
	result := make([]domain.AccountDataset, 0, 3)
	for _, value := range strings.Split(raw, ",") {
		dataset := domain.AccountDataset(strings.TrimSpace(value))
		if dataset == "" || seen[dataset] ||
			(dataset != "aero-vault.buckets" && dataset != "aero-vault.tenants" && dataset != "aero-vault.usage") {
			t.Fatalf("%s contains an invalid or duplicate dataset", vaultContractDatasetsEnv)
		}
		seen[dataset] = true
		result = append(result, dataset)
	}
	return result
}

func requiredVaultContractEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Fatalf("%s is required when the real Vault contract suite is enabled", name)
	}
	return value
}

func csvEnv(t *testing.T, name string) []string {
	t.Helper()
	values := strings.Split(strings.TrimSpace(os.Getenv(name)), ",")
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			t.Fatalf("%s must contain at least one exact OAuth origin", name)
		}
		result = append(result, value)
	}
	return result
}

func optionalEnv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(t *testing.T, name string, fallback time.Duration) time.Duration {
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

func intEnv(t *testing.T, name string, fallback int) int {
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

// httpPanicRoundTripper is only reached if constructor validation fails open.
// Its name makes an accidental network call fail loudly without holding any
// endpoint or credential in the test output.
type httpPanicRoundTripper struct{}

func (httpPanicRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("Vault constructor performed network I/O")
}
