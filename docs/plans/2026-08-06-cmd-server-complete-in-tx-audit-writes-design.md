# Design — Complete the in-transaction audit migration: all post-commit audit writes moved into AppendInTx/WithAudit, dead inline publisher loop kept deleted, no-swallow gate made meaningful

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` (210-line canonical mirror; REQ-1..REQ-7) |
| Module | `internal/app` composition root; spans `internal/service`, `internal/repository`, `scripts/check-audit-swallow.sh`, Makefile wiring |
| Head verified | `13e4676` + uncommitted working tree (39 modified files) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery; gate G3: T-4) |
| Date | 2026-08-06 |
| Status | Design — evidence re-verified against the live tree; supersedes the stale pre-implementation draft (which described the dead loop as still present). **Hardened by `2026-08-06-cmd-server-complete-in-tx-audit-writes-hardening.md` (C1–C6 corrections are load-bearing: R-1 allowlist for `buildLegacyAuditFact`, `new.go` fileShare construction-order fix, `buildAuditComplianceServices` param type, §3.2 unconditional-append wording, admin dual-field note, gate receiver-name constraint)** |

## 1. Evidence verification (untrusted claims vs. repository)

All five evidence claims were re-checked against the working tree. **All hold**, with line drift and three
material amplifications (F1–F3) that the design must carry.

| # | Claim | Verdict (proof) |
|---|---|---|
| 1 | Dead inline publisher loop already removed; `auditLedgerService` has no `publishers` field; `NewAuditLedgerService(repo)` single-arg; `Record` appends only; zero `_ = publisher.Publish` | ✅ `internal/service/audit_ledger.go:48-56` struct has only `repo` + `metrics`; constructor at `:56` single-arg; `Record` `:68-82` calls `repo.Append` only; `grep -rn "_ = publisher.Publish" internal/` = 0. Comment at `:53-54` documents the drain-only contract |
| 2 | Wiring holds: `buildAuditStack` passes no publishers; `wireAuditServices` wires publishers+publication repo into `callbackSvc` only; `drainPublications` has exactly one call site inside `Start` | ✅ `internal/app/audit.go:51` `NewAuditLedgerService(auditLedgerRepo)`; `wireAuditServices` `:59-75` → `SetAuditPublicationRepository`/`SetAuditPublishers` on callbackSvc only; `drainPublications` defined `internal/service/callback_audit.go:47`, sole call at `callback.go:282` inside the `Start` ticker (`callback.go:269-290`); `callbackService.Publish` `callback_audit.go:19-27` no-ops when the publication repo is wired |
| 3 | 10 swallowed post-commit `_ = s.audit.Log` sites remain; gate deliberately excludes `Log(` | ✅ Exactly 10 in `internal/service`: admin.go:93,120 · ban.go:83,119 · block.go:54,77 · comment.go:415 · message.go:313 · file_share.go:114,146. `scripts/check-audit-swallow.sh` P1/P2 method lists are `Record\|Publish\|FinishPublication\|ClaimPublication` — no `Log`; the negative fixture documents `_ = s.audit.Log(...)` as excluded. **Amplification F1:** all ten sites' repos (`adminRepo`, `banRepo`, `blockRepo`, `commentRepo`, `messageRepo`, `fileShareRepo`) have **no `audit` field and no ledger constructor parameter** (`repository/admin.go:31-35`, `ban.go:36-41`, `block.go:40-42`, `comment.go:64-72`, `message.go:63-73`, `file_share.go:32-34`). REQ-3 therefore requires **constructor API changes** (below), not just new methods — the evidence summary does not call this out |
| 4 | 2 checked-but-post-commit Records; 4 provably-unreachable `!atomicAudit` fallback tails | ✅ `legacy_cutover.go:97-108`, `audit_hold.go:69-81` and `:96-107` are checked but post-commit. Tails at `operation.go:135-142`, `sync_job.go:162-169`, `sync_job.go:466-471`, `account_profile.go:61-68` are unreachable in the wired root: `createWithAudit` (`operation_audit.go:19-28`, `sync_audit.go:19-28`) returns `false` only when `s.audit == nil` (tail guard `s.audit != nil` fails) or the repo lacks the audit interface — and all wired repos are the concrete structs (`NewOperationRepository`/`NewSyncJobRepository`/`NewAccountRepository`), which always implement it; `requireAuditLedger` (`repository/audit_wiring.go:19-28`) panics on nil, so a concrete repo always has a ledger. **Amplification F2:** the hold/cutover repos (`NewAuditHoldRepository(db)` `audit_hold.go:66`, `NewLegacyCutoverRepository(db)` `legacy_cutover.go:80`) also lack the ledger param; REQ-4 needs the same constructor change |
| 5 | T-4 crash-injection and wiring tests exist but only partially | ✅ `internal/repository/audit_wiring_integration_test.go`: happy path `:60`, rollback atomicity `:175`, publication-insert trigger injection `:268-310` (profile/operation/event coverage only). `internal/app/audit_integration_test.go:49` `TestAuditLedgerRecordEnqueuesOnlyIntegration` exists. Missing: per-mutation matrix (16 classes), `drainPublications`-only-from-`Start` test, mechanical wiring assertions. **Amplification F3:** no `admin`/`ban` repo integration tests exist today, and service unit tests assert `Log` calls via `mocks.Auditor` + `mocks.BlockRepo`-style fakes that lack `WithAudit` fns (e.g. `internal/service/block_test.go:74,102`) — the conversion rewrites those assertions to fact-level checks |

Additional verified facts the design relies on: `AppendInTx` interface `repository/audit_ledger.go:39`, impl `:106`; `createAuditRows` writes ledger row + `pending` publication row `:172-200`; UNIQUE `(fact_id)` on `audit_publications` (migration 000045); `ErrAuditDuplicate` returned by `checkAuditDuplicate` `:147-159` and translated from 23505 `:183-193`; `buildAuditStack` runs at `new.go:134`, before every repo construction it must feed (fileShare `:132`, msg `:141`, comment `:142`, block `:144`, ban `:145`, admin `:148`); `With*AuditLogger` options keep the `AuditLogger` field as the nil gate in all six services; `notification.go:281,294` bare `d.pusher.Publish` is the best-effort SSE hub (excluded); `internal/{file,block,comment,message}/…` still contain `_ = s.audit.Log` (unwired legacy tree — excluded).

## 2. Target architecture

```text
service mutation (admin/ban/block/comment/message/file_share/hold/cutover)
   │  s.audit != nil → type-assert repo.(XxxAuditRepository)  (fail-closed head)
   ▼
repo XxxWithAudit:  gorm tx { domain write(+outbox event where today) + ledger.AppendInTx }
   │
   └─ audit_ledger row + pending audit_publications row commit with the domain row
   └─ drainPublications (callbackService.Start ticker) is the ONLY delivery path
```

Post-migration, the only out-of-tx `s.audit.Record` calls are the justified, always-checked
read-path/standalone set (REQ-5.3): `account.go:155,186`, `operation_state.go:39`, `replay.go:145,240`,
`event_ingest.go:154`.

## 3. API changes

### 3.1 Repository constructors (8 signatures change; `requireAuditLedger` fail-closed precedent)

| Constructor | Old | New |
|---|---|---|
| `NewAdminRepository` | `(db)` | `(db, audit AuditLedgerTransactionalRepository)` |
| `NewBanRepository` | `(db)` | `(db, audit)` |
| `NewBlockRepository` | `(db)` | `(db, audit)` |
| `NewCommentRepository` | `(db, ob outbox.Repository)` | `(db, ob, audit)` |
| `NewMessageRepository` | `(db, ob, counterOpt ...*UnreadCounter)` | `(db, ob, audit, counterOpt ...*UnreadCounter)` |
| `NewFileShareRepository` | `(db)` | `(db, audit)` |
| `NewAuditHoldRepository` | `(db)` | `(db, audit)` |
| `NewLegacyCutoverRepository` | `(db)` | `(db, audit)` |

Each struct gains `audit AuditLedgerTransactionalRepository` and the constructor calls
`requireAuditLedger("NewXxxRepository", audit)` (panic on nil = fail at startup, matching
operation/sync/account/callback repos). Plain methods (`GrantRole`, `Ban`, `Block`, `ResolveReport`,
`SoftDeleteWithEvent`, `Grant`, `PutHold`, `SetCutover`, …) stay for tests and legacy callers —
`XxxWithAudit` variants are additive, like `Finish` vs `FinishWithAudit`.

### 3.2 New repository audit interfaces (sibling files, one cohesive concern per file)

| Interface (new file) | Methods (one gorm tx each: domain write → `AppendInTx`; `ErrAuditDuplicate` swallowed in-tx after the domain write) |
|---|---|
| `AdminAuditRepository` (`repository/admin_audit.go`) | `GrantRoleWithAudit(ctx, uid, roleCode, grantedBy string, fact *domain.AuditFact) error` · `RevokeRoleWithAudit(ctx, uid, roleCode string, fact) error` |
| `BanAuditRepository` (`ban_audit.go`) | `BanWithAudit(ctx, uid, actorUID, reason string, expiresAt *time.Time, fact) error` · `UnbanWithAudit(ctx, uid string, fact) error` |
| `BlockAuditRepository` (`block_audit.go`) | `BlockWithAudit(ctx, blocker, blocked string, fact) error` · `UnblockWithAudit(ctx, blocker, blocked string, fact) error` |
| `CommentAuditRepository` (`comment_audit.go`) | `ResolveReportWithAudit(ctx, reportID domain.ID, status domain.ReportStatus, resolverUID string, at time.Time, hideCommentID domain.ID, fact) error` — merges today's two-call service flow (report update + conditional comment hide) into one tx; `hideCommentID == zero` skips the hide. `RowsAffected == 0` on the report update returns `ErrNotFound` (same as plain `ResolveReport`) |
| `MessageAuditRepository` (`message_audit.go`) | `SoftDeleteWithEventAudit(ctx, id domain.ID, evt *outbox.Event, fact) error` — one tx: soft delete + outbox event (when `evt != nil && r.outbox != nil`) + fact |
| `FileShareAuditRepository` (`file_share_audit.go`) | `GrantWithAudit(ctx, fileID domain.ID, ownerUID string, granteeUIDs []string, facts []*domain.AuditFact) error` — N grant rows + N facts, one tx; `len(facts) == len(granteeUIDs)` (service guarantees) · `RevokeWithAudit(ctx, fileID domain.ID, granteeUID string, fact) error` |
| `AuditHoldAuditRepository` (`audit_hold_audit.go`) | `PutHoldWithAudit(ctx, hold *AuditHold, fact) error` · `RemoveHoldWithAudit(ctx, tenantID string, scopeType AuditHoldScope, scopeID string, fact) error` |
| `LegacyCutoverAuditRepository` (`legacy_cutover_audit.go`) | `SetCutoverWithAudit(ctx, cutover *LegacyCutover, fact) error` |

Unconditional fact append matches today's Log-after-call placement (facts record even when the domain
write was a 0-row no-op: revoke of absent role, unban of unbanned, unshare of unshared, resolve with no
status change). Short-circuits that today return before the Log site stay short-circuits (message delete
of a missing message, file share with zero grantees). `AuditFact` is appended exactly as built by the
service — the repo never re-derives anchors.

### 3.3 Service layer

**Fact building** — new shared helper `internal/service/audit_fact.go`:

```go
// buildLegacyAuditFact reproduces the Log-adapter mapping
// (audit_ledger.go:135-141: ActorUID, Action, SubjectType→ResourceType,
// SubjectID→ResourceID, Meta→Payload) plus TargetUID→Payload["target_uid"]
// (REQ-3.2) and a fresh EventID per call (REQ-3.3).
func buildLegacyAuditFact(ctx context.Context, actor, action, subjectType, subjectID, targetUID string,
	meta map[string]any) *domain.AuditFact
```

**Six ten-site services** (`admin.go`, `ban.go`, `block.go`, `comment.go`, `message.go`, `file_share.go`) —
replace the `if s.audit != nil { _ = s.audit.Log(...) }` tail with the landed fail-closed shape:

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

Per-mutation specifics: message delete keeps one tx (soft delete + outbox event + fact) via
`SoftDeleteWithEventAudit`; comment resolve passes `hideCommentID = rep.CommentID` when
`status == Resolved && c.Status == Normal` (the fact's `comment_id`/`status` payload unchanged); file
share builds one fact per grantee (`target_uid` = grantee) and calls `GrantWithAudit`; admin/ban/block
facts keep `Payload["role"]`, `{"reason","expiresAt"}`, etc. exactly as today's `Meta`. The `audit`
field stays (nil gate) — `With*AuditLogger` options and their wiring are unchanged. No `s.audit.Log`
call remains for these mutations (the `Log` adapter itself stays: `List`-adjacent legacy surface).

**Hold/cutover (REQ-4)** — same shape, but the facts keep the **existing anchored EventIDs** untouched
(`idgen.Anchor("legacy.cutover.set", tenant, status, previous)` and the 6-/4-part hold anchors): the
service builds the identical `AuditFactInput` it builds today and hands it to
`PutHoldWithAudit`/`RemoveHoldWithAudit`/`SetCutoverWithAudit`. Crash-retry re-derives the same anchor;
the in-tx `ErrAuditDuplicate` swallow (Class B) commits the domain write — exactly today's
`Record`+`ErrAuditDuplicate→nil` semantics minus the crash window.

**Fallback-tail deletion (REQ-5)** — delete the four tails; drop the `atomicAudit` bool from
`operationService.createWithAudit`/`syncJobService.createWithAudit`/`updateAccountProfile` (become
error-returning with fail-closed heads `if !ok { return apperr.Internal(...) }`); the sync-job finish
path becomes `s.audit != nil → repo.(SyncJobAuditRepository) else fail-closed`, plain `repo.Finish`
only when `s.audit == nil`. New error codes: `account.profile_audit_unavailable`, `sync.audit_unavailable`
(existing `operation.audit_unavailable` reused). With `s.audit == nil` (unit tests) the plain repo path
is byte-for-byte today's behavior.

### 3.4 No-swallow gate (REQ-2)

`scripts/check-audit-swallow.sh`:

- **P1** gains a receiver-based alternative: `_ = s\.audit\.` (any method, **including `Log`**), kept in
  union with the existing `_ = <recv>.(Record|Publish|FinishPublication|ClaimPublication)(` (which also
  catches `_ = publisher.Publish(` and `_ = s.publications.FinishPublication(`).
- **P2** gains `s\.audit\.` (any method) at line start — bare `s.audit.Log(ctx, e)` / `s.audit.Record(...)`
  statements also discard errors. The `d.pusher.Publish` SSE exclusion is untouched (`d.pusher` is not
  `s.audit`; `Publish` stays out of the P2 method list).
- **Fixtures**: positive fixture gains `_ = s.audit.Log(ctx, event)` and `_ = publisher.Publish(ctx, fact)`
  (self-test fails without them); negative fixture **drops** the documented "Log is excluded" line.
- Update the stale "Baseline on today's tree" comment (it still names the removed `audit_ledger.go:79`
  inline loop): baseline is now 10 `_ = s.audit.Log` hits (invisible to the old gate) + 0 others; target 0.
- **Mechanical wiring assertions (REQ-7.3)** — new section in the same script (Makefile/done-gate target
  unchanged): (a) `drainPublications(` occurs exactly once outside its definition, inside
  `callbackService.Start`; (b) `NewAuditLedgerService(` is never called with a second argument in
  non-test Go; (c) `_ = publisher.Publish` remains 0.

### 3.5 Composition root (`internal/app`)

- `new.go`: pass `auditLedgerRepo` (in scope from `buildAuditStack` at `:134`) into the six mutation
  repos (`:132,141,142,144,145,148`), and into **both** `NewLegacyCutoverRepository` sites
  (`new.go:228` and the duplicate seam inside `NewAccountHandlerWithExtensions` at `new.go:398`);
  `buildAuditComplianceServices` passes it to `NewAuditHoldRepository(gdb, auditLedgerRepo)`
  (`app/audit.go:147`).
- No changes to `buildAuditStack`/`wireAuditServices`/`callbackService.Start` — REQ-1 is keep-enforced.

## 4. Compatibility constraints

1. **HTTP/gRPC API surfaces unchanged.** Same request/response shapes, same error codes for the same
   causes. The only new failure class is a ledger-append failure failing the whole mutation (below).
2. **Plain repo methods and service constructors/options retained.** `GrantRole`, `Ban`, `Block`,
   `ResolveReport`+`SetStatus`, `SoftDeleteWithEvent`, `Grant`/`Revoke`, `PutHold`/`RemoveHold`,
   `SetCutover`, `NewXxxService`, `With*AuditLogger` all keep working — only the eight repo constructors
   gain a mandatory ledger parameter (compile-time breaking; all call sites are mechanical and listed in
   §6).
3. **Anchored EventIDs preserved byte-identical** for hold/cutover (same anchor inputs, same payload) —
   the B2-4 compatibility constraint. Fresh `idgen.NewID()` for the ten `Log`-derived facts: today those
   rows carry a NULL `event_id` (the `Log` adapter never sets one); after conversion they carry a unique
   UUID — dedup `(tenant_id, source, event_id)` can never fire on them, and `ErrAuditDuplicate` stays
   swallowed in-tx (Class B) so it cannot roll back a legitimate domain write (the D3 hazard the
   callback replay-reset fix resolved).
4. **Fact content parity + one additive key**: the ten converted facts equal the `Log`-adapter mapping
   plus `Payload["target_uid"]` (previously dropped by the adapter). Unit tests asserting audit events
   adjust for the added key; ledger payloads are versioned JSONB — no migration.
5. **Comment-resolve atomicity change (intended, edge case)**: today a failing conditional comment-hide
   (`comment.not_found`) surfaces *after* the report resolve already committed; in-tx the resolve rolls
   back with it. Report-not-found still wins when both fail (report update runs first).
6. **Fail-closed change (intended)**: non-duplicate append failure now fails the whole mutation (API
   error, no domain row without its fact) instead of committing the domain write and losing the fact
   (swallowed at 10 sites, surfaced post-commit at hold/cutover).
7. **Out-of-tx residual set unchanged and always checked**: `account.viewed`/`account.listed`,
   `operation.reconciled`, replay facts, quarantine record — never `_ =`.
8. **Exclusions hold**: legacy unwired tree (not scanned, not migrated), SSE `d.pusher.Publish`, dead-letter
   replay, TokenSource/401, activation gate, `QuarantineEvent` swallow — out of scope.
9. **No new migrations, no protobuf changes.** `audit_ledger`/`audit_publications` + UNIQUE `(fact_id)`
   already exist.
10. **Engineering gates**: new repo methods live in sibling `*_audit.go` files (≤500-line rule); each new
    function stays ≤50 lines / cyclop ≤10 (the WithAudit bodies are straight-line tx wrappers).

## 5. Failure modes

| Failure | Behavior after migration | Recovery |
|---|---|---|
| Ledger append fails (signing-key misconfig, DB error) in-tx | Whole mutation rolls back: 0 ledger rows, 0 publication rows, 0 domain rows; API returns the existing domain error code (repo wraps the tx error, service maps it) | Startup guard `validateAuditLedgerConfig` + `requireAuditLedger` panic catch config errors before traffic; transient DB errors retry at the client |
| `ErrAuditDuplicate` in-tx (anchored hold/cutover crash-retry) | Swallowed after the domain write inside the same tx; domain write commits, one fact exists | Ledger dedup by `(tenant_id, source, event_id)`; drain delivers the single publication row at least once (idempotent) |
| Crash between domain commit and append | **Eliminated** for all migrated mutations — the commit-to-append gap no longer exists | n/a |
| Mis-wired repo (nil ledger) | Constructor panic at startup (fail fast) | Composition-root test + `requireAuditLedger` |
| Non-concrete repo at runtime lacking the audit interface | Fail-closed `*.audit_unavailable` error; no silent plain-write fallback | Only reachable with a non-production repo (mocks); unit tests use `s.audit == nil` or implement the audit interface |
| Ledger partition lock contention | Each mutation takes one `pg_advisory_xact_lock` on its partition (actor-scoped); file-share N facts share one partition → one lock | Short critical sections; read path never appends |
| Delivery outage (governance endpoint down) | Facts are durable in `audit_publications` (pending); `drainPublications` retries with backoff/jitter, dead-letters after `MaxAttempts` | Manual replay path (existing); callback failure never erases the fact |
| Gate regression (someone reintroduces `_ = s.audit.Log` or `_ = publisher.Publish`) | `make check-audit-swallow` turns red (positive fixtures + live scan) | Gate is in the done-gate (AGENTS.md) |
| SSE false positive | Impossible: `d.pusher.Publish` is not an `s.audit.` receiver and `Publish` stays out of P2 | n/a |

## 6. Migration steps (each step leaves the tree green)

1. **Gate first (REQ-2/REQ-7.3).** Extend P1/P2, update fixtures (positive gains `_ = s.audit.Log(` +
   `_ = publisher.Publish(`; negative drops the Log exclusion), fix the baseline comment, add the
   mechanical wiring assertions. `make check-audit-swallow` must now **fail on the live tree with exactly
   10 hits** (self-tests still pass) — this proves the hole before the conversions close it.
2. **Repo layer (REQ-3/REQ-4 surface).** Add the `audit` field + constructor param + `requireAuditLedger`
   to the eight repos; add the eight audit interfaces + `WithAudit` methods in sibling files; update all
   constructor call sites: `internal/app/new.go` (9 sites: six mutation repos + cutover ×2 + hold via
   `app/audit.go:147`), and the repo
   integration tests that construct them (`block_integration_test.go:19`, `comment_test.go:22`,
   `message_test.go:22`, `file_share_integration_test.go:21`, `audit_hold_integration_test.go`,
   `legacy_cutover_integration_test.go`) — each passes a real ledger repo
   (`NewAuditLedgerRepository(db, cfg)`). Add commit-side integration assertions for the new variants
   (fact row + pending publication row land with the domain row).
3. **Service conversions (REQ-3/REQ-4).** Add `buildLegacyAuditFact`; convert the ten `Log` sites and the
   hold/cutover Records to the fail-closed WithAudit shape; delete the four fallback tails and the
   `atomicAudit` plumbing (REQ-5); add `*.audit_unavailable` codes. Rewrite the affected unit tests
   (`block_test.go:74,102`, `comment_test.go:559`, `message_test.go:571,599`, `audit_hold_test.go`,
   `legacy_cutover_test.go`, admin/ban/file tests): `internal/service/mocks` fakes gain `*WithAuditFn`
   fields; audit assertions move from `mocks.Auditor.LogFn` to the fact argument captured by the fake's
   WithAudit fn (assert Action/ActorUID/ResourceType/ResourceID/`target_uid`).
4. **Wiring (REQ-1/REQ-7).** new.go passes `auditLedgerRepo` into the eight constructors. Run
   `go build ./...`, `make test`, `make lint`, `make lint-strict`, `make check-file-sizes`.
5. **T-4 matrix (REQ-6).** Extend `internal/repository/audit_wiring_integration_test.go` (or per-repo
   siblings) with the table-driven crash-injection matrix: 16 mutation classes × {commit side, crash
   side} using the existing `BEFORE INSERT ON audit_publications` trigger mechanism. Add the
   `drainPublications`-only-from-`Start` unit test (`internal/service/callback_audit_test.go`, fake
   `AuditPublicationRepository` recording `ClaimPublication`).
6. **Full gate.** `make test && make lint && make lint-strict && make check-file-sizes &&
   make check-audit-swallow && make check-anchored-event-ids && make build` + `make test-integration`
   (repository SQL, migrations, storage, source connectors, event ingestion, callback delivery all
   touched). `make check-audit-swallow` reports 0 hits.

## 7. Testable acceptance mapping

| Supplied acceptance | Requirement | Test artifact / pass criterion |
|---|---|---|
| T-4 crash-injection, every migrated mutation | REQ-6 | `go test -tags=integration ./internal/repository/` — per-class table: crash side → error + 0 `audit_ledger` rows + 0 `audit_publications` rows + 0 domain rows; commit side → 1 ledger row (correct `event_id`/`action`) + 1 `pending` publication row + domain row; file-share grant commits N/N/N. Classes: profile, op create, op step finish, event apply, callback register/delete/replay-reset, hold put/remove, cutover set, provisioning (both variants), sync create/finish, admin grant/revoke, ban/unban, block/unblock, comment resolve, message delete, file share grant/unshare (16) |
| Gate fails on `_ = s.audit.` / `_ = publisher.Publish` | REQ-2 (+3/5) | `make check-audit-swallow` — positive fixtures incl. `_ = s.audit.Log(` + `_ = publisher.Publish(` (self-test red without them); live scan 0 hits post-conversion; a reintroduced swallow in any live service file turns it red; wired in the done-gate |
| Wiring: drain only from `Start`; `NewAuditLedgerService` no publishers; Record enqueues only | REQ-1, REQ-7 | `TestAuditLedgerRecordEnqueuesOnlyIntegration` stays green (1+1 rows, no delivery); new fake-publication-repo unit test: `Start` ticker drives `ClaimPublication`, no other entry point does; gate asserts single `drainPublications(` call site, single-arg `NewAuditLedgerService(`, zero `_ = publisher.Publish` |
| Anchored EventIDs preserved | REQ-4.2 | hold/cutover integration tests: same anchor inputs → same `event_id` in the ledger row; crash-retry re-derivation → in-tx duplicate swallowed, domain write commits, exactly one fact (existing dedup assertions extended to the in-tx path) |
| Fact parity + `target_uid` | REQ-3.2/3.3 | unit tests assert converted facts carry adapter-equivalent fields plus `Payload["target_uid"]` and a fresh non-zero `event_id`; existing audit-event assertions updated for the added key |
| Residual out-of-tx Records stay checked | REQ-5.3 | `grep` gate: the only `.audit.Record(` calls in `internal/service` are the five justified sites, all `if err :=`/`return` (never `_ =`); `make check-audit-swallow` enforces the swallow half |
