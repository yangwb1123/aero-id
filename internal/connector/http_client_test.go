package connector

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/pkg/metrics"
)

func TestSourceClientInstallsMTLSOnCustomTransport(t *testing.T) {
	certFile, keyFile, certPEM := writeSourceClientCertificate(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	baseTLS := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "preserved.example"}
	baseTransport := &http.Transport{TLSClientConfig: baseTLS}
	baseClient := &http.Client{Transport: baseTransport, Timeout: 17 * time.Second}

	connector, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{
		Endpoint: "https://source.example", TLSCertFile: certFile, TLSKeyFile: keyFile,
		Client: baseClient, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("construct mTLS connector: %v", err)
	}
	transport, ok := connector.client.Transport.(*http.Transport)
	if !ok || transport == baseTransport {
		t.Fatalf("custom transport was not safely cloned: %#v", connector.client.Transport)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("cloned transport has no TLS config")
	}
	if transport.TLSClientConfig.ServerName != "preserved.example" {
		t.Fatalf("server name = %q, want preserved.example", transport.TLSClientConfig.ServerName)
	}
	if transport.TLSClientConfig.RootCAs != roots {
		t.Fatal("custom CA pool was not preserved")
	}
	if len(transport.TLSClientConfig.Certificates) != 1 {
		t.Fatalf("client certificates = %d, want 1", len(transport.TLSClientConfig.Certificates))
	}
	if len(baseTLS.Certificates) != 0 {
		t.Fatal("injected client was mutated")
	}
	if connector.client.Timeout != baseClient.Timeout {
		t.Fatalf("client timeout = %s, want preserved custom timeout %s", connector.client.Timeout, baseClient.Timeout)
	}
}

func TestSourceClientRejectsArbitraryTransportForMTLS(t *testing.T) {
	certFile, keyFile, _ := writeSourceClientCertificate(t)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, nil
	})}
	_, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{
		Endpoint: "https://source.example", TLSCertFile: certFile, TLSKeyFile: keyFile, Client: client,
	})
	if err == nil || !strings.Contains(err.Error(), "standard HTTP transport") {
		t.Fatalf("error = %v, want arbitrary transport rejection", err)
	}
}

func TestSourceAuthCompatibilityMetricIsConstructionBound(t *testing.T) {
	registry := metrics.NewRegistry()
	for range 2 {
		if _, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{
			Endpoint: "http://127.0.0.1:1", AllowInsecureLoopback: true, Environment: "test",
			Client: http.DefaultClient, Metrics: registry,
		}); err != nil {
			t.Fatalf("loopback connector: %v", err)
		}
	}
	var output strings.Builder
	registry.WriteText(&output)
	line := `aeroid_source_auth_compatibility_total{source="aero-im",mode="loopback"} 2`
	if !strings.Contains(output.String(), line) {
		t.Fatalf("metrics output missing %q:\n%s", line, output.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func writeSourceClientCertificate(t *testing.T) (certFile, keyFile string, certPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "source-client"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal test key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certFile = t.TempDir() + "/client.crt"
	keyFile = t.TempDir() + "/client.key"
	if err := writeTestFile(certFile, certPEM); err != nil {
		t.Fatal(err)
	}
	if err := writeTestFile(keyFile, keyPEM); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, certPEM
}

func writeTestFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
