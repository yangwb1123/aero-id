DROP INDEX IF EXISTS idx_sync_jobs_actor_tenant;
DROP INDEX IF EXISTS idx_operations_actor_tenant;

ALTER TABLE sync_jobs
    DROP CONSTRAINT IF EXISTS uq_sync_job_actor_tenant_key;

ALTER TABLE sync_jobs
    ADD CONSTRAINT uq_sync_job_actor_key UNIQUE (actor_uid, idempotency_key);

ALTER TABLE operations
    DROP CONSTRAINT IF EXISTS uq_operation_actor_tenant_key;

ALTER TABLE operations
    ADD CONSTRAINT uq_operation_actor_key UNIQUE (actor_uid, idempotency_key);
