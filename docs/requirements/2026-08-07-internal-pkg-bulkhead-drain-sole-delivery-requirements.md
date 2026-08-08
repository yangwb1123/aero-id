# Requirements specification — drainPublications as the sole audit delivery path, no swallowed audit writes

| | |
|---|---|
| Module (batch label) | `internal/pkg/bulkhead` (direction scope: `internal/service` + `internal/repository` audit delivery path) |
| Source analysis | `docs/auto/analyses/internal-pkg-bulkhead-942e0fa7.json` (item 1 of 3) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery path) |
| Direction | **Make drainPublications the sole audit delivery path and eliminate swallowed audit writes (B2 items 1+2)** |
| Date | 2026-08-07 |
| Status | **Evidence verified at HEAD `90c4c24` + working tree — the direction's problem is already resolved in the current tree; the acceptance checks map to existing green tests.** |

## 1. Direction (verbatim from the source analysis)

> `auditLedgerService.Record` runs a synchronous inline publisher loop
> (`internal/service/audit_ledger.go:76-80`, `_ = publisher.Publish(ctx, fact)`) that is dead in the
> production composition root (`internal/app/new.go:146` wires `NewAuditLedgerService` without
> publishers), yet it would double-deliver to governance if a publisher were ever wired to both it and
> the callback service (`SetAuditPublishers` at `internal/app/audit.go:34`). The live path is the
> bulkhead-guarded `drainPublications` (`internal/service/callback_audit.go:52-76`), but its
> `_ = s.publications.FinishPublication(...)` (`callback_audit.go:71`) swallows
> `ErrAuditPublicationLeaseLost`, leaving rows `'delivering'` until lease expiry and causing duplicate
> governance deliveries. Swallowed audit facts also exist at `internal/service/callback.go:153/171/196`
> (`_ = s.audit.Record`) and `internal/service/audit_hold.go:59,74`; no `check-audit-swallow` gate
> exists in the Makefile (verified absent). The `XxxWithAudit`/`AppendInTx` pattern already exists for
> operation/sync/account repos (`internal/repository/operation.go:178`, `sync_job.go:111`,
> `account_profile.go:101`) but is not applied to callback registration/deletion/holds.

## 2. Evidence verification

The analysis snapshot predates HEAD `90c4c24` ("feat(audit): async audit export (P4-08), layered
source health, control-plane metrics") and the earlier `check-audit-swallow` gate work. Every cited
file/symbol was re-checked against the current tree; **five claims are superseded by the current code
(rows marked ✗ — defect gone), and one line citation is corrected**. The `internal/pkg/bulkhead`
module itself is unchanged and is the concurrency guard the live drain path depends on.

| # | Claim (direction evidence) | Verification at HEAD `90c4c24` + working tree | Status |
|---|---|---|---|
| E1 | `Record` runs an inline `_ = publisher.Publish(ctx, fact)` loop at `audit_ledger.go:76-80` | ✗ **Gone.** `Record` (`internal/service/audit_ledger.go:63-74`) appends via `s.repo.Append` and returns; there is no publisher field, no loop, no `Publish` call. The doc comment on `NewAuditLedgerService` states: "Record only appends, it never publishes." `AuditPublisher` (`audit_ledger.go:23-25`) remains but is consumed only by the drain path | Superseded |
| E2 | `internal/app/new.go:146` wires `NewAuditLedgerService` without publishers → dead loop in production | ✗ **Gone with E1.** `new.go:134` calls `buildAuditStack`; `internal/app/audit.go:39-57` constructs the ledger (`NewAuditLedgerService` at `:51`) and calls `wireAuditServices` (`:55`). `wireAuditServices` (`audit.go:65-86`) attaches the publication repo and publishers to the **callback** service only, failing closed with panics if either assertion fails. The ledger service has no publisher surface, so wiring a publisher to both ledger and callback — the cited double-delivery hazard — is structurally impossible | Superseded |
| E3 | `SetAuditPublishers` at `internal/app/audit.go:34` | Corrected: `SetAuditPublishers` is asserted/called inside `wireAuditServices` at `internal/app/audit.go:79-86`; the implementation is `callbackService.SetAuditPublishers` at `internal/service/callback.go:103-104` | Corrected, confirmed |
| E4 | `drainPublications` at `callback_audit.go:52-76` is the live, bulkhead-guarded path | ✅ `drainPublications` at `internal/service/callback_audit.go:52-77`; claims via `ClaimPublication` (5 min lease, limit 50), fan-out under `s.bulkhead.Acquire/Release` (`:60-68`, P6-06), `finishPublicationAttempt` per item. Worker ticker calls it at `internal/service/callback.go:281` inside `Start` (`:268-283`). Bulkhead type: `internal/pkg/bulkhead/bulkhead.go` (`New/Acquire/Release/Run`); tests `internal/pkg/bulkhead/bulkhead_test.go` (limits concurrency, honors context, serial default, Run) | Confirmed |
| E5 | `_ = s.publications.FinishPublication(...)` at `callback_audit.go:71` swallows `ErrAuditPublicationLeaseLost` | ✗ **Gone.** `finishPublicationAttempt` (`callback_audit.go:80-92`) does `if err := s.publications.FinishPublication(...); err != nil { s.metrics.ObserveAuditPublicationFinishFailure(status, err); return }` — the error is observed, never discarded. `FinishPublication` (`internal/repository/audit_publication.go:77-92`) is a lease-guarded UPDATE (`WHERE id = ? AND status = 'delivering' AND lease_owner = ?`); `RowsAffected != 1` returns `ErrAuditPublicationLeaseLost` (defined `internal/repository/audit_ledger.go:50`). `ClaimPublication` (`audit_publication.go:31-75`) re-claims `status='delivering' AND lease_until < now` rows, so a failed finish is retried after lease expiry — no duplicate delivery (lease owner is re-asserted) | Superseded |
| E6 | Swallowed `_ = s.audit.Record` at `callback.go:153/171/196` | ✗ **Gone.** `callback.go:151-171` — `registerWithAudit` → `repo.CreateSubscriptionWithAudit` (one transaction); `:223` — `DeleteSubscriptionWithAudit`; `:260` — `ResetDeliveryWithAudit`. All three are `CallbackAuditRepository` methods (`internal/repository/callback_audit.go:17-24`) and return errors to the caller | Superseded |
| E7 | Swallowed `_ = s.audit.Record` at `audit_hold.go:59,74` | ✗ **Gone.** `internal/service/audit_hold.go:66-73` and `:111-115` both do `if err := s.audit.Record(...); err != nil { return err }`; the atomic paths use `PutHoldWithAudit`/`RemoveHoldWithAudit` (`internal/repository/audit_hold.go:127-146, 154-170`) | Superseded |
| E8 | No `check-audit-swallow` gate in the Makefile | ✗ **Present now.** `Makefile:63-65` defines `check-audit-swallow` → `scripts/check-audit-swallow.sh`. The script scans `internal/service` with P1 (`_ = ...(Record\|Log\|Publish\|FinishPublication\|ClaimPublication)(`) and P2 (bare `Record\|Log\|FinishPublication\|ClaimPublication` calls) patterns, self-tests against `scripts/testdata/audit-swallow/{positive,negative}`, and requires 0 live matches. **Verified run: exit 0, "no swallowed or bare-call audit writes"** | Superseded |
| E9 | `AppendInTx` exists at `audit_ledger.go:38-40,123` | ✅ Interface method at `internal/repository/audit_ledger.go:39`; implementation at `:106`. Both `Append` and `AppendInTx` funnel into `appendInTx` (`:120-140`), which creates the `audit_ledger` row **and** the `status='pending'` `audit_publications` row in the same transaction, with duplicate check on `(tenant_id, source, event_id)` (`checkAuditDuplicate` at `:157`) | Confirmed |
| E10 | Existing `WithAudit` users at `operation.go:178`, `sync_job.go:111`, `account_profile.go:101` | ✅ `operation.go:182` (`CreateWithAudit` + `FinishStepWithAudit`/`SetStatusWithAudit`, interface `:36-43`), `sync_job.go:116` (`CreateWithAudit`/`FinishWithAudit`, `:42-43`), `account_profile.go:101` (`UpdateProfileWithAudit`, `:67`) — each `r.audit.AppendInTx(ctx, tx, fact)` inside the repo's own `Transaction(...)`, ignoring only `ErrAuditDuplicate` | Confirmed |
| E11 | `XxxWithAudit` **not** applied to callback registration/deletion/holds | ✗ **Applied now.** Callback: `CreateSubscriptionWithAudit`/`DeleteSubscriptionWithAudit`/`ResetDeliveryWithAudit` (`internal/repository/callback_audit.go`, rollback test at `callback_audit_integration_test.go:129`). Holds: `PutHoldWithAudit`/`RemoveHoldWithAudit` (`internal/repository/audit_hold.go:127,154`, tests at `audit_hold_integration_test.go:108-308`) | Superseded |
| E12 | `ErrAuditPublicationLeaseLost` / `FinishPublication` at `audit_ledger.go:251-263` | Corrected: `ErrAuditPublicationLeaseLost` at `internal/repository/audit_ledger.go:50`; `FinishPublication` and `ClaimPublication` live in `internal/repository/audit_publication.go:31-92` | Corrected, confirmed |

**Conclusion:** every defect cited by the direction is resolved in the current tree. The remaining
deliverable is therefore the *verification* of the direction's invariants and acceptance checks
against the shipped code and tests — not new implementation.

## 3. Requirements (invariants the direction demands)

- **REQ-1 — Single delivery path.** `AuditLedgerService.Record` appends the fact (and its pending
  `audit_publications` row) and performs zero governance deliveries and zero callback enqueues.
  Delivery happens exclusively through `ClaimPublication` + `drainPublications` in the worker loop.
  The composition root fails closed if the publication repository or publishers cannot be attached
  to the callback service (`wireAuditServices` panics).
- **REQ-2 — Bulkhead-guarded drain.** The drain fan-out acquires a bulkhead slot per publication
  (`internal/pkg/bulkhead`), bounded by `WorkerConcurrency`, so a slow governance endpoint cannot
  starve callback deliveries or interactive pools. Losing the bulkhead loses no correctness state
  (leases and receipts live in PostgreSQL).
- **REQ-3 — No swallowed audit writes.** Every live-layer `Record`/`Log`/`Publish`/
  `FinishPublication`/`ClaimPublication` call propagates its error. The `make check-audit-swallow`
  gate (P1+P2 patterns, self-tested) is part of the done gate (`AGENTS.md`) and must stay green at
  exactly 0 live matches.
- **REQ-4 — In-transaction audit for callback and hold mutations.** Callback registration/deletion/
  replay and legal-hold put/remove append their fact via `AppendInTx` in the same transaction as the
  business row; a business rollback removes the `audit_ledger` row and its `audit_publications` row
  together. Duplicate facts fold via `ErrAuditDuplicate` (dedupe key `(tenant_id, source, event_id)`).
- **REQ-5 — FinishPublication errors are surfaced and retried by lease.** A failed
  `FinishPublication` (raw DB error or `ErrAuditPublicationLeaseLost`) is observed on the
  finish-failure metric, is never retried inline, and leaves the row `delivering`; the next tick
  re-claims it after lease expiry, re-publishes, and settles — exactly once per attempt, with the
  outcome counted only when the finish commits.

## 4. Acceptance criteria (preserved from the direction, made testable)

| ID | Acceptance (direction wording) | Testable check in the current tree | How to run |
|---|---|---|---|
| T-10.1 | "Test wiring a recording AuditPublisher proves Record produces zero inline HTTP deliveries and the only delivery is via ClaimPublication + drainPublications" | `TestAuditLedgerRecordEnqueuesOnly` (`internal/service/audit_ledger_test.go:46-58`) asserts Record appends exactly one ledger row and never publishes (the ledger has no publisher surface — the inline loop is gone at compile time). `TestFirstFactDrainDeliversExactlyOnce` (`internal/service/callback_integration_test.go:380-428`) wires the `recordingAuditPublisher` (`:298-302`) via `SetAuditPublishers` and asserts: after Record → 1 ledger row, 1 pending publication (attempt 0), **0 publisher calls, 0 `callback_deliveries` rows**; after one `drainPublications` → `completed`, exactly 1 publish of the matching fact, 1 dedup receipt, outcome metric `[completed]`; a second drain is a no-op | `go test ./internal/service/ -run 'TestAuditLedgerRecordEnqueuesOnly'` (unit); `make test-integration` (drain harness) |
| T-10.2 | "`make check-audit-swallow` (grep for `_ =` against audit.Record/Publish/FinishPublication) fails on current callback.go, audit_hold.go, callback_audit.go call sites and passes after removal" | Gate exists at `Makefile:63-65` → `scripts/check-audit-swallow.sh`. Red proof: the self-test asserts the P1/P2 patterns match the positive fixtures under `scripts/testdata/audit-swallow/positive/` (which contain the historical shapes `_ = s.audit.Record(`, `_ = publisher.Publish(`, `_ = s.publications.FinishPublication(`) — the gate **fails** on them. Green proof: the live tree has 0 matches (the cited call sites no longer exist; all propagate errors). Verified: exit 0 | `make check-audit-swallow` (no Docker; part of the done gate) |
| T-5 | "Repo-level test that callback.Register and hold mutations append the fact via AppendInTx in the same tx — business rollback removes ledger row + audit_publications row together; FinishPublication errors are returned (not `_ =`) and retried on the next tick" | Callback: `TestCallbackAuditHappyPath` + `TestCallbackAuditRollbackWithoutSigningKey` (`internal/repository/callback_audit_integration_test.go:54,129` — failed `CreateSubscriptionWithAudit` leaves 0 subscription, 0 ledger, 0 publication rows; failed `ResetDeliveryWithAudit` leaves the in-flight delivery byte-identical). Holds: `TestAuditHoldPutWithAuditHappyPath/RollbackWithoutSigningKey/DuplicateFold/PublicationInsertRollsBack` and `TestAuditHoldRemoveWithAudit*` (`internal/repository/audit_hold_integration_test.go:108-308`). FinishPublication: `TestFinishPublicationFailureObservesMetricNoRetry` (`internal/service/callback_audit_test.go:146` — lease-lost error observed, no inline retry, no outcome counted) and `TestFinishPublicationRawDBErrorNotCountedUntilCommitted` (`internal/service/callback_integration_test.go:816` — drain #1: finish fails, row stays `delivering`; after lease expiry drain #2 re-claims, re-publishes, commits `completed`, outcome counted once, dedup receipt across attempts) | `make test-integration` |

## 5. Residual delta

- **No code, migration, or wiring changes are required.** All five superseded claims (E1, E2, E5,
  E6, E7) and the gate gap (E8) and pattern gap (E11) are fixed at HEAD `90c4c24` / working tree.
- The only nuance: T-10.1's "recording AuditPublisher wired to Record" is realized as (a) a unit
  test proving Record has no publish surface at all, plus (b) an integration test wiring the
  recording publisher into the callback service and proving zero deliveries until the drain runs.
  This is stronger than the acceptance wording because the ledger service cannot be wired to a
  publisher by construction.
- T-5's "retried on the next tick" is implemented as lease-expiry re-claim (5-minute lease,
  `lease_until < now` predicate in `ClaimPublication`), which is the documented at-least-once
  mechanism; the retry tests manipulate `lease_until` directly rather than waiting a real tick.

## 6. Out of scope (explicitly excluded)

- Analysis item 2 (TokenSource 401 invalidation / epoch single-flight / scope-missing alerting) and
  item 3 (stable anchor-derived `event_id`) are separate directions and are **not** part of this
  specification.
- No changes to `internal/pkg/bulkhead` itself: the module is the shipped concurrency guard the
  drain path uses (P6-06), with existing unit tests.
- No changes to migrations, `cmd/*` binaries, or the legacy dead tree (`internal/{file,message,
  comment,block,notification,search,audit}`).
