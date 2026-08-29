-- Bind each source event key to a digest of its complete canonical envelope.
-- Empty content_hash is an explicit legacy/unbound marker: old rows lack
-- enough metadata to prove an exact replay, so application code fails closed
-- with event.integrity_conflict instead of guessing from redacted payload.
ALTER TABLE event_receipts
    ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT '';

ALTER TABLE event_store
    ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT '';
