CREATE TABLE IF NOT EXISTS sync_jobs (
    id              uuid PRIMARY KEY,
    account_id      uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    actor_uid       text NOT NULL,
    job_type        text NOT NULL,
    status          text NOT NULL,
    idempotency_key text NOT NULL,
    request         jsonb NOT NULL DEFAULT '{}'::jsonb,
    result          jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code      text NOT NULL DEFAULT '',
    error_message   text NOT NULL DEFAULT '',
    attempt         int NOT NULL DEFAULT 0,
    lease_owner     text NOT NULL DEFAULT '',
    lease_until     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    completed_at    timestamptz,
    CONSTRAINT uq_sync_job_actor_key UNIQUE (actor_uid, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_sync_jobs_claim ON sync_jobs (status, lease_until, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_sync_jobs_account ON sync_jobs (account_id, created_at DESC, id DESC);
