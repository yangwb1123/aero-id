# Requirements — B2-6 activation gate completion at the config layer: binding TokenURL validation + gate-matrix, coverage-race, and first-event e2e tests (T-10.1, T-10.2)

| | |
|---|---|
| Module (batch label) | `internal/config` (gate wiring reaches `internal/app` + `internal/service` for T-10.2) |
| Source analysis | `docs/auto/analyses/internal-config-a932ee1e.json` (direction 1 of 3: `b2-6-activation-gate-config`) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-6 follows B2-1/B2-2; contract `docs/proposals/audit-contract-batch-aero-id.md` §B2-6) |
| Contract | `docs/proposals/audit-contract-batch-aero-id.md` line 18: "空 bindings + enabled → boot 失败**已实现**，补测试锁定 + 首个事件 e2e 编排" |
| Date | 2026-08-07 |
| Status | Draft — every cited file/symbol re-verified against the repository working tree at head `13e4676` (uncommitted B2-1/B2-2 rewiring is present and is the contract to keep, not re-do) |

## 1. Evidence verification status

All direction citations were re-checked against the working tree. The gate exists and is incomplete exactly
as claimed; line numbers drift in two citations (noted below). `go test ./internal/config/...` is green at
verification time, so "existing cases stay green" has a measured baseline.

| # | Claim (direction evidence) | Verification (working tree @ `13e4676`) |
|---|---|---|
| E1 | `internal/config/commercial.go:64` — `validateAuditBindings` never validates `binding.TokenURL` | ✅ stands, line drift (`:54`, loop body `:61-69`). The function checks endpoint/bindings non-empty (`:58-60`), per-tenant `canonicalIdentity` + `exactScopes` + `exactResources` (`:62-64`), and `registerCredential` (`:66`). `binding.TokenURL` is never read. An empty or non-https `token_url` passes `validate()` and fails only later at boot-wiring — connector construction is eager at boot (`new.go:134` → `buildAuditPublishers` → `NewOAuthTokenSource` `app/audit.go:99`) and aborts startup; never at `Load()` |
| E2 | `internal/config/audit_governance.go` — `AuditGovernanceBindingConfig.TokenURL` | ✅ exact: `TokenURL string \`mapstructure:"token_url"\`` plus `AllowInsecureLoopback bool` on `AuditGovernanceConfig` (`:19`) — the flag the scheme rule needs is already in the config surface |
| E3 | `internal/config/commercial_test.go` — no gate-matrix cases (enabled+empty bindings, enabled+missing endpoint, disabled+empty, TokenURL scheme/loopback) | ✅ verified: the four tests cover credentials-distinctness (`:8`), exact scopes/resources + same tenant set (`:26`), error non-disclosure (`:46`), control characters (`:61`). No enabled/disabled matrix, no TokenURL case. Helper `commercialTestConfig()` (`:77`) builds a valid enabled+endpoint+bindings fixture to mutate |
| E4 | `internal/connector/auditgovernance/oauth.go:75` — `secureEndpoint(binding.TokenURL, config.AllowInsecureLoopback)`: enforcement at connector construction (eager at boot wiring), not at config load | ✅ exact: `newTenantTokenSource` calls `secureEndpoint` at `:75` and additionally requires `endpoint.RawQuery == ""` at `:76`; `secureEndpoint` at `internal/connector/auditgovernance/types.go:89-98` (https, or http only when `allowLoopback` && hostname is `localhost` or a loopback IP; no userinfo, no fragment, non-empty host; input trimmed), `allowedLoopbackHTTP` `:100-108`. These are the exact rules the config gate must mirror |
| E5 | `internal/app/audit.go:85` — `validateAuditBindingCoverage` | ✅ stands, line drift (`:134-152`). Single-statement `UNION` over `accounts.tenant_id` and pending/delivering/dead_letter `audit_publications` joined to `audit_ledger`, excluding `''`/`'legacy'`; any durable tenant missing from `bindings` → error `"audit governance bindings do not cover durable tenant state"` (`:149`). Called from `buildAuditPublishers` (`:96`) before `NewOAuthTokenSource` (`:99`) |
| E6 | `internal/app/audit_integration_test.go:15` — single sequential coverage test; no race/lock test | ✅ exact: `TestAuditBindingCoverageIncludesAccountsAndDurableBacklog` (`:16-42`) is sequential (Record → EnsureByIdentity → two gate calls). `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`:49-78`, tag T-10.2a/REQ-1) pins enqueue-only at `Record` (1 ledger row + 1 pending publication row, no delivery). No concurrency test exists |
| E7 | `internal/service/callback_audit.go:47` — `drainPublications` sole delivery path | ✅ stands, line drift (`:38`). `publisher.Publish(ctx, fact)` reachable only from `publishAuditFact` (`:114`), which is reachable only from `finishPublicationAttempt` (`:68`) ← `drainPublications` (`:38`); the only non-test call site of `drainPublications` is the `Start` ticker (`internal/service/callback.go:281`). Static-pin greps from the sibling B2-2 spec hold verbatim |
| E8 | First-event e2e gap | ✅ confirmed: `TestAuditLedgerRecordEnqueuesOnlyIntegration` proves Record-time enqueue-only, and `TestDrainExhaustsAttemptsIntoDeadLetter` (`internal/service/callback_integration_test.go:193`) drives the drain but seeds via `ledger.Append` directly with a **failing** publisher. No test drives `Record` (first fact) → `drainPublications` → `completed` with a succeeding publisher. No succeeding/recording `AuditPublisher` fake exists yet (only `failingAuditPublisher`, `callback_audit_test.go:61-66`, `Publish` at `:65`) |
| E9 | Race-test precedent to reuse | ✅ `TestAppendConcurrentDuplicateAcrossPartitions` (`internal/repository/audit_consolidation_integration_test.go:48-102`) and helper `waitForLockPoll` (`:18-27`) prove the deterministic advisory-lock handshake pattern (coordinator session lock `:62`, `pg_advisory_unlock` `:97`); direct-SQL `audit_ledger`+`audit_publications` INSERT column lists are proven at `:85-99` |

Baseline: `go test ./internal/config/...` → `ok` (10 top-level tests, all current commercial tests green).

## 2. Problem statement (current state)

The boot-failure gate for audit governance exists but is incomplete and untested at the edges:

1. **`validateAuditBindings` never validates `binding.TokenURL`** (E1/E2). A config with
   `audit_governance.enabled: true`, a non-empty endpoint and bindings whose `token_url` is empty, plain
   `http`, non-loopback-http with `allow_insecure_loopback: true`, or malformed passes `config.validate()`
   (`internal/config/validate.go:130` → `validateCommercialIntegrations` → `validateAuditBindings`) and
   `Load()` succeeds. The failure surfaces only later at boot-wiring: connector construction is eager
   (`buildAuditPublishers` → `auditgovernance.NewOAuthTokenSource` → `newTenantTokenSource` →
   `secureEndpoint`, oauth.go:75-76), so the process already aborts startup (exit 1, never serves) — there
   is no lazy first-publish path and no "boots but broken" window. The gate only moves this *same* failing
   set earlier (to `Load()`, before DB connect); it does not newly reject any working config.
2. **Zero gate-matrix tests** (E3): enabled+empty bindings, enabled+missing endpoint, disabled+empty
   bindings, and the TokenURL scheme/loopback rules have no test pins; the existing commercial tests cover
   only credentials/scope/tenant-set. The matrix cells that do work today (E1 lines 58-60, early-return on
   `!Enabled`) are therefore unpinned behavior.
3. **`validateAuditBindingCoverage` has no locking/race test** (E6): only one sequential integration test.
   The boot gate decides over durable state (accounts + pending/delivering/dead_letter publications) while
   provisioning can insert rows concurrently; nothing pins the snapshot semantics ("boot either fails or the
   row is covered; no partial boot").
4. **No first-event e2e** (E8): nothing drives the first `Record`-produced fact through
   `drainPublications` to `completed` with a succeeding publisher, proving delivery happens only via the
   drain worker and not an inline publisher loop.

## 3. Requirements

### REQ-1 — Binding `TokenURL` is validated at config load with connector-parity rules

1. `validateAuditBindings` (`internal/config/commercial.go:54`) must validate every binding's `TokenURL`
   for every enabled configuration, before/independent of credential resolution, using exactly the rules
   the connector applies at construction (`newTenantTokenSource`, oauth.go:75-76; `secureEndpoint`,
   types.go:89-98, `allowedLoopbackHTTP` types.go:100-108): after `strings.TrimSpace`, the value must parse
   as a URL with a non-empty host, no userinfo, no fragment, no raw query; the scheme must be `https`, or
   `http` only when `value.AllowInsecureLoopback` is true and the hostname is `localhost` or a loopback IP
   (`net.ParseIP(...).IsLoopback()`).
2. **Parity, not approximation:** for any `TokenURL` value, the config gate must reject exactly the set of
   values `auditgovernance.NewOAuthTokenSource` rejects on the TokenURL dimension. A configuration that
   passes `validate()` must never fail later at connector construction (boot-wiring) because of a token
   URL (T-10.1.6 pins this cross-layer).
3. Error text must be stable, non-disclosing, and consistent with the existing style
   (`TestCommercialValidationErrorsDoNotDiscloseBindingMaterial`): a new message
   `audit governance binding contains an invalid token URL` that never echoes the raw URL value. The
   existing messages are preserved verbatim: `audit governance requires an endpoint and tenant bindings`
   (endpoint/bindings cells) and `audit governance bindings do not cover durable tenant state`
   (`internal/app/audit.go:149`).
4. The `!value.Enabled` early return (`commercial.go:55-57`) is preserved: a disabled audit governance
   block with empty bindings and empty/invalid token URLs must pass. No behavior change for billing
   bindings (see §5).

### REQ-2 — Gate-matrix tests pin every cell of the activation gate (T-10.1)

Config unit tests (package `config`, no DB) must pin, at a minimum:

| Cell | Input | Expected |
|---|---|---|
| enabled + empty bindings | `Enabled: true`, `Endpoint` set, `Bindings: {}` | `validate()`/`validateCommercialIntegrations` error containing `audit governance requires an endpoint and tenant bindings` |
| enabled + missing endpoint | `Enabled: true`, `Endpoint: ""` (and whitespace-only), bindings non-empty and otherwise valid | same error |
| disabled + empty bindings | `Enabled: false`, `Endpoint: ""`, `Bindings: {}` | `nil` |
| disabled + invalid TokenURL | `Enabled: false` + otherwise valid config, any `token_url` | `nil` (gate inactive) |
| enabled + TokenURL matrix | valid config, `token_url` varied per §REQ-1.1 | accept/reject exactly as the connector does (T-10.1.3 table) |
| enabled + valid TokenURL | `https://sso.example/token`, any `AllowInsecureLoopback` | `nil` (no regression) |

Existing scope/resource/credential-distinctness/tenant-set/non-disclosure/control-character cases must stay
green (baseline measured in §1).

### REQ-3 — `validateAuditBindingCoverage` race contract: the boot either fails or the row is covered (T-10.2)

1. **Snapshot semantics:** the coverage check is a single-statement `UNION` (`internal/app/audit.go:136-149`)
   and must remain one statement: the gate's decision covers exactly the durable state visible at its
   snapshot. It must never observe a partial write (a durable account without its fact, or a pending
   publication without its account — in-tx audit commits both sides atomically), and it must never fail on
   rows that are not yet durable.
2. **Read-only:** the gate must never claim, settle, delete, or mutate `audit_publications` rows; a passing
   boot leaves concurrently arriving unbound facts durably `pending` (recoverable via the existing
   `errAuditTenantUnbound` → `pending`/`audit.binding_unavailable` drain outcome,
   `internal/service/callback_audit.go:94-95`, unit-pinned by
   `TestPublicationOutcomeKeepsUnboundTenantRecoverable`).
3. **Deterministic tests** (integration tag, package `app`): the advisory-lock handshake pattern proven by
   `TestAppendConcurrentDuplicateAcrossPartitions` + `waitForLockPoll` must pin (a) an uncommitted
   unbound row is invisible to the gate (gate passes; the write is not durable, so this is not a partial
   boot), and (b) once that row commits, the gate fails — a committed unbound row is never accepted
   (T-10.2a). A bounded stress loop over concurrent provisioning must pin the tail assertion (after all
   writers join, the gate fails), no-fact-loss, and read-only (T-10.2b).

### REQ-4 — First-event e2e: delivery exclusively via `drainPublications`, no inline publisher loop (T-10.2)

1. `Record` of the first audit fact appends exactly one `audit_ledger` row and one `pending`
   `audit_publications` row and performs **no** governance delivery and **no** callback enqueue
   (keep-enforced: existing `TestAuditLedgerRecordEnqueuesOnlyIntegration` stays green **unchanged** —
   pinned by T-10.2d; the publisher-call count and callback-delivery count legs are asserted by T-10.2c
   in the service package).
2. The first fact is delivered exactly once **only** when the drain worker runs
   (`callbackService.drainPublications`): one claim → one `completed` settlement, exactly one
   `AuditPublisher.Publish` invocation with the matching fact, exactly one `callback_deliveries` row
   (dedup receipt), and a second drain tick is a no-op (at-least-once transport with unique receipts;
   exactly-once delivery).
3. Static keep-enforced pins (already green; must stay green in the done-gate): `publisher.Publish(ctx,
   fact)` reachable only from `publishAuditFact` (`callback_audit.go:114`); `drainPublications(` callable
   only from the `Start` ticker (`callback.go:281`) outside tests; `make check-audit-swallow` green.

## 4. Testable acceptance mapping

| ID | Requirement | Test pin | Executable assertions |
|---|---|---|---|
| T-10.1.1 | REQ-2 | `internal/config/commercial_test.go` (extend) or new sibling `commercial_gate_test.go` | Enabled+empty `Bindings` and enabled+`Endpoint ""`/whitespace → `validateCommercialIntegrations` error containing `audit governance requires an endpoint and tenant bindings`; disabled+empty bindings and disabled+bad TokenURL → `nil`. Build fixtures by mutating `commercialTestConfig()` (E3); keep `setCommercialSecrets` for credential legs |
| T-10.1.2 | REQ-1 | same file | Enabled + valid config where one binding's `TokenURL` is `""` or whitespace → error containing `audit governance binding contains an invalid token URL`, `err` non-nil from `validateCommercialIntegrations`; asserted with and without `setCommercialSecrets` (the no-secrets leg disables billing — `validateCommercialIntegrations` runs `validateBillingBindings` first, `commercial.go:21-23` — so billing's credential error cannot mask the URL error); the error text never contains the raw URL value (non-disclosure) |
| T-10.1.3 | REQ-1 | same file | TokenURL scheme/loopback table, `AllowInsecureLoopback` both values: `http://sso.example/token` (reject both), `http://localhost/token` (accept iff loopback allowed), `http://127.0.0.1/token` and `http://[::1]/token` (accept iff loopback allowed), `https://sso.example/token` (accept both), `https://sso.example/token?` (accept both — `ForceQuery=true`, `RawQuery==""`; parity pin so a future "cleanup" cannot break it asymmetrically), `not-a-url` / `ftp://…` (reject), `https://u:p@sso.example/token` (reject), `https://sso.example/token#frag` (reject), `https://sso.example/token?x=1` (reject), `"  https://sso.example/token  "` (accept — trimmed) |
| T-10.1.4 | REQ-1/REQ-2 | `internal/config/config_test.go` (one Load-level case + two env-merged cases) | Full YAML with `audit_governance.enabled: true`, endpoint set, one binding with valid tenant/scope/resources (`tenant-one` / `audit:event:write` / `[audit-governance]`) so the combined tenant/scope/resource error cannot fire first, `token_url: http://sso.example/token`, billing left disabled (default), secret env optional (URL error precedes credential resolution) → `Load(path)` returns error containing `audit governance binding contains an invalid token URL` — failure at config load, not at boot-wiring. Two env-merged `AERO_ID_*` cases (env wiring `config.go:374-380`; viper env reaches scalar keys only — `bindings` is a map and not env-addressable): `AERO_ID_AUDIT_GOVERNANCE_ENABLED=true` on a file-disabled bad config → Load fails with the URL error (env cannot bypass the gate); `AERO_ID_AUDIT_GOVERNANCE_ALLOW_INSECURE_LOOPBACK=true` on a file-enabled `http://localhost/token` config → Load succeeds (env flag lands in the same merged struct the gate and the connector read) |
| T-10.1.5 | REQ-2 | `internal/config/commercial_test.go` | Existing tests stay green unchanged: `TestCommercialCredentialsAreDistinctAcrossDestinations`, `TestCommercialBindingsRequireExactScopesResourcesAndTenantSets`, `TestCommercialValidationErrorsDoNotDiscloseBindingMaterial`, `TestCommercialCredentialsRejectEmbeddedControlCharacters` |
| T-10.1.6 | REQ-1.2 | `internal/config/commercial_test.go` (imports `github.com/aero/aero-id/internal/connector/auditgovernance`; no cycle — the connector does not import `config`) | Parity pin: for each TokenURL case in T-10.1.3, assert accept/reject agreement between `validateCommercialIntegrations` (secret env set, ≥32-char values) and `auditgovernance.NewOAuthTokenSource` with a full valid `OAuthConfig` (canonical scope `audit:event:write`, resources `[audit-governance]`, ≥32-char secret, `AllowInsecureLoopback` matched) whose only variant is `TokenURL`. No config that passes validate() may fail connector construction on the TokenURL dimension |
| T-10.2a | REQ-3.1/3.3 | `internal/app/audit_integration_test.go` (extend; copy `waitForLockPoll` helper, precedent `internal/repository/audit_consolidation_integration_test.go:18-27`) | Deterministic handshake (coordinator/writer split — a writer-side `pg_advisory_lock` with no holder would return immediately and the poll would time out): (1) a **coordinator session conn** executes `SELECT pg_advisory_lock(hashtext('coverage-gate-writer'))` *before* the writer spawns (precedent `:62`); (2) a **writer goroutine** on its own conn `BEGIN` → direct INSERT `audit_ledger` row (tenant `race-unbound`, column list per precedent `:85-99`) + `pending` `audit_publications` row → `SELECT pg_advisory_xact_lock(hashtext('coverage-gate-writer'))` — transaction-scoped, so it cannot leak past COMMIT and it *blocks* until the coordinator releases; (3) `waitForLockPoll(t, db, "advisory", 10s)` — fail-closed (a writer that never blocks times out and `Fatalf`s); (4) **while the writer is blocked**: gate call with `bindings: {}` → `nil`, and a plain SELECT from a second connection confirms the row is invisible (MVCC); (5) coordinator `SELECT pg_advisory_unlock(hashtext('coverage-gate-writer'))` (precedent `:97`) → writer's xact lock acquires → writer `COMMIT`, close conn; (6) gate call → error `audit governance bindings do not cover durable tenant state`; (7) covered leg: same handshake with `bindings: {"race-unbound": valid binding config}` → gate call after commit → `nil`. This pins: committed unbound rows always fail the boot; in-flight uncommitted writes are outside the boot decision (not a partial boot); covered rows never fail |
| T-10.2b | REQ-3.2/3.3 | same file | Stress loop: 8 writer goroutines (4× `ledger.Record` for tenants `race-N`, 4× `accounts.EnsureByIdentity("race-N", …)` — real provisioning paths, atomic account/fact writes — with **distinct per-writer `(issuer, subject)`** (e.g. issuer `race-issuer`, subject `race-N`) so writers never contend on the `(issuer, subject)` unique index; signature `(ctx, tenantID, issuer, subject, …)` at `account_identity.go:20`), 4 gate goroutines × 20 calls with `bindings: {}`; join all; assertions: (1) every fact exists exactly once in `audit_ledger` (no loss); (2) every `audit_publications` row is `status='pending'`, `attempt=0`, `lease_owner=''` (gate is read-only — never claims/settles/mutates); (3) tail gate call after all writers join → error (deterministic; no flaky middle-state assertions); (4) rerun with bindings covering all `race-N` → every gate call `nil` (no spurious failure, "row is covered" leg) |
| T-10.2c | REQ-4.1/4.2 | `internal/service/callback_integration_test.go` (sibling of `TestDrainExhaustsAttemptsIntoDeadLetter:193`); new succeeding `recordingAuditPublisher` fake (records `*domain.AuditFact`, returns nil) beside `failingAuditPublisher` | Setup as the drain precedent (real `callbackRepo` + wired `SetAuditPublicationRepository` + `recordingAuditPublisher` + `svc.metrics = metricSpy` per `callback_integration_test.go:210-211`); seed one enabled/verified subscription (tenant-one, action `account.provisioned`) **and record the first fact with the same `Action`** — `enqueueFact` materializes delivery rows only for matching `(tenant, action)` (`ListEnabledByAction`, `callback_audit.go:20-37`); `ledger.Record` first fact with known `EventID`; (1) immediately after `Record`: 1 ledger row, 1 `pending` publication row (`attempt=0`), `publisher.calls == 0`, 0 `callback_deliveries` rows (no inline publisher loop, no inline enqueue); (2) `svc.drainPublications(ctx)`: publication `completed`, `error_code ''`, publisher received exactly 1 fact with matching `EventID`/`TenantID`/`Action`, exactly 1 `callback_deliveries` row (dedup receipt), outcome metric `[completed]`; (3) second `drainPublications`: no claim, `publisher.calls` still 1, status still `completed` (exactly-once delivery; at-least-once transport) |
| T-10.2d | REQ-4.3 | done-gate (no new files) | `grep -rn "\.Publish(ctx, fact)" internal/service/` → only `callback_audit.go:114`; `grep -rn "drainPublications(" internal/ --include='*.go' | grep -v _test.go` → `callback_audit.go:38` (definition) + `callback.go:281` (ticker) only — the `_test.go` filter is required because three test call sites already exist today (`callback_integration_test.go:235,285`, `callback_audit_test.go:184`) and T-10.2c adds more; `make check-audit-swallow` green. `TestAuditLedgerRecordEnqueuesOnlyIntegration` stays green unchanged |

## 5. Scope boundaries (explicit non-goals)

- **Billing `TokenURL`**: `validateBillingBindings` has the same shape of gap (billing connector enforces
  `secureEndpoint` at `internal/connector/billing/oauth.go:45`), but this direction is scoped to audit
  bindings per its acceptance. Billing gets no new validation and no new tests here; a symmetric follow-up
  must reuse the same parity-pin pattern.
- **Governance `endpoint` scheme**: `auditgovernance.NewPublisher` also enforces `secureEndpoint` on
  `Config.Endpoint` (`publisher.go:34`) at wiring time. The direction's acceptance covers `binding.TokenURL`
  only; the endpoint cell stays as-is (the empty-endpoint case is already a config-load error, E1 `:58-60`).
- **`source_prefix`**: `validSourcePrefix` (types.go:71-83) is still enforced only at connector
  construction; out of scope, noted for a follow-up.
- **B2-3 (401 invalidation) and B2-5 (purge-pause/requeue)**: separate directions in the same analysis
  batch; nothing here adds config fields for them.
- **No behavior change** to `Record`/`drainPublications`/`publishAuditFact`; REQ-4's code path is already
  shipped — the requirement is test pins and keep-enforced greps.
- **No new migrations, no protobuf changes, no new packages** (test-only helpers allowed in the two test
  packages).

## 6. Engineering gates

- Unit leg: `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow
  && make check-anchored-event-ids && make build` — `internal/config/commercial.go` grows by ~29 lines
  (≤500-line rule holds: 161 → ~190; the requirements' earlier 142-line estimate was stale); new validation
  helper is one function ≤50 lines / cyclop ≤10 in `commercial.go` or `audit_governance.go`; `cmd/server/main.go`
  grows 45 → ~48 lines (clean-exit delta, B-1).
- Integration leg: `make test-integration` (Postgres via testcontainers) — required because T-10.2a/b touch
  repository SQL and T-10.2c drives the real publication repository (AGENTS.md integration gate).
- The parity pin (T-10.1.6) is the anti-drift guard: if the connector's URL rules ever change, the config
  gate must change with them or the pin fails.
