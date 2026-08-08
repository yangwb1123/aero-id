# Verification: AppendInTx transaction poisoning, FM-6 lock ordering, and SKIP LOCKED drain interaction

Date: 2026-08-06. Scope: `internal/repository/audit_ledger.go` (working tree), the in-tx swallow
sites, `internal/repository/audit_publication.go` + `internal/service/callback_audit.go` drain, and
§5 FM-4..FM-6 of `docs/plans/2026-08-06-internal-block-in-tx-audit-design.md`. All line numbers are
from the current working tree (`grep -n` verified).

Method: code reading of every path that can return `ErrAuditDuplicate`, plus an empirical
PostgreSQL 17 probe (scratch container) for the transaction-state semantics that the in-tx swallow
contract depends on.

---

## V1 — `createAuditRows` 23505 translation can poison the enclosing tx: CONFIRMED (latent today)

### The two `ErrAuditDuplicate` sources behave differently

`ErrAuditDuplicate` is returned from exactly two places (`audit_ledger.go:49`):

1. **Pre-check `checkAuditDuplicate` (:157-170)** — a plain MVCC `SELECT ... First()`; finding a
   committed row returns `ErrAuditDuplicate` with the transaction **still usable**. Called before
   (:125) and after (:131) the advisory lock; the post-lock call is authoritative for
   same-partition appends. Swallowing this variant and committing is safe.
2. **`createAuditRows` INSERT fallback (:172-205, working-tree diff vs. HEAD)** — when two
   concurrent appends of the same `(tenant_id, source, event_id)` take **different** advisory locks
   (different partitions), both pre-checks pass, the loser's `INSERT` fails raw `23505`
   (`uq_audit_ledger_tenant_event`, migrations/000050), and :184-194 translates it to
   `ErrAuditDuplicate`. **A failed statement puts the PostgreSQL transaction into aborted state**;
   translating the error does not heal it.

### Empirical probe (postgres:17-alpine, unique-constraint violation inside a transaction)

```
BEGIN;
INSERT ... (duplicate)      -> ERROR: duplicate key value violates unique constraint
COMMIT;                     -> command tag "ROLLBACK" (no error to the client)
                            -> the whole transaction silently rolled back

BEGIN;
INSERT ... (duplicate)      -> ERROR
UPDATE ...                  -> ERROR: current transaction is aborted,
                               commands ignored until end of transaction block (25P02)
```

Consequences for the swallow sites (`err != nil && !errors.Is(err, ErrAuditDuplicate)`):

- **Append-last sites** — `operation.go:182`, `callback_audit.go:49,73,101`,
  `sync_job.go:116`, `sync_job_audit.go:55`, `operation_audit.go:136`: the append is the last
  statement; GORM `Transaction` returns `nil` → `Commit()` → server replies `ROLLBACK` with no
  error → `tx.Commit().Error == nil`. **The domain write (operation row, callback subscription,
  sync job, step/status update) is silently discarded while the caller reports success.** This
  directly contradicts the sites' own contract comments ("the swallow runs after the domain write
  inside the same tx, so it can never mask a failed domain write") — it does not mask a *failed*
  write; it silently discards a *successful* one together with the fact. For `sync_job.finish` /
  `operation setStatus`, the job/operation stays leased/running and is re-executed after lease
  expiry (side effects re-run); for `operation.create` / `callback create` the caller gets success
  with nothing persisted (idempotency retry would eventually repair, since the race window closes).
- **`event_receipt.go:180`** — the swallow is *not* last: `finishEventReceipt` runs next and fails
  with `25P02` → the whole apply fails loudly and rolls back. Duplicate tolerance degrades to a
  hard failure of the event apply for the concurrent loser (a sequential retry then hits the safe
  pre-check path once the winner committed).

The standalone `Append` (:87-100) is **not** affected: its transaction is internal, GORM rolls it
back when `appendInTx` errors, and the service-level swallow (`Record`, `service/audit_ledger.go:71`;
`callback.go:229,260`; `account_provision.go:170`) happens outside any caller transaction. The C1
integration test (`TestAppendConcurrentDuplicateAcrossPartitions`,
`audit_consolidation_integration_test.go`) exercises the 23505 path only through the standalone
`Append` — the in-tx loser consequence is **untested**.

### Reachability today: latent, not live

Every current in-tx fact derives `(tenant, source, event_id)` and the partition key from the same
identity: operation facts anchor on `[action, tenant, operation, step, status]` with
account-derived partitions (`operation_audit.go:65`), callback facts use the subscription/delivery
ID (fresh per intent for replay, `callback.go:261`), sync facts anchor on `[action, tenant, job,
status]` (`sync_audit.go:39`), event-receipt facts use `envelope.EventID` with account partitions
(`event_ingest.go:158`, `eventAuditInput`), account-provision facts use `accountID` everywhere
(`account_provision.go:157`). So the same triple always implies the same partition → the same
advisory lock → concurrent losers serialize and are caught by the pre-check (safe path). The 23505
in-tx path becomes live only if a future fact builder mints the same triple under different
partitions, or on a `hashtext` collision (2^-32 per pair — spurious serialization, no correctness
impact). The block design's FM-5 swallow is inert by construction (`EventID` unset →
`checkAuditDuplicate` short-circuits at :158-160, and the constraint ignores NULLs).

**Verdict:** the design's claim "the swallow can never mask a failed domain write" is true only for
the pre-check variant; the 23505 variant (already in the working tree) silently rolls back the
enclosing transaction at the append-last sites and fails it loudly at `event_receipt.go`. The
correctness of the current tree rests on an implicit, untested invariant (partition determinism).
Recommended hardening, in order: (a) make the INSERT itself non-aborting for the duplicate case —
`ON CONFLICT (tenant_id, source, event_id) DO NOTHING` + `RowsAffected == 0 → ErrAuditDuplicate`
(chain_seq conflicts remain impossible under the partition lock, so `DO NOTHING` cannot mask a
corruption case), or a savepoint around the INSERT; (b) add an integration test that runs the
loser's append via `AppendInTx` inside a caller transaction and asserts the subsequent statement /
commit behavior; (c) if (a) is declined, let the raw 23505 propagate in the in-tx path (loud
fail-closed rollback) instead of returning a swallowable `ErrAuditDuplicate`.

## V2 — `pg_advisory_xact_lock` ordering (:128) vs. block lock order backs FM-6: CONFIRMED

- `pg_advisory_xact_lock(hashtext(?))` is at `audit_ledger.go:128`, taken on `fact.PartitionKey`,
  transaction-scoped (released at tx end), held until commit — so the same-partition
  pre-check/chain-read/INSERT are serialized and `chain_sequence` assignment is race-free.
- `user_blocks` (migrations/000012) has **no FK** — only `PRIMARY KEY (blocker_uid, blocked_uid)`.
  The block path's locks are the PK-index row locks of `Block` (`ON CONFLICT DO NOTHING` insert)
  / `Unblock` (delete), all acquired before the advisory lock in the planned
  `BlockWithAudit`/`UnblockWithAudit` tx.
- Every other in-tx append caller also acquires its domain row locks (operations, callbacks, sync
  jobs, account snapshots/receipts) before the advisory lock. The standalone `Append` takes the
  advisory lock first, but acquires no lock afterward that any other transaction holds *before* its
  advisory lock: `audit_ledger` has no FKs, and `audit_publications.fact_id` references the
  same-tx ledger row (migrations/000045). The only post-advisory domain lock in the tree is
  `finishEventReceipt`'s UPDATE (:190), whose row is contested only by same-event appliers that
  also take the advisory lock first — same order, no cycle. The drain never takes the advisory
  lock at all.
- The wait-for graph therefore has edges only in one direction (domain-row → advisory lock →
  ledger unique index); a deadlock needs a cycle, and no path acquires a lock *after* the advisory
  lock that any advisory-lock holder needs *before* it. `hashtext` collisions only add spurious
  serialization.

**Verdict:** FM-6's conclusion (deadlock impossible for the block path) holds. Its wording "no
other code path takes the advisory lock first" is imprecise — the standalone `Append` does — but
with no other locks held, which does not create a cycle.

## V3 — ledger row + pending publication roll back atomically and interact correctly with the SKIP LOCKED drain: CONFIRMED

- `createAuditRows` inserts the ledger row and the `audit_publications` row (`status='pending'`)
  in the same transaction (:172-205), both under the partition advisory lock; `Append` wraps them
  in its own tx. A publication-site failure rolls the ledger row back with it — proven by
  `TestAuditWiringPublicationInsertRollsBackLedgerRow`.
- `ClaimPublication` (`audit_publication.go:31-67`) runs in its own transaction with
  `FOR UPDATE SKIP LOCKED`: MVCC hides uncommitted pending rows from in-flight business
  transactions, rolled-back business txs leave no trace, and concurrent drains cannot double-claim
  the same row. The post-claim ledger read is safe because `audit_ledger` is append-only (trigger,
  migrations/000034) — the fact cannot vanish between claim and read.
- `uq_audit_publication_fact UNIQUE (fact_id)` guarantees one publication per fact (single
  delivery), and `TestAuditPublicationDrainCycleSingleDelivery` covers claim → failing finish →
  re-claim → completed. `FinishPublication` is lease-guarded (`id, status='delivering',
  lease_owner`), so a stale drain cannot double-finish.

**Verdict:** confirmed — atomicity and drain interaction are correct, including the rollback case.

---

## Summary

| Claim | Verdict | Evidence |
|---|---|---|
| 23505-translated `ErrAuditDuplicate` aborts the enclosing tx; swallow sites become unsafe | **Confirmed (latent)** — silent full rollback at the six append-last swallow sites; loud 25P02 failure at `event_receipt.go:180`; unreachable from current fact builders (partition determinism); untested for the in-tx path | code :172-205 + :157-170; PostgreSQL 17 probe; `audit_consolidation_integration_test.go` (standalone only); `audit_wiring_integration_test.go` (pre-check path only) |
| FM-6: advisory-lock ordering vs. block lock order → no deadlock | **Confirmed** (wording nit: standalone `Append` does take the lock first, with no other locks held) | :128; migrations/000012 (no FK), 000034 (no FKs), 000045 (same-tx FK) |
| Ledger + publication roll back atomically; correct with SKIP LOCKED drain | **Confirmed** | :172-205; `audit_publication.go` SKIP LOCKED claim; existing integration tests |

Actions recommended before the block in-tx design lands: (1) fix the 23505 path with
`ON CONFLICT ... DO NOTHING`/`RowsAffected` or a savepoint so the in-tx swallow is poison-free;
(2) add an integration test for the in-tx loser; (3) the design's FM-5 row should cite the
partition-determinism invariant as the reason the swallow cannot fire in-tx, not just "EventID
unset" (which covers block only).
