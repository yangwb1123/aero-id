# Requirements — module `cmd/migrate`: pin B2 DDL shapes (dedup UNIQUE, due/lease/purge indexes, claim columns) in the migration sweep — table existence is not enough

| | |
|---|---|
| Module (batch label) | `cmd/migrate` — migration-sweep coverage (`migrations/migrations_test.go`, integration-tagged). The change is **test-only**: one test file grows an index/constraint/column-assertion leg. Zero production deltas |
| Source analysis | `docs/auto/analyses/cmd-migrate-c1b3771d.json` (direction 3 of 3: DDL-shape pins in the migration sweep). Sibling directions 1 (fail-closed schema-version gate) and 2 (CLI test coverage for `cmd/migrate`) are **out of scope here**; referenced only where their tests would overlap |
| Campaign | `aero-id-b2-in-tx-audit` (B2 fail-closed doctrine). The sweep is the only automated proof that the migration chain delivers the DDL every B2 runtime invariant depends on |
| Design | None yet — this spec is the design source. The direction's evidence states the assertion leg is proposed ("Index-assertion leg proposed.") |
| Date | 2026-08-08 |
| Status | **Proposed — implementation pending.** Every citation in the direction's evidence re-verified against the working tree (§2); two cosmetic line-range drifts (A1, A2) and one un-enumerated load-bearing fact (A3: the dedup constraint's **name** is matched in runtime Go code, not just its column set) |

## 1. Problem statement (direction, preserved)

`migrations/migrations_test.go` asserts only that `audit_ledger`/`audit_publications`/`callback_deliveries` exist after up and vanish after down. Every B2 runtime invariant is index/constraint-shaped and unpinned:

- the `(tenant_id, source, event_id)` UNIQUE dedup on `audit_ledger` (000050) that makes stable anchored event_ids dedup-friendly (B2-4),
- `uq_audit_publication_fact` + `idx_audit_publications_due`/`lease` (000045) that the SKIP LOCKED claim (`internal/repository/audit_publication.go:55`) and lease fencing rely on,
- `idx_audit_publications_purge` / `idx_callback_deliveries_purge` (000059) that turn `PurgeDeadLetterPublications`' (`updated_at < cutoff`, order `updated_at, id`) predicate into a range bound instead of a per-tick seq scan.

A future edit of an applied migration or a new migration that renames/drops any of these passes the sweep green while silently degrading purge/replay or breaking dedup fencing.

**Verified failure shape (§2.3):** `internal/repository/audit_ledger.go:202-218` translates the duplicate-append INSERT error to `ErrAuditDuplicate` by matching `pgErr.ConstraintName == "uq_audit_ledger_tenant_event"` — the constraint **name** is part of the runtime contract. A rename of the constraint (or a new migration replacing it with an equivalent-looking UNIQUE under a new name) would pass today's existence-only sweep and silently break the duplicate contract's error classification (C1): the loser of a concurrent append would surface a raw 23505 instead of the service-level duplicate error. This strengthens the direction: the pin must assert names and definitions, not just column sets.

## 2. Verified evidence (working tree; line numbers live)

### 2.1 Direction-evidence claims (re-checked)

| # | Claim (direction evidence) | Verification |
|---|---|---|
| E1 | `migrations/migrations_test.go:60-72` — landmark assertion = table existence only | ✅ holds with cosmetic drift (A1): the up-landmark comment+loop block is at `:65-78` (`for _, table := range []string{"files", "messages", "comments", "audit_ledger", "audit_publications", "callback_deliveries"}` at `:73`, `tableExists` at `:74`). The whole file (164 lines) contains **exactly two** `tableExists` loops (up `:73-78`, down `:94-99`) and **zero** `pg_indexes`/`pg_constraint`/`information_schema.columns` queries |
| E2 | `:93-103` — down: table absence only | ✅ same loop shape at `:94-99` ("Assert clean rollback"); only `tableExists` |
| E3 | `migrations/000045_audit_publications.up.sql` — `uq_audit_publication_fact`, `idx_audit_publications_due`/`lease`, `lease_owner`/`lease_until` columns | ✅ exact: `CONSTRAINT uq_audit_publication_fact UNIQUE (fact_id)`; `idx_audit_publications_due ON audit_publications (status, next_attempt_at ASC, id ASC)`; `idx_audit_publications_lease ON audit_publications (status, lease_until, next_attempt_at ASC)`; `lease_owner text NOT NULL DEFAULT ''`, `lease_until timestamptz` |
| E4 | `migrations/000050_scope_events_and_audit_by_tenant.up.sql:8,16` — UNIQUE `(tenant_id, source, event_id)` on `audit_ledger` and `event_receipts` | ✅ exact: `:7-8` `uq_event_receipt_tenant_source_event UNIQUE (tenant_id, source, event_id)`; `:15-16` `uq_audit_ledger_tenant_event UNIQUE (tenant_id, source, event_id)`. The down (`000050_...down.sql`) drops both and re-adds the pre-tenant `(source, event_id)` constraints |
| E5 | `migrations/000059_add_dead_letter_purge_indexes.up.sql` — `(status, updated_at ASC, id ASC)` on both tables | ✅ exact: `idx_audit_publications_purge` and `idx_callback_deliveries_purge`, both `(status, updated_at ASC, id ASC)`; comment names `PurgeDeadLetterPublications`/`PurgeDeadLetterDeliveries` as the consumers. Down drops both |
| E6 | `internal/repository/audit_publication_replay.go` `PurgeDeadLetterPublications` — `updated_at < cutoff` + order | ✅ `:438`; predicate `status = 'dead_letter' AND lease_owner = '' AND (lease_until IS NULL OR lease_until < now()) AND updated_at < ?`, `Order("updated_at ASC, id ASC")`, SKIP LOCKED subquery, `Limit`; doc comment: "the cutoff is an index range bound on `idx_audit_publications_purge`" |
| E7 | `internal/repository/audit_publication.go:55-56` — `clause.Locking` SkipLocked | ✅ `:55` `tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked})` in `ClaimPublication`; predicate `(status = 'pending' AND next_attempt_at <= now()) OR (status = 'delivering' AND lease_until < now())`, `Order("next_attempt_at ASC, id ASC")` — the exact shape of `idx_audit_publications_due`/`lease` |
| E8 | T-10.1's four named integration tests exist | ✅ `TestConcurrentClaimPublicationPartition` (`internal/repository/audit_consolidation_integration_test.go:599`), `TestExpiredLeasePublicationReclaimedByAnotherWorker` (`:301`), `TestPurgeDeadLetterPublicationsPredicate` (`internal/repository/audit_publication_replay_integration_test.go:805`), `TestFirstEventGovernanceE2E` (`internal/app/audit_integration_test.go:563`). All four migrate via `testpg.Migrate` (7 call sites across the three files) — i.e. they run against the **same full up-file set** the sweep applies |

### 2.2 Corroborating runtime shapes (same DDL family, cited in 000059/000038)

- `internal/repository/callback_audit.go:281` `PurgeDeadLetterDeliveries` — identical predicate/order shape; doc comment cites `idx_callback_deliveries_purge` as the range bound.
- `internal/repository/callback.go:196-204` claim — SKIP LOCKED, `(status = 'pending' AND next_attempt_at <= ?) OR (status = 'delivering' AND lease_until < ?)`, `Order("next_attempt_at ASC, id ASC")`, writes `lease_owner`/`lease_until` — the columns 000038 (`migrations/000038_harden_callback_delivery_claims.up.sql`) adds, and the shape of `idx_callback_deliveries_lease`/`due` (000036/000038).
- `internal/repository/event_receipt.go:196,223` — `clause.OnConflict{DoNothing: true}` inserts: the `uq_event_receipt_tenant_source_event` dedup is the only guard against duplicate `(tenant_id, source, event_id)` receipts.

### 2.3 Un-enumerated load-bearing fact (A3)

`internal/repository/audit_ledger.go:202-218` `createAuditRows`: on INSERT error, `errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_audit_ledger_tenant_event"` → `ErrAuditDuplicate`; **any other** 23505 (PK collision, `uq_audit_ledger_chain_seq`, publication PK) stays raw so the enclosing transaction rolls back. The comment makes the intent explicit: "The mapping is constraint-precise on purpose." A constraint rename therefore changes runtime behavior — the sweep's `pg_constraint` pin must assert the constraint **name** `uq_audit_ledger_tenant_event` (not just a UNIQUE on the three columns).

### 2.4 Gate context (T-4)

- `migrations/migrations_test.go` is `//go:build integration` — it runs under `make test-integration` (`go test -tags=integration -p 1 ./... -count=1`, `Makefile:15`), never `make test`.
- `make check-audit-swallow` (`Makefile:67-69`) roots: `internal/service internal/repository internal/app internal/storage internal/block cmd` — `migrations/` is **not** scanned. `make check-anchored-event-ids` (`Makefile:75-77`) roots: `internal cmd` — also excludes `migrations/`. A test-only change in `migrations/` cannot trip either gate.
- `make check-file-sizes` exempts non-test/non-generated Go files only — `migrations/migrations_test.go` is a test file; no constraint (file is 164 lines today; growth to ~260 is fine).
- `make check-integration-compile` compiles every integration-tagged `_test.go` — new test helpers must compile under it.

## 3. Design decisions (bounded by the direction)

1. **Assert in the existing sweep, not a new test.** The assertion leg runs inside `TestMigrationsUpAndDown` at the same two existing checkpoints: after the ascending up chain (post-000059 state) and after the descending down chain (post-000001 state). No new test file, no new DB setup, no new build tag. The descending down order (000059 first, 000001 last) is already the FK-dependency-safe order the sweep uses; the absence leg runs where the rollback-absence loop runs today.
2. **Pin names *and* normalized definitions.** Postgres normalizes `ASC` (default direction) away, so `pg_indexes.indexdef` for the 000059 indexes renders `(status, updated_at, id)`. The assertions compare against the **normalized** definitions: exact indexdef strings via `pg_indexes` (`indexname`, `indexdef`), exact constraint definitions via `pg_constraint` + `pg_get_constraintdef(oid)`, exact column shapes via `information_schema.columns`. A migration that reorders columns, drops `ASC`, widens the index, or renames the object fails the pin even though the object "exists".
3. **The audit_ledger dedup pin is name-precise (A3).** Assert `conname = 'uq_audit_ledger_tenant_event'` **and** `pg_get_constraintdef = 'UNIQUE (tenant_id, source, event_id)'` — the name is the runtime contract (`audit_ledger.go:218`), the definition is the planner/durability contract. Same treatment for `uq_event_receipt_tenant_source_event` (000050's second constraint, cited in the direction evidence at `:8`).
4. **Claim-column pin (direction title).** `audit_publications.lease_owner` (`NOT NULL`, default `''::text`) and `lease_until` (nullable `timestamptz`) per 000045 — the columns `ClaimPublication` writes and `PurgeDeadLetterPublications`' predicate excludes on. Same family on `callback_deliveries` (000038 columns) since the direction title's "claim columns" is unqualified and the callback claim writes them (`callback.go:204`). Minimal: presence + `is_nullable` + `column_default`, not the full column list.
5. **No behavior probes.** The T-10.1 tests already prove the queries work against the migration set; the sweep pins the DDL. The two together are the coupling proof. The sweep stays a DDL-only sweep (no data, no planner assertions) — a `SET enable_seqscan` probe would expand scope into query-shape testing that `TestPurgeDeadLetterPublicationsPredicate`-class tests already own.

## 4. Requirements (supplied acceptance checks preserved; testable criteria)

### REQ-1 — T-10.2: the sweep pins every named B2 object's existence and shape after up, and their absence after the descending down sweep

> Supplied acceptance (preserved): "T-10.2: after full up, pg_indexes/pg_constraint assertions for idx_audit_publications_purge, idx_callback_deliveries_purge, idx_audit_publications_due/lease, uq_audit_publication_fact, and the audit_ledger dedup UNIQUE (tenant_id, source, event_id) — all absent after the descending down sweep."

Testable criteria (integration, `migrations/migrations_test.go`, inside `TestMigrationsUpAndDown` at the existing up/down checkpoints; new helpers `indexDef(t, db, name) (indexdef string, ok bool)` and `constraintDef(t, db, name) (def string, ok bool)`):

1. **Up leg — indexes (existence + normalized definition):**

   | indexname | expected `pg_indexes.indexdef` (normalized) |
   |---|---|
   | `idx_audit_publications_due` | `CREATE INDEX idx_audit_publications_due ON public.audit_publications USING btree (status, next_attempt_at, id)` |
   | `idx_audit_publications_lease` | `CREATE INDEX idx_audit_publications_lease ON public.audit_publications USING btree (status, lease_until, next_attempt_at)` |
   | `idx_audit_publications_purge` | `CREATE INDEX idx_audit_publications_purge ON public.audit_publications USING btree (status, updated_at, id)` |
   | `idx_callback_deliveries_purge` | `CREATE INDEX idx_callback_deliveries_purge ON public.callback_deliveries USING btree (status, updated_at, id)` |

   (Exact rendered forms — schema qualification, `USING btree`, parentheses — are pinned from the live container output during implementation and asserted verbatim, so a column reorder or dropped tail column fails the pin. The required columns, in order, are the hard contract: `(status, next_attempt_at, id)`, `(status, lease_until, next_attempt_at)`, `(status, updated_at, id)` × 2.)

2. **Up leg — constraints (existence + name + definition):**

   | conname | expected `pg_get_constraintdef(oid)` |
   |---|---|
   | `uq_audit_publication_fact` | `UNIQUE (fact_id)` |
   | `uq_audit_ledger_tenant_event` | `UNIQUE (tenant_id, source, event_id)` |
   | `uq_event_receipt_tenant_source_event` | `UNIQUE (tenant_id, source, event_id)` |

   The `audit_ledger` pin asserts the name too — a rename to any other name fails even with identical columns (A3, `audit_ledger.go:218`).

3. **Up leg — claim columns (direction title):** `information_schema.columns` — `audit_publications.lease_owner`: `is_nullable = NO`, `column_default = ''::text`; `audit_publications.lease_until`: `is_nullable = YES`, `column_default IS NULL`; same two shapes on `callback_deliveries` (000038 columns, same family per §3.4).

4. **Down leg — absence after the descending sweep (post-000001 state):** `indexDef` returns not-ok for all four index names; `constraintDef` returns not-ok for all three constraint names; `audit_publications` absent via the existing `tableExists`. Note the sweep's descending order makes this meaningful per object: 000059's down drops the purge indexes, 000050's down drops the tenant constraints (re-adding `uq_audit_ledger_event`/`uq_event_receipt_source_event` — the pin must not assert those are absent), 000045's down drops the table and everything on it. A down that stops dropping (e.g. a future migration's down missing a `DROP INDEX`) fails here.

5. **Structural:** the assertion leg reuses the existing `sqlDB` handle and `tableExists` helper; no new DB, no new files beyond `migrations/migrations_test.go` (164 → ~260 lines; test file, exempt from `check-file-sizes`). `make check-integration-compile` green.

### REQ-2 — T-10.1: the four named runtime tests keep passing against the same migration set, proving DDL shape matches runtime queries

> Supplied acceptance (preserved): "T-10.1: existing repo integration tests (TestConcurrentClaimPublicationPartition, TestExpiredLeasePublicationReclaimedByAnotherWorker, TestPurgeDeadLetterPublicationsPredicate, TestFirstEventGovernanceE2E) keep passing against the same migration set, proving DDL shape matches runtime queries."

Testable criteria:

1. `go test -tags=integration ./internal/repository/ -run 'TestConcurrentClaimPublicationPartition|TestExpiredLeasePublicationReclaimedByAnotherWorker|TestPurgeDeadLetterPublicationsPredicate' -count=1` — all pass (they migrate via `testpg.Migrate`, the same up-file set the sweep applies, §2.1 E8).
2. `go test -tags=integration ./internal/app/ -run TestFirstEventGovernanceE2E -count=1` — passes.
3. No changes to `internal/repository` or `internal/app` are permitted by this direction; the T-10.1 proof is the unmodified green run alongside the new pins. A future DDL edit that degrades purge/claim behavior while keeping the sweep green is now impossible for the pinned objects (REQ-1) and is caught behaviorally by these tests for everything else.

### REQ-3 — T-4: pure test change — zero production deltas; static gates stay green

> Supplied acceptance (preserved): "T-4: pure test change — check-audit-swallow/check-anchored-event-ids stay green, zero production deltas."

Testable criteria:

1. **Zero production deltas:** `git diff --stat` after implementation touches only `migrations/migrations_test.go` (and nothing else). No `internal/` file, no `cmd/` file, no `migrations/*.sql`, no `Makefile` change.
2. `make check-audit-swallow` green — `migrations/` is not in its roots (`Makefile:69`), and no audit call is added anyway.
3. `make check-anchored-event-ids` green — `migrations/` is not in its roots (`Makefile:77`).
4. `make test` green (unit suite unaffected — the sweep is integration-tagged) and `make lint`/`make lint-strict` green (test-file only; new helpers stay under the function limits).

## 5. Compatibility constraints

- **No migration file changes, no new migration.** `migrations/*.sql` are untouched; the sweep keeps applying raw files in ascending/descending order exactly as today.
- **No production behavior change.** Assertions only observe `pg_indexes`/`pg_constraint`/`information_schema.columns`; nothing in `internal/`, `cmd/`, config, or the wire surface changes.
- **Down-leg absence is name-scoped.** The 000050 down legitimately re-adds `uq_audit_ledger_event`/`uq_event_receipt_source_event`; the absence assertions target only the three pinned constraint names and four pinned index names, never "no constraints/indexes at all".
- **Normalized definitions are the contract.** Postgres renders `ASC`-only columns without the qualifier; the pins use the normalized forms so the sweep asserts real DDL, not source-file cosmetics.
- **Test-file growth is unbounded by the size gate** (`check-file-sizes` exempts test files), but the file stays a single cohesive sweep; helpers are local to `migrations_test.go`.

## 6. Failure modes (closed by this direction)

| Failure | Closure | Pin |
|---|---|---|
| Applied migration 000045/000050/000059 edited (column reorder, dropped tail, renamed constraint, weakened UNIQUE) | Up-leg shape pins fail red on the exact definition/name; today the sweep passes green | REQ-1.1–REQ-1.3 |
| New migration renames/drops a pinned object (e.g. replaces `uq_audit_ledger_tenant_event`) | Name-precise `pg_constraint` pin fails; today the 23505→`ErrAuditDuplicate` mapping (`audit_ledger.go:218`) would silently degrade | REQ-1.2 (name), A3 |
| New migration's down fails to drop its objects | Down-leg absence pins fail red | REQ-1.4 |
| DDL shape drifts from runtime queries (purge cutoff stops being a range bound; claim predicate unindexed) | Shape pins + the four unmodified T-10.1 tests jointly fail; pins document the coupling the tests exercise | REQ-1 + REQ-2 |
| Sweep mis-fires on legitimate rollback state | Absence pins are name-scoped, allowing the pre-000050 legacy constraints to exist after down | REQ-1.4, §5 |

## 7. Done-gate evidence (planned for the implement/verify stage)

```text
make test && make lint && make lint-strict && make check-file-sizes \
  && make check-audit-swallow && make check-anchored-event-ids && make check-integration-compile && make build
make test-integration    # runs the sweep (REQ-1) + the four T-10.1 tests (REQ-2)
git diff --stat          # migrations/migrations_test.go only (REQ-3.1)
```

`make test-integration` is required because the sweep is integration-tagged and the acceptance's behavioral proof lives in integration tests. Cross-data-center/load gates are untouched.

## 8. Testable acceptance mapping (supplied checks preserved verbatim)

| Supplied acceptance (direction) | Status | Testable criteria (proposed pins) |
|---|---|---|
| **T-10.2** "after full up, pg_indexes/pg_constraint assertions for idx_audit_publications_purge, idx_callback_deliveries_purge, idx_audit_publications_due/lease, uq_audit_publication_fact, and the audit_ledger dedup UNIQUE (tenant_id, source, event_id) — all absent after the descending down sweep" | **Proposed** | REQ-1: up leg asserts the four indexdefs verbatim (REQ-1.1) and the three constraint names + `pg_get_constraintdef` (REQ-1.2, name-precise for `uq_audit_ledger_tenant_event` per A3), plus the title's claim columns (REQ-1.3); down leg asserts all seven absent by name after the descending sweep (REQ-1.4) |
| **T-10.1** "existing repo integration tests (TestConcurrentClaimPublicationPartition, TestExpiredLeasePublicationReclaimedByAnotherWorker, TestPurgeDeadLetterPublicationsPredicate, TestFirstEventGovernanceE2E) keep passing against the same migration set, proving DDL shape matches runtime queries" | **Proposed** | REQ-2: all four run unmodified against `testpg.Migrate`'s up-file set and pass in `make test-integration`; no repository/app deltas permitted |
| **T-4** "pure test change — check-audit-swallow/check-anchored-event-ids stay green, zero production deltas" | **Proposed** | REQ-3: `git diff --stat` = `migrations/migrations_test.go` only; both gates green (`migrations/` outside both roots); `make test`/`lint`/`check-integration-compile` green |

## 9. Amendment register (stale or un-enumerated wording in the direction / source analysis)

| # | Claim | Correction | Folded into |
|---|---|---|---|
| A1 | "`migrations/migrations_test.go:60-72` … `:93-103`" | Cosmetic drift: the up-landmark loop is at `:73-78` (comment block `:65-72`), the down-absence loop at `:94-99` (comment `:93`). Substance holds: the file's only assertions are the two `tableExists` loops; zero `pg_indexes`/`pg_constraint` queries anywhere | §2.1 E1/E2 |
| A2 | "`internal/repository/audit_publication.go:55-56`" | The `clause.Locking` clause is a single statement at `:55` (wrapping to `:56`); `ClaimPublication` starts at `:46`. Holds | §2.1 E7 |
| A3 | (un-enumerated) the dedup constraint's **name** is a runtime contract, not just its column set | `internal/repository/audit_ledger.go:218` matches `pgErr.ConstraintName == "uq_audit_ledger_tenant_event"` to translate 23505 → `ErrAuditDuplicate`; other 23505s stay raw by design ("constraint-precise on purpose"). A rename breaks error classification silently — the pin must assert the name, which the direction's acceptance ("the audit_ledger dedup UNIQUE (tenant_id, source, event_id)") implies but does not state | §2.3, REQ-1.2, §6 |

---

### Done-gate relevance

This direction is **test-only** (one integration-tagged test file grows an assertion leg). It changes no production code, no migration file, and no shared harness — so the full `make test-integration` run is the only heavyweight gate, and the static gates (`check-audit-swallow`, `check-anchored-event-ids`) are green by construction (§2.4). Sibling direction 1 owns the schema-version gate and sibling direction 2 owns `cmd/migrate` CLI coverage; neither is required for this spec's acceptance, and no requirement above expands into them.
