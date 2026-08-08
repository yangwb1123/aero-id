# Requirements — Sweep-and-lock the in-tx audit conversion: enumerated bare-site manifest gate, per-site observability + retry-dedup proof, atomicity matrix extension (B2-1 hardening)

| | |
|---|---|
| Module (batch label) | `cmd/migrate` (campaign slot for `docs/auto/analyses/cmd-migrate-c1b3771d.json`, direction 2 of 3). **Scope note:** `cmd/migrate` itself is the production migration runner (migrations/ + `schema_migrations`, zero audit code — verified). This direction's work lives in `internal/service` + `internal/repository` + `internal/pkg/metrics` + `scripts/` + `Makefile`, exactly as the direction's evidence lists |
| Source analysis | `docs/auto/analyses/cmd-migrate-c1b3771d.json` (direction 2 of 3) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit; gate G3: T-4/T-5/T-10.1) |
| Contract | `docs/proposals/audit-contract-batch-aero-id.md`; sibling specs `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` (owns the ten post-commit `Log` conversions, REQ-3, and the fallback-tail deletions, REQ-5) and `docs/plans/2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop-design.md` |
| Date | 2026-08-07 |
| Status | Draft — every cited file/symbol re-verified against the working tree (head `13e4676` + uncommitted changes) |

## 1. Evidence verification status

All citations from the direction's evidence and problem statement were re-checked against the live tree.
Verdicts (line numbers current; drift noted):

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `XxxWithAudit` repo surface live: `account_profile.go:67`, `account_provision_audit.go:31`, `callback_audit.go:31`, `legacy_cutover.go:137`, `sync_job.go:86`, `audit_hold.go`, `event_receipt.go` | ✅ all hold: `repository/account_profile.go:67` (`UpdateProfileWithAudit`; interface `account.go:40`), `account_provision_audit.go:31` (`EnsureByIdentityWithAudit`, plus `:56` quota variant), `callback_audit.go:31/60/85` (`CreateSubscriptionWithAudit`/`DeleteSubscriptionWithAudit`/`ResetDeliveryWithAudit`), `legacy_cutover.go:137` (`SetCutoverWithAudit`), `sync_job.go:86` (`CreateWithAudit`, `FinishWithAudit` `:292`), `audit_hold.go:127/154` (`PutHoldWithAudit`/`RemoveHoldWithAudit`), `event_receipt.go:89` (`ApplySnapshotEventWithAudit`; interface `:39`). All append via `AppendInTx` inside the business tx; `requireAuditLedger` fail-closed head (`audit_wiring.go:14-28`) |
| E2 | Service WithAudit call sites: `account_profile.go:48`, `account_provision.go:165`, `callback.go:176`, `audit_hold.go:96`, `legacy_cutover.go:124`, `account_backfill.go:124` | ✅ with drift: `account_profile.go:48` ✓; `account_provision.go:165/167` ✓ (both variants); `callback.go:174/226/263` (cited `:176`; drift — create/delete/reset all converted); `audit_hold.go:96` ✓; `legacy_cutover.go:124` ✓; `account_backfill.go:124` ✓; plus `event_ingest.go:141` (`ApplySnapshotEventWithAudit`), not in the citation |
| E3 | ~20 bare `s.audit.Record/Log` sites incl. `account.go:155,186; operation.go:136; operation_state.go:39; event_ingest.go:154; replay.go:145,240; sync_job.go:163,466; admin.go:93,122; ban.go:84,122; block.go:54,79; message.go:358; comment.go:415` | ✅ all 18 verified at the cited lines. **Three additional bare sites exist that the enumeration omits** (the manifest must be exhaustive — that is the acceptance): `account_profile.go:62` (atomicAudit fallback Record), `legacy_cutover.go:102` (fallback tail Record), `file_share.go:114,148` (post-commit `AuditFileShareGrant`/`AuditFileShareRevoke` Logs). **True total: 21 sites** — 6 `with-audit-fallback` arms (§3 REQ-1 table), 5 read-path/standalone Records, 10 post-commit mutation `Log`s. All 21 are error-checked (`if err :=`); zero `_ =` today |
| E4 | `operation_state.go:39` is a documented A1 standalone fact (`operation.reconciled`) | ✅ the documentation-comment pattern exists exactly there (`:31-38`: "operation.reconciled is a standalone fact with no domain write of its own … A1-class"). `operationAuditFact` (`operation_audit.go:58-68`) anchors the EventID (`idgen.Anchor(action, tenant, op, step, status)`). This is the model the sweep generalizes to every site |
| E5 | `scripts/check-audit-swallow.sh` "only bans `_ =`, not out-of-tx writes" | ⚠️ partially stale, conclusion stands: the current script bans `_ =` (P1) **and** bare calls (P2), with self-test fixtures. The residual gap is exactly as the direction states: **checked out-of-tx writes are unclassified** — nothing distinguishes a mutation-path `s.audit.Record` from a read-path/standalone one, so a NEW mutation path can regress to a checked non-tx Record and the gate stays green. No in-tx/mutation-path classification check exists anywhere in the done-gate |
| E6 | `internal/service/audit_ledger.go:68 (Record), 136 (Log)` | ✅ `Record` at `:68` (maps `ErrAuditDuplicate` → nil, observes `ObserveAuditIngestFailure` on failure only — **no success/standalone counter exists**); `Log` at `:136` adapts `*domain.AuditEvent` → `Record` (all ten post-commit `Log` sites flow through it). The acceptance's "observability counter" therefore requires a **new** counter (REQ-3) |
| E7 | Rollback atomicity tested at `internal/repository/audit_wiring_integration_test.go:175,294` | ✅ `TestAuditWiringRollbackAtomicity` `:175` (profile duplicate → whole tx rolls back; step duplicate → Class B in-tx swallow commits); `TestAuditWiringPublicationInsertRollsBackLedgerRow` `:294` (BEFORE INSERT trigger on `audit_publications` at `:268` → 0 ledger + 0 publication + 0 domain rows). **Coverage is profile / operation-step / event only** — hold put/remove, cutover set, sync job create/finish, callback create/delete/reset, account provision (both variants), operation create are **not** covered (matches B2-1 design F3). File is 328 lines — net-new tests go in a sibling file |
| E8 | Retry-dedup contract | ✅ `checkAuditDuplicate` (`repository/audit_ledger.go:157-171`) skips facts with **empty** EventID; unique `(tenant_id, source, event_id)` (migration 000050). Retry-dedup therefore holds only for sites with deterministic/anchored event IDs (message delete `ID`, comment resolve `reportID`, `operation.reconciled`/`sync.job.finished` anchors, replay.requested `operation.ID`, sync.job.requested `job.ID`, cutover anchor); `account.viewed/listed`, admin/ban/block, file share, replay dry-run mint no EventID (or a fresh one) and append one row **per execution by design**. `TestRecordAnchoredEventID_Dedup` (`service/audit_ledger_test.go:131`, spy-based) is the unit precedent; the per-site integration proof is net-new |
| E9 | `cmd/migrate` module | ✅ `cmd/migrate/main.go` is the migration CLI only (up/down/version); no audit code. Module label is a campaign slot; no work lands in `cmd/migrate` |

## 2. Problem statement (current state)

The in-tx `XxxWithAudit` conversion is live and rollback-atomicity-tested for a subset of mutations, but
three gaps remain, exactly as the direction states:

1. **No regression lock.** 21 checked out-of-tx `s.audit.Record/Log` call sites remain in `internal/service`.
   Some are documented standalone facts (`operation.reconciled`); most are not documented at all. The gate
   (`check-audit-swallow`) bans swallowed and bare calls but cannot tell a mutation path from a read path —
   a new mutation that records post-commit with a checked call passes the gate silently. T-5 "operation
   failed" atomicity could regress on an untested path.
2. **No per-site observability proof.** No counter exists for the standalone/fallback audit path, and no
   integration test proves per-site retry behavior (deterministic-event-ID sites must dedup to one ledger
   row; fresh-ID sites must write exactly one row per execution).
3. **Atomicity matrix incomplete.** The trigger-injected rollback and crash-after-commit fact-count proofs
   cover only profile/operation-step/event. Hold/cutover/sync/callback/provisioning/operation-create paths
   have no fact-count-exactness proof.

This direction **sweeps and locks**: it does not convert the ten post-commit `Log` sites (the sibling spec
`2026-08-06-cmd-server-complete-in-tx-audit-writes.md` REQ-3/REQ-4 owns those conversions and the fallback
tail deletions in its REQ-5; the manifest entries for those sites are removed by that work when the bare
calls disappear).

## 3. Requirements

### REQ-1 — Enumerated bare-site manifest + gate `make check-audit-manifest`

1. New script `scripts/check-audit-manifest.sh` (sibling of `check-audit-swallow.sh`, one concern per
   script; self-tested like the swallow gate) embedding the canonical manifest table — one row per bare
   `s.audit.Record(` / `s.audit.Log(` call site in `internal/service`, keyed by `(file, action-or-symbol
   token)` with the class field `with-audit-fallback | standalone-fact` and, for fallback entries, the
   sibling `WithAudit` repo symbol. Initial table (line numbers current at writing; the gate is
   line-drift-proof because it matches tokens, not lines):

   | File:line (current) | Key token in the call | Class | WithAudit sibling (checked by gate) | Event-ID retry class (test table, REQ-4) |
   |---|---|---|---|---|
   | `service/account_profile.go:62` | `"account.profile.updated"` | with-audit-fallback | `UpdateProfileWithAudit` | fresh (fallback input has no EventID) |
   | `service/operation.go:136` | `"operation.requested"` | with-audit-fallback | `CreateWithAudit` (via `createWithAudit`) | stable (`operation.ID`) |
   | `service/sync_job.go:163` | `"sync.job.requested"` | with-audit-fallback | `CreateWithAudit` (via `createWithAudit`) | stable (`job.ID`) |
   | `service/sync_job.go:466` | `syncJobFinishedAudit` | with-audit-fallback | `FinishWithAudit` | stable (anchored `"sync.job.finished"`) |
   | `service/legacy_cutover.go:102` | `"legacy.cutover.set"` | with-audit-fallback | `SetCutoverWithAudit` | stable (anchored) |
   | `service/event_ingest.go:154` | `eventAuditInput` | with-audit-fallback | `ApplySnapshotEventWithAudit` | stable (`envelope.EventID`) |
   | `service/account.go:155` | `"account.viewed"` | standalone-fact | — | fresh (no EventID) |
   | `service/account.go:186` | `"account.listed"` | standalone-fact | — | fresh (no EventID) |
   | `service/operation_state.go:39` | `"operation.reconciled"` | standalone-fact | — | stable (anchored `operationAuditFact`) |
   | `service/replay.go:145` | `"audit.replay.dry_run"` | standalone-fact | — | fresh (`replayID` minted per call) |
   | `service/replay.go:240` | `"audit.replay.requested"` | standalone-fact | — | stable (`operation.ID`) |
   | `service/admin.go:93` | `domain.AuditAdminGrantRole` | standalone-fact | — | fresh (no EventID) |
   | `service/admin.go:122` | `domain.AuditAdminRevokeRole` | standalone-fact | — | fresh (no EventID) |
   | `service/ban.go:84` | `domain.AuditUserBan` | standalone-fact | — | fresh (no EventID) |
   | `service/ban.go:122` | `domain.AuditUserUnban` | standalone-fact | — | fresh (no EventID) |
   | `service/block.go:54` | `domain.AuditUserBlock` | standalone-fact | — | fresh (no EventID) |
   | `service/block.go:79` | `domain.AuditUserUnblock` | standalone-fact | — | fresh (no EventID) |
   | `service/comment.go:415` | `domain.AuditReportResolve` | standalone-fact | — | stable (`reportID`) |
   | `service/message.go:358` | `domain.AuditMessageDelete` | standalone-fact | — | stable (message `ID`) |
   | `service/file_share.go:114` | `domain.AuditFileShareGrant` | standalone-fact | — | fresh (no EventID; one fact per grantee) |
   | `service/file_share.go:148` | `domain.AuditFileShareRevoke` | standalone-fact | — | fresh (no EventID) |

2. Gate behavior (exit 1 on any violation):
   - **No unmapped site:** every bare `s.audit.Record(`/`s.audit.Log(` match in `internal/service`
     (scan pattern identical to the swallow gate's P2) must resolve to exactly one manifest row (file +
     key token). Any unmapped match fails the gate — this is the "a NEW mutation path cannot regress to a
     non-tx Record" lock. A developer adding a bare audit call must either route the mutation through a
     `WithAudit` repo method (no bare call at all) or add a manifest row with the documentation marker
     (REQ-2) and let the REQ-4 integration test prove the site's behavior.
   - **Documented marker:** every manifest row's call site must carry the class marker comment from REQ-2.
   - **Fallback truth:** every `with-audit-fallback` row must have its `WithAudit` sibling symbol present in
     the same file (the bare call is then provably the non-atomic fallback arm of an in-tx mutation).
   - **Self-tests:** positive fixtures (an unmapped site; a mapped site missing its marker) must fail the
     scan; negative fixtures must pass (precedent: `scripts/testdata/audit-swallow/`).
3. New Makefile target `check-audit-manifest` wired into the done-gate beside `check-audit-swallow`
   (AGENTS.md done-gate list). The script is bash + grep only, no new Go binary.

### REQ-2 — Per-site documentation markers (the "documented A1 standalone fact" half)

1. Every site in the REQ-1 table gains a comment containing the exact token `audit-site: <class>` where
   `<class>` matches the table row, placed on the call's line or the line(s) immediately above it.
   - `standalone-fact` sites: the comment states the justification (e.g., `operation.reconciled`'s existing
     wording; for the ten post-commit `Log` sites: "post-commit legacy-compat fact — no audit atomicity
     contract with the domain write; conversion owned by the sibling in-tx migration
     (REQ-3/REQ-4 of 2026-08-06-cmd-server-complete-in-tx-audit-writes)").
   - `with-audit-fallback` sites: the comment names the `WithAudit` sibling (the four existing fallback
     comments in `account_profile.go`/`operation.go`/`sync_job.go`/`legacy_cutover.go` are normalized to
     the token; `event_ingest.go:154` gains one).
2. The gate (REQ-1.2) mechanically verifies the marker's presence and class — "documented" is enforced,
   not aspirational. When the sibling conversion deletes a bare call, its manifest row and marker are
   removed together; the gate cannot go stale because an orphan row with no matching call is inert and the
   reverse direction (call without row) fails.

### REQ-3 — Standalone/fallback observability counter

1. Add `ObserveAuditStandaloneRecorded(action string)` to `service.ControlPlaneMetrics`
   (`internal/service/control_plane_metrics.go:12-29`), implemented by `metrics.Registry`
   (`internal/pkg/metrics/control_plane.go`) as the bounded counter
   `aero_control_audit_standalone_recorded_total{action=...}` (counter map keyed by the controlled action
   vocabulary, same shape as `aero_control_audit_ingest_failures_total`), and by
   `noopControlPlaneMetrics` (`:31-`).
2. `auditLedgerService.Record` (`internal/service/audit_ledger.go:68-82`) increments it on **every
   completed execution** of the out-of-tx path — append success **and** `ErrAuditDuplicate` fold — keyed
   by `input.Action`; failures keep observing `ObserveAuditIngestFailure` instead. `Log` (`:136`) routes
   through `Record`, so all 21 sites are counted at one seam. The in-tx `AppendInTx` paths never touch
   `Record`, so the counter counts exactly the out-of-tx surface.
3. Extend the metrics text-output assertion (`internal/pkg/metrics/metrics_test.go:134`-style) with the new
   counter line.

### REQ-4 — Per-site integration proof: counter increments + no ledger double-write on retry (T-5)

New integration-tagged test file `internal/service/audit_standalone_sites_integration_test.go` (real
Postgres via `testpg.Start` + `testpg.Migrate`, real repos, real ledger, recording `ControlPlaneMetrics`
spy). For **every** site in the REQ-1 table with class `standalone-fact` (the 15 sites; the 6 fallback
arms are covered by their existing fake-repo unit tests and by REQ-5's repo-level matrix on the primary
paths):

1. **Retry simulation:** invoke the site's owning service method twice with identical input (seeding the
   minimal domain rows through the same repos — message row for `message.go:358`, report row for
   `comment.go:415`, file + grantee for `file_share.go:114/148`, account for `account.go:155/186`, etc.).
   Where a domain guard makes the second service invocation fail before the audit call (e.g., a second
   `GrantRole`), the retry drives the site's audit call directly (the site's audit section is the unit
   under test; the domain guard is not).
2. **Counter:** the spy records exactly one `ObserveAuditStandaloneRecorded(action)` per audit-call
   execution — the retry sequence increments the counter every time the site's audit path executes and
   never less than once.
3. **No ledger double-write on retry:** for `event-id: stable` sites (per REQ-1 table), `count(*)` on
   `audit_ledger` for `(tenant_id, source, event_id)` across the whole retry sequence is exactly **1**
   (dedup fold; `Record` returns nil on the second append) and `audit_publications` holds exactly 1 row
   per ledger row. For `event-id: fresh` sites, rows == audit-call executions (exactly one row per
   execution, never two for one execution), documented as by-design in the test table.
4. This is the integration-level counterpart of the spy-based `TestRecordAnchoredEventID_Dedup`
   (`audit_ledger_test.go:131`): a retry of any stable standalone site can never double-write the ledger
   row, and any future standalone site added to the manifest must carry its own row in the test table
   (test table completeness is enforced by iterating the same site list as the REQ-1 table).

### REQ-5 — Atomicity matrix extension: fact count stays exact on rollback and crash-after-commit (T-10.1)

Extend the repo-level integration suite (new sibling file
`internal/repository/audit_wiring_atomicity_matrix_integration_test.go` — `audit_wiring_integration_test.go`
is 328 lines and its two mechanisms at `:175`/`:294` are reused, not duplicated) to every `with-audit-fallback`
row's primary path: profile update (covered, keep), operation create, sync job create and finish, hold
put and remove, cutover set, callback subscription create/delete/reset, account provision (both variants),
event apply (covered, keep):

1. **Rollback-after-failure (no fact in a rolled-back tx):** with the `fail_publication_insert` BEFORE
   INSERT trigger installed (`:268` mechanism), each `XxxWithAudit` call returns an error and **0**
   `audit_ledger` rows, **0** `audit_publications` rows, and **0** (or unchanged) domain rows survive for
   that fact's event_id.
2. **Duplicate-append retry:** re-invoking with the identical anchored event_id behaves per the class
   already documented in the repo code (verified at writing):
   - **Class A — duplicate surfaces, whole tx rolls back** (fact count 1, domain unchanged): profile
     update (`account_profile.go`, proven by `TestAuditWiringRollbackAtomicity`), account provision
     (`account_provision_audit.go:45` propagates; note the retry shape: `created=false` appends nothing —
     assert both arms).
   - **Class B — duplicate swallowed in-tx after the domain write, tx commits** (fact count 1, domain
     committed): operation create/step-finish/compensation (`operation_audit.go:119-140`), callback
     create/delete/reset (`callback_audit.go:45-49`), hold put/remove (`audit_hold.go:132/137/160`),
     cutover set (`legacy_cutover.go:142-147`), sync job create/finish (`sync_job_audit.go:43-55`).
   - Event apply dedups at the receipt level (`claimEventReceipt`); assert the existing
     `TestAuditWiringHappyPath` receipt-dedup behavior plus the trigger-injected rollback arm.
   Either way the fact count is exactly 1 — never 0 after a commit, never 2. Each path's assertion is
   pinned to the class documented in its repo comment, so a future reclassification (swallow added to
   profile, swallow removed from hold) fails the test.
3. **Crash-after-commit (no missing fact after commit):** commit a WithAudit mutation (e.g., hold put),
   then assert the ledger row **and** its pending publication row are both present from a fresh
   connection (the "crash before drain" window — fact is not missing); then run the drain once and assert
   exactly one delivery/settlement and the ledger row count still 1 (fact count exact, no re-append).
   Precedent shape: `TestFirstFactDrainDeliversExactlyOnce`
   (`internal/service/callback_integration_test.go:392`).

### REQ-6 — T-4: the sweep introduces no `_ =`

1. `make check-audit-swallow` stays green (today 0 matches across `internal/service internal/repository
   internal/app cmd`) — the marker comments (REQ-2) and counter plumbing (REQ-3) introduce no swallowed or
   bare-discarding audit writes; `Record`'s counter increment is a checked, error-propagating call like the
   rest of the method.
2. The new gate script ships its own positive/negative fixtures and fails its self-test when they stop
   matching (precedent: `check-audit-swallow.sh` self-test block).
3. The done-gate runs `make test && make lint && make lint-strict && make check-file-sizes &&
   make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest &&
   make check-integration-compile && make build`; `make test-integration` covers REQ-4/REQ-5.

## 4. Acceptance mapping (supplied checks preserved, made testable)

| Supplied acceptance | Testable form |
|---|---|
| **T-5:** "an enumerated manifest (script or test) maps every bare Record/Log call site in internal/service to either a WithAudit repo path or a documented A1 standalone fact, and fails the gate on any unmapped site" | REQ-1 (manifest table = all 21 sites; class `with-audit-fallback` ⇔ WithAudit repo path with the sibling symbol mechanically verified; class `standalone-fact` ⇔ documented marker enforced by REQ-2; gate exits 1 on any unmapped P2 match). "Fails the gate" is the script's exit code wired into `make check-audit-manifest` in the done-gate |
| **T-5:** "a new integration case proves each remaining standalone site increments its observability counter and never double-writes the ledger row on retry" | REQ-3 (new `ObserveAuditStandaloneRecorded` counter) + REQ-4 (per-site integration test: spy-counter == audit executions ≥ 1; stable sites → exactly 1 ledger row across the retry; fresh sites → exactly 1 row per execution; 1 publication row per ledger row) |
| **T-10.1:** "rollback-after-remote-failure and crash-after-commit cases keep the fact count exact (no fact in a rolled-back tx, no missing fact after commit)" | REQ-5.1 (trigger-injected rollback → 0 ledger/0 publication/0 domain rows), REQ-5.2 (duplicate retry → exactly 1 ledger row, Class A rollback or Class B fold per documented class), REQ-5.3 (post-commit crash window → ledger + pending publication present; drain → exactly one settlement, count still 1) |
| **T-4:** "sweep introduces no `_ =`" | REQ-6: `make check-audit-swallow` green after the sweep; new script self-tested; gate list extended without weakening the existing bans |

## 5. Non-goals (scope boundaries — do not expand)

- **No conversion** of the ten post-commit `Log` sites (admin×2, ban×2, block×2, comment, message,
  file_share×2) to `WithAudit`; no deletion of the fallback tails. That is the sibling spec's REQ-3/REQ-4/
  REQ-5. This direction only enumerates, documents, gates, and proves them. When the sibling work lands,
  the manifest rows for the removed calls are deleted with them (the gate tolerates orphan rows by design).
- **No changes to `cmd/migrate`** or its migration files.
- **No new WithAudit repo methods** beyond what exists; REQ-5 reuses existing methods and test mechanisms.
- **No changes to dedup semantics, anchors, or error codes**; REQ-4/REQ-5 assert current behavior.
- **No expansion of the manifest scan** beyond `internal/service` (the acceptance's scope). The existing
  swallow gate keeps scanning `internal/repository internal/app cmd`.
- **No new broker/queue/replay work**; the drain is used as-is.

## 6. Verification commands

```bash
make check-audit-manifest            # new gate: unmapped bare site / missing marker / orphaned fallback symbol
make check-audit-swallow             # stays green (T-4)
make test                            # unit: counter unit tests, manifest self-tests live in the script
make test-integration                # REQ-4 per-site proof + REQ-5 atomicity matrix (real Postgres)
make lint && make lint-strict && make check-file-sizes && make check-integration-compile && make build
```
