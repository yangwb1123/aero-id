CREATE TABLE accounts (
    id             UUID PRIMARY KEY,
    canonical_uid  TEXT NOT NULL,
    home_region    TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'active',
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_accounts_uid ON accounts(canonical_uid);
CREATE INDEX idx_accounts_region_status ON accounts(home_region, status);

CREATE TABLE account_identities (
    id             UUID PRIMARY KEY,
    account_id     UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    issuer         TEXT NOT NULL,
    subject        TEXT NOT NULL,
    canonical_uid  TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    last_seen_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (issuer, subject),
    UNIQUE (account_id, issuer, subject)
);

CREATE INDEX idx_account_identities_account ON account_identities(account_id);

CREATE TABLE account_profiles (
    account_id     UUID PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    display_name   TEXT NOT NULL DEFAULT '',
    avatar_url     TEXT NOT NULL DEFAULT '',
    locale         TEXT NOT NULL DEFAULT '',
    timezone       TEXT NOT NULL DEFAULT 'UTC',
    preferences    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL
);

CREATE TABLE account_sources (
    id              UUID PRIMARY KEY,
    account_id      UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    source          TEXT NOT NULL,
    source_region   TEXT NOT NULL DEFAULT '',
    source_account_id TEXT NOT NULL,
    scope_type      TEXT NOT NULL DEFAULT '',
    scope_id        TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'unknown',
    version         BIGINT NOT NULL DEFAULT 0,
    generated_at    TIMESTAMPTZ,
    last_synced_at  TIMESTAMPTZ,
    error_code      TEXT NOT NULL DEFAULT '',
    data            JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (account_id, source, source_account_id, scope_type, scope_id)
);

CREATE INDEX idx_account_sources_account ON account_sources(account_id, id DESC);
CREATE INDEX idx_account_sources_source ON account_sources(source, source_region, updated_at DESC);

CREATE TABLE account_memberships (
    id                UUID PRIMARY KEY,
    account_id        UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    source            TEXT NOT NULL,
    source_region     TEXT NOT NULL DEFAULT '',
    scope_type        TEXT NOT NULL,
    scope_id          TEXT NOT NULL,
    source_member_id  TEXT NOT NULL,
    role              TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active',
    version           BIGINT NOT NULL DEFAULT 0,
    last_synced_at    TIMESTAMPTZ,
    data              JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL,
    UNIQUE (account_id, source, scope_type, scope_id, source_member_id)
);

CREATE INDEX idx_account_memberships_account ON account_memberships(account_id, id DESC);
CREATE INDEX idx_account_memberships_scope ON account_memberships(source, scope_type, scope_id, id DESC);
