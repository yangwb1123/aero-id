DROP INDEX IF EXISTS idx_callback_deliveries_lease;
ALTER TABLE callback_deliveries
    DROP COLUMN IF EXISTS lease_owner,
    DROP COLUMN IF EXISTS lease_until;
