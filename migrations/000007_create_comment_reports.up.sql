CREATE TABLE comment_reports (
    id           UUID PRIMARY KEY,
    comment_id   UUID NOT NULL REFERENCES comments(id),
    reporter_uid TEXT NOT NULL,
    reason       TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (comment_id, reporter_uid)
);
CREATE INDEX idx_comment_reports_comment ON comment_reports(comment_id, created_at DESC);
