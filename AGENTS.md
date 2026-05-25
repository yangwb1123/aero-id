# AGENTS.md — aero-id

Go service for **data owned by accounts** (files, messages, comments). Identity lives in
an external **SSO**: aero-id stores no account rows — every record references an SSO `uid`
(`owner_uid`, or `from_uid`/`to_uid`). aero-id calls SSO over gRPC to validate tokens and
batch-resolve users.

Human docs live in `README.md` (API tables + quickstart); the canonical design is
`docs/superpowers/specs/2026-05-22-aero-id-design.md`. **This file is the agent-facing
contract** — invariants, conventions, gotchas. Don't restate the README.

## Commands

```bash
make test              # unit tests (go test ./... -count=1) — no Docker needed
make test-integration  # -tags=integration; starts Postgres + MinIO via testcontainers (needs Docker)
make lint              # go vet ./...
make build             # -> bin/aero-id
make run               # go run ./cmd/server -config configs/config.yaml
make migrate-up        # apply migrations to local Postgres (see Gotchas: NOT auto-applied)
make proto             # regenerate gRPC stubs — ONLY after editing api/proto/**/*.proto
```

Before claiming work is done, the gate is `make test && make lint`.

## Architecture — strict one-way dependency

```
handler/{http,grpc}  →  service  →  repository  →  Postgres (GORM)
                             ├──→  storage  (local | s3)
                             └──→  sso      (gRPC client + caches)
```

- **handler** — transport I/O only: decode, call service, render response/error. No business logic.
- **service** — ALL business logic + authorization (ownership checks). Single source of truth; HTTP and gRPC share it and must behave identically.
- **repository** — GORM persistence; returns **raw** errors (incl. `gorm.ErrRecordNotFound`).
- **storage** — `FileStorage` interface: `local` (HMAC-signed presigned URLs) or `s3` (MinIO-compatible).
- **sso** — lazy-dial gRPC client; `TokenCache` (TTL-capped) + batch `UserCache`.

`internal/` layout: `app` (composition root) · `config` · `domain` (entities + invariants) ·
`handler/{http,grpc}` · `middleware` · `repository`(+`testpg`) · `service`(+`mocks`) · `sso` ·
`storage` · `pkg/{db,errors,httpx,idgen,logger}`. Entrypoint: `cmd/server/main.go`.

## Conventions

- **Errors**: repo returns raw → service translates to `*pkg/errors.AppError`
  (`BadRequest`/`Unauthorized`/`Forbidden`/`NotFound`/`Conflict`/`Internal`, each carrying HTTP +
  gRPC codes) via `.Wrap(cause)` → handler renders. Never surface raw DB errors past the service.
- **IDs & pagination**: UUIDv7 primary keys (`pkg/idgen`) double as opaque cursors. List =
  `WHERE id < cursor ORDER BY id DESC LIMIT n+1` (the +1 detects the next page).
- **Soft deletes**: `gorm.DeletedAt` everywhere; deletes are soft.
- **Config**: `configs/config.yaml`, every key overridable by `AERO_ID_*` env (`.` → `_`).
  `config.Load` validates required keys at startup.

## Invariants — do not break

- **SSO owns identity.** No accounts table, no FK to accounts; reference users only by `uid`
  string. aero-id **never signs user tokens** — SSO does.
- **Auth**: every `/v1/*` route needs a Bearer token (validated via SSO). Only `/healthz`,
  `/readyz`, and `/files/dl` (HMAC-signature-verified, no token) are public. A validated-but-empty
  `uid` is rejected on **both** transports.
- **Secrets via env only**, never committed: `AERO_ID_DATABASE_PASSWORD`, `AERO_ID_SIGNING_KEY`,
  `AERO_ID_S3_ACCESS_KEY`/`_SECRET_KEY`. `SIGNING_KEY` is used *solely* for local-storage
  presigned-download HMAC — not for tokens.
- **Comments nest exactly two levels** (top-level + one reply layer, 楼中楼). Replying to a reply →
  `comment.nesting_too_deep`. Enforced in `domain` AND `service`.

## Testing

- Unit tests sit beside code; service tests use the fakes in `internal/service/mocks`.
- Integration tests carry `//go:build integration` and use testcontainers — real Postgres
  (`internal/repository/testpg`) and MinIO. `testpg` is integration-only and is excluded from the
  default `go build/test/vet ./...`, so those commands stay green without Docker.
- After changing repo SQL/queries or a storage backend, run `make test-integration`.

## Gotchas

- **Generated protobuf is committed** (`internal/handler/grpc/aeropb`, `internal/sso/ssopb`). Don't
  hand-edit `*.pb.go`. Edit the `.proto`, then `make proto` — needs `protoc`,
  `protoc-gen-go@v1.34.1` (pinned to the `google.golang.org/protobuf` version in `go.mod`) and
  `protoc-gen-go-grpc@v1.5.1`.
- **Migrations are not auto-applied** at startup — run `make migrate-up` (or the compose `migrate`
  job). Every `*.up.sql` needs a matching `*.down.sql`.
- Build via `make build` / `go build ./...` (output lands in gitignored `bin/`) — don't leave stray
  binaries at the repo root.
- Pinned to **Go 1.26**, module `github.com/aero/aero-id`.
