ALTER TABLE sync_jobs ADD COLUMN IF NOT EXISTS request_id text NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN IF NOT EXISTS request_id text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_sync_jobs_request_id ON sync_jobs (request_id) WHERE request_id <> '';
CREATE INDEX IF NOT EXISTS idx_operations_request_id ON operations (request_id) WHERE request_id <> '';
