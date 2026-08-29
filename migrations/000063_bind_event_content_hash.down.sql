ALTER TABLE event_store DROP COLUMN IF EXISTS content_hash;
ALTER TABLE event_receipts DROP COLUMN IF EXISTS content_hash;
