package connector

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
)

type testSummaryAccountLoader struct {
	account *domain.Account
}

func (l testSummaryAccountLoader) GetByID(context.Context, string, domain.ID) (*domain.Account, error) {
	return l.account, nil
}

const testTargetAssertionPrivateKeyPEM = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEIGrD/e7uKYqSY4twDEsRfMMuLSrODf14dpTiTK6K1YI0
-----END PRIVATE KEY-----
`

func testTargetAssertionSigner() *accountSummaryTargetSigner {
	return testTargetAssertionSignerForClient("test-client")
}

func testTargetAssertionSignerForClient(clientID string) *accountSummaryTargetSigner {
	block, _ := pem.Decode([]byte(testTargetAssertionPrivateKeyPEM))
	key, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
	signer, err := newAccountSummaryTargetSigner(targetAssertionSignerConfig{
		PrivateKey: key.(ed25519.PrivateKey), KeyID: "test-key",
		Issuer: "https://aero-id.example.test/assertions", Audience: "aero-im-account-summary",
		Subject: "aero-id-sync", ClientID: clientID, Lifetime: time.Minute,
	})
	if err != nil {
		panic(err)
	}
	return signer
}

func trustedTestSummaryRequest(request SummaryRequest) SummaryRequest {
	if request.CanonicalUID == "" {
		request.CanonicalUID = "test-canonical-user"
	}
	if request.TenantID == "" {
		request.TenantID = "test-tenant"
	}
	if request.Region == "" {
		request.Region = "local"
	}
	sort.Slice(request.Datasets, func(left, right int) bool { return string(request.Datasets[left]) < string(request.Datasets[right]) })
	request.trustedTarget = &trustedSummaryTarget{
		accountID: request.AccountID.String(), canonicalUID: request.CanonicalUID,
		tenantID: request.TenantID, region: request.Region,
		datasets: append([]domain.AccountDataset(nil), request.Datasets...),
	}
	return request
}

func addTestTargetAssertion(cfg HTTPConfig) HTTPConfig {
	return addTestTargetAssertionForClient(cfg, "test-client")
}

func addTestTargetAssertionForClient(cfg HTTPConfig, clientID string) HTTPConfig {
	cfg.SummaryPath = accountSummaryCanonicalPath
	cfg.targetAssertionSigner = testTargetAssertionSignerForClient(clientID)
	cfg.testTargetRequest = trustedTestSummaryRequest
	return cfg
}

func newTestAeroIMConnector(t *testing.T, endpoint string, cfg HTTPConfig) *JSONSummaryConnector {
	t.Helper()
	cfg = addTestTargetAssertion(cfg)
	cfg.Endpoint = endpoint
	cfg.SummaryPath = accountSummaryCanonicalPath
	cfg.AllowInsecureLoopback = true
	cfg.Environment = "test"
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.MaxBodyBytes == 0 {
		cfg.MaxBodyBytes = 1 << 20
	}
	client, err := NewJSONSummaryConnector(SourceAeroIM, cfg)
	if err != nil {
		t.Fatalf("new Aero IM connector: %v", err)
	}
	return client
}

func testAccount(id byte) *domain.Account {
	return &domain.Account{ID: domain.ID{id}, TenantID: "test-tenant", CanonicalUID: "test-canonical-user", HomeRegion: "local", Status: domain.AccountActive}
}
