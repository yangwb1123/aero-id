# Design — Relay poison-event panic hardening: recover in `drain`, `MarkFailed` so panics dead-letter instead of crash-looping

- **Date:** 2026-08-16
- **Module:** `internal/outbox` (live transactional outbox — `internal/app/outbox.go` composition root, `Relay` drain loop)
- **Direction:** approved, verification_status verified (source: `docs/auto/runs/relay-poison-event-panic-hardening-recover-in-dr-21f2d1f0/`)
- **Status:** proposed (no repository changes in this document; implement per the migration steps below)

---

## 1. Verification verdict

Every load-bearing claim in the incoming requirements was re-verified against the current tree. **All claims confirmed.** One test-design gap was found (Section 6): the existing `fakeRepo.ClaimBatch` does not model the dead-letter claimability exclusion, so AC-1/AC-2's "dead-lettered event no longer claimable" assertion needs a one-line fake extension to be a genuine assertion rather than an incidental pass through the backoff window.

| Claim (evidence) | Verification |
|---|---|
| `Relay.drain` at `internal/outbox/relay.go:113`, zero `recover()` in the whole module; `grep recover()` in `internal/outbox/` + `internal/service/` → no matches | ✅ Confirmed. `recover()` appears only in `internal/handler/grpc/server.go`, `internal/connector/auditgovernance/oauth.go`, `internal/middleware/recovery.go`, `internal/middleware/idempotency.go` — none on the outbox path. |
| Bare `r.handler.Handle(ctx, e)` at relay.go:191, `err != nil` guards the whole failure path | ✅ Confirmed (line 191 exactly). |
| `repo.MarkFailed(ctx, ce.Claim, e.RetryCount+1, r.maxRetries, now)` at relay.go:205, only reachable via a returned error | ✅ Confirmed. A panic skips log, `ObserveOutboxFailed`, `MarkFailed`, `dropHeld`, `failed++` entirely and escapes `drain`. |
| `Start` ticker goroutine at relay.go:256/259 calls `r.drain(ctx)` directly with no recovery (including drain-on-stop at :262) | ✅ Confirmed. |
| `relay_test.go` has 8 `TestRelay_*` functions, no panicking-handler case | ✅ Confirmed: `DrainPublishesEvents`, `HandlerErrorLeavesEventUnpublished`, `MetricsSinkRecordsPublishedFailedAndLag`, `NilMetricsSinkIsSafe`, `DrainOnStop`, `BackstopRelease`, `RenewsClaimsDuringLongDrain`, `HandlerErrorClearsClaimAndRetries`. |
| Handler implementations have no `recover()`: `WebhookHandler.Handle` (webhook.go:59), `Chain.Handle` (chain.go:15 — plain loop), `NotificationDispatcher.Handle` (notification.go:226) | ✅ Confirmed. |
| Dead-letter only via `MarkFailed` for returned errors; `ClaimBatch` contract = "unpublished, non-dead-letter events" (outbox.go:50–55) | ✅ Confirmed. `Event.RetryCount/LastRetryAt/DeadLetter` (outbox.go:20–31); `MarkFailed` doc: "when the new retry_count reaches maxRetries the event becomes a dead letter" (:78–84). |
| Fail-closed precedent `internal/event/nats.go` `handleMessage`: malformed envelope → `msg.Term()` (dead-letter, never replayed), transient → `Nak()` | ✅ Confirmed (nats.go:62–91). |
| Live wiring `internal/app/outbox.go:58–63`: `relayHandler = outbox.NewChain(dispatcher, outbox.NewWebhookHandler(...))`, then `outbox.NewRelay(...)`; the only production `NewRelay` call site | ✅ Confirmed. `NewRelay` has no other callers outside tests. |
| `relay.go` is 273 lines (≤ 500 gate); `backoff(1)` = 5s | ✅ Confirmed (273 lines; `5s * 2^0`). |
| Package doc/`Relay` doc claim at-least-once delivery | ✅ Confirmed (outbox.go:97–104, 109–121). |

**Defect confirmed:** a handler panic (e.g. a poison event reaching `NotificationDispatcher.Handle` or `WebhookHandler.Handle` through the `Chain` loop) is not a returned error, so it bypasses the entire failure path, escapes `drain` and the `Start` goroutine, and crashes the process. On restart the same event is re-claimed and re-handled, panic again — an indefinite crash-loop with the poison event never reaching `DeadLetter`. This violates AGENTS.md's event/outbox invariant ("Consumers reject malformed or unsupported versions to a dead-letter/quarantine path; they do not acknowledge poison events as successfully applied") and the module's own at-least-once claim.

---

## 2. Problem statement

`Relay.drain` treats only returned errors as failures. A panic inside `r.handler.Handle`:

1. skips the log + `ObserveOutboxFailed` (failure invisible in the gauge — the process death is the only signal),
2. skips `MarkFailed` (no `retry_count` advance, no `last_retry_at`, no backoff, no `DeadLetter` transition),
3. skips `dropHeld` (the claim dies with the process; SQL lease expiry eventually re-claims, but only after a full process restart and lease window),
4. escapes `drain`, escapes the `Start` goroutine (relay.go:259/262), and terminates the process.

Restart → `ClaimBatch` returns the same unpublished event → handler panics again → crash-loop. The dead-letter state machine engages only for `err != nil` at relay.go:191; a panic is not a returned error. There is no `recover()` anywhere in `internal/outbox/` or `internal/service/`, and no panicking-handler test guards the relay.

---

## 3. Design overview

One behavioral change, no schema/interface/transport changes, one regression test.

1. **`Relay.handle` helper** — wraps the single `r.handler.Handle` call in a `defer`-based `recover()`. A recovered panic is converted into a normal handler error (with `event_id`, `event_type`, panic value, and stack logged at error level) and returned. Routing panics into the error path means the existing failure branch — warn log, `ObserveOutboxFailed`, `MarkFailed(retryCount+1, maxRetries, now)`, `dropHeld`, `failed++`, loop continuation — runs **verbatim** for panics, so error and panic handling cannot drift (R6 holds structurally).
2. **`drain` call site** — replace the bare `r.handler.Handle(ctx, e)` at relay.go:191 with `r.handle(ctx, e)`. Everything else in `drain` is untouched.
3. **No `recover()` on the `Start` goroutine** — recovery is a property of the drain loop, not the goroutine wrapper (R5). A panic that still escapes `drain` (repo/infrastructure panic) is a bug that must surface in tests/CI, not be swallowed.
4. **Regression test `TestRelay_HandlerPanicDoesNotCrashLoop`** in `internal/outbox/relay_test.go` using the existing `fakeRepo`/`recordingSink` plus a one-line fake extension (Section 6) so the dead-letter freeze is asserted against the real `ClaimBatch` contract.

Scope boundaries (from the direction, preserved): recover **only** the `handler.Handle` call; repo-call and renewal panics remain unrecovered infrastructure errors; no `recover()` added to `WebhookHandler`, `Chain`, or `NotificationDispatcher`; no changes to `Repository`, migrations, dead-letter SQL, metrics interfaces, or the `Start` goroutine structure.

---

## 4. API changes

### 4.1 `internal/outbox/relay.go` — the only production-code change

```go
// handle invokes the handler, converting a handler panic into a handler error
// so the poison event follows the MarkFailed/dead-letter path instead of
// crashing the drain goroutine (crash-loop hardening). Infrastructure panics
// (repo calls, renewal) are intentionally not recovered.
func (r *Relay) handle(ctx context.Context, e *Event) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("outbox: handler panic; treating as failure",
				zap.String("event_id", e.ID.String()),
				zap.String("event_type", e.EventType),
				zap.Any("panic", rec),
				zap.Stack("stack"))
			err = fmt.Errorf("outbox: handler panic: %v", rec)
		}
	}()
	return r.handler.Handle(ctx, e)
}
```

Call-site change (relay.go:191):

```go
-		if err := r.handler.Handle(ctx, e); err != nil {
+		if err := r.handle(ctx, e); err != nil {
```

Required import addition: `"fmt"` (current imports: `context`, `errors`, `math`, `time`, `go.uber.org/zap`).

Shape constraints:
- Helper is ~14 lines (≤ 50), cyclomatic ~1 (≤ 10); `drain` keeps its existing `//nolint:cyclop,funlen` (no new nolint needed — the helper is small and simple).
- `relay.go` grows 273 → ~288 lines, well under the 500-line gate.
- `r.log` is never nil (`NewRelay` defaults to `zap.NewNop()`), so the recovery log cannot nil-panic.

### 4.2 Public surface — unchanged

No changes to: `Repository` interface, `Handler`/`HandlerFunc`, `MetricsSink`, `NewRelay` signature, `Event`/`Claim`/`ClaimedEvent`, `backoff` math, `ClaimBatch`/`RenewClaims`/`Release`/`MarkPublished`/`MarkFailed`/`PurgeDeadLetters` semantics. No migrations, no protobuf, no config keys, no HTTP/gRPC routes, no new error codes (panic failures surface as the existing handler-failure warn log + `ObserveOutboxFailed`, which is metric-only). `make check-composition-root` is unaffected (no new composition surface; the only `NewRelay` call site in `internal/app/outbox.go:63` keeps working).

---

## 5. Compatibility constraints

- **Returned-error behavior is byte-for-byte preserved** (R6): the `err != nil` branch is untouched; the panic path is funneled into that same branch, so error and panic handling cannot drift.
- **At-least-once preserved**: a recovered panic leaves the event unpublished with a cleared claim; the next pass re-claims and re-handles it once due (backoff), exactly like a returned error. Panics now get the same retry/backoff/dead-letter cadence as handler errors.
- **Dead-letter contract preserved**: panics reuse the existing `MarkFailed` machinery; the SQL `ClaimBatch` "non-dead-letter" exclusion then makes a dead-lettered poison event permanently unclaimable, matching the `internal/event/nats.go` `Term()`/DLQ precedent.
- **No observability contract change**: `MetricsSink` is untouched; recovered panics increment `failed` per event type via the existing `ObserveOutboxFailed` call. The only new signal is the error-level log line carrying the panic value + stack (which the returned-error path already logs at warn with `zap.Error`).
- **Per-event isolation**: recovery is scoped to each `handler.Handle` invocation, so one poison event cannot abort the rest of the batch; non-poison events in the same pass still `MarkPublished`.
- **`Start` goroutine unchanged**: no recovery added at relay.go:256–269; the drain-on-stop path (:262) is covered by the same drain-level guarantee (R5).
- **Gate compatibility**: no migration-set, proto-sync, audit-manifest, audit-swallow, load-scenario, or integration-compile surface is touched. `make check-file-sizes` stays green (relay.go ≈ 288 lines).

---

## 6. Failure modes

| Failure mode | Behavior after fix | Classification / handling |
|---|---|---|
| Poison event panics every time (deterministic handler bug, malformed payload) | First pass: panic → recovered → `MarkFailed` (retry_count 1, `last_retry_at` now, backoff 5s). Subsequent passes back off and re-panic until `retry_count >= maxRetries` → `DeadLetter = true` → `ClaimBatch` never returns it → drain returns 0, handler not invoked. Process never crashes. | Intended target behavior (R1–R3). Matches nats `Term()`/DLQ semantics for poison. |
| Panic value with a panicking `String()` method reaches `fmt.Errorf("%v", rec)` inside the recovery defer | A second panic inside the deferred recovery is itself unrecoverable → escapes `drain` as before. | Accepted edge; a `%v`/`%+v` panic in a user type's `Stringer` is a handler bug of the same class. Documented limitation; do not widen recovery to catch it (would swallow the re-panic silently). |
| Stack overflow / fatal runtime errors (not ordinary panics) | Not recoverable by `recover()` at all — process dies. | Out of scope; these are process-level failures, not per-event failures. |
| Panic in repo calls (`ClaimBatch`, `RenewClaims`, `MarkFailed`, `MarkPublished`, `Release`) or renewal path | Still escapes `drain` → surfaces in CI/tests. | Deliberate (R5, scope constraint): infrastructure panics are programming bugs, not poison events. |
| Panic inside `ObserveOutboxFailed` / metrics sink | Not recovered (recovery wraps only `handler.Handle`). | Out of scope; sink bugs surface as before. |
| `panic(nil)` (Go 1.21+ raises `*runtime.PanicNilError`) | `recover()` returns non-nil `PanicNilError` → normal failure path. | Covered; Go toolchain is 1.26 (AGENTS.md). |
| Handler panics after partially performing a side effect | Same as a returned error after partial effect: at-least-once re-delivery; downstream consumers must be idempotent (existing contract, AGENTS.md). | No new risk introduced; identical to today's returned-error semantics. |
| Backoff window race: panic → `MarkFailed` sets `last_retry_at`; a concurrent drain skips/releases inside the window | Existing D4/H5 backstop behavior, unchanged. | Verified against existing `BackstopRelease` test. |

---

## 7. Migration steps

No data or schema migration. Implementation order:

1. **`internal/outbox/relay.go`**: add `"fmt"` to imports; add the `handle` helper (Section 4.1) immediately above `drain`; change relay.go:191 from `r.handler.Handle(ctx, e)` to `r.handle(ctx, e)`. No other edits to `drain`.
2. **`internal/outbox/relay_test.go`**: add the one-line fake extension (Section 6 of this doc's acceptance mapping — skip `e.DeadLetter` rows in `fakeRepo.ClaimBatch` to mirror the SQL contract) and the new `TestRelay_HandlerPanicDoesNotCrashLoop` test (Section 8). Test-only change; `fakeRepo` is package-local and unused in production.
3. **Red/green proof**: before applying step 1, run `go test -run TestRelay_HandlerPanicDoesNotCrashLoop ./internal/outbox` — must fail (panic escapes `drain`; the test harness reports the panic). After step 1, it passes. This is the regression proof.
4. Run the verification gates (Section 9).
5. Commit as a single unit; update no docs beyond this design (module doc comments already state at-least-once and now become true for panicking handlers — no prose change required; the `Relay` doc comment already describes the retry/backoff/dead-letter contract that this fix makes panics obey).

Rollback: revert the single-file change (relay.go) plus the test additions; no migration or schema state is touched, so rollback is a plain revert.

---

## 8. Acceptance mapping (testable)

New test `TestRelay_HandlerPanicDoesNotCrashLoop` in `internal/outbox/relay_test.go`, using `fakeRepo`, `recordingSink`, and a `HandlerFunc` that panics for one event ID and succeeds for another.

Setup: `good`/`bad` events, `maxRetries = 2`, `relay := NewRelay(repo, handler, 2, nil, sink)` where `handler` returns nil for `good` and `panic("poison payload")` for `bad`.

| Acceptance criterion | Testable assertion |
|---|---|
| **AC-1 (R1, R3, R4):** drain returns; batch integrity preserved; subsequent events publish; panic observable via metrics | First `relay.drain(ctx)` returns `n == 1`; `repo.isPublished(good.ID)` true; `repo.isPublished(bad.ID)` false; `sink.failed[bad.EventType] == 1` (metrics parity with the error path); `repo.claimsHeld() == 0` (no claim left held). |
| **AC-2 (R2):** panic follows `MarkFailed` → backoff → dead-letter; dead-lettered event no longer claimable | After first drain: `bad.RetryCount == 1`, `bad.DeadLetter == false`. Backdate `bad.LastRetryAt` (e.g. `time.Now().Add(-time.Hour)`), drain again (handler panics again) → `RetryCount == 2`, `DeadLetter == true`. Next drain returns `0` and the handler invocation counter is frozen. **Fake extension required:** `fakeRepo.ClaimBatch` must skip `e.DeadLetter` rows (one-line `if e.DeadLetter { continue }` next to the existing `published` skip) — this is what makes the freeze a genuine dead-letter-contract assertion (SQL `ClaimBatch` = "unpublished, non-dead-letter events") rather than an incidental pass via the 10s `backoff(2)` window. With the current fake, the immediate post-dead-letter drain passes only because `last_retry_at` is fresh; the extension makes it robust (freeze survives backdating + extra drains). |
| **AC-3 (R1, R5):** second drain does not re-panic; `Start`/`stop` never crash-loop | Immediately after AC-1's first drain, a second `relay.drain(ctx)` completes without panic: `bad` is inside its `backoff(1)` = 5s window → claimed, skipped, `Release`d, handler **not** invoked, returns `0`. Then `stop := relay.Start(24*time.Hour); stop()` — the drain-on-stop path runs without panic and without re-delivering the poison event (binary survival is the assertion). |
| **Negative check (regression proof):** before the fix the test fails | `go test -run TestRelay_HandlerPanicDoesNotCrashLoop ./internal/outbox` must fail on the pre-fix tree (panic escapes `drain`; test harness reports `panic: poison payload`) and pass after the fix. |

Expected pass counts (with the fake extension): drain 1 → `n=1` (good published, bad MarkFailed); drain 2 (backoff skip) → `n=0`, no handler call; backdate + drain 3 → `n=0`, panic → MarkFailed → dead-letter; drain 4 → `n=0`, no handler call (dead-letter excluded from `ClaimBatch`). Assert handler call count and `claimsHeld()==0` at each step.

---

## 9. Verification gates

```bash
make test          # relay suite (incl. new TestRelay_HandlerPanicDoesNotCrashLoop) + full tree
make lint          # go vet
make lint-strict   # golangci-lint: new helper ≤ 50 lines, cyclop ≤ 10
make check-file-sizes        # relay.go ≈ 288 lines < 500 (test files exempt)
make check-integration-compile
# full done-gate:
make test && make lint && make lint-strict && make check-file-sizes && \
make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && \
make check-load-scenarios && make check-composition-root && make check-proto-sync && \
make check-integration-compile && make build
```

`make test-integration` is not required (no SQL/storage/connector change) but must remain green if run. Pre-fix red/green proof: the new test fails on the current tree and passes after the relay.go change.

---

## 10. Out of scope (explicitly preserved from the direction)

- No change to the `Repository` interface, migrations, or dead-letter SQL.
- No new dead-letter/quarantine mechanism, poison-detection heuristics, or schema changes — panics reuse the existing `MarkFailed`/`DeadLetter` machinery.
- No `recover()` in `WebhookHandler`, `Chain`, or `NotificationDispatcher`; no handler-side hardening.
- No change to the `Start` goroutine structure, ticker cadence, or drain-on-stop semantics.
- No metrics/interface additions (`MetricsSink` unchanged; panic failures reuse `ObserveOutboxFailed`).
