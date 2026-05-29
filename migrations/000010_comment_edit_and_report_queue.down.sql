DROP INDEX IF EXISTS idx_comment_reports_status;
ALTER TABLE comment_reports DROP COLUMN IF EXISTS resolved_by;
ALTER TABLE comment_reports DROP COLUMN IF EXISTS resolved_at;
ALTER TABLE comment_reports DROP COLUMN IF EXISTS status;

ALTER TABLE comments DROP COLUMN IF EXISTS edited_at;
