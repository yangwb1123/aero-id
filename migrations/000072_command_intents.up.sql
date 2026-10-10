CREATE TABLE command_intents (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts(id),
    tenant_id varchar(200) NOT NULL,
    actor_uid varchar(200) NOT NULL,
    issuer text NOT NULL,
    subject text NOT NULL,
    client_id varchar(200) NOT NULL,
    idempotency_key varchar(200) NOT NULL,
    request_fingerprint varchar(67) NOT NULL CHECK (request_fingerprint ~ '^v1:[0-9a-f]{64}$'),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    status varchar(16) NOT NULL DEFAULT 'prepared' CHECK (status IN ('prepared', 'executed')),
    document jsonb NOT NULL CHECK (jsonb_typeof(document) = 'object'),
    receipt_kind varchar(16) NOT NULL DEFAULT '' CHECK (receipt_kind IN ('', 'sync_job', 'operation')),
    receipt_id uuid,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    CHECK (expires_at > created_at),
    CHECK ((status = 'prepared' AND receipt_id IS NULL AND receipt_kind = '') OR
           (status = 'executed' AND receipt_id IS NOT NULL AND receipt_kind <> '')),
    UNIQUE (tenant_id, issuer, subject, client_id, idempotency_key)
);
CREATE INDEX idx_command_intents_account ON command_intents (tenant_id, account_id, id DESC);
