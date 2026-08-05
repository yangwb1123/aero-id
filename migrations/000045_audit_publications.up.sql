CREATE TABLE IF NOT EXISTS audit_publications (
    id              uuid PRIMARY KEY,
    fact_id         uuid NOT NULL REFERENCES audit_ledger(id) ON DELETE CASCADE,
    status          text NOT NULL DEFAULT 'pending',
    attempt         int NOT NULL DEFAULT 0,
    lease_owner     text NOT NULL DEFAULT '',
    lease_until     timestamptz,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    error_code      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_audit_publication_fact UNIQUE (fact_id)
);

CREATE INDEX IF NOT EXISTS idx_audit_publications_due
    ON audit_publications (status, next_attempt_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_audit_publications_lease
    ON audit_publications (status, lease_until, next_attempt_at ASC);
