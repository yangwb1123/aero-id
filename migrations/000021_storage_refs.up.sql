-- Add storage_ref_count to files for atomic reference counting. Each file row
-- that references a storage_key via content-dedup increments this counter on
-- the first row's storage_key; each soft-delete decrements it. The GC sweeper
-- and the file Delete path only remove the physical blob when the count for
-- that key reaches zero, closing the race window where a concurrent Create
-- (content-dedup) could reuse a key whose last row is being deleted.

-- Existing rows that were created before this migration get count = 1 (the
-- canonical number for a non-deduplicated file). A backfill script is not
-- necessary because the count is only used for GC/delete decisions: setting
-- it to 1 is correct for every pre-existing file.

-- We create a separate table rather than a column on files because one
-- storage_key may be referenced by many file rows (N:1 via content dedup).
CREATE TABLE IF NOT EXISTS storage_refs (
    storage_key TEXT PRIMARY KEY,
    ref_count   INT NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
