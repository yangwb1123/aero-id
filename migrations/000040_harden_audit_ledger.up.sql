ALTER TABLE audit_ledger
    ADD COLUMN IF NOT EXISTS hash_version int NOT NULL DEFAULT 0;
