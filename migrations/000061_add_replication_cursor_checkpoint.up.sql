-- Projection replication must checkpoint the source row's update timestamp as
-- well as its UUID. account_snapshots are upserted in place, so an updated
-- projection keeps the same id and would otherwise be skipped forever.
ALTER TABLE replication_cursors
    ADD COLUMN last_updated_at TIMESTAMPTZ;

UPDATE replication_cursors AS cursor
SET last_updated_at = snapshot.updated_at
FROM account_snapshots AS snapshot
WHERE cursor.last_snapshot_id = snapshot.id;

CREATE INDEX idx_account_snapshots_replication_checkpoint
    ON account_snapshots (updated_at ASC, id ASC)
    WHERE status IN ('fresh', 'stale', 'unavailable');
