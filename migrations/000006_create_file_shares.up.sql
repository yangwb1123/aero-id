CREATE TABLE file_shares (
    file_id      UUID NOT NULL,
    grantee_uid  TEXT NOT NULL,
    owner_uid    TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (file_id, grantee_uid)
);
CREATE INDEX idx_file_shares_grantee ON file_shares(grantee_uid);
