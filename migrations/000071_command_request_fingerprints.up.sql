-- Historical requests cannot be backfilled with an authentic original client.
-- Empty fingerprints remain readable/reconcilable but are not reusable receipts.
ALTER TABLE operations
    ADD COLUMN request_fingerprint varchar(67) NOT NULL DEFAULT '',
    ADD CONSTRAINT operations_request_fingerprint_format
        CHECK (request_fingerprint = '' OR request_fingerprint ~ '^v1:[0-9a-f]{64}$');

ALTER TABLE sync_jobs
    ADD COLUMN request_fingerprint varchar(67) NOT NULL DEFAULT '',
    ADD CONSTRAINT sync_jobs_request_fingerprint_format
        CHECK (request_fingerprint = '' OR request_fingerprint ~ '^v1:[0-9a-f]{64}$');
