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
- Docker (for integration tests and local stack)
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` (only to regenerate protos)
- `migrate` CLI (golang-migrate) for running migrations locally

## Quickstart (Docker)

```bash
export POSTGRES_PASSWORD='local-db-password'
export MINIO_ROOT_USER='local-minio-user'
export MINIO_ROOT_PASSWORD='local-minio-password'
export AERO_ID_SIGNING_KEY="$(openssl rand -hex 32)"
export AERO_ID_AUDIT_LEDGER_SIGNING_KEY="$(openssl rand -hex 32)"
export AERO_ID_SSO_ENDPOINT='host.docker.internal:9000' # Snaplink gRPC
docker compose -f deployments/docker-compose.yml up --build
curl -s localhost:8080/healthz        # {"status":"ok"}
curl -s localhost:8080/readyz         # {"status":"ready"} once Postgres is up
```

## Local development

```bash
export AERO_ID_DATABASE_PASSWORD='local-db-password'
export AERO_ID_SIGNING_KEY="$(openssl rand -hex 32)"
export AERO_ID_AUDIT_LEDGER_SIGNING_KEY="$(openssl rand -hex 32)"
export AERO_ID_SSO_ENDPOINT='127.0.0.1:9000' # Snaplink or a test double
make migrate-up        # apply DDL to a local Postgres at 127.0.0.1:5432
make run               # run the server with configs/config.yaml
make test              # unit tests
make test-integration  # integration tests (spins up Postgres/MinIO via testcontainers)
make proto             # regenerate gRPC stubs
```

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
