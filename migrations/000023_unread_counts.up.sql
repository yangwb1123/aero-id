-- Materialized unread counters: replaces COUNT(*) scans on messages and
-- notifications with O(1) lookups from this table.
--
-- The counters are updated transactionally alongside message/notification
-- INSERT (increment) and read updates (decrement). A background reconciliation
-- job (cmd/reconcile-unread) periodically corrects drift between the materialized
-- counters and the real COUNT(*) to guard against bugs or partial failures.
CREATE TABLE IF NOT EXISTS unread_counts (
    uid            TEXT PRIMARY KEY,
    msg_count      BIGINT NOT NULL DEFAULT 0,
    notif_count    BIGINT NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed initial values from existing data (idempotent — ON CONFLICT DO NOTHING
-- so re-running is safe).
INSERT INTO unread_counts (uid, msg_count, notif_count, updated_at)
SELECT
    m.to_uid AS uid,
    COUNT(*) FILTER (WHERE m.read_at IS NULL AND m.deleted_at IS NULL) AS msg_count,
    0::BIGINT AS notif_count,
    now() AS updated_at
FROM messages m
GROUP BY m.to_uid
ON CONFLICT (uid) DO NOTHING;

INSERT INTO unread_counts (uid, msg_count, notif_count, updated_at)
SELECT
    n.recipient_uid AS uid,
    0::BIGINT AS msg_count,
    COUNT(*) FILTER (WHERE n.read_at IS NULL) AS notif_count,
    now() AS updated_at
FROM notifications n
GROUP BY n.recipient_uid
ON CONFLICT (uid) DO UPDATE SET
    notif_count = unread_counts.notif_count + EXCLUDED.notif_count,
    updated_at  = now();
