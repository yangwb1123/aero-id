# Requirements — module `internal/app`: fail closed on the last out-of-transaction audit write path (account profile update + account-service `if s.audit != nil` skip guards)

| | |
|---|---|
| Module (batch label) | `internal/app` — the composition root. The direction's change surface is `internal/service/account_profile.go` + `internal/service/account.go` (+ same-class guard in `internal/service/account_backfill.go`), pinned by service unit tests and the composition-root wiring assertion; the `internal/app` obligations are the composition-root fail-fast coverage and the app-level gate integration |
| Source analysis | `docs/auto/analyses/internal-app-73e67e01.json` (direction 3 of 3: B2 item 1 tail — account profile update two-phase fallback + `if s.audit != nil` skip guards) |
| Campaign | `aero-id-b2-1-in-tx-audit` (sibling of `2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop.md`; joint gate G3: T-5, T-4) |
| Design | `docs/plans/2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop-design.md` — T-5 row (design doc line 199) pins the `*.audit_unavailable` fail-closed shape and the read-path assertion pattern; this spec owns the account-service closure |
| Date | 2026-08-08 |
| Status | **Evidence re-verified against the working tree.** The direction's primary code claim — "`UpdateProfile` retains a two-phase fallback: plain `UpdateProfile` commit, then standalone `s.audit.Record`" — is **false in the working tree**: the two-phase fallback has already been replaced by a fail-closed `account.profile_audit_unavailable` branch (see §2 E1). What remains true and unimplemented: (a) the reachable `if s.audit != nil` skip guards at `account.go:154,184` and `account_profile.go:44` (a nil audit still silently drops the `account.profile.updated` / `account.viewed` / `account.listed` facts), (b) **no** unit test pins the profile fail-closed path (the direction's T-5 test is missing), and (c) the direction's premise that these guards are "protected by startup assertions (assertAuditWiring)" is itself false — `assertAuditWiring` covers only the six legacy services; the account service is asserted by **nothing** (§2 E3). Every supplied acceptance check (T-5, T-10.1, `_ = s.audit.` = 0, T-4 conditional) is preserved and made runnable in §5 |

## 1. Problem statement (direction, preserved)

B2 item 1 is nearly complete (`EnsureByIdentityWithAudit`/`EnsureByIdentityWithQuotaWithAudit` at
`internal/repository/account_provision_audit.go:31,56` and `UpdateProfileWithAudit` at
`internal/repository/account_profile.go:67` all use `AppendInTx`; drain is the only delivery path), but
`accountService.UpdateProfile` retains a two-phase fallback: when the repo lacks
`AccountProfileAuditRepository`, the mutation commits via plain `UpdateProfile` and only then runs a
standalone `s.audit.Record` (`internal/service/account_profile.go:54-70`). A `Record` failure there
returns an error **after** the commit — the mutation persists without its audit fact (the exact
silent-divergence class T-5 forbids). The operation service already has the fail-closed precedent
(`TestOperationAuditUnavailableWhenRepoLacksWithAudit`, `operation_audit_test.go:89`); the profile path
is the remaining exception. The `if s.audit != nil` guards at `account.go:155,186` and
`account_profile.go:62` are only protected by startup assertions (`assertAuditWiring`), not by
construction.

**Current-state correction (verified, §2):** the two-phase fallback no longer exists. The working tree
already fails closed with `account.profile_audit_unavailable` when a wired service's repo lacks
`AccountProfileAuditRepository`, and the in-tx `UpdateProfileWithAudit` + `AppendInTx` path is complete.
The remaining work is the direction's second half, which is still open: the reachable
`if s.audit != nil` / `if s.audit == nil` skip guards in the account service (nil audit ⇒ mutation or
sensitive read persists **without** its fact), the missing T-5 unit tests for the profile path, and the
composition-root assertion that the direction assumed already existed but does not.

## 2. Verified evidence (working tree; line numbers live)

### 2.1 Direction-evidence claims (re-checked)

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `internal/service/account_profile.go:54-70` — "`atomicAudit`/`!atomicAudit` fallback + post-commit `Record`" | ❌ **false today.** No `atomicAudit` symbol exists in `account_profile.go` (the identifier survives only in `event_ingest.go:86,94` and `legacy_cutover.go:85,95,113`, unrelated sites). `UpdateProfile` (`account_profile.go:30-61`) already contains the fail-closed branch: `if s.audit != nil { repo, ok := s.repo.(repository.AccountProfileAuditRepository); if !ok { return nil, apperr.Internal("account.profile_audit_unavailable", "profile audit persistence is unavailable") } err = repo.UpdateProfileWithAudit(...) } else { err = s.repo.UpdateProfile(ctx, profile) }` at `:44-57`, with the in-file comment "Fail closed (B2-1 REQ-4)". **The two-phase fallback is already removed.** The remaining `else` at `:57` is the skip guard (see E2) |
| E2 | "`if s.audit != nil` guards at `account.go:155,186` and `account_profile.go:62`" | ✅ **present, line drift.** Guards at `account.go:154` (`GetAccount` → `account.viewed` standalone `Record` at `:158`) and `account.go:184` (`ListAccounts` → `account.listed` `Record` at `:191`); guard at `account_profile.go:44`. When `s.audit == nil` the facts are silently skipped: profile mutation commits via plain `UpdateProfile` (`:57`) with no `account.profile.updated` row; the admin reads complete with no access-audit row. **Same-class site found during verification (not cited by the direction):** `account_backfill.go:112` `if s.audit == nil { s.repo.EnsureByIdentity(...) }` — the shadow-account backfill provisions with no `account.provisioned` fact when audit is nil |
| E3 | "The guards ... are only protected by startup assertions (`assertAuditWiring`)" | ❌ **false — the account service is not asserted at all.** `assertAuditWiring` (`internal/app/audit.go:201-219`) requires `AuditLoggerWired()`, implemented only by file/message/comment/block/admin/ban/hold/cutover (`internal/service/audit_wiring.go:10-23`). The composition-root call (`internal/app/new.go:262-270`) registers exactly those six legacy services; `accountService` has no `AuditLoggerWired()` and is absent. Production always wires audit (`new.go:213` `service.WithAccountAudit(auditLedger)`), but a future composition-root regression that drops that option would silently disable profile/read/backfill audit facts with **no** startup failure. The direction's premise understates the gap |
| E4 | `EnsureByIdentityWithAudit`/`EnsureByIdentityWithQuotaWithAudit` at `account_provision_audit.go:31,56` use `AppendInTx` | ✅ exact match. Both wrap the account (+quota) write and `r.audit.AppendInTx(ctx, tx, fact)` in one `Transaction` (`:38-49`, `:63-76`) |
| E5 | `UpdateProfileWithAudit` at `account_profile.go:67,101` | ✅ `UpdateProfileWithAudit` at `internal/repository/account_profile.go:67` (nil-audit guard inside at `:69-71`), shared `updateProfile` at `:75-101` — profile upsert and `r.audit.AppendInTx(ctx, tx, fact)` (`:99`) in one `Transaction`. Repo-level integration already pins in-tx commit and rollback: `audit_wiring_integration_test.go` `TestAuditWiringHappyPath` (`:60`, profile leg `:66-76`) and `TestAuditWiringRollbackAtomicity` (`:175`, profile leg `:181-191`) |
| E6 | Precedent `TestOperationAuditUnavailableWhenRepoLacksWithAudit`, `operation_audit_test.go:89` | ✅ present at `:104` (comment `:100`; citation off by 15 lines). Full precedent matrix: `TestOperationCreateWithAuditFailsClosedWhenRepoLacksWithAudit` `:124`, `TestOperationCreateWithAuditWiredPath` `:138`, `TestOperationCreateWithAuditDisabledPlainPath` `:158`, `TestOperationAuditNoFallbackRecordWhenWired` `:172`. Fake shape to mirror: `opRepoWithoutAudit` embeds `repository.OperationRepository` and overrides only the touched method with a call counter (`:13-28`); `auditRecordSpy` counts standalone `Record` calls (`:81-88`) |
| E7 | "`grep ".Publish(ctx, fact)"` still only `callback_audit.go:157`" | ⚠️ single-site claim holds; cited line stale. The only call site is `publisher.Publish(ctx, fact)` at `callback_audit.go:179`, reachable only via `publishAuditFact` (`:156`) ← `finishPublicationAttempt` ← `drainPublications` ← the `Start` ticker. `:157` is inside `publishAuditFact`'s loop body region; no second delivery path exists |
| E8 | "`grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go` stays 0" | ✅ already 0 hits in the working tree; `make check-audit-swallow` (Makefile:64-69, scans `internal/service internal/repository internal/app internal/storage cmd`) is green on it |
| E9 | T-4 "proposed if the profile fact gains an anchor" | ✅ precondition is currently unmet: the profile fact is built via `buildAuditFact` (`audit_ledger.go:92-105`) with no `EventID` input ⇒ zero `EventID` ⇒ `checkAuditDuplicate` (`audit_ledger.go:180-192`) skips dedup for zero IDs (`:181-183`), and `event_id` is nullable (`migrations/000034_create_audit_ledger.up.sql`, `uq_audit_ledger_event UNIQUE (source, event_id)` — multiple NULLs legal). Anchored precedents exist (`idgen.Anchor` at `legacy_cutover.go:143`, `operation_audit.go:65`, `audit_hold.go:69,96`) and the derivation gate `scripts/check-anchored-event-ids.sh` bans random mints outside the C2 allowlist — so anchoring the profile fact is a deliberate, gated decision, consistent with the direction's "proposed if" wording |

### 2.2 Live surface this spec pins (line numbers live)

| # | Surface | Verified location |
|---|---|---|
| A1 | Fail-closed `!ok` head in `UpdateProfile` (already landed) | `internal/service/account_profile.go:45-51` — `apperr.Internal("account.profile_audit_unavailable", ...)`, returned before any repo call |
| A2 | Reachable skip guards (the residual delta) | `account_profile.go:44` (`else` plain `UpdateProfile` at `:57`), `account.go:154` (`account.viewed`), `account.go:184` (`account.listed`), `account_backfill.go:112` (`EnsureByIdentity` without fact) |
| A3 | In-tx repository surface (unchanged by this direction) | `UpdateProfileWithAudit` `repository/account_profile.go:67` + `AppendInTx` `:99`; `EnsureByIdentityWithAudit` `repository/account_provision_audit.go:31`; `EnsureByIdentityWithQuotaWithAudit` `:56` |
| A4 | Read-path fail-closed assertions already exist and must stay green | `internal/service/account_test.go:67-86` `TestAccountViewedListedFailClosedOnLedgerOutage` (503 `audit.ingest_failed`, spy pattern `:70-72`); integration `TestStandaloneSiteAccountViewedAndListed` (`audit_standalone_sites_integration_test.go:174-209`) |
| A5 | Missing T-5 unit tests for the profile path | `internal/service/account_profile_test.go` is 15 lines — preference validation only; no `UpdateProfile` audit test of any kind |
| A6 | Composition-root assertion gap (the direction assumed coverage that does not exist) | `assertAuditWiring` `internal/app/audit.go:201-219`; registration `internal/app/new.go:262-270` (six legacy services only); `AuditLoggerWired()` implementations `internal/service/audit_wiring.go:10-23`; app unit pin `TestAssertAuditWiringFailsFastOnNilAuditLogger` `internal/app/audit_wiring_test.go:96-103` |
| A7 | Account service audit construction | `WithAccountAudit` option `account.go:116-119`; wired at `new.go:213`; nil audit is the default when the option is omitted |

## 3. Current state and residual delta

**Already landed (verified, no work required beyond regression proof):**
- The two-phase fallback (plain `UpdateProfile` commit → post-commit standalone `Record`) is **gone**; `UpdateProfile` fails closed with `account.profile_audit_unavailable` when the repo lacks `AccountProfileAuditRepository` (E1).
- In-tx append for provision and profile is complete and repo-integration-tested, including rollback atomicity (E4, E5).
- Drain is the only delivery path; `_ = s.audit.` swallow count is 0 (E7, E8).

**Residual delta (this spec's requirements):**
- **R-A** — the reachable skip guards at `account_profile.go:44`, `account.go:154,184`, and the same-class `account_backfill.go:112`: a nil audit (today: only preventable by wiring discipline, which `assertAuditWiring` does **not** enforce for this service) silently drops `account.profile.updated`, `account.viewed`, `account.listed`, and backfill `account.provisioned` facts.
- **R-B** — the direction's T-5 unit test for the profile path does not exist (A5); the operation precedent (E6) is the required shape.
- **R-C** — the composition-root assertion does not cover the account service (E3/A6); the direction's premise ("protected by startup assertions") is false and must be made true.
- **R-D** — T-4 anchored-dedup fold stays conditional on the profile fact gaining an anchor (E9); the anchor decision itself is out of scope.

## 4. Requirements

### REQ-1 — Profile update fails closed on a repo lacking `AccountProfileAuditRepository` (T-5; already implemented, pin with tests)

`accountService.UpdateProfile`, when constructed with a non-nil audit logger (the only production-valid
construction), must return an error with code `account.profile_audit_unavailable` (503-class
`apperr.Internal`) **before** any repository mutation when `s.repo` does not implement
`repository.AccountProfileAuditRepository`, and must never fall back to a standalone `s.audit.Record`.
The repository update counter of the fake must stay 0 and the standalone-audit spy must stay 0.

Acceptance:
1. New unit test `TestProfileUpdateAuditUnavailableWhenRepoLacksWithAudit` in
   `internal/service/account_profile_test.go`, mirroring `TestOperationAuditUnavailableWhenRepoLacksWithAudit`
   (`operation_audit_test.go:104`) and `TestOperationCreateWithAuditWiredPath` (`:138`): a fake that
   embeds `repository.AccountRepository` and overrides only `UpdateProfile` with a call counter
   (no `AccountProfileAuditRepository` methods — the `opRepoWithoutAudit` shape, `operation_audit_test.go:13-28`),
   plus the existing `auditRecordSpy` (`operation_audit_test.go:81-88`). Call `UpdateProfile` with a valid
   input under a `WithIdentity` context and assert: error non-nil, `errors.As` to `*apperr.AppError` with
   Code `account.profile_audit_unavailable`, `updateCalls == 0`, `spy.records == 0`.
2. New unit test `TestProfileUpdateWithAuditWiredPath` — fake implementing `AccountProfileAuditRepository`
   captures the fact argument; assert one `UpdateProfileWithAudit` call, fact fields `Action ==
   "account.profile.updated"`, `ResourceType == "account_profile"`, `ResourceID == account.ID.String()`,
   payload contains the changed-field list, and `spy.records == 0` (single write path, no standalone
   fallback — the `TestOperationCreateWithAuditWiredPath` shape).
3. Existing repo integration stays green: `TestAuditWiringHappyPath` and
   `TestAuditWiringRollbackAtomicity` (`audit_wiring_integration_test.go:60,175`) — profile update +
   `AppendInTx` are one transaction with the concrete repo.

### REQ-2 — Remove the reachable `if s.audit != nil` / `if s.audit == nil` skip guards in the account service (fail closed by construction)

A nil audit logger must never be able to let an account-service mutation or sensitive read commit/succeed
without its durable audit fact. The guards at `account_profile.go:44`, `account.go:154`, `account.go:184`,
and the same-class guard at `account_backfill.go:112` are removed so that the fact-emitting path is
unconditional:

- `UpdateProfile` always routes through `repository.AccountProfileAuditRepository` (the `!ok` head of
  REQ-1 applies regardless of `s.audit`); the plain-`UpdateProfile` `else` branch is deleted. The
  repository's own nil-audit guard (`repository/account_profile.go:69-71`, error "transactional audit
  repository is not configured") remains as the last-line defense.
- `GetAccount`/`ListAccounts` always emit `account.viewed`/`account.listed` via `s.audit.Record`;
  `ensureBackfillAccount` always provisions via `EnsureByIdentityWithAudit` (the plain
  `EnsureByIdentity` branch is deleted). A nil `s.audit` is a construction error, caught by REQ-3 at
  startup and by the unit tests below.

Acceptance:
1. `grep -nE 'if s\.audit (==|!=) nil' internal/service/account.go internal/service/account_profile.go internal/service/account_backfill.go` → **0 hits**.
2. Existing fail-closed behavior is preserved and re-proven: `TestAccountViewedListedFailClosedOnLedgerOutage`
   (`account_test.go:67-86`) stays green — the read facts still return 503 `audit.ingest_failed` on ledger
   failure — and `TestStandaloneSiteAccountViewedAndListed` (`audit_standalone_sites_integration_test.go:174-209`)
   stays green (one fresh-ID row per execution).
3. Every unit test that constructs `&accountService{...}` **without** an audit logger and reaches a
   fact-emitting method is updated to wire `audit: NewAuditLedgerService(&auditRecordSpy{})` (verified
   today: no test calls `UpdateProfile`/`GetAccount`/`ListAccounts`/backfill on a nil-audit service —
   `account_profile_test.go` has no `UpdateProfile` tests; `account_test.go` uses `newAccountViewService`
   with audit wired; `account_backfill_test.go:334` wires audit — so this is a no-op inventory, verified
   at analysis time).
4. `go test ./internal/service/ -run 'TestProfile|TestAccount' -count=1` and the full
   `go test ./internal/service/ -count=1` are green.

### REQ-3 — Composition root fails fast if the account service is constructed without audit (close the assertion gap the direction assumed closed)

`assertAuditWiring` must cover the account service so a future composition-root regression that drops
`service.WithAccountAudit` from `internal/app/new.go:210-219` fails at startup instead of silently
disabling profile/read/backfill audit facts. Mechanism: `accountService` gains `AuditLoggerWired()`
(returning `s.audit != nil`, registered in `internal/service/audit_wiring.go`) and the assertion call at
`new.go:262-270` gains `auditWiredService{name: "account", svc: accountSvc}`. With REQ-2's guards removed,
this is also the nil-deref protection for the unconditional `s.audit.Record` calls.

Acceptance:
1. `internal/app/audit_wiring_test.go` gains a case mirroring the existing probe shape (`TestAssertAuditWiringFailsFastOnNilAuditLogger` at `:96-103`):
   `assertAuditWiring` panics for a probe whose `AuditLoggerWired()` returns false and passes for the
   real composition-root construction (`TestAssertAuditWiringRejectsNilAuditAccountService`).
2. `make build` (composition root) is green with the new registration; `go test ./internal/app/ -count=1`
   (non-integration) is green.

### REQ-4 — Single delivery path preserved (T-10.1)

The change must not introduce any second audit delivery path. `publisher.Publish(ctx, fact)` remains
reachable only from `publishAuditFact` ← `finishPublicationAttempt` ← `drainPublications` ← the `Start`
ticker.

Acceptance: `grep -rn "\.Publish(ctx, fact)" internal/service/` → exactly
`internal/service/callback_audit.go:179` (single hit); `grep -rn "s.audit.Record(" internal/service/account*.go`
matches only the standalone read sites (REQ-2 acceptance 1 already proves the guards are gone).

### REQ-5 — No swallowed audit writes (gate invariant)

Acceptance: `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go` → 0 hits, and
`make check-audit-swallow` is green (scans `internal/service internal/repository internal/app
internal/storage cmd`, Makefile:64-69). The check-audit-swallow gate keeps its existing positive
fixtures and self-tests; no new allowlist entries are added by this direction.

### REQ-6 — T-4 conditional: repeated identical profile updates fold on an anchored event_id (only if the profile fact gains an anchor)

Conditional, preserved from the direction ("proposed if the profile fact gains an anchor"). If and only
if a follow-up decision anchors the `account.profile.updated` fact (`idgen.Anchor(...)` derivation, added
to the `scripts/check-anchored-event-ids.sh` allowlist analysis), then: re-running the identical profile
update folds into exactly one `audit_ledger` row (dedup via `checkAuditDuplicate` →
`ErrAuditDuplicate`, `audit_ledger.go:180-192`) while the profile row reflects the second execution.
**This direction does not require the anchor change** — the profile fact keeps its current
fresh/zero-`EventID` semantics (E9) — and REQ-6 is satisfied vacuously if no anchor is added. If the
anchor is added, the acceptance is:

1. New integration case in `internal/repository/audit_wiring_integration_test.go` mirroring the
   hold/cutover retry-fold shape (`TestAuditHoldPutRetryFold` per design doc line 203): run the same
   `UpdateProfileWithAudit` twice with identical inputs and the same anchored `EventID` → exactly 1
   `audit_ledger` row, no error on the second call, profile row reflects the second execution.
2. `make check-anchored-event-ids` stays green (the new derivation site is either anchored via
   `idgen.Anchor` or explicitly allow-listed with rationale — no random `idgen.NewID()` mint added).

## 5. Preserved acceptance checks (direction, made testable)

| Direction check | Testable form | Gate |
|---|---|---|
| T-5: "new unit test mirroring the operation precedent — repo fake lacking `AccountProfileAuditRepository` → `UpdateProfile` returns `account.profile.audit_unavailable`-class error and performs NO repo update (no fallback `Record`)" | REQ-1.1: `TestProfileUpdateAuditUnavailableWhenRepoLacksWithAudit` — fake embeds `repository.AccountRepository` (no `AccountProfileAuditRepository`), `UpdateProfile` call counter, `auditRecordSpy`; assert error code `account.profile_audit_unavailable`, `updateCalls == 0`, `records == 0` | `go test ./internal/service/ -run TestProfileUpdateAuditUnavailableWhenRepoLacksWithAudit -count=1` |
| T-5: "with the concrete repo, profile update + append are one tx" | REQ-1.2 (`TestProfileUpdateWithAuditWiredPath`, fact rides `UpdateProfileWithAudit`, spy untouched) + REQ-1.3 (existing `TestAuditWiringHappyPath` / `TestAuditWiringRollbackAtomicity` stay green) | `go test ./internal/service/ -run TestProfileUpdateWithAuditWiredPath -count=1`; `go test -tags=integration ./internal/repository/ -run 'TestAuditWiring' -count=1` |
| T-10.1: "`grep ".Publish(ctx, fact)"` still only `callback_audit.go:157`" | REQ-4: exactly one hit, at `callback_audit.go:179` (line drift from the citation), reachable only via `publishAuditFact` | `grep -rn '\.Publish(ctx, fact)' internal/service/` → 1 hit; `make check-audit-swallow` |
| "`grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go` stays 0" | REQ-5 | grep → 0; `make check-audit-swallow` |
| T-4: "repeated identical profile updates fold on anchored `event_id` (dedup) — proposed if the profile fact gains an anchor" | REQ-6, conditional on the anchor decision; if added: retry-fold integration case + gate | `go test -tags=integration ./internal/repository/`; `make check-anchored-event-ids` |

## 6. Out of scope

- **The anchor change for `account.profile.updated`** (REQ-6 precondition; separate decision affecting
  the `check-anchored-event-ids` C2 analysis).
- Other services' `if s.audit != nil` guards (hold/cutover keep their guards + `assertComplianceAuditWiring`,
  `internal/app/audit.go:230-243`; legacy file/message/comment/block/admin/ban keep theirs +
  `assertAuditWiring`, `new.go:262-270` — the operation service keeps its documented nil-audit plain path
  for unit-test construction, `operation_audit_test.go:158`).
- Backfill failure semantics (`ensureBackfillAccount`'s count-not-fatal policy and its
  `repository.ErrNotFound` degradation for a repo lacking `AccountProvisionAuditRepository`) — only the
  nil-audit skip branch is removed (REQ-2).
- Handler, API, protobuf, config, and migration surfaces; error-message wording of the existing
  `account.profile_audit_unavailable` code; audit payload content.

## 7. Done-gate verification

```bash
go test ./internal/service/ -count=1            # REQ-1/REQ-2 unit pins
go test ./internal/app/ -count=1                # REQ-3 assertion pin
grep -nE 'if s\.audit (==|!=) nil' internal/service/account.go internal/service/account_profile.go internal/service/account_backfill.go   # REQ-2 → 0
grep -rn '\.Publish(ctx, fact)' internal/service/   # REQ-4 → 1 hit
grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go   # REQ-5 → 0
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-integration-compile && make build
```

Run `make test-integration` after the change: REQ-1.3/REQ-6 touch audit repository behavior assertions,
and AGENTS.md requires the integration gate after storage/audit work. File-size note: `account_profile.go`
is 218 lines (≤ 500) and the added tests keep `account_profile_test.go` well under its limit; if the new
test file approaches 500 lines, split the fail-closed cases into `account_profile_audit_test.go` (tests
follow the split rule).
