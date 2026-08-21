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
#    aero_vault_bearer_token.
# 3. Deploy dependencies (postgres, redis, nats) first so workers can start.
kubectl apply -f deployments/k8s/regional-cell.yaml
```

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
