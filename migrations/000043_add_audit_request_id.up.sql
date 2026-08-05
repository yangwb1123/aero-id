ALTER TABLE audit_ledger ADD COLUMN IF NOT EXISTS request_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_audit_ledger_request_id ON audit_ledger (request_id) WHERE request_id <> '';
