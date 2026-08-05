package service

import (
	"bytes"
	"testing"
)

func TestExportArtifactRoundTrip(t *testing.T) {
	key := deriveExportKey("test-secret")
	plain := []byte(`{"account_id":"a","datasets":[]}`)
	ciphertext, err := encryptExport(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ciphertext, plain) {
		t.Fatal("export was not encrypted")
	}
	decoded, err := decryptExport(ciphertext, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, plain) {
		t.Fatalf("round trip = %q, want %q", decoded, plain)
	}
	if _, err := decryptExport(ciphertext, deriveExportKey("wrong-secret")); err == nil {
		t.Fatal("artifact decrypted with the wrong key")
	}
}

func TestExportArtifactRejectsInvalidPrefix(t *testing.T) {
	if _, err := decryptExport([]byte("plain"), deriveExportKey("test-secret")); err == nil {
		t.Fatal("invalid artifact was accepted")
	}
}
