package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// auditGovernanceBadYAML is a fully valid sample plus an enabled audit
// governance block whose token_url the connector would reject (non-loopback
// http, loopback flag off). Billing stays disabled (YAML default) so its
// credential error cannot mask the URL error; the binding carries valid
// tenant/scope/resources so the combined tenant/scope/resource error cannot
// fire first. Secret env may be unset: the URL check precedes credentials.
const auditGovernanceBadYAML = sample + `
audit_governance:
  enabled: true
  endpoint: "https://audit.example"
  source_prefix: aero-id
  actor_id: aero-id
  schema_id: aero.id.audit-fact
  schema_version: 1
  data_classification: personal
  retention_class: account_7y
  timeout: 5s
  refresh_skew: 30s
  allow_insecure_loopback: false
  bindings:
    tenant-one:
      token_url: "http://sso.example/token"
      client_id: audit-client
      client_secret_env: TEST_AUDIT_SECRET
      scope: "audit:event:write"
      resources: ["audit-governance"]
`

// TestLoadAuditGovernanceTokenURLGate (T-10.1.4): the gate fails at Load(),
// before boot-wiring; env overrides merge into the same validated struct and
// can neither bypass the gate (env-enable) nor change the failing set (env
// loopback flag enables exactly what the connector enables). The documented
// kill switch also holds via env.
func TestLoadAuditGovernanceTokenURLGate(t *testing.T) {
	const wantErr = "audit governance binding contains an invalid token URL"
	if _, err := Load(writeTemp(t, auditGovernanceBadYAML)); err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("Load error = %v, want containing %q (fail at load, not boot-wiring)", err, wantErr)
	}

	// Env cannot bypass the gate: file-disabled bad config + env-enabled fails.
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "true")
	disabled := strings.Replace(auditGovernanceBadYAML, "enabled: true", "enabled: false", 1)
	if _, err := Load(writeTemp(t, disabled)); err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("env-enabled Load error = %v, want containing %q", err, wantErr)
	}

	// Env loopback flag lands in the same merged struct the gate reads: the
	// connector accepts http://localhost when the flag is set, so Load must too.
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ALLOW_INSECURE_LOOPBACK", "true")
	t.Setenv("TEST_AUDIT_SECRET", "audit-secret-value-000000000000000001")
	loopback := strings.Replace(auditGovernanceBadYAML, `token_url: "http://sso.example/token"`, `token_url: "http://localhost/token"`, 1)
	if _, err := Load(writeTemp(t, loopback)); err != nil {
		t.Fatalf("env loopback Load error = %v, want success", err)
	}

	// Kill switch via env: disabling the gate accepts the bad config.
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "false")
	if _, err := Load(writeTemp(t, auditGovernanceBadYAML)); err != nil {
		t.Fatalf("env-disabled Load error = %v, want success (kill switch)", err)
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

// TestDataRetentionPurgePausedValidation (T-5.1): the purge_paused flag is
// accepted only when the outbox cleaner is actually wired; negative outbox
// retentions are rejected (they would purge every eligible row on the next
// tick).
func TestDataRetentionPurgePausedValidation(t *testing.T) {
	base := `
storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}
audit_ledger: {signing_key: change-me-please-very-long-audit-key}
signing_key: testkey
database: {dbname: aero_id, host: localhost, port: 5432, user: aero, password: x, sslmode: disable}
sso: {endpoint: "localhost:9090"}
`
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"pause with cleaner disabled", "data_retention: {purge_paused: true}", "outbox_clean_interval"},
		{"pause without dead-letter retention", "data_retention: {purge_paused: true, outbox_clean_interval: 1h}", "outbox_dead_letter_retention"},
		{"negative dead-letter retention", "data_retention: {outbox_dead_letter_retention: -1h}", "must not be negative"},
		{"negative published retention", "data_retention: {outbox_published_retention: -1h}", "must not be negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, base+"\n"+tc.yaml+"\n"))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}

	// Pause with the cleaner fully enabled parses and validates.
	okYAML := base + "\ndata_retention: {purge_paused: true, outbox_clean_interval: 1h, outbox_dead_letter_retention: 168h}\n"
	cfg, err := Load(writeTemp(t, okYAML))
	if err != nil {
		t.Fatalf("valid paused config rejected: %v", err)
	}
	if !cfg.DataRetention.PurgePaused {
		t.Fatal("PurgePaused = false, want true")
	}

	// Defaults unchanged: flag absent parses to false with the cleaner off.
	cfg, err = Load(writeTemp(t, base))
	if err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if cfg.DataRetention.PurgePaused {
		t.Fatal("PurgePaused default = true, want false")
	}

	// Env override lands in the same merged struct (viper AutomaticEnv is
	// lazy: the env value wins when the key exists in the file) and cannot
	// bypass validation — env=true over a file-enabled cleaner validates,
	// env=true over a file-disabled cleaner still fails the gate.
	t.Setenv("AERO_ID_DATA_RETENTION_PURGE_PAUSED", "true")
	cfg, err = Load(writeTemp(t, base+"\ndata_retention: {purge_paused: false, outbox_clean_interval: 1h, outbox_dead_letter_retention: 168h}\n"))
	if err != nil {
		t.Fatalf("env-enabled paused config rejected: %v", err)
	}
	if !cfg.DataRetention.PurgePaused {
		t.Fatal("env PurgePaused = false, want true")
	}
	_, err = Load(writeTemp(t, base+"\ndata_retention: {purge_paused: false, outbox_clean_interval: 0}\n"))
	if err == nil || !strings.Contains(err.Error(), "outbox_clean_interval") {
		t.Fatalf("env cannot bypass validation: err = %v", err)
	}
}

// TestAuditGovernanceDeadLetterRetentionValidation (CFG-2): a negative
// dead_letter_retention is rejected at load; purge_pause and a positive
// retention parse.
func TestAuditGovernanceDeadLetterRetentionValidation(t *testing.T) {
	base := `
storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}
audit_ledger: {signing_key: change-me-please-very-long-audit-key}
signing_key: testkey
database: {dbname: aero_id, host: localhost, port: 5432, user: aero, password: x, sslmode: disable}
sso: {endpoint: "localhost:9090"}
`
	neg := base + "\naudit_governance: {dead_letter_retention: -1h}\n"
	if _, err := Load(writeTemp(t, neg)); err == nil || !strings.Contains(err.Error(), "dead_letter_retention must not be negative") {
		t.Fatalf("negative retention err = %v, want rejection", err)
	}
	ok := base + "\naudit_governance: {purge_pause: true, dead_letter_retention: 168h}\n"
	cfg, err := Load(writeTemp(t, ok))
	if err != nil {
		t.Fatalf("valid governance purge config rejected: %v", err)
	}
	if !cfg.AuditGovernance.PurgePause || cfg.AuditGovernance.DeadLetterRetention != 168*time.Hour {
		t.Fatalf("governance purge config = %v/%v", cfg.AuditGovernance.PurgePause, cfg.AuditGovernance.DeadLetterRetention)
	}
}

// TestLoadAuditGovernanceKnobEnvLegs (T-10.1.9): AERO_ID_AUDIT_GOVERNANCE_*
// env overrides reach the B2-5 knobs (replacer, config.go) and cannot bypass
// validation. viper AutomaticEnv is lazy (see the T-5.1 comment above: the
// env value wins when the key exists in the file), so the file MUST carry the
// default-valued keys (purge_pause: false, dead_letter_retention: 0 — never
// the env values themselves, which would be a wrong-reason pass: the
// assertion would be green without any env plumbing). The negative leg fails
// if the env never lands OR if validation is relaxed; a duration-parse
// failure would produce an unmarshal error instead of the pinned message, so
// the substring assertion prevents wrong-reason passes. Bindings stay
// non-env-addressable — no leg for them.
func TestLoadAuditGovernanceKnobEnvLegs(t *testing.T) {
	base := `
storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}
audit_ledger: {signing_key: change-me-please-very-long-audit-key}
signing_key: testkey
database: {dbname: aero_id, host: localhost, port: 5432, user: aero, password: x, sslmode: disable}
sso: {endpoint: "localhost:9090"}
audit_governance: {purge_pause: false, dead_letter_retention: 0}
`
	t.Run("env wins over file defaults", func(t *testing.T) {
		t.Setenv("AERO_ID_AUDIT_GOVERNANCE_PURGE_PAUSE", "true")
		t.Setenv("AERO_ID_AUDIT_GOVERNANCE_DEAD_LETTER_RETENTION", "1h")
		cfg, err := Load(writeTemp(t, base))
		if err != nil {
			t.Fatalf("Load error = %v", err)
		}
		if !cfg.AuditGovernance.PurgePause {
			t.Fatal("PurgePause = false, want env true (env-wins-over-file)")
		}
		if cfg.AuditGovernance.DeadLetterRetention != time.Hour {
			t.Fatalf("DeadLetterRetention = %v, want 1h", cfg.AuditGovernance.DeadLetterRetention)
		}
	})
	t.Run("env cannot bypass negative-retention validation", func(t *testing.T) {
		t.Setenv("AERO_ID_AUDIT_GOVERNANCE_DEAD_LETTER_RETENTION", "-1h")
		_, err := Load(writeTemp(t, base))
		if err == nil || !strings.Contains(err.Error(), "audit_governance.dead_letter_retention must not be negative") {
			t.Fatalf("Load error = %v, want containing the negative-retention message", err)
		}
	})
}

// TestAuditGovernanceAnchorSchemeValidation (T-5-1): the anchor scheme/version
// declaration is validated at Load() with the parity pattern — accepted
// exactly when it matches the compiled-in derivation, Enabled-independent
// (the ledger dedup guard runs without the relay), env cannot bypass.
func TestAuditGovernanceAnchorSchemeValidation(t *testing.T) {
	base := `
storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}
audit_ledger: {signing_key: change-me-please-very-long-audit-key}
signing_key: testkey
database: {dbname: aero_id, host: localhost, port: 5432, user: aero, password: x, sslmode: disable}
sso: {endpoint: "localhost:9090"}
`
	// Defaults: absent fields keep today's behavior and default to the scheme.
	cfg, err := Load(writeTemp(t, base))
	if err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if cfg.AuditGovernance.AnchorScheme != AnchorSchemeUUIDv5NulSeparated {
		t.Fatalf("default anchor scheme = %q, want %q", cfg.AuditGovernance.AnchorScheme, AnchorSchemeUUIDv5NulSeparated)
	}
	if cfg.AuditGovernance.AnchorSchemeVersion != AnchorSchemeVersionDefault {
		t.Fatalf("default anchor version = %d, want %d", cfg.AuditGovernance.AnchorSchemeVersion, AnchorSchemeVersionDefault)
	}

	// Explicit correct values pass.
	okYAML := base + "\naudit_governance: {anchor_scheme: uuidv5-nul-separated, anchor_scheme_version: 1}\n"
	if _, err := Load(writeTemp(t, okYAML)); err != nil {
		t.Fatalf("explicit correct scheme rejected: %v", err)
	}

	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"unknown scheme", "audit_governance: {anchor_scheme: uuidv4-random}", `anchor_scheme must be "uuidv5-nul-separated"`},
		{"unknown version", "audit_governance: {anchor_scheme_version: 2}", "anchor_scheme_version must be 1"},
		{"wrong scheme, relay disabled", "audit_governance: {enabled: false, anchor_scheme: uuidv4-random}", "anchor_scheme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, base+"\n"+tc.yaml+"\n"))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}

	// Env cannot bypass: a wrong env value over a valid file fails.
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME", "uuidv4-random")
	if _, err := Load(writeTemp(t, base+"\naudit_governance: {anchor_scheme: uuidv5-nul-separated}\n")); err == nil ||
		!strings.Contains(err.Error(), "anchor_scheme") {
		t.Fatalf("env cannot bypass validation: err = %v", err)
	}
	// A correct env value wins only when it is itself valid (viper lazy env
	// merge: the key exists in the file, so the env value is read).
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME", "uuidv5-nul-separated")
	cfg, err = Load(writeTemp(t, base+"\naudit_governance: {anchor_scheme: uuidv5-nul-separated}\n"))
	if err != nil {
		t.Fatalf("valid env scheme rejected: %v", err)
	}
	if cfg.AuditGovernance.AnchorScheme != "uuidv5-nul-separated" {
		t.Fatalf("merged anchor scheme = %q", cfg.AuditGovernance.AnchorScheme)
	}
}

// TestAuditGovernanceActivationGate pins B2-6 (F-03): enabling the relay
// with zero tenant bindings fails at Load() (the relay would silently drop
// every publication); disabled relay with empty bindings and enabled relay
// with a binding both load.
func TestAuditGovernanceActivationGate(t *testing.T) {
	base := `
storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}
audit_ledger: {signing_key: change-me-please-very-long-audit-key}
signing_key: testkey
database: {dbname: aero_id, host: localhost, port: 5432, user: aero, password: x, sslmode: disable}
sso: {endpoint: "localhost:9090"}
`
	const wantErr = "audit_governance.enabled requires at least one tenant binding"
	enabledEmpty := base + "\naudit_governance: {enabled: true}\n"
	if _, err := Load(writeTemp(t, enabledEmpty)); err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("enabled+empty Load error = %v, want containing %q", err, wantErr)
	}
	// Env-enable cannot bypass the gate (file-disabled + env-enabled fails).
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "true")
	if _, err := Load(writeTemp(t, base+"\naudit_governance: {enabled: false}\n")); err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("env-enabled+empty Load error = %v, want containing %q", err, wantErr)
	}
	// Disabled relay accepts empty bindings (the kill switch / pre-provision).
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "false")
	if _, err := Load(writeTemp(t, base+"\naudit_governance: {}\n")); err != nil {
		t.Fatalf("disabled+empty Load error = %v, want success", err)
	}
	// Enabled with a binding loads (positive leg).
	t.Setenv("TEST_AUDIT_SECRET", "audit-secret-value-000000000000000001")
	withBinding := strings.Replace(auditGovernanceBadYAML, `token_url: "http://sso.example/token"`, `token_url: "https://sso.example/token"`, 1)
	if _, err := Load(writeTemp(t, withBinding)); err != nil {
		t.Fatalf("enabled+binding Load error = %v, want success", err)
	}
}
