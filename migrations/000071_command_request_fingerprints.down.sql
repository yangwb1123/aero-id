ALTER TABLE sync_jobs DROP CONSTRAINT sync_jobs_request_fingerprint_format,
    DROP COLUMN request_fingerprint;
ALTER TABLE operations DROP CONSTRAINT operations_request_fingerprint_format,
    DROP COLUMN request_fingerprint;
