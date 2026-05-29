CREATE TABLE user_blocks (
    blocker_uid  TEXT NOT NULL,
    blocked_uid  TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_uid, blocked_uid)
);
CREATE INDEX idx_user_blocks_blocked ON user_blocks(blocked_uid);
