# Design — Idempotency middleware: observe store-error discards + bound response capture

**Direction:** *Idempotency middleware: silent store-error discards + unbounded response buffering break the "authoritative receipt / bounded memory" contract*
**Source:** `docs/auto/analyses/internal-middleware-697499e2.json` (item 1); requirements spec `requirements-10762e10` (2026-08-16, approved)
**Date:** 2026-08-16
**Status:** Design (not implemented)

---

## 0. Verification verdict

The evidence was treated as untrusted and re-verified against the current tree. **All load-bearing claims verified.** Line-level checks:

| # | Claim | Verified at | Result |
|---|---|---|---|
| E1 | `_ = store.Release(...)` on the 5xx path discards its error | `internal/middleware/idempotency.go:43` | ✅ exact |
| E2 | `_ = store.Complete(...)` on the success path discards its error | `internal/middleware/idempotency.go:45` | ✅ exact |
| E3 | `captureWriter` tees the full body into an uncapped `bytes.Buffer` | `:39` (`&bytes.Buffer{}`), `:76` field, `:79-87` (`Write`/`WriteString` unconditionally `body.Write`) | ✅ exact |
| E4 | Default TTL 24h → post-TTL retry re-executes a lost receipt | `idempotency.go:13` `defaultIdempotencyTTL = 24 * time.Hour` (evidence already flagged its own `:10` drift) | ✅ |
| E5 | `Complete`/`Release` return errors by design; `reservationTTL = 2 * time.Minute` | `internal/idempotency/store.go:29-40` (interface), `reservationTTL` const | ✅ exact |
| E6 | Retry after lost Complete hits the `default:` 409 branch | `idempotency.go:53-59` (`case rec.Completed:` … `default:` → 409 `idempotency.in_progress`) | ✅ exact |
| E7 | `RouterConfig.Metrics` plumbed; Idempotency wired inside `authenticatedMiddleware` which holds `cfg` | `router.go:32` (field), `:61-62` (global `middleware.Metrics`), `:100` (Idempotency wiring). Evidence cited `:60-61`/`:100` — cosmetic drift, substance confirmed | ✅ |
| E8 | `Observe*` pattern + `WriteText` delegation precedent | `metrics.go` `ObserveHTTP/GRPC/SSO/Outbox*`; `WriteText` (≈`:150`, evidence cited `:220` — drift) calls `r.writeControlPlaneText(w)` (≈`control_plane.go:228`, evidence cited `:216` — drift). Substance confirmed | ✅ |
| E9 | `check-audit-swallow` P1/P2 match only the listed audit/outbox methods | `scripts/check-audit-swallow.sh:143-144` — `store.*`, `CompleteFailed`, `ReleaseFailed`, `Overflowed`, `ObserveIdempotency` match neither pattern | ✅ |
| E10 | AGENTS.md contracts (authoritative receipts; bounded memory; fail-open carve-outs) | project instructions | ✅ |
| E11 | Existing tests: `memStore` `:19-49`, `setupIdemRouter` `:52-63`, three tests | `idempotency_test.go` | ✅ exact |
| E12 | File sizes: 87 / 360 / 129 | `wc -l` on `idempotency.go`, `metrics.go`, `router.go` | ✅ exact |
| O4 | Exactly two live callers of `middleware.Idempotency` | `router.go:100`, `idempotency_test.go:56` (`.claude/worktrees/*` are stale copies, not live) | ✅ |
| — | `internal/middleware` already imports `internal/pkg/metrics` (adapter can live there) | `internal/middleware/metrics.go:8` | ✅ |
| — | `/metrics` tests use substring assertions, not byte-pinned exposition → conditional rendering of new series is safe | `metrics_test.go`, `metrics_handler_test.go` | ✅ |

Minor drifts found (all cosmetic): the evidence's `metrics.go:220`/`control_plane.go:216` citations are off by ~70/12 lines; its `router.go:60-61` is `:61-62`. No symbol or substance discrepancy.

---

## 1. Problem statement

`internal/middleware/idempotency.go` has three defects, all unobservable today:

1. **Silent `Complete` failure (`:45`)** — client gets 200, the durable receipt row is never written. Retry before TTL (24h) hits the `default:` branch → 409 `idempotency.in_progress` even though the mutation executed; retry after TTL re-executes. Violates "receipts are authoritative and are not disposable" and breaks exactly-once.
2. **Silent `Release` failure (`:43`)** — a 5xx response leaves the row reserved (`status=0`) until `reservationTTL` (2 min) or cleaner purge; the client's retry of a genuine server error gets 409 for the strand window.
3. **Unbounded capture** — `captureWriter.Write/WriteString` (`:79-87`) append the entire body to an uncapped `bytes.Buffer` for every keyed POST, then write the whole thing into the Postgres `response` column. Under concurrency, memory grows as N·S without bound; a single oversized response lands unbounded into the DB.

Observability primitives already exist: the `metrics.Registry` `Observe*` pattern, the sibling-file rendering precedent (`control_plane.go` + one delegated call in `WriteText`), and `RouterConfig.Metrics` plumbed into `authenticatedMiddleware`.

This design (a) makes every store-error discard observable through a nil-safe observer hook, (b) bounds response capture at 1 MiB with a documented, metered exactly-once degrade for oversized responses, and (c) wires both into the existing metrics registry. It changes no store contract, no schema, no migration, and no client-visible response bytes.

---

## 2. API changes

### 2.1 New: `IdempotencyObserver` (in `internal/middleware/idempotency.go`)

```go
// IdempotencyObserver observes idempotency middleware failures and degrades.
// A nil observer is a no-op; implementations must not block the request path
// (the middleware guards nil and recovers panics before invoking).
type IdempotencyObserver interface {
    CompleteFailed(err error) // store.Complete failed on the success path (F1)
    ReleaseFailed(err error)  // store.Release failed on the 5xx or overflow path (F2/F4)
    Overflowed()              // response capture exceeded maxIdempotencyCaptureBytes (F4)
}
```

- `err` is the already-context-wrapped store error (`"idempotency complete: %w"`, `"idempotency release: %w"` — `store.go`), so observers that log can render it directly.
- Each method fires **at most once per occurrence**, per request: a request may fire at most one of `CompleteFailed`/`ReleaseFailed`, and may additionally fire `Overflowed`. The pair `CompleteFailed` + `Overflowed` is impossible by construction (§3.2).

### 2.2 Signature change: `middleware.Idempotency`

```go
func Idempotency(store idempotency.Store, ttl time.Duration, obs IdempotencyObserver) gin.HandlerFunc
```

- Third positional parameter (per O4; preferred over variadic: exactly two callers, both updated — `router.go:100`, `idempotency_test.go:56`; a variadic would conceal mistakes).
- `obs == nil` ⇒ byte-identical behavior to today (all call sites nil-guarded).
- No other middleware signature changes.

### 2.3 New: `internal/pkg/metrics/idempotency.go` (sibling file, C2)

```go
package metrics

// idempotencyMetrics counts idempotency middleware failures and degrades,
// keyed by op. Reuses the control_plane.go counterMap primitive (own mutex,
// lazy map, snapshot under r.mu — no lock-ordering hazard: add() never takes
// r.mu, snapshot() is called only while holding r.mu).
type idempotencyMetrics struct{ counts counterMap }

func (r *Registry) ObserveIdempotency(op string) { r.idem.counts.add(op, 1) }

func (r *Registry) writeIdempotencyText(w io.Writer) {
    counts := r.idem.counts.snapshot()
    if len(counts) == 0 {
        return
    }
    fmt.Fprintln(w, "# HELP aeroid_idempotency_total Idempotency middleware failures and degrades by op.")
    fmt.Fprintln(w, "# TYPE aeroid_idempotency_total counter")
    for _, op := range sortedKeys(counts) {
        fmt.Fprintf(w, "aeroid_idempotency_total{op=%q} %d\n", op, counts[op])
    }
}
```

- **Series:** `aeroid_idempotency_total{op="complete_failed"|"release_failed"|"capture_overflow"}` (op-label form per O2).
- **Registry changes (in `metrics.go`):** add one struct field `idem idempotencyMetrics` and **one** delegated call line `r.writeIdempotencyText(w)` at the end of `WriteText` (after `r.writeOutboxText(w)`). `NewRegistry` needs no change (`counterMap` is lazy). `metrics.go` goes 360 → 362 lines, well under 500 (C2).
- **Conditional rendering:** series appear only once a count exists, matching the outbox/control-plane precedent. Existing `/metrics` tests use substring assertions (verified), so output is stable for zero counts.

### 2.4 New: metrics adapter (`internal/middleware/idempotency_observer.go`)

`internal/middleware` already imports `internal/pkg/metrics` (`middleware/metrics.go:8`), so the adapter lives there per O3 (keeps `handler/http` import-free of new symbols):

```go
// metricsIdempotencyObserver adapts the metrics Registry to IdempotencyObserver.
type metricsIdempotencyObserver struct{ reg *metrics.Registry }

func (o metricsIdempotencyObserver) CompleteFailed(error) { o.reg.ObserveIdempotency("complete_failed") }
func (o metricsIdempotencyObserver) ReleaseFailed(error)  { o.reg.ObserveIdempotency("release_failed") }
func (o metricsIdempotencyObserver) Overflowed()          { o.reg.ObserveIdempotency("capture_overflow") }

func NewMetricsIdempotencyObserver(reg *metrics.Registry) IdempotencyObserver {
    return metricsIdempotencyObserver{reg: reg}
}
```

The error argument is deliberately dropped in the metric (op-only label keeps cardinality bounded; observers that want detail can log `err`).

### 2.5 Router wiring (`internal/handler/http/router.go:100`)

```go
if cfg.Idempotency != nil {
    var obs middleware.IdempotencyObserver
    if cfg.Metrics != nil {
        obs = middleware.NewMetricsIdempotencyObserver(cfg.Metrics)
    }
    result = append(result, middleware.Idempotency(cfg.Idempotency, cfg.IdempotencyTTL, obs))
}
```

Mirrors the existing `if cfg.Metrics != nil` guards (`:61-62`). With metrics unset, the observer is nil and behavior is exactly today's.

---

## 3. Behavioral design

### 3.1 Bounded capture (`captureWriter`)

New package const in `internal/middleware/idempotency.go`:

```go
// maxIdempotencyCaptureBytes bounds the buffered response per keyed POST.
// Larger responses are delivered intact but never stored (see F4).
const maxIdempotencyCaptureBytes = 1 << 20 // 1 MiB
```

```go
type captureWriter struct {
    gin.ResponseWriter
    body       *bytes.Buffer
    overflowed bool
}

func (w *captureWriter) capture(b []byte) {
    if w.overflowed { // already at/over cap; keep forwarding, never append
        return
    }
    remaining := maxIdempotencyCaptureBytes - w.body.Len()
    if remaining <= 0 {
        w.overflowed = true
        return
    }
    if len(b) > remaining {
        w.overflowed = true
        w.body.Write(b[:remaining])
        return
    }
    w.body.Write(b)
}

func (w *captureWriter) Write(b []byte) (int, error) {
    w.capture(b)
    return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
    w.capture([]byte(s))
    return w.ResponseWriter.WriteString(s)
}
```

Invariants:

- **Forwarding is never altered**: every byte of `b`/`s` reaches the underlying `gin.ResponseWriter` regardless of cap state — the client receives the complete, intact response (F3).
- **Buffer is bounded**: `body.Len() ≤ 1 MiB` always (we append at most `remaining`; `WriteString` shares the same `capture` path). Memory per in-flight keyed POST ≤ 1 MiB; N concurrent requests ≤ N·1 MiB.
- **Overflow detection is precise**: `overflowed` becomes true exactly when the total response would exceed the cap. A response of exactly 1 MiB is *not* overflowed (buffer full, `Complete` stores 1 MiB — in-bounds, full exactly-once guarantee). A single write that crosses the cap appends only up to the cap; everything is discarded by the subsequent `Release` (never stored).
- `Write`/`WriteString` return the underlying writer's `(n, err)` unchanged; empty slices are a no-op. `WriteHeader`/`Flush`/`Status` pass through the embedded writer untouched.

### 3.2 Finalize logic (extracted helper, C1)

The closure body today is ~49 lines; overflow handling pushes it past funlen(50). Extract (keeps both the closure and the helper under 50 lines / cyclop 10):

```go
func finalizeIdempotency(c *gin.Context, cw *captureWriter, obs IdempotencyObserver,
    store idempotency.Store, uid, endpoint, key string, ttl time.Duration) {
    status := c.Writer.Status()
    ctx := c.Request.Context()
    switch {
    case cw != nil && cw.overflowed:
        // F4: never store a truncated body. Deliver intact, then release so a
        // retry re-executes; the reservation is held while in flight (409 for
        // concurrent duplicates), gone after this returns.
        notify(obs, func() { obs.Overflowed() })
        if err := store.Release(ctx, uid, endpoint, key); err != nil {
            notify(obs, func() { obs.ReleaseFailed(err) })
        }
    case status >= http.StatusInternalServerError:
        if err := store.Release(ctx, uid, endpoint, key); err != nil {
            notify(obs, func() { obs.ReleaseFailed(err) })
        }
    default:
        if err := store.Complete(ctx, uid, endpoint, key, idempotency.Record{
            Status:      status,
            ContentType: c.Writer.Header().Get("Content-Type"),
            Body:        cw.body.Bytes(),
        }, ttl); err != nil {
            notify(obs, func() { obs.CompleteFailed(err) })
        }
    }
}
```

`notify` (package-level, in `idempotency.go`):

```go
// notify delivers one observation to obs. A nil observer is a no-op; a panic
// inside the callback is recovered so an observer bug can never alter the
// request path (F7).
func notify(obs IdempotencyObserver, fn func()) {
    if obs == nil {
        return
    }
    defer func() { _ = recover() }()
    fn()
}
```

The middleware body becomes: `cw := &captureWriter{...}`; `c.Next()`; `finalizeIdempotency(c, cw, obs, store, uid, endpoint, key, ttl)` — plus the existing pre-branch (`key`/method gate, `Reserve` fail-open, `rec.Completed` replay, `default:` 409), unchanged.

Per-request observation invariant (deterministic, testable):

- success path (status < 500, not overflowed): at most one `CompleteFailed`;
- 5xx path: at most one `ReleaseFailed`;
- overflow path: exactly one `Overflowed`, plus `ReleaseFailed` iff the release errors;
- `CompleteFailed` and `Overflowed` never co-fire;
- metrics increment happens inside the adapter on the same call.

### 3.3 Considered alternatives (rejected)

| Alternative | Rejected because |
|---|---|
| Variadic observer param | Hides missed wiring at call sites; only two callers exist (O4 prefers positional) |
| Retry `Complete` inside the middleware after a failure | Response already delivered; a synchronous retry blocks the worker with no benefit to the client; a store write failure is transient by nature and the row self-heals at 24h expiry — the correct signal is `complete_failed` alerting, and a retry path would need new store capabilities (F6 forbids) |
| Fail closed (convert to 5xx) on `Complete` failure | Response already committed by the time the error surfaces; violates the layer's fail-open contract (F1) |
| Truncate the stored body at the cap and `Complete` | A truncated replay is a silent correctness violation and breaks "exactly-once via receipts"; re-execute-on-retry is strictly better (F4) |
| Make the cap a `RouterConfig` knob | YAGNI; the requirement fixes a 1 MiB const; config knobs can follow later if needed |
| Enforce the cap inside `GormStore` | Store interface/schema are frozen (F6); the cap is a middleware concern |
| Always render zero-valued metric series | Diverges from the outbox/control-plane precedent; existing tests use substring assertions; conditional rendering matches the house style |
| Adapter in `internal/handler/http` | Works, but `internal/middleware` already imports `internal/pkg/metrics`; keeps router wiring to one line |

---

## 4. Compatibility constraints

| Constraint | Mechanism |
|---|---|
| `idempotency.Store` unchanged (F6) | No edits to `internal/idempotency/*` at all — no interface, `GormStore`, `store_redis.go`, cleaner, schema, or migration changes |
| Client-visible bytes unchanged for in-bounds responses (F5) | Capture/forward path only adds a cap check; `Complete` stores byte-identical bodies; replay/409/no-key/TTL/fail-open-`Reserve` untouched; `obs == nil` short-circuits all new code |
| Existing three tests stay green unmodified (A4) | They call `middleware.Idempotency(store, time.Hour)` — must be updated to pass a third arg `nil` (O4) but assert nothing new |
| Gates: file sizes, funlen, cyclop (C1/C2) | `idempotency.go` 87 → ~130 lines (< 500); new helper and `notify` each < 50 lines, cyclop ≤ 4; `metrics.go` 360 → 362 (< 500); new sibling files small |
| `check-audit-swallow` neutrality (C3/E9) | `store.Complete/Release`, `obs.CompleteFailed/ReleaseFailed/Overflowed`, `reg.ObserveIdempotency` match neither P1 nor P2 pattern (verified: `check-audit-swallow.sh:143-144` enumerates only the audit/outbox methods); no new audit-fact sites |
| No new deps / schema / migrations / packages (C4) | Only new files are siblings inside existing packages |
| No config change | Observer derives from the existing `cfg.Metrics != nil` flag |
| Load-bearing middleware order untouched | Idempotency stays after Auth→Ban→RateLimit in `authenticatedMiddleware`; no global-order edits |

---

## 5. Failure modes

| # | Mode | Behavior after change | Residual risk / note |
|---|---|---|---|
| FM1 | `Complete` fails on the success path (transient DB error) | 2xx delivered intact; `CompleteFailed(err)` fired once; metric `complete_failed` +1. Retry within TTL → 409 (correct — the mutation ran); retry after 24h expiry → reservation reclaimed by `Reserve`, mutation re-executes | Receipt is still lost (the observer cannot heal it). This is the accepted, now-visible degradation; alert on `complete_failed` rate. No client-visible change (F1) |
| FM2 | `Release` fails on the 5xx path | 5xx delivered intact; `ReleaseFailed(err)` fired once; metric `release_failed` +1. Row stays reserved until `reservationTTL` (2 min) reclaim or cleaner purge; retries 409 during the strand window | Same strand semantics as today, now observed |
| FM3 | Response exceeds 1 MiB (overflow) | Full body delivered intact; nothing stored (`Complete` never called); `Overflowed()` fired; reservation released ⇒ a retry re-executes (handler runs again). Metric `capture_overflow` +1. Buffer held ≤ 1 MiB; DB `response` column can never receive > 1 MiB from new rows | Deliberate, metered exactly-once degrade for oversized responses only. A retry may re-execute while the original response is still draining to the socket (Release runs when `c.Next()` returns, before transport flush) — same window as today's 5xx Release path; documented, not a new hazard class |
| FM4 | Overflow **and** `Release` fails | `Overflowed()` + `ReleaseFailed(err)` both fire; row strands up to 2 min; retry 409s in that window, then re-executes | Both signals visible; no silent state |
| FM5 | Nil observer (metrics unset) | All `notify` calls no-op; behavior byte-identical to today (F7) | Rollback-safe default |
| FM6 | Observer panics | `notify` recovers; request path unaffected (F7). With a real adapter the panic is impossible (no allocation/error sources) | Defensive only |
| FM7 | Handler panics mid-write (partial buffered body) | gin Recovery (global) writes 500; status ≥ 500 ⇒ Release; retry re-executes. Overflow flag irrelevant on this path | Existing behavior preserved |
| FM8 | `GormStore.Complete` matches 0 rows (reservation already released/expired) | `Updates` returns nil → treated as success; row is gone → a retry re-executes (fail-open direction, consistent) | Pre-existing latent unobservability; store frozen (F6), documented residual — retry semantics stay coherent |
| FM9 | Client disconnects mid-response | Underlying `Write` error propagates to gin as today; cap logic unaffected; buffer bounded | Existing behavior |

---

## 6. Migration steps

No data migration, schema change, or config change. This is a code-only rollout:

1. **Add observer + bounded capture** in `internal/middleware/idempotency.go`: `IdempotencyObserver`, `maxIdempotencyCaptureBytes`, capped `captureWriter.capture`, `notify`, `finalizeIdempotency`; change `Idempotency` signature to take `obs`; delete the two `_ =` discard sites.
2. **Add metrics sibling file** `internal/pkg/metrics/idempotency.go` (`ObserveIdempotency`, `writeIdempotencyText`); add the `idem` field and one `WriteText` delegation line in `metrics.go`.
3. **Add adapter** `internal/middleware/idempotency_observer.go` (`NewMetricsIdempotencyObserver`).
4. **Wire router** `internal/handler/http/router.go:100` (observer from `cfg.Metrics`; nil otherwise).
5. **Update the test call site** `idempotency_test.go:56` to pass `nil`; add regression tests (§7).
6. **Run the done-gate subset** (A0). Optionally extend the wired test for the `/metrics` exposition (A5).
7. **Deploy:** rolling restart of stateless API instances; `/metrics` gains `aeroid_idempotency_total{op=...}` only after first occurrences (conditional render). Add dashboards/alerts on the three op counters. **Rollback:** revert the commit — nil-observer behavior is byte-identical to today, so no compatibility window.

---

## 7. Testable acceptance mapping

| Req | Acceptance test | Assertions |
|---|---|---|
| F1 / O1 | `TestIdempotency_CompleteFailureObserved` — `memStore` gains fault-injection flags (`failComplete error`); recording observer; standard harness; one keyed POST; handler returns 201 JSON | (i) 201 + intact body; (ii) `CompleteFailed` called exactly once with the sentinel error; (iii) no panic; (iv) handler ran exactly once; (v) store holds no completed record for the key |
| F2 / O1 | `TestIdempotency_ReleaseFailureOn5xxObserved` — `failRelease error`; handler returns 500 | (i) 500 delivered; (ii) `ReleaseFailed` called exactly once with sentinel; (iii) no panic; (iv) `Complete` never called on this path |
| F3/F4 / O1/O2 | `TestIdempotency_OversizedResponseReleasedAndRetryReexecutes` — handler writes 2 MiB (deterministic: cap is a 1 MiB const) | (i) first POST 200 with full intact body (byte length + content equal); (ii) `Complete` not called; (iii) reservation released (store holds no row); (iv) second POST same key re-executes (counter == 2) with fresh 200 — re-executes, not replay, not 409; (v) `Overflowed` called exactly once; (vi) optional: wire the real `metrics.Registry` through `NewMetricsIdempotencyObserver` and assert `/metrics` exposition contains `aeroid_idempotency_total{op="capture_overflow"} 1`; (vii) no panic; buffer provably ≤ cap (indirect: no Complete, bounded allocation) |
| F3 edge | `TestIdempotency_CaptureBoundaryExactlyCap` (recommended) — handler writes exactly `maxIdempotencyCaptureBytes` | `Complete` called, body stored byte-equal, replay works, `Overflowed` never fired (exactly-at-cap is in-bounds) |
| F3 edge | `TestIdempotency_CaptureCapViaWriteString` (recommended) — handler uses `c.Writer.WriteString` to exceed the cap | Same as A2 via the `WriteString` path (cap logic shared) |
| F5 / A4 | Existing `TestIdempotency_ReplaysStoredResponse`, `TestIdempotency_NoKeyRunsEachTime`, `TestIdempotency_InProgressConflicts` — updated only to pass `nil` observer | Unmodified assertions stay green |
| F7 | Nil-observer run inside A4 harness + `TestIdempotency_ObserverPanicDoesNotAffectResponse` (recommended): observer whose `CompleteFailed` panics | Response still 201, no 500, no panic propagated |
| O2 | `metrics` package unit test: `ObserveIdempotency("complete_failed")` twice + `WriteText` | Exposition contains `aeroid_idempotency_total{op="complete_failed"} 2`; zero-count ops absent; concurrent-safety via `counterMap` (run with `-race`) |
| O3 / A5 (recommended) | Router-level test: `authenticatedMiddleware` with `cfg.Metrics` + faulting store + keyed POST | Observer passed at `router.go:100` is non-nil when `cfg.Metrics != nil`; the `/metrics` handler output contains the idempotency series |
| A0 | Gates | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build` all green |

---

## 8. Risks and mitigations

- **Exactly-once degrade for oversized (>1 MiB) responses** — deliberate (F4), metered, released-reservation ⇒ retry re-executes instead of observing a wrong 409; in-bounds responses keep the full guarantee. Paginated JSON APIs rarely exceed 1 MiB. Documented in the middleware doc comment.
- **Handler length growth** — mitigated by the `finalizeIdempotency` helper (C1); both the closure and helper stay under funlen(50)/cyclop(10).
- **`metrics.go` growth** — mitigated by the mandatory sibling file (C2); verified the `control_plane.go` precedent and substring-based metrics tests.
- **Lock ordering in the Registry** — `ObserveIdempotency` takes only `counterMap.mu`; `writeIdempotencyText` snapshots under `r.mu`; no path takes `counterMap.mu` then `r.mu` — no deadlock.
- **Retry racing the original response after overflow-release** — a retry during flight still sees 409 (reservation held); after release it re-executes. No double-execution window beyond the existing 2-minute reservation semantics; same shape as today's 5xx Release path.
- **`GormStore.Complete` zero-row success (FM8)** — pre-existing, out of scope (F6); retry semantics remain coherent; noted as residual.

---

## 9. Out of scope (unchanged from requirements)

- Timeout middleware deadline enforcement / 504 (separate analysis item).
- Middleware-order regression tests / shape-pin script (separate analysis item).
- Any change to `idempotency.Store`, `GormStore`, `store_redis.go`, the cleaner, schema, migrations, or TTL values.
- Changing the fail-open semantics of `Reserve` errors.
- Audit-fact emission from the idempotency layer (receipt rows remain the authority; no `Record`/`Log` sites added — C3).
- Redis/distributed rate limiting, SSE, or any other middleware.

## 10. Files touched (summary)

| File | Change |
|---|---|
| `internal/middleware/idempotency.go` | `IdempotencyObserver`, `maxIdempotencyCaptureBytes`, capped `captureWriter`, `notify`, `finalizeIdempotency`, 3-arg `Idempotency`; remove both `_ =` discards |
| `internal/middleware/idempotency_observer.go` | new: metrics adapter `NewMetricsIdempotencyObserver` |
| `internal/pkg/metrics/idempotency.go` | new: `ObserveIdempotency`, `writeIdempotencyText` |
| `internal/pkg/metrics/metrics.go` | +1 field (`idem`), +1 `WriteText` delegation line |
| `internal/handler/http/router.go` | `:100` builds/passes observer from `cfg.Metrics` |
| `internal/middleware/idempotency_test.go` | call site passes `nil`; new regression tests (§7) |
| `internal/pkg/metrics/*_test.go` | optional: idempotency-series unit test |
