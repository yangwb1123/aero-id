-- Add collapsed_count to support notification merging: when multiple
-- notifications of the same type from the same actor arrive within the
-- collapse window (5 minutes), the dispatcher increments this counter
-- instead of creating a new row. Default 1 means "not merged".
ALTER TABLE notifications
    ADD COLUMN collapsed_count INT NOT NULL DEFAULT 1;
