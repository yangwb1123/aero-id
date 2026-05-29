package sso

import (
	"testing"
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
	// insecure=false with no CA file must build creds from the system pool, no error.
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
	// A cert without a key (or vice versa) is a misconfiguration, not silent insecurity.
	if _, err := dialCredentials(TLSConfig{CertFile: "/some/cert.pem"}); err == nil {
		t.Fatal("expected error when only a client cert is configured")
	}
	if _, err := dialCredentials(TLSConfig{KeyFile: "/some/key.pem"}); err == nil {
		t.Fatal("expected error when only a client key is configured")
	}
}
