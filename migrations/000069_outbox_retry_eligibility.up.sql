-- Persist retry eligibility so ClaimBatch can exclude future retries in the
-- database. The column is added nullable to allow an in-place backfill.
ALTER TABLE outbox_events
    ADD COLUMN next_attempt_at TIMESTAMPTZ;

-- Preserve the relay's existing schedule exactly: the first failure waits 5s,
-- each later retry doubles the delay, and retry 11 onward is capped at 1h.
-- Events without retry history retain their original creation-time eligibility.
UPDATE outbox_events
SET next_attempt_at = CASE
    WHEN COALESCE(retry_count, 0) <= 0 OR last_retry_at IS NULL THEN created_at
    WHEN retry_count >= 11 THEN last_retry_at + interval '1 hour'
    WHEN retry_count = 1 THEN last_retry_at + interval '5 seconds'
    WHEN retry_count = 2 THEN last_retry_at + interval '10 seconds'
    WHEN retry_count = 3 THEN last_retry_at + interval '20 seconds'
    WHEN retry_count = 4 THEN last_retry_at + interval '40 seconds'
    WHEN retry_count = 5 THEN last_retry_at + interval '80 seconds'
    WHEN retry_count = 6 THEN last_retry_at + interval '160 seconds'
    WHEN retry_count = 7 THEN last_retry_at + interval '320 seconds'
    WHEN retry_count = 8 THEN last_retry_at + interval '640 seconds'
    WHEN retry_count = 9 THEN last_retry_at + interval '1280 seconds'
    WHEN retry_count = 10 THEN last_retry_at + interval '2560 seconds'
    ELSE created_at
END;

ALTER TABLE outbox_events
    ALTER COLUMN next_attempt_at SET DEFAULT now(),
    ALTER COLUMN next_attempt_at SET NOT NULL;

DROP INDEX IF EXISTS idx_outbox_unpublished;
CREATE INDEX idx_outbox_unpublished
    ON outbox_events(next_attempt_at, created_at, id)
    WHERE published_at IS NULL AND NOT dead_letter;
