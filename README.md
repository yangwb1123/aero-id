# aero-id

A Go service that manages **files, messages, and comments** owned by accounts.
Account identity lives in an external **SSO** service, which aero-id calls over
gRPC for token validation and batch user lookups. aero-id stores no account
records of its own — every row references an SSO `uid`.

## Architecture

Horizontally layered, with two transports sharing one service layer:

```
HTTP (Gin, :8080)  ─┐
                    ├─►  service  ──►  repository (GORM/Postgres)
gRPC (:9090)       ─┘                └►  storage (local | S3)
                                     └►  sso (gRPC client + caches)
```

- **handler** (`internal/handler/{http,grpc}`) — transport-specific I/O only.
- **service** (`internal/service`) — all business logic and authorization.
- **repository** (`internal/repository`) — GORM persistence on PostgreSQL 16.
- **storage** (`internal/storage`) — `FileStorage` with `local` (HMAC-signed
  presigned URLs) and `s3` (MinIO-compatible) backends.
- **sso** (`internal/sso`) — gRPC client with a token cache and a batch user cache.

See `docs/superpowers/specs/2026-05-22-aero-id-design.md` for the full design.

## Prerequisites

- Go 1.26+
- Docker (for integration tests and local stack)
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` (only to regenerate protos)
- `migrate` CLI (golang-migrate) for running migrations locally

## Quickstart (Docker)

```bash
docker compose -f deployments/docker-compose.yml up --build
curl -s localhost:8080/healthz        # {"status":"ok"}
curl -s localhost:8080/readyz         # {"status":"ready"} once Postgres is up
```

## Local development

```bash
make migrate-up        # apply DDL to a local Postgres at 127.0.0.1:5432
make run               # run the server with configs/config.yaml
make test              # unit tests
make test-integration  # integration tests (spins up Postgres/MinIO via testcontainers)
make proto             # regenerate gRPC stubs
```

## Configuration

Config is read from `configs/config.yaml`, with any value overridable by an
`AERO_ID_*` environment variable (e.g. `AERO_ID_DATABASE_PASSWORD`,
`AERO_ID_SIGNING_KEY`, `AERO_ID_SSO_ENDPOINT`). Secrets should be supplied via
env, never committed.

## HTTP API (all under `/v1`, Bearer token required)

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
