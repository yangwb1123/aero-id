# Regional cell deployment (P6-01 / P6-10)

Each region deploys an independent cell: stateless API + background workers,
regional PostgreSQL (single writer), Redis (coordination only) and NATS
JetStream (canonical events). Account-affecting writes route to the account's
home region; other regions serve local projections (see
`docs/plans/2026-08-04-distributed-account-aggregation.md` §4).

## Apply

```bash
# 1. Replace placeholders: REGION_PLACEHOLDER (config + ingress), secrets.
# 2. Deploy dependencies (postgres, redis, nats) first so workers can start.
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
| `aero-id-nats` | JetStream stream `aero_events`, subject `aero.events.canonical` |
| Ingress | `api.<region>.example.com` -> service |

## Operational notes

- **Migrations**: run `cmd/migrate` as a Job per region before rolling the API
  (config `database.auto_migrate: false`). Never edit applied migrations.
- **Cross-region replication** (`replication.enabled: true`): publish approved
  projections to the regional bus; remote cells consume via `event_bus.nats`.
  The stream must exist in every region (`aero_events`).
- **Fencing (P6-02)**: promotion is an admin op
  `POST /v1/admin/regions/promote` with `expected_epoch`; a stale epoch loses
  by CAS. Old-region writes fail closed (`region.write_fenced`) after promotion.
- **Secrets**: replace `aero-id-secrets` values via a secret provider; never
  commit real keys.
