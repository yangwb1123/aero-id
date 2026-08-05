-- 000028_add_file_status.down.sql
DROP INDEX IF EXISTS idx_files_status;
ALTER TABLE files DROP COLUMN IF EXISTS status;
