DROP TABLE IF EXISTS replication_terminal_failures;
ALTER TABLE replication_cursors DROP COLUMN IF EXISTS lease_generation;
