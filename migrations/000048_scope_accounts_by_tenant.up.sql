ALTER TABLE accounts ADD COLUMN tenant_id TEXT;
ALTER TABLE account_identities ADD COLUMN tenant_id TEXT;

-- Existing installations must replace this sentinel from an authoritative
-- Snaplink export before enabling tenant-scoped account APIs.
UPDATE accounts SET tenant_id = 'legacy' WHERE tenant_id IS NULL;
UPDATE account_identities i
SET tenant_id = a.tenant_id
FROM accounts a
WHERE i.account_id = a.id AND i.tenant_id IS NULL;

ALTER TABLE accounts ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE account_identities ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE accounts ADD CONSTRAINT accounts_tenant_nonempty CHECK (btrim(tenant_id) <> '');
ALTER TABLE account_identities ADD CONSTRAINT account_identities_tenant_nonempty CHECK (btrim(tenant_id) <> '');

ALTER TABLE account_identities DROP CONSTRAINT account_identities_issuer_subject_key;
ALTER TABLE account_identities DROP CONSTRAINT account_identities_account_id_issuer_subject_key;
ALTER TABLE account_identities ADD CONSTRAINT account_identities_tenant_issuer_subject_key
    UNIQUE (tenant_id, issuer, subject);
ALTER TABLE account_identities ADD CONSTRAINT account_identities_tenant_account_issuer_subject_key
    UNIQUE (tenant_id, account_id, issuer, subject);

CREATE INDEX idx_accounts_tenant_uid ON accounts (tenant_id, canonical_uid);
CREATE INDEX idx_accounts_tenant_region_status ON accounts (tenant_id, home_region, status);
