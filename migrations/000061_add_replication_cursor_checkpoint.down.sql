DROP INDEX IF EXISTS idx_account_snapshots_replication_checkpoint;

ALTER TABLE replication_cursors
    DROP COLUMN IF EXISTS last_updated_at;
