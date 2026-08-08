# Design — internal/comment: transactional report-resolution audit

- **Requirements:** `docs/requirements/2026-08-06-internal-comment-transactional-report-resolution-audit.md` (R1–R6)
- **Working tree:** HEAD `13e4676`; tree carries uncommitted prior-campaign changes (Makefile, `internal/app/audit.go`, AGENTS.md, go.mod) — this design targets the tree as checked, not bare HEAD.
- **Status:** evidence re-verified; design decisions D-A…D-F resolve gaps found during verification.

---

## 1. Evidence verification (all 14 claims checked against the tree)

| # | Claim | Verdict |
|---|---|---|
| 1 | `internal/comment/service.go:371-404` `ResolveReport`; commits at 387/394; `logResolveAudit` at 401 | ✅ exact |
| 2 | Swallow `_ = s.audit.Log` at service.go:431; `audit` field :22, `WithAuditLogger` :30-33 | ✅ exact |
| 3 | `SetStatus` repository.go:320-329, `ResolveReport` :373-387 — no tx, no audit; `repo{db,outbox}` | ✅ exact |
| 4 | `AppendInTx` iface :39, impl :106-114; fail-closed on empty key (:107) and nil tx (:110); creates ledger row **and** pending publication row (`createAuditRows`) | ✅ exact |
| 5 | Precedents `account_profile.go:101` (+nil guard :97-99), `event_receipt.go:180`, `operation.go:182`, `sync_job_audit.go:55`; all tolerate `ErrAuditDuplicate` in-tx after the domain write | ✅ (drift only: operation.go:178→182, sync_job def 43→55) |
| 6 | Pinned test `service_test.go:653-693` `TestCommentService_ResolveReportEmitsAuditEvent` | ✅ exact |
| 7 | Only `internal/search/*` imports `internal/comment`; zero `NewRepository` callers outside the package | ✅ exact |
| 8 | `Record` (audit_ledger.go:64-77) append-only; `TestAuditLedgerRecordEnqueuesOnly` + integration twin exist | ✅ exact (no new code needed) |
| 9 | `drainPublications` callback_audit.go:38-84 sole delivery path; `enqueueFact` reachable only from `publishAuditFact` | ✅ exact |
| 10 | Makefile:52-54 scans only `internal/service`; P1/P2 exclude the `Log(` form | ✅ exact |
| 11 | Live twin swallow block comment.go:407-426, `_ = s.audit.Log` :415; wired `new.go:202` `WithCommentAuditLogger(auditLedger)` | ✅ exact |
| 12 | `app/audit.go` — publishers only via `wireAuditServices`→`SetAuditPublishers`; `buildAuditPublishers` nil when `!cfg.AuditGovernance.Enabled`; `NewAuditLedgerService(repo)` gets none | ✅ (line drift 41-46→83/87-124) |
| 13 | `checkAuditDuplicate` zero-EventID short-circuit :157-159; `appendInTx` assigns `idgen.NewID()` :145 | ✅ exact |
| 14 | `domain/audit.go:10-11` "failed log must not fail the primary operation" | ✅ exact |

**Verified deltas vs. the evidence summary (all confirmed):** the dead inline publisher loop is gone; `check-audit-swallow` exists but is `Log`-blind and `internal/service`-only; the core defect (post-commit, swallowed, non-atomic audit) is live in both trees; the nine legacy `_ = s.audit.Log(` sites are in non-comment services and stay untouched; exactly **two** `Log`-swallow shapes exist in the comment scope (parallel :431, live :415) — the extended gate is green at 0 post-change.

**Gaps found during re-verification that the requirements spec does not resolve (decisions D-A…D-D):**

- **D-A — `TenantID` is mandatory.** `validateAuditFact` (audit_ledger.go:266-270) rejects a fact with empty `TenantID`, `PartitionKey`, `Source`, `SourceRegion`, `Action`, or zero `OccurredAt`. R2.1's fact-build list omits `TenantID`. The live twin inherits tenant plumbing via `bindAuditContext`→`TenantFrom` (service-private `identityKey`, `*domain.TokenIdentity`). The parallel tree has **no** tenant-from-context facility (`internal/domain` offers only `WithActor`/`ActorFrom`/`MustActor`; `TokenIdentity.TenantID` exists but nothing stores it in ctx outside `internal/service`). Without a tenant source, every parallel-tree append fails validation.
- **D-B — 404-code preservation needs a stage sentinel.** Today the service distinguishes `comment.report_not_found` vs `comment.not_found` by *call site* (`ResolveReport` vs `SetStatus`). A combined method returns one error; both writes fail with the same `ErrNotFound` identity. Preserving both 404 codes requires a second sentinel for the hide step.
- **D-C — negative-fixture conflict.** `scripts/testdata/audit-swallow/negative/fixture.go` contains the exact line `_ = s.audit.Log(ctx, event) // legacy diagnostics surface: excluded` (deliberate P1/P2 exclusion). A P3 self-test asserting "negative fixtures must not match" would fail on that line; the fixture must be reshaped.
- **D-D — partition-scheme divergence.** The live layer prefixes partitions with tenant (`auditTenantPartition` → `"tenant:partition"`, audit_ledger.go:161-163); the parallel tree cannot reuse it (unexported). The parallel fact-builder must mirror the prefix locally so both trees partition chains identically.

---

### 1.1 Decision (resolves D-A/FM-4): empty-tenant precondition is the existing `tenant.required` 403 pre-check, not a 500

Verification of tenant-less-token reachability in this deployment (HEAD `13e4676`, checked tree):

- **SSO adapter claim guarantees.** `SSO.ValidateToken` returns only `uid` + `expires_at_unix` (`api/proto/sso/v1/sso.proto:16-19`). `sso.Client.ValidateIdentity` (client.go:196-202) fabricates `TokenIdentity{UID, Subject}` — **`TenantID` is always `""` on the upstream gRPC validation path, for every token, regardless of its claims**. The local JWKS validator hardcodes `RequireTenant: true` (`internal/app/auth.go:19`), so tokens without a tenant-family claim are rejected 401 at the auth gate there.
- **Reachability per deployment mode.** The checked-in `configs/config.yaml:100` sets `jwks_url: ""` ("empty keeps gRPC validation") — the default deployment is the upstream path, so **every** authenticated request carries `TenantID == ""`, and `Auth` (middleware/auth.go:36-50) and the gRPC interceptor (server.go:261-268) always inject `WithIdentity` — tenant-less identities reach the comment resolve handler for **100% of requests in the default mode**. In JWKS mode they cannot pass auth at all.
- **Today's defect is systematic in the default mode, not crash-window.** The swallow `Log` → `Record` → `buildAuditFact` (`bindAuditContext` only fills non-empty tenant) → `Append` → `validateAuditFact` rejects empty `TenantID` → error swallowed. Every resolve in the default mode silently drops its fact today.
- **Decision: gate the wired resolve with the existing `mustTenant` pre-check (403 `tenant.required`); keep the 500 + rollback as the backstop for genuine append failures.** The 500 was framed as a regression signal, but in the default deployment empty tenant is the adapter's steady state, not a regression — a 500 would misreport an endpoint-wide denial as a server fault. The 403 matches the shipped `WithAudit` precedent exactly: `account_profile.UpdateProfileWithAudit` is gated by `ensureWrite` → `verifiedAccountIdentity` → `mustTenant` (account_provision.go:77-93) before any append; the repo-level append failure → 500 is the backstop there too. Same tiering as the ledger's own `List`/`VerifyChain` and every other tenant-scoped service (account, operation, callback, sync_job, target_authorization). Fail-closed is unaffected: `validateAuditFact` still rejects empty-tenant facts as defense in depth, so no path can commit a domain row without its ledger row; no new error codes (`tenant.required`, `comment.resolve_failed` both exist).
- **Deployment consequence (PR note):** with the pre-check, comment resolve — like account, operation, callback, sync_job, target_authorization — requires a tenant-bearing identity; the default upstream-validation mode cannot provide one until the SSO adapter plumbs tenant (the `ValidateTokenResponse` proto carries no tenant field). The documented direction (AGENTS.md: prefer local JWKS verification) is the path to a functioning deployment; the pre-check makes the constraint explicit instead of papering it over with a 500.

## 2. API changes

### 2.1 Parallel tree — `internal/comment` (unwired, zero external callers)

**`internal/comment/comment.go`** — `Repository` interface (line 72) gains:

```go
// ResolveReportWithAudit transitions the report to a terminal status, optionally
// hides the comment, and appends the audit fact atomically in one transaction.
// hide is nil when no comment status change is requested. Returns ErrNotFound if
// the report is missing, ErrCommentNotFound if the comment vanished for the hide
// step; any audit append failure rolls back all writes.
ResolveReportWithAudit(ctx context.Context, reportID domain.ID, status domain.ReportStatus,
    resolverUID string, at time.Time, hide *domain.CommentStatus, fact *domain.AuditFact) error
```

**`internal/comment/repository.go`**:
- `repo` struct gains `audit repository.AuditLedgerTransactionalRepository` (new import `internal/repository`; cycle-free — nothing in `internal/repository` imports `internal/comment`).
- `NewRepository(db *gorm.DB, ob outbox.Repository, audit repository.AuditLedgerTransactionalRepository) Repository` (zero construction sites to update — verified).
- New exported sentinel: `var ErrCommentNotFound = errors.New("comment not found")` (mirrors `repository.ErrNotFound`, db.go:15).
- New method (≤50 lines; extract a `updateReportAndHide` helper if it overflows): single `r.db.WithContext(ctx).Transaction` containing, in order: (1) report UPDATE identical to `ResolveReport` (:373-387), `RowsAffected == 0` → `domain.ErrNotFound`; (2) if `hide != nil`, comment status UPDATE identical to `SetStatus` (:320-329), `RowsAffected == 0` → `ErrCommentNotFound`; (3) `r.audit.AppendInTx(ctx, tx, fact)` with nil-audit fail-closed guard (`fmt.Errorf("transactional audit repository is not configured")`, precedent account_profile.go:97-99) and `ErrAuditDuplicate` tolerance **only after** the domain writes (contract §4.2, precedent operation.go:178-184).

**`internal/comment/service.go`**:
- `serviceImpl` loses the `audit domain.AuditLogger` field (:22); `WithAuditLogger` (:30-33) deleted (sole consumer was the pinned test).
- `ResolveReport` keeps reads/validation/authz; **adds the tenant pre-check right after `domain.MustActor`: `tenantFrom(ctx) == ""` → `apperr.Forbidden("tenant.required", ...)`** (§1.1; mirrors `mustTenant`, before any DB read). Then: compute `hide` exactly as today (`status == Resolved && c.Status == Normal` → `&domain.CommentStatusHidden`); build the fact via a new package-local `buildResolveFact(ctx, actor, reportID, rep, c, status, now) *domain.AuditFact` implementing **D-A/D-D** (below); call `s.comments.ResolveReportWithAudit(...)`.
- `logResolveAudit` (:423-436) deleted — the swallow surface is gone, not converted.
- Error mapping: `errors.Is(err, ErrCommentNotFound)` → `apperr.NotFound("comment.not_found", ...)`; `errors.Is(err, domain.ErrNotFound)` → `apperr.NotFound("comment.report_not_found", ...)`; else `apperr.Internal("comment.resolve_failed", ...).Wrap(err)`.

**`internal/comment/context.go` (new, sibling file — keeps service.go ≤500) — D-A resolution:**

```go
type ctxKey int
const tenantKey ctxKey = iota
// WithTenant attaches a tenant id for audit fact building (unwired tree; the live
// identity plumbing lives in internal/service and is not importable here).
func WithTenant(ctx context.Context, tenantID string) context.Context
func tenantFrom(ctx context.Context) string
```

`buildResolveFact` mirrors the live normalization locally: `Source: "aero-id"`, `SourceRegion: "local"` (defaults only — ledger requires non-empty), `TenantID: tenantFrom(ctx)` (empty → the append fails validation; see §4 FM-4), `PartitionKey: auditTenantPartition(TenantID, partition)` with local `auditTenantPartition` (D-D) and partition = tenant-else-actor, `ActorUID: actor`, `Action: domain.AuditReportResolve`, `ResourceType: "comment_report"`, `ResourceID: reportID.String()`, `OccurredAt: now`, `Payload: {"comment_id": rep.CommentID.String(), "status": "resolved"|"dismissed"}` (exact vocabulary of :431-435), `EventID` left zero (D-F).

### 2.2 Live tree — `internal/repository`, `internal/service`, `internal/app`

**`internal/repository/comment.go`**:
- `CommentRepository` interface gains `ResolveReportWithAudit` (same signature/contract as §2.1, but returning `repository.ErrNotFound` / new `repository.ErrCommentNotFound`).
- `commentRepo` gains `audit repository.AuditLedgerTransactionalRepository`; `NewCommentRepository(db *gorm.DB, ob outbox.Repository, audit AuditLedgerTransactionalRepository) CommentRepository`.
- New sentinel `repository.ErrCommentNotFound`; same tx body as §2.1.

**`internal/service/comment.go`**:
- `commentService` loses `audit AuditLogger` (:20) and `WithCommentAuditLogger` (:26-29); the swallow block :407-426 is deleted.
- `ResolveReport` (:367) **adds `mustTenant(ctx)` right after `mustActor`** (§1.1 — 403 `tenant.required` gates before any DB read; precedent account_provision.go:78-84), then builds the fact with the shipped `buildAuditFact(ctx, AuditFactInput{...})` (audit_ledger.go:80-95 — inherits `bindAuditContext` tenant/actor/trace fill, `normalizeAuditInput` defaults + tenant-prefixed partition, `redactAuditPayload`; precedent account_profile.go:48). Mapping: `repository.ErrCommentNotFound` → `comment.not_found`, `repository.ErrNotFound` → `comment.report_not_found`, else `comment.resolve_failed` (+ `ObserveAuditIngestFailure` on the append-error branch, G-4).

**`internal/app/new.go`**: `NewCommentRepository(gdb, outboxRepo, auditLedgerRepo)` (:142 — `auditLedgerRepo` already in scope from `buildAuditStack` :134; `FullAuditLedgerRepository` embeds `AuditLedgerTransactionalRepository`); drop `service.WithCommentAuditLogger(auditLedger)` (:202).

**`internal/service/mocks/mocks.go`**: `CommentRepo` (:271) gains `ResolveReportWithAuditFn` + method (default no-op, mirroring `ResolveReport` :366-370).

### 2.3 Unchanged surfaces (hard boundary)

- `Service.ResolveReport` signatures (both trees), HTTP/gRPC handlers, error-code set, proto, config, migrations.
- Plain `ResolveReport`/`SetStatus` repo methods (still used by `Hide`/`Unhide` and kept per AGENTS.md compatibility rule).
- `auditLedgerService.Record`, `drainPublications`, `ClaimPublication`/`FinishPublication`, `AppendInTx`, `AuditLogger` contract, the nine legacy `Log` diagnostics sites.
- Parallel tree remains unwired; this direction only adds call sites.

### 2.4 Gate — `scripts/check-audit-swallow.sh` + Makefile

- Makefile:54 → `bash scripts/check-audit-swallow.sh internal/service internal/comment`.
- Script: P1/P2 scan all roots (unchanged patterns); new **P3** `^[[:space:]]*(_ = |_, _ = )[A-Za-z_][A-Za-z0-9_.]*\.Log\(` applied **only** to the comment scope (`internal/comment/**` + `internal/service/comment.go`), documented in the header as deliberate (nine legacy `Log` sites elsewhere are contract-compliant).
- **D-C fixture reshape**: delete `_ = s.audit.Log(ctx, event)` from `negative/fixture.go`; add a positive `log_fixture.go` with the swallow shape; add `if err := s.audit.Log(ctx, event); err != nil { return err }` to the negative fixture (anchoring at line start keeps P3 off `if err :=`). Self-test: positives match P1|P2|P3 ≥1; negatives match 0.

---

## 3. Compatibility constraints

1. **Behavior change (intended, must be called out in the PR):** an audit append failure now surfaces as `500 comment.resolve_failed` and rolls back the moderation state, instead of being swallowed post-commit. Compliance facts are authoritative; the mutation cannot exist without its fact.
2. **New precondition on the wired path:** `ResolveReport` now requires a tenant-bearing identity in context (`TenantID != ""`), enforced by the existing `mustTenant` pre-check → 403 `tenant.required` (FM-4), **not** a 500 — the ledger's empty-tenant rejection remains the backstop. Unit tests must use `service.WithIdentity` / parallel `comment.WithTenant`; the empty-tenant unit leg asserts 403, not 500 (T-5.1b).
3. **404 codes preserved:** the hide-step race maps to `comment.not_found` via the new stage sentinels; no observable code change in the non-race path.
4. **Retry duplicate facts (parity, not regression):** without EventID anchoring a client retry appends a second fact — exactly what the current post-commit `Log` path does; dedup is direction #2 (out of scope).
5. **No DB migration:** `audit_ledger` (000044) and `audit_publications` (000045) exist; the change adds call sites only.
6. **Constructor churn:** `NewCommentRepository` gains a parameter at its three sites (new.go:142, comment_test.go:22, search_integration_test.go:27); `NewRepository` has zero sites. Accept `nil` audit for tests; fail closed at call time (precedent).
7. **File limits:** 472/411/483/464 → additions are net-neutral; `context.go` absorbs the parallel tenant helper; new repo method ≤50 lines (funlen), ≤10 cyclomatic (extract `updateReportAndHide` if needed); `//nolint:cyclop` on live `ResolveReport` retained only if complexity is unchanged.
8. **Import direction:** `internal/comment` → `internal/repository` is acyclic (verified); `internal/comment` must not import `internal/service` (unexported helpers; keeps the dead tree decoupled).

---

## 4. Failure modes

| FM | Trigger | Behavior | Detection |
|---|---|---|---|
| FM-1 | `AppendInTx` fails inside the resolve tx (key unset, DB error, validation) | Whole tx rolls back; report stays `open`, comment stays `normal`; 500 `comment.resolve_failed`; **no silent fact loss** | T-5.1 (real Postgres), T-5.1b (mapping) |
| FM-2 | Nil audit dependency (mis-wiring, nil passed to constructor) | `ResolveReportWithAudit` errors before any write → 500; production composition always passes the ledger repo (`new.go:142`), so unreachable in prod | unit: nil-repo fake returns the "not configured" error; wiring is compile-enforced by the signature |
| FM-3 | Concurrent deletion of report/comment between read and tx | `RowsAffected == 0` on the report → `ErrNotFound` → 404 `comment.report_not_found`; on the hide step → `ErrCommentNotFound` → 404 `comment.not_found`; rollback | T-5.1b |
| FM-4 | Empty/missing tenant in ctx on the wired path | **403 `tenant.required` via `mustTenant` (live) / local mirror (parallel), before any DB read** (§1.1). The ledger's `validateAuditFact` empty-tenant rejection remains the backstop, so no empty-tenant fact can ever be recorded | T-5.1b empty-tenant ctx leg asserts 403 `tenant.required` (both trees); e2e uses `service.WithIdentity` with a tenant |
| FM-5 | Concurrent resolves of the same report / same partition | Row locks serialize same-report resolves; `pg_advisory_xact_lock(hashtext(partition))` serializes same-partition appends. No new deadlock cycle: `AppendInTx` never touches comment rows, and standalone `Append` never takes comment-row locks | integration stress (optional); lock analysis documented |
| FM-6 | Drain failure after commit | Fact + pending publication committed; worker retries with backoff/lease; `FinishPublication` terminal after cap → `dead_letter`; callback failure never rolls back the fact | T-5.2(e) only asserts the success path; drain semantics untouched (out of scope) |
| FM-7 | Gate regression: `_ = s.audit.Log(` reintroduced in comment code | `make check-audit-swallow` exits non-zero listing file:line | T-4.1(c) mutation check |
| FM-8 | Ledger outage blocks moderation writes | Resolve fails fast (500) rather than losing the fact; degradation is intentional per audit authority | metrics: audit ingest failures already observed via `Record`; comment path should reuse `ObserveAuditIngestFailure` (live twin only — parallel tree has no metrics) |

**G-3 typed-nil confirmation (FM-2).** A typed-nil ledger (`(*auditLedgerRepo)(nil)` stored in the interface) passes the `r.audit == nil` guard and panics on the first statement of `AppendInTx` (`len(r.signingKey)` dereferences the nil receiver), inside the caller's `gorm.Transaction` closure. GORM v1.25.11 rolls back on panic (`panicked := true` + deferred rollback, finisher_api.go:620-644), so the domain writes never commit and no fact is lost; the panic surfaces through the HTTP `middleware.Recovery` (router.go:59) / gRPC `RecoveryInterceptor` (server.go:393) as a 500 — a panic, not the clean "not configured" error, but **fail-closed in every case**. Unreachable in prod: `validateAuditLedgerConfig` blocks keyless startup, `buildAuditStack` always constructs a concrete non-nil repo, `wireAuditServices` panics on wiring mismatch, and `new.go:142` passes the typed `FullAuditLedgerRepository`. Identical exposure exists in the shipped account-profile precedent (`updateProfile`'s `r.audit == nil`), so the design is at parity; no extra guard required (optional hardening: a reflect-based typed-nil check in `ResolveReportWithAudit` — one line, removes the panic path for tests only).

---

## 5. Migration steps (code-only; no DB migration)

Ordered, each step independently green:

1. **Gate first (T-4.1, D-C):** extend `check-audit-swallow.sh` with P3 + reshaped fixtures + Makefile roots; run — the two existing comment swallows make it **red**, proving the gate bites before the fix.
2. **Repositories:** add `ResolveReportWithAudit` + sentinels + constructor param to both `internal/comment/repository.go` and `internal/repository/comment.go`; update `new.go:142`, `repository/comment_test.go:22`, `search_integration_test.go:27`.
3. **Live service:** `internal/service/comment.go` — add `mustTenant` pre-check, delete swallow block/field/option, build fact via `buildAuditFact`, map errors; `new.go:202` drops the option; `mocks.go` gains `ResolveReportWithAuditFn`; `service/comment_test.go:559` converted.
4. **Parallel service:** `internal/comment/service.go` + new `context.go` — add the `tenant.required` pre-check, delete `logResolveAudit`/field/option, build fact via `buildResolveFact`, map errors; rewrite `service_test.go:653-693` (R6).
5. **Integration tests:** T-5.1 (`internal/repository/comment_audit_integration_test.go`, keyless-ledger recipe of callback_audit_integration_test.go:124, `countCallbackAuditRows` style); T-5.2 (e2e in `internal/service`, package `service`, tag `integration`).
6. **Done gate + PR note** (§7); document the FM-2/4 behavior change in the PR description.

---

## 6. Testable acceptance mapping

| Acceptance (requirements) | Test | File / mechanics | Assertions |
|---|---|---|---|
| T-5 atomicity: append failure rolls back report UPDATE + hide | T-5.1 | `internal/repository/comment_audit_integration_test.go` (tag `integration`, `internal/repository/testpg`); real `NewAuditLedgerRepository` with empty `SigningKey` (deterministic `AppendInTx` failure; precedent `TestCallbackAuditRollbackWithoutSigningKey`) | (a) call returns the key error; (b) report row `status=open`, `resolved_at IS NULL`; (c) comment row `status=normal`; (d) 0 `audit_ledger` + 0 `audit_publications` rows for the fact |
| T-5 service mapping | T-5.1b | `internal/comment/service_test.go` + `internal/service/comment_test.go` — fakes' `ResolveReportWithAuditFn` return wrapped append error / `ErrNotFound` / `ErrCommentNotFound`; plus an empty-tenant ctx leg (no fake call needed — pre-check fires first) | append error → `apperr.Internal` code `comment.resolve_failed` (surfaced, not swallowed); `ErrNotFound` → 404 `comment.report_not_found`; `ErrCommentNotFound` → 404 `comment.not_found`; **empty-tenant ctx → 403 `tenant.required` and the repo fake is never invoked** |
| T-5 e2e: one ledger + one publication row; delivery only via drain | T-5.2 | `internal/service` (package `service`, tag `integration`); wire `NewAuditLedgerRepository` (key set) → `NewCallbackService` + `SetAuditPublicationRepository` + `SetAuditPublishers(spy)` (simulates `wireAuditServices` with `AuditGovernance.Enabled`) → `NewCommentRepository(db, outbox, ledgerRepo)` → `NewCommentService`; ctx via `service.WithIdentity(&domain.TokenIdentity{TenantID: "tenant-e2e", UID: "author", ...})`; **worker not started** | (a) exactly 1 `audit_ledger` row (`action='comment.report.resolve'`, `resource_id=<reportID>`, `actor_uid='author'`, payload keys `comment_id`/`status='resolved'`); (b) exactly 1 `audit_publications` row (`status='pending'`); (c) both present post-commit; (d) spy received **0** facts immediately after resolve; (e) after `svc.drainPublications(ctx)` spy received exactly 1 and publication row terminal `completed` |
| T-4 gate fails on `_ =` + Log/Record/Publish/FinishPublication in comment+service scope | T-4.1 | `scripts/check-audit-swallow.sh` self-tests + mutation | (a) self-test: positive Log fixture flagged, negative `if err := ...Log(` accepted; (b) post-change tree exits 0; (c) reintroduce `_ = s.audit.Log(` in either comment service → non-zero exit naming the file:line |
| T-4 no inline publisher loop in `Record` | T-4.2 | already green: `TestAuditLedgerRecordEnqueuesOnly` + integration twin; regression-guarded by P1 `Publish` pattern + T-5.2(d) | keep green; no new code |
| R6 conversions | R6 | `service_test.go:653-693` rewritten against `ResolveReportWithAuditFn` (assert fact action/actor/resource/payload + hide-intent nil/non-nil); `service/comment_test.go:559`; `mocks.go` | fact contract identical to the old event vocabulary; `hide` = `&Hidden` for Resolved+Normal, nil for Dismissed |
| Done gate | — | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build` + `make test-integration` | T-5.1, T-5.2, existing `TestAuditLedgerRecordEnqueuesOnlyIntegration`, `TestCallbackAuditRollbackWithoutSigningKey` all pass |

---

## 7. Notes for the PR

- Behavior change: report resolution is now atomic with its compliance fact; append failure → 500 + rollback (FM-1/FM-2/FM-8). Previously the fact could be silently lost on crash/DB failure between the commits and the swallowed `Log`.
- Wired path requires a tenant identity (FM-4) — enforced as 403 `tenant.required` via the existing `mustTenant` pre-check (not a 500), with the ledger's empty-tenant rejection as backstop; see §1.1 for the reachability analysis and the upstream-mode deployment consequence.
- Retry duplicates facts (parity with today; dedup = direction #2).
- Out of scope: EventID anchoring, dead-letter replay, the nine legacy `Log` sites, any change to `Record`/`drainPublications`/`AppendInTx`.
