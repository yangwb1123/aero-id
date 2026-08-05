UPDATE roles
SET permissions = ARRAY(
    SELECT DISTINCT unnest(permissions || ARRAY['source.event.ingest']::text[])
)
WHERE code = 'admin';
