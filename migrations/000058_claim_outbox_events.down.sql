ALTER TABLE outbox_events
    DROP COLUMN claim_token,
    DROP COLUMN claimed_at;
