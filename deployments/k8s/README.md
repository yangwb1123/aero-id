# Regional cell deployment (P6-01 / P6-10)

Each region deploys an independent cell: stateless API + background workers,
regional PostgreSQL (single writer), Redis (coordination only) and NATS
JetStream (canonical events). Account-affecting writes route to the account's
home region; other regions serve local projections (see
`docs/plans/2026-08-04-distributed-account-aggregation.md` §4).

## Release rendering and apply

`regional-cell.yaml` is a release template, not an applyable production
artifact. Its application image and config checksum are intentionally
placeholders. Render it with the exact image digest and region, then apply the
rendered file:

```bash
export REGION=us-east-1
export AERO_ID_IMAGE_DIGEST=sha256:<64-lowercase-hex-digest>
export RELEASE=2026-08-31
export OUTPUT=/tmp/aero-id-${REGION}-${RELEASE}.yaml
make render-k8s
kubectl apply --dry-run=server -f "$OUTPUT"
kubectl apply -f "$OUTPUT"
kubectl -n aero-id rollout status deploy/aero-id-api --timeout=10m
kubectl -n aero-id rollout status deploy/aero-id-workers --timeout=10m
```

The renderer replaces every `REGION_PLACEHOLDER`, pins the API and worker to
`ghcr.io/aero/aero-id@${AERO_ID_IMAGE_DIGEST}`, and computes SHA-256 over the
rendered `ConfigMap.data.config.yaml`. The resulting `checksum/config`
annotation is on both workload pod templates. Never apply the template,
`kubectl edit` the ConfigMap, or change a config file without rendering a new
release artifact; otherwise a config change will not create a new pod
revision. The PostgreSQL, Redis, and NATS images are also pinned by digest in
the template. The separate `deployments/k8s/migration-job.yaml` template uses
the same application digest and is rendered only for a one-shot schema
operation; never add that Job to the steady-state bundle.

The base cell is bootable with the required application Secret and deliberately
disables the Snaplink, Aero IM, and Aero Vault source connectors. Enable a
source only in a reviewed overlay/config artifact that supplies its complete
authentication and source contract. In particular, do not enable the deployed
Aero Vault summary until its Snaplink `PrincipalService`, tenant-header, token
scope/audience, and region contract has been verified; erase and operation-status
remain disabled.

The `aero-id-secrets` Secret is provisioned out of band by the approved secret
provider. Required keys are `database_password`, `redis_password`,
`signing_key`, `callback_master_key`, `event_ingest_signing_key`,
`audit_ledger_signing_key`, and `export_encryption_key`. Source credentials are
optional Kubernetes references: `snaplink_bearer_token`,
`snaplink_oauth_client_secret`, `aero_im_bearer_token`,
`aero_im_oauth_client_secret`, `aero_im_target_assertion_private_key`,
`aero_vault_bearer_token`, and `aero_vault_oauth_client_secret`. The Vault
bearer reference is retained only for rejecting legacy/misconfigured overlays;
Vault enablement requires the OAuth client secret and tenant binding. A missing
optional key does not block a pod for a disabled source. If an overlay enables
that source, `config.Load` and the init preflight fail closed until the required
credential is present. Secret values never belong in this repository or in the
ConfigMap.

Each API and worker pod has a `preflight` init container using the same
immutable image, ConfigMap, and Secret context as its application container.
It runs before the application can bind a port, validates the full config, and
when Aero IM target assertions are enabled verifies the dedicated Ed25519 JWKS
binding without redirects or proxies. Both the init container and the
application verify the pod-template `checksum/config` against the mounted file.
A failed check leaves the pod in initialization (or exits the process) and
prevents traffic or background work. The manual equivalent for a rendered image
is `/app/preflight -config /etc/aero-id/config.yaml`; it performs no database
migration or application wiring.


### Bound Aero IM source configuration

The source fields use Viper's `AERO_ID_` prefix and `.`-to-`_` mapping. The
base ConfigMap carries blank keys for the OAuth fields so these exact overrides
are reachable; non-secret values may remain in the ConfigMap or be supplied by
an environment-specific overlay. Snaplink command capabilities are separate
from transport paths: a path alone never enables a command, and no production
`erase_contract_version` is currently verified. Keep all four Snaplink
capability values disabled/empty until source-owner evidence is reviewed.

| Config field | Environment override | Storage rule |
|---|---|---|
| `sources.snaplink.capabilities.summary` | `AERO_ID_SOURCES_SNAPLINK_CAPABILITIES_SUMMARY` | non-secret; required for an enabled endpoint |
| `sources.snaplink.capabilities.erase` | `AERO_ID_SOURCES_SNAPLINK_CAPABILITIES_ERASE` | non-secret; requires verified contract/version |
| `sources.snaplink.capabilities.operation_status` | `AERO_ID_SOURCES_SNAPLINK_CAPABILITIES_OPERATION_STATUS` | non-secret; requires `status_path` |
| `sources.snaplink.capabilities.erase_contract_version` | `AERO_ID_SOURCES_SNAPLINK_CAPABILITIES_ERASE_CONTRACT_VERSION` | non-secret; compiled verified version only |
| `sources.snaplink.oauth_token_url` | `AERO_ID_SOURCES_SNAPLINK_OAUTH_TOKEN_URL` | non-secret |
| `sources.snaplink.oauth_client_id` | `AERO_ID_SOURCES_SNAPLINK_OAUTH_CLIENT_ID` | non-secret |
| `sources.snaplink.oauth_scope` | `AERO_ID_SOURCES_SNAPLINK_OAUTH_SCOPE` | non-secret |
| `sources.snaplink.oauth_resource` | `AERO_ID_SOURCES_SNAPLINK_OAUTH_RESOURCE` | non-secret |
| `sources.snaplink.oauth_client_secret` | `AERO_ID_SOURCES_SNAPLINK_OAUTH_CLIENT_SECRET` | Secret only; never a ConfigMap value |
| `sources.aero_im.oauth_token_url` | `AERO_ID_SOURCES_AERO_IM_OAUTH_TOKEN_URL` | non-secret |
| `sources.aero_im.oauth_client_id` | `AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_ID` | non-secret |
| `sources.aero_im.oauth_scope` | `AERO_ID_SOURCES_AERO_IM_OAUTH_SCOPE` | non-secret |
| `sources.aero_im.oauth_resource` | `AERO_ID_SOURCES_AERO_IM_OAUTH_RESOURCE` | non-secret |
| `sources.aero_im.oauth_client_secret` | `AERO_ID_SOURCES_AERO_IM_OAUTH_CLIENT_SECRET` | Secret only; never a ConfigMap value |
| `sources.aero_vault.allow_insecure_loopback` | `AERO_ID_SOURCES_AERO_VAULT_ALLOW_INSECURE_LOOPBACK` | Development/test only; never enable for a production endpoint |
| `sources.aero_vault.oauth_token_url` | `AERO_ID_SOURCES_AERO_VAULT_OAUTH_TOKEN_URL` | Exact HTTPS origin; use with `oauth_allowed_hosts` |
| `sources.aero_vault.oauth_client_id` | `AERO_ID_SOURCES_AERO_VAULT_OAUTH_CLIENT_ID` | Dedicated non-secret Snaplink client identity |
| `sources.aero_vault.oauth_client_secret` | `AERO_ID_SOURCES_AERO_VAULT_OAUTH_CLIENT_SECRET` | Dedicated Secret only; never a ConfigMap value |
| `sources.aero_vault.oauth_tenant_id` | `AERO_ID_SOURCES_AERO_VAULT_OAUTH_TENANT_ID` | Exact tenant mapped to this client in Vault; no wildcard |
| `sources.aero_vault.oauth_scope` | `AERO_ID_SOURCES_AERO_VAULT_OAUTH_SCOPE` | Must be exactly `read` |
| `sources.aero_vault.oauth_resource` | `AERO_ID_SOURCES_AERO_VAULT_OAUTH_RESOURCE` | Must be exactly `aero-vault` (token audience) |
| `sources.aero_vault.oauth_tls_*` | `AERO_ID_SOURCES_AERO_VAULT_OAUTH_TLS_*` | Token-authority trust profile; separate from Vault `tls_*` |

For the signer metadata, the corresponding names are
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_KEY_FILE`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_KEY_ENV`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_KEY_ID`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_ISSUER`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_TRUST_DOMAIN`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_AUDIENCE`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_SUBJECT`,
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_CLIENT_ID`, and
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_LIFETIME`. When enabling the signer,
set `target_assertion_key_env` to the Secret-backed
`AERO_ID_SOURCES_AERO_IM_TARGET_ASSERTION_PRIVATE_KEY`, and set the trust
-domain URL equal to `target_assertion_issuer`. The base manifest injects that
environment variable but leaves the signer disabled. It does not use a Secret
volume because Kubernetes Secret volume paths are symlinks and the loader
intentionally rejects symlinked key files. These values are references or
non-secret metadata; the private key itself is never stored in the ConfigMap.
This manifest does not publish a JWKS; the matching dedicated JWKS remains an
externally managed deployment boundary and must be verified by `/app/preflight`.

## Real Vault contract gate

The checked-in Vault tests do not treat `httptest` as a source contract. Set the
following protected CI variables (and the client secret as an Actions Secret)
to run the opt-in deployed test:

| Variable | Meaning |
|---|---|
| `AERO_ID_VAULT_CONTRACT_ENDPOINT` | deployed Vault base URL |
| `AERO_ID_VAULT_CONTRACT_TOKEN_URL` | Snaplink client-credentials token URL |
| `AERO_ID_VAULT_CONTRACT_CLIENT_ID` / `...CLIENT_SECRET` | dedicated Vault `PrincipalService` client |
| `AERO_ID_VAULT_CONTRACT_TENANT_ID` | exact tenant mapped to that client in Vault |
| `AERO_ID_VAULT_CONTRACT_ACCOUNT_ID` / `...CANONICAL_UID` | seeded contract account identity |
| `AERO_ID_VAULT_CONTRACT_OAUTH_ALLOWED_HOSTS` | exact token-authority origin(s), including port |
| `AERO_ID_VAULT_CONTRACT_REGION` | must equal `AERO_VAULT_ACCOUNT_SOURCE_REGION` |

`make test-vault-contract` verifies the real `/healthz` and
`/v1/internal/account-summary` paths, OAuth authentication, tenant-bound
summary data, caller deadlines, and the read-only capability boundary. It does
not issue destructive commands. Deterministic retry, unknown-outcome, and
reconciliation fault cases remain local connector/service tests because the
currently deployed Vault does not publish erase or operation-status routes.
With `AERO_ID_VAULT_CONTRACT_REQUIRED=true`, an absent endpoint fails instead
of silently skipping the gate.

## Layout

| Resource | Purpose |
|---|---|
| `aero-id-api` Deployment + Service | HTTP :8080 / gRPC :9090, stateless, 3 replicas |
| `aero-id-api` HPA | P6-10: CPU 65% / memory 70%, 3–20 replicas |
| `aero-id-api` PDB | minAvailable 2, zone-spread via topologySpreadConstraints |
| `aero-id-workers` Deployment | callbacks/audit-publication/operations/sync/gc workers; DB leases fence concurrent workers |
| `migration-job.yaml` (one-shot) | release-image migration/version Job; rendered and applied separately |
| `aero-id-postgres` StatefulSet | regional single-writer PostgreSQL 16, 50Gi volume |
| `aero-id-redis` | rate-limit/cache/lease coordination; correctness stays in PostgreSQL |
| `aero-id-nats` | JetStream stream `aero_events`, subjects `aero.events.canonical` and `aero.events.canonical.dead-letter` |
| Ingress | `api.<region>.example.com` -> service |

## Deployment and rollback sequencing

The current relay requires migration `000069` before any new-version process
starts. The migration is additive but rollout is intentionally quiesced: an old
relay ignores `next_attempt_at`, while a new relay must never share a queue with
it.

### Forward rollout

A release bundle contains the rendered manifest, the exact application image
digest, and the migration image/artifact from the same commit. Do not use a tag
or `kubectl set image` with a tag. For a schema-changing rollout, use the
renderer pause mode so the new ConfigMap and pod template are installed while
API and worker replicas remain zero:

```bash
set -euo pipefail

wait_for_no_pods() {
  local selector=$1 pods
  for _ in {1..60}; do
    pods=$(kubectl -n aero-id get pods -l "$selector" -o name)
    [[ -z "$pods" ]] && return 0
    sleep 2
  done
  echo "writers did not terminate for selector $selector" >&2
  return 1
}

# Capture the exact rollback bundle before changing anything. Record the
# Deployment revisions as well as the old ConfigMap; revision undo restores the
# old pod template without changing the current replica count.
kubectl -n aero-id get configmap/aero-id-config -o yaml > rollback-configmap.yaml
kubectl -n aero-id get deploy/aero-id-api deploy/aero-id-workers \
  -o yaml > rollback-deployments.yaml
kubectl -n aero-id get hpa/aero-id-api -o yaml > rollback-hpa.yaml
OLD_API_REV=$(kubectl -n aero-id get deploy/aero-id-api \
  -o jsonpath='{.metadata.annotations.deployment\\.kubernetes\\.io/revision}')
OLD_WORKER_REV=$(kubectl -n aero-id get deploy/aero-id-workers \
  -o jsonpath='{.metadata.annotations.deployment\\.kubernetes\\.io/revision}')
printf '%s\n%s\n' "$OLD_API_REV" "$OLD_WORKER_REV" > rollback-revisions.txt

# Prevent the HPA from recreating API pods while writers are quiesced.
kubectl -n aero-id delete hpa/aero-id-api --ignore-not-found
kubectl -n aero-id scale deploy/aero-id-workers --replicas=0
kubectl -n aero-id scale deploy/aero-id-api --replicas=0
kubectl -n aero-id rollout status deploy/aero-id-workers --timeout=2m
kubectl -n aero-id rollout status deploy/aero-id-api --timeout=2m
wait_for_no_pods 'app=aero-id,tier=workers'
wait_for_no_pods 'app=aero-id,tier=api'

# Render after every ConfigMap/overlay edit. This also pins the preflight
# image to the same digest as the application image. The paused renderer omits
# the HPA, so it cannot recreate API pods during the migration window.
AERO_ID_PAUSE_WORKLOADS=1 make render-k8s \
  REGION="$REGION" AERO_ID_IMAGE_DIGEST="$AERO_ID_IMAGE_DIGEST" \
  OUTPUT="$OUTPUT.paused"
AERO_ID_PAUSE_WORKLOADS=0 make render-k8s \
  REGION="$REGION" AERO_ID_IMAGE_DIGEST="$AERO_ID_IMAGE_DIGEST" \
  OUTPUT="$OUTPUT"
make render-migration-k8s RELEASE="$RELEASE" \
  AERO_ID_IMAGE_DIGEST="$AERO_ID_IMAGE_DIGEST" \
  MIGRATION_ACTION=up OUTPUT="$OUTPUT.migration"
kubectl apply --dry-run=server -f "$OUTPUT.paused"
kubectl apply -f "$OUTPUT.paused"
kubectl apply --dry-run=server -f "$OUTPUT.migration"

# Run exactly once with the release image and production Secret. The rendered
# Job executes /app/migrate and keeps the DSN out of both the command line and
# the manifest. A unique RELEASE is required because completed Jobs are not
# replaced by apply.
kubectl apply -f "$OUTPUT.migration"
kubectl -n aero-id wait --for=condition=complete \
  job/aero-id-migrate-${RELEASE}-up --timeout=15m
kubectl -n aero-id logs job/aero-id-migrate-${RELEASE}-up

# Apply the normal, preflight-gated release only after the migration succeeds.
# This sets replicas and recreates the HPA in one release artifact.
kubectl apply -f "$OUTPUT"
kubectl -n aero-id rollout status deploy/aero-id-api --timeout=10m
kubectl -n aero-id rollout status deploy/aero-id-workers --timeout=10m
```

For an initial install, apply the paused render, run the rendered migration
Job, then apply the normal render. A failed init preflight keeps the pod in
`Init:` and no API or worker process starts. Run `migrate validate` for
migration-file/bookkeeping drift and verify the required
`outbox_events.next_attempt_at` column is present and `NOT NULL` before
resuming writers.

### Rollback

Rollback must restore a **version-compatible release bundle**, not just swap an
image. Older binaries use `UnmarshalExact`; they reject the newer
`capabilities`, OAuth, and other config keys. Capture and retain the previous
ConfigMap as part of every release bundle; do not synthesize compatibility by
removing one newly observed key from the current config. Also, releases before
this change may not contain `/app/preflight`, so do not attach the new init
container to an old image.

```bash
set -euo pipefail

wait_for_no_pods() {
  local selector=$1 pods
  for _ in {1..60}; do
    pods=$(kubectl -n aero-id get pods -l "$selector" -o name)
    [[ -z "$pods" ]] && return 0
    sleep 2
  done
  echo "writers did not terminate for selector $selector" >&2
  return 1
}

# Stop every API/worker first, and keep the HPA from bringing API pods back.
kubectl -n aero-id delete hpa/aero-id-api --ignore-not-found
kubectl -n aero-id scale deploy/aero-id-workers --replicas=0
kubectl -n aero-id scale deploy/aero-id-api --replicas=0
kubectl -n aero-id rollout status deploy/aero-id-workers --timeout=2m
kubectl -n aero-id rollout status deploy/aero-id-api --timeout=2m
wait_for_no_pods 'app=aero-id,tier=workers'
wait_for_no_pods 'app=aero-id,tier=api'

# Restore the previous ConfigMap, including removal of fields unknown to the
# old binary. Do this while all replicas are zero.
kubectl apply -f rollback-configmap.yaml

# Restore the old pod templates by Deployment revision. rollout undo changes
# only the template and preserves replicas=0; it also removes the new init
# container when the previous release predates /app/preflight. Never merely set
# OLD_DIGEST on the current template, and never apply an exported Deployment
# with its old replicas before the schema is downgraded.
kubectl -n aero-id rollout undo deploy/aero-id-api --to-revision="$OLD_API_REV"
kubectl -n aero-id rollout undo deploy/aero-id-workers --to-revision="$OLD_WORKER_REV"
kubectl -n aero-id get deploy/aero-id-api deploy/aero-id-workers \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.replicas}{" "}{.spec.template.spec.containers[0].image}{"\n"}{end}'

# Use the new release's migration image/artifact for the down migration, not
# an old image that predates the schema change. Set the count from the release
# manifest/change record; do not blindly roll back an unknown number of
# migrations. For example, returning from 000070 to 000068 requires 2.
export ROLLBACK_DOWN_COUNT=2
make render-migration-k8s RELEASE="$RELEASE" \
  AERO_ID_IMAGE_DIGEST="$AERO_ID_IMAGE_DIGEST" \
  MIGRATION_ACTION=down MIGRATION_COUNT="$ROLLBACK_DOWN_COUNT" \
  OUTPUT="$OUTPUT.rollback"
kubectl apply --dry-run=server -f "$OUTPUT.rollback"
kubectl apply -f "$OUTPUT.rollback"
kubectl -n aero-id wait --for=condition=complete \
  job/aero-id-migrate-${RELEASE}-down-${ROLLBACK_DOWN_COUNT} --timeout=15m
kubectl -n aero-id logs job/aero-id-migrate-${RELEASE}-down-${ROLLBACK_DOWN_COUNT}

# Verify the target schema with the same release migration image while every
# writer remains stopped. `version` is a read-only Job and uses the restored,
# old-compatible ConfigMap.
make render-migration-k8s RELEASE="$RELEASE" \
  AERO_ID_IMAGE_DIGEST="$AERO_ID_IMAGE_DIGEST" \
  MIGRATION_ACTION=version OUTPUT="$OUTPUT.version"
kubectl apply --dry-run=server -f "$OUTPUT.version"
kubectl apply -f "$OUTPUT.version"
kubectl -n aero-id wait --for=condition=complete \
  job/aero-id-migrate-${RELEASE}-version --timeout=15m
kubectl -n aero-id logs job/aero-id-migrate-${RELEASE}-version

# Start only the restored old release. Reapply its HPA only after it is healthy.
kubectl -n aero-id scale deploy/aero-id-api --replicas=3
kubectl -n aero-id scale deploy/aero-id-workers --replicas=2
kubectl -n aero-id apply -f /path/to/previous-release/hpa.yaml
kubectl -n aero-id rollout status deploy/aero-id-api --timeout=10m
kubectl -n aero-id rollout status deploy/aero-id-workers --timeout=10m
```

The rollback ConfigMap must be restored before any old process starts; do not
leave new-only keys such as `sources.aero_vault.capabilities` in it. Abort and
restore the new schema if any new-version pod is still running, if the
migration Job fails, or if startup reports a shape mismatch. Never run old and
new relay implementations concurrently. Keep the captured ConfigMap, revision
IDs, rendered manifests, and migration result as the rollback evidence.

## Operational notes

- **Migrations**: run `cmd/migrate` as a Job per region before rolling the API
  (config `database.auto_migrate: false`). Never edit applied migrations. For
  outbox migration `000069`, quiesce all old and new API/worker instances before
  the migration, then deploy the new binary; see
  `docs/runbooks/outbox-retry-eligibility.md`. Rollback requires stopping every
  new-version writer before running the down migration.
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
  provision it via a secret provider before applying the cell. Feature source
  secret references are optional only when the corresponding endpoint is
  disabled; the init preflight is the fail-closed check after an overlay
  enables one. Environment variables are captured at process start, so rotate
  a required or source secret only with an explicit rollout of both API and
  worker Deployments (`kubectl rollout restart deploy/aero-id-api deploy/aero-id-workers`)
  or a new rendered release; do not rely on a Secret update alone. Never commit real
  keys or placeholder secret values.
- **Release integrity**: verify the image digest against the registry/signature
  policy and retain the rendered manifest plus its `checksum/config` value.
  A ConfigMap or Secret update alone is not a rollout; produce and apply a new
  rendered release bundle.
