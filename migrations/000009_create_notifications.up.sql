CREATE TABLE notifications (
    id            UUID PRIMARY KEY,
    recipient_uid TEXT NOT NULL,
    type          TEXT NOT NULL,
    actor_uid     TEXT NOT NULL DEFAULT '',
    subject_type  TEXT NOT NULL DEFAULT '',
    subject_id    TEXT NOT NULL DEFAULT '',
    read_at       TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_notifications_recipient ON notifications(recipient_uid, created_at DESC);
CREATE INDEX idx_notifications_unread    ON notifications(recipient_uid)                WHERE read_at IS NULL;
