-- Admin roles and permissions. A user may hold multiple roles; permissions are
-- checked at the middleware/service layer via RequirePermission.
CREATE TABLE IF NOT EXISTS roles (
    code        TEXT PRIMARY KEY,  -- e.g. "moderator", "admin"
    name        TEXT NOT NULL,     -- human-readable, e.g. "Moderator"
    permissions TEXT[] NOT NULL DEFAULT '{}',  -- e.g. {"content.moderate", "audit.read"}
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_roles (
    uid         TEXT NOT NULL,
    role_code   TEXT NOT NULL REFERENCES roles(code),
    granted_by  TEXT NOT NULL DEFAULT '',  -- uid of the admin who granted it
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (uid, role_code)
);

-- Seed the built-in roles. These are the minimum set; operators can insert
-- additional roles directly into the database.
INSERT INTO roles (code, name, permissions) VALUES
    ('moderator', 'Moderator',   ARRAY['content.moderate', 'audit.read']),
    ('admin',     'Administrator', ARRAY['content.moderate', 'audit.read', 'user.ban', 'system.config'])
ON CONFLICT (code) DO NOTHING;
