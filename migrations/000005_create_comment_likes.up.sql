CREATE TABLE comment_likes (
    comment_id UUID NOT NULL REFERENCES comments(id),
    uid        TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (comment_id, uid)
);
