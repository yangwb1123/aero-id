# Design — internal/comment: anchored in-transaction audit coverage for every comment mutation (Create/Edit/Delete/Like/Unlike/Report/Hide/Unhide)

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-08-internal-comment-mutation-audit-coverage.md` (R0–R5; supplied acceptances T-4, T-5, T-10.1, T-10.2) |
| Predecessor (R0 dependency) | `docs/requirements/2026-08-08-internal-comment-resolve-report-with-audit.md` — ResolveReport conversion; **not landed at HEAD `b005409`** (no `audit_fact.go`, no `repository_audit.go`, no `ResolveReportWithAudit` in `internal/comment`, no `internal/comment` gate root; the doc is the only dirty tracked file in `git status`) |
| Verified at | HEAD `b005409` (working tree: `M docs/requirements/2026-08-08-internal-comment-resolve-report-with-audit.md`, `??` the mutation requirements doc + this design) |
| Verdict | **All 15 evidence items re-verified against the working tree — every one holds.** Confirmed by direct read: the eight mutation paths at `service.go:87-132,179-211,213-231,250-275,277-310,320-342` contain zero audit calls; the module's only audit site is the swallowed `_ = s.audit.Log(` at `service.go:431`; `internal/comment` is absent from the `check-audit-swallow` Makefile roots and documented as excluded in the script header; the `internal` root already makes `check-anchored-event-ids` live for the module; the two test mocks (`internal/comment/service_test.go:18-103`, `internal/search/service_test.go:55-94`) are the only `Repository` implementers outside the package; zero `comment.NewRepository`/`NewService` call sites exist outside the package; `internal/repository` non-test files import neither `comment` nor `search` (no cycle for the new import); all four repo precedents (`comment_audit.go:31-56`, `account_provision_audit.go:31-58`, `callback_audit.go:33-64`, `audit_wiring_integration_test.go`) match the spec's citations. Eight new findings (N-1..N-8) sharpen the design — see §1.** |

## 1. Independent verification of the evidence

Every cited file/symbol was re-checked against the working tree, not taken on trust.

| # | Claim | Independent check | Verdict |
|---|---|---|---|
| E1 | Mutations unaudited; corrected line ranges; only audit site `service.go:431` | Direct read of `internal/comment/service.go`: `Create` :87-132, `Edit` :179-211, `Delete` :213-231, `Like` :234-239, `Unlike` :242-247, `react` :250-275, `Report` :277-310, `Hide` :312-314, `Unhide` :316-318, `setStatusAsAuthor` :320-342, `ListReports` :344-369, `ResolveReport` :371-403, `logResolveAudit` :423-437. `grep -n audit` → field :20, option :37-40, `_ = s.audit.Log(` :431 only | ✅ Confirmed |
| E2 | `comment.go` — `Repository` :72-94 (16 methods), only `CreateWithReplyBump` is `*With*`; `Service` :96-109; `ErrNestingTooDeep` :17 | Direct read matches exactly | ✅ Confirmed |
| E3 | `domain/audit.go:30` — only comment-family constant | Read: `AuditReportResolve = "comment.report.resolve"` at :30 among 10 constants :27-36; `AuditEvent.ID` is `uuid.UUID`; quarantine note :13-21 enumerates four swallow sites incl. `comment/service.go:431` | ✅ Confirmed |
| E4 | `check-anchored-event-ids.sh` R-1/R-2 + roots | Read: R-1 bans `EventID: idgen.NewID()` with C2 allowlist; R-2 bans raw `uuid.NewSHA1/NewHash`, zero-part `Anchor()`, namespace constants; Makefile:77 roots `internal cmd` — the `internal` root descends into `internal/comment` (gate live for new code) | ✅ Confirmed |
| E5 | `idgen.Anchor` / `RenderAnchorTime` | Read `internal/pkg/idgen/idgen.go:28-46`: `Anchor` = `uuid.NewSHA1(AnchorNamespace, NUL-join)`, panics on zero parts/NUL; `RenderAnchorTime` = UTC, 6 fractional digits, `Z`; `AnchorNamespace` :16-20 | ✅ Confirmed |
| E6 | Live anchor precedent `comment_report_anchor_integration_test.go:27-61` | Read: `TestReportResolveAnchoredEventIDAppendsOnce` — 1 ledger + 1 publication row, second append → `ErrAuditDuplicate`, stable `chain_sequence`; helper `startAnchorLedger` at `anchor_dedup_integration_test.go:25` | ✅ Confirmed |
| E7 | Conditional-append precedent `account_provision_audit.go:31-58` | Read: `EnsureByIdentityWithAudit` appends only when `created`; rationale comment :5-8 | ✅ Confirmed |
| E8 | `callback_audit.go:33-64` in-tx `ErrAuditDuplicate` tolerance | Read: `CreateSubscriptionWithAudit` — domain write, then append with `!errors.Is(err, ErrAuditDuplicate)` swallow, rationale :47-50; `DeleteSubscriptionWithAudit` appends only when a row was deleted | ✅ Confirmed |
| E9 | Live `repository/comment_audit.go:31-56` | Read: `ResolveReportWithAudit` — report UPDATE (0 rows → `ErrNotFound`, no fact), hide leg without row check, `AppendInTx` last with in-tx tolerance | ✅ Confirmed |
| E10 | `audit_wiring_integration_test.go` helpers + AC3a/AC3b | Read: `auditWiringFact`, `countAuditLedgerRows`, `countAuditPublicationRows` :23-58; `TestAuditWiringHappyPath` :60+, rollback case :175+ | ✅ Confirmed |
| E11 | `drainPublications` at `service/callback_audit.go:45` + harness | Confirmed exact; harness `TestDrainExhaustsAttemptsIntoDeadLetter` :207-260 (seed subscription, `ledger.Append`, drive drains), `firstFactDrainHarness` :323, `recordingAuditPublisher` :310 | ✅ Confirmed |
| E12 | `service_test.go` mock :18-103; `TestCommentService_ResolveReportEmitsAuditEvent` :652-695; `logged.ID != reportID` "deterministic dedup anchor" | Read: mock fields :18-32; test asserts `logged.ID == reportID` at :690 with exactly that message; `mockAuditor` :718-725; `newService` helper :140 | ✅ Confirmed |
| E13 | Wiring: `internal/search` is the only non-test importer; zero constructions; no import cycle | `grep`: `search.go:27,33` (field/param), `handler_grpc.go:8,41`; `search/service_test.go` mock :55-94 (implements all 16 methods — second compile ripple); `grep -rn "comment.NewRepository\|comment.NewService"` outside the package → 0 hits; `grep -rln` of comment/search in `internal/repository/*.go` → 0 hits | ✅ Confirmed |
| E14 | `repository.go` 411 lines; repo struct :106-110 (`db`+`outbox` only); plain methods at cited lines; `CreateWithReplyBump` :122-147 | Direct read matches; also confirmed `SoftDelete` :220-229 (GORM `Delete`), `UpdateContent` :231-246, `Like` :248-273, `Unlike` :275-299, `CreateReport` :301-318 (`ON CONFLICT DO NOTHING`, 0 rows → `domain.ErrDuplicate`), `SetStatus` :320-331, `ResolveReport` :373-388 | ✅ Confirmed |
| E15 | AGENTS.md exemption "until migration" | Read AGENTS.md: comment create/edit/report listed among zero-audit compatibility surfaces exempt "only until migration" | ✅ Confirmed |

**New findings this verification adds (none contradict the spec):**

- **N-1 (funlen forcing):** `Create` is 46 lines today (with a `//nolint:cyclop`). The audit branch adds ~8-10 lines → ~54-56, over the 50-line funlen gate. The branch bodies must therefore be extracted as `*serviceImpl` methods in `internal/comment/audit_fact.go` (e.g. `createWithAudit`), not inlined. `Edit` (33), `Delete` (19), `react` (26), `Report` (34), `setStatusAsAuthor` (23) stay under 50 with inlined branches, but the extracted-method style is applied uniformly for consistency and to keep `service.go` (473) under 500 with all eight branches (inlining adds ~56 lines → ~529).
- **N-2 (soft-delete time threading is load-bearing):** plain `SoftDelete` uses GORM `Delete`, which mints its own `now` into `deleted_at`. The WithAudit variant must use `tx.Model(&commentRow{}).Where("id = ?", id).UpdateColumn("deleted_at", at)` so the stored `timestamptz` is byte-identical to `RenderAnchorTime(at)` (contract R7) — otherwise a replay re-deriving the anchor from the stored row gets a different EventID and the dedup fold never fires. GORM's soft-delete scope (`deleted_at IS NULL`) still applies to `Model(&commentRow{})` UPDATEs, so a second delete matches 0 rows → `domain.ErrNotFound`, no fact — matching the plain contract.
- **N-3 (like-row stamp):** plain `Like`/`Unlike` mint `time.Now().UTC()` for `commentLikeRow.CreatedAt`; the WithAudit variants stamp the threaded `at` so the stored row agrees with the anchor time part (same replay-stability argument as N-2).
- **N-4 (per-mutation retry-fold expectations differ):** the ledger-level fold (`ErrAuditDuplicate` tolerated in-tx) is reachable only for mutations whose second same-fact call reaches `AppendInTx`: create (new ID per attempt, fold via same-fact re-call), edit/delete/hide/unhide (0-row paths return `ErrNotFound` before append — the fold applies to same-fact re-calls on an existing row), like/unlike (second call is a *domain no-op*: `RowsAffected == 0` → nil, no append — 1 ledger row total by construction, not by fold), report (second call hits `ON CONFLICT DO NOTHING` → `domain.ErrDuplicate` 409, no append). The T-5 retry-fold case must therefore assert per-mutation outcomes; the raw `ErrAuditDuplicate` leg is covered by the T-4 table-driven `ledger.Append` test (8 EventIDs).
- **N-5 (error-class delta in `react`):** if the comment vanishes between the service's load and the like-count read inside the WithAudit tx, the repo returns a wrapped non-sentinel error; the audit-configured branch maps it to `503 audit.ingest_failed` (fail-closed) whereas the plain path maps it to `500 comment.react_failed`. Both are non-committing; documented as a deliberate, spec-mandated delta (§3 FM-6).
- **N-6 (in-tx mutation of the fact):** `AppendInTx` mutates the fact in place (fills `ID`, `ChainSequence`, `PreviousHash`, `HashVersion`, `PayloadHash`, `CreatedAt`, truncates `OccurredAt`). The service must build a fresh fact per call (callback_audit.go doctrine); unit tests must recompute the expected anchor from the mock-captured `at` *before/independently of* the repo call, since the mock does not mutate the fact.
- **N-7 (dedup key and tenant fallback):** `checkAuditDuplicate` keys `(tenant_id, source, event_id)` (`audit_ledger.go:180-191`). The dead tree has no tenant helper, so the fact's `TenantID` and `PartitionKey` fall back to the actor UID — deterministic per actor, so retries fold correctly; different actors never collide.
- **N-8 (Makefile root merge):** the `check-audit-swallow` root line (Makefile:69) currently ends with `internal/block` — the deleted-tree tripwire (the script tolerates missing roots, so the root scans empty). The `internal/comment` root lands on the same line per the predecessor R4.1; merge, do not overwrite.

## 2. Design

### D-1 — Action vocabulary (R1): eight constants in `internal/domain/audit.go`

The const block :27-36 gains, adjacent to `AuditReportResolve`:

```go
AuditCommentCreate = "comment.create"
AuditCommentEdit   = "comment.edit"
AuditCommentDelete = "comment.delete"
AuditCommentLike   = "comment.like"
AuditCommentUnlike = "comment.unlike"
AuditCommentReport = "comment.report.create" // report submission; resolution stays comment.report.resolve
AuditCommentHide   = "comment.hide"
AuditCommentUnhide = "comment.unhide"
```

House dotted `noun.noun.verb` vocabulary, consistent with `file.share.grant`, `comment.report.resolve`, `user.block`, `message.delete`. `comment.report.create` is distinct from `comment.report.resolve` so report submission and moderation resolution are separable ledger facts.

### D-2 — Dead-tree repository: seven `XxxWithAudit` methods in new sibling `internal/comment/repository_audit.go` (R2)

**Constructor and state (shared with predecessor R1.2 — merge, do not duplicate):** the `repo` struct (`repository.go:106-110`) gains `audit repository.AuditLedgerTransactionalRepository`; `NewRepository(db *gorm.DB, ob outbox.Repository, audit repository.AuditLedgerTransactionalRepository) Repository`. Zero external call sites (E13) — safe signature change. The new import `github.com/aero/aero-id/internal/repository` is needed for the interface type and the `ErrAuditDuplicate` sentinel identity; no cycle (E13). The plain methods stay byte-identical and remain the unconfigured service's path.

**Interface additions to `Repository` (`comment.go:72-94`)** — seven methods, `fact` appended **last** in the same GORM transaction as the domain write:

```go
CreateWithReplyBumpWithAudit(ctx context.Context, c *Comment, evt *outbox.Event, fact *domain.AuditFact) error
UpdateContentWithAudit(ctx context.Context, id domain.ID, content string, at time.Time, fact *domain.AuditFact) error
SoftDeleteWithAudit(ctx context.Context, id domain.ID, at time.Time, fact *domain.AuditFact) error
LikeWithAudit(ctx context.Context, commentID domain.ID, uid string, at time.Time, fact *domain.AuditFact) (likeCount int, err error)
UnlikeWithAudit(ctx context.Context, commentID domain.ID, uid string, at time.Time, fact *domain.AuditFact) (likeCount int, err error)
CreateReportWithAudit(ctx context.Context, r *CommentReport, fact *domain.AuditFact) error
SetStatusWithAudit(ctx context.Context, commentID domain.ID, status domain.CommentStatus, at time.Time, fact *domain.AuditFact) error
```

**Shared in-method contract** (each method, in order):
1. One `r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { ... })` containing the domain write(s) verbatim from the plain sibling, with the `at` timestamp threaded into the row where the plain method mints its own (N-2/N-3: `SoftDeleteWithAudit` uses `UpdateColumn("deleted_at", at)` instead of GORM `Delete`; `LikeWithAudit`/`UnlikeWithAudit` stamp `commentLikeRow.CreatedAt = at`; `UpdateContentWithAudit` already stores `at` as `edited_at`/`updated_at`).
2. **Conditional append** (account-provision doctrine): append only when the call is state-changing — `CreateWithReplyBumpWithAudit` always (insert affects 1 row); `CreateReportWithAudit` only on `RowsAffected == 1` (repeat → `domain.ErrDuplicate`, no fact); `LikeWithAudit`/`UnlikeWithAudit` only on `RowsAffected == 1` of the like-row insert/delete (repeat like / non-liked unlike → silent no-op, nil, no fact, count still returned); `UpdateContentWithAudit`/`SoftDeleteWithAudit`/`SetStatusWithAudit` only on `RowsAffected == 1` (0 rows → `domain.ErrNotFound`, no fact).
3. **Nil-audit guard** before the append: `if r.audit == nil { return fmt.Errorf("transactional audit repository is not configured") }` — the tx then rolls back (precedent `account_profile.go:97-99`).
4. `r.audit.AppendInTx(ctx, tx, fact)` with in-tx `ErrAuditDuplicate` tolerance **only after the domain writes, inside the same tx** (contract §4.2; verbatim `callback_audit.go:47-54`/`comment_audit.go:51-54` shape); any other append error wrapped `fmt.Errorf("append comment audit: %w", err)` rolls the whole tx back.

### D-3 — Dead-tree service: enable-flag branches, anchored facts, fail-closed (R3)

**Branch shape.** Each of the eight mutation paths (`Create`, `Edit`, `Delete`, `react` for Like/Unlike, `Report`, `setStatusAsAuthor` for Hide/Unhide) gains:

```go
if s.audit != nil {
    return s.<mutation>WithAudit(ctx, ...)  // helper in audit_fact.go: builds fact, calls XxxWithAudit, maps errors
}
// existing plain path verbatim, zero facts
```

`audit domain.AuditLogger` (:20) and `WithAuditLogger` (:37-40) remain the enable flag exactly as the predecessor established for `ResolveReport`; the mutation paths never call `Log`. The flag is non-nil only when explicitly wired (today: only tests — the module is unwired). `Repository` is a single interface, so the WithAudit surface is compile-time guaranteed — no live-twin-style type assertion needed.

**Fact builders in `internal/comment/audit_fact.go`** (predecessor's file; also absorbs the eight per-mutation branch helpers as `*serviceImpl` methods, N-1). Shared `buildAuditFactBase(ctx, actor, action, resourceType, resourceID, at)` sets `Source: "aero-id"`, `SourceRegion: "local"`, `ActorUID`, `ResourceType` (`"comment"` for all but report → `"comment_report"`), `ResourceID`, `OccurredAt: at`, and `TenantID`/`PartitionKey` from a tenant bound in context when present, else the actor-UID fallback (N-7; `validateAuditFact` requires both non-empty). Payloads are allow-listed, never content: `{"comment_id": ...}`; create adds `target_type`/`target_id`; hide/unhide add `status` (`"hidden"`/`"normal"`); report and edit carry only `comment_id` (reason never persisted — redaction).

**Per-mutation EventID policy (frozen part contracts; only the two R-1 classes):**

| Mutation | Action | EventID | Class |
|---|---|---|---|
| Create | `comment.create` | `c.ID` — pre-minted at `service.go:117` before the repo call | deterministic v7 receipt |
| Edit | `comment.edit` | `idgen.Anchor("comment.edit", tenantID, commentID.String(), actor, idgen.RenderAnchorTime(&at))` — `at` is the same `now` threaded into `UpdateContent` | Anchor, arity 5 |
| Delete | `comment.delete` | `idgen.Anchor("comment.delete", tenantID, commentID.String(), actor, idgen.RenderAnchorTime(&at))` | Anchor, arity 5 |
| Like | `comment.like` | `idgen.Anchor("comment.like", tenantID, commentID.String(), uid, idgen.RenderAnchorTime(&at))` | Anchor, arity 5 |
| Unlike | `comment.unlike` | `idgen.Anchor("comment.unlike", tenantID, commentID.String(), uid, idgen.RenderAnchorTime(&at))` | Anchor, arity 5 |
| Report | `comment.report.create` | `report.ID` — pre-minted at `service.go:297-302` and threaded into the row | deterministic v7 receipt |
| Hide | `comment.hide` | `idgen.Anchor("comment.hide", tenantID, commentID.String(), actor, idgen.RenderAnchorTime(&at))` | Anchor, arity 5 |
| Unhide | `comment.unhide` | `idgen.Anchor("comment.unhide", tenantID, commentID.String(), actor, idgen.RenderAnchorTime(&at))` | Anchor, arity 5 |

Rationale (load-bearing): the receipt class is safe only where one intent exists per resource ID — create (ID minted per attempt) and report (unique `(comment_id, reporter_uid)` makes a second intent fail with 409 before any append). The six repeatable-intent mutations encode action + tenant + resource + actor + the threaded intent timestamp so a retry of the **same** intent re-derives the same EventID (dedup fold) while a **different** intent (new `at`) cannot collide under `uq_audit_ledger_tenant_event` and roll back a legitimate write (the D3 hazard, `service/audit_fact.go:19-22`). Tenant occupies part 2 per house convention (`legacy_cutover`/`audit_hold`/`sync_audit`). Part contracts are frozen — `idgen` doctrine: never reuse a derivation site with a different part contract.

**Error mapping in the audit-configured branch** (each mutation keeps its existing domain-sentinel codes; only the append path changes):
- `domain.ErrNotFound` → the mutation's existing 404 (`comment.not_found` / `comment.report_not_found`); the repo appended nothing.
- `domain.ErrDuplicate` (report) → the existing 409 `comment.already_reported`; no fact.
- any other error (append failed → whole tx rolled back) → **`apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)`** — the B2 fail-closed contract (HTTP 503), byte-identical phrasing to the live layer's audit sites (`internal/service/account.go:161,191`) and the predecessor T-5. The domain write provably did not commit. (N-5: this changes the class of the react race-vanished error from 500 to 503.)

`ListReports`, `List`, `Replies` — read paths, unchanged, no facts. `ResolveReport` — predecessor R0's conversion only.

### D-4 — Gates and doc consistency (R5; shared with predecessor R4 — land once)

1. Makefile:69 `check-audit-swallow` root list gains `internal/comment` (append to the same line after `internal/block` — N-8).
2. `scripts/check-audit-swallow.sh` header exclusion note drops `comment` from `internal/{file,message,comment,notification,search,audit}`.
3. `internal/domain/audit.go` quarantine note (:13-21) — the four-site swallow enumeration becomes three (`internal/file/service_share.go:45,75; internal/message/service.go:289`).
4. `make check-anchored-event-ids` needs no change — the `internal` root already covers the module; new code uses only the two allowed classes.

### 2.1 API changes (complete list)

| Surface | Change | Ripple |
|---|---|---|
| `internal/domain/audit.go` | +8 action constants | none (compile-visible only) |
| `internal/comment/comment.go` | `Repository` interface +7 `*WithAudit` methods | 2 test mocks must gain the methods (compile) |
| `internal/comment/repository.go` | `repo` +`audit` field; `NewRepository` +third param | zero call sites outside the package (E13) |
| `internal/comment/repository_audit.go` (new) | 7 WithAudit methods | none |
| `internal/comment/audit_fact.go` (new, predecessor's file) | fact builders + 8 `*WithAudit` branch helpers | none |
| `internal/comment/service.go` | 8 audit-configured branches; plain paths byte-identical | behavior delta only when `s.audit != nil` |
| `internal/comment/service_test.go`, `internal/search/service_test.go` | mocks +7 methods (nil-default no-op) | test-only |
| Makefile / script header / domain quarantine note | gate root + doc accuracy (with predecessor) | none |

No handler, gRPC, HTTP, proto, migration, `AppendInTx`, `drainPublications`, or ledger-semantics changes. `Service` interface unchanged. `domain.AuditLogger` is the flag type only — no `Log` call anywhere in the module.

### 2.2 Compatibility constraints

- **Plain-path identity:** with `s.audit == nil` (the only reachable configuration today — the module is unwired), every mutation is byte-identical: same repo methods, same sentinel codes, same HTTP codes, zero facts. All existing plain-path tests pass unchanged.
- **Error-code stability:** 404/409 codes and messages are preserved in both branches. The only class delta is the react race case (500 → 503) and the append-failure case (previously nonexistent — plain path had no append).
- **No schema change:** the dead-tree repo operates on the shared `comments`/`comment_likes`/`comment_reports`/`outbox_events` tables (root `migrations/` 000003/000005/000007/000008); `audit_ledger`/`audit_publications` (000034/000040/000045) already exist. `testpg.Migrate` runs the whole tree — integration harnesses work as-is.
- **No new import cycles:** `internal/comment → internal/repository` is acyclic (E13); `internal/service` integration tests may import `internal/comment` (test-only, no cycle).
- **Idempotency story:** retry semantics ride the anchored EventID + in-tx fold + conditional append; no service-level idempotency keys are added (dead-tree surface, §5 of the spec).
- **Gate constraints:** file ≤ 500 lines (new siblings), functions ≤ 50 (N-1), cyclop ≤ 10, frozen anchor part contracts, no `EventID: idgen.NewID()` in non-test code, no swallowed/bare audit calls.

## 3. Failure modes

| # | Failure | Behavior | Protection / test |
|---|---|---|---|
| FM-1 | Ledger append fails (keyless/misconfigured ledger, DB down) | Whole tx rolls back; domain row unchanged; `503 audit.ingest_failed`; 0 ledger + 0 publication rows | T-5 rollback case per mutation (keyless-ledger injection) |
| FM-2 | Same-intent retry reaches `AppendInTx` | In-tx `ErrAuditDuplicate` fold → nil; exactly 1 ledger row; `chain_sequence` stable | T-5 retry-fold per mutation (N-4 expectations); T-4 dedup table (raw `ledger.Append`) |
| FM-3 | Part-contract bug: distinct intents collide under `(tenant, source, event_id)` | `ErrAuditDuplicate` rolls back a legitimate write (D3) | Frozen contracts + T-4 unit mirrors recompute the anchor exactly; `check-anchored-event-ids` |
| FM-4 | No-op domain write appends a fact (repeat like/report, 0-row update) | Ledger pollution: facts without state change | Conditional append (§2.3 step 2); T-5 no-op case asserts zero new facts |
| FM-5 | `NewRepository(db, ob, nil)` with WithAudit call | Nil guard → error → tx rollback (no orphan domain write) | Unit/integration guard path; service never constructs the repo |
| FM-6 | Append-failure class confusion in `react` (comment vanishes mid-tx) | Non-sentinel error → 503 (was 500 in plain path); nothing committed | Documented delta (N-5); covered by the generic fail-closed mapping test |
| FM-7 | Fail-open regression: plain path taken when audit configured | Forbidden: branch structure has no fallback; fail-closed unit asserts plain fn never invoked | T-4 fail-closed unit per mutation |
| FM-8 | Swallowed/bare audit call reintroduced in the module | `check-audit-swallow` (with `internal/comment` root) turns red, listing file:line | T-10.2 + standing gate |
| FM-9 | `EventID: idgen.NewID()` or zero-part `Anchor()` minted | `check-anchored-event-ids` turns red | T-4 + standing gate |
| FM-10 | Content/reason leaked into payload | Redaction contract violated | T-4 payload-key assertions (exact key sets, no content) + review |
| FM-11 | `TenantID`/`PartitionKey` empty | `validateAuditFact` fails → append error → 503 + rollback | Actor-UID fallback (deterministic); T-4 asserts non-empty |
| FM-12 | Anchor time part drifts from stored row (GORM mints its own `deleted_at`/`CreatedAt`) | Replay derives a different EventID → duplicate fact | `UpdateColumn("deleted_at", at)` / stamped `CreatedAt = at` (N-2/N-3); T-5 commit case round-trips `RenderAnchorTime` against the stored row |
| FM-13 | Interface break (mock not updated) | Compile failure in `internal/comment` + `internal/search` tests | Mechanical; caught by `make test` |
| FM-14 | Concurrent same-intent like (different `at`) | Different EventIDs, but the domain no-op suppresses the second append → 1 fact | By construction (conditional append); no action needed |

## 4. Migration steps

Ordered so the tree is buildable and gate-green at every point. **Step 1 must land first (R0)** — the predecessor conversion is the base this design builds on and the only fix for the T-10.2 P1 at `service.go:431`.

1. **Predecessor (R0) — `2026-08-08-internal-comment-resolve-report-with-audit.md` lands as its own change:** dead-tree `repo` audit field + `ResolveReportWithAudit`, `audit_fact.go` skeleton file, `ResolveReport` branch, `logResolveAudit` deletion, mock updates, Makefile `internal/comment` root + script header + quarantine-note updates, predecessor tests. Gate-green after this step: `bash scripts/check-audit-swallow.sh internal/comment` → 0.
2. **D-1:** eight constants in `internal/domain/audit.go`. Compiles alone.
3. **D-2:** `Repository` +7 methods (`comment.go`), `repo` audit field/constructor merge (already present from step 1 — extend, do not duplicate), new `internal/comment/repository_audit.go` with the seven methods. Do **not** commit this step without step 5 — the two test mocks fail to compile the moment the interface grows (keep steps 2-5 in one commit or apply mock fixes in the same working change).
4. **D-3:** `internal/comment/audit_fact.go` builders + eight branch helpers; `service.go` branches. `domain.AuditLogger` stays the flag; no `Log` call added.
5. **Mock ripple (mechanical):** `internal/comment/service_test.go` and `internal/search/service_test.go` mocks gain the seven methods as nil-default no-ops (`if m.XFn == nil { return ... }` style, mirroring `ResolveReportFn`).
6. **T-4 unit tests:** per-mutation mirrors of `TestCommentService_ResolveReportEmitsAuditEvent` (exactly one WithAudit invocation; action; `ActorUID`; `ResourceType`/`ResourceID`; `fact.EventID` == receipt ID or recomputed `idgen.Anchor(...)` from the mock-captured `at`; exact payload key sets; non-empty `TenantID`) + per-mutation fail-closed tests (WithAudit fake returns a non-domain error → `*AppError{Code: "audit.ingest_failed", HTTP: 503}`; plain fn never invoked).
7. **T-5 integration:** `internal/comment/repository_integration_test.go` (`//go:build integration`, `package comment`; `testpg.Start`/`Migrate`/`RepoRoot`, real `repository.NewAuditLedgerRepository(db, AuditLedgerConfig{SigningKey: ...})`, `NewRepository(db, ob, ledger)`; `outbox` nil for top-level creates or `outbox.NewGormRepository(db)` for replies). Per mutation: commit side (1 ledger + 1 pending publication + domain changed + stored row time round-trips `RenderAnchorTime`), rollback side (keyless ledger → error + domain unchanged + 0/0), no-op side (N-4 expectations), retry-fold side (per-mutation expectation, N-4). Plus the T-4 dedup table file (`anchor_dedup_integration_test.go` in the module): 8 EventIDs × `ledger.Append` twice → `ErrAuditDuplicate`, 1 ledger + 1 publication row, stable `chain_sequence`.
8. **T-10.1 drain:** `internal/service/comment_audit_drain_integration_test.go` (`//go:build integration`, `package service`): seed a comment row, append the fact through the module's own `UpdateContentWithAudit` (dead-tree repo over `testpg` DB + real ledger), build `NewCallbackService(...)` + `SetAuditPublicationRepository` + `SetAuditPublishers(recordingAuditPublisher)` + `svc.drainPublications(ctx)`; assert `completed`, publisher observed the anchored EventID + action, second drain claims nothing.
9. **Full gate run + docs** (§5, §7 of the spec).

## 5. Testable acceptance mapping

| Supplied acceptance | Design coverage | Testable pass criterion |
|---|---|---|
| **T-5**: per-mutation integration test — fact appended in the same GORM transaction as the domain write; rolling back removes both (pattern of `audit_wiring_integration_test.go`) | D-2 (shared contract), step 7 | `go test -tags=integration ./internal/comment/ -count=1`: for all 8 mutations (a) commit = domain state changed + exactly 1 `audit_ledger` row + 1 `pending` `audit_publications` row for the anchored event_id, and the stored row time (where stored) round-trips the anchored time part; (b) keyless-ledger call = error + domain row unchanged + 0 ledger + 0 publication rows; (c) no-op calls append zero facts with per-mutation outcomes (repeat like/unlike → nil, 1 ledger row total; repeat report → `ErrDuplicate`, 1 ledger row; 0-row update/delete/status → `ErrNotFound`, 0 ledger rows); (d) same-fact re-call → per-mutation fold (N-4), never > 1 ledger row |
| **T-4**: every new AuditEvent ID is anchor-derived (no `EventID: idgen.NewID()`); `scripts/check-anchored-event-ids.sh internal/comment` → 0; mirror of `TestCommentService_ResolveReportEmitsAuditEvent` (`logged.ID == reportID`, "deterministic dedup anchor") | D-3 (EventID policy), steps 6-7 | (a) `bash scripts/check-anchored-event-ids.sh internal/comment` exits 0; (b) `make check-anchored-event-ids` exits 0; (c) `go test ./internal/comment/ -count=1` — 8 unit mirrors assert `fact.EventID` == pre-minted `c.ID`/`report.ID` or `idgen.Anchor(...)` recomputed from the mock-captured `at` with the frozen part contract; (d) table-driven dedup: duplicate `ledger.Append` per EventID → `ErrAuditDuplicate`, 1 ledger + 1 publication row, stable `chain_sequence` |
| **T-10.1**: drainPublications-based delivery — facts land in `audit_publications`, drained by `internal/service/callback_audit.go:45`; duplicate append rejected via the ledger's unique receipt (`ErrAuditDuplicate`) | D-4/step 8 + dedup table | (a) `go test -tags=integration ./internal/service/ -run TestCommentMutationAuditFactDrains -count=1` — fact seeded via the module's own `UpdateContentWithAudit`, `drainPublications` settles the publication to `completed`, `recordingAuditPublisher` observed exactly one fact with the anchored EventID and `comment.edit` action, second drain claims nothing; (b) the §T-4 (d) table (8 EventIDs) |
| **T-10.2**: gate scan of `internal/comment` stays at 0 swallowed/bare audit calls | R0 + D-3 + D-4 | (a) `bash scripts/check-audit-swallow.sh internal/comment` exits 0 with 0 matches (requires the predecessor's `logResolveAudit` deletion); (b) Makefile:69 contains `internal/comment` and `make check-audit-swallow` exits 0; (c) mutation check: reintroducing `_ = s.audit.Log(` anywhere in the module turns the target red listing the file:line |

**Done gate:**

```bash
make test && make lint && make lint-strict && make check-file-sizes \
  && make check-audit-swallow && make check-anchored-event-ids \
  && make check-audit-manifest && make check-integration-compile && make build
bash scripts/check-audit-swallow.sh internal/comment      # exit 0, 0 matches
bash scripts/check-anchored-event-ids.sh internal/comment # exit 0, 0 violations
make test-integration
go test -tags=integration ./internal/comment/ -count=1      # T-5 matrix (a)-(d), T-4 dedup table
go test -tags=integration ./internal/service/ -run TestCommentMutationAuditFactDrains -count=1
```

## 6. Out of scope and dependencies

- **Dependencies:** predecessor R0 (step 1) is mandatory — T-10.2 cannot pass on today's tree (`service.go:431`); §2.2/§2.3 merge with its repo field, `audit_fact.go`, branch shape, and Makefile root (no duplication). Gate substrate (scripts, fixtures, Makefile targets) is already in the tree at HEAD `b005409` (unlike the quarantine campaign's uncommitted state — N-5 of that design does not apply here).
- **Explicitly out:** read paths (`List`, `Replies`, `ListReports`); `ResolveReport` (predecessor); the live layer (`internal/service/comment.go`, `internal/repository/comment{,_audit}.go`) — its mutations remain zero-fact legacy compatibility surfaces; `internal/search` (mock completion only); migrations/schema; handler/gRPC/HTTP contracts; `AppendInTx`, `drainPublications`, ledger semantics, `check-audit-manifest` (scans `internal/service` only); payload content fields; service-level idempotency keys; access-audit mode for comment reads (separate decision).
