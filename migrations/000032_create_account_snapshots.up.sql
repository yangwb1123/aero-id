CREATE TABLE IF NOT EXISTS account_snapshots (
    id              uuid PRIMARY KEY,
    account_id      uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    source          text NOT NULL,
    source_region   text NOT NULL,
    dataset         text NOT NULL,
    status          text NOT NULL DEFAULT 'unknown',
    version         bigint NOT NULL DEFAULT 0,
    generated_at    timestamptz NOT NULL DEFAULT now(),
    last_synced_at  timestamptz,
    expires_at      timestamptz,
    error_code      text NOT NULL DEFAULT '',
    data            jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_account_snapshot_dataset UNIQUE (account_id, source, dataset)
);

CREATE INDEX IF NOT EXISTS idx_account_snapshots_account
    ON account_snapshots (account_id, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_account_snapshots_dataset
    ON account_snapshots (source, dataset, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_account_snapshots_expiry
    ON account_snapshots (expires_at)
    WHERE expires_at IS NOT NULL;
