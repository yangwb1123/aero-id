-- P4-07: legal hold registry.
--
-- A legal hold suspends retention/cleanup for evidence-preservation orders:
--   - scope_type 'actor'  -> legacy audit_events rows of that actor are exempt
--                            from the retention purge (audit_events has no
--                            tenant column; actor_uid is the join key);
--   - scope_type 'tenant' -> durable audit ledger of the tenant is marked held
--                            (exports carry the flag; future archival/cleanup
--                            policies must consult this table first);
--   - scope_type 'account'-> per-account holds for finer-grained orders.
CREATE TABLE audit_holds (
    id         UUID PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    scope_type TEXT NOT NULL,
    scope_id   TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ,
    UNIQUE (tenant_id, scope_type, scope_id),
    CONSTRAINT audit_holds_scope_type_check CHECK (scope_type IN ('tenant', 'account', 'actor'))
);

-- Partial index covers indefinite holds only; expiry is filtered in SQL
-- (now() is not IMMUTABLE and cannot appear in an index predicate).
CREATE INDEX idx_audit_holds_indefinite ON audit_holds (scope_type, scope_id) WHERE expires_at IS NULL;
