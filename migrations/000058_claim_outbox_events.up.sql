-- Durable claim columns for multi-instance outbox dispatch (REQ-1/REQ-5).
-- NULL claim_token/claimed_at = unclaimed. The partial index is NOT changed:
-- a now()-relative index predicate would eject claimed rows at write time
-- and never re-admit them when the lease expires (see design D5).
ALTER TABLE outbox_events
    ADD COLUMN claim_token UUID,
    ADD COLUMN claimed_at TIMESTAMPTZ;
