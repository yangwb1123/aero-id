# Degradation Matrix — aero-id

> **Status:** Living document — update when endpoints or dependencies change.
> **Last updated:** 2026-07-12

This document defines the **declared behaviour** of every aero-id endpoint when an
upstream dependency (Postgres, SSO, S3/local storage) is degraded or unavailable.
It serves as the contract between operations, developers, and client authors.

---

## Legend

| Column | Meaning |
|---|---|
| **Endpoint** | The HTTP path pattern or gRPC method name |
| **Deps** | Upstream dependencies: `DB` (Postgres), `SSO`, `S3`/`Local` (storage) |
| **DB故障** | Behaviour when Postgres is unreachable, timing out, or returning errors |
| **SSO故障** | Behaviour when the SSO gRPC endpoint is unreachable or the token-cache is stale |
| **S3故障** | Behaviour when the storage backend is unreachable or returns errors |
| **已实现** | Whether this behaviour is coded today (✅ = done, 🟡 = partial, ❌ = not implemented) |

---

## Health / Ops Endpoints

| Endpoint | Deps | DB故障 | SSO故障 | S3故障 | 已实现 |
|---|---|---|---|---|---|
| `GET /healthz` | None | Always 200 | Always 200 | Always 200 | ✅ |
| `GET /readyz` | DB, SSO | 503 if DB probe fails | 503 **only** when `sso.readiness_critical=true` | N/A (excluded from readiness) | ✅ |
| `GET /metrics` | None | Always 200 | Always 200 | Always 200 | ✅ |

---

## File Endpoints

| Endpoint | Deps | DB故障 | SSO故障 | S3故障 | 已实现 |
|---|---|---|---|---|---|
| `GET /files/dl` (`/files/dl`) | None (HMAC-signed) | 200 (no DB/SSO needed) | 200 (no DB/SSO needed) | 200 (local: signed token verified; S3: redirect) | ✅ |
| `POST /v1/files` (upload) | DB, Storage, SSO | 503 | 401 (can't validate token) | 503 | ✅ |
| `GET /v1/files/:id` (metadata) | DB, SSO | 503 (can't read) | 401 (can't validate token) — stale cache: 200(stale) | N/A | ✅ |
| `GET /v1/files/:id?mode=stream` (download) | DB, Storage, SSO | 503 | 401 — stale cache: 200(stale) | 503 | ✅ |
| `GET /v1/files/:id?mode=presign` (presigned) | DB, SSO | 503 | 401 — stale cache: 200(stale) | 503 (can't sign S3 URL) | ✅ |
| `DELETE /v1/files/:id` | DB, Storage, SSO | 503 | 401 — stale cache: 200 if owner matches stale identity | 503 (delete skipped, row marked) | ✅ |
| `PATCH /v1/files/:id` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `POST /v1/files/:id/share` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `DELETE /v1/files/:id/share` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `GET /v1/files/:id/shares` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `GET /v1/files` (list) | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |

### File-specific notes

- **`/files/dl`** (HMAC signature verified) is the **only** fully public endpoint. It
  requires no DB, no SSO, and no bearer token. The HMAC-signing key is checked in
  middleware, then the file is streamed from local storage or redirected to S3.
- Storage failures during upload are recoverable — the file row is never created
  without a successful storage write.
- Storage failures during delete are partially recoverable: the DB row is soft-deleted
  first, then the blob delete is best-effort. The GC sweep cleans up orphaned blobs.

---

## Message Endpoints

| Endpoint | Deps | DB故障 | SSO故障 | S3故障 | 已实现 |
|---|---|---|---|---|---|
| `POST /v1/messages` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `GET /v1/messages/inbox` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `GET /v1/messages/outbox` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `PATCH /v1/messages/:id` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `DELETE /v1/messages/:id` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `POST /v1/messages/:id/react` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `DELETE /v1/messages/:id/react` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `GET /v1/messages/unread` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |
| `POST /v1/conversations/read` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | ✅ |

---

## Notification Endpoints

| Endpoint | Deps | DB故障 | SSO故障 | S3故障 | 已实现 |
|---|---|---|---|---|---|
| `GET /v1/notifications` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `GET /v1/notifications/unread` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `PATCH /v1/notifications/:id/read` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `POST /v1/notifications/read-all` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |

---

## Comment Endpoints

| Endpoint | Deps | DB故障 | SSO故障 | S3故障 | 已实现 |
|---|---|---|---|---|---|
| `POST /v1/comments` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `GET /comments` (by target) | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `PATCH /v1/comments/:id` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `DELETE /v1/comments/:id` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `POST /v1/comments/:id/like` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `DELETE /v1/comments/:id/like` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `POST /v1/comments/:id/report` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |

---

## Block Endpoints

| Endpoint | Deps | DB故障 | SSO故障 | S3故障 | 已实现 |
|---|---|---|---|---|---|
| `POST /v1/blocks` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `DELETE /v1/blocks/:uid` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `GET /v1/blocks` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |

---

## Preference Endpoints

| Endpoint | Deps | DB故障 | SSO故障 | S3故障 | 已实现 |
|---|---|---|---|---|---|
| `GET /v1/preferences` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `PUT /v1/preferences` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |

---

## Search Endpoints

| Endpoint | Deps | DB故障 | SSO故障 | S3故障 | 已实现 |
|---|---|---|---|---|---|
| `GET /v1/search` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |

---

## Admin / Audit Endpoints

| Endpoint | Deps | DB故障 | SSO故障 | S3故障 | 已实现 |
|---|---|---|---|---|---|
| `GET /v1/admin/audit` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `POST /v1/admin/roles` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |
| `DELETE /v1/admin/roles` | DB, SSO | 503 | 401 — stale cache: 200(stale) | N/A | 🟡 |

---

## gRPC Endpoints (equivalents)

All gRPC methods mirror their HTTP counterparts one-to-one. The degradation
behaviour is **identical**; see the HTTP row for the matching method.

The only exception is the gRPC **Health/Watch** endpoint:
- Health check (`/grpc.health.v1.Health/Check`): always returns SERVING (process
  alive), no dependencies.

---

## Response Format

### HTTP

When a request is degraded (stale data served, partial response), the response
carries:

```
X-Aero-Degraded: db,sso     # comma-separated list of degraded dependencies
X-Aero-Degraded: none        # everything healthy
```

The HTTP status code follows the matrix above — read endpoints may return
**200 (stale)**, while write endpoints return **503 Service Unavailable**.

### gRPC

The gRPC response carries the same information in trailing metadata:

```
x-aero-degraded: db,sso
```

Status codes follow the gRPC equivalents of the HTTP codes (e.g. `Unavailable`
for 503, `Unauthenticated` for 401).

---

## Implementation Status

- ✅ **Implemented**: The logic is coded and tested.
- 🟡 **Partial**: The handler has degradation awareness for some operations but
  not all (e.g. read paths without stale cache fallback).
- ❌ **Not implemented**: The endpoint has no degradation awareness.

**Status (2026-07-12):** The degradation state manager, middleware, gRPC
interceptor, and handler adaptation helpers (`DegradationAction`,
`AbortIfDegraded`) are all implemented. File and message handlers have
write-path degradation checks wired. Other handler domains (comment,
notification, block, preference, search) still use default error propagation
and will be wired incrementally as stale-cache fallbacks are added.
