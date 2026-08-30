ALTER TABLE audit_publications
    ADD COLUMN IF NOT EXISTS lease_generation bigint NOT NULL DEFAULT 0;

ALTER TABLE callback_deliveries
    ADD COLUMN IF NOT EXISTS lease_generation bigint NOT NULL DEFAULT 0;
