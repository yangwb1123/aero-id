DROP INDEX IF EXISTS idx_audit_ledger_tenant_query;
ALTER TABLE audit_ledger DROP CONSTRAINT IF EXISTS uq_audit_ledger_tenant_event;
ALTER TABLE audit_ledger ADD CONSTRAINT uq_audit_ledger_event UNIQUE (source, event_id);
DROP INDEX IF EXISTS idx_event_receipts_aggregate;
ALTER TABLE event_receipts DROP CONSTRAINT IF EXISTS uq_event_receipt_tenant_source_event;
ALTER TABLE event_receipts ADD CONSTRAINT uq_event_receipt_source_event UNIQUE (source, event_id);
ALTER TABLE event_receipts DROP CONSTRAINT IF EXISTS event_receipts_tenant_nonempty;
ALTER TABLE event_receipts DROP COLUMN tenant_id;
CREATE INDEX idx_event_receipts_aggregate
    ON event_receipts (source, aggregate_type, aggregate_id, aggregate_version DESC);
