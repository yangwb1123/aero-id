DROP INDEX IF EXISTS idx_operations_request_id;
DROP INDEX IF EXISTS idx_sync_jobs_request_id;
ALTER TABLE operations DROP COLUMN IF EXISTS request_id;
ALTER TABLE sync_jobs DROP COLUMN IF EXISTS request_id;
