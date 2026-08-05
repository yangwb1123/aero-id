UPDATE roles
SET permissions = ARRAY(
    SELECT value FROM unnest(permissions) AS value
    WHERE value NOT IN (
        'account.read', 'account.manage', 'callback.manage', 'operation.read',
        'operation.manage', 'source.sync', 'account.export', 'account.erase', 'audit.replay'
    )
)
WHERE code = 'admin';
