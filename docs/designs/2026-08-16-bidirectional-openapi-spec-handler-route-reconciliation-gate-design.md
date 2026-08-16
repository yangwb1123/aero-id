# Design — Bidirectional OpenAPI Spec ↔ Handler Route Reconciliation Gate

Status: **Proposed** (implementation-ready)
Date: 2026-08-16
Module: `internal/handler/http` + `api/openapi/aero-id-account.yaml` (+ optional `internal/pkg/httpx` — not needed under Option A)
Campaign: `docs/campaigns/pipeline-openapi-route-reconciliation.yaml`
Spec: "Bidirectional OpenAPI spec ↔ handler route reconciliation gate" (REQ-1..9)
Baseline verified at: HEAD on 2026-08-16; every claim below re-verified against the live tree (file:line given).

---

## 1. Verification verdict (untrusted evidence → tree)

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | Served route `POST /v1/admin/callback-subscriptions/deliveries/{id}/replay`; handler returns 200 `{"status":"queued"}` | **Confirmed** | `callback.go:21-29` (`RegisterRoutes`: group `/admin/callback-subscriptions`, `group.POST("/deliveries/:id/replay", h.Replay)`); `callback.go:82` `httpx.OK(c, gin.H{"status": "queued"})`; `httpx.go:43` `OK` = `http.StatusOK`. |
| E2 | Spec documents `POST /admin/callback-deliveries/{id}/replay` (wrong path) with success `202` | **Confirmed** | `api/openapi/aero-id-account.yaml:323-331` — `operationId: replayCallbackDelivery`, responses `'202': {description: Delivery reset for manual replay.}`. The path is never registered anywhere in `internal/handler/http`. |
| E3 | `openapi_test.go:87` pins the wrong path; the gate is one-directional (spec ⊇ hardcoded subset) | **Confirmed** | `openapi_test.go:55-90` `requiredClientPaths`; entry at line 87 `{"/admin/callback-deliveries/{id}/replay", "POST"}`. `TestOpenAPISpec_CoversClientContract` only asserts the spec **contains** the list. *Delta D2: the list has **32** entries, not 29 (see §11).* |
| E4 | `httpx.OK` = 200; no `httpx.Accepted` exists | **Confirmed** | `httpx.go:43-45` — only `OK`/`Created`/`NoContent`. |
| E5 | 22 served control-plane routes absent from the spec | **Confirmed, all 22 line-verified** | `aggregation.go:26` (`GET /me/resources`), `:34-35` (`/:id/overview`, `/:id/data`); `account.go:47-48` (`GET /admin/accounts`, `GET /admin/accounts/:id`), `:56` (backfill POST), `:74,83-85` (migration/cutover); `sync_job.go:31,33,35` (`/:id/sync|export|erase`); `admin.go:56-58` (audit-log, roles, roles/:uid), `:65-66` (grant/revoke), `:74-76` (bans); `audit_ledger.go:44` (`GET /audit/verify`). |
| E6 | Direction (a): exactly one documented-but-unserved path | **Confirmed** | All 34 spec paths cross-checked path-by-path against every `RegisterRoutes`; only `/admin/callback-deliveries/{id}/replay` is unserved. *Delta D1: spec has **34 paths / 38 operations**, not "38 paths" (§11).* |
| E7 | No full-router inventory test (`router_test.go` only ping + health) | **Confirmed** | `router_test.go:36-95` — `pingRegistrar` + `/healthz`,`/readyz`,`/v1/ping` probes; no `engine.Routes()` enumeration anywhere in the package. |
| E8 | Production wiring reference: `buildRouter` lists all 18 registrars + optional deps | **Confirmed** | `internal/app/handlers.go:46-98` (`buildRouter`); registrar list at 65-97 = SourceHealth, File, Message, Comment, Notification, Block, Events, Preference, Admin, Search, Account(WithExtensions), Aggregation, EventIngest, audit (via `buildAuditLedgerHandler`, `internal/app/audit.go:318-322`), Callback, Operation, SyncJob, Replay(WithProjection). *Delta D3: evidence said "64-109"; actual span 46-98, registrar list 65-97 — same content.* |
| E9 | `RequirePermission`/`RequirePermissionIfConfigured` never dereference the checker at registration time | **Confirmed** | `internal/middleware/permission.go:20-46` — checker dereferenced only inside the returned closure (request time); nil-checked at request start. |
| E10 | Exclusion-surface registrations exist as claimed | **Confirmed** | `message.go:26-36` (incl. `/conversations` 35-36); `notification.go:22-27`; `preference.go:22-23`; `router.go:72-76` (`/healthz`,`/readyz`, conditional `/metrics`); `source_health.go:26` (`/health/sources`); `file.go:38-52` (incl. conditional `/files/dl`); `comment.go:24-38`; `block.go:20-22`; `search.go:25`; `events.go:41` (SSE); `event_ingest.go:83-87` (`/internal/v1/events`, `InternalAuth` group). |
| E11 | Fixture-pair self-test doctrine cited from `scripts/check-architecture.sh` | **Confirmed** | `scripts/check-architecture.sh:65-68` ("positive fixture that stops matching is a SELF-TEST FAIL … negative fixture that starts matching is a SELF-TEST FAIL") and the sweep loop at 299-320. |
| E12 | Fully-wired router would include every conditional route | **Confirmed** | Conditionals: fencing/migration/cutover (`account.go:60-85`), bans (`admin.go:71-76`), export/anchor/holds (`audit_ledger.go:46-71`), projection (`replay.go:41-44`), `/files/dl` (`file.go:51-53`), `/metrics` (`router.go:75-76`). All non-nil only when the corresponding dep is non-nil. |

**Verdict: the evidence is substantively accurate.** Every load-bearing claim (both drift directions, the wrong spec pin, the 202-vs-200 drift, the registration lines, the wiring list, the middleware non-dereference, the exclusion surface) is confirmed against the tree. Four minor, content-neutral deltas are itemized in §11.

---

## 2. Problem statement

`openapi_test.go` guards only the direction "the spec contains a hardcoded client subset". Three drift classes are live and undetected:

1. **Direction (a) — documented but unserved.** `POST /admin/callback-deliveries/{id}/replay` is in the spec (YAML:323) but no such route exists in the router. A spec-following client 404s. The one-directional test pins this very path (`openapi_test.go:87`) so the gate stays green.
2. **Direction (b) — served but undocumented.** 22 served control-plane routes (the entire `/admin/accounts`, migration/cutover, roles/bans/audit-log surface, `/me/resources`, `/audit/verify`) plus the actually-served replay route have no spec entry. The spec silently tracks only part of the conditional-wiring surface.
3. **Status drift on replay.** The spec promises `202`; the handler returns `200` (`httpx.OK`). Neither a 202-checking client nor a 200-checking client is served by the *documented* contract.

No gate fails when handler and contract diverge in either direction; there is no full-router inventory anywhere in the package.

---

## 3. Design overview

```text
                    ┌──────────────────────────────────────────────┐
                    │  openapi_reconcile_test.go  (package http_test)│
                    │                                               │
                    │  routeManifest  ── 60 control-plane (m,p)      │
                    │      │              (single hardcoded list)    │
                    │      ▼                                        │
                    │  pure helpers: normalizeRoute / excludeRoute / │
                    │  routesFromSpec / servedRoutes /               │
                    │  documentedButNotServed / servedButNotDocumented│
                    │  missingFromManifest / missingFromServed /     │
                    │  missingFromSpec                               │
                    │      │            ▲                            │
                    │      ▼            │ self-test fixtures         │
                    │  M1..M4 assertions│ (positive + negative pair)  │
                    │  + exclusion pin  │                            │
                    └──────┬────────────┴────────────────────────────┘
                           │ engine.Routes()            │ yaml.v3 parse
                           ▼                            ▼
              fully-wired test router            api/openapi/aero-id-account.yaml
              18 registrars (mirror of           (single spec; after this design:
              internal/app/handlers.go:65-97)     replay path+status fixed, 22 routes
                                                  added — 38 → 60 operations)
```

**Design decisions (single source of truth per concern):**

| Concern | Canonical source | Notes |
|---|---|---|
| Served route surface | `engine.Routes()` on a fully-wired test router | Built by mirroring `buildRouter`; registrar list pinned by comment |
| Documented contract surface | `api/openapi/aero-id-account.yaml` | Parsed with the existing `gopkg.in/yaml.v3` (already used by `openapi_test.go`) |
| In-scope control-plane surface | `routeManifest` (one declarative list) | Replaces `requiredClientPaths`; the only hardcoded path list in the package |
| Excluded surface | Exact-pinned `excludedRoutePrefixes` const + per-entry justification | Widening requires a deliberate test edit |

**REQ-7 resolution — Option A (recommended, adopted):** keep the served route (live handlers are the source of truth for shipped behavior, per AGENTS.md) and update the spec: rename the path at YAML:323 to `/admin/callback-subscriptions/deliveries/{id}/replay`, change the success response `202` → `200` with the actual body, keep `operationId: replayCallbackDelivery` (stable client-side identifier), leave the handler and `httpx` untouched. Option B (align the handler: route rename at `callback.go:29` + 202 + new `httpx.Accepted`) is documented as the rejected alternative in §11 — it changes shipped behavior on an audited surface for no client benefit, since no working client can currently rely on the documented (404) path or the documented (never-returned) 202.

---

## 4. Detailed design

### 4.1 `openapi_reconcile_test.go` — manifest (REQ-1)

```go
package http_test

type route struct{ method, path string } // path in spec-normalized form, {param} placeholders

// routeManifest is the single hardcoded path list in this package. It replaces
// requiredClientPaths (openapi_test.go, deleted by REQ-1). Coverage: every
// documented operation (38, after REQ-7/REQ-3) AND every served, non-excluded
// control-plane route (60). Entries marked [control-plane-only] have no spec
// entry until the REQ-3 YAML additions land; M3 trips until then.
var routeManifest = []route{
    // -- client contract (spec ops) --
    {method: "GET", path: "/account-datasets"}, {method: "GET", path: "/me"}, ...
    // -- control-plane only (direction b; added to spec by REQ-3) --
    {method: "GET", path: "/admin/accounts"}, ...
    // -- replay, canonical form (REQ-7) --
    {method: "POST", path: "/admin/callback-subscriptions/deliveries/{id}/replay"},
}
```

- Exactly one hardcoded path list in the package (asserted by a comment + review; the fixture/helper files hold no route lists).
- The full 60-entry inventory is tabulated in §5.1. Note the evidence's "29 + 22 + served replay" undercounts: M2 (`served_in_scope ⊆ manifest`) forces the 6 audit ops that `requiredClientPaths` never listed (`/audit/export`, `/admin/audit/holds` ×2, `/admin/audit/holds/{scope_type}/{scope_id}`, `/admin/audit/anchor`, `/admin/audit/anchor/manifest/{source}/{partition}`) into the manifest too — so the manifest is the complete 60-route control-plane surface, not a client subset.
- Manifest entries use spec-normalized form (`{param}`). `normalizeRoute` converts gin `:param` → `{param}` before comparison, so the same list serves both directions.

### 4.2 Direction tests (REQ-2, REQ-3, REQ-4)

All four invariants are independent assertions in separate test functions, each `t.Errorf`-producing, over the fully-wired router (4.4) and the parsed spec:

| Invariant | Test | Meaning | Pre-fix state |
|---|---|---|---|
| M1 | `TestReconcile_ManifestIsServed` | `manifest ⊆ served` | green (all 60 served today) |
| M2 | `TestReconcile_ManifestCoversServed` | `served_in_scope ⊆ manifest` | green (the 22 are the manifest's raison d'être) |
| M3 | `TestReconcile_ManifestIsDocumented` | `manifest ⊆ spec` | **red** (22 entries absent) |
| M4 | `TestReconcile_DocumentedRoutesAreServed` | `spec ⊆ served` (direction a) | **red** (replay path unserved) |
| — | `TestReconcile_ServedControlPlaneRoutesAreDocumented` | `served_in_scope ⊆ spec` (direction b, via manifest) | **red** (22 routes + served replay) |
| — | `TestReconcile_CallbackReplayCanonicalForm` | spec path+method == served path+method == manifest entry; spec success status == handler status | **red** (path + 202-vs-200) |

Direction (b) is implemented as: `served_control_plane = servedRoutes(engine) − excluded(prefix)` and each element asserted present in `routesFromSpec(spec)`, with failure message `"served route GET /admin/accounts is absent from api/openapi/aero-id-account.yaml"`.

**Normalization rules** (pure, fixture-tested):
- Strip a leading `/v1` from `engine.Routes()` paths (Auth group prefix) before comparing; public/internal routes (`/healthz`, `/files/dl`, `/internal/v1/events`, …) have no `/v1` and are excluded anyway.
- gin `:param` → `{param}` via `normalizeRoute` (a regexp `:[^/]+` → `{…}` — no `*` wildcards exist in the control-plane surface, verified by grep).
- Spec paths are already in `{param}` form; `routesFromSpec` just flattens `paths` → `(method, path)` pairs, skipping the `parameters` key (same guard `openapi_test.go:123` already uses).

### 4.3 Self-test fixtures and exclusion pin (REQ-5, REQ-6)

Mirroring `scripts/check-architecture.sh:65-68` doctrine — a positive fixture that stops matching is a SELF-TEST FAIL (gate went vacuous); a negative fixture that starts matching is a SELF-TEST FAIL (rule went over-broad):

- **Positive fixtures** (each must fire):
  - fabricated spec missing one manifest route → `servedButNotDocumented` reports it;
  - fabricated spec containing an unserved path → `documentedButNotServed` reports it;
  - fabricated served set missing one manifest entry → M1 violation;
  - fabricated served set containing a route absent from spec+manifest → M2 violation;
  - `normalizeRoute("/admin/accounts/:id")` == `/admin/accounts/{id}` and `!normalizeRoute`-corrupt case (e.g. an `:id` inside a literal segment) is covered by a negative case.
- **Negative fixture** (must pass): perfectly aligned synthetic spec+served pair → zero violations from every checker.
- **Exclusion pin** — `TestExclusionSetIsPinned` asserts `excludedRoutePrefixes` equals the declared const exactly (ordering + membership) and that every non-default entry carries a non-empty justification:

```go
// excludedRoutePrefixes — served routes outside the client/control-plane
// contract. Default (legacy compat surfaces):
//   /blocks /comments /files /messages /search /events /internal/v1/events
// Justified extensions (each must carry a reason; TestExclusionSetIsPinned
// asserts the set is exact and every justified entry has a non-empty reason):
//   /conversations*        — legacy IM conversation surface (message.go:35-36)
//   /notifications*        — legacy IM notification surface (notification.go:22-27)
//   /notification-preferences — legacy preference surface (preference.go:22-23)
//   /healthz /readyz /metrics — operational probes (router.go:72-76)
//   /health/sources        — operational probe (source_health.go:26)
var excludedRoutePrefixes = []excludedPrefix{...}
```

`excludeRoute` is prefix-matched (so `/files` covers `/files/dl`). The exclusion shape test also asserts `excludeRoute` returns false for every §1 route (no control-plane route matches an exclusion) — the "negative fixture that starts matching" class.

### 4.4 Fully-wired test router (REQ-8)

```go
// TestReconcile_FullyWiredRouter mirrors internal/app/handlers.go:65-97
// (buildRouter). UPDATE THIS LIST WHEN handlers.go CHANGES.
func newFullyWiredRouter(t *testing.T) *gin.Engine {
    gin.SetMode(gin.TestMode)
    perm := &stubPermissionChecker{}
    return httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{},
        httpapi.NewHealthHandler(),
        httpapi.NewSourceHealthHandler(nil),                                  // 1
        httpapi.NewFileHandler(nil, &stubSignedDownloader{}, time.Minute),    // 2  (downloader non-nil → /files/dl)
        httpapi.NewMessageHandler(nil),                                       // 3
        httpapi.NewCommentHandler(nil),                                       // 4
        httpapi.NewNotificationHandler(nil),                                  // 5
        httpapi.NewBlockHandler(nil),                                         // 6
        httpapi.NewEventsHandler(nil, nil),                                   // 7  (*hub.Hub may be nil; not dereferenced)
        httpapi.NewPreferenceHandler(nil),                                    // 8
        httpapi.NewAdminHandler(nil, []service.BanService{&stubBanService{}},  // 9  bans non-nil → /admin/bans*
            []httpapi.HubDisconnector{nil}, perm),
        httpapi.NewSearchHandler(nil),                                        // 10
        httpapi.NewAccountHandlerWithExtensions(nil, &stubRegionFencing{},     // 11 fencing/migration/cutover non-nil
            &stubLegacyMigration{}, &stubLegacyCutover{}, perm),
        httpapi.NewAggregationHandler(nil, perm),                             // 12
        httpapi.NewEventIngestHandler(nil, "", 1<<20, time.Minute),           // 13
        httpapi.NewAuditLedgerHandlerWithExtensions(nil, &stubAuditHold{},     // 14 holds/anchor/jobs non-nil
            &stubAuditAnchor{}, &stubSyncJob{}, perm),
        httpapi.NewCallbackHandler(nil, perm),                                // 15
        httpapi.NewOperationHandler(nil),                                     // 16
        httpapi.NewSyncJobHandler(nil, perm),                                 // 17
        httpapi.NewReplayHandlerWithProjection(nil, &stubReplayProjection{}, perm), // 18
    )
}
```

- **Non-nil required only where `RegisterRoutes` branches on the field**: fencing, migration, cutover (`account.go:60-85`), bans (`admin.go:71-76`), holds/anchor/jobs (`audit_ledger.go:46-71`), projection (`replay.go:41-44`), downloader (`file.go:51-53`). Services never dereferenced during registration may be nil — verified per constructor. The permission checker is a non-nil fake so `RequirePermission`/`RequirePermissionIfConfigured` attach exactly as in production (`permission.go:20-46` never dereferences at registration, but request-time realism is kept).
- RouterConfig: `Metrics` nil → `/metrics` not registered (it is excluded from the contract surface anyway); `Idempotency`/`RateLimiter`/`BanChecker`/`RegionEpoch`/`CutoverChecker` affect middleware, not route inventory — left nil.
- The stubs are ~10 small types (`stubPermissionChecker`, `stubBanService`, `stubRegionFencing`, `stubLegacyMigration`, `stubLegacyCutover`, `stubAuditHold`, `stubAuditAnchor`, `stubSyncJob`, `stubReplayProjection`, `stubSignedDownloader`); each implements the one interface with methods returning zero values. They live in a sibling `openapi_reconcile_router_test.go` so no single file approaches 500 lines.
- Assertion: `engine.Routes()` includes every conditional route (`/admin/regions/promote`, `/admin/regions/directory/:account_id`, `/admin/audit/holds*`, `/admin/audit/anchor*`, `/audit/export`, `/audit/replays/projection`, `/admin/migration/*`, `/admin/bans*`, `/files/dl`) — so direction (a) verifies the documented conditional surface against the router.

### 4.5 Spec YAML changes (REQ-3, REQ-7)

1. **Replay fix (Option A)** — replace the block at `aero-id-account.yaml:323-331`:

```yaml
  /admin/callback-subscriptions/deliveries/{id}/replay:
    post:
      operationId: replayCallbackDelivery     # kept stable (client codegen)
      security: [{snaplinkBearer: []}]
      parameters:
        - {$ref: '#/components/parameters/ID'}
      responses:
        '200':
          description: Delivery queued for manual replay.
          content:
            application/json:
              schema:
                type: object
                properties:
                  data: {type: object, properties: {status: {type: string, example: queued}}}
        '403': {$ref: '#/components/responses/Error'}
```

   Body matches `httpx.OK`'s `{"data": ...}` envelope wrapping `{"status":"queued"}` (`callback.go:82`).
2. **22 new path blocks** (full inventory in §5.1), each with: non-empty `operationId`, `security: [{snaplinkBearer: []}]`, path parameters via existing component refs (extend `#/components/parameters` with `TenantID`; reuse `ID` for `{id}`, `{uid}`, `{account_id}` or add distinct refs as clarity demands), and at least one 2xx response (bodies may be `object`-typed initially — no deep schema modeling; must pass `TestOpenAPISpec_OperationsHaveSuccessResponses`). The 6 `{id}`-suffixed admin account paths use `{id}` to match the handler's param names (`/:id/sync`, `/:id/overview`, …); `{tenant_id}` for cutover/cleanup-readiness; `{uid}` for roles/bans.
3. Total operations 38 → 60; path count 34 → 56.

### 4.6 `openapi_test.go` refactor (REQ-9)

- Delete `requiredClientPaths` (lines 55-90) and the wrong pin (line 87).
- `TestOpenAPISpec_CoversClientContract` iterates `routeManifest` (REQ-1) instead; per-entry assertions unchanged (path present, method present, non-empty operationId, non-empty responses).
- `TestOpenAPISpec_OperationsHaveSuccessResponses` and `TestOpenAPISpec_HasCoreSchemas` unchanged in behavior (they already sweep the whole spec).
- `openapi_test.go` stays at 148 lines → well under 500; new test files each kept under 500 by splitting helpers/stubs into siblings.

---

## 5. API / interface changes (complete inventory)

### 5.1 Route surface — 60-entry manifest (single source of truth)

Legend: `[S]` documented in spec today (38 ops) · `[N]` control-plane-only, added to spec by this design (22 ops) · replay row is both served and (after fix) documented.

| Method | Path | Source | Method | Path | Source |
|---|---|---|---|---|---|
| GET | /account-datasets | [S] | GET | /me | [S] |
| GET | /me/profile | [S] | PATCH | /me/profile | [S] |
| GET | /me/sources | [S] | GET | /me/memberships | [S] |
| GET | /me/data | [S] | GET | /me/overview | [S] |
| GET | /me/activity | [S] | POST | /me/sync | [S] |
| POST | /me/export | [S] | POST | /me/erase | [S] |
| GET | /sync/jobs | [S] | GET | /sync/jobs/{id} | [S] |
| POST | /sync/jobs/{id}/reconcile | [S] | GET | /sync/jobs/{id}/export/download | [S] |
| GET | /operations | [S] | POST | /operations | [S] |
| GET | /operations/{id} | [S] | POST | /operations/{id}/run | [S] |
| POST | /operations/{id}/reconcile | [S] | GET | /audit/events | [S] |
| GET | /audit/operations/{id} | [S] | POST | /audit/replays | [S] |
| POST | /audit/replays/projection | [S] | GET | /audit/replays/{id} | [S] |
| POST | /audit/export | [S] | GET | /admin/callback-subscriptions | [S] |
| POST | /admin/callback-subscriptions | [S] | DELETE | /admin/callback-subscriptions/{id} | [S] |
| GET | /admin/audit/holds | [S] | POST | /admin/audit/holds | [S] |
| DELETE | /admin/audit/holds/{scope_type}/{scope_id} | [S] | POST | /admin/audit/anchor | [S] |
| GET | /admin/audit/anchor/manifest/{source}/{partition} | [S] | POST | /admin/regions/promote | [S] |
| GET | /admin/regions/directory/{account_id} | [S] | **POST** | **/admin/callback-subscriptions/deliveries/{id}/replay** | **REQ-7** |
| GET | /me/resources | [N] | GET | /admin/accounts | [N] |
| GET | /admin/accounts/{id} | [N] | POST | /admin/accounts/backfill/legacy-identities | [N] |
| POST | /admin/migration/legacy/compare | [N] | GET | /admin/migration/legacy/cutover/{tenant_id} | [N] |
| POST | /admin/migration/legacy/cutover | [N] | GET | /admin/migration/legacy/cleanup-readiness/{tenant_id} | [N] |
| GET | /admin/accounts/{id}/overview | [N] | GET | /admin/accounts/{id}/data | [N] |
| POST | /admin/accounts/{id}/sync | [N] | POST | /admin/accounts/{id}/export | [N] |
| POST | /admin/accounts/{id}/erase | [N] | GET | /admin/audit-log | [N] |
| GET | /admin/roles | [N] | GET | /admin/roles/{uid} | [N] |
| POST | /admin/roles/grant | [N] | POST | /admin/roles/revoke | [N] |
| POST | /admin/bans | [N] | GET | /admin/bans | [N] |
| DELETE | /admin/bans/{uid} | [N] | GET | /audit/verify | [N] |

### 5.2 Code changes

| File | Change | Lines |
|---|---|---|
| `internal/handler/http/openapi_reconcile_test.go` | **new** — manifest, 4 invariant tests + 2 direction tests + replay canonical test, checker helpers, fixtures, exclusion pin | < 500 (split if needed) |
| `internal/handler/http/openapi_reconcile_router_test.go` | **new** — fully-wired router + ~10 stubs (REQ-8) | < 500 |
| `internal/handler/http/openapi_test.go` | delete `requiredClientPaths` + wrong pin; `TestOpenAPISpec_CoversClientContract` iterates manifest | 148 → ~95 |
| `api/openapi/aero-id-account.yaml` | replay path/status/body fix (§4.5.1) + 22 path blocks (§4.5.2) | 450+ → ~620 |
| `internal/handler/http/callback.go` | **none** (Option A) | — |
| `internal/pkg/httpx/httpx.go` | **none** (Option A; no `Accepted` helper needed) | — |
| `internal/app/*`, `cmd/*`, proto, migrations, services | **none** | — |

No new third-party dependencies: the gate uses `gopkg.in/yaml.v3` (already imported by `openapi_test.go`) and gin's `engine.Routes()`.

---

## 6. Compatibility constraints

1. **Client contract semantics preserved for working clients.** The documented replay path was never served (404), and the documented 202 was never returned (real: 200) — no working client can depend on either. Fixing the spec to the served reality is strictly additive to correctness. `operationId: replayCallbackDelivery` is kept stable so generated client identifiers do not churn.
2. **The 22 new spec entries are additive.** No existing operation, response, or schema is modified by them; `TestOpenAPISpec_OperationsHaveSuccessResponses` and `TestOpenAPISpec_HasCoreSchemas` remain green.
3. **Zero handler/behavior change under Option A.** `callback.go` stays untouched; the replay route keeps its existing in-transaction audit behavior (`service.CallbackService.ReplayDelivery`). No audit sites added or removed — `check-audit-swallow` / `check-audit-manifest` unaffected.
4. **Conditional routes documented as contract.** `/admin/migration/*`, `/admin/bans*`, `/admin/audit/holds*`, `/admin/audit/anchor*`, `/audit/export`, `/audit/replays/projection`, `/admin/regions/*` are served only when wired. Direction (a) validates them against the fully-wired router (the deployment-maximum contract). A partially-wired production deployment legitimately 404s on the absent subset — the same semantics the spec already documents for `/admin/regions/*`; no spec note is required beyond the existing conditional-feature convention.
5. **Legacy compat surface excluded by prefix, not by omission.** `/files`, `/messages`, `/conversations`, `/comments`, `/blocks`, `/search`, `/events`, `/notifications`, `/notification-preferences`, `/internal/v1/events`, `/healthz`, `/readyz`, `/metrics`, `/health/sources` are explicitly excluded with written justification (REQ-6), so they cannot silently pass through an empty exclusion list.
6. **No API consumers of `requiredClientPaths`** — it is test-internal to `package http_test`; deletion is safe.
7. **Gin route conflicts**: the manifest mirrors already-shipped registrations (static-vs-param coexistence under `/admin/accounts`, `/admin/audit`, `/sync/jobs` already works in production); no new registration code is introduced, so no new conflict class is possible.

---

## 7. Failure modes and mitigations

| # | Failure mode | Detection | Mitigation |
|---|---|---|---|
| F1 | Gate goes vacuous (manifest trimmed or checker weakened to silence drift) | Positive fixtures stop firing → SELF-TEST FAIL | REQ-5 fixtures: fabricated spec/served pairs must trip each checker; commenting out any fixture assertion fails the test |
| F2 | Exclusion widened to silence a control-plane route | `TestExclusionSetIsPinned` exact-set assertion fails; justification requirement makes the widening visible in diff | Exact-pinned const + mandatory inline reason + `excludeRoute` negative check over all 60 manifest routes |
| F3 | Test router drifts from production wiring (a route added to `buildRouter` but neither manifest nor test router) | Review discipline only (mechanization out of scope) | REQ-8 pinned comment ("update when `internal/app/handlers.go` changes"); `handlers.go` is 99 lines and composition-root C-7 line-pins it; M1/M2 keep the *test router itself* honest |
| F4 | Placeholder normalization bug (gin `:id` ↔ spec `{id}`) | `normalizeRoute` positive/negative fixtures fire | Pure helper + fixture pair, per REQ-5 |
| F5 | Spec YAML additions fail the existing contract tests | `TestOpenAPISpec_OperationsHaveSuccessResponses` / `_HasCoreSchemas` fail on the new blocks | Every new block carries operationId + ≥1 2xx; bodies object-typed |
| F6 | Spec-following client 404s on conditional routes when the feature is unwired in a given deployment | Runtime, not gate | Pre-existing conditional semantics (see §6.4); direction (a) tests the fully-wired maximum |
| F7 | File-size / lint-strict regression in new tests | `check-file-sizes` (≤500/file) + `funlen 50 / cyclop 10 / avg ≤ 5.0` | Small pure helpers by construction; router + stubs in a sibling test file; no test function > ~40 lines |
| F8 | Red-state proof skipped (gate lands green without ever having been red) | Review of the change description | §8 step 1 requires recording the pre-fix failures (`/admin/callback-deliveries/{id}/replay`, 22 routes, 202-vs-200) |

---

## 8. Implementation steps (ordered)

1. **Add the reconcile test file(s) only** (`openapi_reconcile_test.go`, `openapi_reconcile_router_test.go`): manifest, helpers, fixtures, fully-wired router, all invariant/direction/canonical tests. **Do not touch the spec or handler yet.**
2. **Prove the red state** (non-vacuity demonstration — record output in the change description):
   - `TestReconcile_DocumentedRoutesAreServed` fails on `POST /admin/callback-deliveries/{id}/replay`;
   - `TestReconcile_ServedControlPlaneRoutesAreDocumented` fails on the 22 routes + served replay path;
   - `TestReconcile_CallbackReplayCanonicalForm` fails on path mismatch + 202-vs-200;
   - `TestReconcile_ManifestIsDocumented` (M3) fails on the 22.
3. **Fix the spec replay entry** (Option A, §4.5.1): path rename + `202`→`200` + body schema.
4. **Add the 22 spec path blocks** (§4.5.2) with operationId + security + params + 2xx.
5. **Refactor `openapi_test.go`** (REQ-9): delete `requiredClientPaths` + wrong pin; iterate the manifest.
6. **Run the gates**: `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-audit-manifest && make check-anchored-event-ids && make check-load-scenarios && make check-migration-set && make check-composition-root && make check-proto-sync && make check-integration-compile && make build`.
7. **Confirm the one-canonical-form grep**: `grep -n 'replay' api/openapi/aero-id-account.yaml` and `internal/handler/http/callback.go` agree; `grep -n '"/admin/callback-deliveries/{id}/replay"' internal/handler/http/*_test.go` matches nothing.

---

## 9. Testable acceptance mapping

| Requirement | Test / check | Command |
|---|---|---|
| REQ-1 manifest, one list, replaces pin | `grep -n '"/admin/callback-deliveries/{id}/replay"' internal/handler/http/*_test.go` → no match; only `routeManifest` declares routes | grep |
| REQ-2 direction (a) | `TestReconcile_DocumentedRoutesAreServed` (red pre-fix on replay; green post-fix) | `go test ./internal/handler/http/ -run TestReconcile -count=1` |
| REQ-3 direction (b) | `TestReconcile_ServedControlPlaneRoutesAreDocumented` names missing routes (red pre-fix on 22; green post-fix) | same |
| REQ-4 triple invariant | `TestReconcile_ManifestIsServed` (M1), `TestReconcile_ManifestCoversServed` (M2), `TestReconcile_ManifestIsDocumented` (M3), `TestReconcile_DocumentedRoutesAreServed` (M4) — each independent t.Errorf | same |
| REQ-5 self-test fixtures | every checker has ≥1 positive + ≥1 negative fixture; commenting out any fixture assertion fails the test; exclusion pin `TestExclusionSetIsPinned` | same |
| REQ-6 exclusion set | `TestExclusionSetIsPinned` exact-pins the const; every non-default entry carries a justification; `excludeRoute` false for all 60 manifest routes | same |
| REQ-7 replay canonical form | `TestReconcile_CallbackReplayCanonicalForm` green; spec/served/manifest agree on path+method+status(200) | same + grep |
| REQ-8 fully-wired router | router construction panics-free; `engine.Routes()` includes every conditional route | same |
| REQ-9 openapi_test refactor | `openapi_test.go` + new files each ≤ 500 lines; full `internal/handler/http` suite green | `go test ./internal/handler/http/ -count=1` |
| Done gate | all of §8.6 | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-audit-manifest && make check-anchored-event-ids && make check-load-scenarios && make check-migration-set && make check-composition-root && make check-proto-sync && make check-integration-compile && make build` |

---

## 10. Out of scope (must not)

Legacy compat handler behavior (file/message/comment/search/block/notification/events/preference routes, including `/conversations*`, `/notifications*`, `/notification-preferences`); the nine-handler unit-coverage gap; gRPC; protobuf; migrations; the audit ledger or its gates; `internal/app` composition-root edits; new third-party dependencies; behavior changes beyond the single REQ-7 resolution (none, under Option A). No audit `Record`/`Log` sites are added anywhere — the change is spec + tests only.

---

## 11. Open items / evidence deltas / rejected alternative

**Evidence deltas (content-neutral, do not affect the design):**
- D1: "spec (38 paths)" — the spec has **34 path keys / 38 operations** (method keys). The 38 figure is the operation count.
- D2: "all 29 current `requiredClientPaths` entries" — the list has **32 entries**.
- D3: "`buildRouter` … `internal/app/handlers.go:64-109`" — actual function span **46-98**, registrar list **65-97**; same content.
- D4: "spec documents only `/admin/regions/*` from the conditional-wiring surface" — the spec **also** documents `/admin/audit/holds*`, `/admin/audit/anchor*`, `/audit/export`, `/audit/replays/projection`. The operative conclusion (22 undocumented routes) is exactly right.

**Rejected alternative — REQ-7 Option B** (align handler to documented contract): rename `callback.go:29` to `/admin/callback-deliveries/:id/replay` and return 202 (new `httpx.Accepted` beside `httpx.go:43-45`). Rejected because: (1) AGENTS.md holds live handlers as the source of truth for shipped behavior; (2) it changes shipped route/status behavior on an audited surface with no working client to protect — the documented path 404s today and the documented 202 is never returned, so Option A fixes the same drift with zero runtime risk; (3) it would need an atomic handler+httpx+spec+manifest change set and an audit-site review. Option B remains available if product ownership explicitly wants `202 Accepted` semantics.

**Residual risk (accepted, review discipline):** a route added to production `buildRouter` but to neither the manifest nor the test router escapes the gate. Mechanization would require importing `internal/app` into the test (cycle) or a separate composition-root gate; flagged, not in scope.
