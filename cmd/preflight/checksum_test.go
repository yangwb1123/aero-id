package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestVerifyConfigChecksum(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	const content = "app:\n  name: aero-id\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(content))
	expected := hex.EncodeToString(hash[:])
	if err := verifyConfigChecksum(path, expected); err != nil {
		t.Fatalf("valid checksum: %v", err)
	}
	if err := verifyConfigChecksum(path, strings.Repeat("0", sha256.Size*2)); err == nil {
		t.Fatal("mismatched checksum was accepted")
	}
	if err := verifyConfigChecksum(path, "not-a-checksum"); err == nil {
		t.Fatal("malformed checksum was accepted")
	}
}

func TestVerifyConfigChecksumAllowsStandaloneUse(t *testing.T) {
	if err := verifyConfigChecksum(t.TempDir()+"/missing.yaml", ""); err != nil {
		t.Fatalf("empty expected checksum: %v", err)
	}
}
