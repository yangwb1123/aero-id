-- Revert retry columns. Drop the updated partial index and recreate the original.
DROP INDEX IF EXISTS idx_outbox_unpublished;
ALTER TABLE outbox_events
    DROP COLUMN retry_count,
    DROP COLUMN last_retry_at,
    DROP COLUMN dead_letter;
CREATE INDEX idx_outbox_unpublished ON outbox_events(created_at, id) WHERE published_at IS NULL;
