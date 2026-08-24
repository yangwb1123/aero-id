ALTER TABLE account_memberships
    ADD COLUMN error_code TEXT NOT NULL DEFAULT '';

CREATE TABLE account_summary_fences (
    account_id    UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    source        TEXT NOT NULL,
    coverage      TEXT NOT NULL,
    source_region TEXT NOT NULL,
    version       BIGINT NOT NULL,
    fingerprint   TEXT NOT NULL DEFAULT '',
    complete      BOOLEAN NOT NULL DEFAULT FALSE,
    reconciled    BOOLEAN NOT NULL DEFAULT FALSE,
    observed_at   TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (account_id, source, coverage)
);

CREATE INDEX idx_account_summary_fences_source_version
    ON account_summary_fences (source, version DESC, updated_at DESC);
