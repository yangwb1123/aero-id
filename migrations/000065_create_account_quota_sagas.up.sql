CREATE TABLE account_quota_sagas (
    id UUID PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    issuer TEXT NOT NULL,
    subject TEXT NOT NULL,
    reservation_id TEXT NOT NULL UNIQUE,
    account_id UUID,
    state TEXT NOT NULL,
    release_attempt INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_until TIMESTAMPTZ,
    lease_generation BIGINT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT account_quota_saga_identity_unique UNIQUE (tenant_id, issuer, subject),
    CONSTRAINT account_quota_saga_state_valid CHECK (state IN ('reserving', 'persisted', 'committed', 'release_pending', 'released')),
    CONSTRAINT account_quota_saga_tenant_nonempty CHECK (btrim(tenant_id) <> ''),
    CONSTRAINT account_quota_saga_identity_nonempty CHECK (btrim(issuer) <> '' AND btrim(subject) <> ''),
    CONSTRAINT account_quota_saga_reservation_valid CHECK (btrim(reservation_id) <> '' AND char_length(reservation_id) <= 256)
);
CREATE INDEX idx_account_quota_sagas_work ON account_quota_sagas (state, next_attempt_at, updated_at);
