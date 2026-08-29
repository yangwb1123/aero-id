package connector

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
)

func decodeTargetAssertion(t *testing.T, token string) (map[string]any, map[string]any) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("target assertion segments = %d, want 3", len(parts))
	}
	decode := func(value string) map[string]any {
		data, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			t.Fatalf("decode assertion: %v", err)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("unmarshal assertion: %v", err)
		}
		return result
	}
	return decode(parts[0]), decode(parts[1])
}

func trustedRequestForTest(t *testing.T, datasets []domain.AccountDataset) SummaryRequest {
	t.Helper()
	account := testAccount(1)
	request, err := NewTrustedSummaryRequest(t.Context(), testSummaryAccountLoader{account}, TrustedSummaryRequestInput{
		TenantID: account.TenantID, AccountID: account.ID, ActorUID: "actor-1",
		RequestID: "request-1", OperationID: "operation-1", Datasets: datasets,
	})
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestCanonicalAccountSummaryRequestVectorMatchesRust(t *testing.T) {
	bytes, err := canonicalAccountSummaryRequest(accountSummaryCanonicalMethod, accountSummaryCanonicalPath,
		"acct-opaque-01", "us-east-1", []domain.AccountDataset{"aero-im.workspaces", "aero-im.profile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(bytes); got != "000000176165726f2d6163636f756e742d73756d6d6172792d763100000003474554000000192f696e7465726e616c2f6163636f756e742d73756d6d6172790000000e616363742d6f70617175652d30310000000975732d656173742d310000000f6165726f2d696d2e70726f66696c65000000126165726f2d696d2e776f726b737061636573" {
		t.Fatalf("canonical bytes = %s", got)
	}
	hash, err := canonicalAccountSummaryRequestHash(accountSummaryCanonicalMethod, accountSummaryCanonicalPath,
		"acct-opaque-01", "us-east-1", []domain.AccountDataset{"aero-im.workspaces", "aero-im.profile"})
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.RawURLEncoding.EncodeToString(hash[:]); got != "6U4cCAVicfmUggJmtb9rNdAJsKhhkfymA3teXMoDq3g" {
		t.Fatalf("canonical hash = %s", got)
	}
}

func TestSummaryRequestCarriesTargetAssertion(t *testing.T) {
	client, err := NewJSONSummaryConnector(SourceAeroIM, addTestTargetAssertion(HTTPConfig{
		Endpoint: "http://127.0.0.1", SummaryPath: "/internal/account-summary",
		AllowInsecureLoopback: true, Environment: "test", BearerToken: "broad-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := trustedRequestForTest(t, []domain.AccountDataset{"aero-im.workspaces", "aero-im.profile"})
	httpRequest, err := client.newSummaryRequest(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	header, claims := decodeTargetAssertion(t, httpRequest.Header.Get(accountSummaryTargetAssertionHeader))
	if !reflect.DeepEqual(header, map[string]any{"alg": "EdDSA", "kid": "test-key", "typ": accountSummaryTargetAssertionType}) {
		t.Fatalf("assertion header = %#v", header)
	}
	for key, want := range map[string]string{
		"iss": "https://aero-id.example.test/assertions", "aud": "aero-im-account-summary",
		"sub": "aero-id-sync", "client_id": "test-client", "account_id": request.AccountID.String(),
		"canonical_uid": request.CanonicalUID, "tenant_id": request.TenantID, "region": request.Region,
		"method": "GET", "path": "/internal/account-summary", "scope": accountSummaryAssertionScope,
	} {
		if got := claims[key]; got != want {
			t.Fatalf("claim %s = %#v, want %q", key, got, want)
		}
	}
	if _, ok := claims["actor_uid"]; ok {
		t.Fatal("actor must not be promoted into the target assertion")
	}
	if _, ok := claims["typ"]; ok {
		t.Fatal("target type belongs in the protected header")
	}
	if got := httpRequest.URL.Query()["dataset"]; !reflect.DeepEqual(got, []string{"aero-im.profile", "aero-im.workspaces"}) {
		t.Fatalf("wire dataset order = %v, want canonical order", got)
	}
}

func TestSummaryRequestUsesCanonicalRequestHash(t *testing.T) {
	client, err := NewJSONSummaryConnector(SourceAeroIM, addTestTargetAssertion(HTTPConfig{
		Endpoint: "http://127.0.0.1", SummaryPath: "/internal/account-summary",
		AllowInsecureLoopback: true, Environment: "test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := trustedRequestForTest(t, []domain.AccountDataset{"aero-im.workspaces", "aero-im.profile"})
	httpRequest, err := client.newSummaryRequest(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	_, claims := decodeTargetAssertion(t, httpRequest.Header.Get(accountSummaryTargetAssertionHeader))
	hash, err := canonicalAccountSummaryRequestHash(accountSummaryCanonicalMethod, accountSummaryCanonicalPath,
		request.AccountID.String(), request.Region, request.Datasets)
	if err != nil {
		t.Fatal(err)
	}
	if claims["request_hash"] != base64.RawURLEncoding.EncodeToString(hash[:]) {
		t.Fatalf("request_hash = %v, want canonical hash", claims["request_hash"])
	}
}

func TestSummaryRequestDoesNotSignCallerSuppliedMismatchedAccount(t *testing.T) {
	client, err := NewJSONSummaryConnector(SourceAeroIM, addTestTargetAssertion(HTTPConfig{
		Endpoint: "http://127.0.0.1", SummaryPath: "/internal/account-summary",
		AllowInsecureLoopback: true, Environment: "test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := trustedRequestForTest(t, []domain.AccountDataset{"aero-im.profile"})
	request.AccountID = domain.ID{2}
	if _, err := client.newSummaryRequest(t.Context(), request); err == nil {
		t.Fatal("mismatched account was signed")
	}
	request = trustedRequestForTest(t, []domain.AccountDataset{"aero-im.profile"})
	request.CanonicalUID = "different-user"
	if _, err := client.newSummaryRequest(t.Context(), request); err == nil {
		t.Fatal("mismatched canonical UID was signed")
	}
	request = trustedRequestForTest(t, []domain.AccountDataset{"aero-im.profile"})
	request.TenantID = "different-tenant"
	if _, err := client.newSummaryRequest(t.Context(), request); err == nil {
		t.Fatal("mismatched tenant was signed")
	}
}

func TestTargetAssertionJTIIsDeterministicOnlyThroughTheTestSeam(t *testing.T) {
	signer := testTargetAssertionSigner()
	signer.now = func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }
	ids := []uuid.UUID{
		uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		uuid.MustParse("00000000-0000-4000-8000-000000000002"),
	}
	index := 0
	signer.newJTI = func() (uuid.UUID, error) {
		value := ids[index]
		index++
		return value, nil
	}
	request := trustedRequestForTest(t, []domain.AccountDataset{"aero-im.profile"})
	first, err := signer.sign(request.trustedTarget)
	if err != nil {
		t.Fatal(err)
	}
	second, err := signer.sign(request.trustedTarget)
	if err != nil {
		t.Fatal(err)
	}
	_, firstClaims := decodeTargetAssertion(t, first)
	_, secondClaims := decodeTargetAssertion(t, second)
	if firstClaims["jti"] != ids[0].String() || secondClaims["jti"] != ids[1].String() {
		t.Fatalf("deterministic JTIs = %v/%v", firstClaims["jti"], secondClaims["jti"])
	}
}

func TestSummaryRetryMintsFreshAssertionJTI(t *testing.T) {
	var mu sync.Mutex
	var assertions []string
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		assertions = append(assertions, r.Header.Get(accountSummaryTargetAssertionHeader))
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"source_region":"local","version":1,"generated_at":"2026-08-20T00:00:00Z","complete":true,"datasets":{"aero-im.profile":{}}}`)
	}))
	defer server.Close()
	client := newTestAeroIMConnector(t, server.URL, HTTPConfig{MaxAttempts: 3, RetryBackoff: time.Millisecond})
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{"aero-im.profile"}}); err != nil {
		t.Fatal(err)
	}
	if len(assertions) != 3 || assertions[0] == assertions[1] || assertions[1] == assertions[2] {
		t.Fatalf("assertion count/JTIs = %d/%v, want three distinct assertions", len(assertions), assertions)
	}
	for _, token := range assertions {
		_, claims := decodeTargetAssertion(t, token)
		if claims["jti"] == "" {
			t.Fatal("retry assertion has no jti")
		}
	}
}

func TestSummaryUnauthorizedRefreshMintsFreshAssertionJTI(t *testing.T) {
	var mu sync.Mutex
	var assertions []string
	tokenCalls := 0
	sourceCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/token":
			tokenCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"token-`+string(rune('0'+tokenCalls))+`","token_type":"Bearer","expires_in":300}`)
		case accountSummaryCanonicalPath:
			sourceCalls++
			assertions = append(assertions, r.Header.Get(accountSummaryTargetAssertionHeader))
			if sourceCalls == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"source_region":"local","version":1,"generated_at":"2026-08-20T00:00:00Z","complete":true,"datasets":{"aero-im.profile":{}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroIM, addTestTargetAssertionForClient(HTTPConfig{
		Endpoint: server.URL, AllowInsecureLoopback: true, Environment: "test",
		Client: server.Client(), MaxAttempts: 1, OAuth: OAuthClientCredentialsConfig{
			TokenURL: server.URL + "/token", ClientID: "aero-id", ClientSecret: "secret",
			Scope: "aero.account.summary.read", Resource: "aero-im",
		},
	}, "aero-id"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{"aero-im.profile"}}); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 2 || sourceCalls != 2 || len(assertions) != 2 || assertions[0] == assertions[1] {
		t.Fatalf("token/source/assertions = %d/%d/%v, want 2/2/distinct", tokenCalls, sourceCalls, assertions)
	}
}

func TestAeroIMSummaryRemainsFailClosedWithoutAssertionConfiguration(t *testing.T) {
	var sourceCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sourceCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: server.URL,
		SummaryPath: accountSummaryCanonicalPath, AllowInsecureLoopback: true, Environment: "test", Client: server.Client(),
		BearerToken: "broad-token"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{"aero-im.profile"}})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Code != "source.target_assertion_not_configured" {
		t.Fatalf("error = %v, want target assertion configuration failure", err)
	}
	if sourceCalls != 0 {
		t.Fatalf("source calls = %d, want 0", sourceCalls)
	}
}

func TestAeroIMTargetAssertionRejectsNonCanonicalPath(t *testing.T) {
	var sourceCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sourceCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := addTestTargetAssertion(HTTPConfig{Endpoint: server.URL, AllowInsecureLoopback: true,
		Environment: "test", Client: server.Client(), MaxAttempts: 1})
	cfg.SummaryPath = "/summary"
	client, err := NewJSONSummaryConnector(SourceAeroIM, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1},
		Datasets: []domain.AccountDataset{"aero-im.profile"}})
	var remote *RemoteError
	if !errors.As(err, &remote) || remote.Kind != ErrorInvalid ||
		remote.Code != "source.target_assertion_path" {
		t.Fatalf("error = %v, want invalid target assertion path", err)
	}
	if sourceCalls != 0 {
		t.Fatalf("source calls = %d, want 0 for a non-canonical signed path", sourceCalls)
	}
}

func TestTargetAssertionPrivateKeyLoadingUsesSafeReferences(t *testing.T) {
	block, _ := pem.Decode([]byte(testTargetAssertionPrivateKeyPEM))
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/assertion-key.pem"
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadEd25519PrivateKey(path, "")
	if err != nil || !reflect.DeepEqual(loaded, key) {
		t.Fatalf("file key load = %v/%v", loaded, err)
	}
	envName := "AERO_TEST_ACCOUNT_ASSERTION_KEY"
	t.Setenv(envName, testTargetAssertionPrivateKeyPEM)
	loaded, err = loadEd25519PrivateKey("", envName)
	if err != nil || !reflect.DeepEqual(loaded, key) {
		t.Fatalf("environment key load = %v/%v", loaded, err)
	}
	if _, err := loadEd25519PrivateKey(path, envName); err == nil {
		t.Fatal("multiple private-key sources were accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEd25519PrivateKey(path, ""); err == nil {
		t.Fatal("group/world-readable private-key file was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := t.TempDir() + "/assertion-key-link.pem"
	if err := os.Symlink(path, link); err == nil {
		if _, err := loadEd25519PrivateKey(link, ""); err == nil {
			t.Fatal("symlinked private-key file was accepted")
		}
	}
}
