# Design — Complete the in-tx audit migration: ten post-commit Log sites + fallback tails → WithAudit, atomicAudit plumbing dropped, T-4/T-5 gates made testable

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-08-cmd-server-complete-in-tx-audit-writes.md` (REQ-1..REQ-7, corrections C-1..C-6 folded in) |
| Supersedes | `docs/plans/2026-08-06-cmd-server-complete-in-tx-audit-writes-design.md` — stale on six points (it describes 8 constructor changes, 10 `_ = s.audit.Log` sites, a dead inline publisher loop, and a gate without `Log`; C-3/C-4 corrected all of these) |
| Module | `internal/app` composition root (`new.go`); spans `internal/service`, `internal/repository`, `scripts/check-audit-swallow.sh`, `scripts/check-anchored-event-ids.sh` |
| Head verified | `13e4676` + uncommitted working tree (39 modified files) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery; gate G3: T-4/T-5) |
| Date | 2026-08-08 |
| Status | Design — evidence re-verified in full; two new findings (D-1, D-2) amend the requirements doc. |

## 1. Evidence verification (untrusted claims vs. repository at `13e4676` + working tree)

All direction citations in the requirements doc were re-checked. **All hold**, with the doc's own corrections
(C-1..C-6) confirmed and two additional findings (D-1, D-2):

| # | Claim (requirements doc) | Verification |
|---|---|---|
| 1 | Ten `s.audit.Log` sites, all `if err :=`-checked, all after domain commit | ✅ Exact: `admin.go:93` GrantRole, `admin.go:122` RevokeRole, `ban.go:84` Ban, `ban.go:122` Unban, `block.go:54` Block, `block.go:79` Unblock, `comment.go:415` ResolveReport, `file_share.go:114` grant loop, `file_share.go:148` UnshareFile, `message.go:358` via `auditMessageDelete` (`message.go:354-363`; helper tail is `return s.audit.Log(...)`). Payloads match: admin `{role}`, ban `{reason, expiresAt}`, comment `{comment_id, status}`, others TargetUID only |
| 2 | Six backing repos without ledger field; constructors at `new.go:132,141,142,144,145,148` | ✅ `fileShareRepo` (`file_share.go:34`), `messageRepo` (`message.go:80`), `commentRepo` (`comment.go:72`), `blockRepo` (`block.go:42`), `banRepo` (`ban.go:41`), `adminRepo` (`admin.go:35`) |
| 3 | **C-3:** hold/cutover already converted | ✅ `NewAuditHoldRepository(db, audit)` `repository/audit_hold.go:82` + `requireAuditLedger`; `NewLegacyCutoverRepository(db, audit)` `repository/legacy_cutover.go:97`; wired `new.go:228`, `app/audit.go:221-223` |
| 4 | Four fallback tails + C-6 (sync finish is type-assert-else, not bool) + C-5 (legacy_cutover residual, out of scope) | ✅ `operation.go:127-142` (bool, tail `:135-142`), `sync_job.go:155-169` (bool, tail `:162-169`), `sync_job.go:454-472` (type-assert-else, `FinishWithAudit` else `Finish`+`Record`), `account_profile.go:44-68` (bool, tail `:61-68`), `legacy_cutover.go:85-107` (bool, tail `:96-107`, provably unreachable in root — concrete repo always satisfies `LegacyCutoverAuditRepository`; kept out of scope) |
| 5 | **C-4:** gate Log-inclusion landed; only mechanical wiring assertions missing | ✅ `scripts/check-audit-swallow.sh:60-61` P1/P2 include `Log` and `Requeue*`; fixtures carry `_ = s.audit.Log(` / `return s.audit.Log(` / `_ = publisher.Publish(`; Makefile scans `internal/service internal/repository internal/app cmd` |
| 6 | T-4 lines `:60/:175/:294`, trigger `:264-285`; coverage today = profile/operation/event | ✅ `TestAuditWiringHappyPath`, `TestAuditWiringRollbackAtomicity`, `TestAuditWiringPublicationInsertRollsBackLedgerRow`, `installFailPublicationInsertTrigger` all exact in `internal/repository/audit_wiring_integration_test.go` |
| 7 | **C-1:** `drainPublications` sole non-test call site is `callback_worker.go:47` inside `Start` | ✅ Defined `internal/service/callback_audit.go:45`; call inside `Start` ticker `callback_worker.go:29-51`; test-only calls at `callback_audit_test.go:187,242,408` (+integration tests) |
| 8 | **C-2:** message-delete and comment-resolve already carry anchored EventIDs | ✅ `auditMessageDelete` `ID: id` (retry-dedup contract documented `message.go:351-353`, exercised by `deleteAlreadyGone` `:337-349`); comment `ID: reportID` `comment.go:419`. The Log adapter maps `legacy.ID → EventID` (`audit_ledger.go:141`), so both rows are non-NULL event_id today |
| 9 | Anchored IDs: hold/cutover via `idgen.Anchor`; C2 allowlist = `internal/service/callback.go|cmd/audit-replay/main.go` | ✅ Anchors at `internal/service/audit_hold.go:164` (`audit_hold.set`, 6-part) and `:184` (`audit_hold.removed`, 4-part), `internal/service/legacy_cutover.go:142` (`legacy.cutover.set`, 4-part); R-1 ban + C2 allowlist exact at `scripts/check-anchored-event-ids.sh:31` |
| 10 | Backing facts: `AppendInTx` interface `repository/audit_ledger.go:39`, impl `:123`, `createAuditRows` `:189`, `ErrAuditDuplicate` `:59`, `requireAuditLedger` `repository/audit_wiring.go:19-28`, `buildAuditFact` `service/audit_ledger.go:82`, Log mapping `:136-141` (**TargetUID dropped**) | ✅ All exact |
| 11 | Unit-test fakes: `mocks.Auditor` LogFn-only; test constructor sites | ✅ `mocks.Auditor` (`mocks/*.go:590-601`) has `LogFn` only, no `*WithAuditFn` anywhere in `internal/service/mocks`. Constructor call sites: `repository/comment_test.go:22`, `message_test.go:22`, `block_integration_test.go:19`, `file_share_integration_test.go:21` — **plus two the doc misses: `reaction_audit_integration_test.go:29` and `search_integration_test.go:26-27`** (both build live `NewMessageRepository`/`NewCommentRepository`). No admin/ban repo integration tests exist |
| 12 | Error-code precedents | ✅ `admin.audit_unavailable` `admin.go:51`, `operation.audit_unavailable` `operation_audit.go:28`, `sync.audit_unavailable` `sync_audit.go:29`, `callback.audit_unavailable` `callback.go:167`, `account.audit_unavailable` `account_provision.go:154` |

### D-1 (material — the requirements doc missed a fifth live fallback tail)

`internal/service/event_ingest.go:86-100` is an out-of-tx `s.audit.Record` fallback of exactly the class REQ-4
deletes: `applySnapshot` returns `atomicAudit=false` when the repo lacks the audit surface, and `IngestEvent`
then appends the fact **after** the domain write committed via `recordAudit` (`event_ingest.go:94-100`). It is
provably unreachable in the wired root (concrete `eventReceiptRepo` implements `EventSnapshotAuditRepository`,
`event_receipt.go:39,89`; `s.audit` is never nil in the root) — identical to the `legacy_cutover.go:96-107`
case, but **unlike cutover it is not explicitly deferred anywhere**: REQ-4.3's "residual set is exactly {…}"
claim silently omits it. This design folds it into REQ-4 as the fifth tail (§4.4): same mechanical deletion,
`applySnapshot` becomes error-returning with a fail-closed head, new code `event.audit_unavailable`. The
`recordAudit` helper stays for the quarantine path (`event_ingest.go:121`). No new repository surface: event
apply is already WithAudit and already T-4-covered.

### D-2 (minor — path drift)

Hold/cutover anchors live in `internal/service/audit_hold.go` and `internal/service/legacy_cutover.go`, not
`internal/repository/` (line numbers 164/184/142 are exact). Non-material.

## 2. Target architecture

```text
service mutation (admin/ban/block/comment/message/file_share)        fallback tails (operation/sync/profile/event)
   │  s.audit != nil → repo.(XxxAuditRepository) type-assert             │  deleted — fail-closed heads only
   │  (fail-closed: else apperr.Internal("<x>.audit_unavailable"))       │  (createWithAudit/applySnapshot become error-returning)
   ▼                                                                     ▼
repo XxxWithAudit:  gorm tx { domain write (+ outbox event where today) + ledger.AppendInTx }
   │
   ├─ audit_ledger row + pending audit_publications row commit WITH the domain row (one tx)
   └─ drainPublications (callbackService.Start ticker) = the ONLY delivery path (at-least-once, dedup-keyed)

Residual out-of-tx Record/Log calls after migration (all checked, never `_ =`):
  account.go:155,186 (sensitive reads) · operation_state.go:39 · replay.go:145,240 ·
  event_ingest.go:121 (quarantine) · message deleteAlreadyGone fact-only append (domain row predates the request)
```

Invariant restated: **no migrated mutation has a commit-to-append gap.** A committed fact is eventually
delivered at least once; a failed append never leaves a domain row without its fact.

## 3. API changes

### 3.1 Repository constructor API — six signatures change (C-3: hold/cutover already done)

| Constructor | Old | New |
|---|---|---|
| `NewAdminRepository` | `(db *gorm.DB)` | `(db, audit AuditLedgerTransactionalRepository)` |
| `NewBanRepository` | `(db)` | `(db, audit)` |
| `NewBlockRepository` | `(db)` | `(db, audit)` |
| `NewCommentRepository` | `(db, ob outbox.Repository)` | `(db, ob, audit)` |
| `NewMessageRepository` | `(db, ob, counterOpt ...*UnreadCounter)` | `(db, ob, audit, counterOpt ...*UnreadCounter)` |
| `NewFileShareRepository` | `(db)` | `(db, audit)` |

Each struct gains `audit AuditLedgerTransactionalRepository`; each constructor calls
`requireAuditLedger("NewXxxRepository", audit)` (panic on typed-nil or nil = fail at startup; precedent
`repository/audit_wiring.go:21`). Plain methods stay; `XxxWithAudit` variants are additive. **Call-site
inventory (all must compile):**

| Call site | Change |
|---|---|
| `internal/app/new.go:141,142,144,145,148` | pass `auditLedgerRepo` (in scope from `buildAuditStack` at `:134`) |
| `internal/app/new.go:132` (`fileShareRepo`) | **F-1:** the construction moves **below** `buildAuditStack` (`:134`) in the same step, then passes `auditLedgerRepo` — as originally written the ledger is not in scope at `:132` |
| `internal/repository/comment_test.go:22`, `message_test.go:22`, `reaction_audit_integration_test.go:29`, `search_integration_test.go:26-27` | pass `NewAuditLedgerRepository(db, cfg)` (integration keyed) |
| `internal/repository/block_integration_test.go:19`, `file_share_integration_test.go:21` | pass `NewAuditLedgerRepository(db, cfg)` |
| `internal/message/repository.go` (dead tree) | **untouched** (scope boundary) |

### 3.2 New repository audit interfaces (sibling `*_audit.go` files; one gorm tx each: domain write → `AppendInTx`)

| Interface (new file) | Methods (wrapping the plain methods at the cited lines) |
|---|---|
| `AdminAuditRepository` (`admin_audit.go`) | `GrantRoleWithAudit(ctx, uid, roleCode, grantedBy string, fact *domain.AuditFact) error` (wraps `admin.go:106`) · `RevokeRoleWithAudit(ctx, uid, roleCode string, fact) error` (wraps `:119`) |
| `BanAuditRepository` (`ban_audit.go`) | `BanWithAudit(ctx, uid, actorUID, reason string, expiresAt *time.Time, fact) error` (wraps `ban.go:68`) · `UnbanWithAudit(ctx, uid string, fact) error` (wraps `:89`) |
| `BlockAuditRepository` (`block_audit.go`) | `BlockWithAudit(ctx, blocker, blocked string, fact) error` (wraps `block.go:44`) · `UnblockWithAudit(ctx, blocker, blocked string, fact) error` (wraps `:54`) |
| `CommentAuditRepository` (`comment_audit.go`) | `ResolveReportWithAudit(ctx, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time, hideCommentID domain.ID, fact) error` — one tx merging today's two-call service flow (`comment.go:390-408`: `ResolveReport` + conditional `SetStatus`); `hideCommentID == zero` skips the hide; report-update `RowsAffected == 0` → `ErrNotFound` (same as plain `ResolveReport` `comment.go:370`) |
| `MessageAuditRepository` (`message_audit.go`) | `SoftDeleteWithEventAudit(ctx, id domain.ID, evt *outbox.Event, fact) error` — one tx: soft delete + outbox event (when `evt != nil && r.outbox != nil`, as plain `SoftDeleteWithEvent` `message.go:282`) + fact |
| `FileShareAuditRepository` (`file_share_audit.go`) | `GrantWithAudit(ctx, fileID domain.ID, ownerUID string, granteeUIDs []string, facts []*domain.AuditFact) error` — N grant rows + N facts, one tx, `len(facts) == len(granteeUIDs)` (service guarantees) · `RevokeWithAudit(ctx, fileID domain.ID, granteeUID string, fact) error` |

Shared semantics (mirror existing `operation_audit.go:136`, `sync_job_audit.go:55`, `callback_audit.go:49`):

```go
if err := r.audit.AppendInTx(ctx, tx, fact); err != nil && !errors.Is(err, ErrAuditDuplicate) {
    return fmt.Errorf("append <mutation> audit: %w", err)
}
```

- **Unconditional fact append** matches today's Log-after-call placement: facts record even when the domain
  write was a 0-row no-op (revoke of absent role, unban of unbanned, unshare of unshared, resolve with no
  status change). Short-circuits that return before the Log site stay short-circuits (message delete of a
  missing message → `deleteAlreadyGone`; file share with zero grantees → early return `file_share.go:110-112`).
- `AuditFact` is appended exactly as built by the service — the repo never re-derives anchors or EventIDs.
- `ErrAuditDuplicate` is swallowed **after** the domain write succeeded in the same tx (Class B); it never
  masks a failed domain write (the tx aborts at the domain statement before the append).

### 3.3 Service conversions — the ten Log sites become fail-closed WithAudit calls

New shared helper `internal/service/audit_fact.go`:

```go
// buildLegacyAuditFact reproduces the Log-adapter mapping (audit_ledger.go:136-141:
// ActorUID, Action, SubjectType→ResourceType, SubjectID→ResourceID, Meta→Payload)
// plus TargetUID→Payload["target_uid"] (REQ-3.2) and the EventID policy of REQ-3.3.
func buildLegacyAuditFact(ctx context.Context, actor, action, subjectType, subjectID, targetUID string,
    meta map[string]any) *domain.AuditFact
```

1. **Shape** (six services `admin.go`, `ban.go`, `block.go`, `comment.go`, `message.go`, `file_share.go`):
   each `if s.audit != nil { s.audit.Log(...) }` tail becomes (block example):

   ```go
   if s.audit != nil {
       repo, ok := s.blocks.(repository.BlockAuditRepository)
       if !ok {
           return apperr.Internal("block.audit_unavailable", "block audit persistence is unavailable")
       }
       return repo.BlockWithAudit(ctx, actor, uid,
           buildLegacyAuditFact(ctx, actor, domain.AuditUserBlock, "user", uid, uid, nil))
   }
   return s.blocks.Block(ctx, actor, uid)
   ```

   New codes: `ban.audit_unavailable`, `block.audit_unavailable`, `comment.audit_unavailable`,
   `message.audit_unavailable`, `file.audit_unavailable`; admin reuses the existing
   `admin.audit_unavailable` (`admin.go:51`). With `s.audit == nil` (unit tests) the plain path is
   byte-for-byte today's behavior.

2. **Fact parity:** each converted fact equals the Log-adapter mapping plus `Payload["target_uid"]` (the
   adapter drops `TargetUID` today — `audit_ledger.go:136-141`; conversion is the moment to stop losing it).
   Per-mutation payloads unchanged: admin `Payload["role"]`, ban `{"reason","expiresAt"}`, comment
   `{"comment_id","status"}` (statusLabel `resolved`/`dismissed`), file-share `target_uid` = grantee (one
   fact per grantee).

3. **EventID policy (C-2):**
   - **message delete and comment resolve keep their anchored EventIDs byte-identical** — message ID
     (`message.go:354-363` `ID: id`) and report ID (`comment.go:419` `ID: reportID`). Both anchors are
     load-bearing: `deleteAlreadyGone` (`message.go:337-349`) and resolve retries re-derive the same
     `event_id`, so the in-tx `ErrAuditDuplicate` swallow commits the domain write / no-op without
     fabricating a second fact ("a re-delete never fabricates a fresh audit fact", `message.go:353`).
   - **the other eight sites** (admin×2, ban×2, block×2, file-share×2) mint a fresh `idgen.NewID()` per
     call (today NULL event_id; a stable reused ID across distinct intents would let `ErrAuditDuplicate`
     roll back a legitimate domain write — the D3 hazard).
   - `scripts/check-anchored-event-ids.sh` C2 allowlist (`:31`) gains `internal/service/audit_fact.go` with
     a comment documenting the eight unanchored legacy-Log sites. No grep-evasion shapes (no deferred
     assignment, no string-built call).

4. **Per-mutation semantics:**
   - message delete: one tx (soft delete + outbox event + fact) via `SoftDeleteWithEventAudit`. The
     `deleteAlreadyGone` fact-only append stays a checked out-of-tx residual (its domain row predates the
     request; there is no write to join) — remains `if err :=`-checked and joins the REQ-5.3 residual set.
   - comment resolve: `hideCommentID = rep.CommentID` when `status == Resolved && c.Status == Normal`
     (intended atomicity change: a failing conditional hide now rolls the resolve back with it; report-not-
     found still wins when both fail).
   - file share: one fact per grantee, `GrantWithAudit`.
   - The `audit` field stays (nil gate); `With*AuditLogger` options and wiring unchanged. No `s.audit.Log`
     call remains for these mutations. The `Log` adapter itself stays (`audit_ledger.go:136`) — it serves
     the `deleteAlreadyGone` residual and remains the legacy surface.

### 3.4 Fallback-tail deletion — five tails (four in REQ-4 + D-1), `atomicAudit` plumbing dropped

| Site | Today | After |
|---|---|---|
| `operation.go:127-142` (create) | `atomicAudit, err := s.createWithAudit(...)` + tail `:135-142` | `createWithAudit` → `error`-returning; fail-closed head already present (`operation_audit.go:19-28`); tail deleted |
| `sync_job.go:155-169` (create) | bool + tail `:162-169` | `createWithAudit` → `error`-returning (`sync_audit.go:19-28`); tail deleted |
| `sync_job.go:454-472` (finish, C-6 shape) | `if repo, ok := ...; ok { FinishWithAudit } else { Finish + Record }` | fail-closed: `s.audit != nil → type-assert else apperr.Internal("sync.audit_unavailable")`; plain `repo.Finish` only when `s.audit == nil` |
| `account_profile.go:44-68` (update) | bool + tail `:61-68` | fail-closed head; new code `account.profile_audit_unavailable` (today's tail error is `account.profile_audit_failed`); tail deleted |
| `event_ingest.go:86-100` (apply, **D-1**) | `applySnapshot` returns `(result, bool, err)` + tail `:94-100` (`recordAudit`) | `applySnapshot` → `(EventApplyResult, error)`; fail-closed head `apperr.Internal("event.audit_unavailable", ...)`; tail deleted; `recordAudit` stays for quarantine (`:121`) |

Affected tests: `internal/service/operation_audit_test.go:142-170` and `sync_anchor_test.go:89-112` assert
the `atomicAudit` bool — rewrite to assert the error-returning contract (audit wired → fact in tx, no
standalone Record; `s.audit == nil` → plain path, nil error).

Residual out-of-tx `s.audit.Record`/`s.audit.Log` calls after migration — the closed, justified,
always-checked set: `account.go:155,186` (sensitive reads) · `operation_state.go:39` · `replay.go:145,240` ·
`event_ingest.go:121` (quarantine) · message `deleteAlreadyGone` fact-only append. Never `_ =` —
`make check-audit-swallow` enforces (P1/P2 already include `Record` and `Log`).

### 3.5 Gate changes — mechanical wiring assertions (design §3.4, not yet present)

`scripts/check-audit-swallow.sh` gains a mechanical section asserting:

1. `drainPublications(` occurs exactly **once outside its definition (`callback_audit.go:45`) and outside
   `_test.go` files**, and that call site lies **inside `callbackService.Start`** — current location
   `internal/service/callback_worker.go:47` within the ticker loop `:29-51` (C-1: assert location, not the
   stale `callback.go:286`).
2. `NewAuditLedgerService(` is never called with a second argument in non-test Go.
3. `_ = publisher.Publish` remains 0 (already asserted today).

## 4. Compatibility constraints

1. **No schema, protobuf, or HTTP/gRPC API changes.** `audit_ledger`/`audit_publications` + UNIQUE
   `(fact_id)` (migration 000045) already exist; `AppendInTx` already creates the pending publication row.
2. **Compile-time-breaking constructor changes** are confined to the six live constructors; every call site
   is in the §3.1 inventory. Dead-tree `internal/message/repository.go` and the unwired
   `internal/{file,comment,block,notification,search}` tree are untouched.
3. **`s.audit == nil` behavior is byte-identical** for all ten mutations (unit-test wiring keeps plain
   paths; fail-closed heads only trigger when `s.audit != nil` and the repo lacks the WithAudit surface —
   impossible in the wired root, possible only for unit-test fakes, which the conversion rewrites).
4. **Anchored EventIDs byte-identical** for message delete, comment resolve, hold, and cutover — retry/dedup
   contracts (`deleteAlreadyGone`, resolve retry, hold/cutover crash-retry re-derivation) keep working.
5. **Data-shape change for the eight unanchored sites:** `audit_ledger.event_id` for these actions goes
   NULL → fresh UUIDv7. Column exists; no consumer depends on NULL; `List`/read paths unaffected.
6. **Unconditional append + short-circuit semantics preserved** (§3.2): no-op domain writes still record
   their fact; message-missing and zero-grantee paths still return before any append.
7. **Comment-resolve atomicity change (intended edge case):** a failing conditional hide now rolls the
   resolve back (today the resolve commits first). Report-not-found still wins when both fail.
8. **Message delete tx widens** from (soft delete + outbox event) to (soft delete + outbox event + fact).
   Outbox drain behavior unchanged; publication rows commit with the domain row.
9. **`Log` adapter retained** (`audit_ledger.go:136`) for the `deleteAlreadyGone` residual and legacy
   surface; the ten conversion sites stop calling it.
10. **`createWithAudit`/`applySnapshot` signature changes** (bool dropped) are internal; callers outside
    `operation.go`/`sync_job.go`/`event_ingest.go` and their tests do not exist.
11. **Single delivery path preserved:** `drainPublications` remains the only publisher path; the gate
    mechanical section locks it.
12. **Error codes:** new `ban|block|comment|message|file.audit_unavailable`,
    `account.profile_audit_unavailable`, `event.audit_unavailable`; codes surface through the standard
    wrap chain (e.g., `operation.create_failed` wrapping `operation.audit_unavailable` — the cause is
    preserved, matching today's `createWithAudit` flow).

## 5. Failure modes

| ID | Failure | Behavior after migration | Today |
|---|---|---|---|
| FM-1 | Nil/typed-nil ledger at wiring | `requireAuditLedger` panic at startup (fail fast) — six new constructors | Six repos silently carry no ledger |
| FM-2 | Non-duplicate append failure in-tx (ledger down, signing key missing, constraint) | Whole gorm tx rolls back: API error, **0 domain rows, 0 ledger rows, 0 publications** | Domain row commits, fact lost (swallowed) |
| FM-3 | `ErrAuditDuplicate` in-tx (retry with same event_id) | Swallowed after the domain write; mutation commits; exactly one fact | n/a (no in-tx append) |
| FM-4 | Crash between ledger-row insert and publication insert (trigger-injected; publication insert is the last tx statement) | Whole tx rolls back incl. ledger row — no ledger row without its publication, no mutation without its fact (proven by T-4 crash side) | Ledger row never existed for these sites |
| FM-5 | Crash after commit | `pending` publication drains via `drainPublications` (only delivery path); at-least-once, dedup-keyed; no commit-to-append gap remains for the 10 sites | Fact may be lost forever |
| FM-6 | `s.audit != nil` but repo lacks WithAudit surface (unit-test fakes only; impossible in root) | Fail-closed `*.audit_unavailable` error; no silent plain-path fallback | Silent fallback to swallowed Log |
| FM-7 | `deleteAlreadyGone` residual append fails | Checked `if err :=`; 503 `audit.ingest_failed`; retry re-derives the anchored event_id → dedup, exactly one fact | Same (unchanged) |
| FM-8 | Comment conditional hide fails in-tx | Resolve + fact roll back together (atomicity change); `comment.status_failed`/`comment.not_found` precedence; report-not-found wins when both fail | Resolve committed, hide failed after |
| FM-9 | Gate regression (`_ = s.audit.`, bare call, second `drainPublications` site, `NewAuditLedgerService` two-arg) | `make check-audit-swallow` red (P1/P2 + mechanical section) | Gate green at 0 today |
| FM-10 | `EventID: idgen.NewID()` reintroduced outside the C2 allowlist | `make check-anchored-event-ids` red (R-1) | Green |

## 6. Migration steps (each step leaves the tree green)

1. **Baseline:** full done gate — `make test && make lint && make lint-strict && make check-file-sizes &&
   make check-audit-swallow && make check-anchored-event-ids && make check-integration-compile && make build`
   — must be green before any change (verified today).
2. **Repository layer (§3.1 + §3.2):** first move the `fileShareRepo` construction below `buildAuditStack`
   in `new.go` (**F-1** — `auditLedgerRepo` is not in scope at the old `:132`; already applied to the tree,
   behavior-neutral), then add the ledger field + `requireAuditLedger` to the six constructors; add the six
   `*_audit.go` interface files with WithAudit implementations; update all §3.1 call sites (new.go — six
   sites, all now below `buildAuditStack` — plus 7 test call sites across 6 files). Gate: `make test` +
   `make test-integration` green (existing suites now construct with a ledger; new WithAudit methods
   covered in step 4).
3. **Service conversions (§3.3):** add `internal/service/audit_fact.go` + C2 allowlist entry; convert the
   ten sites with fail-closed heads; preserve message/report anchors; rewrite unit tests from
   `mocks.Auditor.LogFn` to fact-level assertions on captured `*WithAuditFn` arguments
   (`block_test.go:74,102`, `comment_test.go:559,622`, `message_test.go:571,599`, `file_share_test.go:42`,
   plus admin/ban service tests). Gate: `make test` green; `make check-audit-swallow` still 0;
   `make check-anchored-event-ids` green with the allowlisted helper.
4. **Tail deletion (§3.4):** delete the five tails; drop `atomicAudit` from `createWithAudit` (operation,
   sync) and `applySnapshot`; make sync-finish and profile-update fail-closed; add
   `event.audit_unavailable`/`account.profile_audit_unavailable`; rewrite `operation_audit_test.go:142-170`
   and `sync_anchor_test.go:89-112` to the error-returning contract. Gate: `make test` green.
5. **T-4 matrix (§7, REQ-5):** extend `audit_wiring_integration_test.go` (or per-repo siblings; create
   admin/ban suites where none exist) to the 16-class table. Gate: `make test-integration` green.
6. **T-5 wiring (§3.5, REQ-6):** add the mechanical gate section; add
   `TestDrainPublicationsOnlyFromStart` with a recording fake `AuditPublicationRepository`; confirm
   `TestAuditLedgerRecordEnqueuesOnlyIntegration` (`internal/app/audit_integration_test.go:278`) stays
   green. Gate: full done gate + `make test-integration` (repository SQL, outbox, and callback delivery
   are touched).
7. **Docs:** flip the requirements doc status to implemented; note D-1 in the change record.

## 7. Testable acceptance mapping

| Supplied acceptance | Requirement | Test artifact / pass criterion |
|---|---|---|
| **T-4** — crash-injection matrix over all migrated mutations | REQ-5 | `go test -tags=integration ./internal/repository/` — table-driven test enumerating the 16 mutation classes (profile update · operation create · operation step finish · event apply · callback register/delete/replay-reset · audit hold put/remove · legacy cutover set · account provisioning both variants · sync job create/finish · admin grant/revoke · ban/unban · block/unblock · comment resolve · message delete · file share grant/unshare) × {crash, commit}. Crash side (trigger from `installFailPublicationInsertTrigger` `:264-285`): error non-nil; fresh connection sees **0 `audit_ledger` + 0 `audit_publications` + 0 domain rows**. Commit side: **1 ledger row** (correct `event_id`/`action`) **+ 1 pending publication row + domain row**; file-share grant with N grantees → N/N/N. Keep `TestAuditWiringRollbackAtomicity` duplicate-injection assertions where Class A applies |
| **T-5** — gate stays green at 0; converted facts carry adapter-parity fields + `target_uid` | REQ-3.2, REQ-6.1 | `make check-audit-swallow` green (self-test + live scan, 0 hits). Unit tests rewritten to fact-level assertions: `ActorUID`, `Action`, `ResourceType`=SubjectType, `ResourceID`=SubjectID, `Payload`=Meta fields, plus `Payload["target_uid"]` present and equal to the legacy `TargetUID` — for all ten sites including admin/ban |
| **T-5 wiring** — `drainPublications` exactly one call site inside `Start`; `NewAuditLedgerService` single-arg; `Start` is the only `ClaimPublication` entry point | REQ-6.2, REQ-6.3 | Mechanical gate section (counts `drainPublications(` = 1 outside definition + `_test.go`; asserts that site lies inside `Start`; greps two-arg `NewAuditLedgerService(` = 0). New unit test `TestDrainPublicationsOnlyFromStart` with recording fake `AuditPublicationRepository`: claims happen from the ticker; no other service entry point triggers `ClaimPublication` (direct calls in `callback_audit_test.go:187,242,408` are test-only and not the proof). `TestAuditLedgerRecordEnqueuesOnlyIntegration` stays green: 1 `Record` → exactly 1 ledger row + 1 pending publication row, no governance delivery |
| **Anchored EventIDs** byte-identical for hold/cutover; message/report anchors preserved; fresh IDs confined to the allowlisted helper | REQ-7, REQ-3.3 | `make check-anchored-event-ids` green. Hold/cutover integration tests keep asserting same anchor inputs → same `event_id`; crash-retry → in-tx duplicate swallowed, exactly one fact. New assertions: message-delete and comment-resolve ledger rows carry the message-ID/report-ID `event_id` byte-identical to today's Log rows; the only `EventID: idgen.NewID()` sites in non-test Go are the C2 allowlist (now including `internal/service/audit_fact.go`) |
| Engineering gates | — | Each new repo method and service change ≤50 lines / cyclop ≤10 (straight-line tx wrappers); sibling `*_audit.go` files; `make check-file-sizes`, `make lint-strict` green |

**Done gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow
&& make check-anchored-event-ids && make check-integration-compile && make build` + `make test-integration`.

## 8. Scope boundaries (explicit non-goals)

- **Legacy unwired tree** (`internal/{file,message,comment,block,notification,search}` + `internal/audit`):
  not migrated, not scanned (documented gate exclusion). `internal/message/repository.go` keeps its own
  `NewMessageRepository` untouched.
- **`legacy_cutover.go:96-107` fallback tail (C-5 residual):** provably unreachable in the wired root;
  explicitly **not** added to scope. If removed later, it is a one-line mechanical cleanup in a separate
  task. (Note the asymmetry with D-1: the requirements doc made an explicit deferral decision for cutover
  but silently omitted event_ingest; this design honors the explicit decision and corrects the omission.)
- **Other batch directions:** dead-letter replay/requeue (B2-5), TokenSource 401 invalidation (B2-3),
  activation-gate e2e (B2-6) — out of scope (their landed gate additions — `Requeue*` in P1/P2,
  `purgeDeadLetters` — are kept).
- **Best-effort SSE hub** (`notification.go` bare `d.pusher.Publish`): not audit; remains excluded from P2.
- **`_ = s.receipts.QuarantineEvent`** (`event_ingest.go:120`): swallowed non-audit repo call; not part of
  this direction.
- **No new migrations, no protobuf changes, no HTTP/gRPC API changes, no data backfill** (the eight
  unanchored sites' historical NULL-event_id rows stay as-is; only new rows carry UUIDv7 event_ids).
