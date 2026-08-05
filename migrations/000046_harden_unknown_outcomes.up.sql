ALTER TABLE operation_steps
    ADD COLUMN IF NOT EXISTS lease_kind text NOT NULL DEFAULT 'forward';

ALTER TABLE sync_jobs
    ADD COLUMN IF NOT EXISTS lease_kind text NOT NULL DEFAULT 'execute';

ALTER TABLE event_receipts
    ADD COLUMN IF NOT EXISTS attempt int NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS lease_owner text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS lease_until timestamptz;

CREATE INDEX IF NOT EXISTS idx_operation_steps_lease_kind
    ON operation_steps (operation_id, status, lease_kind, lease_until);
CREATE INDEX IF NOT EXISTS idx_sync_jobs_lease_kind
    ON sync_jobs (status, lease_kind, lease_until, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_event_receipts_processing
    ON event_receipts (status, lease_until, updated_at ASC)
    WHERE status = 'processing';

UPDATE operation_steps SET lease_kind = 'forward' WHERE lease_kind = '';
UPDATE sync_jobs SET lease_kind = 'execute' WHERE lease_kind = '';
