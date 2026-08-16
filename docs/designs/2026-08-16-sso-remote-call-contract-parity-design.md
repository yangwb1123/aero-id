# Design — SSO remote-call contract parity (HasPermission + GetUsers)

- **Date:** 2026-08-16
- **Module:** `internal/sso` (direction #1 of `/home/u1/aero-id/docs/auto/analyses/internal-sso-48b01596.json`; directions #2/#3 — UserCache graceful degradation, UserCache eviction — are separate workstreams)
- **Baseline:** git HEAD `ecccc49`; Go 1.26; module `github.com/aero/aero-id`
- **Status:** Design proposal — no repository code changed by this document

---

## 1. Verification verdict (evidence audit)

Every cited claim in the source requirements was re-checked against the current tree on 2026-08-16. **All 13 evidence rows verified; two cosmetic line-number drifts noted.** The requirements are a faithful description of the current code and may be treated as ground truth.

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | `HasPermission` (client.go:204-219) deadline-only | ✅ | `internal/sso/client.go:204` — timeout wrap + bare `c.authz.Check`; no `breaker.Allow`, `callWithRetry`, `recordCall`, or semaphore in the body |
| 2 | `ObserveSSOCall` covers only `validate`/`get_users` | ✅ | `recordCall` sites at client.go:289 (`"validate"`), :335 (`"get_users"`); doc comments client.go:30, `internal/pkg/metrics/metrics.go:53-54,135`; registry maps op-keyed with no allow-list |
| 3 | `breaker.Allow`/`Record` only on validate path | ✅ | client.go:270 (`Allow`), :299/:306/:310 (`Record`) — all inside `validateUpstream` |
| 4 | `fetchUsers` retry+metrics, no breaker | ✅ | client.go:324-348 — `callWithRetry` + `recordCall("get_users")`, zero breaker interaction |
| 5 | `middleware.RequirePermission` call site | ✅ | `internal/middleware/permission.go:29`; wired at `internal/app/services.go:117` via `NewDelegatingPermissionChecker(deps.ssoClient, adminSvc, ...)`; consumers include HTTP admin/account/replay/event_ingest/callback/aggregation/audit_ledger routes and gRPC `internal/handler/grpc/server.go:348`; error path → `apperr.Internal("auth.permission_check_failed")` (500) |
| 6 | `DelegatingPermissionChecker`, no cache | ✅ | `internal/service/permission.go` — pure delegation with local fallback; no permission cache type anywhere in `internal/{service,middleware,sso}` (grep) |
| 7 | Metrics fakes in metrics_test.go | ✅ | `recordingSink` / `newRecordingSink` / `snapshot` / `startMeteredSSO`; tests at :98, :130, :157 |
| 8 | Breaker-on-validate test | ✅ | `TestClient_BreakerOpensAndStopsCallingSSO` at client_test.go:406 |
| 9 | No concurrency cap | ✅ | no semaphore/`MaxConcurrent` anywhere in `internal/sso` or `SSOConfig`; the sole `MaxConcurrentStreams` (config.go:216) is an unrelated gRPC dial knob |
| 10 | Config baseline | ✅ | config.yaml sso block: `timeout: 5s`, `breaker_threshold: 5`, `breaker_cooldown: 10s`, `max_attempts: 3`, `retry_backoff: 50ms`; `internal/app/new.go:97-115` wires all into `ClientConfig` incl. `Metrics: metricsReg` |
| 11 | Authorizer RPC surface | ✅ | `authzpb.AuthorizerClient.Check` (`CheckRequest{SubjectId, ClientId, Permission}` → `CheckResponse{Allowed}`); `authzpb.RegisterAuthorizerServer` available (authz_grpc.pb.go) — bufconn fakes trivially registerable |
| 12 | `errCircuitOpen` not availability-classified | ✅ | breaker.go:16 `errors.New(...)`; `isAvailabilityError` (breaker.go:126-140) checks `errors.Is(DeadlineExceeded)` + `status.Code` in {Unavailable, DeadlineExceeded}; plain error → `Unknown` → false |
| 13 | `BREAKER_EXEMPT_PATTERN` line-agnostic | ✅ | `scripts/check-audit-swallow.sh:160` — statement-anchored `^([^:]*:)?[0-9]*:[[:space:]]*c\.breaker\.Record\(`; doc comments hardcode `client.go:299,306,310` (script lines ~67-71, ~110-115, ~145-160) and will drift |

**Additional verified facts that harden the design:**

- **grpc version and error unwrapping (D3 viability):** `go.mod` pins `google.golang.org/grpc v1.80.0`; its `status.FromError`/`Code` use `errors.As` to unwrap wrapped status errors (verified in module cache). Therefore `fmt.Errorf("…: %w", status.Error(codes.Unavailable, …))` chains are correctly classified by `isAvailabilityError` → D3 is sound without touching `isAvailabilityError`.
- **Retry/breaker single-outcome contract:** `callWithRetry` (client.go) returns the *last* outcome after the bounded attempt sequence, so "exactly one `Record` per logical call" falls out naturally with one `Record` after the retry loop (the `validateUpstream` pattern).
- **Gate thresholds:** funlen 50 lines / cyclop 10 complexity (`.golangci.yml`); `internal/sso/client.go` is currently 348 lines → the planned ~70 lines of additions keep it ≤ 500 (`check-file-sizes`).
- **Validation precedent:** `validSourceLimits` (config/validate.go:448-449) already enforces `MaxConcurrency >= 0` for sources — the same shape applies to `sso.max_concurrent`; config tests use the same negative-value pattern (`validate_test.go:181`).
- **No test asserts exact SSO error strings** (grep across `internal/` for `sso validate:` / `sso get users:` / `snaplink authorization check:` / `circuit breaker open` found no assertions) → wrapper-string preservation is safe.

**Cosmetic drifts (no design impact):**

- Claim 6 cites `service/permission.go:21-36`; actual struct declaration is line 11, methods 21-37.
- Claim "breaker doc comment at breaker.go:9-11" — the "guarding the SSO ValidateToken RPC" text is at breaker.go:37 (lines 9-11 are the errCircuitOpen doc block); the text exists and still needs updating per D1.

---

## 2. Problem statement (confirmed)

AGENTS.md's remote-call contract mandates every remote call carry a deadline, bounded message size, concurrency limit, circuit breaker, and metrics, plus per-source latency/error/circuit-state metrics. The SSO client violates this on two of its three upstream surfaces:

1. **`HasPermission` (client.go:204-219)** — deadline only. Runs on every admin/account/replay/callback/audit/event route via `middleware.RequirePermission`. During a Snaplink outage each such request is a raw upstream gRPC round-trip waiting the full configured timeout (5s) with no breaker, no retry, no `authorize` metric, and no concurrency bound. With `authz_legacy_fallback: true` the `DelegatingPermissionChecker` eventually serves the local fallback — but only after the full-timeout wait per request, and only when configured.
2. **`fetchUsers` (client.go:324-348)** — retry + `get_users` metric, but bypasses the breaker. During the same outage `GetUsers` burns `timeout × maxAttempts` per request while `ValidateToken` fast-fails — inconsistent fast-fail semantics and zero authz circuit-state observability.

There is no permission cache (the analysis only notes absence; caching is a separate decision) and no concurrency cap on any upstream surface.

---

## 3. Goals and non-goals

### Goals

- **G1 — Uniform fast-fail.** All three upstream surfaces (`validate`, `get_users`, `authorize`) fail fast on Snaplink outage via one shared circuit breaker, with the circuit-open error availability-classified everywhere.
- **G2 — Metrics parity.** `authorize` joins `validate`/`get_users` as an `ObserveSSOCall` op reaching `/metrics` (`aeroid_sso_errors_total`, `aeroid_sso_latency_seconds`).
- **G3 — Retry parity.** `HasPermission` retries availability errors exactly like the other surfaces (deadline-bounded, one breaker outcome per logical call).
- **G4 — Concurrency cap.** One shared, configurable semaphore bounds in-flight upstream RPCs across all three surfaces.
- **G5 — Behavioral compatibility.** Dev stacks with `breaker_threshold: 0` / `max_concurrent: 0` are byte-for-byte unchanged; error-message wrappers preserved; all existing tests green.

### Non-goals (explicitly out of scope)

- **No permission cache.** Caching authz is a separate decision with its own staleness/revocation policy.
- **No changes** to `UserCache` stale/partial semantics, `TokenCache`, `DelegatingPermissionChecker`, middleware, or HTTP/gRPC handlers (interface shapes unchanged).
- **No message-size bounding** of `GetUsers` batches (separate concern).
- **No singleflight on authz** (the concurrency cap bounds the stampede).
- **No changes** to `jwks.go`, dial options, keepalive, or the audit ledger.
- Directions #2/#3 (UserCache graceful degradation, eviction) of the analysis file.

---

## 4. Design decisions (binding)

### D1 — One shared breaker

The existing `c.breaker` (triggered by consecutive availability failures, with a recover-probe cooldown) guards all three SSO upstream RPCs. A Snaplink outage is a single dependency failure; one breaker yields the uniform fast-fail semantics the direction targets and requires no new config. Update two doc comments to say "SSO upstream RPCs (`validate`, `get_users`, `authorize`)":

- `internal/sso/breaker.go:37` — "guarding the SSO ValidateToken RPC" → "guarding the SSO upstream RPCs (`validate`, `get_users`, `authorize`)".
- `internal/sso/client.go` — the `breaker` field comment "trips on sustained SSO outage so new tokens fast-fail" → "...so upstream RPCs fast-fail".

### D2 — Breaker outcome semantics (parity with validateUpstream)

Per logical call, the caller records exactly **one** breaker outcome *after* `callWithRetry` returns its last outcome:

- RPC success → `Record(true)`.
- Availability failure (retries exhausted, or slot-wait deadline) → `Record(false)`.
- Non-availability RPC error (e.g. `PermissionDenied`) → `Record(true)` (proves SSO is alive).

An `Allowed=false` response is a *success* for the breaker (the RPC completed) and is returned to the caller — never treated as an error and never fed `Record(false)`.

### D3 — Availability-classified fast-fail

Change `errCircuitOpen` (breaker.go:16) from `errors.New(...)` to:

```go
var errCircuitOpen = status.Error(codes.Unavailable, "sso circuit breaker open")
```

Update its doc comment: it is now an availability-classified fast-fail sentinel detected by `isAvailabilityError` via `errors.As` unwrapping in `status.Code` (verified for grpc v1.80.0). Preserve existing wrapper shapes:

- validate: `fmt.Errorf("sso validate: %w", errCircuitOpen)` (unchanged site, client.go:275)
- get_users: `fmt.Errorf("sso get users: %w", errCircuitOpen)`
- authorize: `fmt.Errorf("snaplink authorization check: %w", errCircuitOpen)`

No change to `isAvailabilityError` itself (it already classifies a wrapped `Unavailable` status; verified).

### D4 — Structure mirrors validateUpstream

Extract the upstream work into a private `authorizeUpstream(uid, permission string) (bool, error)` behind a thin public `HasPermission` that keeps the `c.authz == nil` guard first. `HasPermission` and `authorizeUpstream` each stay within funlen 50 / cyclop 10.

### D5 — Shared semaphore

- `ClientConfig.MaxConcurrent int` — `<= 0` = no cap (preserves dev-stack behavior exactly as `BreakerThreshold <= 0` disables the breaker).
- `NewClientFromConn` builds `c.sem = make(chan struct{}, maxConcurrent)` when `> 0`.
- A nil-safe helper:

```go
// acquireSlot reserves an upstream RPC slot, bounded by the request context.
// A slot-wait that exceeds the deadline surfaces as context.DeadlineExceeded,
// which isAvailabilityError classifies as availability (consistent with D3).
func (c *Client) acquireSlot(ctx context.Context) (func(), error) {
	if c.sem == nil {
		return func() {}, nil
	}
	select {
	case c.sem <- struct{}{}:
		return func() { <-c.sem }, nil
	case <-ctx.Done():
		return func() {}, ctx.Err()
	}
}
```

Call sites: `slot, err := c.acquireSlot(ctx)`; on error return immediately (availability path); `defer slot()` after acquisition; acquire before the breaker check? **No** — acquire *after* the breaker `Allow()` gate (an open breaker must fast-fail without consuming a slot) but *before* the RPC. Order: breaker gate → slot → tracer span → `callWithRetry`. No new dependency (buffered channel; `golang.org/x/sync` already present for singleflight).

### D6 — Config wiring end-to-end

- `internal/config/config.go`: `SSOConfig.MaxConcurrent int` with `mapstructure:"max_concurrent"` + comment `// max concurrent in-flight SSO upstream RPCs across validate/get_users/authorize (0 = no cap)`.
- `configs/config.yaml` sso block: `max_concurrent: 64` (documented `0 = no cap`).
- `internal/app/new.go`: pass `MaxConcurrent: cfg.SSO.MaxConcurrent` into the `ClientConfig` literal (the block at new.go:100-114).
- `internal/config/validate.go`: reject `c.SSO.MaxConcurrent < 0` (mirrors `validSourceLimits`' `MaxConcurrency >= 0` at validate.go:448-449 and the existing `validate_test.go` negative-value test pattern).
- The shipped non-zero default makes the cap live in production; `0 = disabled` is the documented escape hatch.

### D7 — Metrics contract

- `authorizeUpstream` emits `c.recordCall("authorize", start, err)` around the `Check` RPC with `ok = (err == nil)` — a grant or a denial records `ok=true`; any RPC error records `ok=false`.
- Doc-comment updates (code doc only; registry is op-keyed with no allow-list, so no registry code change):
  - `MetricsSink.ObserveSSOCall` (client.go:30): `"validate" or "get_users"` → `"validate", "get_users", or "authorize"`.
  - `internal/pkg/metrics/metrics.go:53-54` (`ssoErrors`/`ssoLatency` op comments) and `:135` (`ObserveSSOCall` method comment).

---

## 5. API changes (complete surface)

### `internal/sso` (public API additions are config-only; no behavior-breaking changes)

| Symbol | Change | Kind |
|---|---|---|
| `ClientConfig.MaxConcurrent int` | new field; `<= 0` = no cap | additive |
| `Client.sem chan struct{}` | new private field; nil when cap disabled | private |
| `Client.acquireSlot(ctx) (func(), error)` | new private helper (D5) | private |
| `Client.authorizeUpstream(uid, permission) (bool, error)` | new private method (D4); breaker gate (R1), `callWithRetry` (R5), `recordCall("authorize", …)` (D7), one `Record` (D3/D2) | private |
| `Client.HasPermission` | body shrinks to: `authz == nil` guard → `authorizeUpstream`; behavior of successful/delegated paths unchanged | signature stable |
| `Client.fetchUsers` | adds breaker gate before RPC + one `Record`; error wrapper `sso get users: %w|errCircuitOpen` | signature stable |
| `Client.validateUpstream` | gains slot acquisition (D5); fast-fail error now availability-classified via D3 | signature stable |
| `errCircuitOpen` (breaker.go:16) | `errors.New` → `status.Error(codes.Unavailable, …)`; doc comment updated | private sentinel; classification change is the point |
| breaker/client doc comments | D1/D3 wording | doc only |

### `internal/config` / `configs` / `internal/app`

| Symbol | Change |
|---|---|
| `SSOConfig.MaxConcurrent int` (`mapstructure:"max_concurrent"`) | new; default zero (no cap) when absent in config files |
| `configs/config.yaml` sso block | adds `max_concurrent: 64` (# `0 = no cap`) |
| `internal/config/validate.go` | SSO validation rejects negative `MaxConcurrent` |
| `internal/app/new.go` | wires `MaxConcurrent` into `ClientConfig` |

### `internal/pkg/metrics` / `scripts`

| File | Change |
|---|---|
| `internal/pkg/metrics/metrics.go` | doc comments only (53-54, 135) |
| `scripts/check-audit-swallow.sh` | refresh hardcoded line references (`client.go:299,306,310`) in the three doc blocks (lines ~67-71, ~110-115, ~145-160) to the post-change line numbers; `BREAKER_EXEMPT_PATTERN` unchanged (statement-anchored + line-agnostic — new statement-start `c.breaker.Record(` sites are already exempt) |

**No gRPC/HTTP handler, middleware, service, proto, or migration changes.** No new dependencies.

---

## 6. Compatibility constraints

1. **Dev-stack inertness.** With `BreakerThreshold <= 0`: `Allow()` always true, `Record` no-op — all new breaker code inert. With `MaxConcurrent <= 0`: `c.sem == nil` and `acquireSlot` is a no-op. Existing tests that omit breaker or concurrency config MUST stay byte-for-byte green.
2. **Error-string preservation.** `sso validate:`, `sso get users:`, `snaplink authorization check:` wrappers preserved — no test asserts exact strings today (verified), but external log scraping may depend on them.
3. **Outage-path semantics unchanged, only faster.** Breaker open → `HasPermission` returns error → middleware maps to `500 auth.permission_check_failed`; `DelegatingPermissionChecker` may serve the legacy local fallback when `authz_legacy_fallback: true` — identical to today's outage path minus the full-timeout wait.
4. **`Allowed=false` stays a successful breaker outcome** — a denial proves SSO is alive and must not trip the breaker.
5. **`UserCache.GetMany` fetcher-error propagation unchanged** — the `return nil, err` on fetcher failure is preserved with the now-classified error.
6. **Nil metrics sink stays safe** — `recordCall` already nil-guards; the new `"authorize"` site uses the same helper.
7. **Config default semantics.** `0` means code default / disabled unless the key explicitly documents `0 = disabled` — `max_concurrent: 0` is documented as "no cap"; config files without the key get zero (no cap) on upgrade until `configs/config.yaml`/operator config adds it.
8. **funlen/cyclop/file-size gates** — `HasPermission` + `authorizeUpstream` each ≤ 50 lines / cyclop 10; client.go stays ≤ 500 lines (348 today; budget ≈ 70–80 lines).

---

## 7. Failure modes and mitigations

| # | Failure mode | Behavior | Mitigation / contract |
|---|---|---|---|
| F1 | Snaplink outage, breaker closed, `authorize` calls | Each `Check` retries up to `maxAttempts` availability-only, deadline-bounded; failures accumulate in breaker | Retry budget + breaker trip after `breaker_threshold` consecutive failures — uniform with validate |
| F2 | Breaker open on `authorize`/`get_users` | Fast-fail with classified `Unavailable` error, zero upstream RPCs, zero slot consumption | Breaker gate precedes slot acquisition; `HasPermission` → 500 `auth.permission_check_failed` → legacy fallback if enabled (same as today, faster) |
| F3 | Breaker open on `validate` with stale identity available | Serves stale identity (existing behavior) | Unchanged; classified error only when no stale identity |
| F4 | Concurrency saturation (all slots held) | New callers wait on `acquireSlot` bounded by request deadline; on deadline → `context.DeadlineExceeded` (availability-classified) | Cap prevents unbounded fan-out of a stampede; block, don't overflow; high-concurrency bulkhead rule satisfied |
| F5 | Half-open probe fails | Breaker re-opens; failures counter continues | Existing `breaker.Record(false)` in `breakerHalfOpen` re-opens — same for all three surfaces |
| F6 | Non-availability RPC error (e.g. `PermissionDenied`) on `authorize` | Returns immediately, no retry, `Record(true)`, `Allowed=false` semantics intact | SSO alive ⇒ breaker closed; denial not treated as error |
| F7 | `Allowed=false` during outage recovery | Returned to caller verbatim; no breaker trip; records `"authorize"` ok=true | D2; permission decision never fabricated |
| F8 | Slot-wait vs breaker interaction | Waiter that times out returns classified error; it does NOT trip the breaker (no RPC attempted) | Only a completed-but-failed RPC feeds `Record(false)` — slot-wait deadline is a caller-side outcome, consistent with `validateUpstream`'s detached-timeout philosophy |
| F9 | Misconfiguration: `max_concurrent: -1` in operator config | Startup validation rejects with a clear error before any traffic | `validate.go` `>= 0` check (D6) |
| F10 | Line-number drift in audit-swallow doc comments | Cosmetic stale references | Refresh script doc blocks in the same change; pattern itself unaffected |
| F11 | New `authorize` op appearing in metrics | Flows through op-keyed registry automatically | No registry change needed; `aeroid_sso_errors_total{op="authorize"}` / `aeroid_sso_latency_seconds{op="authorize"}` appear on `/metrics` |

---

## 8. Migration steps

This is a backward-compatible additive change; no data migration, no proto changes, no handler changes. Steps in dependency order:

1. **`internal/sso/breaker.go`** — reclassify `errCircuitOpen` (D3); update doc comments (D1).
2. **`internal/sso/client.go`** —
   a. add `MaxConcurrent` to `ClientConfig`; add `sem` field; build it in `NewClientFromConn`; add `acquireSlot` (D5);
   b. split `HasPermission` → guard + `authorizeUpstream` with breaker gate (R1), retry (R5), metric (D7), single `Record` (R3), slot (D5);
   c. gate `fetchUsers` + `Record` (R2/R3), slot (D5);
   d. slot in `validateUpstream` (D5);
   e. doc comment updates (D7).
3. **`internal/config/config.go`** — `SSOConfig.MaxConcurrent` field.
4. **`internal/config/validate.go`** — `>= 0` check.
5. **`configs/config.yaml`** — `max_concurrent: 64` with `0 = no cap` comment.
6. **`internal/app/new.go`** — wire `MaxConcurrent`.
7. **`internal/pkg/metrics/metrics.go`** — doc comments only.
8. **`scripts/check-audit-swallow.sh`** — refresh hardcoded line refs in doc comments (pattern untouched).
9. **Tests** — A1–A4 below.
10. **Gates** — A5 below.

Deployment note: since the new config key defaults to no-cap when absent, mixed-version fleets can roll out the new binary first and enable `max_concurrent` afterwards; or ship config + binary together — both are safe. There is no operational step that must precede the binary rollout.

---

## 9. Testable acceptance mapping

All new tests live in `internal/sso/client_test.go` / `internal/sso/metrics_test.go` (external `sso_test` package), using a bufconn `authzpb.RegisterAuthorizerServer` fake (`fakeAuthorizerServer` embedding `authzpb.UnimplementedAuthorizerServer`; counts `Check` calls; injectable error; optional in-flight peak gauge) alongside the existing `fakeSSOServer`/`toggleSSO` helpers.

| Check | Test name / location | Assertions |
|---|---|---|
| **A1** — N consecutive `Unavailable` from authz.Check trip the breaker; subsequent `HasPermission` with breaker open returns fast with zero additional Check calls | `TestClient_AuthorizeBreakerOpensAndStopsCallingSSO` (client_test.go, mirror of :406). Config: `BreakerThreshold=N`, `BreakerCooldown=1h`, `MaxAttempts=1`. Fake returns `status.Error(codes.Unavailable, …)` on every Check | `HasPermission` N times → error each time, `fake.checkCalls == N`; 5 more calls → non-nil classified error each, `fake.checkCalls` still `N` (breaker short-circuits). Unclassified-nowrap check: `status.Code(err) == codes.Unavailable` on the returned chain |
| **A2** — `fetchUsers` with open breaker makes zero `fakeSSOServer.GetUsers` calls and returns a classified availability error | `TestClient_GetUsersFastFailsWhenBreakerOpen` (client_test.go). Trip the breaker via validate (`toggleSSO` down) or via authz path; then public `GetUsers(ctx, []string{"u1"})` (the only reachable surface for private `fetchUsers` via `UserCache.GetMany`) | error satisfies `isAvailabilityError` (`status.Code(err) == codes.Unavailable`); `fake.getUsersCalls == 0` before/after |
| **A3** — MetricsSink fake records `"authorize"` ObserveSSOCall ok=false on authz error and ok=true on grant | extend `recordingSink` harness in metrics_test.go (`startMeteredSSO` gains authorizer fake; one mode `Allowed=true`, another `Unavailable`) | `callOK["authorize"] == 1` after a grant; `callErr["authorize"] == 1` after availability error; existing `validate`/`get_users` counts untouched (no cross-contamination) |
| **A4** — `MaxConcurrent=1` + blocking fake Check keeps server-side in-flight ≤ 1 | `TestClient_AuthorizeConcurrencyCap` (client_test.go). Fake blocks inside Check on a release channel, recording atomic in-flight peak. Config: `MaxConcurrent=1`, `MaxAttempts=1`. 2 concurrent `HasPermission` goroutines | in-flight peak == 1; after release both complete with expected results. (Optional equivalent `GetUsers` variant.) |
| **A5** — gates | `TestClient_ValidateToken*`, `TestClient_ServesStaleWhenSSODown`, `TestClient_NoStaleForUnknownTokenWhenDown`, `TestClient_Breaker*`, `TestClient_Retry*`, `TestClient_GetUsersDedupesAcrossCalls`, metrics tests, `internal/pkg/metrics` tests, config validation tests | all green unchanged (no `BreakerThreshold`/`MaxConcurrent` ⇒ inert code paths); `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-composition-root` pass; client.go ≤ 500 lines |

**Determinism note for A1/A4:** `MaxAttempts=1` ensures each logical call performs exactly one upstream attempt, so N calls == N recorded failures (trips at threshold N); A4's fake must block while holding the lock-ordered in-flight counter and coordinate via channels (no sleeps) to avoid flakes under `-race`/`-count=1`.

---

## 10. Regression risk register (must remain green)

- `TestClient_ValidateTokenCaches` / `...BadReturnsError` / `...TimesOut` / `...Singleflight` / `TestClient_ServesStaleWhenSSODown` / `TestClient_NoStaleForUnknownTokenWhenDown` / `TestClient_BreakerOpensAndStopsCallingSSO` / `TestClient_RetryRecoversWithinTimeout` / `TestClient_GetUsersDedupesAcrossCalls` — none configure `BreakerThreshold` (breaker inert) or `MaxConcurrent` (no cap) → behavior byte-for-byte unchanged. The one effect visible to them: `errCircuitOpen` reclassification only triggers on an open breaker, which these tests do not reach (or in the breaker test, only after tripping, where they assert error non-nil, not classification).
- `TestClient_MetricsTokenCacheHitMissAndErrors` / `TestClient_MetricsUserCacheAndCall` / `TestClient_NilMetricsSinkIsSafe` — validate/get_users paths emit no `"authorize"` op; nil-sink safety preserved via existing `recordCall` guard.
- `internal/pkg/metrics/metrics_test.go` — asserts only `validate`/`get_users` ops; unaffected.
- `internal/config/validate_test.go` — new `max_concurrent >= 0` check must accept 0 and 64 and reject negatives (new test mirrors the `negative max_attempts` case at :181).
- `check-audit-swallow` — new statement-start `c.breaker.Record(` sites exempted by the existing line-agnostic pattern; fixture set (`scripts/testdata/audit-swallow/breaker_exempt` etc.) untouched.
- `check-composition-root` — no type/wiring changes beyond one config field pass-through.
- Handler/middleware/service tests — no interface or behavior change on those layers.

---

## 11. Implementation checklist (traceability)

| # | Step | Requirements covered |
|---|---|---|
| 1 | `internal/sso/breaker.go`: reclassify `errCircuitOpen`; doc comments | R4, D1 |
| 2 | `internal/sso/client.go`: `MaxConcurrent` + `sem` + `acquireSlot`; `authorizeUpstream` (breaker gate, retry, metric, Record, slot); `fetchUsers` gate + Record + slot; `validateUpstream` slot; doc comments | R1-R3, R5-R8, D4-D5, D7 |
| 3 | `internal/config/config.go`: `SSOConfig.MaxConcurrent` | R9 |
| 4 | `internal/config/validate.go`: `>= 0` | R9 |
| 5 | `configs/config.yaml`: `max_concurrent: 64` | R9 |
| 6 | `internal/app/new.go`: wire field | R9 |
| 7 | `internal/pkg/metrics/metrics.go`: doc comments | R7 |
| 8 | `scripts/check-audit-swallow.sh`: refresh doc comment line refs | R11 |
| 9 | Tests A1-A4 | Acceptance |
| 10 | Gates A5 | R10-R11 |

---

## 12. Verification of evidence — summary

- **13/13 evidence rows confirmed** against the tree; the two cosmetic line-number drifts (service/permission.go, breaker doc-comment location) do not affect any design decision.
- **Critical design viability confirmed independently:** grpc v1.80.0 `status` unwraps wrapped errors via `errors.As` (D3 sound); `callWithRetry` returns last outcome (D2 sound); `validSourceLimits` supplies the validation precedent (D6); no test asserts exact error strings (wrapper preservation safe); client.go at 348 lines leaves ~150-line headroom under the 500-line gate.