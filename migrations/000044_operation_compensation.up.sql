ALTER TABLE operation_steps
    ADD COLUMN IF NOT EXISTS compensation_status text NOT NULL DEFAULT 'pending',
    ADD COLUMN IF NOT EXISTS compensation_attempt int NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS compensation_lease_owner text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS compensation_lease_until timestamptz,
    ADD COLUMN IF NOT EXISTS compensation_source_operation_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS compensation_result jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS compensation_error_code text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS compensation_error_message text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS compensation_updated_at timestamptz;

ALTER TABLE operation_attempts
    ADD COLUMN IF NOT EXISTS attempt_kind text NOT NULL DEFAULT 'forward';

ALTER TABLE operation_attempts
    DROP CONSTRAINT IF EXISTS uq_operation_attempt;

ALTER TABLE operation_attempts
    ADD CONSTRAINT uq_operation_attempt_kind UNIQUE (step_id, attempt, attempt_kind);

CREATE INDEX IF NOT EXISTS idx_operation_steps_compensation_claim
    ON operation_steps (operation_id, compensation_status, compensation_lease_until, updated_at ASC)
    WHERE compensation_action <> '';
