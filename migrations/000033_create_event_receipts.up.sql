CREATE TABLE IF NOT EXISTS event_receipts (
    id                uuid PRIMARY KEY,
    source            text NOT NULL,
    event_id          uuid NOT NULL,
    source_region     text NOT NULL,
    aggregate_type    text NOT NULL,
    aggregate_id      text NOT NULL,
    aggregate_version bigint NOT NULL DEFAULT 0,
    status            text NOT NULL,
    error_code        text NOT NULL DEFAULT '',
    payload           jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_event_receipt_source_event UNIQUE (source, event_id)
);

CREATE INDEX IF NOT EXISTS idx_event_receipts_aggregate
    ON event_receipts (source, aggregate_type, aggregate_id, aggregate_version DESC);
CREATE INDEX IF NOT EXISTS idx_event_receipts_status
    ON event_receipts (status, updated_at DESC);
