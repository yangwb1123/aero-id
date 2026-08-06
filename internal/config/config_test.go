package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const sample = `
app: {name: aero-id, env: test, version: 0.0.1}
server:
  http: {host: 0.0.0.0, port: 8080, read_timeout: 15s, write_timeout: 15s, shutdown_timeout: 10s}
  grpc: {host: 0.0.0.0, port: 9090}
database:
  host: 127.0.0.1
  port: 5432
  user: aero
  password: aero
  dbname: aero_id
  sslmode: disable
  max_open_conns: 10
  max_idle_conns: 2
  conn_max_lifetime: 30m
  log_level: warn
sso: {endpoint: "sso:9000", timeout: 5s, insecure: true, token_cache_ttl: 60s, user_cache_ttl: 300s}
storage:
  type: local
  local: {root: /tmp/files, public_base_url: "http://x"}
audit_ledger: {signing_key: change-me-please-very-long-audit-key}
signing_key: testkey
log: {level: info, encoding: json}
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_fromFile(t *testing.T) {
	cfg, err := Load(writeTemp(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.DBName != "aero_id" {
		t.Errorf("DBName=%q", cfg.Database.DBName)
	}
	if cfg.SSO.TokenCacheTTL.String() != "1m0s" {
		t.Errorf("TokenCacheTTL=%v", cfg.SSO.TokenCacheTTL)
	}
}

func TestLoad_envOverride(t *testing.T) {
	t.Setenv("AERO_ID_DATABASE_PASSWORD", "envpass")
	t.Setenv("AERO_ID_SIGNING_KEY", "envkey")
	cfg, err := Load(writeTemp(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Password != "envpass" {
		t.Errorf("Password=%q", cfg.Database.Password)
	}
	if cfg.SigningKey != "envkey" {
		t.Errorf("SigningKey=%q", cfg.SigningKey)
	}
}

func TestLoad_missingSigningKey(t *testing.T) {
	bad := sample[:len(sample)-len("\nsigning_key: testkey\nlog: {level: info, encoding: json}\n")]
	bad += "\nlog: {level: info, encoding: json}\n"
	_, err := Load(writeTemp(t, bad))
	if err == nil {
		t.Fatal("expected validation error for missing signing_key")
	}
}

func TestDatabaseDSN(t *testing.T) {
	cfg, _ := Load(writeTemp(t, sample))
	dsn := cfg.Database.DSN()
	want := "host=127.0.0.1 port=5432 user=aero password=aero dbname=aero_id sslmode=disable TimeZone=UTC"
	if dsn != want {
		t.Errorf("\n got %s\nwant %s", dsn, want)
	}
}

func TestLoadAuditLedgerSigningKeyValidation(t *testing.T) {
	const validLine = "audit_ledger: {signing_key: change-me-please-very-long-audit-key}"
	if _, err := Load(writeTemp(t, sample)); err != nil {
		t.Fatalf("sample with audit_ledger signing key rejected: %v", err)
	}
	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{"empty key", "", "audit_ledger.signing_key is required"},
		{"whitespace key", "   \t  ", "audit_ledger.signing_key is required"},
		{"31 byte key", strings.Repeat("a", 31), "audit_ledger.signing_key must be at least 32 bytes"},
		{"padded short key", "  " + strings.Repeat("a", 31), "audit_ledger.signing_key must be at least 32 bytes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := strings.Replace(sample, validLine, "audit_ledger: {signing_key: "+strconv.Quote(tc.key)+"}", 1)
			_, err := Load(writeTemp(t, content))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateEventIngestRejectsNonCanonicalBindings(t *testing.T) {
	tests := map[string]EventIngestConfig{
		"client id": {
			Bindings: map[string]EventSourceBindingConfig{
				"client":  {Source: "snaplink", SourceRegion: "us-east-1"},
				" client": {Source: "snaplink", SourceRegion: "us-east-1"},
			},
		},
		"tenant id": {
			Bindings: map[string]EventSourceBindingConfig{
				"client": {TenantID: " tenant", Source: "snaplink", SourceRegion: "us-east-1"},
			},
		},
		"source region": {
			Bindings: map[string]EventSourceBindingConfig{
				"client": {Source: "snaplink", SourceRegion: "us-east-1 "},
			},
		},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateEventIngest(cfg); err == nil {
				t.Fatal("expected non-canonical binding to be rejected")
			}
		})
	}
}
