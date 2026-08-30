# Outbox retry-eligibility rollout and recovery

`next_attempt_at` is read and written by the post-000069 binary. Retry and lease
timestamps are PostgreSQL-owned; the application clock is used only for local
drain duration.

## Forward rollout

1. Quiesce **all** aero-id API and worker instances that contain the new relay.
   Do not leave an old relay running beside the new one: old `ClaimBatch` ignores
   `next_attempt_at`, and old `MarkFailed` does not maintain it.
2. Confirm no old relay process remains active, then apply migration `000069`
   with `cmd/migrate up`. The migration and its bookkeeping row are one
   transaction.
3. Verify `outbox_events.next_attempt_at` is `NOT NULL` and the
   `idx_outbox_unpublished` partial index has
   `(next_attempt_at, created_at, id)` and excludes dead letters.
4. Deploy the new binary and resume the API/workers. Startup rejects a schema
   without the required column before it starts workers or listeners.
5. Watch `aeroid_outbox_eligibility_known`, eligible pending/oldest age,
   claim/finalization errors, renewal errors, claim-loss rate, handler
   failures, and dead-letter counters.

A stats-query failure is non-fatal to delivery, but backlog and age remain at
their last known values and `eligibility_known` is set to `0`. A claim failure
runs no handler and also marks eligibility unknown.

## Rollback

Do not run migration `000069` down under any new binary. First stop **every**
new-version API and worker instance, including instances that only append domain
outbox events. Restore the old binary, run `cmd/migrate down 1`, verify the
pre-000069 index predicate (`published_at IS NULL AND NOT dead_letter`), and
then resume old instances. Never drop `next_attempt_at` while a new API can
write an outbox row.

## Lease and stale-claim response

A lease expiry is not proof that the remote handler failed. A second relay may
reclaim the row and both handlers may run; delivery is at-least-once. The stale
holder's `MarkFailed`, `MarkPublished`, and renewal updates must affect zero
rows (or skip renewal) because the claim token and lease predicate fence them.
Inspect finalization errors and claim-loss rate, and allow the replacement
claim to complete; do not manually clear a newer claim token.

If a handler succeeds but finalization fails, the event remains eligible for
redelivery and is not counted as durably published. Ensure the handler's side
effect is idempotent before forcing replay.

## Incident response

Use a read-only database session for diagnosis. Never put credentials, bearer
tokens, cookies, or event payloads in incident notes.

```sql
SELECT count(*) AS eligible, min(created_at) AS oldest_created_at
FROM outbox_events
WHERE published_at IS NULL AND NOT dead_letter
  AND next_attempt_at <= statement_timestamp()
  AND (claimed_at IS NULL OR claimed_at < statement_timestamp() - interval '30 seconds');

SELECT count(*) AS dead_letters
FROM outbox_events
WHERE dead_letter = true AND published_at IS NULL;
```

### Eligibility, claim, and database failures

For `aeroid_outbox_eligibility_known=0`, do not treat retained pending/age
values as current. A stats failure is non-fatal and delivery may continue; a
claim failure runs no handlers. Check `/readyz`, PostgreSQL saturation/lock
waits, worker logs, and `make migrate-verify`. Remove only an approved blocking
session. Do not manually release or mutate claim rows.

### Handler failures and poison events

`aeroid_outbox_failed_total` counts handler failures, not confirmed retry-state
writes. Identify the event type and bounded IDs, fix the downstream failure,
and let `next_attempt_at` schedule transient retries. For dead letters,
preserve retry history and use an approved, audited replay with a new
idempotency key; never bulk reset `retry_count`, `dead_letter`, or
`next_attempt_at`.

### Finalization, lease expiry, and duplicate delivery

`aeroid_outbox_published_total` is emitted only after durable `MarkPublished`
success. A worker crash, renewal failure, lease expiry, or finalization error
can cause at-least-once redelivery. Check handler duration against the lease,
renewal/finalization counters, and database latency. Do not mark an event
published by hand or retry an unknown remote command without its idempotency
key.

### Worker heartbeat absent

Check worker pod health, crash-loop reasons, rollout history, and migration
jobs. `aeroid_outbox_drain_passes_total` must increase even for empty passes;
a zero backlog alone does not prove the worker is running. To stop delivery
without losing durable work, scale workers to zero and preserve the rows.
