ALTER TABLE event_receipts ADD COLUMN tenant_id TEXT;
UPDATE event_receipts SET tenant_id = 'legacy' WHERE tenant_id IS NULL;
ALTER TABLE event_receipts ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE event_receipts ADD CONSTRAINT event_receipts_tenant_nonempty CHECK (btrim(tenant_id) <> '');

ALTER TABLE event_receipts DROP CONSTRAINT uq_event_receipt_source_event;
ALTER TABLE event_receipts ADD CONSTRAINT uq_event_receipt_tenant_source_event
    UNIQUE (tenant_id, source, event_id);

DROP INDEX IF EXISTS idx_event_receipts_aggregate;
CREATE INDEX idx_event_receipts_aggregate
    ON event_receipts (tenant_id, source, aggregate_type, aggregate_id, aggregate_version DESC);

ALTER TABLE audit_ledger DROP CONSTRAINT uq_audit_ledger_event;
ALTER TABLE audit_ledger ADD CONSTRAINT uq_audit_ledger_tenant_event
    UNIQUE (tenant_id, source, event_id);
CREATE INDEX idx_audit_ledger_tenant_query
    ON audit_ledger (tenant_id, occurred_at DESC, id DESC);
