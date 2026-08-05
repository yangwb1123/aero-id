CREATE TABLE IF NOT EXISTS callback_subscriptions (
    id                 uuid PRIMARY KEY,
    owner_uid          text NOT NULL,
    tenant_id          text NOT NULL DEFAULT '',
    endpoint           text NOT NULL,
    event_types        text[] NOT NULL DEFAULT '{}',
    secret_ciphertext  text NOT NULL,
    enabled            boolean NOT NULL DEFAULT true,
    verified           boolean NOT NULL DEFAULT false,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_callback_subscriptions_owner ON callback_subscriptions (owner_uid, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_callback_subscriptions_enabled ON callback_subscriptions (enabled) WHERE enabled = true;

CREATE TABLE IF NOT EXISTS callback_deliveries (
    id               uuid PRIMARY KEY,
    subscription_id  uuid NOT NULL REFERENCES callback_subscriptions(id) ON DELETE CASCADE,
    event_id         uuid NOT NULL,
    action           text NOT NULL,
    payload          jsonb NOT NULL DEFAULT '{}'::jsonb,
    status           text NOT NULL DEFAULT 'pending',
    attempt          int NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    status_code      int NOT NULL DEFAULT 0,
    response_body    text NOT NULL DEFAULT '',
    error_code       text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_callback_delivery_event UNIQUE (subscription_id, event_id)
);

CREATE INDEX IF NOT EXISTS idx_callback_deliveries_due ON callback_deliveries (status, next_attempt_at ASC);
CREATE INDEX IF NOT EXISTS idx_callback_deliveries_subscription ON callback_deliveries (subscription_id, created_at DESC, id DESC);
