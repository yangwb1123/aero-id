-- Add category column to files table for MIME-based file classification.
-- Categories are derived at upload time; existing rows get 'other' as default.
ALTER TABLE files ADD COLUMN category TEXT NOT NULL DEFAULT 'other';

-- Index for category-based filtering queries.
CREATE INDEX idx_files_category ON files (category) WHERE deleted_at IS NULL;
