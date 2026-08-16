# Design — Give RedisLimiter real test coverage and align distributed/local limiter semantics

- **Direction ID:** `internal-pkg-ratelimit-3fabcad4`
- **Module:** `internal/pkg/ratelimit`
- **Date:** 2026-08-16
- **Status:** Design (proposal — no repository changes made; see §Verification verdict)
- **Primary artifacts (proposed):**
  - `internal/pkg/ratelimit/redis_test.go` (new, mandatory, unit, miniredis-backed)
  - `internal/pkg/ratelimit/redis_integration_test.go` (new, optional, `//go:build integration`, testcontainers)
  - `internal/config/config.go` comment correction (optional path B, non-gating)
  - `go.mod` +1 test dependency (optional integration item only)

---

## 1. Verification verdict

Every claim in the input specification was re-checked against the tree at `/home/u1/aero-id` (2026-08-16). Verdict: **Verified with one minor citation correction** (content claims all hold; see below).

| Cited evidence | Verified location | Status |
|---|---|---|
| `internal/pkg/ratelimit/redis.go` (65 lines, untested) | `internal/pkg/ratelimit/redis.go` (65 lines) | ✅ Exact match. `redisRateScript` (Lua `INCR` + `PEXPIRE`-on-count==1), `RedisLimiter{client, rate, burst, window}`, `NewRedisLimiter` (nil / rate≤0 / burst≤0 → nil; `window = max(1s, burst/rate)` floored), `Allow` (nil-receiver fail-open; `count ≤ burst` allow; `PTTL` retry hint; `<1ms` fallback to full window; two error mappings `"redis rate limit increment"` / `"redis rate limit ttl"`), `key()` = `aero-id:rate:%x` hex sha256, `var _ DistributedLimiter = (*RedisLimiter)(nil)` |
| `internal/pkg/ratelimit/ratelimit_test.go` (8 tests, none touch RedisLimiter) | same file | ✅ Content claim exact: 8 tests, all on local `Limiter`. ⚠️ **Line-count correction:** the file is **107 lines, not 184** (evidence's own line-count error; test names and behavior verified one-by-one). |
| `internal/idempotency/store_redis_test.go:167-186` (miniredis + rueidis helper) | `func newMiniRedis` at line 167 (body through 186) | ✅ Exact. `miniredis.Run()` + `rueidis.NewClient(ClientOption{InitAddress: {srv.Addr()}, DisableCache: true, AlwaysRESP2: true})`. Evidence's own correction confirmed: helper drives SET/GET/DEL; EVAL/PTTL are supported by the same miniredis and exercised by the new tests. |
| miniredis v2.38.0 capabilities | module cache `@v2.38.0`: `cmd_scripting.go:20` registers `EVAL` (via `lua.go`), `cmd_generic.go:45-48` registers `PERSIST`/`PEXPIRE`/`PTTL`, `miniredis.go:365` `FastForward`, `direct.go:444` `Exists`, `direct.go:39` `Keys` | ✅ All present. |
| `go.mod` rueidis v1.0.76 / miniredis v2.38.0 / testcontainers v0.32.0 + modules/minio,nats,postgres, no modules/redis | go.mod:6,17,19-22 | ✅ Exact. |
| `internal/app/ratelimit.go` wiring | `buildDistributedRateLimiter` → `NewRedisLimiter(client, RPS, Burst)`, nil when disabled/client nil, returns `DistributedLimiter` | ✅ Exact. |
| `internal/middleware/ratelimit.go:120-141` | `allowRateLimit(ctx, key, local, distributed, sensitive)` at 120–141; `rateLimitSensitivePrefixes` at 33–44; retry consumed for `Retry-After` with `retry <= 0 → l.RetryAfter()` fallback at ~line 83 | ✅ Exact. Confirms the policy dependency: a `0` retry hint on a deny silently degrades `Retry-After` to the per-instance hint (or `"1"`), and a swallowed error flips sensitive-path fail-closed to fail-open. |
| gRPC interceptor | `internal/handler/grpc/server.go` `RateLimitInterceptor` / `allowGRPCRateLimit` (~lines 295–330): consumes the same triple; on error falls back to local bucket (no sensitive/closed path on gRPC) | ✅ Exact. |
| `config.go:37` "same budget enforced across instances" | `internal/config/config.go:37-38` (comment on `RateLimitConfig`); `internal/pkg/ratelimit/config.go` does **not** exist | ✅ Evidence's own correction confirmed. Overstatement present verbatim. |
| Integration-test conventions | `//go:build integration` present in `internal/app/audit_integration_test.go`, `internal/app/composition_root_integration_test.go`, `internal/pkg/db/db_test.go`, `internal/storage/s3_test.go`; `make test-integration` and `make check-integration-compile` targets exist; `make check-file-sizes` excludes `*_test.go` (Makefile find: `! -name '*_test.go'`) | ✅ Exact. |
| Zero existing RedisLimiter test coverage | `grep -rn RedisLimiter --include=*.go` → only `internal/app/ratelimit.go:15` (wiring) and the definition | ✅ Confirmed: no test references `RedisLimiter`. |

**Discrepancy log (evidence → reality):**
1. `ratelimit_test.go` is 107 lines, not 184. Content claims unaffected.
2. Everything else in the spec verified as stated; the spec's own two corrections (config-comment location; helper uses SET/GET/DEL) are themselves confirmed correct.

**Baseline semantics confirmed (unchanged by this design):**
- `RedisLimiter` is a **request-anchored fixed-window counter**: full `burst` admitted per window, nothing between; two adjacent windows can each admit `burst` → up to **2×burst** across a boundary.
- Local `Limiter` is a smooth refilling token bucket (`rate`/s, capacity `burst`).
- On Redis outage, middleware/gRPC fall back to the local bucket → the effective limit changes **shape** (fixed window → token bucket) and **scope** (global → per-instance), contradicting `internal/config/config.go:37-38`.
- This design **pins** that divergence with tests (R8); it does **not** mandate rewriting the Lua (see §4, optional path A).

---

## 2. Problem / gap

1. **Zero direct tests** for the only cross-instance enforcement path AGENTS.md permits for *global* limits ("process-local unless Redis-backed"). The Lua script, `window = max(1s, burst/rate)` math, PTTL fallback, both error mappings, sha256 key hashing, and nil-client behavior are unverified. One Lua bug silently defeats the entire cross-instance throttle.
2. **Policy dependency:** `allowRateLimit` (HTTP, `internal/middleware/ratelimit.go:120-141`) and `allowGRPCRateLimit` (gRPC, `internal/handler/grpc/server.go`) consume `DistributedLimiter.Allow`'s `(allowed, retry, error)` triple:
   - a `0` retry hint on a deny → `Retry-After` degrades to the per-instance hint or `"1"` (middleware line ~83);
   - a swallowed error → sensitive paths (`/v1/me/erase`, `/v1/me/export`, `/v1/me/sync`, `/v1/sync/jobs`, `/v1/audit`, `/v1/admin`, `/v1/operations`, `/internal/v1/events`) flip from fail-closed 503 `rate.limiter_unavailable` to fail-open.
3. **Documented semantic equivalence that does not hold:** `internal/config/config.go:37-38` claims "the same budget is enforced across instances"; an outage swaps fixed-window/global for token-bucket/per-instance.

---

## 3. Design decisions

### D1 — Test-only mandatory scope; zero production code changes
The mandatory scope is exactly one new file: `internal/pkg/ratelimit/redis_test.go` (white-box `package ratelimit`, matching the existing `ratelimit_test.go` convention). No change to `redis.go`, `ratelimit.go`, middleware, gRPC interceptor, wiring, config validation, or the `DistributedLimiter` interface.

Rationale: the current fixed-window semantics are a shipped, documented (in the config comment) contract; changing them silently is a behavior regression for anyone relying on bursty 2×window admission. The direction's acceptance criteria mandate *pinning* current behavior with tests (R1–R8), with alignment made optional (§4).

### D2 — Deterministic unit harness: miniredis + rueidis, no Docker, no sleeps, no network
Duplicate the ~15-line `newMiniRedis` helper (from `internal/idempotency/store_redis_test.go:167-186`) into the new test file rather than extracting a shared test package.

- **Why duplicate:** extracting `internal/pkg/testutil` would create a new package and widen the change set; the pattern is small and both files are test-only (exempt from `make check-file-sizes`). Add a comment citing the idempotency original for single-source drift detection.
- **Why it works:** `rueidis.B().Eval()`/`B().Pttl()`/`B().Persist()` all execute against miniredis v2.38.0 (EVAL via `lua.go`, PTTL/PERSIST via `cmd_generic.go:45-48`). `DisableCache: true` + `AlwaysRESP2: true` are mandatory (miniredis has no RESP3 client-side caching). The Lua script uses only `redis.call('INCR', …)` / `redis.call('PEXPIRE', …)` — both supported inside miniredis' Lua interpreter.
- **Time control:** `srv.FastForward(d)` advances the miniredis clock and expires keys; never `time.Sleep`. Miniredis clock resolution is ms → retry-hint assertions use ±50ms tolerance; the window-fallback assertion is exact.

### D3 — White-box reach for deterministic branch coverage
Tests live in `package ratelimit` to reach `l.key()`, `l.window`, and the `ttl < 1` fallback deterministically. The `PTTL`-fallback branch (R3 fallback) is forced by issuing `PERSIST` on the hashed key after exhausting the budget (PTTL → −1), so the full-window fallback is exercised without waiting. Closed-client behavior (R7) is forced with `client.Close()` — a genuine client error that must surface as `(false, 0, err)`, the tripwire for middleware fail-closed policy.

### D4 — Boundary regression pinned, alignment made optional (two paths)
- **R8 pins** the 2×burst boundary admission and contrasts it with the local token bucket's smooth refill, in executable form.
- **Optional path A (behavioral alignment):** rewrite `redisRateScript` to a token-bucket Lua (INCRBYFLOAT/HGET-based refill). Gated by the new proof test `TestRedisLimiter_tokenBucket_sustainedRateNoBoundarySpike` (R9): sustained rate == rate, no 2×burst instant, `burst+1` within one refill interval denies. **If the script is rewritten, the change is rejected without R9.** Re-run R1–R3 with token-bucket expectations. Not required by this design.
- **Optional path B (documentation alignment):** correct `internal/config/config.go:37-38` to state that the distributed path is a fixed-window counter that, on Redis outage, falls back to a per-instance smooth token bucket. Comment-only; recommended but non-gating; must not represent target behavior as shipped (AGENTS.md).

### D5 — Optional integration smoke test (explicitly optional, isolated)
`internal/pkg/ratelimit/redis_integration_test.go` with `//go:build integration`: one smoke test (allow-to-burst → deny → expiry-reset → sha256 key presence) against real Redis via `github.com/testcontainers/testcontainers-go/modules/redis v0.32.0`. This is the **only** new dependency (`go.mod`), matching the already-pinned testcontainers core version. It must compile under `make check-integration-compile` and pass under `make test-integration`; it never runs under `make test`.

### D6 — API surface (explicit statement)
- **External API:** none changed in mandatory scope. `DistributedLimiter.Allow(context.Context, string) (bool, time.Duration, error)`, `NewRedisLimiter(client rueidis.Client, rate float64, burst int) *RedisLimiter`, `RedisLimiter`, `DenyAllLimiter`, `Limiter` are byte-for-byte unchanged. `go build ./...` output is identical.
- **Observable contract under test:** the `(allowed, retry, error)` triple semantics — allow implies `retry == 0 && err == nil`; deny implies `retry > 0` (ms) `&& err == nil`; error implies `allowed == false && retry == 0`. Plus key hygiene (sha256, never raw), window math, and constructor validation.
- **New test-only API:** none exported; unexported reach only (`l.key()`, `l.window`).

---

## 4. Compatibility constraints

1. **No production code changes** in mandatory scope → no behavior, config, wire, or storage compatibility risk. `make check-audit-swallow`, `make check-migration-set`, `make check-proto-sync`, `make check-architecture`, `make check-load-scenarios`, `make check-composition-root` are unaffected (no audit sites, migrations, protos, packages, or wiring touched).
2. **`make check-file-sizes`:** new files are `*_test.go` → explicitly exempt (Makefile `find … ! -name '*_test.go'`).
3. **No dead-tree edits** (`internal/{file,message,notification,audit}`), no migration files, no proto changes (AGENTS.md gotchas).
4. **Unit suite stays Docker-free / network-free / sleep-free** (AGENTS.md engineering constraints): miniredis is in-process; the only clock control is `srv.FastForward`.
5. **`go.mod`:** unchanged for mandatory scope. Optional integration item adds exactly `github.com/testcontainers/testcontainers-go/modules/redis v0.32.0` (version pinned to the already-present core v0.32.0); no `go.sum` churn beyond that single module.
6. **Interface-bound compatibility:** middleware and gRPC interceptor depend on error-vs-allow discrimination. Tests must never assert behavior that would require changing `Allow`'s signature or semantics — R1–R8 all hold under the current implementation as written (verified by reading `redis.go` line-by-line).
7. **Toolchain:** Go 1.26, module `github.com/aero/aero-id`; tests follow the existing `package ratelimit` white-box style.

---

## 5. Failure modes (and how the tests make them observable)

| # | Failure mode | Observable effect today | Pinned by |
|---|---|---|---|
| F1 | Lua/`INCR` failure (Redis down, closed client, network partition, script regression) | `Allow` returns `(false, 0, err)` with `"redis rate limit increment: …"`. If this were ever swallowed into `(true, …)`, sensitive paths would fail open silently. | R7 (closed client must produce `err != nil`, `allowed == false`, `retry == 0`) |
| F2 | `PTTL` failure after a successful INCR | `(false, 0, err)` with `"redis rate limit ttl: …"` — deny-with-error is the safe direction. | R7's structural sibling; covered by the returned-triple contract asserted in every deny test (never `(true, …)` on error) |
| F3 | `PTTL` returns −1 / no expiry (e.g., key persisted, expiry stripped) | Without the `<1ms` fallback, deny would return `retry = 0` → middleware silently substitutes per-instance `RetryAfter()` (or `"1"`) → wrong `Retry-After` and cross-instance hint lost. | R3 fallback test: `PERSIST` then deny → `(false, window, nil)` **exactly** |
| F4 | Window boundary admits 2×burst (fixed-window, request-anchored) | Documented divergence from the smooth local token bucket; if "fixed" silently, shared-budget shape changes for existing deployments. | R8 (second full burst re-admitted at boundary) + R8 contrast (local bucket admits only 1 after 1s refill) |
| F5 | Wrong retry hint on deny (e.g., off-by-one in window math, ms vs s confusion) | `Retry-After` wrong; too-small hint → client thundering herd; too-large → underutilization. | R1, R3 (tolerance ±50ms against `PTTL`), R3 fallback (exact) |
| F6 | Constructor accepts invalid args (nil client / rate≤0 / burst≤0) and installs a broken limiter | nil-return contract violated → wiring (`internal/app/ratelimit.go`) could install a non-functional limiter. | R4 table |
| F7 | Raw key written to Redis (hashing regression) | PII/actor UIDs leak into Redis keys; collision across namespaces. | R6 (`srv.Exists` hash true, raw absent, `srv.Keys()` substring scan, `l.key()` equality) |
| F8 | Nil-receiver / disabled limiter denies (fail-closed by accident) | Middleware expects fail-open for disabled state; a deny here would 429 all traffic after misconfiguration. | R5 (100 iterations, mirrors `TestAllow_disabledLimiterAlwaysPasses`) |
| F9 | Window-expiry reset broken (PEXPIRE-on-first not re-armed) | After expiry, counter never resets → permanent 429 after first window. | R2 (post-expiry key exists again, full budget, TTL ≈ window) |
| F10 | Token-bucket rewrite lands without proof (optional path A) | Unproven burst/sustain characteristics replace a pinned contract. | R9 is the gate: change rejected without it |
| F11 | Test-harness failures (miniredis gaps, RESP3, real sleeps) | Tests would be flaky or silently skip. Mitigations: `AlwaysRESP2`+`DisableCache`; `FastForward` for time; ±50ms tolerance for ms-resolution clock; no sleeps. | Harness construction in §3 D2 |

---

## 6. Test plan (concrete)

All tests live in `internal/pkg/ratelimit/redis_test.go`, `package ratelimit`. Shared helper `newMiniRedis(t)` duplicated from `internal/idempotency/store_redis_test.go:167-186` (comment cites the original). Hash helper: `hashed(k) = fmt.Sprintf("aero-id:rate:%x", sha256.Sum256([]byte(k)))` for assertions, cross-checked against unexported `l.key()`.

| Test | Requirement | Steps (deterministic) |
|---|---|---|
| `TestRedisLimiter_allowsExactlyBurst` | R1 | Table over `(rate=10, burst=3)` → window 1s (1s-floor path) and `(rate=1, burst=5)` → window 5s (non-floor path). For each: `Allow(ctx,"k")` N times → first `burst` are `(true,0,nil)`, the next is `(false,r,nil)` with `r > 0`. |
| `TestRedisLimiter_windowExpiryResetsCount` | R2 | `(rate=1, burst=5)`; exhaust 5, 6th denies. `srv.FastForward(5s + 1ms)`. Next 5 pass, following denies. Assert `srv.Exists(hashed)` is true again and `PTTL` ≈ 5s (PEXPIRE-on-count==1 re-armed). |
| `TestRedisLimiter_denyRetryHintMatchesTTL` | R3 | `(rate=1, burst=5)`; exhaust; `srv.FastForward(2500ms)`; next call → `(false,r,nil)`, assert `r > 0`, `r ≤ 5s`, `|r − 2500ms| ≤ 50ms`. |
| `TestRedisLimiter_pttlFallbackUsesWindow` | R3 (fallback) | Exhaust; `client.Do(ctx, client.B().Persist().Key(hashed).Build())`; next call → `(false, 5s, nil)` **exactly** (window in ms). |
| `TestNewRedisLimiter_invalidArgsNil` | R4 | Table: `(nil client,1,1)`, `(real,0,1)`, `(real,1,0)`, `(real,−1,1)`, `(real,1,−1)` → all nil; positive control `(real,1,1)` → non-nil. |
| `TestRedisLimiter_disabledAlwaysAllows` | R5 | `var l *RedisLimiter; l.Allow(ctx,"k") == (true,0,nil)`; `&RedisLimiter{client:nil, rate:1, burst:1}`; `&RedisLimiter{client:real, rate:1, burst:0}`; 100 iterations each. |
| `TestRedisLimiter_hashesRedisKey` | R6 | `Allow(ctx, "actor-secret-1")`; assert `srv.Exists(hashed("actor-secret-1"))`; `!srv.Exists("actor-secret-1")`; no key in `srv.Keys()` contains `"actor-secret-1"`; `l.key("actor-secret-1") == hashed("actor-secret-1")`. |
| `TestRedisLimiter_closedClientErrors` | R7 | `client.Close()`; `Allow` → `allowed == false`, `retry == 0`, `err != nil`, `strings.Contains(err.Error(), "redis rate limit increment")`. |
| `TestRedisLimiter_fixedWindow_boundaryAllowsSecondFullBurst` | R8 | `(rate=1, burst=5)`. Exhaust 5 (6th denies). `srv.FastForward(5s + 1ms)`. All 5 pass back-to-back with no further advance; 6th denies. Total admitted = 10 = 2×burst. Control: `Allow(ctx,"other")` passes while `"k"` exhausted. |
| `TestLocalLimiter_tokenBucket_refillsSmoothly` (contrast) | R8 | Local `Limiter(1, 5)`, pinned injectable `now` clock. Exhaust 5, 6th blocked. Advance 1s → exactly 1 passes, next blocked. Advance 5s → 5 available. Documents the divergence in executable form. |
| `TestRedisLimiter_tokenBucket_sustainedRateNoBoundarySpike` | R9 (**conditional**) | Only if `redisRateScript` is rewritten to a token bucket: `(rate=10, burst=5)`, drive at exactly rate/s across ≥3 window-equivalents (`srv.FastForward` between bursts): all sustained-rate requests pass; no instant admits 2×burst back-to-back; `burst+1` within one refill interval denies. |

**Optional integration smoke** (`redis_integration_test.go`, `//go:build integration`): real Redis via testcontainers `modules/redis v0.32.0`; one test — allow-to-burst → deny → expiry-reset → sha256 key assertions. Must compile under `make check-integration-compile`; passes under `make test-integration`; inert under `make test`.

---

## 7. Migration steps (rollout order)

All steps are additive; no data, config, or wire migration. "Migration" here = bringing the unverified cross-instance path under test + (optionally) aligning documentation.

1. **Land `redis_test.go`** with the `newMiniRedis` helper + R1–R8 tests (§6). Run `make test` — the unit suite must stay green and Docker-free.
2. **Static gates:** `make lint && make lint-strict && make build` (direction's gate). Then the full done-gate from AGENTS.md (`make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && make check-migration-set && make check-composition-root && make check-proto-sync && make check-integration-compile && make build`) — expected: all green, since the change set is test files only.
3. **Optional path B (recommended, separate small commit):** correct the `internal/config/config.go:37-38` comment to describe the fixed-window counter and per-instance token-bucket fallback on outage. Comment-only; re-run `make lint`/`make build`.
4. **Optional integration item (independent):** add `testcontainers-go/modules/redis v0.32.0` to `go.mod`, add `redis_integration_test.go`; verify `make check-integration-compile` (no Docker) then `make test-integration` (Docker). Must not affect `make test`.
5. **Optional path A (behavioral alignment, separate explicitly-scoped change):** rewrite `redisRateScript` to a token bucket **only together with** the R9 proof test; re-run R1–R3 with token-bucket expectations and the full done-gate. Coordinate with the corrected config comment from step 3 so documentation and behavior match. If the rewrite lands without R9, the change is rejected (F10).
6. **Review/acceptance:** run the acceptance mapping in §8; no binaries committed at repo root; `make build` output stays out of tree.

---

## 8. Testable acceptance mapping

| Acceptance criterion (from spec §9) | Testable check | Gate |
|---|---|---|
| 1. `redis_test.go` exists; miniredis-backed; covers within-window exactly `burst` then deny | `TestRedisLimiter_allowsExactlyBurst` | `make test` |
| 1. window expiry resets the count | `TestRedisLimiter_windowExpiryResetsCount` | `make test` |
| 1. deny retry hint ≈ TTL incl. PTTL-fallback branch | `TestRedisLimiter_denyRetryHintMatchesTTL` + `TestRedisLimiter_pttlFallbackUsesWindow` | `make test` |
| 1. `NewRedisLimiter(nil/rate≤0/burst≤0)` returns nil | `TestNewRedisLimiter_invalidArgsNil` | `make test` |
| 1. disabled limiter always allows | `TestRedisLimiter_disabledAlwaysAllows` (100 iterations) | `make test` |
| 1. key stored is sha256 hash, never raw | `TestRedisLimiter_hashesRedisKey` | `make test` |
| 1. closed client surfaces an error | `TestRedisLimiter_closedClientErrors` (asserts `"redis rate limit increment"`) | `make test` |
| 2. regression pinning fixed-window boundary (2×burst) | `TestRedisLimiter_fixedWindow_boundaryAllowsSecondFullBurst` + contrast `TestLocalLimiter_tokenBucket_refillsSmoothly` | `make test` |
| 3. conditional token-bucket proof exists **iff** script rewritten | `TestRedisLimiter_tokenBucket_sustainedRateNoBoundarySpike` (change rejected without it) | code review + `make test` |
| 4. optional integration-tagged test compiles/runs via testcontainers | `redis_integration_test.go` (`//go:build integration`) | `make check-integration-compile` (no Docker) + `make test-integration` (Docker) |
| 5. direction gate green | `make test && make lint && make lint-strict && make build`; full done-gate standing | CI |
| 6. unit suite deterministic (no sleeps/real Redis/network/Docker) | no `time.Sleep` in new files; all time via `srv.FastForward` | code review + `make test` (CI env has no Redis) |
| 7. no production behavior change in mandatory scope | `go build ./...` unchanged; only `*_test.go` added (optional B is comment-only) | `git diff --stat` review + `make check-file-sizes` |

---

## 9. Non-goals / scope boundary

- **No** changes to the Lua script semantics, `window` formula, key format, `DistributedLimiter` interface, middleware policy, gRPC interceptor, or wiring.
- **No** new rate-limit features (per-route tiers, sliding windows, circuit breakers, metrics, Redis cluster support).
- **No** migration of the local `Limiter` to Redis; **no** config-validation changes.
- **No** shared test-helper package extraction (keep the ~15-line duplication; revisit only if a third consumer appears).
- Unit tests remain Docker-free and deterministic; no real cross-data-center calls (AGENTS.md).
- Mandatory scope modifies only test files; optional path B touches only a comment in `internal/config/config.go`.

---

## 10. Evidence baseline

`internal/pkg/ratelimit/redis.go`, `internal/pkg/ratelimit/ratelimit.go`, `internal/pkg/ratelimit/ratelimit_test.go`, `internal/idempotency/store_redis_test.go` (helper at 167–186), `internal/config/config.go:37-38`, `internal/app/ratelimit.go`, `internal/middleware/ratelimit.go` (33–44, 55–120, 120–141), `internal/handler/grpc/server.go` (~295–330), `go.mod` (rueidis v1.0.76, miniredis v2.38.0, testcontainers v0.32.0 + minio/nats/postgres modules), miniredis v2.38.0 module cache (`cmd_scripting.go:20`, `cmd_generic.go:45-48`, `lua.go`, `miniredis.go:365`, `direct.go:39,444`), Makefile (`test-integration`, `check-integration-compile`, `check-file-sizes`).

**Verification verdict:** verified with one minor correction — `ratelimit_test.go` is 107 lines (not 184); all substantive claims (8 local-only tests, redis.go behavior, helper pattern, module versions, middleware/interceptor contracts, config-comment location, gate exemptions) hold exactly as stated.
