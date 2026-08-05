ALTER TABLE callback_deliveries
    ADD COLUMN IF NOT EXISTS lease_owner text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS lease_until timestamptz;

CREATE INDEX IF NOT EXISTS idx_callback_deliveries_lease
    ON callback_deliveries (status, lease_until, next_attempt_at ASC);
