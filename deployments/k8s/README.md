# Regional cell deployment (P6-01 / P6-10)

Each region deploys an independent cell: stateless API + background workers,
regional PostgreSQL (single writer), Redis (coordination only) and NATS
JetStream (canonical events). Account-affecting writes route to the account's
home region; other regions serve local projections (see
`docs/plans/2026-08-04-distributed-account-aggregation.md` §4).

## Apply

```bash
# 1. Replace placeholders: REGION_PLACEHOLDER (config + ingress), source and
#    Snaplink endpoints.
# 2. Provision the `aero-id-secrets` Secret out of band with the approved
#    secret provider. Required keys are database_password, redis_password,
#    signing_key, callback_master_key, event_ingest_signing_key,
#    audit_ledger_signing_key and export_encryption_key. Optional source bearer
#    keys are snaplink_bearer_token, aero_im_bearer_token and
#    aero_vault_bearer_token. To enable the bound Aero IM summary, also provide
#    aero_im_target_assertion_private_key as an Ed25519 PKCS#8 PEM key and
#    aero_im_oauth_client_secret. They are injected only through
#    `AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_PRIVATE_KEY` and
#    `AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_SECRET`; keep both corresponding
#    ConfigMap values blank. Set the complete target_assertion_* block in the
#    ConfigMap, including target_assertion_key_env; the key is never stored in
#    the ConfigMap.
# 3. Before enabling that block, publish the matching public Ed25519 JWK set at
#    the dedicated external JWKS URI configured in Aero IM
#    (`AERO__ACCOUNT_SUMMARY__TARGET_ASSERTION_JWKS_URI`). This endpoint is not
#    owned by aero-id or this manifest and must never be Snaplink's general JWKS.
#    It must expose unique kids, `alg: EdDSA`, `crv: Ed25519`, `use: sig`, and
#    `key_ops: [verify]`; overlap old/new kids through the JWKS cache and rollout.
# 4. Deploy dependencies (postgres, redis, nats) first so workers can start.
kubectl apply -f deployments/k8s/regional-cell.yaml
```

### Bound Aero IM source configuration

The source fields use Viper's `AERO_ID_` prefix and `.`-to-`_` mapping. The
base ConfigMap carries blank keys for the OAuth fields so these exact overrides
are reachable; non-secret values may remain in the ConfigMap or be supplied by
an environment-specific overlay:

| Config field | Environment override | Storage rule |
|---|---|---|
| `sources.aero_im.oauth_token_url` | `AERO_ID_SOURCES_AERO_IM_OAUTH_TOKEN_URL` | non-secret |
| `sources.aero_im.oauth_client_id` | `AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_ID` | non-secret |
| `sources.aero_im.oauth_scope` | `AERO_ID_SOURCES_AERO_IM_OAUTH_SCOPE` | non-secret |
| `sources.aero_im.oauth_resource` | `AERO_ID_SOURCES_AERO_IM_OAUTH_RESOURCE` | non-secret |
| `sources.aero_im.oauth_client_secret` | `AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_SECRET` | Secret only; never a ConfigMap value |

For the signer metadata, the corresponding names are
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_KEY_FILE`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_KEY_ENV`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_KEY_ID`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_ISSUER`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_AUDIENCE`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_SUBJECT`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_CLIENT_ID`, and
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_LIFETIME`. When enabling the signer,
set `target_assertion_key_env` to the Secret-backed
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_PRIVATE_KEY`; the base manifest
injects that environment variable but leaves the signer disabled. It does not
use a Secret volume because Kubernetes Secret volume paths are symlinks and the
loader intentionally rejects symlinked key files. These values are references
or non-secret metadata; the private key itself is never stored in the
ConfigMap. This manifest does not publish a JWKS; the matching dedicated JWKS
remains an externally managed deployment boundary.

## Layout

| Resource | Purpose |
|---|---|
| `aero-id-api` Deployment + Service | HTTP :8080 / gRPC :9090, stateless, 3 replicas |
| `aero-id-api` HPA | P6-10: CPU 65% / memory 70%, 3–20 replicas |
| `aero-id-api` PDB | minAvailable 2, zone-spread via topologySpreadConstraints |
| `aero-id-workers` Deployment | callbacks/audit-publication/operations/sync/gc workers; DB leases fence concurrent workers |
| `aero-id-postgres` StatefulSet | regional single-writer PostgreSQL 16, 50Gi volume |
| `aero-id-redis` | rate-limit/cache/lease coordination; correctness stays in PostgreSQL |
| `aero-id-nats` | JetStream stream `aero_events`, subjects `aero.events.canonical` and `aero.events.canonical.dead-letter` |
| Ingress | `api.<region>.example.com` -> service |

## Operational notes

- **Migrations**: run `cmd/migrate` as a Job per region before rolling the API
  (config `database.auto_migrate: false`). Never edit applied migrations.
- **NATS stream**: configure `aero_events` with both the canonical and
  `aero.events.canonical.dead-letter` subjects. Poison messages are acknowledged
  only after a redacted metadata record is durably published to the latter.
- **Cross-region replication** (`replication.enabled: true`): publish approved
  projections to the regional bus; remote cells consume via `event_bus.nats`.
  The stream must exist in every region (`aero_events`).
  The optional HTTP transport requires an HTTPS endpoint, HMAC key, and a
  Snaplink service bearer token; bind that client explicitly to
  `source: aero-id`, `tenant_id: "*"`, and the sending region at the remote
  event-ingest boundary.
- **Fencing (P6-02)**: promotion is an admin op
  `POST /v1/admin/regions/promote` with `expected_epoch`; a stale epoch loses
  by CAS. Old-region writes fail closed (`region.write_fenced`) after promotion.
- **Secrets**: `aero-id-secrets` is intentionally not created by this manifest;
  provision it via a secret provider before applying the cell. Never commit
  real keys or placeholder secret values.
