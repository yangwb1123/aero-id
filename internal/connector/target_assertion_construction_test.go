package connector

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const targetAssertionConstructionKeyEnv = "AERO_ID_TEST_TARGET_ASSERTION_CONSTRUCTION_KEY"

type targetAssertionCountingTransport struct {
	calls atomic.Int32
}

func (t *targetAssertionCountingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return nil, io.ErrUnexpectedEOF
}

func targetAssertionConstructionConfig(client *http.Client) HTTPConfig {
	return HTTPConfig{
		Endpoint: "https://im.example", SummaryPath: accountSummaryCanonicalPath, Environment: "test", Client: client,
		OAuth: OAuthClientCredentialsConfig{TokenURL: "https://token.example/token", ClientID: "aero-id-im-source",
			ClientSecret: "oauth-secret", Scope: accountSummaryAssertionScope, Resource: accountSummaryOAuthResource,
			AllowedHosts: []string{"token.example"}},
		TargetAssertionKeyEnv: targetAssertionConstructionKeyEnv, TargetAssertionKeyID: "target-key-2026-08",
		TargetAssertionIssuer: "https://aero-id.example/assertions", TargetAssertionAudience: accountSummaryTargetAudience,
		TargetAssertionSubject: accountSummaryTargetSubject, TargetAssertionClientID: "aero-id-im-source",
	}
}

func TestNewJSONSummaryConnectorRejectsMissingAssertionBeforeClientSetup(t *testing.T) {
	transport := &targetAssertionCountingTransport{}
	cfg := targetAssertionConstructionConfig(&http.Client{Transport: transport})
	cfg.TargetAssertionKeyEnv = ""
	cfg.TargetAssertionKeyID = ""
	cfg.TargetAssertionIssuer = ""
	cfg.TargetAssertionAudience = ""
	cfg.TargetAssertionSubject = ""
	cfg.TargetAssertionClientID = ""
	_, err := NewJSONSummaryConnector(SourceAeroIM, cfg)
	if err == nil || !strings.Contains(err.Error(), "target assertion is required") {
		t.Fatalf("constructor error = %v, want target assertion requirement", err)
	}
	if strings.Contains(err.Error(), "oauth-secret") || strings.Contains(err.Error(), cfg.Endpoint) {
		t.Fatalf("constructor error disclosed sensitive configuration: %v", err)
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("remote calls during rejected construction = %d, want 0", got)
	}
}

func TestNewJSONSummaryConnectorLoadsTargetAssertionWithoutHTTP(t *testing.T) {
	t.Setenv(targetAssertionConstructionKeyEnv, testTargetAssertionPrivateKeyPEM)
	client, err := NewJSONSummaryConnector(SourceAeroIM, targetAssertionConstructionConfig(nil))
	if err != nil {
		t.Fatalf("constructor error = %v", err)
	}
	if client.targetAssertionSigner == nil {
		t.Fatal("target assertion signer is nil after valid key construction")
	}
}

func TestNewJSONSummaryConnectorLoadsTargetAssertionFromRestrictedFileWithoutHTTP(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(path, []byte(testTargetAssertionPrivateKeyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := targetAssertionConstructionConfig(nil)
	cfg.TargetAssertionKeyEnv = ""
	cfg.TargetAssertionKeyFile = path
	client, err := NewJSONSummaryConnector(SourceAeroIM, cfg)
	if err != nil {
		t.Fatalf("constructor error = %v", err)
	}
	if client.targetAssertionSigner == nil {
		t.Fatal("target assertion signer is nil after valid file construction")
	}
}

func TestNewJSONSummaryConnectorRejectsTargetAssertionKeyFailuresWithoutHTTP(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*testing.T, *HTTPConfig)
		secret string
	}{
		{name: "missing environment value", setup: func(t *testing.T, cfg *HTTPConfig) {
			t.Setenv(targetAssertionConstructionKeyEnv, "")
			cfg.TargetAssertionKeyEnv = targetAssertionConstructionKeyEnv
		}},
		{name: "invalid environment value", secret: "not-a-private-key", setup: func(t *testing.T, cfg *HTTPConfig) {
			t.Setenv(targetAssertionConstructionKeyEnv, "not-a-private-key")
			cfg.TargetAssertionKeyEnv = targetAssertionConstructionKeyEnv
		}},
		{name: "missing file", setup: func(t *testing.T, cfg *HTTPConfig) {
			cfg.TargetAssertionKeyEnv = ""
			cfg.TargetAssertionKeyFile = filepath.Join(t.TempDir(), "missing.pem")
		}},
		{name: "unsafe file mode", setup: func(t *testing.T, cfg *HTTPConfig) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "key.pem")
			if err := os.WriteFile(path, []byte(testTargetAssertionPrivateKeyPEM), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			cfg.TargetAssertionKeyEnv = ""
			cfg.TargetAssertionKeyFile = path
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			transport := &targetAssertionCountingTransport{}
			cfg := targetAssertionConstructionConfig(&http.Client{Transport: transport})
			tc.setup(t, &cfg)
			_, err := NewJSONSummaryConnector(SourceAeroIM, cfg)
			if err == nil || !strings.Contains(err.Error(), "target assertion") {
				t.Fatalf("constructor error = %v, want target assertion failure", err)
			}
			if tc.secret != "" && strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("constructor error disclosed key material: %v", err)
			}
			if cfg.TargetAssertionKeyFile != "" && strings.Contains(err.Error(), cfg.TargetAssertionKeyFile) {
				t.Fatalf("constructor error disclosed key path: %v", err)
			}
			if got := transport.calls.Load(); got != 0 {
				t.Fatalf("remote calls during rejected construction = %d, want 0", got)
			}
		})
	}
}

func TestTargetAssertionWhitespaceOnlyMetadataIsUnconfigured(t *testing.T) {
	client, err := NewJSONSummaryConnector(SourceAeroIM, HTTPConfig{
		Endpoint: "http://127.0.0.1:8080", Environment: "test", AllowInsecureLoopback: true,
		TargetAssertionKeyID: " \t",
	})
	if err != nil {
		t.Fatalf("whitespace-only target assertion metadata rejected: %v", err)
	}
	if client.targetAssertionSigner != nil {
		t.Fatal("whitespace-only target assertion metadata unexpectedly constructed a signer")
	}
}
