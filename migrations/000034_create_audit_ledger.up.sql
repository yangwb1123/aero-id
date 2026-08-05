CREATE TABLE IF NOT EXISTS audit_ledger (
    id                 uuid PRIMARY KEY,
    chain_sequence     bigint NOT NULL,
    source             text NOT NULL,
    source_region      text NOT NULL,
    partition_key      text NOT NULL,
    event_id           uuid,
    operation_id       uuid,
    correlation_id     uuid,
    causation_id       uuid,
    trace_id           text NOT NULL DEFAULT '',
    actor_uid          text NOT NULL DEFAULT '',
    account_id         uuid,
    tenant_id          text NOT NULL DEFAULT '',
    action             text NOT NULL,
    resource_type      text NOT NULL DEFAULT '',
    resource_id        text NOT NULL DEFAULT '',
    occurred_at        timestamptz NOT NULL,
    payload            jsonb NOT NULL DEFAULT '{}'::jsonb,
    payload_hash       text NOT NULL,
    previous_hash      text NOT NULL DEFAULT '',
    record_hash        text NOT NULL,
    signature_version  int NOT NULL DEFAULT 0,
    signature          text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_audit_ledger_chain_seq UNIQUE (source, partition_key, chain_sequence),
    CONSTRAINT uq_audit_ledger_event UNIQUE (source, event_id)
);

CREATE INDEX IF NOT EXISTS idx_audit_ledger_query
    ON audit_ledger (account_id, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_ledger_actor
    ON audit_ledger (actor_uid, occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_ledger_operation
    ON audit_ledger (operation_id, occurred_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_audit_ledger_partition
    ON audit_ledger (source, partition_key, chain_sequence ASC);

CREATE OR REPLACE FUNCTION prevent_audit_ledger_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_ledger is append-only';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS audit_ledger_append_only ON audit_ledger;
CREATE TRIGGER audit_ledger_append_only
    BEFORE UPDATE OR DELETE ON audit_ledger
    FOR EACH ROW EXECUTE FUNCTION prevent_audit_ledger_mutation();
