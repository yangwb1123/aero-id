DROP INDEX IF EXISTS idx_messages_fts;
ALTER TABLE messages DROP COLUMN IF EXISTS search_vector;

DROP INDEX IF EXISTS idx_comments_fts;
ALTER TABLE comments DROP COLUMN IF EXISTS search_vector;

DROP INDEX IF EXISTS idx_files_fts;
ALTER TABLE files DROP COLUMN IF EXISTS search_vector;
