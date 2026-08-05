-- Extend notification_preferences with scope_type and scope_id to allow
-- resource-level notification preferences (e.g. mute a specific file or
-- conversation). An empty scope_type signals a global (unscoped) preference.
-- The existing primary key (uid, notification_type) remains valid for global
-- rows; the new columns extend the key for scoped rows.

ALTER TABLE notification_preferences ADD COLUMN scope_type TEXT NOT NULL DEFAULT '';
ALTER TABLE notification_preferences ADD COLUMN scope_id   TEXT NOT NULL DEFAULT '';

-- Drop the existing primary key constraint (it was (uid, notification_type)).
-- The underlying index was created by GORM; we need to rebuild the PK.
ALTER TABLE notification_preferences DROP CONSTRAINT notification_preferences_pkey;

-- Recreate the primary key to include scope columns. Global rows have
-- scope_type='' so (uid, notif_type, '', '') remains unique.
ALTER TABLE notification_preferences ADD PRIMARY KEY (uid, notification_type, scope_type, scope_id);

-- Index for resource-scoped lookups: find all preferences for a given resource.
CREATE INDEX idx_pref_scope ON notification_preferences (scope_type, scope_id)
  WHERE scope_type != '';
