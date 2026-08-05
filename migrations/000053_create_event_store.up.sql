-- P3-07: durable canonical event store. Source events that pass ingest are
-- appended atomically with their receipt/projection update so projection
-- replay and forensic reconstruction are possible without re-fetching sources.
-- Payloads are stored redacted (event.RedactPayload) — never raw bodies.
CREATE TABLE event_store (
    id                 UUID PRIMARY KEY,
    event_id           UUID NOT NULL,
    tenant_id          TEXT NOT NULL,
    source             TEXT NOT NULL,
    source_region      TEXT NOT NULL,
    event_type         TEXT NOT NULL,
    schema_version     INT NOT NULL,
    aggregate_type     TEXT NOT NULL,
    aggregate_id       TEXT NOT NULL,
    aggregate_version  BIGINT NOT NULL,
    actor_uid          TEXT NOT NULL DEFAULT '',
    operation_id       UUID,
    correlation_id     UUID,
    causation_id       UUID,
    trace_id           TEXT NOT NULL DEFAULT '',
    occurred_at        TIMESTAMPTZ NOT NULL,
    received_at        TIMESTAMPTZ NOT NULL,
    payload            JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (tenant_id, source, event_id)
);

CREATE INDEX idx_event_store_source_agg
    ON event_store (tenant_id, source, aggregate_type, aggregate_id, aggregate_version);
CREATE INDEX idx_event_store_occurred
    ON event_store (tenant_id, source, occurred_at, id DESC);
CREATE INDEX idx_event_store_cursor
    ON event_store (tenant_id, source, id DESC);
