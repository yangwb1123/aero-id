ALTER TABLE replication_cursors
    DROP COLUMN IF EXISTS last_replication_sequence;
DROP TABLE IF EXISTS account_snapshot_replication_changes;
DROP TABLE IF EXISTS replication_change_clock;
DROP SEQUENCE IF EXISTS replication_change_sequence;
