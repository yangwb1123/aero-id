CREATE TABLE comments (
    id           UUID PRIMARY KEY,
    owner_uid    TEXT NOT NULL,
    target_type  TEXT NOT NULL,
    target_id    UUID NOT NULL,
    parent_id    UUID REFERENCES comments(id),
    root_id      UUID,
    content      TEXT NOT NULL,
    like_count   INT NOT NULL DEFAULT 0,
    reply_count  INT NOT NULL DEFAULT 0,
    status       SMALLINT NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    deleted_at   TIMESTAMPTZ
);
CREATE INDEX idx_comments_target ON comments(target_type, target_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_owner  ON comments(owner_uid, created_at DESC)              WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_root   ON comments(root_id, created_at)                     WHERE deleted_at IS NULL;
