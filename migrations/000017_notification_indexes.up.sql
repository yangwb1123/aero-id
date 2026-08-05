-- Composite index for the common notification list query:
--   SELECT ... FROM notifications
--   WHERE recipient_uid = ?
--   ORDER BY created_at DESC, id DESC
--   LIMIT n+1;
CREATE INDEX IF NOT EXISTS idx_notifications_recipient_created
    ON notifications(recipient_uid, created_at DESC);

-- Partial index for the unread count query:
--   SELECT count(*) FROM notifications
--   WHERE recipient_uid = ? AND read_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_notifications_recipient_unread
    ON notifications(recipient_uid)
    WHERE read_at IS NULL;
