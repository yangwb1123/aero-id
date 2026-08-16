# Design — Request-Context Log Correlation for `internal/pkg/logger`

**Date:** 2026-08-16
**Module:** `internal/pkg/logger`
**Source spec:** `docs/auto/runs/request-context-log-correlation-surface-trace-id-9024d1e5/` (requirements, stage 2 of pipeline)
**Status:** Design (evidence re-verified against the live tree; acceptance mapped to tests)

## 1. Verification verdict

Every load-bearing claim in the source evidence was re-checked against the current repository. All confirmed; no substantive corrections required beyond those the spec already made.

| # | Claim | Verified state |
|---|---|---|
| 1 | `internal/pkg/logger/logger.go` — thin factory `New(level, encoding) (*zap.Logger, error)`, no ctx support; sole prod caller `cmd/server/main.go:28` | ✅ 28-line file; `logger.New` at `main.go:28`; only non-test importer of the package |
| 2 | `logger_test.go` coverage limited to level parsing | ✅ only `TestNew_invalidLevel`, `TestNew_validLevels` |
| 3 | `access_log.go` emits `request_id`+`actor`, no `trace_id` | ✅ emission block lines 31–36: `zap.String("request_id", …)` :33, `zap.String("actor", …)` :34 |
| 4 | `tracing.go:52-54` sets `X-Trace-ID`, `c.Set("trace_id", …)`, `service.WithTraceID` | ✅ exact lines confirmed |
| 5 | `request_id.go:28-31` sets `c.Set("request_id")`, ctx write, `service.WithRequestID`; `RequestIDFrom` :37-40 | ✅ exact lines confirmed |
| 6 | gRPC `server.go:158` `service.WithRequestID`; `:168-169` `service.WithTraceID`+`x-trace-id` md; `:268` `service.WithActor` in `AuthInterceptor` (plus `service.WithIdentity` at :267) | ✅ confirmed |
| 7 | Outbox relay **12** `r.log.*` sites; GC sweeper **11** `s.log.*` sites; all bare `*zap.Logger` | ✅ 12 + 11 = 23 sites, none receive/emit correlation fields |
| 8 | `service.go` — unexported `ctxKey` int, `actorKey/identityKey/requestIDKey/traceIDKey`, exported `WithActor/ActorFrom/WithRequestID/RequestIDFrom/WithTraceID/TraceIDFrom`; `ActorFrom` value-based `(string, bool)` | ✅ confirmed (exports span ~30–59) |
| 9 | `check-architecture.sh` rule (d): `INFRA_LAYERS=(connector event outbox region sso)` ban on importing `internal/service`/`internal/repository`; rule (b) permits domain → `internal/pkg/*` | ✅ confirmed in script header |
| 10 | `TestTracing_preservesChainAndCorrelates` at `internal/middleware/tracing_test.go:18` (external pkg `middleware_test`); `TestAccessLog_*` at `middleware_extra_test.go:283/296/306` | ✅ confirmed (spec's location correction was right) |

Additional facts re-verified and relied on by this design:

- **HTTP chain order** (`router.go:57-67`): `RequestID → Tracing → Recovery → [Metrics] → Sample → AccessLog → CORS → [Degradation] → Timeout`. `Tracing()` derives ctx via `propagator.Extract` + `tracer.Start` (both child-context ops), so values placed by `RequestID()` survive.
- **gRPC chain order** (`server.go`): `Tracing → Metrics → [Degradation] → Recovery → Auth → [RateLimit] → [Permission] → handler` (slice order = outermost first in `ChainUnaryInterceptor`). Tracing's ctx flows into Auth's ctx and the handler, so logger-carrier values written in `TracingInterceptor` survive into `AuthInterceptor` and the handler.
- **No import cycle risk:** `internal/service` does not import `internal/pkg/logger`; `service → {domain, pkg/errors, sso}`; `sso → {domain, ssopb, authzpb}`; `domain → internal/pkg/idgen`; `pkg/errors` imports nothing internal. Therefore (a) the new edge `internal/{middleware,handler/grpc} → internal/pkg/logger` is cycle-free, and (b) a `package logger` test file may import `internal/service` for the isolation test without a test-binary import cycle.
- **File headroom:** logger.go 28, access_log.go 37, tracing.go 66, request_id.go 40, auth.go 70, grpc/server.go 438 lines — all ≪ 500.
- **gRPC test harness:** `server_test.go` already invokes interceptors directly (`TestAuthInterceptor_*` with `fakeValidator`) and has a `bufconn` server pattern (`TestNewServerWithOptions_acceptsGRPCServerOptions`, :107–137) for transport-observable assertions. `middleware_test.go:29` provides `newEngine()`.
- **`grpc.SetHeader` is a no-op in direct interceptor invocation** (returns an error the interceptor ignores via `_ =`), so the R3 header-equality assertion requires the bufconn server path, not direct invocation.

## 2. Problem statement (as verified)

`internal/pkg/logger` returns a bare `*zap.Logger`. Every request-scoped consumer drops correlation data that is already computed at the write sites: the HTTP/gRPC middleware put `request_id`, `trace_id`, and `actor_uid` on the gin ctx and the `internal/service` context carrier, but no logger consumer emits them. `AccessLog` logs `request_id` + `actor` but omits `trace_id` even though the chain guarantees `c.Set("trace_id", …)` ran first. Logs cannot be stitched to OTLP spans or across HTTP/gRPC hops.

**Architecture constraint that shapes the design:** rule (d) bans `internal/outbox` (the relay — 12 sites) from importing `internal/service`, so the correlation read API cannot live in `internal/service`. It must live in `internal/pkg/logger`, which is importable everywhere and currently imports only `fmt` + zap.

## 3. API changes

### R1 — Correlation carrier in `internal/pkg/logger` (new file `internal/pkg/logger/ctx.go`)

```go
package logger

import (
    "context"

    "go.uber.org/zap"
)

// ctxKey is an unexported context key type so callers cannot collide with us
// (mirrors internal/service's discipline; independence is load-bearing).
type ctxKey int

const (
    traceIDKey ctxKey = iota
    requestIDKey
    actorKey
)

// WithTraceID returns a child context carrying the trace id (W3C traceparent's
// trace-id). HTTP Tracing middleware and the gRPC TracingInterceptor set this
// alongside service.WithTraceID.
func WithTraceID(ctx context.Context, traceID string) context.Context {
    return context.WithValue(ctx, traceIDKey, traceID)
}

// WithRequestID returns a child context carrying the request id (X-Request-ID).
func WithRequestID(ctx context.Context, requestID string) context.Context {
    return context.WithValue(ctx, requestIDKey, requestID)
}

// WithActor returns a child context carrying the authenticated actor uid.
func WithActor(ctx context.Context, actorUID string) context.Context {
    return context.WithValue(ctx, actorKey, actorUID)
}

// CtxFields returns zap fields for the non-empty carrier values in stable order
// trace_id, request_id, actor_uid. An empty (or empty-string) carrier yields a
// nil slice, which append() handles safely. Values are presence-based: an
// explicit empty string is treated as absent, matching service.ActorFrom.
func CtxFields(ctx context.Context) []zap.Field {
    fields := make([]zap.Field, 0, 3)
    if v, _ := ctx.Value(traceIDKey).(string); v != "" {
        fields = append(fields, zap.String("trace_id", v))
    }
    if v, _ := ctx.Value(requestIDKey).(string); v != "" {
        fields = append(fields, zap.String("request_id", v))
    }
    if v, _ := ctx.Value(actorKey).(string); v != "" {
        fields = append(fields, zap.String("actor_uid", v))
    }
    return fields
}
```

- Exported surface: exactly four functions. No `TraceIDFrom/RequestIDFrom/ActorFrom` exports (the package's read contract is `CtxFields`; middleware only needs setters). No change to `New` or `logger.go` itself.
- `CtxFields` reads only its own keys. It never reads `internal/service`'s carrier — that is the architectural point (rule (d) consumers cannot read the service carrier) and is pinned by a test.
- Preallocation `cap 3` keeps `append(logger.CtxFields(ctx), …)` from reallocating in the populated case; a nil slice for the empty case is append-safe.
- Size: ~45 lines; every function ≤ 4 lines, cyclomatic ≤ 3.

### R2 — HTTP middleware populates the carrier (existing write sites only)

- `internal/middleware/request_id.go` (after `ctx = service.WithRequestID(ctx, rid)`, before `c.Request = c.Request.WithContext(ctx)`):
  `ctx = logger.WithRequestID(ctx, rid)`
- `internal/middleware/tracing.go` (after `ctx = service.WithTraceID(ctx, sc.TraceID().String())`, before `c.Request = c.Request.WithContext(ctx)`):
  `ctx = logger.WithTraceID(ctx, sc.TraceID().String())`
  The `request_id` carrier value placed by `RequestID()` survives `propagator.Extract`/`tracer.Start` (child-context ops), so the carrier holds both fields after Tracing runs.
- `internal/middleware/auth.go` (both branches, restructured from inline calls):
  ```go
  c.Set("actor_uid", uid)
  if identity != nil {
      ctx = service.WithIdentity(c.Request.Context(), identity)
      ctx = logger.WithActor(ctx, uid)
      c.Request = c.Request.WithContext(ctx)
  } else {
      ctx = service.WithActor(c.Request.Context(), uid)
      ctx = logger.WithActor(ctx, uid)
      c.Request = c.Request.WithContext(ctx)
  }
  ```
  The auth site is required by the R1 contract: without it `actor_uid` could never be non-empty in production. Same one-line pattern, zero auth-behavior change.
- New import edge: `internal/middleware → internal/pkg/logger` (allowed — `internal/pkg/logger` is in no banned list; rule (d) bans infra → `internal/service`, not pkg).

### R3 — gRPC interceptors populate the carrier (existing write sites only)

- `internal/handler/grpc/server.go` `TracingInterceptor`:
  - after `ctx = service.WithRequestID(ctx, requestID)` (:158): `ctx = logger.WithRequestID(ctx, requestID)`
  - after `ctx = service.WithTraceID(ctx, spanContext.TraceID().String())` (:168): `ctx = logger.WithTraceID(ctx, spanContext.TraceID().String())`
- `internal/handler/grpc/server.go` `AuthInterceptor` (:267-268, restructured):
  ```go
  if identity != nil {
      ctx = service.WithIdentity(ctx, identity)
      ctx = logger.WithActor(ctx, uid) // uid is identity.UID
      return handler(ctx, req)
  }
  ctx = service.WithActor(ctx, uid)
  ctx = logger.WithActor(ctx, uid)
  return handler(ctx, req)
  ```
- Chain order guarantees Tracing's carrier values (request_id, trace_id) are already in the ctx `AuthInterceptor` receives, so the handler sees all three populated fields after Auth runs.
- New import edge: `internal/handler/grpc → internal/pkg/logger` (allowed; handler → repository ban unaffected).

### R4 — AccessLog emits `trace_id`

`internal/middleware/access_log.go`: add one field adjacent to `request_id` (:33):

```go
zap.String("trace_id", c.GetString("trace_id")),
```

- Reads the gin ctx (set by `Tracing()`), not the carrier — the chain guarantee `RequestID → Tracing → … → AccessLog` makes the field present exactly when the `X-Trace-ID` header is present. No dependency on R1 for correctness; R1/R2 still required for non-AccessLog line sites.
- No renames (`actor` stays `actor`, `request_id` stays `request_id`); no sampling/status behavior change.

## 4. Compatibility constraints

1. **`New` unchanged.** Signature, encoder config, level parsing, and behavior identical. Existing `TestNew_*` must pass unmodified.
2. **`service.With*` calls preserved.** Middleware/interceptors write both carriers (dual-carrier). The `service` carrier is the downstream propagation/authorization channel and must not be removed or reordered.
3. **No field renames or removals anywhere.** AccessLog gains exactly one field (`trace_id`). The `actor` (AccessLog) vs `actor_uid` (CtxFields) asymmetry is deliberate and preserved for log-pipeline compatibility.
4. **No new middleware, no chain reorder.** Carrier population happens inside existing write sites.
5. **No consumer wiring in this change.** The 23 relay/sweeper sites are untouched; `CtxFields` is additive API with zero callers until the documented follow-up.
6. **No config, no protos, no migrations, no new dependencies.** `context` (stdlib) + existing zap. `make check-migration-set`, `make check-proto-sync`, `make check-integration-compile` unaffected.
7. **Package remains dependency-free of `internal/`.** `internal/pkg/logger` must never import `internal/service` — this is the load-bearing direction (test-pinned) and the reason rule (d) consumers can use it.
8. **Field-name stability for `CtxFields`:** keys `trace_id`, `request_id`, `actor_uid` in that stable order; consumers that append to the returned slice get a valid line (nil-append-safe).

## 5. Failure modes

| Failure mode | Behavior | Mitigation / acceptance |
|---|---|---|
| Carrier empty (background drain/sweep loops, unsampled/disabled tracing, no auth) | `CtxFields` returns zero fields; AccessLog `trace_id` is `""` exactly when `X-Trace-ID` is absent | Documented, tested behavior of an empty carrier — not a defect. `TestCtxFields_emptyCtxReturnsNoFields`, `TestCtxFields_partialPopulation` pin it. |
| AccessLog vacuous pass (empty==empty) | Test would pass without trace_id ever being emitted | R2 acceptance asserts non-empty `X-Trace-ID` header *before* comparing line `trace_id` to it. |
| Carrier independence regression (someone routes `CtxFields` through `internal/service`) | Rule (d) consumers (relay) would silently lose correlation | `TestCtxFields_isolatedFromServiceCarrier` + rule (d) make the dependency direction load-bearing; the test documents why infra cannot read the service carrier. |
| Import cycle re-introduced (`internal/service` starts importing `internal/pkg/logger`) | Test-binary cycle for `logger_ctx_test.go` → fails `make test` | Compile-time failure is the guard; no runtime risk. |
| Field duplication (consumer appends `CtxFields` to a line that already carries one of the keys) | Duplicate JSON keys | Documented usage pattern: use `CtxFields` at sites that lack these keys; AccessLog does *not* use `CtxFields` (explicit fields + `trace_id`). |
| `grpc.SetHeader` silently dropped in direct interceptor invocation | R3 header-equality assertion vacuous/error if asserted against ctx | Use the existing bufconn server harness for the header-equality assertion; direct-invocation tests assert carrier contents only. |
| Dual-carrier divergence (middleware writes one carrier, forgets the other) | Downstream auth/propagation OK but log correlation missing | R2/R3 acceptance asserts the carrier contents end-to-end at handler scope; existing `service.*` assertions in middleware tests still pass. |
| AccessLog field-count churn breaks downstream parsers | Extra key | Only one field added, nothing renamed/removed; `TestAccessLog_withActorCarrier` pins existing `request_id`/`actor` keys. |

## 6. Migration steps

All steps are additive; the change is shippable atomically but is sequenced so each stage leaves `make test` green:

1. **Stage 1 — R1 carrier + unit tests.** Add `internal/pkg/logger/ctx.go` and `internal/pkg/logger/logger_ctx_test.go` (`TestCtxFields_*`, `TestNew_unchanged`). Zero production callers yet. Green: `make test && make lint`.
2. **Stage 2 — R2 + R4 HTTP surface + tests.** Edit `request_id.go`, `tracing.go`, `auth.go`, `access_log.go`; extend `TestTracing_preservesChainAndCorrelates`; add `TestAccessLog_withTraceID`, `TestAccessLog_withActorCarrier` in `middleware_extra_test.go`. Green: `make test`.
3. **Stage 3 — R3 gRPC surface + tests.** Edit `internal/handler/grpc/server.go`; add `TestTracingInterceptor_populatesLoggerCarrier` (direct invocation) and `TestAuthInterceptor_populatesLoggerCarrier` (direct invocation); add bufconn-based `TestGRPC_traceIDHeaderMatchesCarrier` for the R3 header-equality clause (transport-observable only).
4. **Stage 4 — full gate.** `make test && make lint && make lint-strict && make check-architecture && make check-file-sizes && make build`.
5. **Documented follow-up (out of scope for this change).** Consumer adoption pattern for the 23 relay/sweeper sites:
   `r.log.Info("outbox drain", append(logger.CtxFields(ctx), zap.String("event_id", …))…)`.
   *Caveat to document at adoption time:* relay drain and GC sweep loops currently run on background/non-request ctx, so `CtxFields` yields zero fields there until a request ctx (or an explicitly propagated `operation_id`/`request_id`) is threaded into the loop — empty carrier output is the tested, documented behavior, not a bug. Threading request ctx through those loops is a separate design decision (queue-delivery path in the outbox dispatcher already carries ctx) and must not be folded into this change.

## 7. Testable acceptance mapping

| Req | Acceptance test (location) | Asserts |
|---|---|---|
| R1 | `TestCtxFields_emptyCtxReturnsNoFields` (`internal/pkg/logger/logger_ctx_test.go`) | `CtxFields(context.Background())` length 0 |
| R1 | `TestCtxFields_populatedCtxReturnsExactlyThreeFields` | exactly 3 fields; keys exactly `["trace_id","request_id","actor_uid"]` in order; values match; `append(fields, zap.String("x","y"))` yields a 4-field line (nil-slice not poisoned) |
| R1 | `TestCtxFields_partialPopulation` | only trace_id → 1; only request_id → 1; only actor → 1; empty-string value → 0 |
| R1 | `TestCtxFields_isolatedFromServiceCarrier` | populating only `service.With*` → `CtxFields` = 0; populating only logger carrier → `service.*From` empty (pins why the carrier cannot live in `internal/service`) |
| R1 | `TestNew_unchanged` | `New("info","json")` still non-nil |
| R2 | extend `TestTracing_preservesChainAndCorrelates` (`tracing_test.go:18`) | existing assertions unchanged; handler sees exactly 2 `CtxFields` fields in order `trace_id, request_id`; `request_id` non-empty; `trace_id` == `X-Trace-ID` header |
| R2/R4 | `TestAccessLog_withTraceID` (`middleware_extra_test.go`, beside :283/296/306) | chain `RequestID → Tracing → AccessLog(capturing JSON core)`; (a) `X-Trace-ID` header non-empty **first**; (b) line has `trace_id` key; (c) line `trace_id` == header |
| R2 | `TestAccessLog_withActorCarrier` | chain `RequestID → Tracing → emulated-auth (c.Set("actor_uid","u1") + service.WithActor + logger.WithActor) → AccessLog(capturing)`; line keeps `request_id`/`actor` keys, gains `trace_id` |
| R2 | existing `TestAccessLog_passThroughAndNoOp`, `TestAccessLog_doesNotPanicWithoutContext`, `TestAccessLog_withRequestID` | remain green unmodified |
| R3 | `TestTracingInterceptor_populatesLoggerCarrier` (`internal/handler/grpc/server_test.go` or sibling) | incoming md `x-request-id` + real in-memory tracer provider; handler captures exactly `request_id` + `trace_id`; `request_id` == md value; `trace_id` non-empty |
| R3 | `TestAuthInterceptor_populatesLoggerCarrier` | stub `TokenValidator` uid `"u"`; handler captures `actor_uid == "u"` |
| R3 | `TestGRPC_traceIDHeaderMatchesCarrier` (bufconn, pattern :107-137) | full server: response `x-trace-id` header == handler-carrier `trace_id` (header only observable through real transport) |

**Gate mapping (Definition of Done):** `make test` (all above + unchanged suites) → `make lint` → `make lint-strict` (new funcs ≤ 50 lines, cyclomatic ≤ 10; actuals ≤ 4 lines / ≤ 3) → `make check-architecture` (only new edges middleware/grpc → `internal/pkg/logger`; no infra → service edge) → `make check-file-sizes` (logger 28→~75 lines; server.go 438→~444) → `make build`.

## 8. Out of scope (explicit non-goals, preserved)

- Direction 2 (encoding whitelist / TTY color handling) — not implemented.
- Direction 3 (`WithWriter(io.Writer)` option, JSON output-contract pinning, sampling assertions) — not implemented. Test capture uses a test-local `zapcore.NewCore(...)` sink; no production API added.
- Wiring `CtxFields` into the 23 relay/sweeper sites — follow-up pattern only (§6 Stage 5), with the documented empty-carrier semantics for background loops.
- Renaming AccessLog `actor` → `actor_uid` or changing any existing log field.
- Any change to `internal/service`'s ctx API, `cmd/server/main.go`, `internal/app` wiring, or the middleware chain order.
