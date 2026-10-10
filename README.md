# aero-id

`aero-id` is evolving into a distributed account aggregation and control plane.
Snaplink remains the authentication and global authorization authority; Aero IM
and Aero Vault remain authoritative for messaging and object storage. aero-id
owns account links, selected profile/preferences, local projections,
cross-application operations, synchronization, callbacks, and the durable audit
ledger.

The existing files, messages, comments, notifications, and search APIs remain
compatibility surfaces while source-system migration is completed. They are not
new canonical Aero Vault or Aero IM data authorities.

## Architecture

Two transports share the same service layer:

```
HTTP (Gin, :8080)  ─┐
                    ├─►  service  ──►  repository (GORM/Postgres)
gRPC (:9090)       ─┘                └►  storage (local | S3)
                                     ├►  source connectors (Snaplink / IM / Vault)
                                     └►  sso (JWKS or gRPC client + caches)
```

- **handler** (`internal/handler/{http,grpc}`) — transport-specific I/O only.
- **service** (`internal/service`) — all business logic and authorization.
- **repository** (`internal/repository`) — GORM persistence on PostgreSQL 16.
- **storage** (`internal/storage`) — `FileStorage` with `local` (HMAC-signed
  presigned URLs) and `s3` (MinIO-compatible) backends.
- **sso** (`internal/sso`) — Snaplink token/AuthZ client with bounded concurrency,
  caching, retry budget, and circuit breaking.
- **connectors** (`internal/connector`) — public source API anti-corruption
  adapters; they never access source databases.
- **projections and operations** — local eventual/bounded-stale views and
  durable Saga state for cross-source commands.

The target architecture and migration sequence are in
[`docs/plans/2026-08-04-distributed-account-aggregation.md`](docs/plans/2026-08-04-distributed-account-aggregation.md).
The 2026-05-22 design under `docs/superpowers/specs/` is historical input.

## Prerequisites

- Go 1.26+
- Node.js 22+ and pnpm 10.34+ (account Web console)
- Docker (for integration tests and local stack)
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` (only to regenerate protos)
- An existing Snaplink deployment, registered aero-id audience/AuthZ policies,
  and an independent console OAuth client

Use the shipped `cmd/migrate` runner (`make migrate-up`), not the unrelated
`golang-migrate` CLI: their `schema_migrations` bookkeeping contracts differ.

## Quickstart (Docker)

This starts PostgreSQL, MinIO, the **shipped migration runner**, and aero-id.
It does not start Snaplink or enable source connectors. Configure real verified
identity first; a gRPC endpoint alone cannot authenticate control-plane routes.

```bash
cp deployments/.env.example .env
chmod 600 .env
# Fill .env with unique DB/MinIO/signing secrets and actual Snaplink settings.
# Generate keys once (openssl rand -hex 32), then retain them across restarts.
docker compose --env-file .env -f deployments/docker-compose.yml config --quiet
docker compose --env-file .env -f deployments/docker-compose.yml up --build
curl -fsS localhost:8080/healthz
curl -fsS localhost:8080/readyz
curl -fsS localhost:8080/health/sources
```

The Compose environment requires JWKS URL, exact issuer, audience and a registered
AuthZ client; account issuer is kept identical to the token issuer. Global AuthZ
is required and legacy fallback disabled. `host.docker.internal` is mapped on
Linux, but Snaplink must listen on an address reachable from the containers.
Use `AERO_ID_SSO_INSECURE=true` only for an explicitly trusted local gRPC test
server; public CA TLS is the default. Private CA/mTLS needs mounted certificates
and the corresponding SSO TLS settings. These development images/settings are
not a substitute for the pinned Kubernetes release process.

Readiness checks configured required dependencies, not merely PostgreSQL. A
registered dataset or green process health does not prove a source is enabled,
authorized, fresh, or production-accepted.

## Local development

Provision a local PostgreSQL and export the retained secrets plus these verified
Snaplink values (replace endpoints/identifiers with your deployment):

```bash
export AERO_ID_DATABASE_PASSWORD='<your local DB password>'
export AERO_ID_SIGNING_KEY='<retained URL-signing key>'
export AERO_ID_AUDIT_LEDGER_SIGNING_KEY='<retained ledger-signing key>'
export AERO_ID_SSO_ENDPOINT='snaplink.example.com:443'
export AERO_ID_SSO_JWKS_URL='https://snaplink.example.com/.well-known/jwks.json'
export AERO_ID_SSO_ISSUER='https://snaplink.example.com'
export AERO_ID_ACCOUNT_ISSUER="$AERO_ID_SSO_ISSUER"
export AERO_ID_SSO_AUDIENCE='aero-id'
export AERO_ID_SSO_AUTHZ_CLIENT_ID='aero-id'
export AERO_ID_SSO_INSECURE=false
export AERO_ID_SSO_AUTHZ_REQUIRED=true
export AERO_ID_SSO_AUTHZ_LEGACY_FALLBACK=false
export AERO_ID_SERVER_HTTP_CORS_ORIGINS='http://localhost:5180'
make migrate-up        # shipped runner; explicit DDL through migration 000072
make migrate-verify    # deployed relay and command-receipt shape
make run               # configs/config.yaml + AERO_ID_* overrides
make test              # no Docker or real source calls
make test-integration  # real disposable PostgreSQL/MinIO fixtures
```

Tokens must include verified issuer, subject, tenant, client and the registered
API audience. Source enablement is a separate step in
[`docs/contracts/source-account-summary.md`](docs/contracts/source-account-summary.md).
Start with one verified read-only source, then sync and inspect its freshness/audit
facts. Export needs `AERO_ID_EXPORT_ENCRYPTION_KEY`; Callback registration needs
`AERO_ID_CALLBACK_MASTER_KEY`.

## Web console

The independent account console lives in [`web/`](web/README.md). It uses
Snaplink Authorization Code + PKCE for login and calls only aero-id's `/v1`
account-control-plane APIs. Install and run it with:

```bash
cp web/.env.example web/.env.local
# Configure actual Snaplink issuer/Hosted Login, independent public client,
# exact redirect URI and VITE_AERO_ID_API_BASE=http://localhost:8080/v1.
make web-install
make web-dev            # http://localhost:5180
make web-check          # tests, lint, typecheck, format and production build
```

Register a tenant-bound console client instead of reusing `snaplink-console`.
OAuth scopes alone do not grant administrator permissions.

On this workstation, `cd web && pnpm dev:iris` resolves Iris UI directly from
`~/iris-ui` for component development. Normal builds consume the published,
versioned Iris packages and do not depend on that local path.

## Configuration

Config is read from `configs/config.yaml`, with any value overridable by an
`AERO_ID_*` environment variable. The committed config intentionally contains
no database password or signing keys. Required secrets include
`AERO_ID_DATABASE_PASSWORD`, `AERO_ID_SIGNING_KEY`, and
`AERO_ID_AUDIT_LEDGER_SIGNING_KEY`; production-like environments additionally
require encrypted database transport, TLS SSO, and Snaplink AuthZ with legacy
fallback disabled.

The target account-control-plane HTTP contract is in
[`api/openapi/aero-id-account.yaml`](api/openapi/aero-id-account.yaml); source
integration semantics are in
[`docs/contracts/source-account-summary.md`](docs/contracts/source-account-summary.md).

## Command retry and export contracts

Operation and Sync creation persist a versioned request fingerprint with their
existing audited receipt. The same normalized intent/key returns its existing
resource; a changed account, action, dataset scope, ordered workflow, payload or
originating client returns `operation.idempotency_conflict` or
`sync.idempotency_conflict` (HTTP 409 / gRPC AlreadyExists). Current authorization
is still checked. These create routes bypass generic HTTP response caching.

Historical receipts without a verified original client fingerprint cannot be
reused: inspect them through GET/list/reconciliation, rather than automatically
minting a new key. Migrations through 000072 are mandatory before starting this version;
`migrate verify` and startup check the actual receipt and command-intent schema. The shipped
`preflight` also shares startup's claims-capable JWKS configuration guard; it
does not prove live Snaplink login/AuthZ or perform database migrations.

`POST /v1/command-intents` creates a caller/client/tenant-bound five-minute preview;
`GET /v1/command-intents/:id` retrieves it. Explicit dataset scope and normalized
ordered steps are persisted without source calls. Execute accepts only the original
version and confirmation digest, reauthorizes/replans, and atomically checks local
metadata/region fencing while creating the existing Operation/Sync receipt and audit.
Confirmed retries return that receipt; they do not automatically run or roll back a
command. Local versions are not causal watermarks; Snaplink policy versions and
source ACL versions remain unsupported/unknown. Operation execution now checks the
durable initiator's current Snaplink action grants before claims and before each
forward/compensation/status-query call, under a shared three-second admission budget.
Ownership or preview eligibility alone cannot renew a delayed execution grant; manual
operators cannot substitute their privileges for the initiator. The production checker
bypasses legacy fallback. Local live-lease/account/directory-epoch checks also fail closed.
Denial/outage preserves original receipts and known/unknown outcomes, records audited
blocking metadata, and paces background retries. Only a claim known not to have sent an
effect can return to pending. Expired forward/compensation attempts become audited
`unknown` and reconcile the original effect rather than reissuing it. Unknown forward
outcomes do not authorize compensation. These checks cannot recall sent bytes or form
a distributed transaction with Snaplink or the source. Sync/Erase/Export/audit-export
workers likewise recheck the stored initiator directly, current target scope and exact
live lease/fence at admission and before calls/publication. Audit exports also require
current `audit.read` at each page. Audited pauses retain original source receipts,
summary replays and artifact key/digest/TTL; only untouched execution claims return
pending, while continuation claims remain unknown. Expired Sync execution leases
commit unknown plus audit before returning the sentinel. Revocation cannot cancel an
already admitted call. Disposal of an expired/invalidated original artifact remains
retention work, not permission to regenerate it. Distributed lifecycle, full integration
and real authority/source/browser acceptance remain separate open gates.

Account exports honor their normalized dataset selection and include core
account/profile plus references for the selected sources. They exhaust bounded
keyset pages (10,000 references total and the configured byte cap), never
silently truncate. Artifacts expose selected/missing datasets, counts, a content
digest and coverage/traversal completeness; they explicitly are **not** a global
point-in-time snapshot. Job responses store/show metadata only; contents are in
the encrypted, TTL-bound artifact. Deterministic storage writes checkpoint the
original key/digest/size/TTL before Put; unknown recovery reads only that object,
never regenerating or extending it. Unknown disposal retries delete the old
artifact. Final local publication/download admission shares the account erase
lock; one-time consumption and successful-read audit commit atomically. A read
admitted before erase can still arrive afterwards; already-sent bytes cannot be
recalled. These local guards do not prove distributed erasure.

Local erased-account locks fence profile/projection writes and quarantine late
source events without their content. Cross-region disposition propagation,
historical retention cleanup, source deletion proof and Legal Hold acceptance
remain distinct follow-ups—not a claim of complete distributed erasure.

Implementation progress and remaining acceptance work:
[`docs/plans/control-plane-production-readiness.md`](docs/plans/control-plane-production-readiness.md).

## Legacy compatibility HTTP API (all under `/v1`, Bearer token required)

The table below describes the currently shipped compatibility surface. New
account-control-plane endpoints are defined by the OpenAPI contract linked in
the Configuration section; source systems remain authoritative for messaging
and object storage.

| Method | Path | Description |
|---|---|---|
| POST | `/v1/files` | Upload a file (multipart `file`) |
| GET | `/v1/files` | List your files |
| GET | `/v1/files/:id` | File metadata |
| GET | `/v1/files/:id/url` | Presigned download URL |
| DELETE | `/v1/files/:id` | Delete a file |
| POST | `/v1/messages` | Send a message |
| GET | `/v1/messages` | Inbox (`?unread=true`) |
| GET | `/v1/messages/:id` | Get a message |
| POST | `/v1/messages/:id/read` | Mark read |
| GET | `/v1/conversations/:uid` | Conversation with a peer |
| POST | `/v1/comments` | Create a comment (`parent_id` for replies) |
| GET | `/v1/comments?target_type=&target_id=` | Top-level comments |
| GET | `/v1/comments/:id/replies` | Replies under a root comment |
| DELETE | `/v1/comments/:id` | Delete your comment |

`GET /healthz` and `GET /readyz` are unauthenticated. Local presigned downloads
are served at `GET /files/dl` (signature-verified, no token).

The gRPC API (`api/proto/aero_id/v1/aero_id.proto`) mirrors these operations for
internal callers.

## Comment nesting

Comments support exactly two levels (top-level and one reply layer, 楼中楼).
Replying to a reply is rejected with `comment.nesting_too_deep`.
