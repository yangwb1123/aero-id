UPDATE roles
SET permissions = ARRAY(
    SELECT DISTINCT unnest(permissions || ARRAY[
        'account.read', 'account.manage', 'callback.manage', 'operation.read',
        'operation.manage', 'source.sync', 'account.export', 'account.erase', 'audit.replay'
    ]::text[])
)
WHERE code = 'admin';
