CREATE TABLE IF NOT EXISTS sync_job_summary_replays (
    job_id             uuid NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    source             text NOT NULL,
    execution_attempt  int NOT NULL,
    request            jsonb NOT NULL,
    request_sha256     text NOT NULL,
    state              text NOT NULL DEFAULT 'pending',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    resolved_at        timestamptz,
    PRIMARY KEY (job_id, source, execution_attempt),
    CONSTRAINT ck_sync_job_summary_replay_source CHECK (length(source) BETWEEN 1 AND 128),
    CONSTRAINT ck_sync_job_summary_replay_attempt CHECK (execution_attempt > 0),
    CONSTRAINT ck_sync_job_summary_replay_request CHECK (jsonb_typeof(request) = 'object'),
    CONSTRAINT ck_sync_job_summary_replay_hash CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT ck_sync_job_summary_replay_state CHECK (state IN ('pending', 'unknown', 'resolved')),
    CONSTRAINT ck_sync_job_summary_replay_resolution CHECK
        ((state = 'resolved' AND resolved_at IS NOT NULL) OR
         (state <> 'resolved' AND resolved_at IS NULL))
);

CREATE INDEX IF NOT EXISTS idx_sync_job_summary_replays_claim
    ON sync_job_summary_replays (state, updated_at, job_id)
    WHERE state IN ('pending', 'unknown');

CREATE TABLE IF NOT EXISTS sync_job_source_outcomes (
    job_id              uuid NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    source              text NOT NULL,
    requested_datasets  jsonb NOT NULL DEFAULT '[]'::jsonb,
    status              text NOT NULL DEFAULT 'pending',
    source_operation_id text NOT NULL DEFAULT '',
    error_code          text NOT NULL DEFAULT '',
    result              jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, source),
    CONSTRAINT ck_sync_job_source_outcome_source CHECK (length(source) BETWEEN 1 AND 128),
    CONSTRAINT ck_sync_job_source_outcome_datasets CHECK (jsonb_typeof(requested_datasets) = 'array'),
    CONSTRAINT ck_sync_job_source_outcome_status CHECK (status IN ('pending', 'completed', 'failed', 'unknown')),
    CONSTRAINT ck_sync_job_source_outcome_operation CHECK (length(source_operation_id) <= 512),
    CONSTRAINT ck_sync_job_source_outcome_error CHECK (length(error_code) <= 256),
    CONSTRAINT ck_sync_job_source_outcome_result CHECK (jsonb_typeof(result) = 'object')
);

CREATE TABLE IF NOT EXISTS sync_job_artifacts (
    job_id          uuid NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    kind            text NOT NULL,
    storage_key     text NOT NULL,
    -- SHA-256 of the canonical plaintext manifest; ciphertext integrity is
    -- provided separately by AES-GCM.
    manifest_sha256 text NOT NULL,
    size_bytes      bigint NOT NULL,
    continuation    jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, kind),
    CONSTRAINT ck_sync_job_artifact_kind CHECK (kind IN ('export', 'audit_export')),
    CONSTRAINT ck_sync_job_artifact_key CHECK (length(storage_key) BETWEEN 1 AND 1024),
    CONSTRAINT ck_sync_job_artifact_hash CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT ck_sync_job_artifact_size CHECK (size_bytes >= 0),
    CONSTRAINT ck_sync_job_artifact_continuation CHECK (jsonb_typeof(continuation) = 'object')
);
