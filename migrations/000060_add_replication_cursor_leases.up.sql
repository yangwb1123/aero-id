-- P6-03 hardening: multiple regional API/worker replicas may start the
-- replication loop. A durable owner/expiry lease prevents concurrent cursor
-- advancement while preserving at-least-once retries after a crash.
ALTER TABLE replication_cursors
    ADD COLUMN lease_owner TEXT NOT NULL DEFAULT '',
    ADD COLUMN lease_until TIMESTAMPTZ;

CREATE INDEX idx_replication_cursors_lease
    ON replication_cursors (source_region, target, lease_until);
