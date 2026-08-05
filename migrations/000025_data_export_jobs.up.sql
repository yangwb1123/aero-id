-- Data export jobs: one row per asynchronous user‑data export request.
-- The export worker picks pending jobs, gathers the user's data into a
-- ZIP archive, writes it to the storage backend, and marks the job as
-- completed (or failed if something goes wrong).
CREATE TABLE IF NOT EXISTS data_export_jobs (
    id                  uuid        NOT NULL PRIMARY KEY,
    owner_uid           TEXT        NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','processing','completed','failed')),
    archive_storage_key TEXT        NOT NULL DEFAULT '',   -- storage key once completed
    file_size           BIGINT      NOT NULL DEFAULT 0,    -- size of the generated archive
    error_message       TEXT        NOT NULL DEFAULT '',
    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at        TIMESTAMPTZ                        -- set when status becomes completed/failed
);

CREATE INDEX IF NOT EXISTS idx_data_export_jobs_owner
    ON data_export_jobs (owner_uid, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_data_export_jobs_status
    ON data_export_jobs (status, created_at)
    WHERE status = 'pending';
