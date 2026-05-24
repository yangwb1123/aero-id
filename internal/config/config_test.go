package config

import (
	"os"
	"path/filepath"
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
