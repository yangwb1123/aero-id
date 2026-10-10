# AGENTS.md — aero-id

## Product boundary and current state

`aero-id` is a distributed account aggregation, management, operation-orchestration, and audit control plane.
It is not a second identity provider, messaging authority, or object store.

| System | Authority / aero-id boundary |
|---|---|
| **Snaplink** | Human identity, authentication, OAuth/OIDC, global authorization, tenants, sessions, MFA, consent, lifecycle. Never duplicate credentials or issue user tokens. |
| **Aero IM** | Messaging, notifications, chat/live, participants/workspaces. Use public connectors/events, never its database. |
| **Aero Vault** | Objects, buckets, metadata, ACLs, shares, usage. Use public connectors, never copy blobs or bypass resource authorization. |
| **aero-id** | Canonical account links, explicitly owned profile/preferences, source references, approved projections, operations, local audit ledger, callbacks, sync/export state. |

Migration is not complete: namespace/migrate source-local human password/JWT/PAT flows; bot/service/API/S3 keys
and capabilities are separate principal kinds. Legacy aero-id roles/bans are compatibility controls, not global RBAC.

**Wired:** `internal/app/new.go` + sibling builders assemble accounts/profile/links/memberships, aggregation,
event ingestion, durable audit/compliance, Sagas, sync/export/erase, callbacks, replay, fencing and legacy cutover
alongside files/messages/comments. `web/` is the independent Vue + Iris UI console with Snaplink Authorization Code + PKCE.
**Conditional:** connectors, JetStream, replication, audit-governance publication and billing/quota need configuration
and external contracts. Wired ≠ enabled ≠ production-accepted; unavailable capabilities must fail closed.

Verify `internal/app/{services,handlers,grpc,workers}.go`, handlers, Protobuf/OpenAPI and config before changes.
README tables, plans and generated `docs/agent-context/platform-contract.md` may lag or misstate ownership.
Architecture baseline: `docs/plans/2026-08-04-distributed-account-aggregation.md`; the 2026-05-22 spec is historical.

## Live architecture

```text
HTTP/gRPC handler -> service -> repository -> regional PostgreSQL
                            -> connector/storage/outbox ports
web/ -> HTTP JSON; source APIs/events -> connector/event adapters
```

- **Handlers:** decode/validate/render only. No policy, orchestration, repositories or source-specific calls.
- **Services:** authorization intent, consistency/partial-result rules, workflows and audit; shared by HTTP/gRPC.
- **Repositories:** aero-id persistence only; services translate raw errors to `*errors.AppError`
  (`internal/pkg/errors`). gRPC uses the shared mapper, never a handler-local `grpcError`.
- **Domain:** stdlib + `internal/pkg/*` only; no GORM/transport/broker/source types. Connectors translate public APIs,
  not policy; `connector/event/outbox/region/sso` cannot import service/repository.
- Extend live layered sibling packages; new packages/binaries require a real boundary and composition-root wiring.
  Never restore `internal/{file,message,notification,audit,block,comment,search}`; unwired code is not shipped.
  `scripts/check-architecture.sh` defines rules/exceptions; `check-audit-swallow` pins quarantine.

## Identity, tenancy and source calls

- `account_id` is UUIDv7; external identities are unique by `(issuer, subject)`. Compatibility `canonical_uid`
  may equal the Snaplink subject; source IDs stay opaque. Tenant-scoped lookups never trust a supplied account ID alone.
- Prefer Snaplink JWKS; validate issuer/audience/expiry and non-empty subject/tenant/client claims. Standard startup
  rejects UID-only/fallback auth (`internal/app/auth.go`). Global AuthZ belongs to Snaplink; disable legacy local
  fallback when authoritative. Never grant on cache miss/uncertainty; sources recheck resource authorization.
- Memberships retain source + scope type + scope ID; tenants/workspaces/buckets are not interchangeable.
  Profile writes own only display name/avatar/locale/timezone/allow-listed preferences, never verified identity/status.
- Propagate actor/tenant/request/trace/operation attribution with audience-bound service identity. User tokens require
  a valid target audience; otherwise exchange/use scoped service credentials. Never persist source credentials or let request
  tenants select another credential. Browser HTTP JSON/OpenAPI; internal gRPC/Protobuf; no invented source RPCs or N+1.
- Verify `docs/contracts/source-account-summary.md` before source enablement: Snaplink summary defaults off and no
  production erase version is verified; IM requires service identity + dedicated target-assertion/JWKS; Vault requires
  tenant/region-bound Snaplink service OAuth and defaults read-only. Paths alone never enable erase/status.
- Console: independent tenant-bound public OAuth client, not `snaplink-console`; tokens in memory, no frontend secrets
  or cross-console token forwarding.

## Data and distributed correctness

- Resolve creation/linking home region from `(issuer, subject)` before persistence; regional uniqueness is a final
  guard, not global routing. Writes require durable directory/fencing; promotion is CAS + audit. Unfenced failover
  must pause writes, not create split-brain; eligible stale reads may continue. No 2PC or casual regional multi-writer.
- Default aggregate/list/search/dashboard reads use local projections. `eventual/bounded/strong` are freshness modes,
  not global transactions. Expose source `status/version/generated_at/last_synced_at`, partial/stale/unavailable;
  writer epoch headers are not causal projection watermarks.
- Sagas persist steps/attempts/source operation IDs/results/compensation with `completed/partially_completed/failed/unknown`
  outcomes and one active leased/fenced attempt. Timeouts stay `unknown` until reconciled by key/source operation ID;
  restart resumes durable state, cancellation is not rollback. Redis/memory loss must not lose authoritative state/receipts.
- Datasets: `internal/domain/aggregation.go`. Use indexed normalized columns and redacted versioned JSONB extensions;
  never proxy arbitrary paths/SQL/filters/response bodies. Prefer batch source enrichment.
- Project approved references/summaries, never unrestricted IM history/raw live subjects, Vault bytes or mutable ACL
  copies. Replication obeys residency. Each field needs ownership/freshness/retention/erase rules; encrypted exports
  use access-controlled, TTL-bound object storage. Erase changes must cover late events, sync/export races, stale
  replicas and Legal Hold without claiming these gaps are already closed.
- Success stays `{"data": ...}` with sanitized source errors. High-cardinality lists use descending keyset
  (`id < cursor`, `limit + 1`), never offset.

## Events and projections

- At-least-once delivery: mutation + required event/outbox commit atomically at their authority. Consumers persist
  `(source, event_id)` receipts and compare versions. Canonical contract: `internal/event/envelope.go`.
- Keep request/trace/operation/event/correlation/causation IDs distinct; order by source/aggregate/version, not globally.
  Poison/unsupported/wrong-source events go to durable quarantine/DLQ, not successful acknowledgement.
- Transport stays behind ports; bounded reconciliation repairs gaps. Projections are replayable/disposable;
  account links, operations, audit, receipts and deliveries are not. Test duplicates, ordering and concurrent summaries.
- Summary application preserves tenant/home-region fences, monotonic versions and coverage/complete semantics;
  omission outside declared coverage is not deletion authority. Replication cursors/tombstones are commit-ordered
  (`internal/repository/replication_changes.go`); a sequence alone cannot replace the clock lock.

## Audit, replay and callbacks

- New mutations/admin/security/export/replay/callback management/sensitive reads need durable redacted facts;
  local mutations append in-tx. Never regress `WithAudit` to post-commit logging; routine read audit/retention is explicit.
- Live `Record`/`Log` errors propagate: standalone ingest is fail-closed `audit.ingest_failed`/503; in-tx failures abort.
  Legacy middleware fail-open never excuses audit failure. Deleted dead-tree `AuditLogger` was diagnostics,
  not the live `service.AuditLogger` adapter. Never store tokens/cookies/passwords/MFA/bytes/unrestricted bodies/secrets.
- `scripts/check-audit-manifest.sh` enumerates standalone/fallback sites; new bare sites need markers/manifest/integration
  proof. Coverage is not legacy transactional atomicity; file/message/preference/promotion/replication/replay are audited.
- Preserve create/send/delete resource anchors, preference/promote `idgen.Anchor`, replication envelope IDs;
  update/edit/react/unreact/replay use fresh execution IDs, never resource-wide dedup. Keep anchor goldens byte-compatible.
  Exceptions only: `SendSystem` (no tenant/actor/production callers) and `persistSyncSummary` (job lifecycle audited).
- Ledger facts are immutable; retention purges are hold-aware. Tamper evidence is versioned partition hash/signature
  chains + WORM archives, not global order or automatic production compliance.
- Forensic replay has no effects; dry-run simulates; controlled replay reauthorizes, uses new replay/idempotency IDs,
  confirmation and bounded steps, never historical credentials. Confirmation is not multi-party approval; projection replay is shadow.
- Callbacks follow durable facts asynchronously: signed/versioned/idempotent, rate-limited, bounded retries/backoff/jitter,
  DLQ/manual replay. Failure never removes facts. Enforce HTTPS/challenge, redirect/DNS/private-network/egress policy and
  bounded reads; tenant endpoints must not become SSRF. Do not cache one-time callback secrets in replay responses.

## Compatibility APIs

Preserve until explicit migration/cutover; add no new canonical file/chat authority.

- Files: owner-only mutation/search; owner/public/shared reads. `signing_key` signs local download URLs, not tokens.
  Messages: participant-only, no forged `system`, sender-owned attachments. Comments: two levels, target-readable.
  Notifications come from outbox dispatch; SSE is best-effort, durable state/replay provides correctness.
  Outbox drain uses `FOR UPDATE SKIP LOCKED`.
- Global order: RequestID → Tracing → Recovery → Metrics → Sample → AccessLog → CORS → Degradation → Timeout.
  Auth → Ban → RateLimit → Idempotency; control-plane uses verified claims. Legacy deprecation/epoch/cutover guards
  stay on compatibility routes (`internal/handler/http/router.go`). Ban fail-open is not a new security-policy default.
- Generic keyed POST idempotency caches responses via Redis or GORM; store failure/overflow can re-execute.
  It is not durable/body-bound business intent. No generic gRPC equivalent: command retries need explicit durable keys.

## Resource, error and configuration budgets

- Bound deadlines/bodies/streams/batches/queues/concurrency; remote calls need breakers/metrics and budgeted idempotent
  retries. Isolate query/command/health/background pools. Version caches, expose stale, never claim local limits are global.
- Liveness is process health; readiness checks required dependencies, source health/lag is separate. Track source
  latency/errors/circuits, lag/drift, operation age/unknown, audit failures, callback retries/DLQ.
- Dotted AppErrors match HTTP/gRPC semantics; distinguish outage/stale/partial/unknown/retry exhaustion.
  Never leak SQL/topology/source URLs/secrets/stacks/auth internals.
- `configs/config.yaml` + `AERO_ID_*`: validate regions/endpoints/TLS/audiences/deadlines/caps at startup;
  secrets from env/approved providers only. `0` means code default unless documented as disabled.

## Engineering and verification

Go 1.26; module `github.com/aero/aero-id`. One concern per file: non-test/non-generated Go ≤500 lines,
functions ≤50 lines, cyclomatic ≤10, package average ≤5.0. Plan splits at 400 lines; `internal/app/new.go` has
its own ≤400-line gate. Prefer sibling files; do not add lint exceptions to avoid a proper split.

Backend done-gate (script headers define mechanical contracts):

```bash
make test && make lint && make lint-strict && \
make check-architecture && make check-file-sizes && make check-audit-swallow && \
make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && \
make check-migration-set && make check-db-startup-hardening && make check-composition-root && \
make check-proto-sync && make check-k8s-manifest && make check-integration-compile && make build
```

- Colocate deterministic unit tests; no Docker/real source/WAN dependencies. Integration compile tests tagged `_test.go`
  (`go test -tags integration -run '^$'` + vet), unlike ordinary build.
- Run `make test-integration` (Docker PostgreSQL/MinIO) after SQL/migration/storage/connector/event/callback/transaction
  changes. Cover duplicates/order/gaps/poison/replay, crash around remote success, unknown outcomes, Redis loss,
  DB/region failover, callback retry/rotation/replay. Real source/WAN/load acceptance remains a separate release gate.
- Web: `make web-install && make web-check`. Vault releases: configured `make test-vault-contract`, never a skipped
  suite as verification. Load single source: `scripts/load/ghz/scenarios.yaml`, guarded by `check-load-scenarios`.
- Migrations: unique `NNNNNN_name.up.sql`/`.down.sql` pairs; never edit applied files. Production uses `cmd/migrate`,
  auto-apply opt-in. `make migrate-validate` checks integrity/applied drift without mutation; `make migrate-up` executes.
- Proto: edit `.proto` → `make proto` → commit stubs; never hand-edit `*.pb.go`. Add files to both Makefile generation
  and `scripts/check-proto-sync.sh`, using its pinned toolchain. `make run` uses the configured server;
  `make build` writes server/audit-replay/preflight to `bin/`, never repo root. Report tests/skips and measured vs static findings.

## Read when relevant

- Source enablement/auth/coverage: `docs/contracts/source-account-summary.md`.
- Internal membership attestation: `docs/contracts/agent-membership-check.md`.
- Browser contract/auth: `docs/clients/aero-account-console-contract.md`, `web/README.md`.
- Retention/hold/erase: `docs/DATA_RETENTION.md`; runtime code wins if older status text differs.
- Release/preflight: `deployments/k8s/README.md`; load: `scripts/load/README.md`.
- Recovery: `docs/runbooks/{outbox,outbox-retry-eligibility,replication-parked}.md`, `docs/runbook.md`.
