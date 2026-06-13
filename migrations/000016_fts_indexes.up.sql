-- Add generated tsvector columns and GIN indexes for full-text search.
-- CONCURRENTLY is valid outside a transaction block (migrate tool runs each
-- statement individually). IF NOT EXISTS guards make this idempotent.

ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_fts
    ON messages USING GIN(search_vector) WHERE deleted_at IS NULL;

ALTER TABLE comments ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_comments_fts
    ON comments USING GIN(search_vector) WHERE deleted_at IS NULL;

ALTER TABLE files ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(name, ''))) STORED;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_files_fts
    ON files USING GIN(search_vector) WHERE deleted_at IS NULL;
