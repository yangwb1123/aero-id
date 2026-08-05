DROP TABLE IF EXISTS account_quota_reservations;
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_tenant_id_id_key;
