-- Add generated tsvector columns and GIN indexes for full-text search.
-- NOTE: no CONCURRENTLY. The migrator (internal/pkg/migrate, applyTx) runs each
-- migration inside a single transaction, and CREATE INDEX CONCURRENTLY is illegal
-- inside a transaction block (SQLSTATE 25001). Plain CREATE INDEX runs in-transaction
-- like every other index in this schema; it briefly holds a write lock while the GIN
-- index builds, acceptable for this deployment. IF NOT EXISTS keeps it idempotent.

ALTER TABLE messages ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED;
CREATE INDEX IF NOT EXISTS idx_messages_fts
    ON messages USING GIN(search_vector) WHERE deleted_at IS NULL;

ALTER TABLE comments ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED;
CREATE INDEX IF NOT EXISTS idx_comments_fts
    ON comments USING GIN(search_vector) WHERE deleted_at IS NULL;

ALTER TABLE files ADD COLUMN IF NOT EXISTS search_vector tsvector
    GENERATED ALWAYS AS (to_tsvector('simple', coalesce(name, ''))) STORED;
CREATE INDEX IF NOT EXISTS idx_files_fts
    ON files USING GIN(search_vector) WHERE deleted_at IS NULL;
