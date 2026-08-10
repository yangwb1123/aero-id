# HARNESS.md — Auto-check Harness

Before any code is accepted, ALL of these checks must pass.

## Required Checks

```bash
make test                # unit tests
make lint                # go vet
make lint-strict         # golangci-lint (complexity, function length, static analysis)
make check-file-sizes    # file ≤ 500 lines (non-test, non-generated)
make check-architecture  # live-layer boundaries (rules a-e): dead-tree import ban, domain purity, repository/service, infra layers, handler/repository
make build               # compile
```

## Architecture — strict one-way dependency

```
handler  →  feature/service  →  feature/repository  →  Postgres (GORM)
                                   ├→  storage (local | s3)
                                   └→  sso (gRPC client + caches)
```

Where each feature is a self-contained package under `internal/`:
`file`, `message`, `comment`, `block`, `notification`, `search`.

Rules:
- `internal/domain/` must import nothing from `internal/` (only external deps + `internal/pkg/`)
- Feature packages must not import from other feature packages.
- `internal/audit/` may import only `internal/domain/` + GORM.
- `internal/handler/` must NOT be imported by any non-handler package.
- `internal/app/` is the composition root; nothing imports it.
- `internal/pkg/` packages must not import from feature packages.

## File Size Constraints

- Any `.go` file > 500 lines → FAIL (excl. generated protobuf)
- Any function > 50 lines → FAIL
- Any function with cyclomatic complexity > 10 → FAIL
- Package average complexity ≤ 5.0 → FAIL

## Gate

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-architecture && go build ./...
```

If any check fails, **stop feature work and fix the constraint violation first**.
Refactoring priority is always higher than new feature development.
