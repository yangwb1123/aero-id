ALTER TABLE callback_deliveries DROP COLUMN IF EXISTS lease_generation;
ALTER TABLE audit_publications DROP COLUMN IF EXISTS lease_generation;
