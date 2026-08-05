DROP INDEX IF EXISTS idx_accounts_tenant_region_status;
DROP INDEX IF EXISTS idx_accounts_tenant_uid;
ALTER TABLE account_identities DROP CONSTRAINT IF EXISTS account_identities_tenant_account_issuer_subject_key;
ALTER TABLE account_identities DROP CONSTRAINT IF EXISTS account_identities_tenant_issuer_subject_key;
ALTER TABLE account_identities ADD CONSTRAINT account_identities_issuer_subject_key UNIQUE (issuer, subject);
ALTER TABLE account_identities ADD CONSTRAINT account_identities_account_id_issuer_subject_key UNIQUE (account_id, issuer, subject);
ALTER TABLE account_identities DROP COLUMN tenant_id;
ALTER TABLE accounts DROP COLUMN tenant_id;
