CREATE TABLE IF NOT EXISTS operations (
    id               uuid PRIMARY KEY,
    account_id       uuid REFERENCES accounts(id) ON DELETE SET NULL,
    actor_uid        text NOT NULL,
    tenant_id        text NOT NULL DEFAULT '',
    home_region      text NOT NULL,
    operation_type   text NOT NULL,
    status           text NOT NULL,
    idempotency_key  text NOT NULL,
    request_payload  jsonb NOT NULL DEFAULT '{}'::jsonb,
    result           jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code       text NOT NULL DEFAULT '',
    error_message    text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    completed_at     timestamptz,
    CONSTRAINT uq_operation_actor_key UNIQUE (actor_uid, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_operations_account ON operations (account_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_operations_status ON operations (status, updated_at ASC);

CREATE TABLE IF NOT EXISTS operation_steps (
    id                  uuid PRIMARY KEY,
    operation_id        uuid NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    step_key            text NOT NULL,
    source              text NOT NULL,
    action              text NOT NULL,
    status              text NOT NULL,
    attempt             int NOT NULL DEFAULT 0,
    lease_owner         text NOT NULL DEFAULT '',
    lease_until         timestamptz,
    source_operation_id text NOT NULL DEFAULT '',
    request_payload     jsonb NOT NULL DEFAULT '{}'::jsonb,
    result              jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code          text NOT NULL DEFAULT '',
    error_message       text NOT NULL DEFAULT '',
    compensation_action text NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_operation_step_key UNIQUE (operation_id, step_key)
);

CREATE INDEX IF NOT EXISTS idx_operation_steps_claim ON operation_steps (status, lease_until, updated_at ASC);
CREATE INDEX IF NOT EXISTS idx_operation_steps_operation ON operation_steps (operation_id, created_at ASC, id ASC);

CREATE TABLE IF NOT EXISTS operation_attempts (
    id                  uuid PRIMARY KEY,
    operation_id        uuid NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    step_id             uuid NOT NULL REFERENCES operation_steps(id) ON DELETE CASCADE,
    attempt             int NOT NULL,
    status              text NOT NULL,
    source_operation_id text NOT NULL DEFAULT '',
    error_code          text NOT NULL DEFAULT '',
    started_at          timestamptz NOT NULL DEFAULT now(),
    finished_at         timestamptz,
    CONSTRAINT uq_operation_attempt UNIQUE (step_id, attempt)
);
