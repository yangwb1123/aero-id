CREATE TABLE idempotency_keys (
    id           UUID PRIMARY KEY,
    uid          TEXT NOT NULL,
    endpoint     TEXT NOT NULL,
    idem_key     TEXT NOT NULL,
    status       INT  NOT NULL DEFAULT 0,   -- 0 = in progress; >0 = completed HTTP status
    content_type TEXT NOT NULL DEFAULT '',
    response     BYTEA,
    created_at   TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (uid, endpoint, idem_key)
);
CREATE INDEX idx_idempotency_expires ON idempotency_keys(expires_at);
