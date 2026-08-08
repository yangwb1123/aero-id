# Design — B2-6 activation gate completion at the config layer: TokenURL validation, gate-matrix, coverage-race, and first-event e2e (T-10.1, T-10.2)

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-07-internal-config-b2-6-activation-gate.md` (REQ-1..REQ-4, T-10.1.1..T-10.1.6, T-10.2a..T-10.2d) |
| Module (batch label) | `internal/config` (wiring reaches `internal/app` + `internal/service` for the integration legs) |
| Direction | B2-6 activation gate: close the `binding.TokenURL` validation gap at config load with connector parity; pin the gate matrix, the coverage-race snapshot contract, and the first-event drain e2e |
| Status | Design — all requirement evidence re-verified against the working tree at head `13e4676` on 2026-08-07 (§1); production-code delta is one helper + one loop-statement in `internal/config/commercial.go` + one boot-exit statement in `cmd/server/main.go` (§3.1) |

## 1. Evidence verification (untrusted claims re-checked)

Every citation in the requirements evidence was re-checked against the live working tree. **All eight claims hold.** Line drifts already recorded in the requirements doc are re-confirmed; six additional facts that the design depends on are stated below the table.

| # | Evidence claim | Verdict |
|---|---|---|
| E1 | `commercial.go` — `validateAuditBindings` never validates `binding.TokenURL` | ✅ Exact. `validateAuditBindings` at `:54-71`: enabled-early-return `:55-57`, endpoint/bindings non-empty `:58-60`, per-tenant `canonicalIdentity`+`exactScopes`+`exactResources` `:62-64`, `registerCredential` `:66`. Repo-wide `grep -rn "\.TokenURL"` finds the struct field and the wiring read `app/audit.go:127` only. The connector is the sole enforcement point (`oauth.go:75-76`) — a bad `token_url` aborts boot at wiring (connector construction is eager, §4), not at `Load()` |
| E2 | `audit_governance.go` — `TokenURL` + `AllowInsecureLoopback` on the config surface | ✅ Exact. `TokenURL string \`mapstructure:"token_url"\`` at `:24`; `AllowInsecureLoopback bool` at `:19`. The flag the scheme rule needs already exists and is plumbed to the connector (`app/audit.go:102` for `NewOAuthTokenSource`, `:111` for `NewPublisher`) |
| E3 | `commercial_test.go` — no gate-matrix cases | ✅ Exact. Exactly 4 tests: credentials-distinctness `:8`, scopes/resources/tenant-set `:26`, error non-disclosure `:46`, control characters `:61`. Fixture helper `commercialTestConfig()` at `:77` |
| E4 | Connector rules: `oauth.go:75` `secureEndpoint`, `RawQuery == ""` at `:76`, rules at `types.go:89-108` | ✅ Exact. `newTenantTokenSource` `:71` trims then calls `secureEndpoint` `:75` and requires `endpoint.RawQuery == ""` `:76`; `secureEndpoint` `types.go:89-98` (non-empty host, no userinfo, no fragment, scheme https, or http iff `allowLoopback` && `localhost`/loopback-IP), `allowedLoopbackHTTP` `:100-108`. `Go url.Parse` lowercases the scheme, so `HTTPS://…` behaves as `https` on both sides |
| E5 | `app/audit.go:85` `validateAuditBindingCoverage` | ✅ Exact at `:134-152`. Single-statement `UNION` over `accounts.tenant_id` and pending/delivering/dead_letter `audit_publications` ⋈ `audit_ledger`, excluding `''`/`'legacy'`; error `"audit governance bindings do not cover durable tenant state"` at `:149`. Called from `buildAuditPublishers` `:96` before `NewOAuthTokenSource` `:99` |
| E6 | `audit_integration_test.go` — no race/lock test | ✅ Exact. `TestAuditBindingCoverageIncludesAccountsAndDurableBacklog` `:16-42` (sequential), `TestAuditLedgerRecordEnqueuesOnlyIntegration` `:49-78`. No concurrency test in the file |
| E7 | `callback_audit.go:47` `drainPublications` sole delivery path | ✅ Exact at `:38-66` (drift re-confirmed). `publisher.Publish` reachable only at `:114` inside `publishAuditFact` `:103-117`, itself reachable only from `finishPublicationAttempt` `:68` ← `drainPublications`; sole non-test call site `callback.go:281` in the `Start` ticker (`:268-282`, ticker at `:275`). `publishAuditFact` also calls `enqueueFact` `:20` first — the `callback_deliveries` dedup-receipt row T-10.2c asserts comes from here, not from `Record` |
| E8 | No succeeding `AuditPublisher` fake; connector imports no `config`; config tests green | ✅ Exact. Only `failingAuditPublisher` (`callback_audit_test.go:61-66`, `Publish` at `:65`); `staticTokenSource` (`callback_integration_test.go:64`) is a `TokenSource`, not an `AuditPublisher`. `grep -rn "internal/config" internal/connector/auditgovernance/` → empty, so the parity pin can import the connector from the config test package. `go test ./internal/config/...` → `ok` (10 top-level tests: 4 in `commercial_test.go` + 6 in `config_test.go`) |

**Additional verified facts the design depends on (beyond the evidence):**

- **F1 — Zero fixture churn:** `commercialTestConfig()` (`commercial_test.go:77-89`) already sets `TokenURL: "https://sso.example/token"` on the audit binding. The new rule accepts it, so the 4 existing tests (T-10.1.5) pass with no fixture edits — the baseline is not just green, it is already compliant.
- **F2 — Shipped config cannot regress:** `configs/config.yaml:174-186` has `audit_governance.enabled: false`, empty `bindings`, empty `endpoint`. The new validation only fires for `enabled: true`, so the out-of-box boot and every existing `Load` test are unaffected.
- **F3 — Config layer is a leaf:** `internal/config` imports only stdlib + `gopkg.in/yaml.v3`-family today (`validate.go` already imports `net/url`). Importing `internal/connector/auditgovernance` from the config *package* would drag `net/http`, `golang.org/x/sync/singleflight`, and `internal/domain` into the leaf for ~15 lines of rule — rejected (§3.2); the parity pin lives in the config *test* package where there is no cycle.
- **F4 — Race-test precedent is directly copyable:** `waitForLockPoll` (`internal/repository/audit_consolidation_integration_test.go:18-27`) polls `pg_stat_activity` for a `wait_event` (`advisory`/`transactionid`); the deterministic handshake test `TestAppendConcurrentDuplicateAcrossPartitions` `:48-102` — coordinator session conn holds `pg_advisory_lock` `:62`, `waitForLockPoll` `:73`, `pg_advisory_unlock` `:97`, `COMMIT` `:102` — and its direct-SQL `audit_ledger`/`audit_publications` INSERT column lists `:85-99` are the recipe for T-10.2a. Note the writer side uses the *transaction*-scoped `pg_advisory_xact_lock`: a session lock taken inside a transaction would survive `COMMIT` and leak (D1). The audit service path used by T-10.2b (`ledger.Record`) serializes per partition with `pg_advisory_xact_lock(hashtext(partition_key))` (`audit_ledger.go:128`) — already race-safe for the "no fact loss" leg.
- **F5 — Billing gap is the same shape, confirmed out of scope:** `internal/connector/billing/oauth.go:45-48` (`secureEndpoint` call `:45`, `RawQuery` check `:48`) mirrors the audit connector; `validateBillingBindings` (`commercial.go:34-52`) has the same TokenURL hole. Also `auditgovernance.NewPublisher` enforces `secureEndpoint` on `Config.Endpoint` (`publisher.go:34-35`). Both stay untouched (§9).
- **F6 — T-10.2c harness exists:** `startCallbackService` (`callback_integration_test.go:24-36`) + type-assert `svc.(*callbackService)` + `SetAuditPublicationRepository`/`SetAuditPublishers` pattern at `:92, 128, 207-209`; `TestDrainExhaustsAttemptsIntoDeadLetter` at `:193` is the drain-precedent sibling, and its `metrics := &metricSpy{}` + `svc.metrics = metrics` at `:210-211` (spy type `callback_audit_test.go:71`) is the outcome-metric capture pattern T-10.2c needs.

## 2. Goals and non-goals

**Goals.** (G1) Make the activation gate fail at `Load()` for every enabled configuration whose binding `TokenURL` the connector would reject at construction, using exactly the connector's rules (REQ-1). (G2) Pin every gate-matrix cell as config unit tests plus one Load-level case (REQ-2, T-10.1.1–T-10.1.5). (G3) Pin connector parity cross-layer with a single table-driven test so the gate cannot drift from the connector (REQ-1.2, T-10.1.6). (G4) Pin the coverage gate's snapshot/read-only contract with the deterministic advisory-lock handshake plus a bounded stress loop with tail assertions only (REQ-3, T-10.2a/b). (G5) Prove first-fact delivery is exclusively `Record` → `drainPublications` → exactly-once `completed` with a succeeding publisher (REQ-4, T-10.2c/d).

**Non-goals.** No behavior change to `Record`, `drainPublications`, `publishAuditFact`, `publicationOutcome`, or `validateAuditBindingCoverage` — REQ-4's code path is already shipped; the deliverable is pins. No new config fields (the `AllowInsecureLoopback` flag already exists, E2). No new migrations, no protobuf, no new packages, no new Makefile gates (test-only helpers allowed in `internal/config` and `internal/service` test packages). Billing `TokenURL`, governance `endpoint` scheme, and `source_prefix` stay out of scope (§9).

## 3. API changes

### 3.1 Production code — one helper + one loop statement + one boot-exit statement

No exported API changes. No interface changes. No config-surface changes (the `token_url` key and `allow_insecure_loopback` flag already exist, E2).

**New unexported helper** (≤50 lines, cyclop ≤10, per AGENTS.md limits):

```go
// validAuditTokenURL mirrors auditgovernance.newTenantTokenSource's URL
// rules (secureEndpoint + RawQuery) so a misconfigured enabled gate fails
// at Load() instead of connector construction at boot-wiring.
func validAuditTokenURL(raw string, allowLoopback bool) bool {
	value := strings.TrimSpace(raw)
	endpoint, err := url.Parse(value)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" ||
		endpoint.RawQuery != "" {
		return false
	}
	if endpoint.Scheme == "https" {
		return true
	}
	if endpoint.Scheme != "http" || !allowLoopback {
		return false
	}
	if strings.EqualFold(endpoint.Hostname(), "localhost") {
		return true
	}
	ip := net.ParseIP(endpoint.Hostname())
	return ip != nil && ip.IsLoopback()
}
```

Rules are byte-for-byte the connector's: trim → parse → non-empty host → no userinfo → no fragment → no raw query (`oauth.go:75-76`) → scheme https, or http only when `allowLoopback` and hostname is `localhost` (case-insensitive) or a loopback IP (`types.go:89-98`; loopback rule `allowedLoopbackHTTP` `:100-108`). Both sides use `net/url`, so scheme-case, port, and IDN behavior cannot diverge (F-verified: `url.Parse` lowercases schemes). New imports on `commercial.go`: `net` and `net/url` (`fmt`, `os`, `strings`, `unicode` already present).

**Modified loop in `validateAuditBindings`** (`commercial.go:61-69`) — insert one branch with defined error priority. The existing combined check and its message are preserved byte-identical (no existing test's expected text changes); the new branch sits between it and `registerCredential`:

```go
	for tenant, binding := range value.Bindings {
		if !canonicalIdentity(tenant, 256) || !exactScopes(binding.Scope, auditWriteScope) ||
			!exactResources(binding.Resources, auditResource) {
			return fmt.Errorf("audit governance contains an invalid tenant, scope, or resource binding")
		}
		if !validAuditTokenURL(binding.TokenURL, value.AllowInsecureLoopback) {
			return fmt.Errorf("audit governance binding contains an invalid token URL")
		}
		if err := registerCredential(binding.ClientID, binding.ClientSecretEnv, clients, secrets); err != nil {
			return err
		}
	}
```

Ordering contract (D2): the token-URL check runs **before** `registerCredential` so a URL error is never masked by a missing/short secret env (REQ-1.1 "before/independent of credential resolution") — T-10.1.2 asserts the URL error with secrets both set and unset. Final error priority for combined-invalid configs: tenant identity → scope/resource (existing combined message) → token URL (new message) → credentials (existing messages). The new message `audit governance binding contains an invalid token URL` (REQ-1.3) is non-disclosing — it never echoes the raw value. The `!value.Enabled` early return `:55-57` is untouched (REQ-1.4).

**Per-binding priority (F3 map-order note):** the chain above is a *per-binding* contract — it fixes the order in which one binding's checks are evaluated and guarantees the URL error is never masked by the credential check of the *same* binding. When *multiple* bindings carry different defects, Go map iteration order decides which binding's error is returned (pre-existing property of the `for tenant, binding := range value.Bindings` loop, identical to how scope-vs-credential conflicts behave today); boot always fails with a non-disclosing message either way. The T-10.1.3 matrix keeps the "vary exactly one dimension" discipline (FM-3) so no unit cell depends on cross-binding order.

**Second production delta — clean boot failure presentation (B-1).** The new gate error surfaces on the existing config-load path `cmd/server/main.go:20-22`, which today is `panic("load config: " + err.Error())` — a stack trace and exit 2, *worse* than the `log.Fatal` (exit 1, no stack trace) that the same bad config hits at boot-wiring today (`cmd/server/main.go:35-37`). The design therefore also changes `cmd/server/main.go:22-23` from `panic(...)` to `fmt.Fprintln(os.Stderr, "load config: "+err.Error()); os.Exit(1)` — one stderr line, no panic, no stack trace, exit 1; not worse than today's failure. (The logger is not yet initialized at that point, so stderr is the only sink; the identical pattern already ships in `cmd/migrate/main.go:31-33`.) `cmd/migrate` needs no change — it already prints `migrate: <err>` to stderr and exits 1, and its `config.Load` at `cmd/migrate/main.go:62` runs before the DB open at `:67` for every command (B-4, §6 step 4).

File-size impact: `commercial.go` is 161 lines today (requirements doc's 142-line estimate was stale) → ~190 with imports, the 24-line helper, and the inserted branch (≤500; helper ≤50 lines, cyclop ≤10). `cmd/server/main.go` grows 45 → ~48 lines (≤500; the B-1 delta is 2 statements). Alternative placement in `audit_governance.go` is acceptable (keeps `commercial.go` smaller) but the validator family lives in `commercial.go` today, so cohesion wins.

### 3.2 Rejected alternative — import the connector from the config package

`validateAuditBindings` could call `auditgovernance.NewOAuthTokenSource` directly. Rejected: it drags `net/http`, `singleflight`, and `internal/domain` into the config leaf, couples `Load()` to a network client's constructor (which also validates credentials, scopes, and source prefix — the gate must report the *URL* error with its own message), and inverts the current leaf dependency direction. Replication + the T-10.1.6 parity pin is the anti-drift guard instead; the pin fails red if either side changes without the other.

## 4. Compatibility constraints

| Constraint | Guarantee | Pinned by |
|---|---|---|
| Existing 4 commercial tests pass unchanged | Their fixture already uses `https://sso.example/token` (F1); new message text is disjoint from all existing expected texts (E3, §3.1) | T-10.1.5 |
| Shipped config boots | `audit_governance.enabled: false` (F2) → early return, no validation | T-10.1.1 disabled cells; existing `TestLoad_*` |
| Disabled gate accepts anything | `!Enabled` early return preserved (REQ-1.4) | T-10.1.1/T-10.1.3 disabled rows |
| No behavior change to audit runtime | `Record`/`drainPublications`/`publishAuditFact`/`validateAuditBindingCoverage` untouched | T-10.2c/d, keep-enforced greps |
| Billing bindings unchanged | `validateBillingBindings` untouched (§9) | existing `TestCommercialCredentialsAreDistinctAcrossDestinations` |
| Load-level error contract | `Load()` → `validate()` → `validateRuntimeLimits` → `validateCommercialIntegrations` is the single validation path (validate.go:130); no second path to drift | T-10.1.4 |
| Clean boot failure (B-1) | Gate failure prints one stderr line and exits 1 — no panic, no stack trace; not worse than today's `log.Fatal` path at boot-wiring (`cmd/server/main.go:37`) | §3.1 (B-1); error text at Load via T-10.1.4 |
| Error text stability | New message is a fixed literal; the URL value is never echoed; existing three messages verbatim (REQ-1.3) | T-10.1.2, existing non-disclosure test |
| No config-surface change | `token_url`/`allow_insecure_loopback` already shipped keys (E2); operators do not need to edit configs to keep booting | T-10.1.5 |

**Backward-compatibility risk assessment (B-2/F1):** the failing set is **unchanged** — only the stage moves earlier. In the shipped wiring, `buildAuditPublishers` runs at boot: `new.go:134` → `buildAuditStack` → `app/audit.go:46` → `buildAuditPublishers` `:87` constructs every binding eagerly via `NewOAuthTokenSource` `:99`, and the error propagates to `log.Fatal("initialize app", …)` (`cmd/server/main.go:37`). An enabled config with a bad `token_url` therefore already aborts startup today (exit 1, no serving, no lazy first-publish path); there is no "boots but is broken" window to eliminate — the process never serves. The gate moves the *same* failure from boot-wiring (after DB connect and any auto-migrate) to `Load()` (`cmd/server/main.go:20-22`, before DB connect) with a stable, non-disclosing message. No config that boots successfully today (connector construction succeeds) can be newly rejected, because the gate accepts exactly the set the connector accepts — byte-identical rules, including `RawQuery == ""` on both sides (`oauth.go:76`, §3.1) — pinned by T-10.1.6. One positive side effect: the gate removes today's hazard that auto-migrate applies migrations before the URL failure surfaces.

## 5. Failure modes

| ID | Failure mode | Detection | Mitigation / design answer |
|---|---|---|---|
| FM-1 | Gate drifts from the connector (over- or under-strict), so a config passes `Load()` but fails at wiring, or a working config stops booting | Parity pin T-10.1.6 goes red | Single table-driven test asserts accept/reject agreement per TokenURL case with `AllowInsecureLoopback` both values; both sides share `net/url` so parse-level behavior cannot diverge |
| FM-2 | URL error masked by secret-env error (env unset in CI), making T-10.1.2 non-deterministic | Test fails when secrets unset | D2 ordering: token-URL check precedes `registerCredential`; the cell asserts the URL error with secrets absent too |
| FM-3 | Combined-invalid configs get a different message than the matrix expects (error-priority ambiguity) | Unit cells assert exact message containment | §3.1 defines a *per-binding* priority: tenant identity → scope/resource → token URL → credentials; each matrix cell varies exactly one dimension, so no cell depends on cross-binding Go map iteration order (pre-existing property, F3) |
| FM-4 | T-10.2a race test flakes (gate sees the uncommitted row, or `waitForLockPoll` times out) | `waitForLockPoll(t, db, "advisory", 10s)` fails the test — fail-closed, never a false green | Deterministic handshake: a coordinator session conn holds `pg_advisory_lock(hashtext('coverage-gate-writer'))` *before* the writer spawns; the writer goroutine blocks on the transaction-scoped `pg_advisory_xact_lock` inside its own tx (auto-released at COMMIT/ROLLBACK — a session lock taken inside a tx would survive COMMIT and leak); the poll proves the writer is blocked *before* the gate call; the uncommitted row is invisible by MVCC, which the test additionally proves with a plain SELECT while the writer is blocked (REQ-3.1); then release the session lock → writer commits → gate re-run (T-10.2a) |
| FM-5 | T-10.2b stress loop asserts middle states and flakes | — | Only tail assertions after all writers join: gate fails with `bindings: {}`, all gate calls nil with full coverage, no fact loss, publications untouched (read-only). No mid-flight assertions |
| FM-6 | Gate mutates `audit_publications` (claims/settles), violating REQ-3.2 | T-10.2b assertion (3): every row `status='pending'`, `attempt=0`, `lease_owner=''` after the stress run | The gate is a read-only `SELECT`; the assertion makes the contract executable |
| FM-7 | Second drain tick re-delivers (exactly-once regression) | T-10.2c step (3): second `drainPublications` → no claim, publisher calls still 1, status still `completed` | `ClaimPublication` claims only `pending` due / `delivering` lease-expired rows (`repository/audit_publication.go:36-44`); `UNIQUE(fact_id)` + `callback_deliveries` dedup receipt (`enqueueFact`) back the at-least-once/unique-receipt contract |
| FM-8 | Operator with `enabled: true` + non-loopback `http` token_url + `allow_insecure_loopback: false` (prod) | Boot now fails at `Load()` with the stable message | This is the intended behavior (REQ-1); remediation in §6 step 4. The flag itself is unchanged |
| FM-9 | A token URL with a fragment/query is accepted by the gate but rejected by the connector | T-10.1.6 (both rejection directions) | Both sides reject fragment (`types.go:92`) and raw query (`oauth.go:76`); the pin covers `?x=1` and `#frag` rows |
| FM-10 | T-10.2c's drain picks up other pending rows (test pollution) | Assertions pin exact row counts (1 ledger, 1 publication, 1 delivery, 1 publisher call) | Fresh `testpg.Start`/`Migrate` DB per test (harness F6); seed exactly one fact |

## 6. Migration steps

No schema migration, no proto regeneration, no config-file migration, no new packages. The "migration" is a boot-behavior change for enabled gates plus test additions, in this order:

1. **Land the production delta (§3.1):** helper + loop statement in `internal/config/commercial.go` and the clean boot-exit change in `cmd/server/main.go` (B-1). Unit leg runs green because the existing fixture is already compliant (F1) and the shipped config is disabled (F2).
2. **Add config unit pins (T-10.1.1–T-10.1.3, T-10.1.5):** extend `internal/config/commercial_test.go` (or a sibling `commercial_gate_test.go` — either is fine; the file is 89 lines, extending is cheaper). Table-driven matrix over `commercialTestConfig()` mutations; the parity pin (T-10.1.6) imports `github.com/aero/aero-id/internal/connector/auditgovernance` in the **test** package (no cycle, E8/F3).
3. **Add the Load-level pins (T-10.1.4):** one temp-YAML `TestLoad_*`-style case plus two env-merged `AERO_ID_*` cases (B-3) in `internal/config/config_test.go` (precedents `TestLoad_fromFile` `:45`, `TestLoad_envOverride` `:58`).
4. **Ops remediation note (documented, no code; B-4):** this is a *preflight*, not a rescue — a deployment with `enabled: true` and an invalid `token_url` is not serving today (it already aborts at boot-wiring, §4). Fix `token_url` to `https://…` (or, for local dev only, set `allow_insecure_loopback: true` with a loopback URL) *before* upgrading so the earlier-stage failure never fires on the new binary. Upgrade-ordering facts to record in the runbook: (a) `cmd/migrate` calls `config.Load` before opening the DB (`cmd/migrate/main.go:62` → `:66`) for **every** command, so `migrate up` is the first new-binary gate — with a bad config it refuses to run before touching the database; (b) `migrate version` is a zero-code preflight — running the new binary's `migrate version` on a target host fails at Load (stderr, exit 1, no DB connection) iff the config would gate, so it doubles as a config precheck before the rollout; (c) runbooks keyed on today's failure text — `initialize app … audit governance oauth: …` (`cmd/server/main.go:37`) or the `load config:` panic text — stop firing; the new stable text is `load config: audit governance binding contains an invalid token URL` (stderr, exit 1). Deployments with `enabled: false` (including the shipped config) are unaffected and can upgrade freely.
5. **Add integration pins (T-10.2a/b in `internal/app/audit_integration_test.go`; T-10.2c in `internal/service/callback_integration_test.go`):** copy `waitForLockPoll` (F4) into the app test file; add `recordingAuditPublisher` beside `failingAuditPublisher` (package `service`, file-local to `callback_integration_test.go`); T-10.2c captures the outcome metric with the existing `metricSpy` (`callback_audit_test.go:71`) via the `svc.metrics = metrics` pattern (`callback_integration_test.go:210-211`). Integration tests run via `make test-integration` (testcontainers; requires Docker).
6. **Run the done-gate:** §8.

Rollback: the change is one helper, one loop statement, one boot-exit statement, plus tests; reverting the commit restores the later-stage boot-wiring failure with no data impact (no rows, no migrations, no config keys touched).

## 7. Testable acceptance mapping

| ID | Requirement | Test pin (file) | Executable assertions |
|---|---|---|---|
| T-10.1.1 | REQ-2 | `internal/config/commercial_test.go` (extend) | Enabled+empty `Bindings` and enabled+`Endpoint ""`/whitespace → `validateCommercialIntegrations` error containing `audit governance requires an endpoint and tenant bindings`; disabled+empty and disabled+bad-TokenURL → `nil`. Mutate `commercialTestConfig()`; keep `setCommercialSecrets` for credential legs |
| T-10.1.2 | REQ-1 | same file | Enabled valid config, one binding `TokenURL: ""` (and whitespace-only) → error contains `audit governance binding contains an invalid token URL`; asserted with and without `setCommercialSecrets` — the no-secrets leg must also set `Billing.Enabled = false` on the mutated fixture, because `validateCommercialIntegrations` runs `validateBillingBindings` first (`commercial.go:21-23`) and billing's credential error would mask the URL error (proves D2 ordering); plus a negative substring assertion (pattern of `TestCommercialValidationErrorsDoNotDiscloseBindingMaterial` `:46`) that the error text does **not** contain the raw URL value (F4 leakage pin) |
| T-10.1.3 | REQ-1 | same file | Scheme/loopback table, `AllowInsecureLoopback` both values: `http://sso.example/token` reject-both; `http://localhost/token`, `http://127.0.0.1/token`, `http://[::1]/token` accept-iff-loopback; `https://sso.example/token` accept-both; `https://sso.example/token?` accept-both (`ForceQuery=true`, `RawQuery==""` — both sides accept; pinned so a future "cleanup" cannot break parity asymmetrically, F2); `not-a-url`, `ftp://…`, `https://u:p@…`, `…/token#frag`, `…/token?x=1` reject; `"  https://sso.example/token  "` accept (trim) |
| T-10.1.4 | REQ-1/REQ-2 | `internal/config/config_test.go` | Temp-YAML `Load(path)` (precedent `TestLoad_fromFile` `:45`) with `enabled: true`, endpoint set, one binding carrying a **valid** tenant/scope/resources (`tenant-one` / `audit:event:write` / `[audit-governance]`) so the combined tenant/scope/resource error cannot fire first, and `token_url: http://sso.example/token`; billing left disabled (YAML default) so its credential error cannot mask the URL error; secret env may be unset (D2 ordering) → error contains `audit governance binding contains an invalid token URL` — failure at load, not at boot-wiring. Plus two env-merged `AERO_ID_*` cases (precedent `TestLoad_envOverride` `:58`; env wiring `config.go:374-380` — viper env reaches scalar keys only; `bindings` is a map and not env-addressable): (1) file `enabled: false` with endpoint set and one binding `token_url: http://sso.example/token` (valid tenant/scope/resources) + env `AERO_ID_AUDIT_GOVERNANCE_ENABLED=true` → Load fails with the URL error (env cannot bypass the gate); (2) file `enabled: true` + `token_url: http://localhost/token` + env `AERO_ID_AUDIT_GOVERNANCE_ALLOW_INSECURE_LOOPBACK=true` → Load succeeds (the env flag lands in the same merged struct the gate and the connector read — parity survives env overrides; B-3). The documented kill switch also holds via env: `AERO_ID_AUDIT_GOVERNANCE_ENABLED=false` on a bad config → Load succeeds (REQ-1.4) |
| T-10.1.5 | REQ-2 | `internal/config/commercial_test.go` | The 4 existing tests pass unchanged (baseline `ok`, F1) |
| T-10.1.6 | REQ-1.2 | `internal/config/commercial_test.go` (imports connector; no cycle, E8) | Parity pin: for every T-10.1.3 TokenURL case, assert agreement between `validateCommercialIntegrations` (secret env ≥32 chars) and `auditgovernance.NewOAuthTokenSource` (full valid `OAuthConfig`: `SourcePrefix: "aero-id"`, canonical scope `audit:event:write`, `Resources: ["audit-governance"]`, ≥32-char secret, `AllowInsecureLoopback` matched; only `TokenURL` varies). No config that passes `validate()` may fail connector construction on the TokenURL dimension |
| T-10.2a | REQ-3.1/3.3 | `internal/app/audit_integration_test.go` (extend; `waitForLockPoll` copied, F4) | Deterministic advisory-lock handshake, fully specified (D1): (1) open a **coordinator session conn** (`sqlDB.Conn`) and execute `SELECT pg_advisory_lock(hashtext('coverage-gate-writer'))` — the lock is held *before* the writer spawns (precedent `audit_consolidation_integration_test.go:62`); (2) spawn a **writer goroutine** on its own conn: `BEGIN` → direct INSERT `audit_ledger` row (tenant `race-unbound`, column lists per `audit_consolidation_integration_test.go:85-99`) + `pending` `audit_publications` row → `SELECT pg_advisory_xact_lock(hashtext('coverage-gate-writer'))` — transaction-scoped, so it cannot leak past COMMIT and it *blocks* until the coordinator releases; (3) `waitForLockPoll(t, db, "advisory", 10s)` — fail-closed: a writer that never blocks times out the poll and `Fatalf`s, never a false green; (4) **while the writer is blocked**, call the gate `validateAuditBindingCoverage(db, bindings{})` → `nil`, and run a plain `SELECT count(*) FROM audit_ledger WHERE tenant_id='race-unbound'` → 0 — MVCC invisibility proof (REQ-3.1); (5) coordinator `SELECT pg_advisory_unlock(hashtext('coverage-gate-writer'))` (precedent `:97`) → writer's xact lock acquires → writer `COMMIT`; (6) gate again with `bindings: {}` → `"audit governance bindings do not cover durable tenant state"`; with the `race-unbound` binding present → `nil`. Conn budget fits the test pool (`MaxOpenConns: 5` in `internal/repository/testpg/testpg.go:42`: coordinator + writer + gate/poll ≤ 4) |
| T-10.2b | REQ-3.2/3.3 | same file | Stress: 8 writers (4× `ledger.Record` tenants `race-N`, 4× `accounts.EnsureByIdentity("race-N", …)` with **distinct per-writer `(issuer, subject)`** — e.g. issuer `race-issuer`, subject `race-N` — so writers never contend on the `(issuer, subject)` unique index; `EnsureByIdentity` signature `(ctx, tenantID, issuer, subject, …)` at `account_identity.go:20`), 4 gate goroutines × 20 calls with `bindings: {}`; join; then: (1) every fact exactly once in `audit_ledger`; (2) every `audit_publications` row `pending`/`attempt=0`/`lease_owner=''` (read-only proof); (3) tail gate call → error (deterministic); (4) rerun with full coverage → all gate calls `nil` |
| T-10.2c | REQ-4.1/4.2 | `internal/service/callback_integration_test.go` (sibling of `TestDrainExhaustsAttemptsIntoDeadLetter` `:193`); new `recordingAuditPublisher` (records `*domain.AuditFact`, returns nil) | Harness F6; seed one enabled+verified subscription (tenant-one) with **action `account.provisioned` and record the first fact with the same `Action`** — `enqueueFact` materializes delivery rows only for matching `(tenant, action)` (`ListEnabledByAction`, `callback_audit.go:20-37`); `svc.metrics = metricSpy` (`callback_integration_test.go:210-211`) to capture the outcome metric. `ledger.Record` first fact with known `EventID`; (1) after `Record`: 1 ledger row, 1 `pending` publication (`attempt=0`), `publisher.calls == 0`, 0 `callback_deliveries` rows (no inline loop, no inline enqueue); (2) `svc.drainPublications(ctx)`: publication `completed`, `error_code ''`, publisher received exactly 1 fact with matching `EventID`/`TenantID`/`Action`, exactly 1 `callback_deliveries` row, metricSpy outcome `[completed]`; (3) second `drainPublications`: no claim, `publisher.calls` still 1, status still `completed` (exactly-once delivery over at-least-once transport) |
| T-10.2d | REQ-4.3 | done-gate (no new files) | `grep -rn "\.Publish(ctx, fact)" internal/service/` → only `callback_audit.go:114`; `grep -rn "drainPublications(" internal/ --include='*.go' | grep -v _test.go` → `callback_audit.go:38` + `callback.go:281` only (the `_test.go` filter is required: three test call sites already exist today — `callback_integration_test.go:235,285`, `callback_audit_test.go:184` — and T-10.2c adds more; D2); `make check-audit-swallow` green; `TestAuditLedgerRecordEnqueuesOnlyIntegration` green unchanged |

## 8. Engineering gates

- Unit leg: `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build`. `commercial.go` 161 → ~190 lines (≤500); helper one function ≤50 lines / cyclop ≤10; `cmd/server/main.go` 45 → ~48 lines (≤500, B-1 delta).
- Integration leg: `make test-integration` — mandatory: T-10.2a/b touch repository SQL (advisory locks, direct inserts) and T-10.2c drives the real publication repository (AGENTS.md integration gate).
- Anti-drift guard: T-10.1.6 fails red if the connector's URL rules or the config gate change without the other.
- File-size check applies to non-test files only; the two new test helpers (`waitForLockPoll` copy, `recordingAuditPublisher`) live in `_test.go` files.

## 9. Follow-ups (explicitly out of scope)

1. **Billing `TokenURL`** — identical gap shape (`validateBillingBindings` `commercial.go:34-52`; connector rule `billing/oauth.go:45-48`). Reuse the same parity-pin pattern; needs its own acceptance wording (billing's connector additionally gates `validBillingScopes`).
2. **Governance `endpoint` scheme** — `NewPublisher` enforces `secureEndpoint` on `Config.Endpoint` (`publisher.go:34-35`) at wiring time only; the empty-endpoint cell is already a load error (E1 `:58-60`) but non-empty bad-scheme endpoints still fail late.
3. **`source_prefix`** — `validSourcePrefix` (`types.go:71-83`) enforced only at connector construction.
4. **Shared URL-rule extraction** — if a third consumer appears, extract `secureEndpoint`-family into a shared leaf (e.g., `internal/pkg/secureurl`) rather than replicating a third time; deliberately not done here to avoid a new package for one ~15-line rule.
