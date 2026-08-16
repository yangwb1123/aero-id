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
audit_ledger: {signing_key: 5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d}
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
	const validLine = "audit_ledger: {signing_key: 5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d}"
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

// billingBadYAML mirrors the auditGovernanceBadYAML discipline: an enabled
// billing block with a valid endpoint and valid tenant/scope/resources but an
// invalid token_url (http non-loopback, loopback off). Secret env may be
// unset: the URL check precedes credentials (D2), so a credential error can
// never mask the URL error.
const billingBadYAML = sample + `
billing:
  enabled: true
  endpoint: "https://billing.example"
  timeout: 5s
  max_body_bytes: 262144
  allow_insecure_loopback: false
  bindings:
    tenant-one:
      token_url: "http://sso.example/token"
      client_id: billing-client
      client_secret_env: TEST_BILLING_SECRET
      scope: "billing:entitlement:read metering:write"
      resources: ["billing-api"]
`

// TestLoadBillingTokenURLGate: the billing URL gate fails at Load(), before
// boot-wiring, with the stable non-disclosing message. The URL check precedes
// credentials (D2), so an unset secret env can never mask the URL error; the
// raw URL is never echoed (userinfo and raw query included).
func TestLoadBillingTokenURLGate(t *testing.T) {
	const wantErr = "billing binding contains an invalid token URL"
	cells := map[string]string{
		"http non-loopback": "http://sso.example/token",
		"userinfo":          "https://u:p@sso.example/token",
		"raw query":         "https://sso.example/token?x=1",
	}
	for name, raw := range cells {
		t.Run(name, func(t *testing.T) {
			yaml := strings.Replace(billingBadYAML,
				`token_url: "http://sso.example/token"`, `token_url: "`+raw+`"`, 1)
			_, err := Load(writeTemp(t, yaml))
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("Load error = %v, want containing %q (fail at load, not boot-wiring)", err, wantErr)
			}
			if strings.Contains(err.Error(), raw) {
				t.Fatalf("Load error disclosed the raw URL %q: %v", raw, err)
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

// TestDataRetentionPurgePausedValidation (T-5.1): the purge_paused flag is
// accepted only when the outbox cleaner is actually wired; negative outbox
// retentions are rejected (they would purge every eligible row on the next
// tick).
func TestDataRetentionPurgePausedValidation(t *testing.T) {
	base := `
storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}
audit_ledger: {signing_key: 5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d}
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
audit_ledger: {signing_key: 5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d}
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
audit_ledger: {signing_key: 5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d}
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
audit_ledger: {signing_key: 5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d}
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
audit_ledger: {signing_key: 5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d}
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

// TestStorageGCPurgePaused (storage GC pause): purge_paused parses and is
// rejected when the sweep is disabled (the flag would be inert).
func TestStorageGCPurgePaused(t *testing.T) {
	base := `
storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}
audit_ledger: {signing_key: 5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d}
signing_key: testkey
database: {dbname: aero_id, host: localhost, port: 5432, user: aero, password: x, sslmode: disable}
sso: {endpoint: "localhost:9090"}
`
	// Pause with the sweep enabled and valid: accepted (gc keys inline in the
	// single storage block — yaml forbids duplicate top-level keys).
	ok := strings.Replace(base,
		`storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}`,
		`storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}, gc: {enabled: true, interval: 1h, retention: 168h, purge_paused: true}}`, 1)
	cfg, err := Load(writeTemp(t, ok))
	if err != nil {
		t.Fatalf("valid paused gc config rejected: %v", err)
	}
	if !cfg.Storage.GC.PurgePaused {
		t.Fatal("PurgePaused = false, want true")
	}
	// Pause with the sweep disabled: rejected (never inert).
	bad := strings.Replace(base,
		`storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}`,
		`storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}, gc: {enabled: false, purge_paused: true}}`, 1)
	if _, err := Load(writeTemp(t, bad)); err == nil || !strings.Contains(err.Error(), "purge_paused requires storage.gc.enabled") {
		t.Fatalf("disabled gc with pause err = %v, want rejection", err)
	}
	// Defaults: absent flag is false.
	cfg, err = Load(writeTemp(t, base))
	if err != nil {
		t.Fatalf("default config rejected: %v", err)
	}
	if cfg.Storage.GC.PurgePaused {
		t.Fatal("PurgePaused default = true, want false")
	}
}

// repoRootConfigTest walks up from the test binary's CWD (the package
// directory under `go test`) to the module root, mirroring testpg.RepoRoot
// without the integration-tagged dependency (config tests are unit tests).
func repoRootConfigTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := dir; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
	}
	t.Fatalf("repo root not found from %s", dir)
	return ""
}

// TestLoadShippedConfigFileAndEnvGate (REQ-3, T-10.2, L5.1/L10.2.2): the
// shipped configs/config.yaml must Load as-is (governance disabled, zero
// bindings) with the file-derived knob values intact — this is the first
// full-file boot leg — and env-enabling it must trip the activation gate
// with the documented error (viper env merge happens inside Load, before
// validate, so the gate reads the merged config and env cannot bypass it).
// Every real-file Load pins AERO_ID_AUDIT_GOVERNANCE_ENABLED=false first so
// ambient CI env cannot leak in; t.Setenv restores automatically and the
// package stays sequential (no t.Parallel).
func TestLoadShippedConfigFileAndEnvGate(t *testing.T) {
	shipped := filepath.Join(repoRootConfigTest(t), "configs", "config.yaml")

	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "false")
	t.Setenv("AERO_ID_BILLING_ENABLED", "false")
	cfg, err := Load(shipped)
	if err != nil {
		t.Fatalf("shipped configs/config.yaml must load: %v", err)
	}
	gov := cfg.AuditGovernance
	if gov.Enabled {
		t.Fatal("shipped file governance enabled = true, want false (disabled until bindings exist)")
	}
	if len(gov.Bindings) != 0 {
		t.Fatalf("shipped file bindings = %d, want 0", len(gov.Bindings))
	}
	bill := cfg.Billing
	if bill.Enabled {
		t.Fatal("shipped file billing enabled = true, want false (disabled until bindings exist)")
	}
	if len(bill.Bindings) != 0 {
		t.Fatalf("shipped file billing bindings = %d, want 0", len(bill.Bindings))
	}
	// File-derived knob values (configs/config.yaml:177-184) — the boot leg
	// pins these on the wire; pin them at Load so file edits fail loudly.
	for name, got := range map[string]string{
		"source_prefix":       gov.SourcePrefix,
		"actor_id":            gov.ActorID,
		"schema_id":           gov.SchemaID,
		"data_classification": gov.DataClassification,
		"retention_class":     gov.RetentionClass,
		"anchor_scheme":       gov.AnchorScheme,
	} {
		want := map[string]string{
			"source_prefix": "aero-id", "actor_id": "aero-id", "schema_id": "aero.id.audit-fact",
			"data_classification": "personal", "retention_class": "account_7y",
			"anchor_scheme": "uuidv5-nul-separated",
		}[name]
		if got != want {
			t.Fatalf("shipped file %s = %q, want %q (file drifted)", name, got, want)
		}
	}
	if gov.SchemaVersion != 1 || gov.AnchorSchemeVersion != 1 {
		t.Fatalf("shipped file schema/anchor versions = %d/%d, want 1/1", gov.SchemaVersion, gov.AnchorSchemeVersion)
	}

	// Env leg: env-enabling the shipped file must fail with the documented
	// activation-gate error (the file carries zero bindings, and env cannot
	// conjure a nested bindings map).
	const wantErr = "audit_governance.enabled requires at least one tenant binding (activation gate)"
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "true")
	if _, err := Load(shipped); err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("env-enabled shipped file Load err = %v, want containing %q", err, wantErr)
	}
}

// TestLoadRejectsPlaceholderSecrets (REQ-C1/REQ-C4): both shipped signing-key
// literals and the conventional placeholder families (change-me, changeme,
// replace-me) are denied at Load() for the root signing_key and
// audit_ledger.signing_key alike. Each row is applied to both fields; the
// ≥32-byte prefix rows guarantee the failure is the deny error, never the
// length error (deny-before-length ordering in validateAuditLedger). The
// positive leg proves the fixed committed hex audit key and a plain root key
// still load.
func TestLoadRejectsPlaceholderSecrets(t *testing.T) {
	const fixedHexAuditKey = "5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d"
	const validAuditLine = "audit_ledger: {signing_key: " + fixedHexAuditKey + "}"
	if _, err := Load(writeTemp(t, sample)); err != nil {
		t.Fatalf("sample with valid signing keys rejected: %v", err)
	}
	const wantErr = "signing_key is a known placeholder"
	rows := []struct {
		name string
		key  string
	}{
		{"shipped audit literal", "change-me-please-very-long-audit-key"},
		{"shipped root literal", "change-me-please-very-long-random"},
		{"change-me prefix, 32+ bytes", "change-me-" + strings.Repeat("x", 45)},
		{"changeme prefix, 32+ bytes", "changeme-" + strings.Repeat("x", 45)},
		{"replace-me prefix, 32+ bytes", "replace-me-" + strings.Repeat("x", 45)},
	}
	for _, tc := range rows {
		t.Run("audit/"+tc.name, func(t *testing.T) {
			content := strings.Replace(sample, validAuditLine, "audit_ledger: {signing_key: "+strconv.Quote(tc.key)+"}", 1)
			_, err := Load(writeTemp(t, content))
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("Load error = %v, want containing %q", err, wantErr)
			}
		})
		t.Run("root/"+tc.name, func(t *testing.T) {
			content := strings.Replace(sample, "signing_key: testkey", "signing_key: "+strconv.Quote(tc.key), 1)
			_, err := Load(writeTemp(t, content))
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("Load error = %v, want containing %q", err, wantErr)
			}
		})
	}

	// Positive leg: the fixed committed hex audit key and a plain root key
	// load, with the merged audit key intact.
	cfg, err := Load(writeTemp(t, sample))
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.AuditLedger.SigningKey != fixedHexAuditKey {
		t.Fatalf("AuditLedger.SigningKey = %q, want the fixed hex fixture", cfg.AuditLedger.SigningKey)
	}
}

// TestLoadRejectsPlaceholderSecretEnvLeg (REQ-C2): Load merges AERO_ID_* env
// into the struct before validate, so a placeholder supplied via env over a
// valid file still fails with the pinned deny error — env cannot bypass the
// deny-list. Mirrors TestLoadAuditGovernanceKnobEnvLegs (env pinned before
// Load; the package stays sequential, no t.Parallel). The audit ledger key
// exists in the file (viper lazy env merge: env wins when the key exists), so
// a green leg proves env plumbing AND the deny check, never a wrong-reason
// pass.
func TestLoadRejectsPlaceholderSecretEnvLeg(t *testing.T) {
	const wantErr = "signing_key is a known placeholder"

	t.Run("audit ledger env cannot bypass", func(t *testing.T) {
		t.Setenv("AERO_ID_AUDIT_LEDGER_SIGNING_KEY", "change-me-please-very-long-audit-key")
		_, err := Load(writeTemp(t, sample))
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("Load error = %v, want containing %q (env placeholder must not bypass)", err, wantErr)
		}
	})

	t.Run("root env cannot bypass", func(t *testing.T) {
		t.Setenv("AERO_ID_SIGNING_KEY", "change-me-please-very-long-random")
		_, err := Load(writeTemp(t, sample))
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("Load error = %v, want containing %q (env placeholder must not bypass)", err, wantErr)
		}
	})
}
