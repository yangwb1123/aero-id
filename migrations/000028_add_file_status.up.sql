-- 000028_add_file_status.up.sql
-- Adds a status column to the files table for post-upload pipeline tracking.
-- All existing files are backfilled as Ready since they predate the pipeline.
-- The status column is NOT NULL with a default of 1 (Pending) so new uploads
-- automatically enter the pipeline; existing rows get backfilled explicitly.

ALTER TABLE files ADD COLUMN IF NOT EXISTS status smallint NOT NULL DEFAULT 1;
UPDATE files SET status = 3 WHERE status = 1; -- backfill existing rows to Ready (3)

-- Partial index: pipeline workers query for non-terminal files.
CREATE INDEX IF NOT EXISTS idx_files_status ON files (status) WHERE status IN (1, 2);
