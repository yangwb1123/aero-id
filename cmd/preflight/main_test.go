package main

import (
	"strings"
	"testing"
)

func TestPreflightCommandRejectsUIDOnlyConfigAndAcceptsClaimsConfig(t *testing.T) {
	// The real loader and shipped config are used, but no database/source call
	// is made. Environment values are deterministic and do not contain secrets.
	for key, value := range map[string]string{
		"AERO_ID_CONFIG_SHA256": "", "AERO_ID_APP_ENV": "test",
		"AERO_ID_SIGNING_KEY":              "test-preflight-only-not-a-production-secret",
		"AERO_ID_AUDIT_LEDGER_SIGNING_KEY": "test-preflight-ledger-not-a-production-secret",
		"AERO_ID_SSO_ENDPOINT":             "127.0.0.1:1", "AERO_ID_SSO_INSECURE": "true",
		"AERO_ID_SSO_AUTHZ_REQUIRED": "false", "AERO_ID_SSO_AUTHZ_LEGACY_FALLBACK": "true",
		"AERO_ID_SSO_JWKS_URL": "", "AERO_ID_SSO_ISSUER": "", "AERO_ID_SSO_AUDIENCE": "",
		"AERO_ID_SOURCES_SNAPLINK_ENDPOINT": "", "AERO_ID_SOURCES_AERO_IM_ENDPOINT": "",
		"AERO_ID_SOURCES_AERO_VAULT_ENDPOINT": "",
	} {
		t.Setenv(key, value)
	}
	const path = "../../configs/config.yaml"
	if err := run(path); err == nil || !strings.Contains(err.Error(), "claims-capable authentication") {
		t.Fatalf("UID-only configuration passed preflight: %v", err)
	}
	t.Setenv("AERO_ID_SSO_JWKS_URL", "https://snaplink.example/jwks")
	t.Setenv("AERO_ID_SSO_ISSUER", "snaplink")
	t.Setenv("AERO_ID_SSO_AUDIENCE", "aero-id")
	if err := run(path); err != nil {
		t.Fatalf("claims-config preflight: %v", err)
	}
}
