DROP INDEX IF EXISTS idx_account_summary_fences_source_version;
DROP TABLE IF EXISTS account_summary_fences;

ALTER TABLE account_memberships
    DROP COLUMN IF EXISTS error_code;
