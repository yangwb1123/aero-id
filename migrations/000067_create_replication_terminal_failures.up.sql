-- Apply this migration before starting a worker that understands parked
-- replication failures. lease_generation fences poison-state writes as well as
-- cursor writes during rolling deployment.
ALTER TABLE replication_cursors
    ADD COLUMN IF NOT EXISTS lease_generation BIGINT NOT NULL DEFAULT 0;

CREATE TABLE replication_terminal_failures (
    id                 UUID PRIMARY KEY,
    source_region      TEXT NOT NULL CHECK (btrim(source_region) <> ''),
    target             TEXT NOT NULL CHECK (btrim(target) <> ''),
    envelope_event_id  UUID NOT NULL,
    snapshot_id        UUID NOT NULL,
    tenant_id          TEXT NOT NULL CHECK (btrim(tenant_id) <> ''),
    -- Deliberately no FK: parked work and its audit fact must survive account deletion.
    account_id         UUID NOT NULL,
    source             TEXT NOT NULL CHECK (btrim(source) <> ''),
    dataset            TEXT NOT NULL CHECK (btrim(dataset) <> ''),
    snapshot_version   BIGINT NOT NULL CHECK (snapshot_version > 0),
    terminal_attempts  INTEGER NOT NULL CHECK (terminal_attempts > 0),
    remote_status      INTEGER NOT NULL CHECK (remote_status BETWEEN 100 AND 599),
    reason_code        TEXT NOT NULL CHECK (btrim(reason_code) <> ''),
    first_failed_at    TIMESTAMPTZ NOT NULL,
    last_failed_at     TIMESTAMPTZ NOT NULL,
    state              TEXT NOT NULL CHECK (state IN ('retrying', 'parked')),
    terminal_code      TEXT NOT NULL DEFAULT '',
    parked_at          TIMESTAMPTZ,
    parking_token      UUID,
    created_at         TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL,
    UNIQUE (source_region, target, envelope_event_id),
    CHECK (state = 'retrying' OR btrim(terminal_code) <> '')
);

CREATE INDEX idx_replication_terminal_failures_state
    ON replication_terminal_failures(source_region, target, state, updated_at);
