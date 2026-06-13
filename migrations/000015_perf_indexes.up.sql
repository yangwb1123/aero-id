-- Composite partial index for the notification unread feed.
-- Covers: WHERE recipient_uid = ? AND read_at IS NULL ORDER BY created_at DESC, id DESC
-- The existing idx_notifications_unread indexes only (recipient_uid) with no ordering
-- columns, forcing a sort step after filtering. This index eliminates that sort for the
-- common "fetch my unread notifications" path.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_notifications_recipient_unread
    ON notifications(recipient_uid, created_at DESC)
    WHERE read_at IS NULL;

-- Partial index on unpublished outbox events covering the relay's poll query.
-- Covers: WHERE published_at IS NULL ORDER BY created_at ASC, id ASC
-- Once the queue is large, the relay's FetchUnpublished scans only the (typically small)
-- set of un-relayed rows instead of the full event history, making each poll
-- O(unpublished) rather than O(total events).
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_outbox_events_unpublished
    ON outbox_events(created_at ASC, id ASC)
    WHERE published_at IS NULL;
