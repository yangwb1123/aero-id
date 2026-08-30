-- Restore the exact outbox index shape used immediately before 000069.
DROP INDEX IF EXISTS idx_outbox_unpublished;
ALTER TABLE outbox_events
    DROP COLUMN next_attempt_at;
CREATE INDEX idx_outbox_unpublished
    ON outbox_events(created_at, id)
    WHERE published_at IS NULL AND NOT dead_letter;
