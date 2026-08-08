# Remediation plan — dead-letter replay: synthesis of the three post-landing reviews (audit integrity × database semantics × operator runbook)

| | |
|---|---|
| Synthesizes | `2026-08-08-internal-app-dead-letter-replay-design.md` (design), `2026-08-08-operator-runbook-stress-test.md` (operability), audit-integrity review (replay path vs AGENTS.md), database review (migration + replay/purge SQL) |
| Supersedes | Design §2 D1/D7, §3.4 flag contract, §4 index claim, §5 exact-partition row, §6.3 runbook; requirements `2026-08-08-cmd-server-dead-letter-replay.md` §3.2 divergence table (amended, §9) |
| Status | **IMPLEMENTED (2026-08-08)** per §9 execution order; all §8 invariant checks land green. Execution-time corrections recorded: migration renumbered `000046` → `000059` (000046–000058 landed meanwhile); `check-anchored-event-ids` arg-loop bug fixed (every root scanned); `BuildAuditReplayIntent` takes an explicit kind + shared `operationID` (one OperationID per run across both legs). Deterministic test mechanisms from `2026-08-08-deterministic-replay-regression-tests-design.md` (B1/B2 barriers) used for the EPQ and crash-window tests |
| Date | 2026-08-08 |

## 1. The five cross-review tensions and their resolutions

### T1 — Restored payload × unlocked SELECT (audit_integrity vs database_reviewer)

**Positions.** audit_integrity requires restoring `OperationID` + the allow-listed
`{count, publication_ids, scope, prior_error_code, prior_attempt}` payload through the shared
`buildAuditFact`/`redactAuditPayload` path. database_reviewer justified the unlocked
SELECT→UPDATE only *conditionally*: "safe **now that the payload was dropped**". The payload is the
load-bearing condition — restoring it destroys the justification, because the payload derives from
the *selected rows' prior values* (`prior_error_code`, `prior_attempt`) and the *affected set*
(`count`, `publication_ids`), and an unlocked selection can diverge from the affected set (a
concurrent purge deletes or a concurrent requeue flips a selected row between the SELECT snapshot
and the UPDATE's EPQ recheck).

**Resolution — `FOR UPDATE` returns; divergence is made impossible, then asserted.**
1. The requeue selection becomes the rev-2 D1 locked form, in the two-statement shape that avoids
   locking ledger/subscription rows (see §3.2): an inner `SELECT id … WHERE status='dead_letter'
   … ORDER BY next_attempt_at ASC, id ASC LIMIT n FOR UPDATE` (or `id IN (ids)`), then the tenant
   decorator join, then the UPDATE `WHERE status='dead_letter' AND id IN (locked)`.
2. Under lock-hold + EPQ re-check at lock acquisition, **selection == affected exactly**:
   rows purged/requeued before the lock are filtered out of the locked read; rows locked by us
   cannot change before the UPDATE; the only other outcome is a whole-tx `40P01` rollback (rev-2 F4,
   documented, benign). The payload is therefore accurate **by lock-hold, not by luck** — the
   property rev-2 D1 was designed to give and the landing silently traded away.
3. **RowsAffected-vs-selection divergence handling (the question asked):** the contract becomes
   `RowsAffected == len(locked selection)`; a violation is a programming error, not a runtime
   condition. The repository asserts it inside the tx and aborts (`fmt.Errorf`) rather than mint a
   fact whose `count`/`publication_ids` disagree with the ledger rows they claim — fail-closed:
   no requeue without a truthful fact. `RowsAffected` stays the authoritative *report* value and
   the fact-mint gate (`> 0`).
4. The purge side of the race gets the outer status guard + `SKIP LOCKED` (§T4/W2) so purge never
   deletes a requeued row and never blocks on an in-flight requeue.

### T2 — Fact-mandatory (one fact per affected tenant) × per-kind two-tx commits (audit_integrity vs operability R3)

**Positions.** audit_integrity: the fact must be mandatory; the un-audited default
(`-tenant` empty → `requeueFact` → `nil` → commit with zero facts, pinned by
`TestAuditReplayDefaultInvocationCommitsUnAudited`) violates AGENTS.md; facts must not be
mis-attributable (`-tenant tenant-unbound` stamping over `tenant-a` rows). Operability R3:
`-kind both` commits in two separate transactions, so a kill between legs leaves a cross-kind
partial state invisible to any single-kind check.

**Resolution — per-(leg, tenant) mandatory facts under one per-run operation id.**
1. **Fact tenant derives from the rows, never from a flag.** The selection joins the tenant
   (`audit_ledger.tenant_id` for publications via `fact_id`; `callback_subscriptions.tenant_id`
   for deliveries via `subscription_id` — neither row table carries a tenant column today,
   verified) and the repository mints **one fact per distinct affected tenant** in-tx, all
   `AppendInTx` calls inside the same transaction (rev-2 §7.1 already established this is
   interface-free). The `-tenant` flag is **repurposed from attribution to selection filter**
   (requeue only rows whose tenant matches; empty = all). Mis-attribution becomes structurally
   impossible: the fact tenant *is* the row tenant.
2. **No un-audited default.** `-actor` is required (trimmed, non-empty, length ≤ 256 bytes,
   fail-closed) in mutating mode — the `audit-replay` default is removed. Rows whose tenant is
   empty (`''`) cannot be attributed: the whole tx rolls back with
   `ErrAuditReplayUnattributableTenant` listing the offending row ids — **fail-closed**, the
   direct inverse of today's silent nil-fact path. Tenant `'legacy'` is attributable and exempt
   from the binding check, mirroring the boot gate's exclusions exactly (`internal/app/audit.go:168,172`).
3. **Binding enforcement moves in-tx.** The CLI passes the binding allow-list (`keys of
   `cfg.AuditGovernance.Bindings` when `Enabled`, else `nil` = no constraint) in the intent; the
   repository checks every affected tenant against it on the *actual locked selection* (stronger
   than rev-2 §7.1's pre-flight-only check) and rolls back with `ErrAuditReplayTenantNotBound`
   → exit 1, zero rows. A `DeadLetterTenants` pre-flight stays for the friendly early error and
   the dry-run tenant report, but is no longer load-bearing.
4. **Multi-kind semantics.** Each leg keeps its own transaction (cross-leg atomicity would need a
   two-table API spanning both repos — rejected). One **`OperationID` is minted once per CLI run**
   and stamped on every fact of both legs (`CorrelationID = OperationID`; `CausationID` = the
   row's original `fact_id` when a tenant group has exactly one affected row, zero for multi-row
   groups — batch linkage is the payload's `publication_ids`). Idempotency under
   `uq_audit_publication_fact`: each fact gets a fresh v7 `EventID` per (run, leg, tenant), so the
   dedup key `(tenant_id, source, event_id)` can never collide across re-runs (existing pins
   `TestRequeuePublicationSecondRequeueMintsDistinctFact` keep holding); a re-run after a
   between-legs kill affects 0 rows in the completed leg (no facts) and completes the other leg
   (facts under the new run's `OperationID`). A `-report` journal (§6) plus the restored stderr
   logfmt run record make the partial state visible CLI-side — closing R6's "no marker" gap.

### T3 — Bounded dry-run counts × loop-until-zero runbook × >limit verify (database §4 vs operability R2)

**Positions.** database §4: GORM `Count` drops ORDER BY and the `LIMIT` is a no-op on
`count(*)` — `-dry-run` prints the **full backlog** while a mutating run affects ≤ `-limit`
(default 100; the CLI never loops). Operability R2: with backlog > limit, the verify step's
"second run prints requeued 0" is false (the second run requeues the *next* 100), and the pinned
idempotency test never exercises the >limit case.

**Resolution — the count is bounded to the exact next-run prediction; termination is loop-until-zero.**
1. `Count*` is rebuilt as a bounded count: `SELECT count(*) FROM (SELECT id … ORDER BY
   next_attempt_at ASC, id ASC LIMIT n) s` (the id-selection core shared with the requeue path).
   The result is now **exactly the number the next mutating run will affect** (min(backlog,
   limit)), absent concurrent churn — "estimate" semantics preserved, but the estimate can no
   longer overstate by an order of magnitude. With `-id`, the count is exact.
2. The CLI prints the bound: `publications to requeue: 100 (dry run; capped at -limit 100)`;
   `-dry-run` also prints the affected tenants (from `DeadLetterTenants`).
3. The runbook's replay step becomes **loop-until-zero**: repeat `-dry-run -kind both` → mutate
   while any line is nonzero. Bounded counts make this a *sound termination check*: 0 ⟺ no
   `dead_letter` rows of that kind (the selection is status-scoped), so "second run prints 0" is
   correct **at the end of the loop**, never after one run over a >limit backlog. The verify step
   is likewise `-dry-run -kind both` → both 0, plus row-level terminal-state checks (§7).
4. The `>limit` case becomes a first-class pinned test (`TestAuditReplayCLIBacklogExceedsLimit`:
   250 rows, `-limit 100` → runs print 100/100/50/0; dry-run prints the bounded value at each
   stage).

### T4 — EPQ purge guard + purge index fold into design §4/§5 claims (database §2–3)

**Positions.** database §2 (High): `PurgeDeadLetterPublications`'s DELETE carries the status
predicate only inside the subquery — under READ COMMITTED a concurrent requeue commit can flip a
row to `pending` while the DELETE is in flight and the EPQ recheck (outer quals only: `id IN
(subquery)` still matches) **deletes the requeued row**. The delivery twin already has the outer
guard (`callback_audit.go:189`); the outbox dead-letter leg (`outbox/repository.go:102`) has the
identical flaw while its published leg (`:116`) has the guard. database §3 (Medium): the purge
`updated_at` cutoff has no index support — `idx_audit_publications_due`/`_lease` carry no
`updated_at`, so steady-state ticks walk the whole dead_letter index slice with heap fetches, per
second, forever. Both make design claims **known false**: §4 "requeue/purge ride the existing
due/lease indexes" (false for the purge cutoff) and §5 "no row transitions twice / exact partition
… both sides re-check the dead_letter status in their write" (false for the purge DELETE; the
claim is only *probabilistically* true — `TestConcurrentPurgeAndRequeueRace` passes by luck).

**Resolution — §5's claim becomes true by code, §4's claim is corrected, and both are amended (§9).**
1. Outer guard on the publication purge DELETE: `Where("id IN (?) AND status = 'dead_letter'", sub)`
   (mirror `callback_audit.go:189`); outer guard on the outbox dead-letter leg:
   `Where("id IN (?) AND dead_letter = true", sub)` (mirror `:116`).
2. `FOR UPDATE SKIP LOCKED` in both audit-leg purge subqueries (publication + delivery): purge
   skips rows locked by an in-flight requeue instead of blocking — the design's "bounded
   non-blocking" purge claim stays true now that requeue re-takes `FOR UPDATE` (T1). Outbox leg:
   guard only (no requeue races it; minimal change).
3. New migration `000046_add_dead_letter_purge_indexes` (§5): `idx_audit_publications_purge
   (status, updated_at, id)` and `idx_callback_deliveries_purge (status, updated_at, id)`; both
   purge subqueries reorder to `ORDER BY updated_at ASC, id ASC` so the cutoff becomes the index
   range bound (ordering change is semantics-free: no cross-call cursor). `000045` is applied and
   never edited.
4. A forced-interleaving regression test replaces the probabilistic race coverage (hold the
   requeue tx open between UPDATE and commit; run the purge DELETE; assert the requeued row
   survives) and the test comment at `audit_publication_replay_integration_test.go:322-327` is
   corrected. `migrations_test.go`'s hardcoded 000001–000003 sweep is replaced by a full up/down
   sweep (000045's down has never been executed by any test — database §1).
5. Design doc §4/§5 text amended; amendment registers of all three docs extended (§9).

### T5 — AGENTS.md replay/audit invariant verification

The resulting plan is checked against the invariants in §10: mandatory durable fact, causation and
linkage, allow-listed redacted payload, fail-closed preflight and append, `uq`/re-run idempotency,
controlled-replay doctrine (fresh operation id + reauthorization + fresh idempotency keys + new
audit facts). Every check maps to a concrete plan element and a pinned test.

## 2. Final API contracts

### 2.1 `internal/repository` — replay surface (replaces the landed signatures)

```go
// ReplayIntent is the operator intent for one requeue call (per kind). The
// repository mints one fact per affected tenant; every fact of one CLI run
// shares OperationID. Fact tenant derives from the rows, never from a flag.
type ReplayIntent struct {
    OperationID    domain.ID // one fresh v7 per CLI run (all legs, all tenants)
    ActorUID       string    // required, trimmed, <= 256 bytes (CLI-validated)
    TenantFilter   string    // optional selection filter ('' = all tenants)
    AllowedTenants []string  // binding allow-list; nil = no constraint (governance disabled)
}

// AuditPublicationRepository (internal/repository/audit_ledger.go:42-54) gains:
RequeuePublication(ctx context.Context, ids []domain.ID, limit int, intent ReplayIntent) (int, []*domain.AuditFact, error)
CountDeadLetterPublications(ctx context.Context, ids []domain.ID, limit int, tenantFilter string) (int, error)
DeadLetterTenants(ctx context.Context, ids []domain.ID, limit int, tenantFilter string) ([]string, error)
PurgeDeadLetterPublications(ctx context.Context, cutoff time.Time, limit int) (int64, error) // unchanged shape; internals change

// CallbackRepository (internal/repository/callback.go:19-39) gains the twins:
RequeueDead(ctx context.Context, ids []domain.ID, limit int, intent ReplayIntent) (int, []*domain.AuditFact, error)
CountDeadLetterDeliveries(ctx context.Context, ids []domain.ID, limit int, tenantFilter string) (int, error)
DeadLetterTenants(ctx context.Context, ids []domain.ID, limit int, tenantFilter string) ([]string, error) // via subscriptions join
PurgeDeadLetterDeliveries(ctx context.Context, cutoff time.Time, limit int) (int64, error) // unchanged shape
```

New sentinels (package-level, alongside `ErrAuditDuplicate`): `ErrAuditFactRequired` (nil/zero
intent — defense; rev-2 D11 restored), `ErrAuditReplayUnattributableTenant` (affected rows with
tenant `''`; message lists row ids), `ErrAuditReplayTenantNotBound` (affected tenant outside
`AllowedTenants` and not `'legacy'`).

Return value change `(int, error)` → `(int, []*domain.AuditFact, error)`:
- the facts (IDs filled by `AppendInTx`) drive the CLI's per-leg report and `-report` journal;
- the affected ids ride the facts' `publication_ids`/`delivery_ids` payload (rev-2 R2's rollback
  SQL input is preserved without a second return slice);
- `requeued == 0` → empty fact slice (no fact on 0-row re-runs — unchanged gate).

Compile impact: the two service-test fakes (`settlingPublicationRepo`/`failingPublicationRepo`,
`internal/service/callback_audit_test.go:32-58`) and any `CallbackRepository` fakes — updated to
recording no-ops with the new signatures. `check-audit-swallow` P1/P2 patterns key on the method
names, which are unchanged → gate stays effective.

### 2.2 Requeue transaction (both legs; READ COMMITTED, one tx)

```text
1. inner locked selection:  SELECT id FROM audit_publications
                            WHERE status='dead_letter' AND (id IN (ids) | ORDER BY next_attempt_at ASC, id ASC LIMIT n)
                            FOR UPDATE                                  -- T1: window closed; no join inside the lock
2. tenant decorator:        SELECT p.id, l.tenant_id FROM audit_publications p
                            JOIN audit_ledger l ON l.id = p.fact_id
                            WHERE p.id IN (locked ids)                  -- no locks; fact_id is immutable
   (delivery leg:           JOIN callback_subscriptions s ON s.id = d.subscription_id, s.tenant_id)
3. binding check:           for each distinct tenant t of the locked set:
                              t == ''          -> ErrAuditReplayUnattributableTenant (rollback; row ids in message)
                              t == 'legacy'    -> allow (boot-gate exclusion mirrored)
                              AllowedTenants==nil -> allow (governance disabled)
                              t in AllowedTenants -> allow; else ErrAuditReplayTenantNotBound (rollback)
4. UPDATE:                  SET status='pending', attempt=0, next_attempt_at=now(Go), lease_owner='',
                                 lease_until=NULL, error_code='manual_replay', updated_at=now(Go)
                            WHERE status='dead_letter' AND id IN (locked ids)     -- map form (GORM struct update
                                                                                  -- would skip attempt=0 / lease_until=NULL)
5. invariant assert:        RowsAffected == len(locked ids) else abort   -- T1.3; unreachable by construction
6. fact per tenant:         clone the service-built envelope template; per tenant mint EventID: idgen.NewID()
                            (fresh v7 per run+leg+tenant — C2); set TenantID/PartitionKey/ResourceID; finalize
                            payload; AppendInTx per tenant (ErrAuditDuplicate folded, defense-only — fresh v7
                            makes it unreachable); each append takes its own pg_advisory_xact_lock on the
                            tenant's partition; tenants processed in sorted order (deterministic lock order)
7. return (RowsAffected, facts, nil)
```

Lock ordering: publication rows (ordered) → per-tenant advisory locks (sorted); purge takes row
locks only (`SKIP LOCKED`); `ClaimPublication` touches `pending`/`delivering` only — disjoint.
No cycle; `40P01` documented as whole-tx rollback, re-run (rev-2 F4).

### 2.3 Payload allow-list (restored, construction-only)

```go
// finalized in-tx from the locked selection; exactly these five keys, nothing else:
payload := map[string]any{
    "count":             affected,                    // int == RowsAffected
    "publication_ids":   ids,                         // []string of affected ids   (delivery leg: "delivery_ids")
    "scope":             scope,                       // "ids" | "limit:N" (effective limit)
    "prior_error_code":  priorErrorCodes,             // uniform prior code; distinct codes joined by ","
    "prior_attempt":     priorAttempt,                // int — max prior attempt
}
```

Bounded: ≤ limit ids (100 × ~36 bytes ≈ 3.6KB) ≪ the 64KB guard — documented, no truncation
possible by construction. **Never:** row payloads, lease owners, endpoints, credentials, stack
traces (rev-2 audit 4 "Never" list kept).

### 2.4 `internal/service/audit_replay.go` (new, ~60 L) — the envelope seam

`BuildAuditReplayIntent(actor string, tenantFilter string, allowedTenants []string) (repository.ReplayIntent, error)`:
mints `OperationID: idgen.NewID()` (one per run), validates actor (required, trimmed, ≤ 256 bytes),
and builds the per-kind **envelope template through `buildAuditFact`** (shared path restored:
`normalizeAuditInput` partition composition, `OccurredAt`, `redactAuditPayload` on the empty
template payload, `RequestID` binding) with `Action`/`ResourceType` fixed per kind
(`audit.publication.requeued`/`audit_publication`, `callback.delivery.requeued`/`callback_delivery`),
`EventID` left zero (the repository mints per-tenant `EventID`s inside the tx — it alone knows the
tenant set; a pre-tx mint would drift from the locked selection).

**C2 gate consequence (explicit):** `EventID: idgen.NewID()` moves from `cmd/audit-replay/main.go`
(`requeueFact` deleted) to `internal/repository/audit_publication_replay.go` and
`internal/repository/callback_audit.go` — both inside the gate's scan root (`internal cmd`,
`Makefile:77`). `C2_ALLOWLIST` (`scripts/check-anchored-event-ids.sh:31`) becomes
`internal/service/callback.go|internal/service/audit_fact.go|internal/repository/audit_publication_replay.go|internal/repository/callback_audit.go`,
with the per-(run, leg, tenant) fresh-v7 rationale — the same class as the existing entries
(fresh intent per replay), not a scan-root loophole: the repository is scanned, the edit is
explicit, and the gate keeps policing every other mint.

**Redaction-bypass disposition (the audit_integrity redaction point, resolved honestly):** the
free-form `{"actor": actor}` payload that bypassed the shared path is **deleted** — operator input
lands only in the `ActorUID` envelope column (identical exposure to every other audit fact's
actor, never payload-redacted in any path) with CLI-side validation. The restored payload is
construction-only from allow-listed columns — the *stronger* property than redaction, per rev-2
audit 4's accepted "construction beats redaction". The envelope goes through `buildAuditFact`, so
normalization and the truncation/redaction machinery apply to whatever payload the template
carries. This satisfies the requirement's substance: `OperationID` restored, five-key allow-list
restored, shared path used for the envelope, no verbatim operator input in any payload.

## 3. Purge fixes (T4) — exact SQL forms

### 3.1 `PurgeDeadLetterPublications` (`internal/repository/audit_publication_replay.go:143-156`)

```go
sub := r.db.WithContext(ctx).Model(&auditPublicationRow{}).
    Where("status = 'dead_letter' AND lease_owner = '' AND (lease_until IS NULL OR lease_until < now()) AND updated_at < ?", cutoff).
    Order("updated_at ASC, id ASC").                        // reordered: rides idx_audit_publications_purge
    Select("id").Limit(limit).
    Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked})
result := r.db.WithContext(ctx).
    Where("id IN (?) AND status = 'dead_letter'", sub).     // OUTER GUARD — EPQ re-checked on the new version
    Delete(&auditPublicationRow{})
```

`PurgeDeadLetterDeliveries` (`callback_audit.go:180-194`): identical change (outer guard already
present at `:189`; add reorder + `SKIP LOCKED`). Outbox leg (`outbox/repository.go:102`): outer
guard only (`AND dead_letter = true`), no reorder, no `SKIP LOCKED`.

### 3.2 Migration `000046_add_dead_letter_purge_indexes` (new; `000045` untouched)

```sql
-- up
CREATE INDEX IF NOT EXISTS idx_audit_publications_purge  ON audit_publications  (status, updated_at, id);
CREATE INDEX IF NOT EXISTS idx_callback_deliveries_purge ON callback_deliveries (status, updated_at, id);
-- down
DROP INDEX IF EXISTS idx_audit_publications_purge;
DROP INDEX IF EXISTS idx_callback_deliveries_purge;
```

Steady-state cost drops from O(DLQ size) heap-fetching index entries per tick to O(1) qualifying
entries per tick (≤ 50 + zero skipped leases for `dead_letter` rows). Down-order safety: 000046's
down runs before 000045's/000038's down (reverse application) — indexes are dropped while their
columns still exist.

## 4. CLI contract (`cmd/audit-replay/main.go`)

```text
audit-replay [-config path] [-kind publication|delivery|both] [-limit N] [-id id[,id...]]
             [-tenant T] [-actor NAME] [-dry-run] [-report path]

  -actor   operator identity on requeue facts — REQUIRED in mutating mode (no default);
           trimmed non-empty, <= 256 bytes; fail-closed exit 1
  -tenant  SELECTION FILTER (repurposed): requeue only rows whose ledger/subscription tenant
           equals T; facts still derive tenant from the rows; '' = all tenants
  -report  append-only journal path: one line per leg after commit —
           ts, operation_id, kind, affected, fact_ids — fsynced; closes the R6
           between-legs partial-state marker gap (no marker: re-run both kinds)
  -dry-run prints: "publications to requeue: N (dry run; capped at -limit L)" + affected tenants;
           no -actor needed; mutates nothing, mints nothing
```

- **Preflight order (all fail-closed, exit 1, zero mutation):** flag parse → `-actor` (mutating)
  → config load → signing-key preflight (unchanged, `main.go:83-89`) → DB open → intent build →
  `DeadLetterTenants` pre-flight (friendly early binding error; authoritative check stays in-tx)
  → mutate.
- **Per-leg stdout** keeps the pinned shape (`requeued N publication(s)`) — tests parse it;
  **stderr gains the rev-2 logfmt run record** per leg (restored from rev-2 §2.3:
  `ts=… level=info cmd=audit-replay operator=… operation_id=… kind=… dry_run=false limit=100
  affected=3 fact_ids=… duration_ms=… exit=0`), the observability the landing dropped.
- `-kind both`: leg A (publications) then leg B (deliveries), two txs, one `OperationID`;
  a kill between legs leaves leg A's journal line + facts and no leg B line — re-run `-kind both`
  completes leg B idempotently (0-row leg mints nothing).
- `usage()` text rewritten (no more "commits with NO audit fact" language).

## 5. Corrected runbook (replaces design §6.3; folds R1–R6)

1. **Pause.** Set `audit_governance.purge_pause: true` and **restart** — the flag is boot-captured
   (`internal/app/audit.go:52-53`); a file edit without a restart changes nothing, silently
   (R1). Confirm the restart (process start time; purge counters frozen). The pause is about
   **control and completeness, not atomicity**: the requeue tx + status predicates hold without
   it, but an unpaused drain claims requeued rows within ~1s (`worker_interval`, R5) and the
   purge can delete un-replayed rows between CLI runs. `data_retention.purge_paused` is
   **not needed for this tool** (it gates only the outbox `eventRow` cleaner, R4) — set it only
   if outbox rows are being handled in the same window. Note: in the default config
   (`dead_letter_retention: 0`) the purge is a no-op, so the restart matters only once a
   positive retention is configured (R1).
2. **Dry-run.** `bin/audit-replay -dry-run -kind both`. Each line is **bounded at `-limit`**
   (default 100 per kind) and equals exactly what the next mutating run will affect; it is not
   the backlog. For the true backlog: SQL (`SELECT status, count(*) FROM audit_publications
   WHERE status='dead_letter'` + the `callback_deliveries` twin), or raise `-limit`.
3. **Replay.** `bin/audit-replay -kind both -actor <operator> [-report /var/log/audit-replay.journal]`.
   **Loop-until-zero**: repeat while any `-dry-run` line is nonzero (each run drains the next
   `limit` rows per kind; re-runs are idempotent — a killed run's committed leg affects 0 on
   re-run). After any crash, re-run `-kind both` and check both journal/stderr lines for the new
   `operation_id`. `requeued 0` means "nothing left in this set" — idempotency proof, not
   delivery proof (R6).
4. **Verify.** (a) `bin/audit-replay -dry-run -kind both` → both lines `0` (now a sound
   termination check, T3). (b) Completion: row-level terminal state per kind (publications
   `completed`, deliveries `success` via SQL), or ledger facts
   `action IN ('audit.publication.requeued','callback.delivery.requeued')` in the run window
   (query by `operation_id`). Metrics corroborate only — there is no lag metric for this
   subsystem and the outcome counters are cumulative, global-per-status, batch-uncorrelated (R3);
   the `manual_replay` marker is overwritten by the first finish.
5. **Unpause.** Revert `purge_pause` and restart. Until then the purge scan is suspended and
   rows that re-dead-letter during the window are preserved.

## 6. Tests — new and changed

| Test | Change | Closes |
|---|---|---|
| `TestConcurrentPurgeAndRequeueRace` (`audit_publication_replay_integration_test.go:328`) | Pass a `ReplayIntent` (with `AllowedTenants`); partition claim now *guaranteed* by the outer guard + `SKIP LOCKED`, not probabilistic; comment at `:322-327` corrected | db §2 |
| `TestPurgeDeadLetterPublicationsPredicate` | Update for the `updated_at` ordering | db §3 |
| **New** `TestPurgeDeadLetterPublicationsOuterGuard` | Forced interleaving: connection A holds the requeue tx open between UPDATE and commit; connection B runs the purge DELETE; assert the requeued row survives (deterministic, not a race) | db §2 |
| **New** outbox-leg guard test | Connection A locks a `dead_letter=true` row and flips it to `false`; `PurgeDeadLetters` then skips it (guard) vs deletes it (regression) | db §2 |
| `TestRequeuePublicationFactAppendsInTx` | Intent-based; assert per-tenant facts, five-key payload, shared `OperationID`, `RowsAffected==selection` invariant | audit 1/4, T1 |
| **New** `TestRequeuePublicationPerTenantFacts` | Two tenants in one batch → one fact per tenant, distinct v7 `EventID`s, same tx, payload id sets partitioned correctly | T2 |
| **New** `TestRequeuePublicationEmptyTenantFailsClosed` | Row whose ledger fact has tenant `''` → `ErrAuditReplayUnattributableTenant`, zero rows, zero facts | audit 2a |
| **New** `TestRequeuePublicationUnboundTenantRollsBack` | `AllowedTenants` excludes an affected tenant → `ErrAuditReplayTenantNotBound`, zero rows, zero facts; `'legacy'` rows pass | audit 2a/3 |
| `TestRequeuePublicationLeaseInvariant` | Unchanged (status predicate + lease guard) | — |
| `TestRequeuePublicationSecondRequeueMintsDistinctFact` | Unchanged (fresh per-run v7 → no `uq` collision); add an assertion that both facts share nothing but the run envelope shape | T2 |
| `TestAuditReplayDefaultInvocationCommitsUnAudited` (`i7_boot_hazard_test.go:87`) | **Inverted**: default invocation (no `-actor`) exits 1, zero rows, zero facts | audit 2a |
| `TestAuditReplayUnboundTenantEntersBootCoverage` (`:115`) | **Rewritten**: `-tenant tenant-unbound` over `tenant-a` rows is a filter → empty selection, `requeued 0`, no facts; unbound-tenant rows are refused pre-mutation; boot-coverage backstop for requeue facts becomes unreachable by construction | audit 2b |
| `TestAuditReplayCLIRequeueFactWithTenant` | Reworked for filter semantics | T2 |
| **New** `TestAuditReplayCLIBacklogExceedsLimit` | 250 rows, `-limit 100` → runs print 100/100/50/0; dry-run prints the bounded value at each stage | R2, T3 |
| **New** `TestAuditReplayDryRunCountIsBounded` | Backlog 250, `-limit 100` → dry-run prints 100, not 250 | db §4 |
| **New** `TestAuditReplayLegResumeAfterPartial` | Leg A completes, leg B never runs (simulated) → journal shows A only; re-run completes B idempotently, A re-run affects 0 and mints nothing | R6 |
| `migrations_test.go` | Replace the hardcoded 000001–000003 sweep with a full up/down sweep over all `*.up.sql`/`*.down.sql` pairs (000045 down + 000046 both ways now executed) | db §1 |
| Service fakes (`callback_audit_test.go:32-58`) | New signatures as recording no-ops | §2.1 |
| `TestAuditReplayCLIRunTwiceIsIdempotent`, `TestRequeuePublicationResetsDeadLetterRows`, `TestRequeueDeadResetsDeliveries` | Signature/flag updates only; semantics unchanged | — |

## 7. Design-doc and register amendments (T4; documentation is part of the gate)

Known-false claims and their corrections:

1. **Design `2026-08-08-internal-app-dead-letter-replay-design.md` §4** — "requeue/purge ride the
   existing due/lease indexes" → *requeue rides `idx_audit_publications_due` (locked selection,
   index-only); **purge's `updated_at` cutoff is served by the new
   `idx_audit_publications_purge`/`idx_callback_deliveries_purge` (migration 000046)**; the
   original claim was false for the purge cutoff.*
2. **Design §5** — "no row transitions twice / exact partition … both sides re-check the
   dead_letter status in their write" → *true only after the outer purge guard + `SKIP LOCKED`
   (this plan §3.1); as landed, the purge DELETE re-checked only `id IN (subquery)` and could
   delete a concurrently requeued row (EPQ outer-quals-only).*
3. **Design §2 D1/D7** — replace with the T1 locked selection + T2 per-tenant mandatory facts
   (mint site moves to the repository; C2 allowlist changes). **§3.4** flag contract replaced by
   §4 of this plan. **§6.3** runbook replaced by §5 of this plan. **§6.2 residual delta** —
   `make test-integration` becomes mandatory in this plan's gate; the stale status-line item is
   folded into item 6 below.
4. **Design §9 register** — add: A6 locked-selection restore (supersedes the unlocked drift);
   A7 per-tenant mandatory facts + `-actor` required (supersedes D7's optional-fact drift);
   A8 `-tenant` repurposed to filter; A9 purge outer guard + index + reorder (supersedes the
   §4/§5 claims); A10 bounded dry-run count; A11 C2 allowlist edit (mint moves into
   `internal/repository`); A12 `-report` journal + stderr logfmt record restored.
5. **Requirements `2026-08-08-cmd-server-dead-letter-replay.md` §3.2 divergence table** — add the
   same rows (the table is the referenced home of the drift record).
6. **Rev-2 design `2026-08-07-dead-letter-replay-requeuepublication-design.md`** — status line:
   append "further amended by the 2026-08-08 remediation plan (FOR UPDATE restored D1/A12;
   per-tenant mandatory facts restored D7/§2.2a; payload allow-list restored; mint site moved to
   `internal/repository` with C2 allowlist edit)".
7. **Stress-test doc `2026-08-08-operator-runbook-stress-test.md`** — status: findings accepted
   into the remediation plan: R1 (runbook step 1 + restart confirmation), R2 (loop-until-zero +
   bounded counts), R3 (verify semantics), R4 (knob-role correction), R5 (control-not-atomicity
   wording), R6 (`-report` + leg-resume test). Optional hardening from its §8 that is **not** in
   scope here: paused-boot gauge, unbounded dry-run escape hatch, crash-window kill tests,
   `-report` was promoted to required.
8. **`i7_boot_hazard_test.go`** — file header updated: the two hazards it pinned (un-audited
   default, mis-attribution) are closed by this plan; tests rewritten per §6.

## 8. AGENTS.md invariant verification (T5) — final plan checklist

| Invariant (AGENTS.md) | Plan element | Status |
|---|---|---|
| "Every … **replay** … operation produces a durable audit fact" | Fact mandatory: per-(leg, tenant) in-tx facts; nil-intent → `ErrAuditFactRequired`; empty-tenant rows → fail-closed rollback; the nil-fact path (`requeueFact` returning nil) is deleted; only `-dry-run` and 0-row re-runs (no state change) are fact-free | ✅ by code |
| Controlled replay: "creates a new `replay_id`, reauthorizes, uses new idempotency keys, and writes new audit facts" | One fresh v7 `OperationID` per run (all facts, both legs); actor required + binding membership checked in-tx on the actual selection; fresh v7 `EventID` per (run, leg, tenant); facts appended in-tx after the reset | ✅ |
| Causation/linkage: envelope `operation_id`, `correlation_id`, `causation_id` | `OperationID` restored (per run); `CorrelationID = OperationID`; `CausationID` = the row's original `fact_id` for single-row tenant groups, zero (documented) for multi-row groups — batch linkage is `publication_ids`/`delivery_ids` in the payload; `ResourceID` set per fact | ✅ |
| "Audit payloads are allow-listed and redacted before persistence" | Five construction-only keys from allow-listed columns (nothing sensitive can enter — stronger than redaction, rev-2 audit 4); free-form `{"actor": actor}` payload deleted; envelope through `buildAuditFact` (redaction + 64KB truncation apply to any template payload); payload size bounded ≈ 3.6KB | ✅ |
| Fail-closed preflight/append | Signing-key preflight before DB open + in-tx re-check (kept); `-actor` preflight restored; tenant/binding pre-flight (friendly) + in-tx enforcement (authoritative); append failure rolls back the whole tx (kept); `ErrAuditDuplicate` fold defense-only (kept) | ✅ |
| `uq_audit_publication_fact` + re-runs idempotent | Fresh v7 per (run, leg, tenant) → dedup key `(tenant_id, source, event_id)` never collides; 0-row re-run mints nothing; second requeue of a re-failed row mints a distinct fact (pinned) | ✅ |
| At-least-once / no loss | Delivery stays drain-only (single `Publish` site untouched); ledger fact survives any purge (FK ledger→publications only, kept); requeue/purge partition exact by guard + locks | ✅ |
| "Never reuse historical credentials / reauthorizes" | Requeue uses no credentials at all (row status transition only); the drain's existing publisher path is unchanged | ✅ |

## 9. Execution order and done gate

1. **Migration 000046** + `migrations_test.go` full sweep (independently gate-green).
2. **Purge fixes** (§3.1: outer guards, reorder, `SKIP LOCKED`) + forced-interleaving tests.
3. **Repository replay surface** (§2.2: locked selection, tenant decorator, intent API,
   per-tenant fact minting + payload, bounded counts, `DeadLetterTenants`, new sentinels).
4. **`internal/service/audit_replay.go`** (intent builder; `OperationID` mint; envelope via
   `buildAuditFact`).
5. **CLI** (§4: `-actor` required, `-tenant` filter, `-report`, logfmt records, dry-run wording)
   + `C2_ALLOWLIST` edit.
6. **Tests** (§6) — including the inverted `i7_boot_hazard_test.go` tests and the >limit legs.
7. **Docs** (§7 amendments across the three docs + requirements register).
8. **Gate:**

```text
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow \
&& make check-anchored-event-ids && make check-integration-compile && make build \
&& make test-integration        # now MANDATORY: repository SQL + migration 000046 + new race/interleaving tests
```

File-size budget: `audit_publication_replay.go` 157 → ~300 L, `callback_audit.go` 194 → ~280 L,
`cmd/audit-replay/main.go` 209 → ~290 L, `internal/service/audit_replay.go` new ~60 L — all under
the 500-line gate; per-function ≤ 50 L / cyclomatic ≤ 10 via extraction (payload builder, fact
minting loop, journal writer are separate functions).

## 10. Finding disposition register (the three reviews, consolidated)

| Finding | Disposition |
|---|---|
| audit 1 causation/linkage dropped (`OperationID`, payload) | **Accepted** — restored (T1/T2); mint site moved to the repository with an explicit C2 allowlist edit (§2.4) |
| audit 2a un-audited default | **Accepted** — deleted; `-actor` required; empty-tenant rows fail closed; test inverted (§6) |
| audit 2b free-form `-actor`/`-tenant`, mis-attribution | **Accepted** — `-tenant` repurposed to filter; actor validated (required, ≤ 256 B); facts derive tenant from rows; binding check in-tx (§2.2) |
| audit 3 redaction bypass (`{"actor": actor}` payload) | **Accepted with the honest construction argument** — free-form payload deleted; five construction-only keys; envelope via `buildAuditFact` (§2.4) |
| audit 4/5 idempotency + fail-closed append | **No change needed** — mechanically sound as landed; kept verbatim |
| db §1 down-migration never tested | **Accepted** — full up/down sweep (§6) |
| db §2 EPQ purge outer guard (+ outbox leg) | **Accepted, required** — §3.1 + forced-interleaving test; design §5 claim amended (§7) |
| db §2 optional `SKIP LOCKED` in purge | **Accepted** for both audit legs (keeps non-blocking purge true under restored `FOR UPDATE`); outbox leg guard-only |
| db §3 purge index | **Accepted** — migration 000046 + reorder; design §4 claim amended (§7) |
| db §4 bounded dry-run count | **Accepted** — T3; bounded-count subquery + cap-aware CLI wording + >limit test |
| ops R1/R2/R3/R4/R5/R6 runbook defects | **Accepted** — corrected runbook (§5); R6 additionally closed by `-report` + leg-resume test (§6) |
| Cross-review tension T1 (payload ⇒ FOR UPDATE) | **Resolved** — locked selection returns; `RowsAffected == selection` asserted in-tx (§2.2) |
| Cross-review tension T2 (per-tenant facts × per-kind txs) | **Resolved** — one `OperationID` per run; per-(leg, tenant) facts; two independent txs; idempotent resume (§2.2, §4) |
| Cross-review tension T3 (bounded counts × loop-until-zero) | **Resolved** — bounded count == next-run prediction; loop-until-zero is the termination protocol (§T3, §5) |
| Cross-review tension T4 (schema/claims) | **Resolved** — §3/§7 |
| Cross-review tension T5 (AGENTS.md invariants) | **Verified** — §8 |
