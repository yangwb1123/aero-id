UPDATE account_quota_reservations SET status = 'pending' WHERE status = 'expired';
UPDATE account_quota_sagas SET state = 'persisted' WHERE state = 'expired';
ALTER TABLE account_quota_reservations DROP CONSTRAINT account_quota_status_valid;
ALTER TABLE account_quota_reservations ADD CONSTRAINT account_quota_status_valid CHECK (status IN ('pending', 'committed'));
ALTER TABLE account_quota_sagas DROP CONSTRAINT account_quota_saga_state_valid;
ALTER TABLE account_quota_sagas ADD CONSTRAINT account_quota_saga_state_valid CHECK (state IN ('reserving', 'persisted', 'committed', 'release_pending', 'released'));
