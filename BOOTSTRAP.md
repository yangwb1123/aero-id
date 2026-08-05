# BOOTSTRAP.md — Project Charter

## Mission

aero-id is the **data-plane service** in the Aero platform: it owns every byte
that belongs to a user account — files, messages, comments — while identity
(authentication, user profiles) lives in an external **SSO** service.

## Tech Stack (pinned)

| Component | Choice | Version |
|---|---|---|
| Language | Go | 1.26 |
| HTTP framework | Gin | v1.10 |
| gRPC | google.golang.org/grpc | v1.80 |
| ORM | GORM | latest |
| Database | PostgreSQL | 16 |
| Object storage | S3-compatible (MinIO for dev) | — |
| Module path | `github.com/aero/aero-id` | — |

## Engineering Principles

1. **SSO owns identity** — aero-id stores no account rows.
2. **Clean Architecture** — handler → service → repository, strict one-way deps.
3. **Dual transport parity** — every HTTP endpoint has a gRPC equivalent.
4. **Test-driven** — unit tests beside code, integration via testcontainers.
5. **No god files** — every file ≤ 500 lines, function ≤ 50 lines, complexity ≤ 10.
6. **Outbox-driven side effects** — notifications, read receipts, and audit events flow through the transactional outbox.
7. **Secrets via env only** — never committed, never hardcoded.

## Current State

- **Phase**: Post-MVP, production hardening
- **gRPC RPCs**: 38 across 5 services (full HTTP↔gRPC parity)
- **Migrations**: 17, all reversible (up + down)
- **Test coverage**: ~39% unit, integration tests via testcontainers
- **Branch**: `main` (feature-complete)

## Key Files

| File | Purpose |
|---|---|
| `AGENTS.md` | Agent behavioral rules + engineering constraints |
| `HARNESS.md` | Auto-check gate (must pass before code is accepted) |
| `docs/ARCHITECTURE.md` | Detailed system architecture |
| `ROADMAP.md` | Feature roadmap + delivery status |
| `.skills/` | Reusable engineering skill cards |

## What to Read First

1. This file (BOOTSTRAP.md)
2. `AGENTS.md` — constraints and invariants
3. `HARNESS.md` — what must pass before code is accepted
4. `docs/ARCHITECTURE.md` — system design
5. `.skills/clean-architecture.md` — dependency rules
