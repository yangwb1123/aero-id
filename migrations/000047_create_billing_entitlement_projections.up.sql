CREATE TABLE billing_entitlement_projections (
    tenant_id TEXT PRIMARY KEY CHECK (btrim(tenant_id) <> ''),
    subscription_id TEXT NOT NULL CHECK (btrim(subscription_id) <> ''),
    plan_id TEXT NOT NULL CHECK (btrim(plan_id) <> ''),
    plan_version BIGINT NOT NULL CHECK (plan_version > 0),
    revision BIGINT NOT NULL CHECK (revision > 0),
    active BOOLEAN NOT NULL,
    features JSONB NOT NULL DEFAULT '{}'::jsonb,
    limits JSONB NOT NULL DEFAULT '{}'::jsonb,
    effective_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ,
    generated_at TIMESTAMPTZ NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL,
    CHECK (expires_at IS NULL OR expires_at > effective_at)
);

CREATE INDEX idx_billing_entitlement_projection_freshness
    ON billing_entitlement_projections (fetched_at);
