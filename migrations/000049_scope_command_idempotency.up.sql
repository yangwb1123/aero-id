ALTER TABLE operations
    DROP CONSTRAINT IF EXISTS uq_operation_actor_key;

ALTER TABLE operations
    ADD CONSTRAINT uq_operation_actor_tenant_key UNIQUE (actor_uid, tenant_id, idempotency_key);

ALTER TABLE sync_jobs
    DROP CONSTRAINT IF EXISTS uq_sync_job_actor_key;

ALTER TABLE sync_jobs
    ADD CONSTRAINT uq_sync_job_actor_tenant_key UNIQUE (actor_uid, tenant_id, idempotency_key);

CREATE INDEX IF NOT EXISTS idx_operations_actor_tenant
    ON operations (actor_uid, tenant_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_actor_tenant
    ON sync_jobs (actor_uid, tenant_id, created_at DESC, id DESC);
