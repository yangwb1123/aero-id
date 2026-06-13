CREATE TABLE IF NOT EXISTS audit_events (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    actor_uid   TEXT NOT NULL,
    action      TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    subject_id   TEXT NOT NULL,
    target_uid   TEXT,
    meta         JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_audit_events_actor   ON audit_events(actor_uid, created_at DESC);
CREATE INDEX idx_audit_events_subject ON audit_events(subject_type, subject_id, created_at DESC);
