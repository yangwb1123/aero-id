package sso

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"testing"
	"time"
)

func TestDialCredentials_Insecure(t *testing.T) {
	creds, err := dialCredentials(TLSConfig{Insecure: true})
	if err != nil {
		t.Fatalf("insecure creds err = %v", err)
	}
	if creds == nil {
		t.Fatal("insecure creds = nil")
	}
}

func TestDialCredentials_SystemPool(t *testing.T) {
	creds, err := dialCredentials(TLSConfig{Insecure: false, ServerName: "sso.example"})
	if err != nil {
		t.Fatalf("system-pool creds err = %v", err)
	}
	if creds == nil {
		t.Fatal("system-pool creds = nil")
	}
}

func TestDialCredentials_BogusCAFileErrors(t *testing.T) {
	if _, err := dialCredentials(TLSConfig{CAFile: "/no/such/ca.pem"}); err == nil {
		t.Fatal("expected error for a missing CA file")
	}
}

func TestDialCredentials_PartialMTLSErrors(t *testing.T) {
	if _, err := dialCredentials(TLSConfig{CertFile: "/some/cert.pem"}); err == nil {
		t.Fatal("expected error when only a client cert is configured")
	}
	if _, err := dialCredentials(TLSConfig{KeyFile: "/some/key.pem"}); err == nil {
		t.Fatal("expected error when only a client key is configured")
	}
}

func TestDialCredentials_WithPins_BuildsCredentials(t *testing.T) {
	// Providing CertPins should not prevent credentials from being built;
	// the verification callback is wired but only fires during an actual handshake.
	creds, err := dialCredentials(TLSConfig{
		CertPins: []string{"deadbeef"},
	})
	if err != nil {
		t.Fatalf("cert pins build err = %v", err)
	}
	if creds == nil {
		t.Fatal("cert pins creds = nil")
	}
}

// selfSignedDER generates a minimal self-signed cert and returns the DER bytes
// alongside the hex-encoded SHA-256 SPKI fingerprint of its public key.
func selfSignedDER(t *testing.T) (derBytes []byte, spkiPin string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return der, hex.EncodeToString(sum[:])
}

func TestVerifySPKIPin_Match(t *testing.T) {
	der, pin := selfSignedDER(t)
	if err := verifySPKIPin([][]byte{der}, []string{pin}); err != nil {
		t.Fatalf("expected pin match, got: %v", err)
	}
}

func TestVerifySPKIPin_Mismatch(t *testing.T) {
	der, _ := selfSignedDER(t)
	err := verifySPKIPin([][]byte{der}, []string{"0000000000000000000000000000000000000000000000000000000000000000"})
	if err == nil {
		t.Fatal("expected pin mismatch error")
	}
}

func TestVerifySPKIPin_MultipleAllowed_OneMatches(t *testing.T) {
	der, pin := selfSignedDER(t)
	_, otherPin := selfSignedDER(t)
	// pin is correct; wrong pin is also in the list — any match is sufficient.
	if err := verifySPKIPin([][]byte{der}, []string{otherPin, pin}); err != nil {
		t.Fatalf("expected match with multiple pins, got: %v", err)
	}
}

func TestVerifySPKIPin_NoCerts(t *testing.T) {
	if err := verifySPKIPin(nil, []string{"apin"}); err == nil {
		t.Fatal("expected error with empty rawCerts")
	}
}

func TestVerifySPKIPin_InvalidDER(t *testing.T) {
	if err := verifySPKIPin([][]byte{{0x00, 0x01}}, []string{"apin"}); err == nil {
		t.Fatal("expected error parsing garbage DER")
	}
}
