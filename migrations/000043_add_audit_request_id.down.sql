DROP INDEX IF EXISTS idx_audit_ledger_request_id;
ALTER TABLE audit_ledger DROP COLUMN IF EXISTS request_id;
