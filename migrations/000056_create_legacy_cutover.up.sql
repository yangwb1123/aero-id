-- P8-04/P8-05: legacy cutover registry.
--
-- Batch cutover of the legacy files/messages/comments compatibility surface is
-- recorded per tenant so the facade can answer explicitly ("moved to the new
-- authority") instead of failing generically, and rollback is one admin call
-- away. Default is 'legacy' — no behavior changes until an admin flips a
-- tenant (ADR-011 keeps the surface live until then).
CREATE TABLE legacy_cutover (
    tenant_id  TEXT PRIMARY KEY,
    status     TEXT NOT NULL DEFAULT 'legacy', -- legacy | shadow | cut_over | rolled_back
    decided_at TIMESTAMPTZ NOT NULL,
    decided_by TEXT NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT legacy_cutover_status_check CHECK (status IN ('legacy', 'shadow', 'cut_over', 'rolled_back'))
);
