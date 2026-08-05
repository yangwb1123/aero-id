DROP INDEX IF EXISTS idx_operation_steps_compensation_claim;

ALTER TABLE operation_attempts
    DROP CONSTRAINT IF EXISTS uq_operation_attempt_kind;

ALTER TABLE operation_attempts
    ADD CONSTRAINT uq_operation_attempt UNIQUE (step_id, attempt);

ALTER TABLE operation_attempts
    DROP COLUMN IF EXISTS attempt_kind;

ALTER TABLE operation_steps
    DROP COLUMN IF EXISTS compensation_status,
    DROP COLUMN IF EXISTS compensation_attempt,
    DROP COLUMN IF EXISTS compensation_lease_owner,
    DROP COLUMN IF EXISTS compensation_lease_until,
    DROP COLUMN IF EXISTS compensation_source_operation_id,
    DROP COLUMN IF EXISTS compensation_result,
    DROP COLUMN IF EXISTS compensation_error_code,
    DROP COLUMN IF EXISTS compensation_error_message,
    DROP COLUMN IF EXISTS compensation_updated_at;
