DROP INDEX IF EXISTS idx_files_category;
ALTER TABLE files DROP COLUMN IF EXISTS category;
