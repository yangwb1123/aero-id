DROP INDEX IF EXISTS idx_pref_scope;

ALTER TABLE notification_preferences DROP CONSTRAINT notification_preferences_pkey;
ALTER TABLE notification_preferences ADD PRIMARY KEY (uid, notification_type);

ALTER TABLE notification_preferences DROP COLUMN IF EXISTS scope_id;
ALTER TABLE notification_preferences DROP COLUMN IF EXISTS scope_type;
