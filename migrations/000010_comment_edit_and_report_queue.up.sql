-- Author comment editing: track when a comment was last edited (NULL = never edited).
ALTER TABLE comments ADD COLUMN edited_at TIMESTAMPTZ;

-- Moderation report queue lifecycle: triage reports as open(1)/resolved(2)/dismissed(3).
ALTER TABLE comment_reports ADD COLUMN status      SMALLINT NOT NULL DEFAULT 1;
ALTER TABLE comment_reports ADD COLUMN resolved_at TIMESTAMPTZ;
ALTER TABLE comment_reports ADD COLUMN resolved_by TEXT;
CREATE INDEX idx_comment_reports_status ON comment_reports(comment_id, status, created_at DESC);
