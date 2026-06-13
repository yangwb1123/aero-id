-- Composite partial index for the notification unread feed.
-- Covers: WHERE recipient_uid = ? AND read_at IS NULL ORDER BY created_at DESC, id DESC
-- The existing idx_notifications_unread indexes only (recipient_uid) with no ordering
-- columns, forcing a sort step after filtering. This index eliminates that sort for the
-- common "fetch my unread notifications" path.
-- NOTE: no CONCURRENTLY — the migrator (internal/pkg/migrate) runs each migration
-- inside a single transaction (see applyTx), and CREATE INDEX CONCURRENTLY is illegal
-- inside a transaction block. A plain CREATE INDEX is consistent with every other
-- index in this schema and only briefly blocks writes during creation.
CREATE INDEX IF NOT EXISTS idx_notifications_recipient_unread
    ON notifications(recipient_uid, created_at DESC)
    WHERE read_at IS NULL;

-- NOTE: the relay's poll query (WHERE published_at IS NULL ORDER BY created_at ASC, id ASC)
-- is already served by idx_outbox_unpublished from migration 000008, which indexes the
-- identical (created_at, id) WHERE published_at IS NULL. A second index would be a pure
-- duplicate (extra write + storage cost, no read benefit), so it is intentionally omitted.
-- See internal/outbox/repository.go for the query.
