# Design — audit publication dead-letter replay: `RequeuePublication`, `cmd/audit-replay`, lease/purge invariants

| | |
|---|---|
| Module (batch label) | `internal/connector/aeroim` — **label only**; change surface is `internal/repository` + `cmd/` (+ verification in `internal/service`/`internal/app`). The aeroim connector (`internal/connector/aeroim/aeroim.go`, 7 lines, JSON-summary adapter) is untouched |
| Input spec | `docs/requirements/2026-08-07-internal-connector-aeroim-b2-5-dead-letter-replay.md` (REQ-1..REQ-6, T-4 a–e) |
| Date | 2026-08-07 |
| Status | **Design (rev 2, post-adversarial-review reconciliation)** — LANDED (2026-08-08), further amended by the 2026-08-08 remediation plan (also implemented 2026-08-08): `FOR UPDATE` restored (D1/A12); per-tenant MANDATORY facts restored (D7/§2.2a) with `-actor` required and `-tenant` repurposed to a selection filter; the five-key allow-listed payload restored; the mint site moved from `cmd/audit-replay/main.go` into `internal/repository/audit_publication_replay.go` + `internal/repository/callback_audit.go` with the C2 allowlist edited; purge legs carry the outer EPQ guard + `SKIP LOCKED` (migration `000059` indexes). Pre-remediation divergences are recorded in `docs/requirements/2026-08-08-cmd-server-dead-letter-replay.md` §3.2 (remediated rows marked) |

## 0. Evidence verification status (independent re-check, head `13e4676` + working tree)

Every cited symbol in the input spec was re-checked against the tree. All claims **hold**; line
numbers confirmed live; no drift beyond what the spec's amendment register A1–A7 already records.

| # | Claim | Verification | Verdict |
|---|---|---|---|
| E1 | `ClaimPublication` excludes `dead_letter`; interface `audit_ledger.go:42-44`; impl + filter `audit_publication.go:31-75` (`:42`) | `((status = 'pending' AND next_attempt_at <= ?) OR (status = 'delivering' AND lease_until < ?))` at `:42`; defaults limit 50 (`:33`), lease 5m (`:36`); `FinishPublication` at `:77-87` guard `id = ? AND status='delivering' AND lease_owner = ?`, clears lease | ✅ |
| E2 | `publicationOutcome` writes terminal `dead_letter` at `callback_audit.go:98` | func at `:90`; `attempt >= maxAttempts` → `"dead_letter", "audit.publication_failed"` at `:98`; finish failure paths re-claim after lease expiry, never double-settle | ✅ |
| E3 | No `RequeueDead`/`RequeuePublication` anywhere | `grep -rn "RequeueDead\|RequeuePublication" --include="*.go" .` → 0 hits (exit 1) | ✅ |
| E4 | `cmd/` = `migrate`, `server` only | `ls cmd/` → `migrate server` | ✅ |
| E5 | No purge-pause flag; no purge of `audit_publications` at all | `purge_paused`/`purge_pause` symbols appear only in `docs/auto/runs/*/pipeline.yaml` campaign artifacts — zero in code/config/SQL; no DELETE on `audit_publications` anywhere; outbox cleaner (`internal/outbox/repository.go:89-163`) targets `outbox_events` only | ✅ |
| E6 | Migration 000045: `status text` without CHECK; `uq_audit_publication_fact UNIQUE (fact_id)` at `:12`; `fact_id REFERENCES audit_ledger(id) ON DELETE CASCADE` at `:4` | Confirmed verbatim; indexes `idx_audit_publications_due (status, next_attempt_at ASC, id ASC)` and `..._lease` support the replay selection | ✅ |
| E7 | `audit_ledger.go` line count | **442** (spec's A2 drift from "499"): `wc -l` → 442; `audit_publication.go` → 88. Gate hazard stands; sibling file mandated | ✅ |
| E8 | `createAuditRows` `:172-208`: ledger + publication row in one tx; 23505 mapping constraint-precise | `tx.Create(ledgerRow)` then `tx.Create(publicationRow)` in the same tx; only `pgErr.ConstraintName == "uq_audit_ledger_tenant_event"` maps to `ErrAuditDuplicate` (`:195-196`); any other 23505 (incl. publication PK) stays raw → rollback | ✅ |
| E9 | Sentinels `ErrAuditDuplicate` (`:49`), `ErrAuditPublicationLeaseLost` (`:50`); in-tx duplicate-swallow precedent | `repository/callback_audit.go:36-40` — `CreateSubscriptionWithAudit` appends last in-tx and folds `ErrAuditDuplicate` (contract §4.2 defensive parity) | ✅ |
| E10 | `validateAuditBindingCoverage` includes `dead_letter` | `internal/app/audit.go:134-146`; `WHERE publication.status IN ('pending','delivering','dead_letter')` at `:142`; unbound tenant fails next server boot | ✅ |
| E11 | Fake governance endpoint precedent | `internal/connector/auditgovernance/publisher_test.go` — `httptest` `/token` + `/api/v1/events`; `TestPublisherConflictReceiptIsIdempotentSuccess` (conflict receipt → idempotent success, never `ErrInvalidReceipt`) | ✅ |
| E12 | Test infrastructure | `audit_ledger_integration_test.go` (testpg, key `auditLedgerTestKey`); fakes `settlingPublicationRepo`/`failingPublicationRepo` implement **only** `ClaimPublication`/`FinishPublication` (`callback_audit_test.go:32-58`); drain ticker `callback.go:275-281`; `MaxAttempts` default 8 (`callback.go:74-76`); `configs/config.yaml:202` `max_attempts: 8` | ✅ |
| E13 | CLI/config plumbing | `cmd/migrate/main.go`: `main()` → `run(args) error`, `flag.NewFlagSet`, `-config` default `configs/config.yaml`, `config.Load` (`config.go:374`), `db.Open(db.Options{DSN: cfg.Database.DSN(), ...})` (`db.go:20`); `cfg.AuditLedger.SigningKey` (`config.go:111`); `validateAuditLedgerConfig` (`app/audit.go:23`) | ✅ |
| E14 | Gates | `check-audit-swallow` scans `internal/service`; `check-anchored-event-ids.sh internal` — `cmd/` outside both scan roots; `make check-file-sizes` = 500-line cap | ✅ |
| E15 | Single implementer assertion | `wireAuditServices` (`app/audit.go:69-92`) panics unless `auditLedgerRepo` satisfies `AuditPublicationRepository`; no other implementers | ✅ |

Two immaterial drifts beyond the spec's register: domain types live in `internal/domain/audit_ledger.go`
(`AuditFact` `:5`, `AuditPublication` `:48`), not `internal/domain/audit.go`; `validateAuditLedgerConfig`
is at `app/audit.go:23-28` (spec cited `:23-31`). Neither changes the design.

**Conclusion:** the spec's problem statement is accurate: `dead_letter` is a terminal state with no
recovery surface; requeue is a pure UPDATE (idempotent by `uq_audit_publication_fact` + status
predicate); no schema change is required.

## 1. Design overview

Recovery from `dead_letter` is an **operator-initiated, idempotent state transition**: a bounded
`UPDATE` moves `dead_letter` rows back to `pending` with a fresh retry budget, a cleared lease, an
`error_code='manual_replay'` marker, and a **required durable audit fact** (`audit.publication.requeued`,
appended in the same transaction when rows were affected, D7); the existing drain worker (unchanged)
claims the row on its next tick and delivers it through the existing single delivery path. A new
`cmd/audit-replay` composition root drives the transition from the operator shell with
`-limit`/`-id`/`-actor`/`-tenant`/`-dry-run`.

```text
operator ── cmd/audit-replay ── RequeuePublication(ctx, ids, limit, fact)   (fact required; -actor)
                                        │  one tx (READ COMMITTED)
                                        ▼
       1. SELECT id, error_code, attempt FROM audit_publications
            WHERE status='dead_letter'
              AND id IN (ids | SELECT id … ORDER BY next_attempt_at ASC, id ASC LIMIT n)
            FOR UPDATE                       (locked selection: prior values + ids; window closed by locks)
       2. UPDATE audit_publications
            SET status='pending', attempt=0, next_attempt_at=now(Go),
                lease_owner='', lease_until=NULL,
                error_code='manual_replay', updated_at=now(Go)
          WHERE id IN (locked ids) RETURNING id
       3. RowsAffected > 0 → finalize fact.Payload (allow-list: count, publication_ids,
          scope, prior_error_code, prior_attempt) → AppendInTx (ErrAuditDuplicate folded,
          defense-only)                       (0 rows ⇒ no fact; exit stays 0)
                                        ▼
            affected/ids ──► stderr logfmt record + "requeued N publication(s)", exit 0
                              (second run: 0 rows, no fact, exit 0)
                                        │
                    server drain ticker (unchanged, callback.go:275-281)
                                        ▼
            ClaimPublication('pending' due) → publishAuditFact → FinishPublication(…, 'completed')
```

No drain, publisher, or migration code changes. The production-code delta is one interface
extension + one new repository file + one new `internal/service` fact-builder helper + one new
`cmd/` binary + the explicit `check-anchored-event-ids` C2-allowlist edit (§2.5/§3); everything
else is tests.

### 1.1 Design decisions

| # | Decision | Rationale |
|---|---|---|
| D1 | **Window-free in-tx selection + UPDATE (one transaction, two statements):** the bounded selection runs as a **locked read** — `SELECT id, error_code, attempt FROM audit_publications WHERE status='dead_letter' AND id IN (ids \| SELECT id … ORDER BY next_attempt_at ASC, id ASC LIMIT n) FOR UPDATE` — then the UPDATE targets the explicit locked id set and `RETURNING id`; explicit `ids` short-circuit the selection and ignore `limit` | The **required requeue fact's payload** (`prior_error_code`, `prior_attempt`, `publication_ids`) must be captured *before* the UPDATE destroys the prior values — `RETURNING` returns only *new* values, so prior state is unrecoverable from the single-statement form (R5, §8). The `FOR UPDATE` locks close the SELECT→UPDATE window the unlocked form opens (§1.2): a concurrent purge/requeue blocks on the held locks, then EPQ re-checks `status='dead_letter'` against the committed version and skips — so **selection == affected exactly** and the payload is accurate by lock-hold, not by luck. Membership is frozen at the locked read (a row finishing `dead_letter` mid-run is not picked up — DB-reviewer P1 lock-hold semantics). Identical `(next_attempt_at, id)` selection order for concurrent runners ⇒ identical lock order ⇒ no requeue-requeue deadlock. The locked read rides `idx_audit_publications_due` |
| D2 | `next_attempt_at`/`updated_at` set from the **Go clock** (`time.Now().UTC()`), not SQL `now()` | `ClaimPublication`'s due predicate compares against Go `now` (`audit_publication.go:42`); setting the timestamp from the same clock makes a requeued row immediately claimable with zero DB/app skew (a DB-clock-ahead host would otherwise stall the row one drain tick). Matches `FinishPublication`'s existing `updated_at: time.Now().UTC()` precedent. Acceptance (a) asserts `next_attempt_at ≈ now` — satisfied exactly |
| D3 | **`attempt` reset to 0** | Spec REQ-1.3 (A7) fixed this: full fresh retry budget; consistent with configs-sibling REPO-1. Documented divergence from callback-delivery `ResetDeliveryWithAudit` (which preserves attempt) — different table, different recovery contract |
| D4 | **Dry-run exposed as a second interface method** `CountDeadLetterPublications(ctx, ids, limit) (int, error)` sharing one unexported selection helper with `RequeuePublication` | `cmd/audit-replay` can only see the interface; `-dry-run` must run the same bounded selection without mutating. One helper (`deadLetterSelection`) builds both the count query and the UPDATE's selection, so the **query shape** (bounds/order/limit) can never drift — but the count is a **point-in-time estimate**: `RowsAffected` from the real run is authoritative (DB rev 1; doc comment + CLI wording say "estimate"). Dry-run mutates nothing and mints no fact |
| D5 | **Fail-closed preflight, before any DB mutation:** signing key (both modes — mirrors `validateAuditLedgerConfig`), `-actor` (mutating mode only — the fact is required, D7), tenant-membership (mutating mode when the run will mint a fact, §7.1) | Uniform behavior avoids the trap where a dry-run succeeds but the real run fails in a partially-configured environment; the audit invariant (operator identity is a named fact element, audit finding 1) is enforced at the flag boundary, same shape as D6's `-id` validation. Rejected alternative (key check only on the mutating path) adds a mode split for no operator value |
| D6 | **`-id` values validated before any DB call**; any malformed UUID → usage error, exit 1 | No partial requeue; the flag contract is "exactly this set", so a bad ID must not silently shrink the set |
| D7 | **Required in-tx fact; append only when `RowsAffected > 0`; fold is defense-only** | `fact == nil` → typed `ErrAuditFactRequired` **before any DB access** (audit finding 1: AGENTS.md's invariant is categorical — `CallbackAuditRepository` has no fact-less variant, and the exemption list is closed/legacy-only). The repository finalizes `fact.Payload` in-tx from the locked selection with the five allow-listed keys (audit findings 4-5). Append runs after the UPDATE **only when rows were affected** — the `ResetDeliveryWithAudit` precedent (`callback_audit.go:75-93`): a 0-row re-run mints no fact and still exits 0 (R4, ops finding 4). `ErrAuditDuplicate` folds in-tx **as pure defense**: the contract requires a **fresh `event_id` per intent** (C2, minted by the service helper §2.2a) — a deterministic anchor would collide on a second requeue of a re-failed row (F9) and the fold would silently drop the second intent's fact while the requeue commits (callback.go:249-256; rejected in §1.2) |
| D8 | **Run-twice exactly-once semantics scoped to "no intermediate drain re-dead-letter"** + **explicit concurrent-run semantics** (ops finding 1) | Two back-to-back CLI runs: second affects 0 rows (status predicate), mints no fact (D7), exit 0. **Concurrent runs:** (a) identical `(next_attempt_at, id)`-ordered selection ⇒ both select the same bounded set (the `id` tiebreak matters — batch requeues stamp identical Go-clock timestamps); (b) B's locked read blocks on A's row locks, then re-reads the newest versions and filters `status='dead_letter'` → now `pending` → **B selects 0, affects 0** — regardless of backlog size; (c) *sequential* B sees A's rows as `pending` and selects the **next** `limit` rows — bounded-batch rollover, "second affects 0" holds only when `N_total ≤ limit`; (d) different limits: B's selection is a superset of A's, the shared rows are skipped, B affects the remainder; (e) no deadlock — identical selection ⇒ identical lock order. If the endpoint is still down, the drain re-dead-letters and a *later* run requeues again — correct at-least-once operator recovery; duplicate governance acceptance is idempotent via conflict receipts (E11). Pinned by T-4.3 case 4 |
| D9 | **Purge contract pinned to a literal keyset form (handoff + race test)** | No purge exists in the codebase (E5); this spec fixes the full sibling contract so it cannot drift (ops finding 6, DB rev 2): `DELETE FROM audit_publications WHERE status='dead_letter' AND lease_owner='' AND (lease_until IS NULL OR lease_until < now()) AND updated_at < cutoff AND (updated_at, id) < (cursor_ts, cursor_id) ORDER BY updated_at DESC, id DESC LIMIT K`. Keyset = `(updated_at, id)` — the `id` tiebreak is **mandatory** (a batch requeue stamps identical Go-clock `updated_at` values). Cursor = last deleted row's `(updated_at, id)`; a pass deleting 0 ends the scan. `cutoff` is **Go-clock** (D2's domain — a SQL-clock cutoff purges up to δ early/late under skew); the `lease_until < now()` disjunct is SQL-clock but vacuously safe (`dead_letter ⇒ lease_until IS NULL`, E1). Neither existing index covers `updated_at` — the sibling may add `idx_audit_publications_purge (status, updated_at, id)` in its own migration (REQ-6 binds only this spec). T-4.3 case 2 inlines this literal; the pause-flag e2e stays landing-gated (§5 step 8) |
| D10 | **Return = rows affected** (UPDATE `RowsAffected`); raw DB errors propagate | Service/CLI translate; no new *outcome* sentinels (a requeue over purged rows is *not* an error — 0 rows is a valid outcome, D9); the sole new sentinel is `ErrAuditFactRequired` (D11) |
| D11 | **Exactly one new error sentinel (`ErrAuditFactRequired`, D7); no new config keys, no migration, no proto/HTTP surface** | `error_code='manual_replay'` is an existing free-text column; requeue has no new failure contract beyond the raw DB error. The `-actor`/`-tenant` CLI flags are flags, not config keys (REQ-6 stands) |

### 1.2 Rejected alternatives

| Alternative | Rejected because |
|---|---|
| Unlocked SELECT ids first, then UPDATE `WHERE id IN (selected)` | Opens a SELECT→UPDATE window against the sibling purge DELETE and lets the fact payload drift from the affected set; the adopted form (D1) closes the window with `FOR UPDATE` locks inside the same transaction |
| Single-statement `UPDATE … WHERE id IN (subquery)` (original D1) | Cannot feed the required fact's payload: `RETURNING` yields only *new* values, and `prior_error_code`/`prior_attempt` are destroyed by the UPDATE (R5, §8). A writable-CTE variant would need raw SQL and unverified plans; the two-statement locked form keeps GORM idioms and each piece individually verifiable |
| Deterministic anchor for the requeue fact's `event_id` (derived from publication/fact id) | A second requeue of a re-failed row (F9) collides under `(tenant_id, source, event_id)` dedup (`checkAuditDuplicate`), and the in-tx fold would silently drop the second intent's fact while the requeue commits — the exact C2 problem documented at `callback.go:249-256`. Fresh v7 per intent is the contract; the fold is defense-only (D7) |
| Fact-less requeue (optional `fact`, CLI passes `nil`) | AGENTS.md's invariant is categorical with a **closed, legacy-only** exemption list; a brand-new operator replay surface cannot be exempted by a design doc, and `CallbackAuditRepository` has no fact-less variant. Legal fact-free modes only: `-dry-run` and 0-row re-runs (D7) |
| `RequeuePublication(ctx, …, dryRun bool)` | Pollutes the mutation API with a mode flag; D4 keeps the side-effect-free surface explicit |
| Auto-requeue in the drain worker (`publicationOutcome` → `pending` instead of `dead_letter`) | Breaks the keep-enforced terminal-state contract (spec REQ-3); recovery must be operator-initiated so an unreachable endpoint cannot spin forever |
| New SQL migration to add a `CHECK (status IN (...))` | Spec REQ-6: no schema change; the text column is deliberately constrained by code predicates, and `manual_replay`/`binding_unavailable` markers are free-form |

## 2. API changes

### 2.1 `internal/repository/audit_ledger.go:42-44` — interface extension

```go
type AuditPublicationRepository interface {
	ClaimPublication(context.Context, string, time.Duration, int) ([]*domain.AuditPublication, error)
	FinishPublication(context.Context, domain.ID, string, string, string, time.Time) error
	// RequeuePublication moves bounded dead_letter rows back to pending with a
	// fresh retry budget (attempt=0) and error_code='manual_replay'. ids
	// non-empty → exactly that set (limit ignored); else the limit-earliest
	// rows by (next_attempt_at, id). fact is REQUIRED — nil returns
	// ErrAuditFactRequired before any DB access; the implementation finalizes
	// fact.Payload from the affected selection (allow-list: count,
	// publication_ids, scope, prior_error_code, prior_attempt) and appends it
	// in the same tx after the UPDATE, only when rows were affected
	// (ErrAuditDuplicate folded in-tx, defense-only). Returns (rows requeued,
	// affected publication ids) — 0 rows for a re-run or already-purged rows
	// is success and mints no fact. Raw DB errors propagate.
	RequeuePublication(context.Context, []domain.ID, int, *domain.AuditFact) (int, []domain.ID, error)
	// CountDeadLetterPublications reports a point-in-time ESTIMATE of the same
	// bounded selection RequeuePublication would affect (query shape can never
	// drift — deadLetterSelection); RowsAffected from the real run is
	// authoritative. Mutates nothing, mints no fact (cmd/audit-replay -dry-run).
	CountDeadLetterPublications(context.Context, []domain.ID, int) (int, error)
	// DeadLetterTenants returns the distinct tenant_ids (via the audit_ledger
	// join) of the same bounded selection, for the CLI's fail-closed binding
	// check (§7.1). Empty when the selection is empty.
	DeadLetterTenants(context.Context, []domain.ID, int) ([]string, error)
}
```

### 2.2 New file `internal/repository/audit_publication_replay.go` (target ≈ 170 lines)

- `deadLetterSelection(db *gorm.DB, ids []domain.ID, limit int) *gorm.DB` — returns the id selection:
  `Model(&auditPublicationRow{}).Select("id").Where("status = 'dead_letter'")` plus either
  `Where("id IN ?", ids)` (non-empty) or `Order("next_attempt_at ASC, id ASC").Limit(n)` where
  `n := limit; if n <= 0 { n = 100 }`. Single source of truth for the selection (D4).
  Implementation note (DB reviewer): `len(ids) > 0` must gate the `IN` clause — an empty slice
  renders `IN (NULL)` and selects nothing.
- `RequeuePublication` — one `Transaction` (D1):
  1. **Locked selection:** `deadLetterSelection(...)` with `clause.Locking{Strength: "UPDATE"}`
     (`FOR UPDATE`), selecting `id, error_code, attempt`; collect the id/prior-value pairs.
  2. If the selection is empty → return `(0, nil, nil)` — **no fact** (D7 gate).
  3. **Finalize `fact.Payload`** from the selection with exactly the five allow-listed keys:
     `count` (int), `publication_ids` ([]string), `scope` (`"ids"` when ids were given, else
     `"limit:N"` with the effective limit), `prior_error_code` (string — the uniform prior code;
     distinct codes joined by `,` if ever mixed), `prior_attempt` (int — max prior attempt).
     **Never:** copies of the requeued facts' payloads (ID references suffice), lease owners,
     governance endpoint URLs, credentials, stack traces. The payload is *constructed* from
     allow-listed columns — nothing sensitive ever enters it (audit findings 4-5).
  4. `tx.Model(&auditPublicationRow{}).Where("status = 'dead_letter' AND id IN ?", lockedIDs).
     Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}}}).
     Updates(map[string]any{"status": "pending", "attempt": 0, "next_attempt_at": now,
     "lease_owner": "", "lease_until": nil, "error_code": "manual_replay", "updated_at": now})` —
     the **map** form is required: a struct update would silently skip `attempt=0` and
     `lease_until=nil` (GORM codegen verified by the DB reviewer).
  5. `RowsAffected > 0` → `AppendInTx(ctx, tx, fact)`, folding only
     `errors.Is(err, ErrAuditDuplicate)` (any other error aborts the tx — no requeue without
     its fact).
  Return `(result.RowsAffected, returnedIDs, nil)`.
- `CountDeadLetterPublications` — `deadLetterSelection(r.db, ids, limit)` → `SELECT count(*)` over
  the selection; mutate nothing. Doc comment + CLI wording: **estimate** (D4/DB rev 1).
- `DeadLetterTenants` — distinct `ledger.tenant_id` via `JOIN audit_ledger ON id = fact_id` over
  the same selection (`SELECT DISTINCT ledger.tenant_id`); the CLI mirrors the server's
  `<> '' AND <> 'legacy'` exclusions itself (§7.1).

### 2.2a New file `internal/service/audit_replay.go` — fact builder (the C2 mint site)

`BuildAuditRequeueFact(actor, tenant string) *domain.AuditFact` — the one place the requeue
`event_id`/`operation_id` are minted (`EventID: idgen.NewID()`, `OperationID: idgen.NewID()` —
fresh per operator intent, C2), built through the shared `buildAuditFact`/`redactAuditPayload`
path with `Action: "audit.publication.requeued"`, `ResourceType: "audit_publication"`,
`ActorUID: actor`, `TenantID: tenant`, and a nil payload (the repository finalizes the
allow-listed payload in-tx, D7). `check-anchored-event-ids` scans `internal/`, so this file joins
the C2 allowlist (§2.5/§3) — the gate keeps policing the mint (audit finding 2: minting in `cmd/`
would be a scan-root loophole, not a compliance argument).

### 2.3 New binary `cmd/audit-replay/main.go` (composition root, `cmd/migrate` shape)

```text
usage: audit-replay [-config path] [-limit N] [-id id1,id2,…] [-actor NAME] [-tenant ID] [-dry-run]
  -config   path to config file            (default configs/config.yaml)
  -limit    max publications per run       (default 100; ignored when -id set)
  -id       comma-separated publication row IDs (overrides -limit)
  -actor    operator identity for the requeue fact   (REQUIRED in mutating mode; fail-closed, D5)
  -tenant   fact tenant for mixed-tenant batches     (REQUIRED when the selection spans tenants, §7.1)
  -dry-run  report only; mutate nothing, mint no fact (no -actor/-tenant needed)
```

`main()` → `run(args []string) error` (testable entry): parse flags (reject positional args) →
`config.Load` → fail-closed preflight (D5): signing key (both modes) + `-actor` (mutating mode) →
`db.Open(db.Options{DSN: cfg.Database.DSN(), MaxOpenConns, MaxIdleConns, ConnMaxLifetime,
LogLevel})` (cmd/migrate precedent) → `repository.NewAuditLedgerRepository(gdb,
repository.AuditLedgerConfig{SigningKey: cfg.AuditLedger.SigningKey})` → parse `-id` (D6).

- **`-dry-run`:** `CountDeadLetterPublications` → print `would requeue N publication(s) (dry run)`
  (the count is an **estimate**; `RowsAffected` from the real run is authoritative — D4/DB rev 1).
- **Mutating:** `DeadLetterTenants` → tenant resolution + fail-closed membership check (§7.1; exit 1
  before any DB mutation on failure, F11) → `fact := service.BuildAuditRequeueFact(actor, tenant)`
  (§2.2a) → `RequeuePublication(ctx, ids, limit, fact)` → print `requeued N publication(s)`;
  exit 0 (including N=0 — the run mints no fact when 0 rows were requeued, D7).

**Exit-code contract** (ops finding 4):

| Exit | Meaning | Guarantee |
|---|---|---|
| 0 | Success, any N **including 0** (idempotent re-run, purged rows, nothing dead-lettered). N=0 mints no fact (D7) | Scripts must parse the printed count; there is no exit 2 for "nothing matched" (T-4.2 step 2 pins exit 0) |
| 1 | Flag parse, config load, missing signing key, missing `-actor`, malformed `-id`, tenant-check failure, DB open, query error | **Exit 1 ⇒ zero rows requeued, by construction** — every exit-1 path precedes the mutation or aborts the single transaction; no partial-apply path |

**stderr run record** (ops finding 3; additive — stdout contract untouched): one logfmt line per
run on stderr, on success *and* failure (`cmd/migrate` plain-fmt style, no new deps):

```
ts=2026-08-07T06:00:00.000Z level=info cmd=audit-replay config=configs/config.yaml operator=alice
  operation_id=<uuid> dry_run=false limit=100 id_count=3 ids=<uuid>,<uuid>,<uuid> affected=3 duration_ms=42 exit=0
ts=... level=error cmd=audit-replay error="..." exit=1
```

`operator` = `-actor` in mutating mode (authoritative); `LOGNAME`/`USER` env best-effort only for
dry-run — audit-fact attribution and log attribution are separate records (R1, §8). The record is
durable **iff** ops capture stderr (run under a wrapper appending to the ops log/journald). The
three-tier record: (a) transient row marker (`error_code='manual_replay'` — overwritten by the
first settlement), (b) this stderr run record (who/when/exact ids), (c) the durable ledger fact
(§2.2a). No DB table: REQ-6 pins no schema change; in-DB id-level durability is the fact's job.

No publisher, no drain — delivery is the server's job. The CLI never mints event IDs itself
(mint site is `internal/service/audit_replay.go`, §2.2a).

### 2.4 Test-fake extension (`internal/service/callback_audit_test.go:32-58`)

`settlingPublicationRepo` and `failingPublicationRepo` gain the three new methods as recording
no-ops (`t.Fatal`/error if invoked): `RequeuePublication` → `(0, nil, nil)`,
`CountDeadLetterPublications` → `(0, nil)`, `DeadLetterTenants` → `(nil, nil)` — the interface
extension is compile-breaking for exactly these two fakes and nothing else (E15).

### 2.5 Explicitly unchanged — and the one explicit gate change

Unchanged: migrations, protobuf, HTTP/gRPC handlers, `internal/config`, the drain worker,
`internal/connector/auditgovernance`, `internal/service/callback_audit.go`, `internal/app/*`,
`internal/connector/aeroim`.

Changed (explicit, not incidental — audit finding 2): `scripts/check-anchored-event-ids.sh`
`C2_ALLOWLIST` gains `internal/service/audit_replay.go` (the requeue fact-mint site, §2.2a) —
same class as the existing callback.go entry (fresh intent per replay). The gate keeps policing
the mint because the mint lives inside the `internal/` scan root.

## 3. Compatibility constraints

- **Wire/proto/API:** none. `POST /v1/callback/deliveries/:id/replay` keeps its owner-scoped
  single-delivery semantics; the operator replay surface is `cmd/audit-replay` only.
- **Compile impact:** the interface extension breaks exactly the two service-test fakes (§2.4);
  `wireAuditServices` assertion (`app/audit.go:69-92`) keeps proving single-implementer status.
- **Attempt accounting:** requeue resets `attempt` to 0 — the only behavioral divergence from the
  callback-delivery `ResetDeliveryWithAudit` (separate table, preserves attempt). Publication
  `attempt+1`-per-claim and the `MaxAttempts` dead-letter threshold are untouched.
- **Idempotency:** `uq_audit_publication_fact` (one row per fact) + the `status='dead_letter'`
  predicate make re-runs no-ops; conflict receipts at the governance endpoint make duplicate
  deliveries idempotent success (E11) — no publisher change.
- **Gates:** new repository file ≤ 500 lines (target ≈ 170), functions ≤ 50 lines, cyclomatic ≤ 10,
  package average cyclomatic ≤ 5.0; `audit_ledger.go` stays 442, `audit_publication.go` 88;
  `internal/service/audit_replay.go` ≈ 40 lines.
- **`make check-audit-swallow`:** the required in-tx append is a checked call (`err := …; handle`);
  no `_ = …AppendInTx` anywhere. (Script scans `internal/service`; the discipline applies in
  `internal/repository` regardless.)
- **`make check-anchored-event-ids` (explicit gate change, audit finding 2):** exactly one new mint
  site — `internal/service/audit_replay.go` (`EventID: idgen.NewID()`, C2) — added to the script's
  `C2_ALLOWLIST` (§2.5). The repository never mints event IDs; the CLI never mints event IDs
  (minting in `cmd/` would dodge the scan root — a loophole, not a compliance argument).
- **`0 = disabled` convention:** `limit <= 0` → code default 100 (documented code default, not a
  config key).
- **Keep-enforced invariants:** `dead_letter` stays terminal *absent operator action* (no auto-
  requeue); `drainPublications` remains the only `AuditPublisher.Publish` reachability.

## 4. Failure modes

| # | Failure | Behavior after REQ-1/REQ-2 | Recovery / guarantee |
|---|---|---|---|
| F1 | Governance endpoint permanently down | Fact dead-letters after `MaxAttempts` (unchanged); operator runs `cmd/audit-replay` → row `pending`, full fresh budget, next drain tick retries | Repeatable; each run is a fresh operator intent (fresh fact per intent, D7) |
| F2 | Operator runs the CLI twice (back-to-back) | Second run affects 0 rows, exit 0, **mints no fact** (D7 gate); row delivered exactly once (single row per fact + single-claim lease) | Exactly-once per requeue intent absent an intermediate re-dead-letter (D8) |
| F3 | Fact append fails (the required fact path) | Append is last in the tx; any error except `ErrAuditDuplicate` rolls the requeue back — no requeue without its fact | `ErrAuditDuplicate` folds in-tx **as pure defense** (fresh `event_id` per intent means legit runs never collide; deterministic anchors are rejected — §1.2); requeue still commits |
| F4 | Purge races requeue (post-sibling landing) | Requeue-then-purge → purge deletes 0 (row no longer `dead_letter`); purge-then-requeue → requeue affects 0, **no error** (D10); the `audit_ledger` fact always survives (FK cascades only the publication row, E6). **Deadlock:** requeue-vs-requeue cycles are impossible (identical `(next_attempt_at, id)` lock order); under divergent scan orders at scale (DB reviewer, 100k plan) a `40P01` is possible in principle — whole-tx rollback, **no partial mutation**, retry succeeds. Documented; the optional `lock_timeout` knob is **rejected** (`db.Options` exposes no timeout knobs) | Recovery = re-run `cmd/audit-replay`; no backfill for the fact |
| F5 | Signing key unconfigured at CLI | Exit 1 before any DB mutation, in both modes (D5) | Fail-closed; matches server boot contract |
| F6 | DB unreachable during CLI run | Config load or `db.Open` or the transaction errors → exit 1; nothing mutated (D1) | Re-run after DB recovery |
| F7 | Malformed `-id` value | Exit 1 before any DB call (D6) | No partial requeue |
| F8 | Drain worker holds a row as `delivering` (live lease) | Unreachable by `RequeuePublication` (status predicate) and by `ClaimPublication`; requeue cannot steal or clear an active lease | No lease-lost path from replay (T-4.3 case 1) |
| F9 | Endpoint still down after requeue | Row re-dead-letters with a fresh attempt history; operator re-runs (each run = one fresh fact — no collision with the first requeue's fact, R3); no auto-requeue loop (REQ-3 keep-enforced) | At-least-once with operator pacing |
| F10 | `-actor` missing in mutating mode | Exit 1 before any DB access (D5) | Fail-closed; operator identity is a named fact element |
| F11 | Tenant check fails (selection tenant unbound / mixed set without `-tenant`) | Exit 1 before any DB access (§7.1) | Fail-closed; mirrors the server's boot-time coverage check |

**Runbook note (ops finding 2, F1/F9):** requeued rows are claimable on the **next drain tick**
(`worker_interval`, default 1s) — there is **no grace period**; a requeue-while-down burns attempt 1
immediately, and with `MaxAttempts=8` (`configs/config.yaml:202`) + 1s exponential backoff,
re-dead-letter lands after ~8 attempts ≈ 2–3 minutes. Re-delivery of an already-delivered fact is
possible (at-least-once); endpoint conflict-receipt idempotency absorbs duplicates (E11). Requeue
only after confirming governance health, or accept the immediate attempt. **Marker transience (ops
finding 3):** `error_code='manual_replay'` is overwritten by the first settlement ('' on completed,
`audit.publication_failed`/`audit.binding_unavailable` on retry/dead-letter) — after delivery,
nothing in the DB says the row was requeued; the durable record is the ledger fact + the captured
stderr run record (three-tier, §2.3).

## 5. Migration steps (ordered)

1. **Baseline proof** (documented in the PR): `grep -rn "RequeueDead\|RequeuePublication" --include="*.go" .` → 0 hits; `ls cmd/` → `migrate server`; `wc -l internal/repository/audit_ledger.go` → 442.
2. **Repository + service (REQ-1):** extend `AuditPublicationRepository` (§2.1); add
   `internal/repository/audit_publication_replay.go` (§2.2) and `internal/service/audit_replay.go`
   (§2.2a).
3. **Test fakes (REQ-5):** extend `settlingPublicationRepo`/`failingPublicationRepo` (§2.4).
4. **CLI (REQ-2):** add `cmd/audit-replay/main.go` (§2.3).
5. **Gate edit (explicit, audit finding 2):** `C2_ALLOWLIST` in `scripts/check-anchored-event-ids.sh`
   gains `internal/service/audit_replay.go`.
6. **Tests:** T-4.1/T-4.1(b) repository integration (red before step 2 — no symbol; green after);
   T-4.2 CLI e2e; T-4.3 invariants + `TestRequeueVsPurgeInterleaving` + concurrent-run case 4
   (real Postgres, `-race`).
7. **Gate:** `make test && make lint && make lint-strict && make check-file-sizes && make
   check-audit-swallow && make check-anchored-event-ids && make build`, then `make test-integration`
   (required — repository SQL boundary + new race tests).
8. **Handoff:** when the sibling purge scan + `audit_governance.purge_pause`/`data_retention.purge_paused`
   land, re-run T-4.3 case 3 and the combined e2e (outbox-pause spec T-5.4) to close acceptance (d)
   end-to-end; the SQL-boundary half is already green.

**Rollback:** revert steps 2–6 and the new tests. No data migration exists. The exact safe SQL
(ops finding 5 — safe at **any** time; "before any drain tick" describes intent, not safety: the
undo window closes per-row at first claim, and the statement then affects the still-pending
subset):

```sql
-- Revert requeue before delivery: return requeued-but-not-yet-claimed rows to dead_letter.
-- Idempotent; affects only rows still in the requeue window.
UPDATE audit_publications
   SET status = 'dead_letter',
       error_code = 'manual_replay',
       lease_owner = '',
       lease_until = NULL,
       updated_at = now()
 WHERE status = 'pending'
   AND error_code = 'manual_replay';
```

Prefer the id-scoped variant when the run record / the requeue fact's `publication_ids` captured
the ids:

```sql
UPDATE audit_publications
   SET status = 'dead_letter',
       error_code = 'manual_replay',
       lease_owner = '',
       lease_until = NULL,
       updated_at = now()
 WHERE id = ANY(ARRAY['<uuid>,…']::uuid[])
   AND status = 'pending';
```

Safety argument: (1) **marker exclusivity** — `error_code='manual_replay'` is written only by
`RequeuePublication` and every settlement overwrites it, so the predicate cannot catch rows the
feature never requeued nor rows already progressed (re-dead-lettered rows carry
`audit.publication_failed` and are correctly terminal again); (2) `status='pending'` excludes
mid-claim rows — the lease protocol is never corrupted; (3) **no DELETE** — the `audit_ledger` fact
is untouched (the FK cascade fires only ledger→publications); (4) lease cleared + `dead_letter`
satisfies terminal-state invariants (`ClaimPublication` can never select the row); (5) idempotent
(second run affects 0); (6) interleaving-safe vs a concurrent drain claim (EPQ re-checks `pending`)
and vs the sibling purge (both orders safe); (7) **admitted fidelity loss:** pre-requeue `attempt`
history was reset to 0 by design (D3) and is not restorable — ops must not expect the old budget
back.

## 6. Testable acceptance mapping (supplied T-4, preserved verbatim)

> **T-4 (supplied):** (a) RequeueDead moves dead_letter rows to pending with next_attempt_at=now, attempt preserved or reset per spec, and is idempotent (unique fact_id constraint); (b) after requeue, drainPublications delivers the fact to a fake governance endpoint and FinishPublication completes; (c) cmd/audit-replay run twice publishes the fact exactly once (receipt/conflict honored, ErrAuditDuplicate path); (d) purge pause flag blocks purge of dead_letter rows while a replay worker holds the lease; (e) new file internal/repository/audit_publication_replay.go stays under 500 lines.

| Supplied acceptance | Requirement | Testable criteria | Status |
|---|---|---|---|
| **(a)** requeue semantics + idempotency | REQ-1 | **T-4.1** (`internal/repository/audit_publication_replay_integration_test.go`, testpg): drive a fact to `dead_letter` through the public surface only — `AppendInTx` → `ClaimPublication` (worker A, `CallbackConfig{MaxAttempts: 3}`-style failing publisher) → `FinishPublication(..., "dead_letter", "audit.publication_failed", ...)` — then: (1) `RequeuePublication(ctx, ids, limit, fact)` → row `status='pending'`, `attempt=0`, `next_attempt_at` ≈ now (D2), `lease_owner=''`, `lease_until=NULL`, `error_code='manual_replay'`, and the returned ids == affected set; (2) second call same rows → **0 rows, nil ids, no new ledger row** (D7 gate), row unchanged; (3) exactly **one** row per `fact_id` before and after (`count(*)` per fact = 1); (4) rows in `pending`/`delivering`/`completed` untouched; (5) `CountDeadLetterPublications` (dry-run path) mutates nothing and matches the requeue-affected count (estimate semantics, D4); (6) **required fact:** `fact == nil` → `ErrAuditFactRequired`, zero rows touched; (7) **payload allow-list:** the appended `audit.publication.requeued` fact's payload contains exactly `{count, publication_ids, scope, prior_error_code, prior_attempt}` with `prior_error_code='audit.publication_failed'` and `prior_attempt` = the pre-reset max; (8) `ErrAuditDuplicate` defense path: `RequeuePublication` with a fact whose `event_id` collides with an existing ledger row → in-tx fold, no error, requeue commits, exactly 1 ledger row for that event | Red today (no symbol); green after REQ-1 |
| **(b)** drain delivers requeued fact once, `FinishPublication` completes | REQ-3 | **T-4.1(b)** (integration): continue T-4.1 — after requeue, run `drainPublications` against a `callbackService` wired with an `auditgovernance.Publisher` pointed at an `httptest` server (`/token` + `/api/v1/events`, E11 precedent): exactly **1** event POST **per `event_id`** (audit finding 6 — the requeue fact's own publication delivers to governance too, so counters are keyed per event_id, never conflated); the requeued fact's row settles `completed`, `error_code=''`, lease cleared; `ObserveAuditPublicationOutcome("completed")` observed. Service-level complement with the `settlingPublicationRepo`-style fakes asserting claim→publish→finish ordering | Drain path already correct (E1); requeue→deliver e2e red today; green after REQ-1/REQ-3 |
| **(c)** CLI run twice publishes exactly once; receipt/conflict honored; `ErrAuditDuplicate` path | REQ-1.4, REQ-2 | **T-4.2** (integration, `cmd/audit-replay/main_test.go` + testpg): seed N ≥ 1 dead-letter publications (public surface). (1) `run([]string{"-limit", "K", "-actor", "alice"})` with K ≥ N → prints `requeued N publication(s)`, exit 0; (2) identical re-run → `requeued 0`, exit 0, **and zero new `audit_ledger` rows** (R4: exit-0 and fact-minting decoupled); (3) one drain cycle → each event delivered **exactly once per `event_id`** (audit finding 6), rows `completed`; (4) **conflict honored at drain level:** fake endpoint returns `conflict: true` + echoed `event_id` → settlement `completed`, no retry, never re-`dead_letter` (publisher-level E11 already covers the receipt parser; this pins the drain-level contract); (5) `-dry-run` prints `would requeue N publication(s) (dry run)` (estimate wording), mutates nothing, mints no fact, and needs no `-actor`; `-id` with a subset requeues exactly that set; malformed `-id` → non-zero exit, zero rows touched; (6) **fail-closed flags:** missing `-actor` in mutating mode → exit 1, zero rows touched; mixed-tenant selection without `-tenant` → exit 1 (F11); unbound tenant with `AuditGovernance.Enabled=true` → exit 1; (7) stderr carries the logfmt run record (ts/operator/operation_id/…/affected/exit); (8) with K < N, a second sequential run rolls over to the next batch (D8 sequential semantics) | Red today (no CLI); green after REQ-1/REQ-2 |
| **(d)** purge pause blocks purge of dead-letter rows while a replay worker holds the lease | REQ-4 | **T-4.3**: (1) **lease invariant (runnable now):** a `delivering` row with a live `lease_until` (mid-claim, worker B) is unreachable by `RequeuePublication` (status predicate) and by `ClaimPublication`; assert row unchanged; (2) **`TestRequeueVsPurgeInterleaving` (runnable now):** two connections, seed dead-letter rows past the retention bound; purge = the sibling contract literal (D9); both orders — requeue→purge: requeue affects N, purge deletes 0; purge→requeue: purge deletes N, requeue affects 0 **and succeeds**; **lock-hold technique** (DB finding P1): connection A holds a `FOR UPDATE` lock on a candidate while a row finishes `dead_letter` mid-run — the mid-run row is not picked up (membership frozen at the locked read); concurrent run: each row requeued or purged, never both, no error, `audit_ledger` fact always survives; `-race`; (3) **pause-flag e2e (landing-gated):** sibling purge scan + `purge_pause`/`purge_paused` land → flag set → purge pass deletes zero dead-letter rows; replay then requeues and delivers; after flag cleared (restart), past-retention rows purge. Until the siblings land, case 3 is recorded **deferred** with the contract fixed (D9); (4) **concurrent operator runs (ops finding 1 + DB finding P2):** two connections, seed N > limit dead-letter rows; deterministically serialize via a lock-hold barrier so the second selection begins while the first holds the row locks and commits first; assert affected sums to exactly `limit` (second selects 0 — the concurrent case), each row requeued at most once, then one drain cycle with per-event_id POST counter = 1 | Cases 1–2 + 4 testable today; case 3 deferred to combined landing |
| **(e)** new file under 500 lines | REQ-1 | `make check-file-sizes` green; target ≈ 170 lines; functions ≤ 50 lines; cyclomatic ≤ 10 | Red today (file absent); green after REQ-1 |

## 7. Open items and handoff contracts

1. **CLOSED in this spec (audit finding 3) — tenant binding for the requeue fact.** The replay fact
   is appended *after* the last boot, so its `tenant_id` is never covered by the boot-time
   `validateAuditBindingCoverage` check; "derive from the rows" alone is not airtight. Contract:
   the CLI resolves the fact tenant from `DeadLetterTenants` and checks membership against
   `cfg.AuditGovernance.Bindings` **before any DB mutation** (exit 1 on failure, F11), mirroring
   the server's own predicate including its exclusions (`tenant_id <> '' AND <> 'legacy'`):
   - uniform requeued set → that tenant, then membership check;
   - mixed set → require explicit `-tenant`, then membership check;
   - `AuditGovernance.Enabled=false` → no constraint (the server never runs the check either —
     exact mirror);
   - empty selection → no fact will be minted → no check.
   (Alternative, documented but not defaulted: one fact per tenant — `AppendInTx` is tx-scoped and
   callable multiple times per tx, so no interface change would be needed.)
2. **Sibling handoff (purge specs):** T-4.3 case 3 + the combined e2e (outbox-pause T-5.4) close
   acceptance (d) end-to-end; the purge contract is fixed by this design (D9) with the literal
   keyset form (`(updated_at, id)`, `ORDER BY updated_at DESC, id DESC LIMIT K`, Go-clock `cutoff`,
   sibling-owned `idx_audit_publications_purge` optional). Coverage note for the sibling: purged
   rows leave `validateAuditBindingCoverage`'s count — expected; do not "fix" it.
3. **Sibling handoff (configs spec, superseded halves):** AUD-1's publication half is now owned by
   this spec (required fact, §2.2a) — the sibling keeps only the delivery-kind fact. The sibling's
   CLI-1 `-actor` **default `audit-replay` conflicts** with this spec's required-`-actor` contract:
   the merged binary must keep `-actor` required in mutating mode (or the sibling records the
   deviation). The sibling's `-tenant` (selection filter) merges with this spec's `-tenant`
   (mixed-batch fact-tenant selector): one flag, two roles. MET-1 (sibling) may feed
   `audit_publications_requeued_total{dry_run,result}` from the same run data; the CLI cannot reach
   the server metrics sink, so the stderr logfmt record (§2.3) is the CLI's own observability
   either way.
4. **No open decisions remain in this spec's scope:** attempt reset (D3), required fact with
   allow-listed payload (D7), fresh event_id per intent (R3), `-limit` default 100 (D1/D6),
   dry-run surface (D4), clock domain (D2), signing-key/`-actor`/tenant preflight (D5), locked
   in-tx selection (D1), exit-code contract (ops finding 4). Follow-up recorded (not a blocker,
   audit finding 5): the dead-letter transition itself should persist richer failure detail
   (today the underlying error lives only in logs/metrics) — sibling purge/metrics era.

---

## 8. Post-review reconciliation register (adversarial reviews folded, 2026-08-07)

Reconciled positions for the four cross-review tensions, then the finding map. **R5** is a new
inconsistency discovered while folding the reviews (not named in any single review).

**R1 — audit `-actor` vs ops LOGNAME (no conflict once the records are separated).** The durable
ledger fact's `actor_uid` comes from the **required `-actor` flag** (mutating mode only; fail-closed,
D5; dry-run mints no fact and needs no `-actor`). The stderr logfmt record's `operator` field is
best-effort observability: `-actor` when present, else `LOGNAME`/`USER` (dry-run only). `LOGNAME`
never feeds the ledger fact. Ops' "sibling AUD-1 `-actor` is authoritative when it lands" is
**inverted**: this spec owns `-actor`; the sibling's CLI-1 default is superseded (§7.3).

**R2 — RETURNING widening vs the pinned signature + both fakes (accepted as a documented
deviation, A8).** REQ-1.1/REQ-1.5 pin `(int, error)`; the interface does not exist yet (E3), there
are zero real implementers, and the only compile impact is the two service-test fakes being
extended anyway (REQ-5) — widening to `(int, []domain.ID, error)` is free at the code level and
*required* by the fact payload (`publication_ids`) and the id-scoped rollback SQL (§5). The fakes
return `(0, nil, nil)`.

**R3 — D7 in-tx fold vs F9 second-requeue semantics (fold is defense-only).** The design's
"deterministic-anchor variant safe" claim was half-true: a deterministic anchor on a *second*
requeue of a re-failed row collides under `(tenant_id, source, event_id)` dedup, and the in-tx fold
would silently drop the second intent's fact while the requeue commits — the exact C2 problem at
`callback.go:249-256`. Fresh `event_id` per intent is the contract (mint site §2.2a, C2
allowlisted); the fold stays as pure defense (T-4.1 case 8); the deterministic anchor is formally
rejected (§1.2).

**R4 — exit-0-on-zero-rows vs append-only-when-RowsAffected>0 (consistent; pinned together).** Exit
code and fact-minting are decoupled: 0 rows = success (exit 0, **no fact** — the repo's
`RowsAffected > 0` gate, D7, precedent `ResetDeliveryWithAudit`), 1 = failure ⇒ zero rows mutated
by construction. T-4.2(2) asserts all three: exit 0, `requeued 0`, zero new ledger rows.

**R5 — payload prior values × single-statement × RETURNING (new, discovered in folding).**
`RETURNING` yields only *new* values; the UPDATE destroys `error_code`/`attempt`; therefore the
single-statement form cannot feed the audit fact's `prior_error_code`/`prior_attempt`/
`publication_ids`, and an unlocked pre-read would reopen the D1 window and let the payload drift
from the affected set. Resolution: D1 amended to the in-tx locked selection (FOR UPDATE), which
makes selection == affected exactly (lock-hold) and captures prior values pre-UPDATE. The DB
reviewer's concurrency verdicts transfer (EPQ story, four purge orders, stress, no-lease-steal);
the P1 lock-hold experiment becomes the primary test of the amended form (T-4.3 case 2).

### Finding map — every numbered finding resolved

| # | Finding | Disposition |
|---|---|---|
| audit 1 | Fact must be required; nil → fail-closed error before mutation; append only when `RowsAffected > 0` | **Accepted** — D7, D11 (`ErrAuditFactRequired`), §2.1/§2.2/§2.3, F2/F3, T-4.1(2)(6) |
| audit 2 | Nil-fact CLI cannot ship; no exemption slot; gates don't measure it | **Accepted** — §2.3 mints the fact via the service helper; "CLI passes nil" sentences deleted (§2.3, §7.1, §3); C2 allowlist extension explicit (§2.5, §3, §5 step 5) |
| audit 3 | Tenant binding closed in this spec (derive-from-rows is not airtight) | **Accepted** — §7.1 closed contract: `DeadLetterTenants` + `-tenant` + membership check vs `cfg.AuditGovernance.Bindings` (exclusions mirrored); `Enabled=false` → no constraint; exit 1 before mutation (F11); one-fact-per-tenant alternative documented |
| audit 4 | Payload allow-list specified now, via the shared redaction path | **Accepted** — §2.2 five keys, constructed from allow-listed columns (nothing sensitive ever enters; construction beats redaction); envelope via `buildAuditFact` (§2.2a); "Never" list in §2.2; T-4.1(7) asserts exact keys |
| audit 5 | `error_code` overwrite is real; the fact is the fix; keep `manual_replay` on the row | **Accepted** — payload carries `prior_error_code`/`prior_attempt` (locked-selection capture, R5); row marker retained; richer dead-letter detail → follow-up (§7.4) |
| audit 6 | Test assertions must count per `event_id` | **Accepted** — T-4.1(b)/T-4.2(3) per-event_id POST counters (the requeue fact's own publication delivers too) |
| ops 1 | Concurrent-run semantics block + T-4.3 case 4 | **Accepted** — D8 (a)–(e), T-4.3 case 4 (with DB P2) |
| ops 2 | Runbook: no grace period, immediate first attempt, ~2–3 min re-dead-letter, duplicate-possible | **Accepted** — §4 runbook note |
| ops 3 | stderr logfmt record + `RETURNING id` + durability obligation + marker transience | **Accepted** — §2.3 contract + three-tier record; §2.1/§2.2 widening (R2); §4 transience note |
| ops 4 | Exit-code table; N=0 success; dry-run count advisory | **Accepted** — §2.3 table; D4/DB rev 1 wording |
| ops 5 | Exact rollback SQL + safety argument; "safe at any time" correction | **Accepted** — §5 rollback block (two statements, seven-point argument, attempt-history note) |
| ops 6 | D9 keyset literal: `(updated_at, id)` + cursor + `ORDER BY … DESC LIMIT K` + index note + Go-clock cutoff | **Accepted** — D9, §7.2 |
| db P1 | Lock-hold technique missing from the test matrix | **Accepted** — T-4.3 case 2; also the primary verification of amended D1 |
| db P2 | Concurrent second-runner-sees-0 case untested | **Accepted** — T-4.3 case 4 |
| db rev 1 | Count drift: "can never drift" is shape-only; doc + CLI wording | **Accepted** — D4, §2.1/§2.2 doc comments, §2.3 "estimate" |
| db rev 2 | Purge `cutoff` needs a clock domain | **Accepted** — D9/§7.2: `cutoff` is Go-clock |
| db finding-3 caveats | 40P01 possible in principle; GORM map-update; empty-ids `IN (NULL)` | **Accepted** — F4 (40P01 documented; `lock_timeout` **rejected**: `db.Options` has no knob, and whole-tx rollback makes it benign); §2.2 implementation notes |
| db finding-4 | Lease term vacuous/skew-proof; keep `updated_at` pinned | **Accepted as documentation** — D9 text |

**Explicit rejections (reasoned, not silently dropped):** the optional `lock_timeout` knob
(ops/db — no `db.Options` support; 40P01 documented instead, F4); the deterministic-anchor
`event_id` variant (audit 1/R3 — C2 collision on second requeue, §1.2); fact-less requeue /
nil-fact CLI (audit 2 — closed legacy-only exemption list, §1.2); unlocked SELECT-first (D1
rejection updated — window + payload drift, §1.2). The single-statement UPDATE core was
*modified* (R5), not retained — every other ops/db verdict on the concurrency story transfers
unchanged.

**Amendment-register additions (superseding the input spec):** **A8** — REQ-1.1/REQ-1.5 signature
widened to `(int, []domain.ID, error)` + `fact` required (R2); **A9** — AUD-1 publication half and
CLI-1 `-actor` default superseded by this spec (§7.3); **A10** — REQ-2.3's "call once (nil fact)" /
REQ-2.5's "no event IDs minted" replaced by the required-fact flow (§2.3/§2.2a); **A11** — REQ-4.2's
"bounded and keyset-ordered" pinned to the D9 literal; **A12** — REQ-1.2's selection is now a locked
read inside the requeue transaction (D1).

---

### Done-gate relevance

Implementation touches repository SQL, adds a `cmd/*` binary, and edits the anchored-event-ids
allowlist: the gate requires
`make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow &&
make check-anchored-event-ids && make build` and **`make test-integration`** (T-4.1/T-4.1(b)/T-4.2/
T-4.3 use real Postgres via testpg; `TestRequeueVsPurgeInterleaving` runs under `-race`).
Cross-data-center and load gates are unaffected.
