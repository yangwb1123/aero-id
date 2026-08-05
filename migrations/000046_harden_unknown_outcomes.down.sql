DROP INDEX IF EXISTS idx_event_receipts_processing;
DROP INDEX IF EXISTS idx_sync_jobs_lease_kind;
DROP INDEX IF EXISTS idx_operation_steps_lease_kind;

ALTER TABLE event_receipts
    DROP COLUMN IF EXISTS lease_until,
    DROP COLUMN IF EXISTS lease_owner,
    DROP COLUMN IF EXISTS attempt;
ALTER TABLE sync_jobs DROP COLUMN IF EXISTS lease_kind;
ALTER TABLE operation_steps DROP COLUMN IF EXISTS lease_kind;
