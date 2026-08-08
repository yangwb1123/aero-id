# Verified design — dead-letter replay for audit publications and callback deliveries (`RequeuePublication`/`RequeueDead`, `cmd/audit-replay`, purge-pause flags)

| | |
|---|---|
| Module (batch label) | `cmd/server` — **label only**. Change surface: `internal/repository` + `cmd/audit-replay` (+ verification in `internal/service`/`internal/app`); `cmd/server/main.go` touched only incidentally |
| Input evidence | `docs/requirements/2026-08-08-cmd-server-dead-letter-replay.md` (evidence-backed requirements; claims below re-verified independently) |
| Design | `docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md` (rev 2, status line stale) |
| Date | 2026-08-08 |
| Status | **Verified design — §5.1 LANDED.** The six-constructor wiring fix is in `internal/app/new.go` (F-1 comment removed), six repository test call sites completed, and the done gate re-verified green on the fixed tree (2026-08-08; see §6). All replay claims verified true; the §7 gate claim of the original evidence was false until §5.1 landed (§1.2, §5.1) |

## 0. Verification verdict (claims in the evidence, re-checked on head `13e4676` + working tree)

| # | Evidence claim | Verdict | Where verified |
|---|---|---|---|
| V1 | Terminal `dead_letter` path at `internal/service/callback_audit.go:146-155` | ✅ holds (drift: `publicationOutcome` `:144-157`; `attempt >= maxAttempts` → `"dead_letter", "audit.publication_failed"` at `:151-153`) | `sed -n '140,160p'` |
| V2 | "No `Requeue*` symbol anywhere" (analysis-time claim) | ❌ false today: `RequeuePublication` (`audit_publication_replay.go:40-72`), `RequeueDead` (`callback_audit.go:115-143`), `CountDeadLetterPublications` (`:121-141`), `PurgeDeadLetterPublications` (`:143-156`), `CountDeadLetterDeliveries` (`:161-176`), `PurgeDeadLetterDeliveries` (`:180-194`); interfaces `audit_ledger.go:42-54`, `callback.go:19-39` | grep + read |
| V3 | "`cmd/` contains only `migrate/` and `server/`" | ❌ false: `cmd/audit-replay/` (main.go 209 L, main_test.go, i7_boot_hazard_test.go — all untracked); `Makefile:9-12` `build-audit-replay` chained into `make build`; flags final (`-config/-kind/-limit/-id/-tenant/-actor/-dry-run`, main.go:57-73); `replay` dispatch `:107`; `requeueFact` mint `:197-209` | `ls cmd/`, `git status` |
| V4 | "No pause flag gates the TTL cleaners" | ⚠️ superseded: `audit_governance.purge_pause` + `dead_letter_retention` gate the drain-loop `purgeDeadLetters` scan (`callback_worker.go:58-80`, no-op guard at `:65`); `data_retention.purge_paused` gates `RunOutboxCleaner` via `buildOutboxCleaner` (`app/outbox.go:77-93` → `outbox/repository.go:180-189`, no-op `:145`); `RunAuditCleaner` TTLs only the legacy `audit_events` table (new.go:337-341) | sed + grep |
| V5 | Design "pending implementation" | ⚠️ status line stale; implementation landed with 7 documented divergences (§3.2 of the requirements) | design doc head |
| V6 | Config validation | ✅ `validate.go:208-214` (`dead_letter_retention < 0` rejected), `:218-227` (`purge_paused` requires wired outbox cleaner — never inert); `audit_governance.go:25-32` | read |
| V7 | Migration 000045 | ✅ `uq_audit_publication_fact` UNIQUE(fact_id), FK `fact_id → audit_ledger(id) ON DELETE CASCADE` (ledger survives publication purge); due/lease indexes | read |
| V8 | All 17 cited tests exist | ✅ all found at cited lines; one trivial drift: `TestRequeuedPublicationDrainsExactlyOnce` at `callback_integration_test.go:878`, cited `:874` (comment block at 874) | grep |
| V9 | Script gates green: `check-file-sizes`, `check-audit-swallow`, `check-anchored-event-ids` | ✅ re-run green on this tree (self-tests ok); P1/P2 patterns include `RequeuePublication`/`RequeueDead` (`check-audit-swallow.sh:60-61`), `cmd` scan root (`Makefile:69`), `cmd_positive` fixture; C2 allowlist `internal/service/callback.go|cmd/audit-replay/main.go` (`check-anchored-event-ids.sh:31`) | ran gates |
| V10 | §7 "make lint / make build / make test / make check-integration-compile — all green on this tree" | ❌ **FALSE on the current tree.** `go build ./...`, `go vet`, `make check-integration-compile` all fail: `internal/app/new.go:141-152` calls six repository constructors with pre-conversion signatures while the repository layer (working tree) now requires `AuditLedgerTransactionalRepository`. HEAD `13e4676` compiles; the break is entirely uncommitted working-tree drift from a parallel in-flight campaign (see §1.2, §5.1). `bin/audit-replay` binary is dated Aug 7 16:28 (pre-break build) | `go build ./...`, `go vet`, `make check-integration-compile` |

**Conclusion:** the replay direction itself is landed and correctly described; the residual delta in the requirements (§3.3: design-doc status update, optional chain test leg) stands. The one false claim is the §7 gate table — the tree moved underneath the evidence via the in-tx audit conversion, and the composition root is now broken. This design therefore adds one blocking migration step (§5.1) and re-pins the done gate on the post-fix tree.

## 1. Design (as-landed surface, pinned to verified code)

### 1.1 API changes

**Repository interfaces** (`internal/repository/audit_ledger.go:42-54`, `callback.go:19-39`):

```go
// AuditPublicationRepository gains (all with an optional in-tx audit fact):
RequeuePublication(ctx, ids []domain.ID, limit int, fact *domain.AuditFact) (int, error) // dead_letter → pending
CountDeadLetterPublications(ctx) (int, error)                                            // dry-run estimate
PurgeDeadLetterPublications(ctx, cutoff time.Time, limit int) (int, error)               // keyset, lease-excluding

// CallbackRepository gains the delivery-leg twins:
RequeueDead(ctx, ids []domain.ID, limit int, fact *domain.AuditFact) (int, error)
CountDeadLetterDeliveries(ctx) (int, error)
PurgeDeadLetterDeliveries(ctx, cutoff time.Time, limit int) (int, error)
```

**Requeue semantics** (one tx per call, `audit_publication_replay.go:40-72`, `callback_audit.go:115-143`):
- Selection is status-scoped: `status = 'dead_letter'` only; exact `ids` (id-scoped) or oldest `limit` ordered by `(next_attempt_at, id)`, default 100; `limit <= 0` → code default.
- Reset UPDATE: `status='pending'`, `attempt=0`, `next_attempt_at=now` (Go clock — freshly due, immediately claimable), lease cleared (`lease_owner=''`, `lease_until=NULL`), `error_code='manual_replay'`.
- Optional in-tx audit fact appended **only when rows were affected**; `ErrAuditDuplicate` folds in-tx (defense-only; fresh UUIDv7 minted at the CLI site, `cmd/audit-replay/main.go:197-209`); any other append error rolls the whole requeue back.
- Idempotent by the `dead_letter` status predicate + `uq_audit_publication_fact`.

**CLI** (`cmd/audit-replay`, sibling binary; `make build` emits `bin/audit-replay`):

```
audit-replay [-config path] [-kind publication|delivery|both] [-limit N]
             [-id id[,id...]] [-tenant tenant] [-actor actor] [-dry-run]
```

- `run(args) error` shape; fail-closed signing-key preflight before any DB mutation (main.go:83-89, mirrors `validateAuditLedgerConfig` `internal/app/audit.go:23-28`); `-dry-run` prints `N (dry run)` via the `Count*` estimates and mutates nothing; exit 0 on 0-row re-runs; `-actor` default `audit-replay`; `-tenant` empty → no fact appended (documented un-audited default).

**Config** (`configs/config.yaml:186-187,238`; `internal/config/audit_governance.go:25-32`):

```yaml
audit_governance:
  dead_letter_retention: 0s   # 0 = never purge; < 0 rejected at validation
  purge_pause: false          # static per boot; paused ticks do no SELECT, no DELETE
data_retention:
  purge_paused: false         # gates outbox.RunOutboxCleaner; requires wired cleaner (validate.go:218-227)
```

**Drain-loop wiring** (`internal/service/callback_worker.go:45-80`): per tick — `drainPublications` → `drain` → `purgeDeadLetters` (gated `DeadLetterRetention <= 0 || PurgePause` → return; purge batch 50, keyset `(next_attempt_at, id)` with `updated_at < cutoff`, lease-excluding predicate at the SQL boundary).

### 1.2 Blocker: composition root of the parallel in-tx audit conversion (not part of this direction, blocks the gate) — **RESOLVED 2026-08-08**

The working tree's repository layer requires `AuditLedgerTransactionalRepository` on six constructors, and `internal/app/new.go:141-152` now passes it at every call site (per the table below). Note: §5.1's per-line table covered `new.go` only; the gate additionally required **six repository test call sites** to be migrated (see §5.1, step 1a).

| Call site (new.go) | Current (broken) | Required |
|---|---|---|
| `:141` | `NewFileShareRepository(gdb)` | `NewFileShareRepository(gdb, auditLedgerRepo)` |
| `:145` | `NewMessageRepository(gdb, outboxRepo, unreadCounter)` | `NewMessageRepository(gdb, outboxRepo, auditLedgerRepo, unreadCounter)` |
| `:146` | `NewCommentRepository(gdb, outboxRepo)` | `NewCommentRepository(gdb, outboxRepo, auditLedgerRepo)` |
| `:148` | `NewBlockRepository(gdb)` | `NewBlockRepository(gdb, auditLedgerRepo)` |
| `:149` | `NewBanRepository(gdb)` | `NewBanRepository(gdb, auditLedgerRepo)` |
| `:152` | `NewAdminRepository(gdb)` | `NewAdminRepository(gdb, auditLedgerRepo)` |

`auditLedgerRepo` is already in scope at that point (returned by `buildAuditStack`, `new.go:132-134`).

## 2. Compatibility constraints

- **Wire/proto/API:** none. `POST /v1/callback/deliveries/:id/replay` keeps its owner-scoped single-delivery semantics; the operator replay surface is `cmd/audit-replay` only. No HTTP/gRPC change, no proto change.
- **Attempt accounting:** requeue resets `attempt` to 0 — the only behavioral difference from `ResetDeliveryWithAudit` (which preserves attempt). `ClaimPublication` never increments attempt (`audit_publication.go:46-87`); the increment rides `FinishPublication` (`callback_audit.go:127-132`).
- **Lease invariants:** `dead_letter` rows are structurally unreachable by `ClaimPublication`; `delivering` rows with live leases are unreachable by requeue **and** by purge (SQL predicates, not app-level checks).
- **Idempotency:** `uq_audit_publication_fact` + the `status='dead_letter'` predicate make re-runs no-ops; governance conflict receipts make duplicate deliveries idempotent success — no publisher change.
- **Audit-ledger survival:** `audit_ledger` is never deleted by any purge (FK cascade runs ledger→publications only, migration 000045); a requeue fact and its publication survive purge in both interleavings.
- **Gates:** `check-audit-swallow` P1/P2 cover `RequeuePublication`/`RequeueDead` (bare-call form) with `cmd` scan root + `cmd_positive` self-test fixture; `check-anchored-event-ids` allowlists `cmd/audit-replay/main.go` as the sole new C2 mint site — the repository never mints event IDs.
- **File-size/cyclomatic:** `audit_publication_replay.go` 156 L, `callback_audit.go` (repo) 194 L, `cmd/audit-replay/main.go` 209 L — all under 500; `run`/`replay` carry documented `//nolint:cyclop,funlen`.
- **`0 = disabled`:** `dead_letter_retention: 0` = never purge; `limit <= 0` → code default 100.
- **Config validity:** `purge_paused` is never inert — validation requires the outbox cleaner to be wired (interval + dead-letter retention positive), matching the `buildOutboxCleaner` composition gate.

## 3. Failure modes (closed by landed code; pinned by tests)

| Failure | Behavior | Pinned by |
|---|---|---|
| Governance endpoint down; fact dead-letters | Operator runs `cmd/audit-replay`; row → `pending`, attempt 0, fresh due time; next drain tick retries with full budget | `TestAuditReplayCLIRunTwiceIsIdempotent`, `TestRequeuedPublicationDrainsExactlyOnce` |
| Tool run twice / 0 eligible rows | Second run affects 0, exit 0, no new fact | `TestAuditReplayCLIRunTwiceIsIdempotent` |
| Replay-fact append fails | Whole tx rolls back (no requeue without its fact); `ErrAuditDuplicate` folds in-tx only | `TestRequeuePublicationFactAppendsInTx` |
| Purge races replay | Both orders safe; ledger fact always survives; partition property under live `-race` (`purged + requeued == seeded`, no row transitions twice) | `TestRequeueVsPurgeInterleaving`, `TestConcurrentPurgeAndRequeueRace` |
| Signing key unconfigured/short | Exit 1 before any DB mutation | `TestAuditReplaySigningKeyPreFlight` |
| DB unreachable | Exit 1, zero mutation | `TestAuditReplayDBOutageFailsClosed` |
| Row mid-lease (`delivering`) | Unreachable by requeue and purge | `TestRequeuePublicationLeaseInvariant` |
| Second requeue of a re-failed row | Commits with a **fresh** v7 fact — no C2 collision | `TestRequeuePublicationSecondRequeueMintsDistinctFact` |
| `-tenant` requeue fact with unbound tenant | Commits today (documented contract); next governance-enabled boot fails the coverage gate — pinned verbatim | `i7_boot_hazard_test.go` (`TestAuditReplayUnboundTenantEntersBootCoverage`, `TestAuditReplayDeliveryFirstIntroducesUnboundTenant`) |
| Requeue error swallowed | Impossible by gate: P1/P2 patterns fire on `_ =` and bare calls; unkeyed-append failure rolls back | `check-audit-swallow.sh:60-61`; `TestRequeuePublicationFactAppendsInTx` |
| Paused purge tick | No SELECT, no DELETE; rows preserved bit-for-bit while replay runs | `TestPurgeDeadLettersScanSkipsWhenDisabled`, `TestOutboxRepo_PurgeDeadLettersPauseGate` |

## 4. Testable acceptance mapping (supplied checks → landed tests, all re-verified present)

| Supplied acceptance | Status | Tests (file:line verified) |
|---|---|---|
| (1) requeue semantics, idempotency, lease guard | **LANDED** | `TestRequeuePublicationResetsDeadLetterRows` (`audit_publication_replay_integration_test.go:109`), `TestRequeueDeadResetsDeliveries` (`:446`), `TestRequeuePublicationLeaseInvariant` (`:409`), `TestRequeuePublicationFactAppendsInTx` (`:194`), `TestRequeuePublicationSecondRequeueMintsDistinctFact` (`:256`), `TestRequeueVsPurgeInterleaving` (`:509`) — REQ-1 |
| (2) `cmd/audit-replay` e2e; row re-enters drain; governance endpoint receives fact | **LANDED** (one optional chain leg) | `TestAuditReplayCLIRunTwiceIsIdempotent` (`cmd/audit-replay/main_test.go:133`), `TestAuditReplayCLIRequeueFactWithTenant` (`:226`), `TestAuditReplaySigningKeyPreFlight` (`:294`), `TestAuditReplayDBOutageFailsClosed` (`:349`), `TestRequeuedPublicationDrainsExactlyOnce` (`internal/service/callback_integration_test.go:878`) — per-`event_id` exactly-once; publisher↔httptest contract in `internal/connector/auditgovernance/publisher_test.go`. Optional leg: tool→drain→real-publisher→httptest single chain (§5.3) |
| (3) purge pause; replay completes before purge resumes | **LANDED** (mechanism drift: drain-loop scan + outbox cleaner, not `RunAuditCleaner`; real clock ±1 min, not fake clock) | `TestPurgeDeadLettersScanSkipsWhenDisabled` (`internal/service/audit_purge_test.go:53`), `TestBuildOutboxCleanerWiringPauseFlag` (`internal/app/outbox_wiring_test.go:61`), `TestOutboxRepo_PurgeDeadLettersPauseGate`, `TestConcurrentPurgeAndRequeueRace` (`:328`) — REQ-3 |
| (4) T-5 regression: no new `_ =` swallow; gate green | **LANDED, gate currently BLOCKED** (§1.2) | `finishPublicationAttempt` keeps `if err := ...` + `ObserveAuditPublicationFinishFailure` (`callback_audit.go:137-141`); script gates green (V9); **compile gates red until §5.1 lands** |

## 5. Migration steps

### 5.1 BLOCKING (parallel campaign, prerequisite for the done gate) — **LANDED 2026-08-08**

1. In `internal/app/new.go:141-152`, pass `auditLedgerRepo` (already in scope from `buildAuditStack`) into the six constructors per §1.2 table. Delete the now-obsolete F-1 comment ("the constructor takes only gdb"). — **done**; the fix landed in the working tree and the F-1 comment was removed.
2. **Test-call-site completion (gate-required, beyond the §1.2 table):** `make check-integration-compile` also requires the six repository test files to pass the new parameter. Completed with the established pattern `repository.NewAuditLedgerRepository(db, repository.AuditLedgerConfig{})`: `block_integration_test.go:19`, `comment_test.go:22`, `file_share_integration_test.go:21`, `message_test.go:22` (`setupMsgRepo`), `reaction_audit_integration_test.go:29`, `search_integration_test.go:26-27`. All six affected tests re-run green against real Postgres.
3. Re-run the compile gates: `make lint && make build && make check-integration-compile && make test`. — **all green** (§6); the replay direction's §7 table is re-claimed green.

### 5.2 This direction's residual delta (verification/documentation only — no production code)

1. Update `docs/plans/2026-08-07-dead-letter-replay-requeuepublication-design.md` status block: "pending implementation" → landed, pointing at the requirements §3.2 divergence table (this design's §1.1 is the as-landed surface).
2. `make test-integration` (testcontainers/Docker) is required by AGENTS.md after repository SQL work and covers the integration-tagged tests in §4; run once with Docker — not run in any of the verification passes.

### 5.3 Optional test-only leg (closes acceptance 2 wording exactly)

One `cmd/audit-replay` integration test that runs `replay(...)`, then one `callbackService.Start` tick with a real `auditgovernance.NewPublisher` pointed at an httptest `/token` + `/api/v1/events` server, asserting one POST per `event_id`. Currently satisfied across two levels (CLI e2e re-enters drain; drain delivers exactly once to a recording publisher; connector httptest suite). Test-only; no production delta.

## 6. Done-gate evidence (post-§5.1 tree, re-run 2026-08-08)

```text
make test                        → ✓ ok (35 packages)
make lint                        → ✓ go vet ./... clean
make lint-strict                 → ✓ golangci-lint run ./... clean
make check-file-sizes            → ✓ "All files ≤ 500 lines"
make check-audit-swallow         → ✓ (roots: internal/service internal/repository internal/app cmd; self-test ok)
make check-anchored-event-ids    → ✓ (C2 allowlist ok, self-test ok)
make build                       → ✓ bin/aero-id + bin/audit-replay built
make check-integration-compile   → ✓ (go test -tags integration -run '^$' ./... + go vet -tags integration ./...)
```

Pinned failure-mode tests (11/11, real Postgres via testpg/Docker): `cmd/audit-replay` 7/7 (incl. `TestAuditReplayCLIRunTwiceIsIdempotent`, `TestAuditReplaySigningKeyPreFlight`, `TestAuditReplayDBOutageFailsClosed`, both i7 boot-hazard tests), `internal/repository` 9/9 (requeue/lease/fact/purge-race suite), `internal/outbox` 1/1 (`TestOutboxRepo_PurgeDeadLettersPauseGate`), `internal/service` 2/2 (`TestRequeuedPublicationDrainsExactlyOnce`, `TestPurgeScanSkipsWhenGovernanceDisabled`). Full `make test-integration` remains the AGENTS.md gate for the parallel in-tx conversion campaign to run on its own completion.

## 7. Amendment register (relative to the evidence)

| # | Evidence claim | Correction |
|---|---|---|
| B1 | §7 "all local done-gate checks re-run green on this tree" | False at evidence time: `lint`/`build`/`check-integration-compile`/`test` failed on `internal/app/new.go:141-152` (in-tx conversion wiring not completed). Script gates (file-sizes, audit-swallow, anchored-event-ids) were green. **After §5.1 landed (plus six test-call-site completions and two trivial campaign-file lint wraps), all eight gates re-run green (§6).** |
| B2 | `TestRequeuedPublicationDrainsExactlyOnce` cited at `callback_integration_test.go:874` | Function at `:878` (comment block at 874) — trivial line drift. **Citation corrected to `:878` in the requirements doc (2026-08-08); design doc §4 already carried `:878`.** |
| B3 | Everything else (V1–V9) | Verified as cited, including all 17 test pins, config validation lines, migration 000045, gate-script patterns, and the stale design status line |
