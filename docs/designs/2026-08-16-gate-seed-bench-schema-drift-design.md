# Design — R5: Gate `seed-bench.sql` Schema Drift (INSERT/TRUNCATE Tables + INSERT Column Lists Resolve in Migrations)

Status: **Proposed** (implementation-ready)
Date: 2026-08-16
Module: `scripts/load/db` (gate: `scripts/check-load-scenarios.sh`)
Spec: requirements R5 (R5.1–R5.5), direction 1 of `docs/auto/analyses/scripts-load-db-6faf2782.json`
Baseline verified at: HEAD on 2026-08-16; every claim re-verified against the live tree (file:line given). The evidence was treated as untrusted; all load-bearing claims were independently re-derived with a working prototype in `/tmp` (no repository files touched by verification).

---

## 1. Verification verdict (untrusted evidence → tree)

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | `scripts/load/db/` holds exactly two files: `seed-bench.sql`, `partition-index-bench.sql` | **Confirmed** | `ls scripts/load/db` → the two files only. |
| E2 | Seed INSERTs into 8 hot tables + marker; TRUNCATE lists 8 hot tables + `message_reactions`; marker self-created | **Confirmed** | `seed-bench.sql:89-90` (TRUNCATE spans two lines, ends `RESTART IDENTITY;`), `:95` (accounts), `:111` (audit_ledger, multiline 24-column list `:111-120`), `:152` (notifications), `:167` (outbox_events), `:183` (audit_publications), `:200` (messages), `:217` (files), `:238` (account_snapshots), `:259` (marker CREATE), `:264` (marker INSERT + `ON CONFLICT … DO UPDATE` at `:275`). Re-derived independently: `INSERT INTO` yields exactly 9 relations (8 + marker); TRUNCATE-list yields 8 including `message_reactions`. |
| E3 | Hazard self-documented in header | **Confirmed** | `seed-bench.sql:37-42` — "UPDATE THEM when those migrations change; the seed fails against a bench DB whose schema has drifted". |
| E4 | R3 covers only `partition-index-bench.sql`; allowlist precedent | **Confirmed** (line numbers drifted, see D1) | `BENCH_REL` constant present; `rule_r3()` at `check-load-scenarios.sh:180-208`; `pg_stat_user_indexes` allowlist in header `:41-42` and in rule body `:199`. |
| E5 | R4 excludes SQL files by design; D10 says column-copy is "verified by actually running the seed … not part of the gate" | **Confirmed** | `docs/designs/2026-08-14-load-tooling-single-source-truth-design.md:185-191` (D10; the exact "not part of the gate" sentence), `:255` (FM-16 "out of gate scope by spec"); scope diagram `:63` annotates the R4 row with `(scripts/load/db/seed-bench.sql)`. |
| E6 | Every seed table/column resolves today (zero drift); `accounts.tenant_id` resolves only via 000048; 93 column identifiers | **Confirmed**, re-derived independently | Prototype run against the real tree: 0 violations; all 8 INSERT column lists resolve (7+24+8+7+10+10+13+14 = 93). `accounts.tenant_id` is absent from `000031` CREATE and present only via `000048:1` `ALTER TABLE accounts ADD COLUMN tenant_id TEXT;` — a prototype that skips the ALTER path reddens `accounts.tenant_id` immediately. |
| E7 | ALTER-path precedent | **Confirmed** | `migrations/000048_scope_accounts_by_tenant.up.sql:1` `ALTER TABLE accounts ADD COLUMN tenant_id TEXT;`. Also found: `000048:2` adds `tenant_id` to `account_identities` — the space-anchored table match (`ALTER TABLE accounts[[:space:]]+ADD COLUMN`) must NOT bleed into it (it does not, verified). |
| E8 | `message_reactions` has a real migration | **Confirmed** | `migrations/000013_message_reactions.up.sql:1` `CREATE TABLE IF NOT EXISTS message_reactions (`. *Delta D2: the seed's own header comment (`seed-bench.sql:86`) says `message_reactions` references messages via "migration 000003"; 000003 is `comments`. The evidence is right, the seed comment is stale.* |
| E9 | Migrations for the other seed tables | **Confirmed** | `000001_create_files` / `000002_create_messages` / `000008_create_outbox_events` / `000009_create_notifications` / `000031_create_account_core` / `000032_create_account_snapshots` / `000034_create_audit_ledger` / `000045_audit_publications` — all present (also `000013_message_reactions`). |
| E10 | Fixture infrastructure supports a new rule | **Confirmed** | fixture-map `case` at `check-load-scenarios.sh:229-233` (four classes), both rule-enumeration loops at `:247` and `:260`, real-tree scan `:274-279`, success echo `:288`; 5 positive fixtures + `negative/clean` at `scripts/testdata/load-scenarios/`; fixture migrations `bench_items`/`bench_users`. None of the existing fixtures carries a `seed-bench.sql` (verified via `find`), which is what forces R5's silent-by-design missing-file path. |
| E11 | Run contract: seed executed by a real DSN target; gate stays CI-safe | **Confirmed** | `Makefile:247` `load-test-db-seed:` runs `psql "$(PG_DSN_BENCH)" -v ON_ERROR_STOP=1 -f scripts/load/db/seed-bench.sql`; `Makefile:238` marker probe (`SELECT schema_version FROM aero_load_bench_seed`); `Makefile:51-52` `check-load-scenarios:` → `bash scripts/check-load-scenarios.sh`. `migrations/` contains zero occurrences of `aero_load_bench_seed`, confirming the marker is self-creating only. |
| E12 | README gate list currently R1–R4 | **Confirmed** | `scripts/load/README.md:130` `## Gates`, bullets R1–R4 at `:136-144`. |
| — | Baseline: `make check-load-scenarios` green, R1–R4 | **Confirmed** | Ran `bash scripts/check-load-scenarios.sh` → `✓ Load-scenario check passed (R1..R4)`; exit 0. |
| — | New fixture migrations do not trip other done-gate checks | **Confirmed** | `check-migration-set.sh` scans only its own `scripts/testdata/migration-set/` fixture root (`FIXTURES="$SELF/testdata/migration-set"` at `:55`); no other gate (`architecture`, `audit-*`, `composition-root`, `proto-sync`, `anchored-event-ids`) references `load-scenarios/` trees (grep-verified). |

### Deltas and corpus gaps (verified, must be carried into the design)

**D1 — stale line citations in the evidence for `check-load-scenarios.sh`.** Actual on HEAD: `BENCH_REL` at `:73` (evidence said `:25`), `R4_FILES` at `:78-84` (evidence `:29-37`), R1's awk at `:104-113` (evidence `:159`), `rule_r3` spans `:180-208` (evidence `:179-206`), fixture-map case at `:229-233` (evidence `:230-233`). All *structural* claims (constants, rule shapes, allowlist location, loops, real-tree scan, echo) are confirmed; only quoted line numbers shifted (header/constant block grew after the citations were authored).

**D2 — seed header doc nit (out of scope, flagged).** `seed-bench.sql:86` cites "migration 000003" for `message_reactions`; the migration is `000013`. R5 does not touch seed content (scope limit), so this is a note for a future non-gated cleanup, not part of this design.

**D3 — real hazard the evidence missed: `ADD COLUMN IF NOT EXISTS` in live migrations.** `migrations/000016_fts_indexes.up.sql:8,18` (`messages`, `files` `search_vector`), `000028_add_file_status.up.sql:7` (`files` `status`), `000043_add_audit_request_id.up.sql:1` (`audit_ledger` `request_id`) all use `ALTER TABLE <t> ADD COLUMN IF NOT EXISTS <col>`. The naive ALTER grep from the evidence's §7 sketch (`grep -oE "ALTER TABLE ${t} ADD COLUMN <ident>"`) captures the garbage token `IF` into the known-column union (verified: `messages`/`files`/`audit_ledger` each gain `IF`). Today that is a permissive extra token, but it is correctness-relevant: if a future seed column is added via the same `IF NOT EXISTS` shape, its real name is never captured and a seed reference to it false-reds. The design therefore mandates `IF NOT EXISTS` neutralization before the ALTER grep (mirroring R3's `FOR UPDATE` neutralization precedent).

**D4 — two implementation bugs in the evidence's §7 extraction sketch, both reproduced.**
1. `local root="$1" seed="$root/$SEED_REL"` on one line hits a bash 5.2 `set -u` bug: `local a="$1" b="$a"` mis-evaluates the second RHS (`seed=/x` with empty `root`) and abort-fails under `set -u` (`unbound variable`). Fix: separate `local` statements (`local root="$1"` then `local seed=…`), which is exactly `rule_r3`'s house style (`check-load-scenarios.sh:183-184`).
2. A greedy `sub(/^.*\(/, …)` opener strip eats the inline `VALUES (…)` clause — which breaks all three of the evidence's own fixture seeds (R5.4 clean, `seed_table_missing`, `seed_col_missing` all use `INSERT INTO t (cols) VALUES (…)` with parens in VALUES). Fix: strip the literal `INSERT INTO ` prefix, then the anchored `^<table>[[:space:]]*\(`, then cut the accumulated buffer at the first `)`.

All four deltas are empirically demonstrated in §8's reproduction appendix and folded into the canonical rule text in §4.

---

## 2. Problem statement (confirmed)

`scripts/load/db/seed-bench.sql` is the bench cell's schema contract, and it is unscanned by the done-gate:

- It `INSERT`s into 8 hot tables plus the self-created `aero_load_bench_seed` marker, and `TRUNCATE`s those 8 plus the legacy compat table `message_reactions`.
- It is the sole input to `make load-test-db-seed` (`Makefile:247`) and the marker it creates is the substantive guard against running `EXPLAIN ANALYZE` on the app DB (`Makefile:238` probe; README guard).
- `check-load-scenarios` R3 scans only `partition-index-bench.sql` relations; R4 deliberately excludes SQL files (design doc D10 + FM-16: the column-mirror contract is verified "by actually running the seed against a bench DB", i.e. at manual bench time).

Consequence (confirmed hazard): a migration that renames a table/column, adds a `NOT NULL` column without a default, or drops `message_reactions` breaks `make load-test-db-seed` only at manual bench time (psql + real bench DB); the done-gate stays green. The seed header itself documents this at `:37-42`. R5 turns the done-gate green/red state into a tripwire against that drift, ahead of any psql run.

Three extraction hazards, verified as load-bearing for the rule design:

1. **`message_reactions` is TRUNCATE-only** — it appears in the seed only at `:90`, never after `FROM|JOIN|INTO|UPDATE|TABLE`; a naive R3-style scan misses it. The relation set must come from `INSERT INTO` **and** the TRUNCATE list.
2. **`aero_load_bench_seed` is a deliberate false positive** — the marker is self-created inside the seed (`:259`) and has no migration (design doc D10 name-collision check; `migrations/` has zero occurrences). It must be allowlisted with a justification, mirroring the `pg_stat_user_indexes` allowlist (`check-load-scenarios.sh:41-42`, `:199`).
3. **A generic keyword scan over the seed captures garbage** — verified: `generate_series` (8 occurrences, from `FROM generate_series(1, :N)`), `IF` (from `CREATE TABLE IF NOT EXISTS aero_load_bench_seed`), and `SET` (from `ON CONFLICT … DO UPDATE SET`). Targeted INSERT/TRUNCATE extraction avoids all three by construction.

---

## 3. Gate API changes

The gate's contract surface is the script header (the canonical rule definition), the rule functions, the fixture map, the fixtures, and the README. There is **no network API, no protobuf, no migration DDL** in this change; "API" below means the load-tooling gate's interface, which is itself a reviewed contract (`AGENTS.md` done-gate chain; `scripts/load/README.md`).

| Surface | Change | Nature |
|---|---|---|
| `scripts/check-load-scenarios.sh` header (`:1-60`) | R5 rules paragraph: extraction semantics (INSERT-INTO ∪ TRUNCATE-list), the `aero_load_bench_seed` allowlist **with justification**, the mandatory `IF NOT EXISTS` neutralization, the three garbage captures by contrast; fixture-map rows `seed_table_missing|seed_col_missing` | Additive, in-house-style prose (header text is canonical) |
| `SEED_REL=scripts/load/db/seed-bench.sql` constant (constants block `:73-84`) | New constant next to `BENCH_REL` / `R4_FILES` | Additive |
| `rule_r5()` (new function after `rule_r4()`) | Root-parametrized, mirrors `rule_r3` shape; silent-by-design on missing seed | Additive |
| Fixture-map `case` (`:229-233`) | `seed_table_missing|seed_col_missing) want=rule_r5 ;;` | Additive |
| Both rule-enumeration loops (`:247`, `:260`) | `rule_r1 rule_r2 rule_r3 rule_r4 rule_r5` | Additive (positive cross-checks + negative sweep cover R5) |
| Real-tree scan (`:274-279`) | `rule_r5 "$ROOT"` | Additive |
| Success echo (`:288`) | `; R5: seed tables/columns resolve in migrations` | Additive |
| `scripts/testdata/load-scenarios/positive/{seed_table_missing,seed_col_missing}/` | Two new positive fixture trees (spec in §5) | Additive |
| `scripts/testdata/load-scenarios/negative/clean/` | Gains `migrations/000003_add_bench_items_tenant_tag.up.sql` + `scripts/load/db/seed-bench.sql` (spec in §5) | Additive to existing clean tree |
| `scripts/load/README.md` Gates section (`:130-144`) | Intro copy "what it guards (R1–R5)"; new R5 bullet | Additive |

**No changes to:** R1–R4 rule bodies, `Makefile`, `migrations/`, `AGENTS.md`, the seed SQL, `partition-index-bench.sql`, any existing fixture asset, or other gates.

---

## 4. `rule_r5` design (canonical rule text for the script header)

```bash
SEED_REL=scripts/load/db/seed-bench.sql

#   R5 — every relation and INSERT column list in
#        scripts/load/db/seed-bench.sql resolves in migrations. Extraction:
#        drop comment lines (--) and psql meta-command lines (\), then take
#        (a) identifiers after INSERT INTO, and (b) the comma/space-separated
#        identifier run of the TRUNCATE statement across its continuation
#        line(s) up to the terminating ';', with the trailer keywords
#        RESTART/IDENTITY/CASCADE stripped (a comma-split without the strip
#        yields the garbage token 'account_snapshots RESTART IDENTITY').
#        Union, sort -u. Relation check: `CREATE TABLE.*<rel>` in some
#        migrations/*.up.sql. Column check (INSERT relations only; skipped
#        for TRUNCATE-only relations like message_reactions): capture the
#        parenthesized INSERT column list (opens on the INSERT line, closes
#        at the first ')'; VALUES (...) on the same line is never captured),
#        and resolve every identifier against the table's CREATE TABLE body
#        leading identifiers (types/CONSTRAINT lines are harmless permissive
#        tokens in this direction) UNION the table-scoped ALTER path
#        (`ALTER TABLE <t> ADD COLUMN <col>`; ADD COLUMN is matched only
#        after neutralizing the `IF NOT EXISTS` idiom — a naive capture
#        yields the garbage token IF on 000016/000028/000043-style lines),
#        both over migrations/*.up.sql. Allowlist (justified):
#          aero_load_bench_seed — the seed self-creates this version/row-count
#            marker via CREATE TABLE IF NOT EXISTS aero_load_bench_seed
#            (seed-bench.sql:259); there is deliberately no migration for it
#            (name collision-checked; aero_ prefix keeps it out of the app
#            namespace). A NEW PHYSICAL RELATION added to the seed requires an
#            allowlist update here, with a justification comment.
#        Missing seed file is silent-by-design (liveness is R4's concern; the
#        existing positive fixtures carry no seed file).
```

Rule body (root-parametrized, `rule_r3` house style — separate `local` statements; do not combine `local root="$1" seed="$root/..."` on one line, bash 5.2 `set -u` mis-evaluates the second RHS):

```bash
rule_r5() {
  local root="$1"
  local seed="$root/$SEED_REL" rels
  [ -f "$seed" ] || return 0   # silent-by-design (AC-4); liveness is R4's concern
  local clean
  clean=$(grep -vE '^[[:space:]]*--' "$seed" | grep -vE '^[[:space:]]*\\')

  # relations: INSERT INTO ∪ TRUNCATE list (trailer-stripped), union
  rels=$( ( printf '%s\n' "$clean" | grep -oE '\bINSERT INTO [A-Za-z_][A-Za-z0-9_]*' | awk '{print $3}'; \
            printf '%s\n' "$clean" | awk '
              /^[[:space:]]*TRUNCATE/ { in_t=1 }
              in_t { line=$0; if (index(line,";")) { in_t=0; sub(/;.*/,"",line) }
                     n=split(line,a,/[,[:space:]]+/); for (i=1;i<=n;i++)
                       if (a[i] !~ /^(TRUNCATE|RESTART|IDENTITY|CASCADE)$/ && a[i] != "") print a[i] }' ) | sort -u )

  while IFS= read -r t; do
    [ -n "$t" ] || continue
    case "$t" in aero_load_bench_seed) continue ;; esac   # allowlist — see header justification
    if ! grep -q "CREATE TABLE.*$t" "$root"/migrations/*.up.sql 2>/dev/null; then
      echo "ERROR: R5 seed relation '$t' ($seed) has no CREATE TABLE in migrations/"
      violations=$((violations + 1))
      continue   # skip column check for a table that is already red (avoids cascading noise)
    fi
  done <<< "$rels"

  # columns: per INSERT relation (marker allowlisted above for columns too —
  # it is self-defining), known set = CREATE TABLE body ids ∪ ALTER ADD COLUMN ids
  local t known c
  for t in $(printf '%s\n' "$clean" | grep -oE '\bINSERT INTO [A-Za-z_][A-Za-z0-9_]*' \
             | awk '{print $3}' | grep -v aero_load_bench_seed | sort -u); do
    seedcols=$(printf '%s\n' "$clean" | awk -v t="$t" '
      BEGIN { state=0; buf="" }
      {
        line=$0
        if (state==0) {
          if (line !~ "^INSERT INTO " t "[[:space:]]*\\(") next
          sub(/^INSERT INTO /, "", line)                     # literal prefix (never greedy)
          sub(/^[A-Za-z_][A-Za-z0-9_]*[[:space:]]*\(/, "", line)   # table name + opening paren
          state=1
        }
        if (line ~ /\)/) { sub(/\).*/, "", line); state=0 } # cut at the FIRST closer
        buf = buf " " line
      }
      END { gsub(/[^A-Za-z0-9_]/, " ", buf); n=split(buf, a, " "); for (i=1;i<=n;i++) if (a[i] != "") print a[i] }' | sort -u)
    createcols=$(awk -v t="$t" '
      $0 ~ "CREATE TABLE (IF NOT EXISTS )?" t "[[:space:]]*\\(" { ind=1; next }
      ind {
        opens=gsub(/\(/, "("); closes=gsub(/\)/, ")"); ind += opens - closes
        line=$0; sub(/[[:space:]]*$/, "", line)
        if (line ~ /^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*/) {
          match(line, /[A-Za-z_][A-Za-z0-9_]*/); print substr(line, RSTART, RLENGTH)
        }
        if (ind <= 0) exit
      }' "$root"/migrations/*.up.sql | grep -vE '^(CONSTRAINT|PRIMARY|UNIQUE|FOREIGN|CHECK|REFERENCES|CREATE|\);|\)|;)' | sort -u)
    altercols=$(grep -vE '^[[:space:]]*--' "$root"/migrations/*.up.sql 2>/dev/null \
      | sed 's/ADD COLUMN IF NOT EXISTS/ADD COLUMN/g' \
      | grep -hoE "ALTER TABLE ${t}[[:space:]]+ADD COLUMN[[:space:]]+[A-Za-z_][A-Za-z0-9_]*" | awk '{print $6}' | sort -u)
    known=$( { printf '%s\n' "$createcols"; printf '%s\n' "$altercols"; } | sort -u )
    for c in $seedcols; do
      if ! printf '%s\n' "$known" | grep -qx "$c"; then
        echo "ERROR: R5 seed column '$c' for seed table '$t' ($seed) resolves nowhere in migrations/ (CREATE TABLE body or ALTER TABLE ADD COLUMN)"
        violations=$((violations + 1))
      fi
    done
  done
}
```

Design decisions (each required by a verified failure mode — see §7):

1. **Separate `local` statements** — avoids the bash 5.2 `set -u` multi-assignment mis-evaluation (D4.1); `rule_r3`-consistent.
2. **Literal, anchored opener strip, first-`)` cut** — inline `VALUES (…)` (the specified fixture shapes) must never leak into the capture (D4.2); `INSERT INTO aero_load_bench_seed (…) VALUES (… jsonb_build_object(…))` is excluded by the marker skip before parsing.
3. **`IF NOT EXISTS` neutralization on the ALTER capture** — otherwise `000016`/`000028`/`000043` inject garbage `IF` and a future `ADD COLUMN IF NOT EXISTS <col>` seed column false-reds (D3).
4. **Space-anchored ALTER table match** — `ALTER TABLE accounts ADD COLUMN` must not bleed into `account_identities`/`account_profiles` (verified: `grep -oE` with `[[:space:]]+` keeps them separate; `000048:2` `account_identities` is not captured for `accounts`).
5. **`continue` after a relation error** — a table missing its CREATE TABLE does not cascade duplicate column errors (bench_ghosts case).
6. **Silent on missing seed** — the five existing positive fixtures carry no seed file; R5's anti-vacuity is enforced by the two new positive fixtures, not by file liveness (R4's concern).

---

## 5. Fixture design

Base tree: copies of `negative/clean`'s R1–R4 assets (proto, scenarios.yaml, benchmark SQL, k6 files, sink, run-ghz.sh) and its two fixture migrations (`000001_create_bench_items.up.sql`, `000002_create_bench_users.up.sql`).

**`positive/seed_table_missing/`** — `scripts/load/db/seed-bench.sql`:

```sql
-- Fixture seed (R5 positive: seed_table_missing): INSERT references a table
-- with no CREATE TABLE in the fixture migrations.
TRUNCATE bench_items, bench_users RESTART IDENTITY;
INSERT INTO bench_items (id, owner) VALUES ('a', 'x') ON CONFLICT DO NOTHING;
INSERT INTO bench_users (id) VALUES ('b');
INSERT INTO bench_ghosts (id) VALUES ('c');
```

Fires the relation violation for `bench_ghosts` only (verified with the prototype: exactly the `bench_ghosts` relation error; columns for `bench_items`/`bench_users` resolve; `bench_ghosts`'s column check is skipped by decision 5).

**`positive/seed_col_missing/`** — fixture migration models a column rename; seed keeps the pre-rename name:

```sql
-- migrations/000001_create_bench_items.up.sql
-- Fixture bench_items (published_at renamed to delivered_at); the fixture
-- seed below still references published_at, so R5 reddens.
CREATE TABLE IF NOT EXISTS bench_items (
    id uuid PRIMARY KEY,
    owner text NOT NULL,
    delivered_at timestamptz
);
-- migrations/000002_create_bench_users.up.sql (unchanged): bench_users (id).
-- scripts/load/db/seed-bench.sql
-- Fixture seed (R5 positive: seed_col_missing): INSERT column list still
-- carries published_at, renamed to delivered_at in the fixture migration.
TRUNCATE bench_items, bench_users RESTART IDENTITY;
INSERT INTO bench_items (id, owner, published_at) VALUES ('a', 'x', now()) ON CONFLICT DO NOTHING;
INSERT INTO bench_users (id) VALUES ('b');
```

Fires exactly one R5 violation, `published_at` (verified: violations = 1, `published_at`).

**`negative/clean/` additions** (all existing clean assets unchanged):

```sql
-- migrations/000003_add_bench_items_tenant_tag.up.sql
ALTER TABLE bench_items ADD COLUMN tenant_tag TEXT;
-- scripts/load/db/seed-bench.sql
-- Fixture seed (clean): R5 exercises INSERT + TRUNCATE extraction and column
-- resolution. bench_items.tenant_tag resolves ONLY via ALTER TABLE ADD COLUMN
-- (000003), proving the ALTER path (mirrors accounts.tenant_id via 000048 in
-- the real tree). The FROM generate_series(...) line pins the INSERT/TRUNCATE-
-- only extraction: a generic R3-style keyword scan would capture the function
-- as a relation and redden this clean fixture. The bench_users INSERT column
-- list spans two lines, pinning the multiline capture (audit_ledger shape).
TRUNCATE bench_items, bench_users RESTART IDENTITY;
INSERT INTO bench_items (id, owner, tenant_tag) VALUES ('a', 'x', 't-1') ON CONFLICT DO NOTHING;
INSERT INTO bench_users (
    id)
SELECT '00000000-0000-4000-8000-000000000002'::uuid FROM generate_series(1, 1);
```

Clean-tree semantics (all three verified with the prototype): skip the ALTER path → `tenant_tag` reddens clean; regress to a generic R3-style keyword scan → `generate_series` reddens clean; single-line-only column capture → the two-line `bench_users` list breaks; greedy opener strip → inline `VALUES (…)` reddens clean; multi-assignment `local` → the rule aborts under `set -u` and the whole gate reddens. The fixture is therefore self-verifying on every run.

Fixture-map rows: `seed_table_missing|seed_col_missing) want=rule_r5 ;;`. Both loops gain `rule_r5`, so `dead_rpc`, `runner_divergence`, `malformed_yaml`, `bench_table_missing`, `missing_k6` (no seed file → R5 silent, verified) and the clean tree (R5 clean, verified) all behave exactly as before.

---

## 6. Compatibility constraints

1. **Isolation class preserved:** the rule uses only `grep`, `sed`, `awk`, `sort`, `tr` (all already permitted; awk precedent `check-load-scenarios.sh:104-113`). No Go, Docker, network, interpreters, or new binaries — CI-safe for the `check-gates` job.
2. **R1–R4 rule bodies untouched**, as are the Makefile, migrations, seed SQL, and `AGENTS.md` (whose gate prose does not enumerate rule numbers and stays accurate).
3. **Existing fixtures unaffected** (AC-4): none carry `seed-bench.sql`, so `rule_r5` reports 0 on each; the augmented loops keep the sweep semantics identical.
4. **Missing seed stays silent by design:** R5 is a drift tripwire over a file that liveness-R4 already budgets for; a real tree that ever loses the seed goes green on R5 (and red on nothing else here) — file liveness is explicitly out of scope (§10) and documented in the README bullet so it is not mistaken for a hole.
5. **Allowlist discipline:** the marker is the *only* allowlisted seed relation. A new physical relation added to the seed requires an allowed-list entry with a justification, exactly like `pg_stat_user_indexes`. No column check applies to allowlisted relations (the marker is self-defining; `message_reactions` is TRUNCATE-only).
6. **Fixture trees are outside other gates:** `check-migration-set.sh` scans only its own `scripts/testdata/migration-set/` root; no other gate references `scripts/testdata/load-scenarios/` (verified). New fixture migrations cannot trip migration-set integrity (`check-migration-set`, `make check-file-sizes`, etc.).
7. **Deterministic, order-independent:** relation/column sets are `sort -u`d; `grep -E '^    call:'`-style ordering assumptions do not extend to R5. Multiline continuation is handled by awk state machines over contiguous statement lines (TRUNCATE `:89-90`; audit_ledger column list `:111-120`).

---

## 7. Failure modes

**Gate-level (red — intended):**
| Failure | Trigger | Detection |
|---|---|---|
| Seed table renamed/dropped, or new hot table without a migration | `grep -q "CREATE TABLE.*<rel>"` fails | `ERROR: R5 seed relation …` before any psql run |
| Seed column renamed/dropped, or `NOT NULL` added without a default (seed can't insert) | INSERT-list id not in CREATE body ∪ ALTER set | `ERROR: R5 seed column …` |
| `message_reactions` dropped from migrations | relation check (TRUNCATE-captured only) | relation error — catches a table a naive scan would miss |
| `ALTER TABLE accounts ADD COLUMN` removed (000048 down-path) | `accounts.tenant_id` unresolvable | column error — the ALTER path is mandatory, not optional polish |

**Rule-implementation failure modes (each empirically reproduced and designed against):**
| Mode | Symptom | Design guard |
|---|---|---|
| Generic R3-style keyword scan over the seed | garbage relations `generate_series`, `IF`, `aero_load_bench_seed`; `message_reactions` missed | INSERT-INTO ∪ TRUNCATE-list extraction only |
| TRUNCATE comma-split without trailer strip | garbage relation `account_snapshots RESTART IDENTITY` reddens the clean tree | `RESTART\|IDENTITY\|CASCADE` exclusion in the awk split |
| Greedy opener strip | inline `VALUES (…)` columns `ONCONFLICTDONOTHING`, `'a','x'` redden the clean fixture | literal `INSERT INTO ` + anchored `^<t>[[:space:]]*\(` strip; first-`)` cut |
| Multi-assignment `local` under `set -u` | `unbound variable` → whole gate reddens (or mis-evaluated seed path) | separate `local` statements (rule_r3 style) |
| Missing `IF NOT EXISTS` neutralization | garbage `IF` in the known set; future `ADD COLUMN IF NOT EXISTS <col>` seed reference false-reds | `sed 's/ADD COLUMN IF NOT EXISTS/ADD COLUMN/g'` before the ALTER grep |
| Unanchored ALTER table match | `accounts` bleeds into `account_identities`/`account_profiles` | `ALTER TABLE ${t}[[:space:]]+ADD COLUMN` space-anchored |
| Vacuity (rule matches nothing) | dedicated positive fixtures never fire | `seed_table_missing`/`seed_col_missing` wired into the fixture map; clean fixture's ALTER+multiline+`generate_series` pins redden the instant the rule regresses |
| Over-broad capture | clean fixture or existing positives redden | negative sweep over R1–R5 must report 0 |

**Runtime bench failure modes now tripwired ahead of time (unchanged by this design, but now detected earlier):** psql `ERROR: column … does not exist` / `relation … does not exist` on `make load-test-db-seed`; silent wrong-shape EXPLAIN plans (drifted column order); accidental app-DSN bench runs losing the substantive marker guard (a `NOT NULL`-without-default migration would break the seed → marker never created → downstream `load-test-db` probe fails safely).

---

## 8. Migration steps (implementation order; each step is CI-safe)

The real tree is verified zero-drift (§1), so landing R5 is a pure tripwire add — green before and green after, with the two new positive fixtures proving non-vacuity.

1. **Script header:** add the R5 rules paragraph (§4 text) and the fixture-map rows to the canonical header (`:1-60`).
2. **Constants + rule:** add `SEED_REL` beside `BENCH_REL` (`:73`); add `rule_r5()` after `rule_r4()` (`:209-218`); wire `rule_r5 "$ROOT"` into the real-tree scan (`:274-279`).
3. **Fixture map + loops:** add `seed_table_missing|seed_col_missing) want=rule_r5 ;;` (`:229-233`); extend both enumeration loops to `rule_r1 rule_r2 rule_r3 rule_r4 rule_r5` (`:247`, `:260`).
4. **Fixtures:** create `positive/seed_table_missing/`, `positive/seed_col_missing/` (base = clean-tree assets, per §5); add the two files to `negative/clean/`.
5. **README:** R5 bullet + intro copy under `## Gates` (`scripts/load/README.md:130-144`).
6. **Success echo:** extend `:288`.
7. **Gate:** `bash scripts/check-load-scenarios.sh` must exit 0 (fixture sweep + real tree), with the R5-augmented success line.
8. **Done gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && make check-migration-set && make check-composition-root && make check-proto-sync && make check-integration-compile && make build`.
9. **Anti-regression proof (one-off, not committed):** temporarily run the rule without the ALTER path against the real tree → `accounts.tenant_id` must redden; restore. Optionally assert the same for the `IF NOT EXISTS` neutralization gap on a scratch migration.

Landing order matters only for step 3 vs 4: the fixture-map entry and the fixture directories must land in the same commit, or the sweep flags `unknown positive fixture`.

---

## 9. Testable acceptance mapping

| AC | Acceptance (evidence) | Test command / check | Pass criterion (all verified against the prototype) |
|---|---|---|---|
| AC-1 | `rule_r5()` lands; bash+grep/awk only; `SEED_REL`; INSERT/TRUNCATE-only extraction; header allowlist + R5 paragraph; per-relation `grep -q "CREATE TABLE.*<rel>"`; per-column CREATE-body ∪ ALTER resolution; TRUNCATE trailer strip; missing seed silent | `grep -n 'rule_r5\|SEED_REL' scripts/check-load-scenarios.sh`; header reads as §4; run `bash scripts/check-load-scenarios.sh` on a tree with no seed → 0 violations | All six structural greps present; per-relation/column checks as specified; default extraction never emits `FROM|JOIN|INTO|UPDATE|TABLE` tokens from the seed |
| AC-2 | `seed_table_missing` fires ≥1 R5 and 0 R1–R4; `seed_col_missing` fires exactly `published_at`; fixture-map routes both; both loops enumerate `rule_r5` | Fixture sweep (automatic on every invocation) | Reproduced: `seed_table_missing` → 1 R5 relation violation, R1–R4 silent; `seed_col_missing` → exactly 1 (`published_at`) |
| AC-3 | Clean fixture: R1–R5 sweep = 0; holds only with ALTER path + multiline capture + targeted extraction | Negative sweep (automatic) | Reproduced: clean violations = 0 (with ALTER, without `generate_series` capture, with two-line `bench_users` list) |
| AC-4 | Existing positives (`dead_rpc`, `runner_divergence`, `malformed_yaml`, `bench_table_missing`, `missing_k6`) untouched, 0 R5 each | Sweep (automatic) | Reproduced: no-seed tree → 0 (silent-by-design); baseline sweep green before change |
| AC-5.1 | Real tree exits 0 with R5 success line; simultaneously proves zero drift + ALTER path (`accounts.tenant_id` via 000048) | `make check-load-scenarios` | Prototype: real tree violations = 0; removing the ALTER path reddens `accounts.tenant_id` (empirical proof of 000048 resolution) |
| AC-5.2 | README Gates lists R1–R5 | `grep -c '^- \*\*R[1-5]\*\*' scripts/load/README.md` | 5 bullets |
| AC-5.3 | Full done-gate green | done-gate command (§8 step 8) | exit 0; fixture trees outside `check-migration-set` scan root (verified) |
| Anti-vacuity | R5 can never go vacuous | Dedicated positives + clean ALTER/multiline/`generate_series` pins | Any regression to vacuous/over-broad parsing reddens the sweep (each pin's failure reproduced) |

---

## 10. Scope limits (explicitly excluded)

- **Volume-block / invariants / marker-version self-consistency** (header counts vs `\set` defaults; `AUDIT_PUBLICATIONS_ROWS <= AUDIT_LEDGER_ROWS`; Makefile `:238` `-ge 1` vs seed `VALUES (1, …)`) — sibling proposal in the same analysis, **not** folded in; R5 stays a schema-drift tripwire.
- **`partition-index-bench.sql` column/index-name resolution** — sibling R3-extension; R3 stays relation-only.
- **Seed file liveness/non-emptiness** — R4's hardcoded list is unchanged; a missing seed file makes R5 silently green by design (required by AC-4) and documented in the README bullet.
- **Marker column resolution** — the marker is self-defining; no column check applies to it.
- **Seed content edits** (incl. the stale `000003` comment at `seed-bench.sql:86`) — no seed SQL changes; flagged for a future non-gated cleanup.
- No changes to migrations, `Makefile`, `AGENTS.md`, other gates, or the R1–R4 rule bodies.

---

## 11. Appendix: independent reproduction (used to verify §1–§7, §9)

All of the following were run against the live tree from `scripts/`, into `/tmp` only (repo untouched):

1. `grep -vE '^[[:space:]]*--' seed-bench.sql | grep -vE '^[[:space:]]*\\'` → pre-filter leaves exactly the 8 INSERT bodies + TRUNCATE + marker + prologue statements.
2. INSERT extraction → 9 relations (8 + marker); TRUNCATE awk → 8 (incl. `message_reactions`); union after allowlist = 9; every relation resolves (`CREATE TABLE.*` grep): `message_reactions` via 000013, `accounts` via 000031, others per E9.
3. Generic keyword scan over the same file → `generate_series` (8 occurrences), `IF`, plus the marker — confirming hazard 3 and the contrast pin in the clean fixture.
4. Column capture per INSERT table (accumulate to first `)`, literal opener strip) → 7/24/8/7/10/10/13/14 = 93 identifiers; known set = CREATE body ∪ `ALTER TABLE <t> ADD COLUMN` (with `IF NOT EXISTS` neutralization), all 93 resolve; `accounts.tenant_id` resolves **only** via `000048:1`.
5. Specified fixture trees (R5.4 verbatim) → clean 0; `seed_table_missing` 1× relation error; `seed_col_missing` exactly `published_at`; no-seed tree 0.
6. Failure-pin reproductions: naive comma-split → `account_snapshots RESTART IDENTITY`; greedy opener strip → `'a','x','ONCONFLICTDONOTHING'-class` junk; multi-assignment `local` → `unbound variable` under `set -u`; unneutralized ALTER capture → `IF` tokens on 000016/000028/000043 shapes.
7. Baseline gates: `bash scripts/check-load-scenarios.sh` → exit 0 (R1–R4); `check-migration-set.sh` fixture scope confirmed to exclude `load-scenarios/`.