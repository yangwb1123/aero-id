-- Partition / index benchmark for the aero-id hot paths (P6-08).
--
-- Measures the exact queries the control plane and workers run, using
-- EXPLAIN (ANALYZE, BUFFERS) so plan shape and buffer traffic are visible.
-- Run against a seeded regional cell database; never against production.
--
-- Usage:
--   psql "$PG_DSN" -f scripts/load/db/partition-index-bench.sql
--
-- Interpretation: each block prints the plan and wall time. Watch for:
--   * Index Scan on idx_audit_ledger_query / idx_notifications_recipient_unread
--     (a Seq Scan on a large table is a regression signal);
--   * high Shared buffers read on keyset pagination (index-only scan expected);
--   * the outbox drain query must use idx_outbox_unpublished and never sort
--     the full table.
--
-- Seed first (adjust counts to your cell):
--   INSERT INTO audit_ledger (...) SELECT ... FROM generate_series(1, 1000000);

-- Query parameters (override on the command line, e.g.
-- psql -v ACCOUNT_ID=<uuid> -f scripts/load/db/partition-index-bench.sql):
-- Values below are placeholders; replace with real IDs from the seeded cell.
\set ACCOUNT_ID '00000000-0000-4000-8000-000000000001'
\set CURSOR 'ffffffff-ffff-4fff-8fff-ffffffffffff'
\set OPERATION_ID '00000000-0000-4000-8000-000000000002'
\set SOURCE 'aero-im'
\set PARTITION 'account:00000000-0000-4000-8000-000000000001'
\set RECIPIENT 'snaplink:u-load-1'
\set PEER_A 'snaplink:u-load-1'
\set PEER_B 'snaplink:u-load-2'
\set OWNER 'snaplink:u-load-1'

\echo '=== 1. Audit ledger: account timeline (keyset, descending) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, occurred_at, action, resource_type, resource_id
  FROM audit_ledger
 WHERE account_id = :'ACCOUNT_ID' AND id < :'CURSOR'
 ORDER BY occurred_at DESC, id DESC
 LIMIT 21;

\echo '=== 2. Audit ledger: operation timeline (causation DAG) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, occurred_at, action
  FROM audit_ledger
 WHERE operation_id = :'OPERATION_ID'
 ORDER BY occurred_at ASC, id ASC
 LIMIT 200;

\echo '=== 3. Audit ledger: partition chain verification ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT chain_sequence, record_hash, signature, previous_hash
  FROM audit_ledger
 WHERE source = :'SOURCE' AND partition_key = :'PARTITION'
 ORDER BY chain_sequence ASC
 LIMIT 1000;

\echo '=== 4. Notifications: unread feed (partial index) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, created_at, action
  FROM notifications
 WHERE recipient_uid = :'RECIPIENT' AND read_at IS NULL
 ORDER BY created_at DESC, id DESC
 LIMIT 21;

\echo '=== 5. Notifications: unread count (partial index only) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT count(*) FROM notifications
 WHERE recipient_uid = :'RECIPIENT' AND read_at IS NULL;

\echo '=== 6. Outbox drain (FOR UPDATE SKIP LOCKED worker) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, event_type, payload
  FROM outbox_events
 WHERE published_at IS NULL
 ORDER BY created_at ASC, id ASC
 LIMIT 100
 FOR UPDATE SKIP LOCKED;

\echo '=== 7. Audit publications: due deliveries (callback worker) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT p.id, p.fact_id, p.subscription_id, p.next_attempt_at
  FROM audit_publications p
 WHERE p.status IN ('pending', 'delivering')
   AND p.next_attempt_at <= now()
 ORDER BY p.next_attempt_at ASC
 LIMIT 50
 FOR UPDATE SKIP LOCKED;

\echo '=== 8. Messages: conversation keyset page ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, from_uid, to_uid, content, created_at
  FROM messages
 WHERE ((from_uid = :'PEER_A' AND to_uid = :'PEER_B')
     OR (from_uid = :'PEER_B' AND to_uid = :'PEER_A'))
   AND deleted_at IS NULL
   AND id < :'CURSOR'
 ORDER BY id DESC
 LIMIT 21;

\echo '=== 9. Files: owner list (keyset) ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, name, size, visibility, created_at
  FROM files
 WHERE owner_uid = :'OWNER' AND deleted_at IS NULL AND id < :'CURSOR'
 ORDER BY id DESC
 LIMIT 21;

\echo '=== 10. Projection snapshot: account datasets freshness ==='
EXPLAIN (ANALYZE, BUFFERS)
SELECT dataset, version, status, last_synced_at
  FROM account_snapshots
 WHERE account_id = :'ACCOUNT_ID'
 ORDER BY dataset ASC;

\echo '=== 11. Index health summary ==='
SELECT schemaname, relname, indexrelname, idx_scan, idx_tup_read, idx_tup_fetch
  FROM pg_stat_user_indexes
 WHERE relname IN ('audit_ledger', 'notifications', 'outbox_events', 'audit_publications', 'messages', 'files', 'account_snapshots')
 ORDER BY relname, idx_scan DESC;
