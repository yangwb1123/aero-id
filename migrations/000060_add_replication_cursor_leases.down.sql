DROP INDEX IF EXISTS idx_replication_cursors_lease;

ALTER TABLE replication_cursors
    DROP COLUMN IF EXISTS lease_owner,
    DROP COLUMN IF EXISTS lease_until;
