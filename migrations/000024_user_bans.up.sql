-- User bans table: a lightweight blocklist for middle‑ware enforcement.
-- Admins ban (with optional expiry and a mandatory reason); banned users
-- receive 403 on every /v1/* route and their SSE connections are closed.
CREATE TABLE IF NOT EXISTS user_bans (
    uid         TEXT        NOT NULL PRIMARY KEY,
    actor_uid   TEXT        NOT NULL DEFAULT '',          -- who issued the ban
    reason      TEXT        NOT NULL DEFAULT '',          -- mandatory reason
    expires_at  TIMESTAMPTZ,                              -- NULL = permanent
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at  TIMESTAMPTZ                               -- soft‑unban via DELETE
);

CREATE INDEX IF NOT EXISTS idx_user_bans_active
    ON user_bans (uid)
    WHERE deleted_at IS NULL;
