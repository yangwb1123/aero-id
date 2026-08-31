DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM sync_job_summary_replays) OR
       EXISTS (SELECT 1 FROM sync_job_source_outcomes) OR
       EXISTS (SELECT 1 FROM sync_job_artifacts) THEN
        RAISE EXCEPTION 'refusing to drop sync recovery state while recovery rows exist';
    END IF;
    IF EXISTS (SELECT 1 FROM sync_jobs WHERE status = 'unknown') OR
       EXISTS (SELECT 1 FROM sync_jobs WHERE status = 'running' AND lease_kind = 'reconcile' AND
               lease_until IS NOT NULL) OR
       EXISTS (SELECT 1 FROM sync_jobs WHERE status = 'partially_completed' AND
               job_type = 'erase' AND error_code = 'erase.local_projection_failed') THEN
        RAISE EXCEPTION 'refusing to drop sync recovery state while recoverable jobs exist';
    END IF;
END $$;

DROP INDEX IF EXISTS idx_sync_job_summary_replays_claim;
DROP TABLE IF EXISTS sync_job_summary_replays;
DROP TABLE IF EXISTS sync_job_source_outcomes;
DROP TABLE IF EXISTS sync_job_artifacts;
