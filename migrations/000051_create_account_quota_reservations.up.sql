ALTER TABLE accounts ADD CONSTRAINT accounts_tenant_id_id_key UNIQUE (tenant_id, id);

CREATE TABLE account_quota_reservations (
    account_id UUID PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    reservation_id TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT account_quota_tenant_nonempty CHECK (btrim(tenant_id) <> ''),
    CONSTRAINT account_quota_reservation_valid CHECK (btrim(reservation_id) <> '' AND char_length(reservation_id) <= 256),
    CONSTRAINT account_quota_status_valid CHECK (status IN ('pending', 'committed')),
    CONSTRAINT account_quota_account_fk FOREIGN KEY (tenant_id, account_id)
        REFERENCES accounts (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_account_quota_pending
    ON account_quota_reservations (tenant_id, status, updated_at ASC)
    WHERE status = 'pending';
