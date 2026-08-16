# Design — Fix erase-path transient read failure misclassified as permanent invalid

| | |
|---|---|
| Date | 2026-08-15 |
| Module | `internal/connector` (behavior lives in the shared `JSONSummaryConnector`; `internal/connector/aeroim/aeroim.go` is a 3-line factory over it) |
| Direction | Split `decodeEraseResult`'s two failure classes: body **read failure** → `ErrorUnavailable` + `Temporary: true` + `source.response_read_failed`; **oversized** body → `ErrorInvalid` + non-temporary + `source.response_too_large` |
| Effort | 1 (one decoder, ~3 lines, plus one regression test file) |
| Status | Proposed — no repository code modified by this document |

## 0. Verification verdict on the evidence

All load-bearing claims in the source specification were re-checked against the current tree before
designing. Every citation resolves:

- **Bug present**: `decodeEraseResult` (`internal/connector/http_summary_request.go:96-100`) collapses
  `io.ReadAll` errors and oversized bodies into a single `ErrorInvalid` non-temporary
  `source.response_too_large` (with `Cause` set even in the oversized branch).
- **Sibling decoders split correctly**: `decodeSummary` (`internal/connector/http_summary.go:262-269`)
  and `decodeCommandResult` (`internal/connector/http_operation.go:85-92`) both classify read errors as
  `ErrorUnavailable`/`Temporary: true`/`source.response_read_failed` and oversized bodies as
  `ErrorInvalid`/`source.response_too_large`. The proposed change makes `decodeEraseResult` byte-for-byte
  isomorphic to `decodeCommandResult`'s two-class split.
- **Downstream classification matches the claims**:
  - `operationRunStatus` (`internal/service/operation_worker.go:88-96`) maps `ErrorInvalid` → `StepFailed`
    (`operation.step_failed`) and `ErrorTimeout`/`ErrorUnavailable`/`ErrorUnknown` → `StepUnknown`
    (`operation.unknown_outcome`).
  - `connectorOperationRunner.erase` (`internal/service/operation_runner.go:115-133`) returns the connector
    error unchanged (no wrapping that would defeat `connector.IsRemote`).
  - Operation reconcile: `operationService.Reconcile` (`internal/service/operation.go:185-199`) →
    `connectorOperationRunner.Reconcile` (`operation_runner.go:75-113`), which calls
    `OperationStatusConnector.OperationStatus` with idempotency key `operation.ID:step.ID`
    (`operation_runner.go:84, 93-99`). The `OperationStatusConnector` seam exists
    (`connector.go:60-70`, `var _ OperationStatusConnector = (*JSONSummaryConnector)(nil)` at
    `http_operation.go:125`) and its doc comment states the "response was lost" contract.
  - Sync-job path: `syncJobService.executeErase` (`internal/service/sync_job.go` ~404-418) counts
    `failures++` for any call error and sets `unknown = true` only when `isUnknownRemote(callErr)`
    (`internal/service/sync_requests.go:30-37`, true only for `ErrorTimeout`/`ErrorUnavailable`/`ErrorUnknown`).
    Terminal status selection (`sync_job.go:429-435`): `unknown` → `SyncJobUnknown` /
    `erase.unknown_outcome`; else `failures > 0` → `SyncJobPartiallyComplete` / `erase.partial_failure`.
    `syncJobService.Reconcile` (`internal/service/sync_reconcile_job.go:14-37`) claims only
    `SyncJobUnknown` jobs and re-drives erase sources through `OperationStatus`.
  - Circuit breaker: `finish` (`internal/connector/http_policy.go:37-51`) counts only
    `Temporary` remote errors toward opening; the current misclassification resets `consecutiveErr`.
  - Retry semantics: `doHTTP` (`http_policy.go:39+`) retries only transport errors and 429/5xx; a 200
    response with a truncated body is returned to the decoder on the first attempt, so the decoder's
    classification is final and reaches the service layer unchanged.
- **Seam is shipped but opt-in**: `internal/app/connectors.go:41-42` wires `ErasePath`/`OperationStatusPath`
  from config; `configs/config.yaml:7,23,39` keep `erase_path`/`status_path` empty by default.
- **Test infrastructure exists**: `newTestConnector` (`internal/connector/fault_injection_test.go:173-185`,
  defaults `SourceAeroIM`, `MaxBodyBytes` 1 MiB, `SummaryPath /summary`) and the `errors.As`/`*RemoteError`
  assertion pattern established by `contract_test.go` and `http_summary_test.go:38-67`.

One minor citation drift: the evidence pinned `operationRunStatus` at `operation_worker.go:90-96`; it lives
at 88-96. `sync_job.go:400-406` maps to ~404-418 in the current file. Neither shifts the meaning.

**Verdict**: Verified. The evidence is accurate; the requirements are minimal, correct, and do not expand
scope.

## 1. Problem statement

`JSONSummaryConnector.decodeEraseResult` treats two distinct failure classes identically:

```go
data, err := io.ReadAll(io.LimitReader(body, c.cfg.MaxBodyBytes+1))
if err != nil || int64(len(data)) > c.cfg.MaxBodyBytes {
    return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_too_large", Cause: err}
}
```

A WAN/partition-truncated erase response (the source may already have applied the erase) is classified as
`ErrorInvalid` — permanent, non-temporary. Consequences, each verified:

1. **Operation worker** maps `ErrorInvalid` → `StepFailed`, which aborts the operation and runs
   compensation. The mandated reconcile-by-idempotency path (`operation.unknown_outcome` →
   `Reconcile` → `OperationStatus` keyed by `operation.ID:step.ID`) is bypassed for exactly the case the
   `OperationStatusConnector` seam was designed for: "a command whose response was lost after the source
   may already have applied it."
2. **Sync job** maps the error via `isUnknownRemote`; `ErrorInvalid` yields `failures++` → terminal
   `SyncJobPartiallyComplete` / `erase.partial_failure` with **no reconciliation**. Correct behavior is
   `SyncJobUnknown` / `erase.unknown_outcome`, which `syncJobService.Reconcile` claims and re-drives.
3. **Circuit breaker** never opens: `finish` resets `consecutiveErr` for non-`Temporary` errors, so repeated
   truncated responses never trip the breaker — inconsistent with the sibling paths.

The two sibling decoders already split the classes correctly, so this is a local defect, not a design gap.

## 2. API changes

### 2.1 Error classification contract (the only public-facing change)

`RemoteError` (`internal/connector/connector.go:96-108`, `IsRemote` at 124) is the contract surface. After
the change, `decodeEraseResult` returns exactly:

| Condition | `Kind` | `Temporary` | `Code` | `Cause` |
|---|---|---|---|---|
| `io.ReadAll` error (truncated/closed body, transport abort mid-body) | `ErrorUnavailable` | `true` | `source.response_read_failed` | wrapped read error |
| `len(data) > MaxBodyBytes` | `ErrorInvalid` | `false` | `source.response_too_large` | nil |
| malformed JSON | `ErrorInvalid` | `false` | `source.response_invalid` | wrapped unmarshal error |
| empty body | — | — | — | `&CommandResult{Status: "accepted"}` (default-accept) |

This is identical to the `decodeCommandResult` split (`http_operation.go:85-92`) and `decodeSummary` split
(`http_summary.go:262-269`). No other API changes: no new methods, no interface changes, no wire-format
changes, no config changes.

### 2.2 Requested change (for the implementer; not applied by this document)

`internal/connector/http_summary_request.go:96-100`:

```go
data, err := io.ReadAll(io.LimitReader(body, c.cfg.MaxBodyBytes+1))
if err != nil {
    return nil, &RemoteError{Source: c.source, Kind: ErrorUnavailable, Code: "source.response_read_failed", Temporary: true, Cause: err}
}
if int64(len(data)) > c.cfg.MaxBodyBytes {
    return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_too_large"}
}
```

The rest of `decodeEraseResult` — empty-body default-accept, `SourceOperationID` fallback to
`OperationID`, empty `Status` → `"accepted"`, **no** `operation_status_missing` rejection — stays
byte-identical. Do not unify with `decodeCommandResult`; the erase decoder intentionally lacks the
status-presence rejection and does not lowercase status.

## 3. Compatibility constraints

- **No behavioral change for non-error paths**: well-formed erase responses (including the empty-body
  default-accept) decode identically. Existing tests
  `TestJSONSummaryConnectorEraseUsesIdempotency` and `TestConnector_PropagatesBearerTokenOnEverySurface`
  must stay green unchanged.
- **Oversized bodies keep their classification**: `ErrorInvalid`/non-temporary/`source.response_too_large`
  is pinned so the fix cannot degrade an explicit protocol violation into a transient retry loop.
- **Service-layer contracts are untouched**: `operationRunStatus`, `isUnknownRemote`, `Reconcile`,
  `sync_reconcile_job.go`, breaker `finish`, and `doHTTP` need zero edits; they are already correct for
  `ErrorUnavailable`/`Temporary`. This is a deliberate constraint — the fix must not introduce retries at
  the connector level (reconciliation is the designed mechanism) and must not reclassify transport errors.
- **Seam/config unchanged**: `erase_path`/`status_path` remain opt-in empty in `configs/config.yaml`.
  The fix only makes the shipped seam correct when enabled.
- **File-size gate**: `http_summary_request.go` is 190 lines today; the change adds ~3 lines (~193),
  well under the 500-line limit and below the 400-line split threshold.
- **No audit/proto/migration/composition-root surface** is touched; the audit gates
  (`check-audit-swallow`, `check-audit-manifest`) are unaffected.

## 4. Failure modes and mitigations

| Failure mode | Behavior after fix | Mitigation |
|---|---|---|
| WAN/partition truncates a 200 erase response (source already applied the erase) | `ErrorUnavailable`, `Temporary: true` → operation `StepUnknown`; sync job `SyncJobUnknown` | Idempotency-keyed `OperationStatus` reconcile (`operation.ID:step.ID`) resolves the actual source outcome; never guessed as failed |
| Source closes connection mid-body with correct `Content-Length` | `io.ErrUnexpectedEOF` from `io.ReadAll` → same transient classification | Same reconcile path; retry budget (`doHTTP`) is unaffected because body-decode failures are not retried — classification is final and reaches the service layer |
| Oversized erase body (protocol violation / misconfigured source) | `ErrorInvalid`, non-temporary — unchanged | Permanent failure surfaces as `StepFailed`/`partial_failure`; no transient retry loop, breaker not tripped by protocol errors |
| Repeated transient truncations | `Temporary: true` now counts toward the breaker via `finish` | Consistent with summary/command-result paths; breaker opens after 5 consecutive transient errors and parks traffic for 10s |
| Malformed JSON | `ErrorInvalid`, `source.response_invalid` — unchanged | Permanent; source contract bug, not a network condition |
| `ErrorUnavailable` propagates but `OperationStatus` also fails | `Reconcile` returns `StepUnknown` with the error; step remains unknown, retried by the reconcile loop | Durable `unknown` state persists; no false terminal state; idempotent retry is safe |
| Home region unavailable during reconcile | `operationService.Reconcile`/`syncJobService.Reconcile` require home region (verified `sync_reconcile_job.go:14-37`); job stays `Unknown`, claim expires after 5 min | Reconcile re-claimable after lease expiry; unknown is a stable, non-terminal state |

Known residual risk (unchanged by this fix, documented for completeness): a body read failure and a body
that is *exactly* `MaxBodyBytes` are indistinguishable at the boundary — the limit reader never signals
overflow, so an exact-size body decodes normally (unchanged semantics, no action needed).

## 5. Migration steps

This is a code fix with no schema, config, or wire-format migration. The migration is purely behavioral:

1. **Implement** the two-class split in `decodeEraseResult` (Section 2.2). ~3 lines.
2. **Add regression tests** (Section 6) in `internal/connector/http_summary_test.go` (or a sibling
   `http_summary_request_test.go`). Unit tests beside the code, no build tags, no Docker, no network.
3. **Run the done gate** (Section 7).
4. **No rollout sequencing required**: because the fix only changes the classification of a class of errors
   that previously produced incorrect terminal states, any deployment order is safe. Sources that return
   oversized bodies keep their permanent-invalid classification; sources whose responses are truncated now
   get the correct transient/unknown treatment.
5. **Observability note (no code change)**: once deployed, `source.response_read_failed` on the erase path
   becomes visible to metrics/circuit state; operators should expect erase-path unknown outcomes to appear
   in reconcile workflows where previously they were silently terminal.

## 6. Testable acceptance mapping

All tests live in `internal/connector`, unit-level, deterministic `httptest` only. Shared helper
`newTestConnector` (`fault_injection_test.go:173-185`) defaults `SourceAeroIM`, `MaxBodyBytes` 1 MiB,
`SummaryPath /summary`, and preserves a caller-supplied `ErasePath`/`MaxBodyBytes`/`MaxAttempts`.

| ID | Requirement | Test | Pass condition | Red/green |
|---|---|---|---|---|
| ACC-1 | REQ-1, REQ-5a | `TestConnector_EraseTruncatedBodyIsTemporaryReadFailure` | `errors.As(err, &*RemoteError)`; `Kind == ErrorUnavailable && Temporary && Code == "source.response_read_failed"`; (strengthening: `Cause != nil`) | **Red on current tree** (fails with `ErrorInvalid`), green after fix |
| ACC-2 | REQ-2, REQ-5b | `TestConnector_EraseRejectsOversizedBody` | `Kind == ErrorInvalid && !Temporary && Code == "source.response_too_large"` | Green before and after (pins the split) |
| ACC-3 | REQ-3 | Existing `TestJSONSummaryConnectorEraseUsesIdempotency` (`http_summary_test.go:38-57`) and `TestConnector_PropagatesBearerTokenOnEverySurface` (`contract_test.go:25-88`) | Stay green unchanged | Green before and after |
| ACC-4 | REQ-4 | `make check-integration-compile` | New test file has no build tags and compiles under both tag sets | Green |
| ACC-5 | REQ-5a mechanics | — | Server declares `Content-Length: 1000`, writes 19 bytes, handler returns → client read yields `io.ErrUnexpectedEOF`; `MaxBodyBytes` 1 MiB means the limit is never hit, so the read-error path (not the size path) is exercised | Deterministic under Go net/http HTTP/1.1 semantics |

Acceptance test shape (ACC-1, canonical form from the requirements; the implementer must reproduce it):

```go
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
    w.Header().Set("Content-Length", "1000")
    _, _ = w.Write([]byte(`{"status":"accepted"}`))
}))
client := newTestConnector(t, server.URL, HTTPConfig{ErasePath: "/erase", MaxAttempts: 1})
_, err := client.Erase(t.Context(), EraseRequest{AccountID: domain.ID{1}, IdempotencyKey: "idem-1", OperationID: "op-1"})
// assert *RemoteError with Kind==ErrorUnavailable, Temporary==true, Code=="source.response_read_failed"
```

`MaxAttempts: 1` is belt-and-braces: `doHTTP` never retries body-decode failures regardless, so the
classification is final on the first attempt.

## 7. Done gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && \
make check-integration-compile && make build
```

- `make test` — ACC-1 is red before the fix and green after; ACC-2/ACC-3 stay green throughout.
- `make lint` / `make lint-strict` — no new findings; `decodeEraseResult`'s cyclomatic complexity is
  unchanged (a conjunction becomes two branches of equal complexity).
- `make check-file-sizes` — unaffected (190 → ~193 lines).
- `make check-integration-compile` — new test file has no build tags.
- `make build` — full build green.
- Expected green but not part of the required gate (no audit/proto/migration surface touched):
  `check-audit-swallow`, `check-audit-manifest`, `check-architecture`, `check-migration-set`,
  `check-proto-sync`.

## 8. Out of scope (explicitly not in this change)

- Modifying `decodeSummary` or `decodeCommandResult` (already correct).
- Modifying `Erase`'s transport-error mapping (`http_summary.go:237-245`; already
  `Temporary: true` via `source.erase_failed`) or `remoteHTTPError`.
- Adding connector-level retries for body-read failures (reconciliation at the service layer is the
  designed mechanism).
- Changing `configs/config.yaml` defaults (`erase_path`/`status_path` stay opt-in).
- Integration-tagged or network-dependent tests.
- Any audit, `.proto`, migration, or composition-root change.
- Unifying the erase decoder with `decodeCommandResult` (the erase decoder must keep its empty-status
  default-accept and must not gain `operation_status_missing`).

## 9. Design alternatives considered

1. **Reclassify oversized bodies as transient too** — rejected: an oversized body is a deterministic
   protocol violation; making it transient would create unbounded retry/breaker churn for a permanent
   source bug. REQ-2 pins the split explicitly.
2. **Retry body-read failures in `doHTTP`** — rejected: `doHTTP` has no idempotency knowledge; a retried
   POST erase with the same body is safe only because `Idempotency-Key` is set, but the designed mechanism
   is service-layer reconciliation, not connector retries, and adding retries would race the
   unknown-step contract.
3. **Unify all three decoders behind one helper** — rejected as scope creep: the erase decoder's
   empty-body default-accept and missing `operation_status_missing` check are deliberate
   differences; unification would change `decodeCommandResult`'s behavior surface. Keep the split local
   and isomorphic.
4. **Service-layer classification of a new error code** — rejected: no new error kind is needed;
   `ErrorUnavailable` + `Temporary` already flows through every downstream path correctly. The defect is
   purely the decoder's classification.
