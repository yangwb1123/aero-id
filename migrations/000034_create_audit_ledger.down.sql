DROP TRIGGER IF EXISTS audit_ledger_append_only ON audit_ledger;
DROP FUNCTION IF EXISTS prevent_audit_ledger_mutation();
DROP INDEX IF EXISTS idx_audit_ledger_partition;
DROP INDEX IF EXISTS idx_audit_ledger_operation;
DROP INDEX IF EXISTS idx_audit_ledger_actor;
DROP INDEX IF EXISTS idx_audit_ledger_query;
DROP TABLE IF EXISTS audit_ledger;
