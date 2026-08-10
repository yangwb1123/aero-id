# Design — module `cmd/migrate`: pin B2 DDL shapes (dedup UNIQUE, due/lease/purge indexes, claim columns) in the migration sweep

| | |
|---|---|
| Source spec | `docs/requirements/2026-08-08-cmd-migrate-pin-b2-ddl-shapes.md` (REQ-1..REQ-3, acceptance T-10.2/T-10.1/T-4) |
| Design status | **Proposed** — concrete test-only surface, pinned strings, call sites, rollout |
| Evidence status | Every claim in the requirements doc re-verified against head `b005409` this pass; the four indexdefs / three constraintdefs / four column shapes additionally captured as **live ground truth** from `postgres:16-alpine` (16.14) running the exact full up-file chain the sweep applies (§0.2) — the spec's "pin from live container output during implementation" step is resolved here, not deferred |
| Change surface | **Test-only.** `migrations/migrations_test.go` 164 → ~300 lines (test file; `make check-file-sizes` exempts test files). Zero production deltas, zero `migrations/*.sql` edits, zero new files |
| API changes | **None outside the test package.** New unexported helpers + pinned data tables in `migrations_test.go` only (§1). No production symbol, wire surface, config, or migration changes |

## 0. Evidence verification (untrusted claims → verified facts)

### 0.1 Claims verified exactly (this pass, against the working tree)

| Claim (requirements spec §2) | Result |
|---|---|
| A1: `migrations/migrations_test.go` is table-existence-only; up loop `:73-78`, down loop `:94-99`, zero `pg_indexes`/`pg_constraint` queries anywhere | ✅ exact — file is 164 lines; the only two assertions are `tableExists` loops; the sole `information_schema` query is `information_schema.tables` in `tableExists` (`:158`) |
| A2: `audit_publication.go:55-56` SKIP LOCKED in `ClaimPublication` (`:46`) | ✅ exact — `clause.Locking` at `:55`; predicate `(status = 'pending' AND next_attempt_at <= now()) OR (status = 'delivering' AND lease_until < now())`, `Order("next_attempt_at ASC, id ASC")` — the shape of `idx_audit_publications_due`/`lease` |
| A3: `audit_ledger.go:218` matches `pgErr.ConstraintName == "uq_audit_ledger_tenant_event"` for 23505 → `ErrAuditDuplicate` | ✅ exact — `createAuditRows` (:196) maps only that constraint name; any other 23505 stays raw ("constraint-precise on purpose"). The **name** is a runtime contract; the pin must assert `conname`, not just the column set |
| E3: 000045 up — `uq_audit_publication_fact UNIQUE (fact_id)`; `idx_audit_publications_due (status, next_attempt_at ASC, id ASC)`; `idx_audit_publications_lease (status, lease_until, next_attempt_at ASC)`; `lease_owner text NOT NULL DEFAULT ''`; `lease_until timestamptz` | ✅ exact; 000045 down is `DROP TABLE IF EXISTS audit_publications` |
| E4: 000050 up `:7-8`/`:15-16` — `uq_event_receipt_tenant_source_event` / `uq_audit_ledger_tenant_event`, both `UNIQUE (tenant_id, source, event_id)` | ✅ exact; 000050 down drops both **and re-adds** `uq_audit_ledger_event` / `uq_event_receipt_source_event` — the down-leg absence pin must be name-scoped (§1.4) |
| E5: 000059 up — `idx_audit_publications_purge` / `idx_callback_deliveries_purge`, both `(status, updated_at ASC, id ASC)` | ✅ exact; 000059 down drops both |
| E6/E7/§2.2: `PurgeDeadLetterPublications` (`audit_publication_replay.go:438`) and `PurgeDeadLetterDeliveries` (`callback_audit.go:281`) — `status = 'dead_letter' AND lease_owner = '' AND (lease_until IS NULL OR lease_until < now()) AND updated_at < ?`, `Order("updated_at ASC, id ASC")`; `FetchDueDeliveries` (`callback.go:196-204`) writes `lease_owner`/`lease_until` | ✅ exact — all four queries' predicates/orders match the pinned index shapes and claim columns |
| E8: four T-10.1 tests exist; all migrate via `testpg.Migrate` (same full up-file set) | ✅ exact — `TestConcurrentClaimPublicationPartition` (`audit_consolidation_integration_test.go:599`), `TestExpiredLeasePublicationReclaimedByAnotherWorker` (`:301`), `TestPurgeDeadLetterPublicationsPredicate` (`audit_publication_replay_integration_test.go:805`), `TestFirstEventGovernanceE2E` (`internal/app/audit_integration_test.go:563`); 7 `testpg.Migrate` call sites across the three files |
| §2.4 gates: `migrations/` outside `check-audit-swallow` roots (`Makefile:69`: `internal/service internal/repository internal/app internal/storage internal/block cmd`) and `check-anchored-event-ids` roots (`Makefile:77`: `internal cmd`) | ✅ exact; sweep is `//go:build integration` (never `make test`); `check-integration-compile` compiles all integration-tagged test files |
| `testpg.Start` = `postgres:16-alpine`, `testpg.Migrate` = raw lexical `*.up.sql` exec, no `schema_migrations` bookkeeping | ✅ exact (`testpg.go:25`, `:53-74`) — the sweep's container version is pinned, which makes the rendered-string pins stable (§6) |

### 0.2 Live ground truth (new evidence, resolves the spec's deferred pin step)

Ran the **exact full up-file chain** the sweep applies (`for f in $(ls migrations/*.up.sql | sort)`) against a throwaway `postgres:16-alpine` container (server 16.14) and captured the rendered catalog output. These are the strings the pins assert:

**Indexes (`pg_indexes.indexdef`, `schemaname='public'`):**

| indexname | rendered indexdef (PG 16.14) |
|---|---|
| `idx_audit_publications_due` | `CREATE INDEX idx_audit_publications_due ON public.audit_publications USING btree (status, next_attempt_at, id)` |
| `idx_audit_publications_lease` | `CREATE INDEX idx_audit_publications_lease ON public.audit_publications USING btree (status, lease_until, next_attempt_at)` |
| `idx_audit_publications_purge` | `CREATE INDEX idx_audit_publications_purge ON public.audit_publications USING btree (status, updated_at, id)` |
| `idx_callback_deliveries_purge` | `CREATE INDEX idx_callback_deliveries_purge ON public.callback_deliveries USING btree (status, updated_at, id)` |

**Constraints (`pg_constraint.conname` + `pg_get_constraintdef(c.oid)`, joined to `pg_class`):**

| conname | rendered definition |
|---|---|
| `uq_audit_publication_fact` | `UNIQUE (fact_id)` |
| `uq_audit_ledger_tenant_event` | `UNIQUE (tenant_id, source, event_id)` |
| `uq_event_receipt_tenant_source_event` | `UNIQUE (tenant_id, source, event_id)` |

**Claim columns (`information_schema.columns`):**

| table.column | is_nullable | column_default |
|---|---|---|
| `audit_publications.lease_owner` | `NO` | `''::text` |
| `audit_publications.lease_until` | `YES` | NULL |
| `callback_deliveries.lease_owner` | `NO` | `''::text` |
| `callback_deliveries.lease_until` | `YES` | NULL |

Confirmed: PG renders `ASC`-only columns without the qualifier; `public.` schema qualification and `USING btree` are part of the rendered form; `pg_get_constraintdef` returns the definition without the table name. The spec's predicted normalized forms (§4 REQ-1.1/1.2) are byte-identical to the live output — **no re-pin drift**.

### 0.3 Nuances found (doc wording, not substance)

1. **The spec's "pin from live container output during implementation" is now resolved**: §0.2 is the captured ground truth (postgres:16-alpine / 16.14). The implementer still re-confirms on their run (the pins fail loudly if the container version renders differently — see §6).
2. **`information_schema.columns.is_nullable` is text (`YES`/`NO`), not boolean** — the design's query renders it as `is_nullable = 'NO'` to scan into a Go `bool` (§1.2); the spec's criterion "is_nullable = NO" maps directly.
3. **Line budget drifts upward vs the spec's ~260 estimate**: the four verbatim indexdef strings + three constraintdefs + four column shapes as data tables add ~40 lines over the estimate → ~300. Still trivially within the file-size budget (test files are exempt from `check-file-sizes`; the 500-line limit is for non-test Go).
4. **`callback_deliveries` claim columns come from 000038, not 000045** — the spec says "000038 columns" and the container confirms both tables render identical claim-column shapes; the pin's `columnShape` data rows carry the table name, so this is a data point, not a design branch.

## 1. API changes

**Zero production API changes.** This direction adds no exported symbol, no handler, no config, no migration. The entire change is the growth of one integration-tagged test file. "API" here means the test-only surface: three catalog-lookup helpers, four shape-assert helpers (up-leg ×3, down-leg ×1), and two pinned data tables.

### 1.1 Pinned data tables (package-level, in `migrations_test.go`)

```go
// b2IndexShapes pins the B2 operational indexes by name AND rendered
// definition (PostgreSQL 16, testpg's postgres:16-alpine; captured from a
// live container on 2026-08-08, see design §0.2). ASC renders unqualified.
// A column reorder, dropped tail column, or widened index fails the pin even
// though the index "exists".
var b2IndexShapes = []struct {
	name, def string
}{
	{"idx_audit_publications_due", `CREATE INDEX idx_audit_publications_due ON public.audit_publications USING btree (status, next_attempt_at, id)`},
	{"idx_audit_publications_lease", `CREATE INDEX idx_audit_publications_lease ON public.audit_publications USING btree (status, lease_until, next_attempt_at)`},
	{"idx_audit_publications_purge", `CREATE INDEX idx_audit_publications_purge ON public.audit_publications USING btree (status, updated_at, id)`},
	{"idx_callback_deliveries_purge", `CREATE INDEX idx_callback_deliveries_purge ON public.callback_deliveries USING btree (status, updated_at, id)`},
}

// b2ConstraintShapes pins the B2 constraints by table, name, and definition.
// The NAME is the runtime contract: audit_ledger.go:218 translates 23505 to
// ErrAuditDuplicate only when pgErr.ConstraintName == "uq_audit_ledger_tenant_event".
var b2ConstraintShapes = []struct {
	table, name, def string
}{
	{"audit_publications", "uq_audit_publication_fact", "UNIQUE (fact_id)"},
	{"audit_ledger", "uq_audit_ledger_tenant_event", "UNIQUE (tenant_id, source, event_id)"},
	{"event_receipts", "uq_event_receipt_tenant_source_event", "UNIQUE (tenant_id, source, event_id)"},
}

// b2ColumnShapes pins the claim columns (direction title): lease_owner is NOT
// NULL with ''::text default; lease_until is nullable with no default — on
// both audit_publications (000045) and callback_deliveries (000038).
var b2ColumnShapes = []struct {
	table, column       string
	notNull, hasDefault bool
	def                 string
}{
	{"audit_publications", "lease_owner", true, true, `''::text`},
	{"audit_publications", "lease_until", false, false, ""},
	{"callback_deliveries", "lease_owner", true, true, `''::text`},
	{"callback_deliveries", "lease_until", false, false, ""},
}
```

### 1.2 Catalog-lookup helpers (unexported; same package)

```go
// indexDef returns the rendered pg_indexes.indexdef for name, or ok=false
// when the index does not exist. indexname is unique per schema, so the
// schemaname filter is the only scoping needed; a same-named index on a
// different table would render a different indexdef and fail the caller's
// comparison anyway.
func indexDef(t *testing.T, sqlDB *sql.DB, name string) (def string, ok bool) {
	t.Helper()
	err := sqlDB.QueryRow(
		`SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1`,
		name,
	).Scan(&def)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("query index %q: %v", name, err)
	}
	return def, true
}

// constraintDef returns pg_get_constraintdef(c.oid) for the named constraint
// on the named table, or ok=false when absent. The conrelid join scopes the
// lookup by TABLE as well as name: conname is only unique per relation, and a
// same-named constraint on a different table must never satisfy the pin.
func constraintDef(t *testing.T, sqlDB *sql.DB, table, name string) (def string, ok bool) {
	t.Helper()
	err := sqlDB.QueryRow(`
		SELECT pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = 'public' AND t.relname = $1 AND c.conname = $2`,
		table, name,
	).Scan(&def)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("query constraint %q on %q: %v", name, table, err)
	}
	return def, true
}

// columnShape reports (notNull, hasDefault, default) for table.column.
// is_nullable renders as text YES/NO in information_schema, hence the
// boolean expression in the SELECT rather than a raw text scan.
func columnShape(t *testing.T, sqlDB *sql.DB, table, column string) (notNull, hasDefault bool, def string, ok bool) {
	t.Helper()
	err := sqlDB.QueryRow(`
		SELECT is_nullable = 'NO', column_default IS NOT NULL, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`,
		table, column,
	).Scan(&notNull, &hasDefault, &def)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, "", false
	}
	if err != nil {
		t.Fatalf("query column %q on %q: %v", column, table, err)
	}
	return notNull, hasDefault, def, true
}
```

### 1.3 Up-leg assertion helpers (called after the existing up `tableExists` loop)

```go
// assertB2IndexShapes pins existence AND rendered definition of the four B2
// indexes after the full up chain. Definition mismatch is reported with both
// forms so a red sweep shows the actual render for a conscious re-pin.
func assertB2IndexShapes(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	for _, want := range b2IndexShapes {
		def, ok := indexDef(t, sqlDB, want.name)
		if !ok {
			t.Errorf("index %q missing after up migrations", want.name)
			continue
		}
		if def != want.def {
			t.Errorf("index %q shape drift:\n  got:  %s\n  want: %s", want.name, def, want.def)
		} else {
			t.Logf("index %q: %s", want.name, def)
		}
	}
}

// assertB2ConstraintShapes pins existence, table, name, and definition of the
// three B2 constraints. Name precision is load-bearing (A3).
func assertB2ConstraintShapes(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	for _, want := range b2ConstraintShapes {
		def, ok := constraintDef(t, sqlDB, want.table, want.name)
		if !ok {
			t.Errorf("constraint %q on %q missing after up migrations", want.name, want.table)
			continue
		}
		if def != want.def {
			t.Errorf("constraint %q on %q definition drift:\n  got:  %s\n  want: %s", want.name, want.table, def, want.def)
		} else {
			t.Logf("constraint %q on %q: %s", want.name, want.table, def)
		}
	}
}

// assertB2ColumnShapes pins the claim columns (direction title).
func assertB2ColumnShapes(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	for _, want := range b2ColumnShapes {
		notNull, hasDefault, def, ok := columnShape(t, sqlDB, want.table, want.column)
		if !ok {
			t.Errorf("column %q on %q missing after up migrations", want.column, want.table)
			continue
		}
		if notNull != want.notNull || hasDefault != want.hasDefault || def != want.def {
			t.Errorf("column %q on %q shape drift:\n  got:  not_null=%v has_default=%v default=%q\n  want: not_null=%v has_default=%v default=%q",
				want.column, want.table, notNull, hasDefault, def, want.notNull, want.hasDefault, want.def)
		}
	}
}
```

### 1.4 Down-leg absence helper (called after the existing down `tableExists` loop)

```go
// assertB2ShapesAbsent pins that all seven pinned objects are gone after the
// descending down sweep. Name-scoped on purpose: 000050's down legitimately
// re-adds uq_audit_ledger_event / uq_event_receipt_source_event (which are
// NOT pinned and must NOT be asserted absent); only the pinned names count.
func assertB2ShapesAbsent(t *testing.T, sqlDB *sql.DB) {
	t.Helper()
	for _, want := range b2IndexShapes {
		if def, ok := indexDef(t, sqlDB, want.name); ok {
			t.Errorf("index %q still present after down migrations: %s", want.name, def)
		}
	}
	for _, want := range b2ConstraintShapes {
		if def, ok := constraintDef(t, sqlDB, want.table, want.name); ok {
			t.Errorf("constraint %q on %q still present after down migrations: %s", want.name, want.table, def)
		}
	}
}
```

### 1.5 Call sites in `TestMigrationsUpAndDown` (the only edits to the existing flow)

1. After the existing up `tableExists` loop (post-000059 state), before the down sweep begins:
   ```go
   assertB2IndexShapes(t, sqlDB)
   assertB2ConstraintShapes(t, sqlDB)
   assertB2ColumnShapes(t, sqlDB)
   ```
2. After the existing down `tableExists` loop (post-000001 state):
   ```go
   assertB2ShapesAbsent(t, sqlDB)
   ```

No new test function, no new DB, no new build tag, no new files. The helpers reuse the already-obtained `sqlDB` handle (`db.DB()` at the up checkpoint; the same handle stays valid through the down sweep — `tableExists` already proves that pattern today).

New imports: `errors` (stdlib). Existing imports (`database/sql`, `testing`) already cover the rest.

**Line accounting:** 164 existing + ~30 data tables + ~70 lookup helpers + ~65 assert helpers + ~5 call sites/comments + 1 import ≈ 300. Every function stays well under funlen 50 / cyclop 10 (largest is `columnShape`-family at ~20 statements).

## 2. Compatibility constraints

- **No migration file changes, no new migration, no `schema_migrations` interaction.** `migrations/*.sql` are untouched; the sweep keeps applying raw files in ascending/descending order exactly as today (`discoverMigrations` + `db.Exec`). `testpg.Migrate`-based tests (65 real invocations across 36 test files, verified with `rg`; the 66th grep hit is the comment mention at `migrations_test.go:52`) are unaffected because the sweep test is a separate package and no shared harness changes.
- **No production behavior change.** Assertions only read `pg_indexes` / `pg_constraint` / `information_schema.columns`. Nothing in `internal/`, `cmd/`, config, or the wire surface changes; `git diff --stat` after implementation shows `migrations/migrations_test.go` only.
- **Down-leg absence is name-scoped.** After the descending sweep the pinned objects are gone, but `uq_audit_ledger_event` / `uq_event_receipt_source_event` (re-added by 000050's down) may transiently exist mid-sweep and are dropped with their tables later in the descent; the absence pin asserts only the seven pinned names, never "no constraints/indexes at all" — so a legitimate rollback state can never false-red.
- **Rendered strings are the contract, pinned to PG 16** (testpg's `postgres:16-alpine`, `testpg.go:25`). The pins use the exact catalog rendering — schema qualification, `USING btree`, parentheses, `ASC` elision. A testcontainers image bump to a major version that renders differently surfaces as a red sweep, which is a conscious re-verification step (§6), not a silent acceptance.
- **Gate placement:** the sweep is `//go:build integration` — it runs only under `make test-integration` / `go test -tags=integration`, never `make test`. `check-audit-swallow` and `check-anchored-event-ids` roots exclude `migrations/` (Makefile:69, :77), so the change is green by construction for those. `check-integration-compile` (`go vet -tags integration`) covers the new helpers — stdlib-only code, no vet concerns.
- **lint-strict applies to test files:** helpers stay under funlen 50 / cyclop 10; no globals other than the three read-only data tables.

## 3. Design decisions and rejected alternatives

| # | Decision | Rationale / rejected alternative |
|---|---|---|
| D1 | **Assert inside the existing `TestMigrationsUpAndDown`, at the two existing checkpoints** (spec decision, preserved) | A separate test would re-run the full up/down chain (double container time, duplicated migration logic); the existing sweep already owns both checkpoints and the FK-safe descending order. Rejected: new test function, new DB, new file |
| D2 | **Verbatim rendered-string comparison** (`indexdef`/`pg_get_constraintdef` equality) | Rejected: structural parsing (pg_attribute column lists, opclass lookups) — it would re-implement Postgres's rendering logic and still need an oracle for opclasses/directions; verbatim equality catches column reorder, dropped tail, added prefix, widened index, AND rename in one comparison. The only cost is re-pin on PG major bumps, managed by §6 |
| D3 | **Constraint lookup scoped by table (conrelid join) AND name** | `pg_constraint.conname` is only unique per relation. Rejected: conname-only lookup — a future same-named constraint on a different table would silently satisfy the pin. The join makes the pin precise and documents the table binding in the data table |
| D4 | **Down-leg pins the seven names absent; never a global "no constraints" assertion** | 000050's down legitimately re-adds `uq_audit_ledger_event`/`uq_event_receipt_source_event`; a global assertion would false-red on the documented rollback state (spec §5) |
| D5 | **Rejected: `SET enable_seqscan` planner probe** (spec decision, preserved) | Query-shape testing belongs to `TestPurgeDeadLetterPublicationsPredicate`-class tests (T-10.1), which already prove the predicates/orders; the sweep pins DDL only. Adding planner probes expands scope into the query-shaping domain sibling tests own |
| D6 | **Rejected: pinning `idx_callback_deliveries_due`/`idx_callback_deliveries_lease` (000036/000038)** | T-10.2's enumeration is exactly four indexes + three constraints + the title's claim columns; the callback due/lease indexes are claimed by `FetchDueDeliveries` but are out of this direction's enumerated scope. The claim-column pins (000038 columns) cover the columns those indexes depend on. Note for a future extension (§6), not a gap |
| D7 | **`is_nullable` compared as a boolean expression** (`is_nullable = 'NO'`) in SQL | information_schema renders `YES`/`NO` text; a Go-side string compare would duplicate the mapping. Rejected: raw text scan |
| D8 | **Data tables carry the expected strings once, at package level** | One source of truth for both the up-leg and down-leg (same names reused); the down-leg ignores the `def` fields by design |

## 4. Failure modes

| # | Failure | Detection (today → with pin) | Pin |
|---|---|---|---|
| FM1 | An applied migration (000045/000050/000059) is edited: column reorder, dropped tail column, `ASC` dropped, index widened | Today: sweep green (table exists). With pin: up-leg `indexdef` mismatch reddens with got/want rendered forms | REQ-1.1 |
| FM2 | `uq_audit_ledger_tenant_event` renamed (or replaced by an equivalent UNIQUE under a new name) by a future migration | Today: sweep green; `audit_ledger.go:218`'s constraint-name match silently stops firing — the concurrent-append loser surfaces a raw 23505 instead of `ErrAuditDuplicate` (the C1 duplicate contract breaks) | REQ-1.2 (name precision, A3) |
| FM3 | `uq_audit_publication_fact` or `uq_event_receipt_tenant_source_event` renamed/dropped/weakened | Today: sweep green. With pin: constraintDef not-ok or definition mismatch reddens | REQ-1.2 |
| FM4 | A new migration's down misses a `DROP INDEX`/`DROP CONSTRAINT` for a pinned object | Today: sweep green (down leg only checks table absence). With pin: down-leg `assertB2ShapesAbsent` reddens | REQ-1.4 |
| FM5 | Claim columns drift (lease_owner becomes nullable, default dropped, lease_until gets a default) | Today: sweep green; lease fencing/purge predicate (`lease_owner = ''`, `lease_until < now()`) semantics drift silently. With pin: column-shape mismatch reddens | REQ-1.3 |
| FM6 | Runtime query drifts from DDL (purge cutoff stops being an index range bound; claim predicate unindexed) | DDL pins alone cannot prove planner behavior — jointly caught by REQ-1 + the four unmodified T-10.1 integration tests (REQ-2), which exercise claim/purge behavior against the same up-file set | REQ-1 + REQ-2 |
| FM7 | A same-named index is created on a different table | `pg_indexes.indexname` is schema-unique — the name resolves to the new object; its indexdef differs → up-leg reddens (correct: the pinned name now names a different shape) | REQ-1.1 |
| FM8 | A same-named constraint is created on a different table | Blocked by design: the conrelid join (D3) means the pin looks up the pinned table only; a foreign constraint can neither satisfy nor fool it | REQ-1.2, D3 |
| FM9 | Testcontainers image bumped to a PG major whose rendering differs | Red sweep on unrelated changes forces a conscious re-pin + rendering-diff review (§6) — a feature, not a false positive | §6 |
| FM10 | Sweep mis-fires on legitimate rollback state (legacy `(source, event_id)` constraints reappearing after 000050 down) | Absence pins are name-scoped (D4); legacy names are never asserted | REQ-1.4, §2 |

False-green analysis (what could still slip): an object that exists with the pinned name and identical rendered definition but different *semantics* — e.g. a UNIQUE constraint on the same three columns with a different index method (UNIQUE always creates btree) or a partial/dedicated index replacing it. `pg_get_constraintdef` renders the full column list, and a UNIQUE constraint cannot be partial — residual risk is negligible and the T-10.1 behavioral tests cover semantics.

## 5. Migration steps (change rollout)

This is a **test-only change to an integration test file**; there are no data or schema migrations. Rollout steps:

1. **Implement** the surface from §1 in `migrations/migrations_test.go` (data tables, three lookup helpers, three up-leg assert helpers, one down-leg helper, two call sites, `errors` import). Do not touch any `migrations/*.sql`, `internal/`, `cmd/`, or the Makefile.
2. **Re-confirm the pinned strings on the implementer's container** (one run): apply the up chain in a scratch `postgres:16-alpine` container, query the four indexdefs / three constraintdefs / four column shapes, and diff against §0.2. If the testcontainers image was bumped, re-pin with the new rendering and record the change in the commit message (§6).
3. **Run the sweep alone** (Docker required): `go test -tags=integration ./migrations/ -run TestMigrationsUpAndDown -count=1 -v` — must pass; the log should show all seven objects' shapes and the absence leg quiet.
4. **Run the four T-10.1 tests unmodified** (REQ-2): the two repository runs and the app run from §7.
5. **Run the full integration suite**: `make test-integration`.
6. **Run the static gates**: `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-integration-compile && make build` (spec §7 done gate).
7. **Prove the pin bites (mutation probe, one-time, not committed)**: in a scratch container, after the up chain, apply one perturbation — e.g. `ALTER TABLE audit_ledger RENAME CONSTRAINT uq_audit_ledger_tenant_event TO uq_audit_ledger_tenant_event_old` — and confirm the corresponding assertion fails red (FM2's exact shape); repeat for one index (`DROP INDEX idx_audit_publications_purge` → FM4-adjacent absence on up-leg) and one column (`ALTER TABLE audit_publications ALTER COLUMN lease_owner DROP NOT NULL`). Restore and re-run step 3 green. This is the acceptance evidence that the pins are not vacuous.
8. **Verify the diff surface**: `git diff --stat` shows `migrations/migrations_test.go` only (REQ-3.1). Commit as a test-only change.

## 6. Maintenance contract

- **Pinned objects are a coupled contract with runtime code.** Any future migration that renames/drops/replaces `uq_audit_ledger_tenant_event` must, in the *same* commit: update `internal/repository/audit_ledger.go:218`'s constraint-name match, update `b2ConstraintShapes`, and re-run REQ-2's four tests. The sweep exists to force exactly this consciousness.
- **New B2 DDL enters the pin when the runtime depends on it.** If a later migration adds an index/constraint a B2 query's predicate/order relies on (the same family as 000045/000050/000059), extend the data tables — including the currently unpinned `idx_callback_deliveries_due`/`idx_callback_deliveries_lease` (D6), which `FetchDueDeliveries` claims but T-10.2 did not enumerate.
- **PG major bumps require a rendering re-verification.** `testpg.Start` pins `postgres:16-alpine`; an upgrade to 17+/18+ must diff the rendered catalog output (§0.2) and consciously re-pin or investigate before merging, per FM9. A red sweep on a pure image bump is a re-pin signal, not a migration defect.
- **Sibling direction coupling.** Direction 1 (fail-closed schema-version gate) owns `MinB2SurfaceVersion = 59`; if a future migration extends the pinned DDL family, the version constant and this pin must be bumped together. Direction 2 (cmd/migrate CLI coverage) is independent.
- **Down-leg coverage is only as good as the sweep's descending order** (000059 → 000001). `discoverMigrations` already fails on unpaired up/down; the new absence leg adds the "down fails to drop" detector (FM4) on top of it.

## 7. Testable acceptance mapping

| Supplied acceptance (direction, preserved) | Requirements criteria | Testable command / evidence |
|---|---|---|
| **T-10.2** — after full up, pg_indexes/pg_constraint assertions for the four indexes, `uq_audit_publication_fact`, and the audit_ledger dedup UNIQUE; all absent after the descending down sweep | REQ-1.1–REQ-1.4 | `go test -tags=integration ./migrations/ -run TestMigrationsUpAndDown -count=1 -v` — passes; log shows all four indexdefs, three constraint defs (name+def), four claim-column shapes after up, silence after down. Mutation probe (§5.7) demonstrably reddens each family (rename/drop/weaken), proving the pins are non-vacuous |
| **T-10.1** — `TestConcurrentClaimPublicationPartition`, `TestExpiredLeasePublicationReclaimedByAnotherWorker`, `TestPurgeDeadLetterPublicationsPredicate`, `TestFirstEventGovernanceE2E` keep passing against the same migration set | REQ-2.1–REQ-2.3 | `go test -tags=integration ./internal/repository/ -run 'TestConcurrentClaimPublicationPartition\|TestExpiredLeasePublicationReclaimedByAnotherWorker\|TestPurgeDeadLetterPublicationsPredicate' -count=1`; `go test -tags=integration ./internal/app/ -run TestFirstEventGovernanceE2E -count=1`; both pass, unmodified, via `testpg.Migrate` (same full up-file set). `git diff --stat` shows zero `internal/` deltas |
| **T-4** — pure test change; check-audit-swallow/check-anchored-event-ids stay green; zero production deltas | REQ-3.1–REQ-3.4 | `git diff --stat` = `migrations/migrations_test.go` only. `make check-audit-swallow` and `make check-anchored-event-ids` green (`migrations/` outside both roots, Makefile:69/:77 — green by construction). `make test`, `make lint`, `make lint-strict`, `make check-integration-compile`, `make build` green |

**Done gate** (spec §7, unchanged): `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-integration-compile && make build` plus `make test-integration` (the sweep and the four T-10.1 tests are integration-tagged; `make test-integration` is the only heavyweight gate). `make check-audit-swallow`/`check-anchored-event-ids` are green by construction (§2).

## 8. Verification delta vs. the requirements spec

| Spec statement | Delta found this pass |
|---|---|
| REQ-1.1 "Exact rendered forms … pinned from the live container output during implementation" | **Resolved**: §0.2 captures the live output (postgres:16-alpine, server 16.14) for all eleven shapes; the predicted forms are byte-identical. Implementer step 2 is a re-confirmation, not a discovery |
| REQ-1.1/1.2 expected strings | ✅ exact — no re-pin drift (spec's normalized predictions == live rendering) |
| REQ-1.3 "audit_publications.lease_owner: is_nullable = NO, column_default = ''::text … same two shapes on callback_deliveries" | ✅ exact — live container confirms both tables render identically |
| REQ-1.4 name-scoped down-leg; legacy `uq_audit_ledger_event`/`uq_event_receipt_source_event` legitimately reappear | ✅ exact — 000050 down verified verbatim |
| "164 → ~260 lines" | Cosmetic: data tables carrying verbatim strings bring the estimate to ~300; test files are exempt from `check-file-sizes` |
| REQ-2/REQ-3 commands and gate roots | ✅ exact — verified against Makefile:15, :69, :77 and the four test locations |

All requirements-spec claims verified; the only adjustments are the resolved pin provenance (§0.2) and the line-count estimate. The design adds no requirement, removes none, and preserves the three supplied acceptance checks verbatim in §7.
