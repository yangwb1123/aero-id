-- P6-03: projection replication cursors. The regional replication worker scans
-- approved projections of locally-homed accounts and publishes them as
-- canonical projection.snapshot events; the cursor is durable so a worker crash
-- resumes exactly where it stopped (at-least-once; consumers dedup by receipt).
CREATE TABLE replication_cursors (
    id               UUID PRIMARY KEY,
    source_region    TEXT NOT NULL,
    target           TEXT NOT NULL,   -- transport target identity, e.g. "nats:aero.events.canonical" or "http:<endpoint>"
    last_snapshot_id UUID,
    published        BIGINT NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL,
    UNIQUE (source_region, target)
);

CREATE INDEX idx_replication_cursors_region ON replication_cursors (source_region);
