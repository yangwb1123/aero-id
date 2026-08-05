-- 000030_create_webhook_subscriptions.up.sql
CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    id                         uuid PRIMARY KEY,
    owner_uid                  text NOT NULL,
    url                        text NOT NULL,
    event_types                text[] NOT NULL DEFAULT '{}',
    signing_key                text NOT NULL,
    secret                     text NOT NULL DEFAULT '',
    enabled                    boolean NOT NULL DEFAULT true,
    max_consecutive_failures   int NOT NULL DEFAULT 10,
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_webhook_subscriptions_owner
    ON webhook_subscriptions (owner_uid);

CREATE INDEX IF NOT EXISTS idx_webhook_subscriptions_enabled
    ON webhook_subscriptions (enabled)
    WHERE enabled = true;

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id               uuid PRIMARY KEY,
    subscription_id  uuid NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
    event_id         text NOT NULL,
    status           text NOT NULL, -- 'success' or 'failed'
    status_code      int NOT NULL DEFAULT 0,
    response_body    text NOT NULL DEFAULT '',
    attempt          int NOT NULL DEFAULT 1,
    duration_ms      bigint NOT NULL DEFAULT 0,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_sub
    ON webhook_deliveries (subscription_id, created_at DESC);
