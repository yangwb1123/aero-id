# Projection replication parked failures

## Meaning

`replication_terminal_failures.state = 'parked'` means the remote returned a
confirmed deterministic rejection `max_terminal_attempts` times. The row is
metadata only; the event payload and credentials are not stored. Parked work is
never automatically retried or deleted.

The durable recovery order is:

```text
temporal attempt -> parked row -> stall metric -> quarantine audit -> cursor
```

A crash at any point is safe. A later worker skips publishing a parked event,
retries the deterministic audit fact, and retries the cursor transition.

## Deployment sequence

1. Deploy the binary/config that understands `max_terminal_attempts` while
   replication remains disabled.
2. Apply migrations with `cmd/migrate` (including `000067` and `000068`). The
   shipped worker always uses the commit-ordered change log when available.
   Verify the schema in the target database.
3. Stop old replication workers. Do not run a pre-parking worker beside a new
   worker; an old worker does not consult the parked table.
4. Start the new worker with `max_terminal_attempts: 3` (or an explicitly
   approved value from 1 through 10). Confirm the startup log contains the
   effective value. Rollback means disabling replication first; do not roll
   back the schema underneath a running new worker.

## Triage

```sql
SELECT source_region, target, envelope_event_id, snapshot_id, tenant_id,
       source, dataset, snapshot_version, terminal_attempts, remote_status,
       reason_code, state, first_failed_at, last_failed_at, updated_at
FROM replication_terminal_failures
WHERE state = 'parked'
ORDER BY updated_at ASC;
```

1. Correlate `target`, status, and reason with the remote ingest logs using the
   envelope event ID. Never copy response bodies, tokens, or payloads into a
   ticket or SQL row.
2. Check `aero_control_replication_failures_total` and
   `aero_control_replication_stalls_total{reason="terminal_rejection"}`.
   Counters are process-local and may miss a crash; the table and audit ledger
   are authoritative.
3. Verify the quarantine audit fact exists:

```sql
SELECT event_id, tenant_id, action, resource_id, occurred_at
FROM audit_ledger
WHERE action = 'projection.replication.quarantine'
  AND resource_id = '<envelope_event_id>';
```

4. Fix the remote contract/data or routing problem. Do not edit attempt counts,
   state, or cursor rows by hand.
5. Leave the parked row in place as evidence. There is no unpark operation in
   this release. A controlled replay, if later approved, must use a new
   idempotency key and new audit fact according to the replay policy.

If the audit query is empty, inspect audit ingestion and restart only the new
worker after the audit dependency is healthy; it will retry audit without
republishing. If the audit exists but the cursor is behind, inspect lease
ownership/generation and database errors; a new owner will advance it without
republishing.

## Crash and lease recovery

- A timeout is not proof of remote failure. Network/5xx and `event.in_flight`
  remain transient and leave the cursor unchanged.
- A stale lease owner cannot write terminal state or advance the cursor.
- On restart, use the same database and target identity. The next owner resumes
  from the durable cursor and recognizes parked rows.
- Preserve the table and audit rows during incident cleanup and migration
  rollback. They are operational evidence, not disposable projections.
