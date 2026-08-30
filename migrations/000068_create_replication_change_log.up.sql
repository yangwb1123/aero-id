-- A timestamp is presentation metadata, not an ordering primitive. This log
-- gives every accepted snapshot mutation (including tombstones) an immutable
-- position. The clock row is locked by writers before allocating a position,
-- so positions follow commit order rather than merely allocation order.
CREATE SEQUENCE replication_change_sequence;
CREATE TABLE replication_change_clock (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    next_sequence BIGINT NOT NULL DEFAULT 0 CHECK (next_sequence >= 0)
);
INSERT INTO replication_change_clock (id, next_sequence) VALUES (TRUE, 0);

CREATE TABLE account_snapshot_replication_changes (
    sequence        BIGINT PRIMARY KEY,
    snapshot_id     UUID NOT NULL,
    account_id      UUID NOT NULL,
    tenant_id       TEXT NOT NULL,
    home_region     TEXT NOT NULL,
    source          TEXT NOT NULL,
    source_region   TEXT NOT NULL,
    dataset         TEXT NOT NULL,
    status          TEXT NOT NULL,
    version         BIGINT NOT NULL,
    generated_at    TIMESTAMPTZ NOT NULL,
    last_synced_at  TIMESTAMPTZ,
    expires_at      TIMESTAMPTZ,
    error_code      TEXT NOT NULL DEFAULT '',
    data            JSONB NOT NULL DEFAULT '{}'::jsonb,
    deleted         BOOLEAN NOT NULL DEFAULT FALSE,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_snapshot_replication_changes_scan
    ON account_snapshot_replication_changes (sequence ASC);

INSERT INTO account_snapshot_replication_changes
    (sequence, snapshot_id, account_id, tenant_id, home_region, source, source_region, dataset,
     status, version, generated_at, last_synced_at, expires_at, error_code, data, occurred_at)
SELECT nextval('replication_change_sequence'), s.id, s.account_id, a.tenant_id, a.home_region,
       s.source, s.source_region, s.dataset, s.status, s.version, s.generated_at,
       s.last_synced_at, s.expires_at, s.error_code, s.data, s.updated_at
FROM account_snapshots s JOIN accounts a ON a.id = s.account_id
WHERE s.status IN ('fresh', 'stale', 'unavailable');

UPDATE replication_change_clock
SET next_sequence = COALESCE((SELECT max(sequence) FROM account_snapshot_replication_changes), 0);

ALTER TABLE replication_cursors
    ADD COLUMN last_replication_sequence BIGINT NOT NULL DEFAULT 0 CHECK (last_replication_sequence >= 0);
