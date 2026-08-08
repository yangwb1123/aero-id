-- Dead-letter purge cutoff index support (remediation T4/§3.2): the purge
-- scans (PurgeDeadLetterPublications / PurgeDeadLetterDeliveries) filter on
-- (status, updated_at) with id as the keyset tie-breaker. The 000045 due/lease
-- indexes carry no updated_at, so the steady-state tick (retention > 0,
-- backlog drained) walked the whole dead_letter index slice with a heap fetch
-- per entry, every tick. These indexes turn the cutoff into a range bound:
-- each tick examines only qualifying entries (<= batch limit).
CREATE INDEX IF NOT EXISTS idx_audit_publications_purge
    ON audit_publications (status, updated_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_callback_deliveries_purge
    ON callback_deliveries (status, updated_at ASC, id ASC);
