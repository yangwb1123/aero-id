UPDATE roles
SET permissions = ARRAY(
    SELECT value
    FROM unnest(permissions) AS value
    WHERE value <> 'source.event.ingest'
)
WHERE code = 'admin';
