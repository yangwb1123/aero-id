CREATE TABLE messages (
    id          UUID PRIMARY KEY,
    from_uid    TEXT NOT NULL,
    to_uid      TEXT NOT NULL,
    kind        SMALLINT NOT NULL DEFAULT 1,
    content     TEXT NOT NULL,
    file_id     UUID REFERENCES files(id),
    read_at     TIMESTAMPTZ,
    meta        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL,
    deleted_at  TIMESTAMPTZ
);
CREATE INDEX idx_messages_inbox  ON messages(to_uid,   created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_messages_outbox ON messages(from_uid, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_messages_unread ON messages(to_uid)                    WHERE read_at IS NULL AND deleted_at IS NULL;
