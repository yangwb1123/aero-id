# Requirements — Make drainPublications the sole audit delivery path: delete the dead inline publisher loop, move the 8 listed audit writes into AppendInTx, and land the check-audit-swallow gate

| | |
|---|---|
| Module (batch label) | `internal/outbox` — the audit publication outbox (`audit_publications` row + `drainPublications`); the machinery lives in `internal/service/callback_audit.go`, `internal/service/audit_ledger.go`, `internal/repository/audit_ledger.go`, `internal/repository/audit_publication.go`, wired in `internal/app/audit.go` |
| Source analysis | `docs/auto/analyses/internal-outbox-f6bb61c6.json` (direction 1 of 3) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery; gate G3: T-5, first-event e2e) |
| Contract | `docs/proposals/audit-contract-batch-aero-id.md` (§B2-1, §B2-2) |
| Head verified | `13e4676` (committed) **plus the uncommitted working tree** (69 modified files, +3762/−371) |
| Sibling specs | `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` (owns the 10 live `_ = s.audit.Log` sites and the `Log` gate hole; shares the gate and the hold/cutover repo APIs) · `docs/requirements/2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop.md` (REQ-1..REQ-5, T-4 anchored IDs) |
| Date | 2026-08-07 |
| Status | Draft — every cited file/symbol re-verified against HEAD **and** the working tree; the direction's implementation has largely landed in the uncommitted working tree and is specified here as contract to keep plus a small verified delta (§6) |

## 1. Evidence verification

The direction was written against a pre-implementation snapshot. All claims were re-checked at HEAD `13e4676`
(where the cited evidence still holds) and against the working tree (where the campaign has already landed the
changes). Verdicts:

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `Record` runs a dead inline publisher loop (`_ = publisher.Publish(ctx, fact)` after `repo.Append`) at `internal/service/audit_ledger.go:77-80` | ✅ holds at HEAD: `audit_ledger.go:76-80` — `for _, publisher := range s.publishers { if publisher != nil { _ = publisher.Publish(ctx, fact) } }`; `NewAuditLedgerService(repo, publishers ...)` at HEAD `:53-54`. ⚠️ **C1 — already removed in the working tree**: `auditLedgerService` (`:49-52`) has no `publishers` field; `NewAuditLedgerService(repo)` is single-argument (`:56`); `Record` (`:68-82`) calls `s.repo.Append` only; doc comment `:53-55` states "Record only appends, it never publishes". `grep -rn "_ = publisher.Publish" internal/` → 0 hits. Requirement becomes keep-enforced and provable (REQ-1) |
| E2 | Wiring: `internal/app/new.go:146` constructs `NewAuditLedgerService` without publishers; publishers wired only into `callbackSvc` via `wireAuditServices` (`internal/app/audit.go:17-36`) | ✅ HEAD `new.go:147` `service.NewAuditLedgerService(auditLedgerRepo)` (no publisher arg); HEAD `audit.go:17-36` `wireAuditServices` hands `SetAuditPublicationRepository` + `SetAuditPublishers` to `callbackSvc` under `if ok` guards (non-fail-closed). ⚠️ working tree makes it fail-closed: `wireAuditServices` (`internal/app/audit.go:65-75`) panics unless the callback service accepts the publication repo and publishers and the ledger repo implements `AuditPublicationRepository`; `buildAuditStack` (`:34-58`) doc comment: "single delivery: the drain worker" (REQ-6) |
| E3 | `drainPublications` (`internal/service/callback_audit.go:47`) drains `audit_publications`; ticker at `callback.go:267` | ✅ exact at HEAD (`callback_audit.go:47`; `callback.go:261,267`). Working tree: `callback_audit.go:38` (drain), `callback.go:275,281` (ticker → `s.drainPublications(workerCtx)`). The ticker is the **only** call site of `drainPublications` in the tree. Claim/lease/finish machinery in `internal/repository/audit_publication.go:14` (`auditPublicationRow`) |
| E4 | `AppendInTx` exists at `internal/repository/audit_ledger.go:123` | ✅ HEAD `:123`; working tree `:106`. Same-tx append of ledger row + pending `audit_publications` row |
| E5 | `createAuditRows` mints the durable `audit_publications` row in the same tx (cited `:206`) | ✅ HEAD `:189` (line drift only); working tree `:172`. Verified body: ledger INSERT, then `publication := &auditPublicationRow{ID: idgen.NewID(), FactID: fact.ID, Status: "pending", NextAttemptAt: now, ...}` INSERTed in the same tx |
| E6 | `checkAuditDuplicate` dedup guard (cited `:172`) | ✅ HEAD `:174` (drift); working tree `:157`; sentinel `ErrAuditDuplicate` at `:49`; constraint-precise 23505 mapping (`uq_audit_ledger_tenant_event`) in `createAuditRows` |
| E7 | `XxxWithAudit` precedents: `operation.go:135`, `sync_job.go:86`, `event_receipt.go:89` | ✅ exact at HEAD and working tree: `CreateWithAudit` (`operation.go:135`), `CreateWithAudit` (`sync_job.go:86`), `ApplySnapshotEventWithAudit` (`event_receipt.go:89`); siblings `callback_audit.go` (Create/Delete/ResetDeliveryWithAudit), `account_provision_audit.go` (EnsureByIdentity[WithQuota]WithAudit), `account_profile.go` (UpdateProfileWithAudit), `audit_hold_audit.go`/`legacy_cutover_audit.go` (Put/Remove/SetWithAudit) |
| E8 | 8 swallowed sites: `account.go:155,184; callback.go:160,221,247; audit_hold.go:59,74; legacy_cutover.go:88; account_provision.go:168` | ✅ all 8 exact at HEAD (`_ = s.audit.Record(...)`). ⚠️ working tree: all 8 converted — `account.go:155,186` → checked `if err := s.audit.Record(...)` → `apperr.Unavailable("audit.ingest_failed", ...)` (sensitive-read path `account.viewed`/`account.listed`, no business tx exists — correctly out-of-tx); `callback.go` → `registerWithAudit` (`:160-171`) + `DeleteSubscriptionWithAudit` (`:223`) + `ResetDeliveryWithAudit` (`:260`) (in-tx); `audit_hold.go` → `putWithAudit`/`removeWithAudit` (`:85,:104`) (in-tx, checked `Record` fallback tail for repos without the WithAudit surface); `legacy_cutover.go` → `setWithAudit` (`:114`) (in-tx, checked fallback tail); `account_provision.go` → `provisionAccountWithAudit` (`:140-167`) (in-tx) (REQ-2) |
| E9 | "27 audit-related `_ =` sites repo-wide today; gate doc cites 49 at commit 756b65b" | ⚠️ **C2 — counts not reproducible.** Measured at HEAD: **19** live `_ = s.audit.Record|Log` sites in `internal/service` (non-test), exactly the census in the sibling cmd-migrate spec; **6** more in the unwired dead tree (`internal/file/service_share.go:45,75`, `internal/block/block.go:66,86`, `internal/comment/service.go:431`, `internal/message/service.go:289`) = 25 repo-wide. "49 at 756b65b" is a historical gate-doc citation, not a current count. Working tree: **0** in `internal/service` |
| E10 | "Makefile (no check-audit-swallow target exists)" | ✅ holds at HEAD. ⚠️ working tree: target exists — `Makefile:52-54` `check-audit-swallow:` → `bash scripts/check-audit-swallow.sh internal/service`; script with P1 (`_ = ` / `_, _ = ` assignment of `Record\|Log\|Publish\|FinishPublication\|ClaimPublication`) and P2 (bare-call `Record\|Log\|FinishPublication\|ClaimPublication`) patterns, positive/negative self-test fixtures under `scripts/testdata/audit-swallow/`, documented dead-tree exclusion; wired into the done-gate (`AGENTS.md:52`); baseline 0 matches (REQ-3). **C3 — scope delta**: the acceptance says the gate greps `internal/+service/+cmd/`; the Makefile passes only `internal/service`. `cmd/` and `internal/app` contain **0** matching lines today (verified: `cmd/` has no `Record/Log/Publish` call sites; `internal/app` has wiring setters only), so extending the invocation is a safe one-line delta — see REQ-3.2 |

## 2. Target contract

```text
business tx (service → repo XxxWithAudit)
   │  one gorm tx { domain write + r.audit.AppendInTx(ctx, tx, fact) }        (REQ-2)
   ▼
audit_ledger row + pending audit_publications row commit atomically            (createAuditRows)
   ▼
callbackService.Start ticker → drainPublications (callback_audit.go:38) — SOLE delivery path
   │  ClaimPublication → publishAuditFact (governance publisher) → FinishPublication
   ▼
completed / dead_letter + dedup receipt delivery row                          (REQ-1, REQ-5)

Record(ctx, input) — append-only; no publisher parameter, no Publish call      (REQ-1)
```

## 3. Requirements

### REQ-1 — `Record` stays append-only; `drainPublications` stays the sole delivery path (keep-enforced)

- `NewAuditLedgerService` takes exactly one argument (the ledger repository) — no `publishers` variadic, no
  `AuditPublisher` field on `auditLedgerService`. This makes the single-path invariant a **compile-time**
  property: a caller cannot configure `Record` to publish.
- `Record` appends exactly one ledger row (via `repo.Append`) and never calls `Publish`; the doc comment
  "Record only appends, it never publishes" stays.
- `drainPublications` (worker ticker in `callbackService.Start`) remains the only call site delivering
  `audit_publications` rows; no second delivery path (inline loop, direct publisher call in a handler/service)
  may be introduced.
- `grep -rn "_ = publisher.Publish" internal/` → 0; `grep -rn "s.publishers" internal/service/audit_ledger.go` → 0.

### REQ-2 — The 8 direction-listed audit writes are converted; business mutations append in-tx, reads fail closed

Per site, as already landed in the working tree (keep-enforced; the direction's list is exactly this set):

| Site (HEAD line) | Working-tree conversion | Append location |
|---|---|---|
| `account.go:155` (`account.viewed`, admin read) | `if err := s.audit.Record(...)` → `apperr.Unavailable("audit.ingest_failed", ...)` | out-of-tx (sensitive read, no business tx — justified, checked) |
| `account.go:184` (`account.listed`) | same | same |
| `callback.go:160` (register) | `registerWithAudit` → `CreateSubscriptionWithAudit` | in business tx |
| `callback.go:221` (delete) | `DeleteSubscriptionWithAudit` | in business tx |
| `callback.go:247` (replay/reset) | `ResetDeliveryWithAudit` | in business tx |
| `audit_hold.go:59,74` (put/remove) | `PutHoldWithAudit` / `RemoveHoldWithAudit`; checked `Record` fallback tail only for repos without the WithAudit surface | in business tx |
| `legacy_cutover.go:88` (set) | `SetCutoverWithAudit`; checked fallback tail | in business tx |
| `account_provision.go:168` (provision) | `provisionAccountWithAudit` → `EnsureByIdentityWithAudit` / `EnsureByIdentityWithQuotaWithAudit` | in business tx |

- Every in-tx append runs through `AppendInTx` inside the same gorm transaction as the domain write;
  `ErrAuditDuplicate` semantics follow the Class B in-tx swallow contract (duplicate append folds, domain
  write commits idempotently; any other append error rolls back the whole transaction).
- No site may regress to `_ = s.audit.Record(...)` or a bare unchecked `s.audit.Record(...)` call (REQ-3).
- Scope note: the 10 `_ = s.audit.Log` sites (`admin.go`, `ban.go`, `block.go`, `comment.go`, `file_share.go`,
  `message.go`) and the `Log` gate hole are owned by the sibling spec
  `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md`; not in scope here.

### REQ-3 — T-5: no-swallow gate meaningful and green

- REQ-3.1 — `make check-audit-swallow` exists (Makefile target → `scripts/check-audit-swallow.sh`), wired into
  the done-gate, and fails on any of the three direction-named shapes: `_ = s.audit.*` (P1 covers
  `_ = <expr>.Record|Log`), `_ = publisher.Publish` (P1 covers `_ = <expr>.Publish`), and
  `_ = s.publications.*` (P1 covers `_ = <expr>.FinishPublication|ClaimPublication`); P2 additionally bans bare
  unchecked `Record|Log|FinishPublication|ClaimPublication` calls.
- REQ-3.2 — the gate scans the **live layers**: `internal/service` (already), plus `cmd/` and `internal/app`
  (0 matching lines today — verified, so extending the Makefile invocation to
  `scripts/check-audit-swallow.sh internal/service internal/app cmd` is a safe delta; the script needs a
  multi-root loop or an aggregate invocation). The unwired parallel tree
  (`internal/{file,message,comment,block,notification,search,audit}`) and the best-effort SSE `Publish`
  (`notification.go`) remain documented exclusions; `Log` is a first-class gate citizen (P1+P2) — no exclusion.
- REQ-3.3 — the gate self-tests: positive fixtures must match, negative fixtures must not, and the live-tree
  scan must report **0** matches.
- REQ-3.4 — the gate is fail-closed for audit writes: no legacy middleware fail-open carve-out (Ban etc.) and no
  dead-tree `AuditLogger` carve-out applies to live-layer `Record`/`Log` sites (AGENTS.md contract).

### REQ-4 — Failure is never silent: metric or operation failure

For every audit write site, an audit failure must either (a) fail the enclosing operation with the stable
`*.audit_failed` / `audit.ingest_failed` error code, or (b) for the standalone append path, increment
`ObserveAuditIngestFailure`. There is no path where the business operation succeeds and the audit fact is lost
unobserved. Already landed: `TestRecordObservesAuditIngestFailure` (metric arm), `TestAuditHoldAuditFailureFailsClosed`,
`TestCutoverAuditFailureFailsClosed`, `TestAuditWiringRollbackAtomicity` (operation-fails arm, atomic rollback).

### REQ-5 — G3 first-event e2e: exactly one publication row, exactly-once delivery, matching receipt

A fact appended in a business transaction yields **exactly one** `audit_publications` row (status `pending`,
attempt 0) for exactly one ledger row; `drainPublications` delivers it **exactly once** to the governance
publisher with the matching fact identity (`event_id`, `tenant_id`, `action`) and settles `completed` with one
dedup receipt delivery row; a second drain tick claims nothing and delivers nothing. Receipt verification
against an HTTP governance endpoint: a 202 receipt that echoes `event_id` settles `completed`; a conflict
receipt is an idempotent success; a receipt that does not echo the fact is a failure (retry/dead-letter per
attempt budget).

### REQ-6 — Wiring fails closed at composition time

`buildAuditStack`/`wireAuditServices` construct `NewAuditLedgerService` with no publishers; the callback service
receives the `AuditPublicationRepository` and the governance publishers; a future wiring regression (callback
service without `SetAuditPublicationRepository`/`SetAuditPublishers`, ledger repo without
`AuditPublicationRepository`) must panic at startup, never pend facts silently. Already landed (app/audit.go:65-75).

## 4. Acceptance mapping (direction checks preserved, made testable)

| Direction acceptance | Testable check | Status |
|---|---|---|
| T-5: new `make check-audit-swallow` greps internal/+service/+cmd/ and fails on any `_ = s.audit.*`, `_ = publisher.Publish`, `_ = s.publications.*` (0 sites after fix) | `make check-audit-swallow` exits 0 with "no swallowed or bare-call audit writes" and self-test ok; `grep -rnE '^[[:space:]]*(_ = \|_, _ = )[A-Za-z_][A-Za-z0-9_.]*\.(Record\|Log\|Publish\|FinishPublication\|ClaimPublication)\(' internal/service internal/app cmd --include='*.go' \| grep -v _test` → 0 lines; script self-tests: `scripts/testdata/audit-swallow/positive` must match, `negative` must not | ✅ gate landed, green (Makefile:52-54, script, fixtures, done-gate); ⚠️ scan scope delta: currently `internal/service` only — REQ-3.2 (extend to `cmd/`, `internal/app`) is the only open item |
| Unit test: Record with a publisher configured performs zero direct Publish calls (single-path invariant) | Compile-time: `NewAuditLedgerService` has no publisher parameter (audit_ledger.go:56). `go test ./internal/service/ -run TestAuditLedgerRecordEnqueuesOnly` (audit_ledger_test.go:48 — Record appends exactly one row, nothing else). Integration guard: `TestFirstFactDrainDeliversExactlyOnce` step (1) asserts `len(publisher.facts) == 0` before the first drain ("no inline loop") | ✅ landed |
| Unit/integration: mock audit failure increments ObserveAuditIngestFailure or fails the operation — never silent | `go test ./internal/service/ -run 'TestRecordObservesAuditIngestFailure|TestAuditHoldAuditFailureFailsClosed|TestCutoverAuditFailureFailsClosed'` (audit_ledger_test.go:173, audit_hold_test.go:235, legacy_cutover_test.go:233) and `go test ./internal/repository/ -run 'TestAuditWiringRollbackAtomicity|TestAuditWiringPublicationInsertRollsBackLedgerRow'` (audit_wiring_integration_test.go:175,294 — duplicate/injected append failure rolls back the business tx; in-tx 23505 → `ErrAuditDuplicate`); `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service` → 0 | ✅ landed |
| First-event e2e (G3): a fact appended via AppendInTx in a business tx yields exactly one audit_publications row that drainPublications delivers to the governance endpoint with a matching receipt | Integration (Postgres, `make test-integration`): `TestAuditWiringHappyPath` (audit_wiring_integration_test.go:60 — `UpdateProfileWithAudit` in business tx → exactly 1 ledger row + exactly 1 publication row) **composes with** `TestFirstFactDrainDeliversExactlyOnce` (callback_integration_test.go:378 — drain delivers exactly once to the governance publisher with matching `event_id`/`tenant_id`/`action`, settles `completed`, 1 dedup receipt delivery row, second drain no-op) and `TestAuditPublicationDuplicateConflictReceiptSettlesCompleted` (callback_integration_test.go:83 — httptest governance endpoint echoing `event_id` in a 202 `receipt`; conflict receipt settles completed); receipt semantics pinned by `publisher_test.go:17,102,152` (derived source + matching receipt, conflict idempotent success, receipt must echo the fact) | ✅ landed (two-test composition; a single combined test is optional, not required) |

## 5. Coordination register

- **`docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md`** — owns the 10 `_ = s.audit.Log`
  sites, the `Log` gate-hole closing, and the hold/cutover repo constructor merge; this spec shares the gate
  edit and the publication-outbox contract.
- **`docs/requirements/2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop.md`** — owns T-4 anchored
  `event_id` derivation and REQ-1..REQ-5 from the `internal/audit` module angle; this spec is the outbox-module
  view of the same campaign.
- **`docs/plans/2026-08-06-internal-outbox-claim-based-dispatch.md`** — the claim/lease machinery
  (`ClaimPublication`/`FinishPublication`/`ErrClaimLost`) that `drainPublications` relies on; not changed here.

## 6. Delta register (open items in the working tree)

1. **REQ-3.2 — gate scan scope**: the acceptance says the gate greps `internal/+service/+cmd/`; the Makefile
   invocation currently covers `internal/service` only. Extend to `cmd/` (+ `internal/app`) — verified safe:
   0 matching lines in both today. The script's single-ROOT argument needs a loop or aggregate invocation.
2. **Commit**: the entire implementation (69 files) is uncommitted on top of `13e4676`; the done-gate
   (`make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow &&
   make check-anchored-event-ids && make build`, plus `make test-integration` for the repo/storage/connector
   surfaces) must run green before landing.
3. **Optional (not required)**: a single e2e test chaining business-tx `AppendInTx` → `drainPublications` →
   governance endpoint in one test; the acceptance is currently proven by the two-test composition in §4.

No scope expansion beyond the direction: no new canonical audit authority, no changes to the claim-based
dispatch machinery, no `Log`-site conversions (sibling), no T-4 anchoring work (sibling), no dead-letter replay
tooling (direction 3 of the source analysis).
