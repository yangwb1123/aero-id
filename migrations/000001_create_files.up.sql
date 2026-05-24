CREATE TABLE files (
    id            UUID PRIMARY KEY,
    owner_uid     TEXT NOT NULL,
    name          TEXT NOT NULL,
    size          BIGINT NOT NULL,
    mime_type     TEXT NOT NULL,
    storage_key   TEXT NOT NULL,
    storage_type  TEXT NOT NULL,
    checksum      TEXT,
    visibility    SMALLINT NOT NULL DEFAULT 0,
    meta          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    deleted_at    TIMESTAMPTZ
);
CREATE INDEX idx_files_owner    ON files(owner_uid, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_files_checksum ON files(checksum);
