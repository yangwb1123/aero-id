-- seed-bench.sql — idempotent seed for the load-test bench database (P6-08).
--
-- This is a LOAD-TEST TOOL database, NOT an app migration: it creates the
-- aero_load_bench_seed marker table (see below) and populates the hot tables
-- the partition/index benchmark measures (scripts/load/db/partition-index-
-- bench.sql) at realistic volumes. Run it via:
--
--   make load-test-db-seed PG_DSN_BENCH=<bench-dsn>
--
-- (same explicit-DSN + app-DSN-refusal guards as make load-test-db; re-runs
-- are safe: every hot table is TRUNCATEd and re-inserted deterministically,
-- so counts stay exact).
--
-- Volumes (per-cell adjustable; command-line psql variables override the
-- defaults below, e.g.:
--   psql -v AUDIT_LEDGER_ROWS=2000000 -f scripts/load/db/seed-bench.sql):
--
--   accounts            1,000   (FK support rows for account_snapshots; the
--                                only table NOT truncated — seeded with
--                                ON CONFLICT DO NOTHING. Keep
--                                ACCOUNTS_ROWS >= 1 whenever
--                                ACCOUNT_SNAPSHOTS_ROWS > 0; the defaults
--                                are consistent.)
--   audit_ledger      1,000,000 (the count the bench header documents)
--   notifications       200,000
--   outbox_events       100,000
--   audit_publications  100,000
--   messages            200,000
--   files               100,000
--   account_snapshots    50,000
--
-- A 10% "hot slice" of every table targets the bench file's \set constants
-- (ACCOUNT_ID 00000000-0000-4000-8000-000000000001, RECIPIENT/PEER_A/OWNER
-- snaplink:u-load-1, PEER_B snaplink:u-load-2) so EXPLAIN plans match the
-- real query shapes.
--
-- Column lists below mirror the migrations at implementation time
-- (000001_create_files, 000002_create_messages, 000008_create_outbox_events,
-- 000009_create_notifications, 000031_create_account_core,
-- 000032_create_account_snapshots, 000034_create_audit_ledger,
-- 000045_audit_publications). UPDATE THEM when those migrations change; the
-- seed fails against a bench DB whose schema has drifted.
--
-- Marker: aero_load_bench_seed (schema_version, seeded_at, per-table row
-- counts). make load-test-db refuses to run against a database without this
-- marker (the app DB never contains it — that is the substantive guard
-- against running EXPLAIN ANALYZE on the live app schema).

\if :{?ACCOUNTS_ROWS}
\else
\set ACCOUNTS_ROWS 1000
\endif
\if :{?AUDIT_LEDGER_ROWS}
\else
\set AUDIT_LEDGER_ROWS 1000000
\endif
\if :{?NOTIFICATIONS_ROWS}
\else
\set NOTIFICATIONS_ROWS 200000
\endif
\if :{?OUTBOX_EVENTS_ROWS}
\else
\set OUTBOX_EVENTS_ROWS 100000
\endif
\if :{?AUDIT_PUBLICATIONS_ROWS}
\else
\set AUDIT_PUBLICATIONS_ROWS 100000
\endif
\if :{?MESSAGES_ROWS}
\else
\set MESSAGES_ROWS 200000
\endif
\if :{?FILES_ROWS}
\else
\set FILES_ROWS 100000
\endif
\if :{?ACCOUNT_SNAPSHOTS_ROWS}
\else
\set ACCOUNT_SNAPSHOTS_ROWS 50000
\endif

BEGIN;

-- Idempotency: truncate every hot table (all FK-related tables in one
-- statement, no CASCADE needed). message_reactions is a legacy compat table
-- that references messages (migration 000003) and is not measured by the
-- bench; it is truncated and left empty. accounts is deliberately NOT
-- truncated: it is app-schema FK support, seeded idempotently below.
TRUNCATE audit_ledger, notifications, outbox_events, audit_publications,
         messages, message_reactions, files, account_snapshots RESTART IDENTITY;

-- accounts: FK support for account_snapshots (000031 + 000048 scope-tenant
-- ALTER: tenant_id NOT NULL with a non-empty CHECK). Idempotent; a small
-- deterministic set; row 1 is the bench file's ACCOUNT_ID constant.
INSERT INTO accounts (id, canonical_uid, home_region, status, tenant_id, created_at, updated_at)
SELECT
  CASE WHEN i = 1 THEN '00000000-0000-4000-8000-000000000001'::uuid
       ELSE md5('aero-load-acct-' || i)::uuid END,
  'snaplink:u-load-' || i,
  'region-a',
  'active',
  'tenant-load',
  now(),
  now()
FROM generate_series(1, :ACCOUNTS_ROWS) AS i
ON CONFLICT (id) DO NOTHING;

-- audit_ledger (000034): 1M rows; every 10th row belongs to the bench's
-- ACCOUNT_ID / partition constant so the timeline, operation-DAG and chain
-- queries hit real shapes. chain_sequence = i stays unique per partition.
INSERT INTO audit_ledger (
    id, chain_sequence, source, source_region, partition_key,
    event_id, operation_id, correlation_id, causation_id, trace_id,
    actor_uid, account_id, tenant_id, action, resource_type, resource_id,
    occurred_at, payload, payload_hash, previous_hash, record_hash,
    signature_version, signature, created_at)
SELECT
  md5('aero-load-' || i)::uuid,
  i,
  'aero-im',
  'region-a',
  CASE WHEN i % 10 = 0 THEN 'account:00000000-0000-4000-8000-000000000001'
       ELSE 'bench:' || (i % 100) END,
  CASE WHEN i % 10 = 0 THEN md5('aero-load-op-' || i)::uuid ELSE NULL END,
  CASE WHEN i % 10 = 0 THEN '00000000-0000-4000-8000-000000000002'::uuid
       ELSE NULL END,
  NULL,
  NULL,
  '',
  'snaplink:u-load-1',
  CASE WHEN i % 10 = 0 THEN '00000000-0000-4000-8000-000000000001'::uuid
       ELSE NULL END,
  'tenant-load',
  CASE i % 5 WHEN 0 THEN 'account.profile.updated' WHEN 1 THEN 'account.link.created'
             WHEN 2 THEN 'callback.delivered'     WHEN 3 THEN 'export.completed'
             ELSE 'sync.finished' END,
  'account',
  CASE WHEN i % 10 = 0 THEN '00000000-0000-4000-8000-000000000001'
       ELSE md5('aero-load-res-' || i) END,
  now() - ((:AUDIT_LEDGER_ROWS - i) * interval '1 second'),
  '{}'::jsonb,
  md5('aero-load-payload-' || i),
  md5('aero-load-prev-' || i),
  md5('aero-load-record-' || i),
  1,
  'seed-signature',
  now()
FROM generate_series(1, :AUDIT_LEDGER_ROWS) AS i;

-- notifications (000009): every 10th row is an unread row for the bench's
-- RECIPIENT constant (partial-index unread feed shape).
INSERT INTO notifications (id, recipient_uid, type, actor_uid, subject_type, subject_id, read_at, created_at)
SELECT
  md5('aero-load-notif-' || i)::uuid,
  CASE WHEN i % 10 = 0 THEN 'snaplink:u-load-1'
       ELSE 'snaplink:u-load-' || (i % 500) END,
  'message.received',
  'snaplink:u-load-2',
  'message',
  md5('aero-load-msg-' || i),
  CASE WHEN i % 3 = 0 THEN now() - ((i % 1000) * interval '1 minute')
       ELSE NULL END,
  now() - ((:NOTIFICATIONS_ROWS - i) * interval '1 second')
FROM generate_series(1, :NOTIFICATIONS_ROWS) AS i;

-- outbox_events (000008): half unpublished (drain query shape).
INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, payload, created_at, published_at)
SELECT
  md5('aero-load-outbox-' || i)::uuid,
  'account',
  md5('aero-load-agg-' || i),
  'account.profile.updated',
  '{"k":"v"}'::jsonb,
  now() - ((:OUTBOX_EVENTS_ROWS - i) * interval '1 second'),
  CASE WHEN i % 2 = 0 THEN now() - ((i % 100) * interval '1 minute')
       ELSE NULL END
FROM generate_series(1, :OUTBOX_EVENTS_ROWS) AS i;

-- audit_publications (000045): every publication references the seeded
-- audit_ledger row with the same index (fact_id FK), so this must run after
-- audit_ledger and :AUDIT_PUBLICATIONS_ROWS <= :AUDIT_LEDGER_ROWS (defaults
-- are consistent). All rows are pending/delivering (due-queue shape).
INSERT INTO audit_publications (id, fact_id, status, attempt, lease_owner, lease_until, next_attempt_at, error_code, created_at, updated_at)
SELECT
  md5('aero-load-pub-' || i)::uuid,
  md5('aero-load-' || i)::uuid,
  CASE WHEN i % 2 = 0 THEN 'pending' ELSE 'delivering' END,
  i % 5,
  CASE WHEN i % 10 = 0 THEN 'worker-' || (i % 4) ELSE '' END,
  CASE WHEN i % 10 = 0 THEN now() - interval '30 seconds' ELSE NULL END,
  now() - ((i % 100) * interval '1 minute'),
  '',
  now(),
  now()
FROM generate_series(1, :AUDIT_PUBLICATIONS_ROWS) AS i;

-- messages (000002): every 10th row is the bench's conversation pair
-- (snaplink:u-load-1 <-> snaplink:u-load-2). file_id kept NULL (the bench
-- never joins files).
INSERT INTO messages (id, from_uid, to_uid, kind, content, file_id, read_at, meta, created_at, deleted_at)
SELECT
  md5('aero-load-msg-' || i)::uuid,
  CASE WHEN i % 10 = 0 THEN 'snaplink:u-load-1'
       ELSE 'snaplink:u-load-' || (i % 500) END,
  CASE WHEN i % 10 = 0 THEN 'snaplink:u-load-2'
       ELSE 'snaplink:u-load-' || ((i + 1) % 500) END,
  1,
  'seed message ' || i,
  NULL,
  NULL,
  '{}'::jsonb,
  now() - ((:MESSAGES_ROWS - i) * interval '1 second'),
  NULL
FROM generate_series(1, :MESSAGES_ROWS) AS i;

-- files (000001): every 10th row owned by the bench's OWNER constant.
INSERT INTO files (id, owner_uid, name, size, mime_type, storage_key, storage_type, checksum, visibility, meta, created_at, updated_at, deleted_at)
SELECT
  md5('aero-load-file-' || i)::uuid,
  CASE WHEN i % 10 = 0 THEN 'snaplink:u-load-1'
       ELSE 'snaplink:u-load-' || (i % 500) END,
  'seed-file-' || i || '.bin',
  (i * 7919) % 1000000,
  'application/octet-stream',
  'bench/' || i,
  'local',
  md5('aero-load-checksum-' || i),
  0,
  '{}'::jsonb,
  now() - ((:FILES_ROWS - i) * interval '1 second'),
  now() - ((:FILES_ROWS - i) * interval '1 second'),
  NULL
FROM generate_series(1, :FILES_ROWS) AS i;

-- account_snapshots (000032): every 10th row targets the bench's ACCOUNT_ID
-- constant; dataset is globally unique so the (account_id, source, dataset)
-- unique constraint holds regardless of the slice distribution.
INSERT INTO account_snapshots (id, account_id, source, source_region, dataset, status, version, generated_at, last_synced_at, expires_at, error_code, data, created_at, updated_at)
SELECT
  md5('aero-load-snap-' || i)::uuid,
  CASE WHEN i % 10 = 0 THEN '00000000-0000-4000-8000-000000000001'::uuid
       ELSE md5('aero-load-acct-' || (i % :ACCOUNTS_ROWS + 1))::uuid END,
  'aero-im',
  'region-a',
  'datasets.' || i,
  'synced',
  7,
  now() - ((i % 100) * interval '1 minute'),
  now() - ((i % 50) * interval '1 minute'),
  NULL,
  '',
  '{}'::jsonb,
  now(),
  now()
FROM generate_series(1, :ACCOUNT_SNAPSHOTS_ROWS) AS i;

-- Marker: schema_version 1 + per-table row counts. make load-test-db probes
-- this table (SELECT schema_version ... LIMIT 1) before running the bench.
CREATE TABLE IF NOT EXISTS aero_load_bench_seed (
    schema_version int PRIMARY KEY,
    seeded_at timestamptz NOT NULL DEFAULT now(),
    rows jsonb NOT NULL
);
INSERT INTO aero_load_bench_seed (schema_version, seeded_at, rows)
VALUES (1, now(), jsonb_build_object(
    'accounts', :ACCOUNTS_ROWS,
    'files', :FILES_ROWS,
    'messages', :MESSAGES_ROWS,
    'outbox_events', :OUTBOX_EVENTS_ROWS,
    'notifications', :NOTIFICATIONS_ROWS,
    'account_snapshots', :ACCOUNT_SNAPSHOTS_ROWS,
    'audit_ledger', :AUDIT_LEDGER_ROWS,
    'audit_publications', :AUDIT_PUBLICATIONS_ROWS
))
ON CONFLICT (schema_version) DO UPDATE
  SET seeded_at = now(), rows = EXCLUDED.rows;

COMMIT;
