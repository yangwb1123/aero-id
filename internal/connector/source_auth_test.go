package connector

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

type sourceAuthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f sourceAuthRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestAeroIMAuthenticationValidationHappensBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: sourceAuthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, io.ErrUnexpectedEOF
	})}
	_, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: "http://127.0.0.1", Client: client})
	if err == nil || !strings.Contains(err.Error(), "sources.aero-im service authentication is required") {
		t.Fatalf("constructor error = %v, want fail-closed auth error", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d, want 0 during validation", calls.Load())
	}
}

func TestSnaplinkAuthenticationValidationHappensBeforeHTTP(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: sourceAuthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, io.ErrUnexpectedEOF
	})}
	_, err := NewJSONSummaryConnector(SourceSnaplink, HTTPConfig{Endpoint: "https://snaplink.example", Client: client})
	if err == nil || !strings.Contains(err.Error(), "snaplink service authentication is required") {
		t.Fatalf("constructor error = %v, want fail-closed Snaplink auth error", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d, want 0 during validation", calls.Load())
	}
}

func TestSnaplinkStaticBearerAuthenticatesEverySurface(t *testing.T) {
	assertStaticBearerSurfaces(t, SourceSnaplink)
}

func assertStaticBearerSurfaces(t *testing.T, source Source) {
	t.Helper()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer static-source-token" {
			t.Fatalf("%s Authorization = %q", r.URL.Path, got)
		}
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/summary" {
			_, _ = w.Write([]byte(`{"source_region":"west","version":1,"generated_at":"2026-08-20T00:00:00Z","datasets":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"completed","source_operation_id":"source-op"}`))
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(source, HTTPConfig{Endpoint: server.URL, SummaryPath: "/summary",
		BearerToken: "  static-source-token  ", ErasePath: "/erase", OperationStatusPath: "/status", MaxAttempts: 1})
	if err != nil {
		t.Fatalf("static bearer constructor: %v", err)
	}
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}}); err != nil {
		t.Fatalf("summary: %v", err)
	}
	if _, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{1}, IdempotencyKey: "erase-1",
		Datasets: []domain.AccountDataset{"snaplink.identity"}}); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if _, err := client.OperationStatus(t.Context(), OperationStatusRequest{AccountID: domain.ID{1}, SourceOperationID: "source-op"}); err != nil {
		t.Fatalf("operation status: %v", err)
	}
	if len(paths) != 3 || paths[0] != "/summary" || paths[1] != "/erase" || paths[2] != "/status" {
		t.Fatalf("authenticated paths = %#v, want summary/erase/status", paths)
	}
}

func TestAeroIMStaticBearerAuthenticatesEverySurface(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer static-source-token" {
			t.Fatalf("%s Authorization = %q", r.URL.Path, got)
		}
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case accountSummaryCanonicalPath:
			_, _ = w.Write([]byte(`{"source_region":"west","version":1,"generated_at":"2026-08-20T00:00:00Z","datasets":{}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"completed","source_operation_id":"source-op"}`))
		}
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroIM, addTestTargetAssertion(HTTPConfig{Endpoint: server.URL,
		BearerToken: "  static-source-token  ", ErasePath: "/erase", OperationStatusPath: "/status", MaxAttempts: 1}))
	if err != nil {
		t.Fatalf("static bearer constructor: %v", err)
	}
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}}); err != nil {
		t.Fatalf("summary: %v", err)
	}
	if _, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}, IdempotencyKey: "erase-1"}); err != nil {
		t.Fatalf("erase: %v", err)
	}
	if _, err := client.OperationStatus(t.Context(), OperationStatusRequest{AccountID: domain.ID{1}, SourceOperationID: "source-op"}); err != nil {
		t.Fatalf("operation status: %v", err)
	}
	if len(paths) != 3 || paths[0] != accountSummaryCanonicalPath || paths[1] != "/erase" || paths[2] != "/status" {
		t.Fatalf("authenticated paths = %#v, want canonical summary/erase/status", paths)
	}
}

func TestAeroIMOAuthValidationIsLazy(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: sourceAuthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, io.ErrUnexpectedEOF
	})}
	_, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{
		Endpoint: "https://im.example", Client: client,
		OAuth: OAuthClientCredentialsConfig{TokenURL: "https://token.example/token", ClientID: "client",
			ClientSecret: "secret", Scope: "summary:read", Resource: "aero-im"},
	})
	if err != nil {
		t.Fatalf("valid OAuth configuration rejected: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("HTTP calls = %d, want lazy token acquisition", calls.Load())
	}
}

func TestAeroIMOAuthFailureDoesNotCallSource(t *testing.T) {
	var sourceCalls atomic.Int32
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer tokenServer.Close()
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sourceCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sourceServer.Close()
	client, err := NewJSONSummaryConnector(SourceAeroIM, addTestTargetAssertion(HTTPConfig{Endpoint: sourceServer.URL,
		OAuth: OAuthClientCredentialsConfig{TokenURL: tokenServer.URL, ClientID: "client",
			ClientSecret: "secret", Scope: "summary:read", Resource: "aero-im"}, MaxAttempts: 1}))
	if err != nil {
		t.Fatalf("OAuth constructor: %v", err)
	}
	_, err = client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}})
	var remote *RemoteError
	if !errorsAsRemote(err, &remote) || remote.Code != "source.oauth_token_unavailable" || !remote.Temporary {
		t.Fatalf("OAuth failure = %+v, want temporary source.oauth_token_unavailable", err)
	}
	if sourceCalls.Load() != 0 {
		t.Fatalf("source calls = %d, want 0 when token acquisition fails", sourceCalls.Load())
	}
}

func TestAeroIMLoopbackCompatibilityMatrixAndMetric(t *testing.T) {
	registry := metrics.NewRegistry()
	for _, endpoint := range []string{"http://localhost", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		_, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: endpoint,
			AllowInsecureLoopback: true, Environment: "test", Client: http.DefaultClient, Metrics: registry})
		if err != nil {
			t.Fatalf("loopback endpoint %q rejected: %v", endpoint, err)
		}
	}
	for _, tc := range []struct {
		name, endpoint, environment string
		allow                       bool
	}{
		{name: "flag missing", endpoint: "http://127.0.0.1"},
		{name: "non-loopback", endpoint: "http://192.0.2.1", environment: "test", allow: true},
		{name: "production", endpoint: "http://localhost", environment: "production", allow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: tc.endpoint,
				AllowInsecureLoopback: tc.allow, Environment: tc.environment, Client: http.DefaultClient}); err == nil {
				t.Fatal("invalid loopback compatibility configuration was accepted")
			}
		})
	}
	var exposition strings.Builder
	registry.WriteText(&exposition)
	text := exposition.String()
	if !strings.Contains(text, "aeroid_source_auth_compatibility_total{source=\"aero-im\",mode=\"loopback\"} 3") {
		t.Fatalf("compatibility metric missing or unbounded: %s", text)
	}
	if strings.Contains(text, "127.0.0.1") || strings.Contains(text, "localhost") {
		t.Fatalf("endpoint leaked into compatibility metric: %s", text)
	}
}

func TestEraseBlankKeyIsSharedAcrossSourcesAndSkipsTokenIO(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: sourceAuthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, io.ErrUnexpectedEOF
	})}
	for _, source := range []Source{SourceSnaplink, SourceAeroIM, SourceAeroVault} {
		cfg := HTTPConfig{Endpoint: "http://127.0.0.1", ErasePath: "/erase", Client: client}
		if source == SourceSnaplink || source == SourceAeroIM {
			cfg.AllowInsecureLoopback = true
			cfg.Environment = "test"
		}
		if source == SourceAeroIM {
			cfg.OAuth = OAuthClientCredentialsConfig{TokenURL: "https://token.example/token", ClientID: "client",
				ClientSecret: "secret", Scope: "summary:read", Resource: "aero-im"}
		}
		item, err := NewJSONSummaryConnector(source, cfg)
		if err != nil {
			t.Fatalf("%s constructor: %v", source, err)
		}
		eraseRequest := EraseRequest{AccountID: domain.ID{1}, IdempotencyKey: " \t"}
		switch source {
		case SourceAeroIM:
			eraseRequest.Datasets = []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}
		case SourceSnaplink:
			eraseRequest.Datasets = []domain.AccountDataset{domain.AccountDataset("snaplink.identity")}
		case SourceAeroVault:
			eraseRequest.Datasets = []domain.AccountDataset{domain.AccountDataset("aero-vault.tenants")}
		}
		_, err = item.Erase(t.Context(), eraseRequest)
		var remote *RemoteError
		if !errorsAsRemote(err, &remote) || remote.Source != source || remote.Kind != ErrorInvalid ||
			remote.Code != "source.idempotency_key_missing" || remote.Temporary {
			t.Fatalf("%s blank erase error = %+v", source, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("source/token calls = %d, want 0", calls.Load())
	}
}

func TestAeroIMMTLSWithInjectedClientPresentsClientCertificate(t *testing.T) {
	material := newMTLSMaterial(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "aero-id-client" {
			t.Fatalf("client certificate was not presented: %#v", r.TLS)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("mTLS-only request carried bearer authorization %q", got)
		}
		_, _ = w.Write([]byte(`{"source_region":"west","version":1,"generated_at":"2026-08-20T00:00:00Z","datasets":{}}`))
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{material.server}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: material.pool}
	server.StartTLS()
	defer server.Close()

	client, err := NewJSONSummaryConnector(SourceAeroIM, addTestTargetAssertion(HTTPConfig{Endpoint: server.URL, SummaryPath: accountSummaryCanonicalPath,
		TLSCAFile: material.caFile, TLSCertFile: material.clientCertFile, TLSKeyFile: material.clientKeyFile,
		Client: server.Client(), MaxAttempts: 1}))
	if err != nil {
		t.Fatalf("mTLS constructor: %v", err)
	}
	if _, err := client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{1}, Datasets: []domain.AccountDataset{domain.AccountDataset("aero-im.profile")}}); err != nil {
		t.Fatalf("mTLS summary: %v", err)
	}
}

func TestAeroIMMTLSMissingFilesFailsBeforeConstruction(t *testing.T) {
	_, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: "https://im.example",
		TLSCertFile: "/no/such/client.crt", TLSKeyFile: "/no/such/client.key"})
	if err == nil || strings.Contains(err.Error(), "client.crt") || strings.Contains(err.Error(), "client.key") {
		t.Fatalf("error = %v, want a redacted local certificate error", err)
	}
}

func TestAeroIMMTLSRejectsArbitraryInjectedTransport(t *testing.T) {
	_, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{Endpoint: "https://im.example",
		TLSCertFile: "/tmp/client.crt", TLSKeyFile: "/tmp/client.key",
		Client: &http.Client{Transport: sourceAuthRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })}})
	if err == nil || !strings.Contains(err.Error(), "standard HTTP transport") {
		t.Fatalf("error = %v, want arbitrary transport rejection", err)
	}
}

type mtlsMaterial struct {
	server, client         tls.Certificate
	pool                   *x509.CertPool
	caFile, clientCertFile string
	clientKeyFile          string
}

func newMTLSMaterial(t *testing.T) mtlsMaterial {
	t.Helper()
	caKey, caCert, caDER := newCertificate(t, "aero-id-test-ca", true, nil, nil)
	serverKey, _, serverDER := newCertificate(t, "localhost", false, caKey, caCert)
	clientKey, _, clientDER := newCertificate(t, "aero-id-client", false, caKey, caCert)
	serverTLS := tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	caFile := writePEM(t, "ca.pem", "CERTIFICATE", caDER)
	clientCertFile := writePEM(t, "client.pem", "CERTIFICATE", clientDER)
	clientKeyFile := writePEM(t, "client.key", "EC PRIVATE KEY", marshalECKey(t, clientKey))
	return mtlsMaterial{server: serverTLS, client: tls.Certificate{Certificate: [][]byte{clientDER}, PrivateKey: clientKey},
		pool: pool, caFile: caFile, clientCertFile: clientCertFile, clientKeyFile: clientKeyFile}
}

func newCertificate(t *testing.T, commonName string, ca bool, parentKey *ecdsa.PrivateKey, parent *x509.Certificate) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true}
	if ca {
		template.IsCA = true
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		parentKey, parent = key, template
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
		template.KeyUsage = x509.KeyUsageDigitalSignature
		template.DNSNames = []string{commonName}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert, der
}

func marshalECKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func writePEM(t *testing.T, name, kind string, data []byte) string {
	t.Helper()
	path := t.TempDir() + "/" + name
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: data}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func errorsAsRemote(err error, target **RemoteError) bool {
	return errors.As(err, target)
}
