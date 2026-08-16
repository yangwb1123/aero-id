package config

import (
	"strings"
	"sync"
	"testing"
)

// Concurrency coverage for the B2-6 activation gate (contract item 6
// "locking tests"; REQ-1 T-10.2, REQ-2 T-4). The gate's correctness depends
// on the merged struct produced by viper env override inside Load: a race
// between env merge and validation — or a future hot-reload/parallel Load —
// would let an enabled-but-unbound relay boot. Every leg pins the
// AERO_ID_AUDIT_GOVERNANCE_* env before spawning goroutines (t.Setenv is
// process-wide and never called inside a goroutine) and the package stays
// sequential (no t.Parallel). Run mode: go test -race ./internal/config/...

// auditGovernanceActivationGateError is byte-identical to the documented
// error from validateAuditGovernance and to the parity const
// auditGovernanceActivationGateError in internal/app/audit.go — any drift
// fails here.
const auditGovernanceActivationGateError = "audit_governance.enabled requires at least one tenant binding (activation gate)"

// auditGovernanceAnchorSchemeError is the byte-identical error from
// validateAuditAnchor for a non-canonical scheme.
const auditGovernanceAnchorSchemeError = `audit_governance.anchor_scheme must be "uuidv5-nul-separated"`

// concurrencyBaseYAML is the minimal valid config used by the sequential
// gate tests (TestAuditGovernanceActivationGate); the governance block is
// appended per leg.
const concurrencyBaseYAML = `
storage: {type: local, local: {root: /tmp/files, public_base_url: "http://x"}}
audit_ledger: {signing_key: 5e8bc08391a257146c2dcffbe98bc4382f9757b048d14b3574368cfbcb57aa4d}
signing_key: testkey
database: {dbname: aero_id, host: localhost, port: 5432, user: aero, password: x, sslmode: disable}
sso: {endpoint: "localhost:9090"}
`

// concurrentLoadOutcome is one goroutine's Load result: either the merged
// config or the exact error text.
type concurrentLoadOutcome struct {
	cfg     *Config
	errText string
}

// loadConcurrent runs Load(path) in n goroutines against one shared file.
// The caller must pin env before calling (never inside a goroutine). Results
// land on distinct slice indices so the race detector stays clean, and all
// assertions run on the calling goroutine (t helpers are never called from
// spawned goroutines).
func loadConcurrent(t *testing.T, path string, n int) []concurrentLoadOutcome {
	t.Helper()
	outcomes := make([]concurrentLoadOutcome, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			cfg, err := Load(path)
			if err != nil {
				outcomes[i].errText = err.Error()
				return
			}
			outcomes[i].cfg = cfg
		}(i)
	}
	wg.Wait()
	return outcomes
}

// concurrentLeg describes one matrix leg: shared file content, env pinned
// before spawn, and either a byte-identical expected error or a merged-shape
// check on the Loaded config.
type concurrentLeg struct {
	name    string
	content string
	env     map[string]string
	wantErr string
	check   func(*testing.T, int, *Config)
}

// runConcurrentLeg runs one matrix leg: sets env (main goroutine), spawns
// the goroutines, then asserts every outcome on the calling goroutine.
func runConcurrentLeg(t *testing.T, leg concurrentLeg) {
	t.Run(leg.name, func(t *testing.T) {
		for k, v := range leg.env {
			t.Setenv(k, v)
		}
		outcomes := loadConcurrent(t, writeTemp(t, leg.content), 32)
		for i, o := range outcomes {
			if leg.wantErr != "" {
				if o.cfg != nil {
					t.Fatalf("goroutine %d: Load succeeded, want byte-identical failure %q", i, leg.wantErr)
				}
				if o.errText != leg.wantErr {
					t.Fatalf("goroutine %d: error = %q, want byte-identical %q", i, o.errText, leg.wantErr)
				}
				continue
			}
			if o.errText != "" {
				t.Fatalf("goroutine %d: Load failed: %s", i, o.errText)
			}
			leg.check(t, i, o.cfg)
		}
	})
}

// TestLoadActivationGateConcurrentMatrix (REQ-1, T-10.2): 32 concurrent
// Load() calls against the same file must partition deterministically:
// env-enabled + zero bindings all fail with the byte-identical gate error,
// enabled + one binding all pass with the merged Enabled=true / 1-binding
// shape, an anchor-scheme contradiction fails identically, and the kill
// switch (env-disabled) wins in every goroutine.
func TestLoadActivationGateConcurrentMatrix(t *testing.T) {
	provisioned := strings.Replace(auditGovernanceBadYAML,
		`token_url: "http://sso.example/token"`, `token_url: "https://sso.example/token"`, 1)

	runConcurrentLeg(t, concurrentLeg{
		name:    "env-enabled zero bindings all fail byte-identically",
		content: concurrencyBaseYAML + "\naudit_governance: {enabled: false}\n",
		env:     map[string]string{"AERO_ID_AUDIT_GOVERNANCE_ENABLED": "true"},
		wantErr: auditGovernanceActivationGateError,
	})
	runConcurrentLeg(t, concurrentLeg{
		name:    "enabled with binding all pass with merged shape",
		content: provisioned,
		env: map[string]string{
			"AERO_ID_AUDIT_GOVERNANCE_ENABLED": "true",
			"TEST_AUDIT_SECRET":                "audit-secret-value-000000000000000001",
		},
		check: func(t *testing.T, i int, cfg *Config) {
			t.Helper()
			gov := cfg.AuditGovernance
			if !gov.Enabled || len(gov.Bindings) != 1 {
				t.Fatalf("goroutine %d: merged governance = enabled=%v bindings=%d, want true/1", i, gov.Enabled, len(gov.Bindings))
			}
		},
	})
	runConcurrentLeg(t, concurrentLeg{
		name:    "anchor contradiction all fail byte-identically",
		content: concurrencyBaseYAML + "\naudit_governance: {anchor_scheme: uuidv5-nul-separated}\n",
		env: map[string]string{
			"AERO_ID_AUDIT_GOVERNANCE_ENABLED":       "false",
			"AERO_ID_AUDIT_GOVERNANCE_ANCHOR_SCHEME": "uuidv4-random",
		},
		wantErr: auditGovernanceAnchorSchemeError,
	})
	runConcurrentLeg(t, concurrentLeg{
		name:    "kill switch env-disable wins in every goroutine",
		content: concurrencyBaseYAML + "\naudit_governance: {enabled: true}\n",
		env:     map[string]string{"AERO_ID_AUDIT_GOVERNANCE_ENABLED": "false"},
		check: func(t *testing.T, i int, cfg *Config) {
			t.Helper()
			if cfg.AuditGovernance.Enabled {
				t.Fatalf("goroutine %d: kill switch lost, merged Enabled = true", i)
			}
		},
	})
}

// bootAttempt mirrors cmd/server/main.go's Load → err-check sequence
// (main.go:22-25): wouldBoot is the state that would reach app.New.
type bootAttempt struct {
	wouldBoot bool
	unbound   bool // would boot with Enabled=true and zero bindings (forbidden)
}

// TestBootSequenceConcurrentLoadNeverBootsUnbound (REQ-2, T-4): 32
// concurrent boot attempts against a shared Load path — even goroutines load
// the provisioned config, odd goroutines the unbound config — must never
// produce a would-be (Enabled=true, Bindings=0) boot state, and the mixed
// input must partition deterministically by file.
func TestBootSequenceConcurrentLoadNeverBootsUnbound(t *testing.T) {
	const n = 32
	t.Setenv("AERO_ID_AUDIT_GOVERNANCE_ENABLED", "true")
	t.Setenv("TEST_AUDIT_SECRET", "audit-secret-value-000000000000000001")

	valid := strings.Replace(auditGovernanceBadYAML,
		`token_url: "http://sso.example/token"`, `token_url: "https://sso.example/token"`, 1)
	validPath := writeTemp(t, valid)
	unboundPath := writeTemp(t, concurrencyBaseYAML+"\naudit_governance: {enabled: true}\n")

	attempts := make([]bootAttempt, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		path := validPath
		if i%2 == 1 {
			path = unboundPath
		}
		go func(i int, p string) {
			defer wg.Done()
			cfg, err := Load(p)
			if err != nil {
				return // main exits 1 here; no boot state
			}
			attempts[i].wouldBoot = true
			attempts[i].unbound = cfg.AuditGovernance.Enabled && len(cfg.AuditGovernance.Bindings) == 0
		}(i, path)
	}
	wg.Wait()

	for i, a := range attempts {
		if a.unbound {
			t.Fatalf("goroutine %d would boot an enabled-but-unbound relay", i)
		}
		if want := i%2 == 0; a.wouldBoot != want {
			t.Fatalf("goroutine %d wouldBoot = %v, want %v (deterministic input partition)", i, a.wouldBoot, want)
		}
	}
}
