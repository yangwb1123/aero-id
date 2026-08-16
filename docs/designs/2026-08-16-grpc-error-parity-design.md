# Design — gRPC error parity: carry the dotted AppError code on the wire via errdetails, unify the duplicate GRPCError, and map deadline/cancel correctly

**Direction:** *Carry the dotted AppError code on the wire via errdetails, unify the duplicate GRPCError, and map deadline/cancel correctly*
**Source:** requirements spec (2026-08-16, verified evidence); module `internal/pkg/errors` + gRPC handler transport
**Date:** 2026-08-16
**Status:** Design (not implemented)

---

## 0. Verification verdict

The evidence was treated as untrusted and re-verified against the current tree. **All load-bearing claims verified.** Line-level checks:

| # | Claim | Repository evidence (verified) | Result |
|---|---|---|---|
| E1 | Exported `GRPCError` exists, dead in production | `internal/pkg/errors/errors.go:62-72`; full-tree grep for `GRPCError` finds only the definition + comment in `errors.go` — zero callers anywhere (production **and** tests; the `server_test.go` tests named `TestGRPCError_*` call the *local* `grpcError`, not this function) | ✅ exact |
| E2 | Duplicate `grpcError` is the sole production mapper | `internal/handler/grpc/server.go:410-421`; 133 call sites across the 15 handler files (`account` 13, `aggregation` 4, `admin` 9, `audit_ledger` 4, `block` 3, `callback` 4, `comment` 27, `event_ingest` 5, `file` 17, `message` 20, `notification` 5, `operation` 11, `preference` 2, `search` 1, `sync_job` 8) + 2 test call sites in `server_test.go` + 1 definition = 136 `grpcError(` occurrences in the live tree | ✅ (count reconciliation: evidence's "135 call sites" = 136 occurrences − 1 definition; 2 of the 135 are in `server_test.go`, so *production* call sites are 133 across exactly the 15 named files) |
| E3 | Both drop the dotted code | `errors.go:68` and `server.go:418` both call `status.Error(ae.GRPC, ae.Message)` | ✅ exact |
| E4 | HTTP keeps the code in the envelope | `internal/pkg/httpx/httpx.go:24-28` (`errorDetail.Code`), `:36` (`Code: ae.Code`) | ✅ exact |
| E5 | Parity tests can only assert `status.Code` | `internal/handler/grpc/parity_test.go:63` (`PermissionDenied`, service returns `apperr.Forbidden("file.forbidden", ...)`) and `:172` (`AlreadyExists`, service returns `apperr.Conflict("comment.already_reported", ...)`); both tests dial a real bufconn server (`dial(t, grpcsrv.NewFileServer/NewCommentServer(svc))`) so errdetails assertions there prove on-the-wire serialization | ✅ exact |
| E6 | Timeout middleware installs only a context deadline | `internal/middleware/timeout.go` (whole file: `context.WithTimeout`, no mapping); grep for `Timeout|Deadline` in non-test `internal/handler/grpc/*.go` returns nothing — no gRPC timeout interceptor exists; `middleware.Timeout` is wired only in the HTTP router (`internal/handler/http/router.go:72`) | ✅ exact |
| E7 | No GRPCError test in-package | `internal/pkg/errors/errors_test.go` (44 lines) tests `TestNotFound`, `TestUnauthorized`, `TestWrap_preservesCause`, `TestAs` only | ✅ exact |
| E8 | No errdetails/WithDetails usage in tree | `grep -rn "errdetails\|WithDetails" --include="*.go" internal/` → zero matches | ✅ exact |
| E9 | errdetails dependency available | `google.golang.org/genproto/googleapis/rpc v0.0.0-20260401024825-9d38bb4040a9` in `go.mod:144` + `go.sum`; `go list google.golang.org/genproto/googleapis/rpc/errdetails` resolves cleanly | ✅ exact |
| E10 | Handler tests pin the duplicate | `internal/handler/grpc/server_test.go:95-101` (`TestGRPCError_mapsAppError`, `TestGRPCError_genericInternal`) call the local `grpcError` | ✅ exact |
| E11 | `stderrors` import in server.go used only by `grpcError` | `server.go:7` (import) and `:417` (sole use) — 2 occurrences total | ✅ exact |
| E12 | Architecture gate pattern | `scripts/check-architecture.sh`: rules (a)–(f), `selftest_rule` harness with positive-must-fire / negative-must-be-clean sweep over `scripts/testdata/architecture/{positive,negative}/`, real-tree scan `internal cmd`, final pass message `"✓ Architecture check passed (rules a-f: ...)"` | ✅ exact |
| — | 15 handler files already import `apperr` | `internal/handler/grpc/file.go:13` (`apperr "github.com/aero/aero-id/internal/pkg/errors"`); all 15 call `apperr.BadRequest/Forbidden/...` today | ✅ exact |
| — | `codes`/`status` imports in server.go stay used after deleting `grpcError` | `RecoveryInterceptor` `:403`; auth interceptor `:259/:263/:342/:346/:349`; rate-limit `:300`; tracing `:173/:211` | ✅ exact |
| — | No other AppError→gRPC mapper exists outside `internal/handler/grpc` | Non-test, non-generated `status.Error/status.New/status.Errorf` sites in the repo outside `handler/grpc` are only `internal/pkg/errors/errors.go` itself; `internal/sso/*pb.go` are generated stubs, not mappers | ✅ exact |
| — | File sizes leave headroom for the gates | `errors.go` 73 lines, `server.go` 438, `errors_test.go` 44, `parity_test.go` 259 (all ≪ 500) | ✅ exact |

No substantive discrepancies. The only imprecision is E2's wording: the 15 handler files hold **133** production call sites; the evidence's "135" counts the two `server_test.go` call sites as well. Substance unaffected.

---

## 1. Problem statement

Two error-to-gRPC-status mappers with identical logic exist:

1. `internal/pkg/errors.GRPCError` (`errors.go:62-72`) — the exported package contract, **zero callers** anywhere in the tree, free to drift silently from shipped behavior.
2. `internal/handler/grpc.grpcError` (`server.go:410-421`) — unexported byte-identical duplicate, the **sole production path** (133 call sites across 15 handler files).

Both call `status.Error(ae.GRPC, ae.Message)`, which **drops the stable dotted code** (`sync.not_found`, `comment.already_reported`, ...). Two gRPC errors sharing a `codes.Code` are indistinguishable to clients. HTTP already preserves the code (`httpx.errorDetail.Code`); gRPC does not.

Second defect: **any non-AppError is misclassified as `codes.Internal`**, including raw `context.DeadlineExceeded`/`context.Canceled`. `internal/middleware/timeout.go` only installs a `context.WithTimeout` deadline (HTTP router only) and never maps the resulting error; no gRPC timeout interceptor exists. When a service-layer call observes the deadline, the error hits `grpcError` and becomes `codes.Internal` — contradicting AGENTS.md: *"A timeout is not proof of remote failure."* grpc-go's transport-level context mapping (`errors.Is(err, context.DeadlineExceeded)` → `codes.DeadlineExceeded`) is preempted because `grpcError` converts the error to an explicit `*status.Error` (a `GRPCStatus`-implementing error) before the transport sees it; grpc-go treats an explicit status as final.

No `errdetails`/`WithDetails` usage exists anywhere in the tree (E8); this design introduces the first structured gRPC error detail. The parity tests (E5) can only assert `status.Code` today; with errdetails on the wire they can assert the reason end-to-end.

---

## 2. API changes

### 2.1 New behavior: `internal/pkg/errors.GRPCError` (replaces its body; same signature)

```go
// GRPCError converts a service error to a gRPC status error. An *AppError
// carries its own code and safe message; the stable dotted code is attached as
// a google.rpc.ErrorInfo detail so clients can distinguish errors that share a
// codes.Code. Context deadline/cancel map to their canonical codes (a timeout
// is not proof of remote failure); anything else becomes Internal so internal
// details are never leaked.
func GRPCError(err error) error {
	if err == nil {
		return nil
	}
	var ae *AppError
	if stdErrors.As(err, &ae) { // AppError wins over a wrapped context error (REQ-2.5)
		st, derr := status.New(ae.GRPC, ae.Message).WithDetails(
			&errdetails.ErrorInfo{Reason: ae.Code, Domain: "aero-id"},
		)
		if derr != nil { // details must never degrade code/message (REQ-2.3)
			return status.New(ae.GRPC, ae.Message).Err()
		}
		return st.Err()
	}
	switch {
	case stdErrors.Is(err, context.DeadlineExceeded):
		return status.New(codes.DeadlineExceeded, "deadline exceeded").Err()
	case stdErrors.Is(err, context.Canceled):
		return status.New(codes.Canceled, "request canceled").Err()
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}
```

- **New imports in `errors.go`:** `context`, `google.golang.org/genproto/googleapis/rpc/errdetails`. Existing `stdErrors "errors"`, `net/http`, `codes`, `status` stay. File grows from 73 to ≈95 lines — far under the 500-line gate.
- **Matching order is load-bearing (REQ-2.5):** `errors.As` (AppError) first, `errors.Is` (deadline/cancel) second. An AppError whose `Cause` is a context deadline keeps its own declared classification; only the cause chain is examined for the `Is` checks.
- **Wire contract (REQ-2.1/2.2):** `status.New(ae.GRPC, ae.Message)` + `WithDetails(&errdetails.ErrorInfo{Reason: ae.Code, Domain: "aero-id"})`. `Reason` is the exact dotted code, never truncated/translated/prefixed. `Message` is `ae.Message` only — never `ae.Error()` (which embeds `" (cause: ...)"`), never `ae.Cause` content.
- **Fallback (REQ-2.3):** a failed `WithDetails` (not expected with a well-formed `ErrorInfo`) returns a plain status error — code and safe message never degraded by the details path.
- **Nil contract (REQ-2.4):** `GRPCError(nil) == nil` preserved.

### 2.2 Delete the duplicate local mapper

- `internal/handler/grpc/server.go:410-421` — delete the `grpcError` comment + function (12 lines).
- `internal/handler/grpc/server.go:7` — delete `stderrors "errors"` (sole use was `:417`; verified E11). `codes` and `status` imports **stay** (used by `RecoveryInterceptor` `:403`, auth `:259/:263/:342/:346/:349`, rate-limit `:300`, tracing `:173/:211`).

### 2.3 Migrate the 133 production call sites (mechanical rename)

In the 15 handler files only (never `server.go`, never `server_test.go`): replace `grpcError(` → `apperr.GRPCError(`. Every file already imports `apperr` (verified), so **zero import churn**. The deletion in 2.2 makes the migration self-checking: any missed call site is a compile error, not a runtime surprise.

Per-file call-site counts (verified): `account` 13, `aggregation` 4, `admin` 9, `audit_ledger` 4, `block` 3, `callback` 4, `comment` 27, `event_ingest` 5, `file` 17, `message` 20, `notification` 5, `operation` 11, `preference` 2, `search` 1, `sync_job` 8.

### 2.4 Delete the local-mapper tests

`internal/handler/grpc/server_test.go:95-101` — `TestGRPCError_mapsAppError`, `TestGRPCError_genericInternal` reference the deleted local function. Their coverage is superseded by the in-package tests (§2.5).

### 2.5 New in-package unit tests (`internal/pkg/errors/errors_test.go`)

New imports: `context`, `fmt`, `strings`, `google.golang.org/grpc/codes`, `google.golang.org/grpc/status`, `google.golang.org/genproto/googleapis/rpc/errdetails` (plus existing `stderrors`, `net/http`, `testing`).

- **T1** `TestGRPCError_nil` — `GRPCError(nil) == nil`.
- **T2** `TestGRPCError_appError_carriesCodeAndSafeMessage` — `e := NotFound("sync.not_found", "sync not found").Wrap(stderrors.New("db: secret detail"))`; `st := status.Convert(GRPCError(e))`; assert `st.Code() == codes.NotFound`, `st.Message() == "sync not found"` and contains neither `"secret"` nor `"cause"`, `len(st.Details()) == 1`, detail is `*errdetails.ErrorInfo` with `Reason == "sync.not_found"` and `Domain == "aero-id"`.
- **T3** `TestGRPCError_deadlineAndCancel` — `status.Code(GRPCError(context.DeadlineExceeded)) == codes.DeadlineExceeded`; `status.Code(GRPCError(context.Canceled)) == codes.Canceled`; `status.Code(GRPCError(fmt.Errorf("wrap: %w", context.DeadlineExceeded))) == codes.DeadlineExceeded` (wrapped variant proves `errors.Is`, not `==`).
- **T4** `TestGRPCError_genericInternal` — `st := status.Convert(GRPCError(stderrors.New("boom")))`; `st.Code() == codes.Internal` and `st.Message() == "internal server error"`.

### 2.6 Strengthen parity tests to assert the wire reason (`internal/handler/grpc/parity_test.go`)

Package `grpc_test` (external). Add `errdetails` import. At both existing assertion sites, on top of the kept `status.Code` check:

- **W1** (`:63`, `TestGRPC_ShareFile_NonOwnerPermissionDenied`): `st, _ := status.FromError(err)`; `d, ok := st.Details()[0].(*errdetails.ErrorInfo)`; assert `ok && d.Reason == "file.forbidden"` (service returns `apperr.Forbidden("file.forbidden", "not owner")`).
- **W2** (`:172`, `TestGRPC_ReportComment_DuplicateConflict`): same pattern asserting `d.Reason == "comment.already_reported"` (service returns `apperr.Conflict("comment.already_reported", "dup")`).

These tests dial a real bufconn server, so the assertion proves the errdetails survive gRPC serialization end-to-end on the wire (not just in-memory).

### 2.7 Architecture tripwire against reintroduction (`scripts/check-architecture.sh`, rule g)

Following the script's own self-test contract (E12), add:

- **New scan function** — for each scanned root, every `.go` file matching `*/internal/handler/grpc/*` (tests included) containing `func grpcError` is a violation:

```bash
scan_grpc_error_mapper() {
  local root f
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      case "/$f" in
        */internal/handler/grpc/*) ;;
        *) continue ;;
      esac
      if grep -q "func grpcError" "$f"; then
        echo "VIOLATION: $f defines a local grpcError (the single mapper must be internal/pkg/errors.GRPCError)"
        violations=$((violations + 1))
      fi
    done < <(find "$root" -name '*.go' 2>/dev/null | sort)
  done
}
```

- **Self-test registration** — `selftest_rule "(g) single grpc error mapper" scan_grpc_error_mapper`, with:
  - positive fixture `scripts/testdata/architecture/positive/grpc_error_duplicate/internal/handler/grpc/server.go` containing `func grpcError(err error) error {` (must fire; a positive that stops matching fails the gate);
  - negative fixture `scripts/testdata/architecture/negative/grpc_error_clean/internal/handler/grpc/server.go` containing `apperr.GRPCError(...)` and **no** `func grpcError` (must stay clean; a negative that starts matching fails the gate).
  - The fixture path pattern `*/internal/handler/grpc/*` matches both fixture trees and the live tree; fixture files import no `internal/repository`, so rule (e) stays unaffected.
- **Real-tree scan** — `scan_grpc_error_mapper internal cmd` added to the real-tree block.
- **Final pass message** — extend to `"✓ Architecture check passed (rules a-g: ..., single grpc error mapper)"`.
- **Header comment** — add rule (g) to the canonical rule list at the top of the script.

### 2.8 No generated-code changes

No `.proto` edits, no generated code, no `make proto`. `ErrorInfo` is the standard `google.rpc.ErrorInfo` detail shipped with grpc-go; the pinned `genproto` version (E9) and grpc-go v1.80.0 already provide everything.

---

## 3. Compatibility constraints

1. **Wire-compatible for existing clients.** For every AppError, `status.Code` and `Message` are byte-identical to today; the `ErrorInfo` detail is purely additive and ignored by clients that don't read `Details`. The only behavioral deltas are (a) the added detail and (b) the deadline/cancel reclassification below.
2. **Intentional behavior delta: deadline/cancel reclassification.** Raw/wrapped `context.DeadlineExceeded`/`context.Canceled` change from `codes.Internal` to `codes.DeadlineExceeded`/`codes.Canceled` with fixed safe messages. This is the point of REQ-3 (AGENTS.md: *"A timeout is not proof of remote failure"*). Internal gRPC clients that retried on `Internal` for timeouts should key off the new codes; the fixed messages are not a contract for string matching.
3. **Zero import churn in the 15 handler files** — all already import `apperr` (verified via `file.go:13`).
4. **`server.go` keeps `codes`/`status`** — only `stderrors` is removed; the interceptors and tracing still use `codes`/`status` (verified).
5. **Leak safety is preserved and test-pinned.** Every path emits either `ae.Message` or a fixed constant; `ae.Error()`/cause content never reach the wire (guarded by T2).
6. **Nil and ordering contracts.** `GRPCError(nil) == nil`; AppError-first matching means a declared classification is never overridden by a wrapped context cause.
7. **No file-size gate risk.** `errors.go` 73→≈95 lines; `server.go` shrinks 12 lines; all touched files ≪ 500.
8. **Compile-enforced migration.** Deleting the local function means `go build` fails on any missed call site — the rename cannot silently miss one.
9. **Out of scope, intentionally unchanged:** `AppError`, its constructors, `httpx`, HTTP timeout behavior, `RecoveryInterceptor` (`server.go:394-407`), `validate.go` and interceptor `status.Error` sites (direct status errors by design, not AppError-mapping duplicates), SSE hub, brokers, and other transports.

---

## 4. Failure modes

| # | Failure mode | Mitigation |
|---|---|---|
| F1 | `WithDetails` errors (malformed `ErrorInfo`) | REQ-2.3 fallback returns `status.New(ae.GRPC, ae.Message).Err()` — code/message never degraded; unreachable with a well-formed literal |
| F2 | Missed call site during rename | Compile error after the local function is deleted (constraint 8); additionally the acceptance grep (`grep -rn "func grpcError" ...` → no matches) and rule (g) catch reintroductions |
| F3 | Cause leak into the wire if someone later switches `ae.Message` → `ae.Error()` | T2 asserts message excludes `"cause"` and cause content; regression gate |
| F4 | Ordering regression (deadline check before AppError check) | T2 (AppError wrapping a raw cause still maps to `NotFound`) + T3 prove ordering; the code comment marks it load-bearing |
| F5 | Reintroduced duplicate mapper in a new file or a new gRPC subtree | Rule (g) scans `internal/handler/grpc/` (tests included) in self-test and real-tree passes; a new duplicate fires the gate |
| F6 | A handler's raw context error previously surfaced as `Internal` and a client depended on that code | Documented, intended delta (constraint 2); new code is the correct classification per AGENTS.md |
| F7 | Parity details assertions silently lose coverage (e.g., bufconn wiring bypasses the mapper) | W1/W2 go through real handlers (`NewFileServer`/`NewCommentServer`) that call `apperr.GRPCError`; a bypassed mapper fails the `Reason` assertion |
| F8 | errdetails serialization drift on dependency bump | `genproto` pinned in `go.mod` (E9) + `go.sum`; W1/W2 prove on-the-wire serialization on every test run |
| F9 | Negative architecture fixture accidentally matches (e.g., a comment mentioning `grpcError` with `func` prefix elsewhere) | Rule (g) greps `func grpcError` (function-definition shape), not bare mentions; fixtures and live tree both verified clean under this shape |

---

## 5. Migration steps (minimal, ordered; each step verifiable)

1. **`internal/pkg/errors/errors.go`** — replace `GRPCError` body per §2.1; add `context` + `errdetails` imports. *Verify:* `gofmt -l`, file still ≪ 500 lines.
2. **`internal/pkg/errors/errors_test.go`** — add T1–T4 (§2.5). *Verify:* `go test ./internal/pkg/errors/ -count=1`.
3. **`internal/handler/grpc/server.go`** — delete lines 410-421 (`grpcError`) and line 7 (`stderrors`). *Verify:* `go build ./internal/handler/grpc/` **fails** (proves no other file compiles against the deleted symbol) — expected mid-migration; see step 4.
4. **15 handler files** — mechanical `grpcError(` → `apperr.GRPCError(` (§2.3 counts). *Verify:* `go build ./...` passes again; `grep -rn "func grpcError" --include="*.go" internal/ cmd/` → no matches.
5. **`internal/handler/grpc/server_test.go`** — delete lines 95-101. *Verify:* `go test ./internal/handler/grpc/ -count=1`.
6. **`internal/handler/grpc/parity_test.go`** — W1/W2 + `errdetails` import (§2.6). *Verify:* `go test ./internal/handler/grpc/ -count=1` with the strengthened assertions.
7. **`scripts/check-architecture.sh`** — rule (g) scan function, self-test registration, fixtures (positive `grpc_error_duplicate`, negative `grpc_error_clean`), real-tree scan line, pass-message extension, header rule list (§2.7). *Verify:* `make check-architecture` green; deleting either fixture file fails the gate (self-test contract preserved).
8. **Full done gate** (§8) plus the explicit acceptance greps and tests.

Rollback: steps 1–6 are a single revert of `errors.go`, `errors_test.go`, `server.go`, the 15 handler files, `server_test.go`, `parity_test.go` (no data migration, no schema, no wire-format change beyond an additive detail, no generated code). Step 7 is independent of the Go changes and can be reverted alone.

---

## 6. Testable acceptance mapping

| Requirement | Test / gate | Evidence command |
|---|---|---|
| REQ-1.1 delete duplicate | build + grep | `go build ./...`; `grep -rn "func grpcError" --include="*.go" internal/ cmd/` → no matches |
| REQ-1.2 migrate call sites | build (compile-enforced) + grep | `grep -rn "GRPCError" --include="*.go" internal/ \| grep -v "_test"` → call sites present in the 15 handler files |
| REQ-1.3 delete local-mapper tests | test run | `go test ./internal/handler/grpc/ -count=1` (no reference to deleted function) |
| REQ-2.1/2.2 wire code + safe message | T2 (unit), W1/W2 (wire) | `go test ./internal/pkg/errors/ -count=1 -run TestGRPCError_appError`; `go test ./internal/handler/grpc/ -count=1 -run TestGRPC_ShareFile_NonOwnerPermissionDenied` |
| REQ-2.3 details fallback | code review + T2 (code/message assertions) | covered by `make test` |
| REQ-2.4 nil contract | T1 | `go test ./internal/pkg/errors/ -count=1 -run TestGRPCError_nil` |
| REQ-2.5 ordering (AppError wins) | T2 (wrapped AppError keeps `NotFound`) | `go test ./internal/pkg/errors/ -count=1 -run TestGRPCError_appError` |
| REQ-3 deadline/cancel mapping | T3 | `go test ./internal/pkg/errors/ -count=1 -run TestGRPCError_deadlineAndCancel` |
| REQ-4 in-package tests | `go test ./internal/pkg/errors/` | `go test ./internal/pkg/errors/ -count=1` |
| REQ-5 wire reason assertions | W1/W2 (bufconn, real serialization) | `go test ./internal/handler/grpc/ -count=1` |
| REQ-6 architecture tripwire | self-test sweep + real-tree scan | `make check-architecture` green; delete positive fixture → gate fails (vacuous); delete negative fixture content → gate fails (over-broad) |
| Global gates | full done gate | `make test && make lint && make lint-strict && make check-file-sizes && make check-architecture && make build` |

Acceptance greps at completion:

```bash
grep -rn "func grpcError" --include="*.go" internal/ cmd/          # → no matches
grep -rn "GRPCError" --include="*.go" internal/ | grep -v "_test"  # → call sites in handler/grpc/*.go present
go test ./internal/pkg/errors/ -count=1
go test ./internal/handler/grpc/ -count=1
```

---

## 7. Non-goals / out of scope

- No change to `AppError`, its constructors, HTTP codes, or `httpx` (HTTP already carries the code).
- No HTTP-side timeout mapping (the direction scopes deadline/cancel mapping to the gRPC mapper; `internal/middleware/timeout.go` and the gin router are untouched).
- `RecoveryInterceptor`, `validate.go`, and interceptor `status.Error` sites remain as-is — intentional direct status errors, not AppError-mapping duplicates.
- No changes to other transports, brokers, the SSE hub, or connectors.
- No new RPCs, protobuf files, or wire message fields beyond the standard `google.rpc.ErrorInfo` detail; no `make proto` run.
