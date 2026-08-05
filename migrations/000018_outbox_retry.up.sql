-- Add retry infrastructure to outbox_events. Events that fail handler dispatch
-- accumulate retry_count and last_retry_at for exponential backoff; when
-- retry_count reaches the relay's max_retries threshold the event is marked
-- dead_letter so it stops being retried (and can be inspected later).
ALTER TABLE outbox_events
    ADD COLUMN retry_count   INT NOT NULL DEFAULT 0,
    ADD COLUMN last_retry_at TIMESTAMPTZ,
    ADD COLUMN dead_letter   BOOLEAN NOT NULL DEFAULT FALSE;

-- Recreate the partial index to exclude dead-letter events from the drain poll.
DROP INDEX IF EXISTS idx_outbox_unpublished;
CREATE INDEX idx_outbox_unpublished
    ON outbox_events(created_at, id)
    WHERE published_at IS NULL AND NOT dead_letter;
