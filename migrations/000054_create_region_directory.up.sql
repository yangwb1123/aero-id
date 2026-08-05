-- P6-02: durable home-region directory and fencing epochs.
--
-- region_directory records the authoritative home region and the fencing epoch
-- proving which regional writer may commit account-affecting writes. A
-- promotion is only valid when it bumps the epoch with compare-and-swap:
-- region_epochs[region] must equal directory.fenced_epoch before promotion, and
-- the write guard refuses commits when the local region's epoch is stale.
CREATE TABLE region_directory (
    id               UUID PRIMARY KEY,
    account_id       UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    tenant_id        TEXT NOT NULL,
    home_region      TEXT NOT NULL,
    resolver_version TEXT NOT NULL,
    fenced_epoch     BIGINT NOT NULL DEFAULT 0,
    promoted_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL,
    updated_at       TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, account_id)
);

CREATE INDEX idx_region_directory_region ON region_directory (home_region, fenced_epoch);

-- One row per region: the durable epoch of the region's current writer lease.
CREATE TABLE region_epochs (
    region     TEXT PRIMARY KEY,
    epoch      BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
