CREATE TABLE notification_preferences (
    uid               TEXT NOT NULL,
    notification_type TEXT NOT NULL,
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,

    -- Quiet hours: when set, notifications of this type are suppressed during
    -- the daily window [quiet_hours_start, quiet_hours_end). Both are stored
    -- as TEXT in "HH:MM" format (24h). NULL means no quiet hours.
    quiet_hours_start TEXT,
    quiet_hours_end   TEXT,
    timezone          TEXT NOT NULL DEFAULT 'UTC',

    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (uid, notification_type)
);

CREATE INDEX idx_notif_prefs_uid ON notification_preferences(uid);
