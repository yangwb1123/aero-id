# Requirements Specification — Land the `make check-audit-swallow` gate and eliminate every `_ = s.audit.*` swallow site

- Module: `cmd/migrate` (campaign assignment label; the change surface is the Makefile gate + `internal/service` + legacy `internal/{file,block,comment,message}`. The `cmd/migrate` binary and migration files are untouched, §5.4)
- Source analysis: `docs/auto/analyses/cmd-migrate-c1b3771d.json` (direction 2 of 3)
- Direction: **Land the make check-audit-swallow gate and eliminate all `_ = s.audit.*` swallow sites**
- Status: evidence re-verified against the repository at head `13e4676`; all cited lines re-checked; corrections C1–C4 in §9 amend the source analysis where it drifted

## 1. Problem statement

25 audit calls in the repository discard their error: `_ = s.audit.Record(...)` / `_ = s.audit.Log(...)` — 19 in the live `internal/service` tree, 6 in the legacy `internal/{file,block,comment,message}` tree. When the ledger is down, or its signing key is unconfigured, `auditLedgerRepo.Append` fails closed with `"audit ledger signing key is not configured"` and `AuditLedgerService.Record` returns that error; these sites discard it, so the compliance fact vanishes with no signal to the caller, the handler, or the metrics path. State-changing operations (callback register/delete, legal-hold set/release, account provisioning, ban/block/admin role changes, file sharing, message deletion) succeed while their audit fact is silently lost. AGENTS.md treats the ledger as authoritative compliance evidence ("the compliance audit ledger is append-only and durable"); a swallow site makes that guarantee depend on the ledger never failing.

There is no enforcement: the Makefile has no `check-audit-swallow` target, so the pattern can regress. The propagation model this direction forces already exists in-tree (`sync.audit_failed`, `replay.audit_failed`, `operation.audit_failed`, `event.audit_failed`, `account.profile_audit_failed` — §4.2). The gate exists to move the remaining 25 sites onto it and keep them there.

## 2. Verified evidence (head `13e4676`)

Every claim below was re-checked against the current tree; line numbers are live.

| # | Claim | Verification |
|---|---|---|
| E1 | Makefile has no `check-audit-swallow`/`check-audit` target | Only check targets are `check-architecture` (`Makefile:26-28`, `@bash scripts/check-architecture.sh`) and `check-file-sizes` (`:32`, inline find loop). `.PHONY` at `:20` lists `lint-strict check-file-sizes` only. The only `audit` mentions are `:58` (proto gen: `api/proto/snaplink/audit/v1/audit.proto`) and `:114` (callback-soak comment). `grep -n "check-audit\|audit-swallow" Makefile scripts/*.sh` → no hits |
| E2 | 19 swallowed calls in the live tree, all on receiver `s` | `account.go:155` (`account.viewed`), `:184` (`account.listed`); `callback.go:160` (`callback.registered`), `:221` (`callback.deleted`), `:247` (`callback.delivery.replayed`); `audit_hold.go:59` (`audit_hold.set`), `:74` (`audit_hold.removed`); `ban.go:83` (`AuditUserBan`), `:119` (`AuditUserUnban`); `admin.go:93` (`AuditAdminGrantRole`), `:120` (`AuditAdminRevokeRole`); `block.go:54` (`AuditUserBlock`), `:77` (`AuditUserUnblock`); `comment.go:415` (`AuditReportResolve`); `file_share.go:114` (`file.share.grant`, inside grantee loop), `:146` (`file.share.revoke`); `message.go:313` (`AuditMessageDelete`); `legacy_cutover.go:88` (`legacy.cutover.set`); `account_provision.go:168` (`account.provisioned`) |
| E3 | 6 swallowed calls in the legacy dead tree | `internal/file/service_share.go:45,75`; `internal/block/block.go:66,86`; `internal/comment/service.go:431`; `internal/message/service.go:289` — all `_ = s.audit.Log(ctx, &domain.AuditEvent{...})` where `s.audit` is `domain.AuditLogger` (`internal/domain/audit.go:12`) |
| E4 | Pattern is exact: 25 sites, all `Record`/`Log`, all on `s`, none in tests, no other `audit.*` member swallowed | `grep -rnE '_ = [A-Za-z0-9_]+\.audit\.(Record|Log)\(' internal/ --include='*.go'` (non-test) → exactly the 25 lines of E2+E3; 0 hits in `*_test.go`; 0 hits for `_ = s.audit.List`/`VerifyChain`; 0 hits on any receiver other than `s` |
| E5 | A swallowed error is a silent compliance hole; fail-closed guard is in the repository | `internal/service/audit_ledger.go:66-78` — `Record` returns the append error and observes `ObserveAuditIngestFailure`. Fail-closed guard: `internal/repository/audit_ledger.go:108-109` (`Append`), `:124-125` (`AppendInTx`), `:334-335` (`VerifyChain`) return `"audit ledger signing key is not configured"` when `SigningKey` is empty (**C2**: source analysis cited the service file `:47-50`, which is only the struct). `Record` intentionally returns nil on `ErrAuditDuplicate` (`audit_ledger.go:68-70`; duplicate check `repository/audit_ledger.go:181`) — idempotent dedup, not a swallow; the gate must not flag it |
| E6 | Propagation model already exists in-tree | `account_profile.go:65` (`account.profile_audit_failed`), `event_ingest.go:96,122` (`event.audit_failed`), `operation.go:140`, `operation_state.go:33` (`operation.audit_failed`), `replay.go:148,245` (`replay.audit_failed`), `sync_job.go:166` (`sync.audit_failed`) — all `apperr.Internal("<scope>.audit_failed", ...).Wrap(err)` where `apperr` = `github.com/aero/aero-id/internal/pkg/errors` |
| E7 | **Correction to the site list:** `sync_job.go:163,467`, `replay.go:145,240`, `operation.go:136` are NOT swallow sites | `sync_job.go:162-167` (`if err := s.audit.Record(...); err != nil { return nil, apperr.Internal("sync.audit_failed", ...) }`), `:464-467` (`return s.audit.Record(...)`); `replay.go:144-149`, `:239-245`; `operation.go:136-141`. Live swallow count is **19**, not "20+" (**C1**); total is **25** |
| E8 | Done gate exists and is where the target registers | `HARNESS.md:45` and `AGENTS.md:52`: `make test && make lint && make lint-strict && make check-file-sizes && make check-architecture && go build ./...` (AGENTS.md's local gate omits `check-architecture`; both get `check-audit-swallow` inserted) |
| E9 | Test plumbing for failing/recording ledger fakes does not exist yet; no testify in module | `go.mod`: zero `testify` entries. `internal/service/audit_ledger_test.go` has `auditLedgerRepoSpy` (repository-level spy: `appended *domain.AuditFact`, `Append` records). `internal/service/service_test.go` exists (shared test helpers). Services inject audit via option funcs: `WithAccountAudit` (`account.go:117-119`), `WithAuditLogger` (`internal/block/block.go:39`) |
| E10 | Legacy `AuditLogger` contract currently mandates the swallow shape | `internal/domain/audit.go:9-11`: "Implementations must be non-blocking from the caller's perspective; a failed log must not fail the primary operation." Must be amended when the 6 legacy sites start propagating (**C4**) |

## 3. Design overview

Two coupled changes, one enforcement surface:

1. **A grep gate** (`make check-audit-swallow`) over the same file set as `check-file-sizes`, matching the discard pattern `_ = [A-Za-z0-9_]+\.audit\.(Record|Log)\(`. Verified to match exactly the 25 current sites and nothing else (E4).
2. **Fail-closed propagation at all 25 sites**, using the in-tree model (E6): audit errors become `*pkg/errors.AppError` with a stable dotted `<scope>.audit_failed` code, so a lost compliance fact can never produce a success response.

The regex is sound because `gofmt` cannot produce a line-broken `_ =` discard (the discard token is a complete expression start); line-oriented grep is sufficient.

## 4. API changes

### 4.1 Error surface (new stable codes)

New dotted error codes, following the existing `<scope>.audit_failed` convention (E6). All map to HTTP 500 via the existing `apperr.Internal` → `pkg/errors` translation; no new HTTP/gRPC status.

| File (sites) | New code |
|---|---|
| `account.go:155,184` (viewed/listed — sensitive reads) | `account.audit_failed` |
| `account_provision.go:168` | `account.audit_failed` |
| `callback.go:160,221,247` | `callback.audit_failed` |
| `audit_hold.go:59,74` | `audit_hold.audit_failed` |
| `ban.go:83,119` | `ban.audit_failed` |
| `admin.go:93,120` | `admin.audit_failed` |
| `block.go:54,77` | `block.audit_failed` |
| `comment.go:415` | `comment.audit_failed` |
| `file_share.go:114,146` | `file_share.audit_failed` |
| `message.go:313` | `message.audit_failed` |
| `legacy_cutover.go:88` | `cutover.audit_failed` |
| `internal/file/service_share.go:45,75` | `file.audit_failed` |
| `internal/block/block.go:66,86` | `block.audit_failed` |
| `internal/comment/service.go:431` | `comment.audit_failed` |
| `internal/message/service.go:289` | `message.audit_failed` |

Canonical shape (error-returning methods):

```go
if s.audit != nil {
    if err := s.audit.Record(ctx, AuditFactInput{...}); err != nil {
        return nil, apperr.Internal("callback.audit_failed", "failed to record callback audit").Wrap(err)
    }
}
```

For methods whose tail statement is the audit call, use `return s.audit.Record(...)` (existing shape at `sync_job.go:464-467`). For `Log`-based legacy sites, the same shape with `s.audit.Log(ctx, &domain.AuditEvent{...})`.

### 4.2 Behavior change (intentional, fail-closed)

- Ledger down or signing key unconfigured → the operation now fails with a stable 500 (`<scope>.audit_failed`) instead of succeeding silently. Ledger healthy → behavior unchanged.
- `ErrAuditDuplicate` remains an idempotent success: `Record` returns nil on dedup (E5); the gate does not touch it.
- `file_share.go:114` sits inside a grantee loop: first audit failure aborts the whole request; no partial-grant success response is emitted.

### 4.3 Build/CI surface

- New `scripts/check-audit-swallow.sh` (mirrors `scripts/check-architecture.sh` conventions: `set -euo pipefail`, explicit violation list, exit 1 on any hit), scanning `find internal/ -name '*.go' ! -name '*.pb.go' ! -name '*_test.go' ! -path '*/testpg/*' ! -path '*/mocks/*'` — the exact `check-file-sizes` predicate (Makefile:40-42).
- `Makefile`: add `check-audit-swallow` to `.PHONY` (`:20`) and a target next to `check-architecture` (`:26-28`) with a compliance rationale comment.
- `HARNESS.md:45` and `AGENTS.md:52` done gates: insert `make check-audit-swallow` after `make check-file-sizes`.

### 4.4 No API changes

- No proto changes, no new HTTP/gRPC endpoints, no `AuditLedgerService`/`domain.AuditLogger` interface changes, no migration files, no `cmd/migrate` binary changes, no SQL, no repository changes.

## 5. Compatibility constraints

- **Wire/proto compatibility:** none affected — this is service-layer error propagation only.
- **Existing error text and codes preserved:** the 8 existing `*_audit_failed` codes (E6) are untouched; new codes follow the same naming so tooling keyed on `*.audit_failed` keeps working.
- **Idempotency semantics preserved:** `ErrAuditDuplicate` → nil stays; the gate regex does not match `if err := ...` or `return ...` forms.
- **Legacy interface contract:** `domain.AuditLogger` method set unchanged; its doc comment (`domain/audit.go:9-11`) is amended to "the caller decides whether a failed log fails the primary operation; compliance-ledger callers fail closed" (C4). The 6 legacy sites are in the unwired dead tree; mechanical 3-line changes, no new dependencies.
- **In-tx direction compatibility:** direction 1 of the same analysis (moving appends into the transaction) is out of scope here, but its sites keep their propagated error handling — the gate requires a *handled* audit error, not a particular transaction boundary.
- **Read-path sites:** `account.viewed`/`account.listed` (sensitive reads) now fail closed per AGENTS.md ("sensitive-read operations produce a durable audit fact"). If a future direction wants read-side best-effort audit, it must introduce an explicit policy — the gate forbids the silent form.
- **File-size gate:** no touched file approaches 500 lines (a few lines per site + tests); no splits required.
- **`0 = disabled` config convention:** unchanged; the `s.audit != nil` guards (already present in E6 sites) mean an unconfigured audit service stays a no-op rather than a crash — the gate applies to *configured-but-failing* ledgers.

## 6. Failure modes

| Failure | Current behavior | After this change |
|---|---|---|
| Ledger DB down / timeout | Operation succeeds; fact lost silently | Operation fails with stable 500 `<scope>.audit_failed`; `ObserveAuditIngestFailure` metric still fires (E5); retry with idempotency key re-attempts the fact |
| Signing key unconfigured | Same silent loss | Same fail-closed 500; operator-visible error text "audit ledger signing key is not configured" wrapped in the cause chain |
| Callback mutation committed, audit fails | Success + lost fact | Error returned *after* the mutation is durable — the mutation is not rolled back (no distributed transaction); caller sees a 500 and may retry via idempotency key; `ErrAuditDuplicate` makes the retry converge |
| Partial grant in `file_share.go` grantee loop | All grants + one lost fact | First audit failure aborts the request; earlier grants in the loop are already durable — reported as failure, never silently partial |
| Duplicate event/fact | `ErrAuditDuplicate` → nil | Unchanged: idempotent success, not flagged by the gate |
| `s.audit == nil` (unconfigured option) | Skip | Unchanged: skip via existing `if s.audit != nil` guard (E6 shape); no nil deref |
| New swallow introduced later | No detection | `make check-audit-swallow` reds the done gate (AC1 negative control) |
| Ledger outage at scale | — | New 500s on affected operations; bulkheads/retry budgets per AGENTS.md apply unchanged; no new load on the ledger (same one call per operation) |

## 7. Migration steps (ordered)

1. **Red proof:** run `grep -rnE '_ = [A-Za-z0-9_]+\.audit\.(Record|Log)\(' internal/ --include='*.go'` (non-test) and capture the 25 violations (E2+E3) as the baseline; commit nothing yet.
2. **Gate first:** add `scripts/check-audit-swallow.sh`, the `Makefile` target + `.PHONY`, and update `HARNESS.md:45` / `AGENTS.md:52`. Verify `make check-audit-swallow` exits 1 listing exactly 25 file:line hits.
3. **Live tree (19 sites):** in each file of §4.1, replace `_ = s.audit.Record|Log(...)` with the propagated shape; keep the existing fact payloads byte-identical. Preserve `if s.audit != nil` guards where the file already uses them.
4. **Legacy tree (6 sites):** same replacement in `internal/{file/service_share,block/block,comment/service,message/service}.go`; amend the `domain.AuditLogger` doc comment (C4).
5. **Tests:** add shared failing + recording fakes (§8.2) and the per-site unit tests (§8.2/§8.3). Run them before step 3 lands to confirm they are red against the current swallow code, then green after.
6. **Green gate:** `make check-audit-swallow` exits 0; full done gate `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build`. `make test-integration` is not required (no repository/SQL/connector change) but must stay green if run.
7. **Negative control:** temporarily add `_ = s.audit.Record(ctx, ...)` to any non-test `internal/**/*.go`; gate reds; remove; gate greens.

Rollback: revert step 3/4 files and the gate target; no data migration exists, so rollback is a pure code revert.

## 8. Testable acceptance mapping

### 8.1 AC1 — Enforcement gate (maps T-10.1; red today, green after)

- `make check-audit-swallow` exists (target + `.PHONY` + script) and is registered in the done gate (E8).
- **Red proof:** exits non-zero, lists exactly 25 violations with file:line.
- **Green proof:** exits 0 after §7.3/§7.4, and stays green across the full done gate.
- **Negative control:** temporary discard flips red; removal flips green.
- **No false positives:** `if err := s.audit.Record(...); err != nil {...}`, `return s.audit.Record(...)`, and `err = s.audit.Record(...)` (all present in E6 files) are not flagged.

### 8.2 AC2 — Propagation: no success response while the fact is not durable (maps T-5)

New white-box unit tests, stdlib only (no testify), beside the code:

- Shared fakes in `internal/service/service_test.go` (and per-legacy-package test files): a failing `AuditLedgerService` (`Record`/`Log` return a sentinel error; `List`/`VerifyChain` no-op) and a failing `domain.AuditLogger` for the legacy packages.
- One test per migrated site, injecting the failing fake via the existing option funcs (E9) and asserting: the operation returns `*pkg/errors.AppError`, code equals the §4.1 assigned `<scope>.audit_failed`, and `errors.Is`/cause chain contains the sentinel. Concretely: `account_test.go` (`ViewAccount`/`ListAccounts`), `account_provision_test.go` (no account returned), `callback_test.go` (`Register`/`Delete`/`ReplayDelivery`), `audit_hold_test.go` (`Set`/`Remove`), `ban_test.go`, `admin_test.go`, `block_test.go`, `comment_test.go`, `file_test.go`, `message_test.go`, `legacy_cutover_test.go`, plus `internal/{file,block,comment,message}` package tests.
- **Red today:** with the current `_ =` sites, every one of these tests fails (operation succeeds despite the failing ledger) — this is the per-site "success responses are impossible while the audit fact is not durable" proof.

### 8.3 AC3 — Coverage: exactly one handled audit call per state-changing operation (maps T-4)

- Reuse a recording fake (shape of `auditLedgerRepoSpy.appended`, E9) asserting, per operation, **exactly one** audit call with the expected `Action`:
  - callback `Register` → `callback.registered`; `Delete` → `callback.deleted`; `ReplayDelivery` → `callback.delivery.replayed`
  - audit hold `Set` → `audit_hold.set`; `Remove` → `audit_hold.removed`
  - account provision → `account.provisioned`
  - sync job create/finish → `sync.job.requested` / `sync.job.finished` (already handled; test pins the existing single call)
  - replay dry-run/requested → `audit.replay.dry_run` / `audit.replay.requested` (already handled; same)
- Count asserted per operation (exactly one — not zero, not two), making coverage testable rather than grep-argued. Assertions are written at the service boundary, so they are independent of the in-tx direction.

## 9. Amendment register (corrections C1–C4)

| # | Stale wording (source analysis) | Correction | Folded into |
|---|---|---|---|
| C1 | "20+ swallowed audit calls in the live tree" including `sync_job.go:163,467`, `replay.go:145,240`, `operation.go:136` | Those five sites already propagate (verified E7). Live tree has exactly **19**; total **25** with the legacy tree | §1, §2 E2/E7, §4.1 |
| C2 | Fail-closed signing-key guard cited at `internal/service/audit_ledger.go:47-50` | Guard is in the repository: `internal/repository/audit_ledger.go:108-109,124-125,334-335`; `service/audit_ledger.go:47-52` is the struct only. Substance unchanged | §2 E5, §6 |
| C3 | Two gate patterns ("`_ = <expr>.audit.Record\|Log`" and "`_ = s.audit.`") | One regex `_ = [A-Za-z0-9_]+\.audit\.(Record|Log)\(` covers both, matches exactly 25 sites, zero false positives (E4) | §3, §8.1 |
| C4 | Legacy sites treated as plain swallows | Legacy `s.audit` is `domain.AuditLogger`, whose doc comment (`domain/audit.go:9-11`) currently mandates the swallow shape; amend the comment when the 6 sites start propagating | §2 E10, §5, §7.4 |
