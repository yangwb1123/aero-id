# AGENTS.md — aero-id

## Product contract and transition status

`aero-id` is evolving into a **distributed account aggregation, account-management, operation-orchestration,
and audit control plane** spanning multiple applications and data centers.

The authority boundaries are load-bearing:

| System | Authority | aero-id relationship |
|---|---|---|
| **Snaplink** | Authentication, OAuth/OIDC tokens, authorization, users, tenants, sessions, MFA, consent, identity lifecycle | Authenticate callers and answer global authorization questions; never duplicate credentials or sign user tokens |
| **snaplink-console** | Snaplink's own client/admin UI | Reference only; the aero-id product gets an independent client and OAuth client registration |
| **Aero IM** | Application messaging, notifications, chat, live streaming, IM participants/workspaces | Query/command through a connector; consume source events; never read its database directly |
| **Aero Vault** | File/object storage, buckets, metadata, ACLs, shares, usage | Query/command through a connector; never copy blobs or bypass its authorization layer |
| **aero-id** | Canonical account links, explicitly owned profile/preferences, source references, aggregate read models, cross-app operations, audit ledger, callbacks, sync/export jobs | Own only these control-plane facts and projections |

This table is the target authority contract, not a claim that every source has completed the migration. The
current Aero IM tree still contains local password/JWT/PAT/OIDC identity flows, while Aero Vault also supports
source-scoped API keys, local JWT, SigV4, shares, and capabilities. Migrate or namespace those surfaces; they
must not become a second global human-account authority. Source-local bot, service, object, and capability
credentials may remain when they are not modeled as human identities.

Target implementation plan: `docs/plans/2026-08-04-distributed-account-aggregation.md`.
The 2026-05-22 design under `docs/superpowers/specs/` is historical input, not the target architecture.
README API tables are stale; live handlers, protobuf contracts, and `internal/app/new.go` remain the source of
truth for behavior that is already shipped.

**Current-state warning:** the running application is still the layered files/messages/comments service wired
from `internal/app/new.go`. The account model, source connectors, regional read model, durable audit ledger, and
workflow services described below are migration targets until their code, migrations, wiring, and tests land.
Do not document target behavior as shipped behavior. Preserve current APIs until an explicit compatibility or
data-migration task removes them.

## Commands and done gate

```bash
make test              # unit tests (go test ./... -count=1) — no Docker
make test-integration  # integration tag; Postgres + MinIO via testcontainers (needs Docker)
make lint              # go vet ./...
make lint-strict       # golangci-lint (funlen 50, cyclop 10)
make check-file-sizes  # non-test/non-generated Go files must be <= 500 lines
make check-integration-compile  # compiles every integration-tagged _test.go file without running tests (go test -tags integration -run '^$' + go vet -tags integration) — no Docker; catches interface breaks that plain `go build -tags integration` misses
make build             # -> bin/aero-id
make run               # go run ./cmd/server -config configs/config.yaml
make migrate-up        # explicit migration runner; migrations are not auto-applied by default
make proto             # regenerate stubs only after editing api/proto/**/*.proto
```

Done-gate:

```bash
make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-proto-sync && make check-integration-compile && make build
```

Run `make test-integration` after changing repository SQL, migrations, storage, source connectors, event
ingestion, callback delivery, or multi-service transaction boundaries. Cross-data-center and load tests are
additional release gates once the corresponding infrastructure exists; they do not replace the local gate.

## Architecture boundaries

### Current live dependency flow

```text
handler/{http,grpc} -> service -> repository -> PostgreSQL (GORM)
                            |-> storage (local | s3)
                            |-> sso     (gRPC client, caches, breaker)
                            |-> outbox  (transactional relay)
                            `-> hub     (best-effort SSE)
```

### Target dependency flow

```text
external HTTP / internal gRPC
              |
              v
handler -> account | aggregation | audit | operation service
              |          |           |
              |          |           `-> workflow/outbox/callback ports
              |          `-> projection repositories
              `-> source connector interfaces
                            |-> Snaplink adapter
                            |-> Aero IM adapter
                            `-> Aero Vault adapter
```

- **Handlers** decode/validate transport input, call a service, and render output. No orchestration,
  authorization policy, repository access, connector calls, or source-specific branching in handlers.
- **Services** own business rules, authorization intent, consistency mode, partial-result semantics, Saga
  transitions, and audit requirements. HTTP and gRPC must share service behavior.
- **Repositories** own only aero-id data. They return raw persistence errors; services translate them into
  `*pkg/errors.AppError`.
- **Connectors** are anti-corruption adapters over public source APIs. They translate canonical requests into
  source contracts but do not own business policy. A connector must never access a source database.
- **Domain** types remain pure and import nothing from `internal/`. Snaplink, IM, Vault, GORM, HTTP, gRPC, and
  event-bus types must not leak into domain entities.
- **Composition roots** wire every live dependency. A type or worker not reachable from a `cmd/*` composition
  root is not a shipped feature.

Preferred target layout extends the live layered tree rather than the dead feature tree:

```text
internal/domain/{account,audit,operation,source}.go
internal/service/{account,aggregation,audit,operation,sync,callback}_*.go
internal/repository/{account,audit,operation,snapshot,callback,sync}_*.go
internal/connector/{snaplink,aeroim,aerovault}/
internal/handler/{http,grpc}/
internal/outbox/ and internal/hub/
```

Split deployment does not require split repositories. Start with cohesive packages and multiple `cmd/*`
binaries only when a service needs independent scaling, security, or failure isolation.

## Multi-region and distributed-systems contract

- Deploy stateless API/query instances in each supported region. Route account-affecting writes to the
  account's **home region**; local regions may serve versioned projections.
- Resolve the initial home region from `(issuer, subject)` before account creation and route every create/link
  attempt there. The regional unique constraint is a final guard, not a substitute for global routing.
- Do not put a synchronous fan-out to Snaplink, Aero IM, and Aero Vault on the default read path. Aggregate
  lists, search, statistics, and dashboards read local projections. Point reads and commands may call a source
  when freshness requires it.
- Do not implement distributed transactions or two-phase commit across services/data centers. Cross-app writes
  are durable Sagas with explicit steps, retries, compensation where meaningful, and terminal
  `completed | partially_completed | failed | unknown` outcomes.
- A timeout is not proof of remote failure. Persist an `unknown` step and reconcile by idempotency key or source
  operation identifier before retrying.
- Strong consistency is local to an authority. Cross-source views are eventual or bounded-stale and must expose
  per-source `status`, `version`, `generated_at`, and `last_synced_at`.
- No global total event order is assumed. Order by `(source, aggregate_id, aggregate_version)` and reconstruct
  cross-app causality with `operation_id` and `causation_id`.
- Cross-region replication transports only approved projections, references, and redacted audit data. Source
  content and residency-restricted PII remain in their authority region.
- Redis and process memory are caches/coordination aids, never the only correctness store. Losing either must
  not lose accounts, operations, audit facts, idempotency receipts, or callback deliveries.
- Regional database multi-writer is not introduced casually. The default migration path is regional PostgreSQL
  plus home-region writes and asynchronous projection replication; distributed SQL requires a separate ADR and
  conflict model.
- If the home region is unavailable, continue eligible stale reads. Accept writes in another region only after
  a durable promotion and fencing epoch proves the old writer cannot commit; otherwise pause writes rather than
  create split-brain.

## Identity, authorization, tenancy, and source references

- Snaplink remains the authentication and global authorization authority. aero-id never stores passwords,
  refresh/access tokens, MFA secrets, or password-reset material and never signs user tokens.
- Snaplink is the sole human-account authority. Source-local bots, service identities, API/S3 keys, and
  short-lived capabilities are separate principal kinds; never silently merge them into a human account.
- Prefer local JWT verification against Snaplink JWKS for the hot path. Use introspection or Snaplink gRPC only
  when token type or policy requires it. Validate issuer, audience, expiry, and non-empty subject.
- The canonical account key is an internal UUIDv7 `account_id`. External identities are unique by
  `(issuer, subject)`; `canonical_uid` may initially equal the Snaplink subject for compatibility.
- Preserve source-native IDs as opaque references: IM participant/workspace IDs and Vault tenant/bucket/object
  IDs are not reinterpreted as aero-id IDs.
- Propagate `actor_uid`, `tenant_id`, `request_id`, `traceparent`, `operation_id`, and an audience-bound service
  identity on downstream calls. Pass a user token only when its audience is valid for the target; otherwise use
  token exchange or a scoped service credential with original-actor attribution.
- Global authorization is decided by Snaplink. Resource authorization is rechecked by the source service.
  Cached authorization may improve latency but cannot silently grant on cache miss or policy uncertainty.
- Current aero-id roles/bans are legacy local-domain controls. Do not expand them into a competing global RBAC
  or identity lifecycle system; migrate or explicitly namespace them before account-management rollout.
- Define field ownership before exposing profile mutation. Snaplink owns verified identity and lifecycle
  fields; aero-id may own explicitly named presentation/preferences fields or orchestrate a Snaplink command.
  A generic profile PATCH must never create two writable copies of email, phone, verification, or status.
- An account can belong to multiple Snaplink tenants, IM workspaces, and Vault tenants/buckets. Never collapse
  these scopes into a single unqualified `tenant_id`; store source and scope type with every membership.

## Transport, query, and projection rules

- Public/browser clients use HTTP JSON + OpenAPI. Internal request/response calls prefer gRPC + Protobuf.
  Source connectors may initially use a source's stable HTTP API when no gRPC control plane exists.
- The checked source snapshot exposes Snaplink JWKS plus stable AuthZ/Admin/Audit gRPC APIs, Aero IM REST/WS
  plus NATS JetStream/outbox, and Aero Vault REST/S3/WebDAV/MCP plus persisted events/SSE/HMAC webhooks. No
  stable public account-summary gRPC API was found in IM or Vault. Use these published seams and re-check the
  deployed source version before implementation; do not invent a gRPC dependency from roadmap prose.
- Every remote call has a deadline, bounded message size, concurrency limit, circuit breaker, and metrics.
  Automatic retries are limited to idempotent operations and consume a retry budget.
- Use batch source APIs for account enrichment and summaries. Never introduce an N+1 cross-data-center loop.
- Do not consume raw IM room/live subjects or Vault object bytes into a general account projection. Add
  redacted account/workspace/resource-summary events or an allow-listed adapter at the source boundary.
- Generic account-data APIs expose an allow-listed, typed dataset registry. They do not proxy arbitrary paths,
  SQL, source filters, or source response bodies.
- Store normalized indexed columns for account/source/scope/status/time fields. Versioned JSONB may hold
  redacted source extensions, but it is not the primary public contract.
- Projections are disposable and replayable; account links, operation state, audit facts, idempotency receipts,
  and callback delivery state are authoritative and are not disposable.
- External success responses retain `{"data": ...}`. Partial aggregate responses additionally report per-source
  freshness/errors without exposing internal network or database details.
- IDs are UUIDv7 and list APIs use keyset pagination (`id < cursor`, descending, `limit + 1`). Do not use offset
  pagination on high-cardinality account, activity, operation, audit, callback, or resource lists.

## Event, outbox, and synchronization invariants

- Delivery is **at least once**. Producers, consumers, projections, callbacks, and replay workers must be
  idempotent. Exactly-once behavior is achieved with unique receipts and state transitions, not assumed from a
  broker.
- A source mutation and its durable source event/outbox row must commit atomically inside that source. Within
  aero-id, account/operation/audit state and the event that announces it follow the same rule.
- Canonical event envelope fields are: `event_id`, `source`, `source_region`, `event_type`, `schema_version`,
  `aggregate_type`, `aggregate_id`, `aggregate_version`, `actor_uid`, `tenant_id`, `operation_id`,
  `correlation_id`, `causation_id`, `trace_id`, `occurred_at`, and redacted payload.
- Consumers reject malformed or unsupported versions to a dead-letter/quarantine path; they do not acknowledge
  poison events as successfully applied.
- Duplicate and out-of-order events are normal. Projection writes compare source version/cursor and record a
  durable receipt keyed by `(source, event_id)`.
- Event transport is an implementation detail behind ports. HTTP CloudEvents/webhooks, NATS JetStream, and
  Kafka-compatible streams may coexist through adapters; do not couple domain code to one broker.
- Periodic reconciliation is mandatory. It compares source summaries/cursors with projections and repairs
  missed events without performing unbounded full scans in one run.

## Audit, operation, replay, and callback invariants

- Distinguish `request_id` (one transport request), `trace_id` (technical call graph), `operation_id` (one user
  intent/workflow), `event_id` (one immutable fact), and `causation_id` (parent fact). Never overload one ID for
  all purposes.
- The compliance audit ledger is append-only and durable. The current best-effort `AuditLogger` is suitable for
  legacy diagnostics only; it is not the target ledger and must not be advertised as complete audit evidence.
- Every state-changing, administrative, security, export, replay, callback-management, and sensitive-read
  operation produces a durable audit fact. Routine high-volume reads may use metadata-only access audit, but
  audit mode and retention must be explicit per dataset/tenant.
- Audit payloads are allow-listed and redacted before persistence. Never record bearer tokens, cookies,
  passwords, MFA/recovery material, object bytes, unrestricted message bodies, or connector credentials.
- Tamper evidence uses a versioned hash/signature chain per source/partition plus immutable/WORM archival.
  Cross-region events form a causal DAG; do not claim one global hash-chain order.
- Replay has three distinct modes: forensic reconstruction (no effects), dry-run simulation, and controlled
  command re-execution. Controlled replay creates a new `replay_id`, reauthorizes, uses new idempotency keys,
  enforces step limits/approval, and writes new audit facts. It never reuses historical credentials.
- Callbacks are asynchronous and emitted only after the source fact is durable. Deliveries are signed,
  versioned, idempotent, rate-limited, retried with backoff/jitter, dead-lettered after a cap, and manually
  replayable. Callback failure never erases or rolls back the audit fact.
- Callback registration and delivery enforce HTTPS/endpoint verification, redirect limits, DNS-rebinding and
  private-network policy, bounded response reads, and controlled egress. Tenant callbacks must not become an
  SSRF path into metadata endpoints or internal control planes.
- Cross-app operations persist every step, attempt, source operation ID, result, and compensation. A worker
  crash or region failover resumes from durable state rather than restarting the operation blindly.

## Data ownership and minimization

aero-id may persist canonical account/profile data, source links, memberships, resource references, aggregate
snapshots, activity indexes, sync cursors/jobs, operation state, audit facts, callback subscriptions/deliveries,
and export metadata.

It must not persist source credentials, Vault object bytes, unrestricted IM message history, or a second mutable
copy of source ACLs. Temporary export artifacts are encrypted, access-controlled, TTL-bound, and stored through
object storage. Every projected field has a documented authority, freshness contract, retention rule, and erase
behavior.

## Legacy live feature invariants

Until explicitly migrated, current live APIs keep their existing behavior:

- File mutation is owner-only; read is owner, public, or shared-with. Search remains owner-scoped.
- Messages are visible/reactable only to participants; user paths cannot forge `system` messages; attachments
  must be owned by the sender.
- Comments have exactly two levels and are readable only when their target is readable.
- Notifications are created only by the dispatcher from outbox events, never directly in request handlers.
- Current domain mutations append required outbox events in the same GORM transaction. Drain concurrency uses
  `FOR UPDATE SKIP LOCKED`.
- Repository errors are translated in service; raw DB errors never cross the service boundary.
- Middleware order remains load-bearing: RequestID -> Tracing -> Recovery -> Metrics -> Sample -> AccessLog ->
  CORS -> Degradation -> Timeout; `/v1`: Auth -> Ban -> RateLimit -> Idempotency.
- `signing_key` signs only local-storage download URLs. It is never a token-signing key.
- SSE is best-effort; durable notification state and replay provide correctness.
- Live-layer audit writes (`Record` and `Log`) are durable; swallowing either is a gate violation
  (`make check-audit-swallow`). The dead-tree `AuditLogger` carve-out does not apply to the live
  `service.AuditLogger` adapter, and the legacy fail-open carve-out for Ban and other middleware
  must never be re-applied to audit writes: the fail-closed `audit.ingest_failed`/503 contract
  stands for every live-layer audit site.
- The audit-coverage invariant covers every operation that emits an audit fact, not every mutation.
  These legacy compatibility surfaces currently emit zero audit facts (before and after the
  fail-closed conversion) and are exempt only until migration: file create/delete/update, message
  send/edit/react, comment create/edit/report, preferences, region promotion, and projection
  replication.

Do not add new canonical file/chat authority to aero-id while Aero Vault/Aero IM are the target authorities.
Treat the current file/message/comment surface as a compatibility source until a migration/deprecation decision
is implemented and tested.

## Errors, configuration, and secrets

- Services translate raw dependency errors to `*pkg/errors.AppError` with stable dotted codes and matching HTTP
  and gRPC status. Source outages, stale projection, partial result, unknown outcome, and retry exhaustion need
  distinct codes.
- Do not leak source URLs, topology, credentials, SQL, stack traces, or authorization internals in API errors.
- Configuration remains in `configs/config.yaml` with `AERO_ID_*` environment overrides. New regional/source
  config validates region IDs, public/internal endpoints, TLS, audiences, deadlines, concurrency, and queue caps
  at startup.
- Secrets come from environment or an approved secret provider, never committed config. This includes DB,
  Redis, signing/HMAC, mTLS, source credentials, callback keys, broker, and object-store credentials.
- A `0` value means code default unless the key explicitly documents `0 = disabled`.

## High-concurrency and availability rules

- API/query services are stateless and horizontally scalable. Background consumers use partitioned ownership,
  leases/fencing, or broker consumer groups; only one active attempt may mutate a given operation step.
- Apply bulkheads per source and workload class. Audit ingestion, callbacks, exports, and reconciliation must not
  exhaust interactive query/command pools.
- Bound queues, request bodies, batch sizes, streams, per-account concurrency, and in-flight source calls.
  Backpressure returns stable overload errors or parks durable work; it never grows memory without limit.
- Cache entries are versioned and invalidated by events; stale service is explicit. Authorization and lifecycle
  revocation paths fail according to a documented security policy, not a generic availability default.
- Health is layered: liveness is process health; readiness includes local required dependencies; source health
  and projection lag are reported separately and do not make every regional read endpoint unready.
- Required metrics include per-source latency/error/circuit state, projection lag, consumer lag, operation age,
  unknown outcomes, audit ingest failures, callback retries/DLQ, reconciliation drift, and regional freshness.
- Release testing covers duplicate/out-of-order events, source outage, WAN timeout/partition, Redis loss,
  database failover, worker crash after remote success, callback outage, poison events, and region failover.

## Engineering constraints

Non-negotiable limits enforced by the repository gates:

| File (non-test/generated) | Function | Cyclomatic | Package average | Responsibility |
|---|---|---|---|---|
| <= 500 lines | <= 50 lines | <= 10 | <= 5.0 | one cohesive concern/file |

At 400 lines, plan a split; over 500, split before feature work. Prefer sibling files within the live layer.
Create a new package only for a real domain or infrastructure boundary. Tests follow the split.

- Unit tests live beside code and use deterministic fakes; they make no real cross-data-center calls.
- Connector contract tests cover auth context, deadlines, pagination, partial responses, idempotency, and error
  mapping. Network-backed tests use the integration build tag.
- Repository and migration work is tested against real PostgreSQL. Source event/projector work tests duplicate,
  out-of-order, gap, replay, and poison-event behavior.
- Operation tests inject crashes before and after remote success. Callback tests cover signing, retry, DLQ,
  duplicate delivery, key rotation, and manual replay.
- Generated protobuf is committed. Never hand-edit `*.pb.go`; edit `.proto`, then run `make proto` (and
  `make check-proto-sync` to prove the committed stubs match the pinned toolchain) with plugin
  versions compatible with `go.mod`.

## Current repository gotchas

- `internal/{file,message,notification}` and `internal/audit` are an unwired parallel tree (the former `internal/block`, `internal/comment`, and `internal/search` dead trees were deleted in the 2026-08-08 quarantine — `internal/comment` carried the last swallowed fail-open out-of-transaction audit write and is guarded by `check-audit-swallow`; `internal/block`'s `BlockChecker` role is served by the live `service.BlockChecker`).
  The running application uses `internal/service` + `internal/repository`. Do not implement target account/audit
  work in an unwired tree.
- `make check-architecture` currently polices that dead tree, not the live layered runtime. A green result does
  not prove runtime wiring.
- Migrations are not auto-applied unless `database.auto_migrate: true`; production uses `cmd/migrate`. Every
  zero-padded `NNNNNN_name.up.sql` has a matching `.down.sql`; never edit an applied migration.
- HTTP idempotency currently covers POST + `Idempotency-Key`; existing gRPC calls have no generic idempotency.
  New command RPCs must carry an explicit idempotency/operation key before enabling automatic retries.
- `make proto` currently enumerates only `sso.proto` and `aero_id.proto`. Adding split account/audit/operation
  protobuf files also requires updating the generation target; creating a `.proto` file alone generates nothing.
- Current rate limits, SSO caches, and some notification limits are process-local unless Redis-backed. Do not
  describe them as globally enforced.
- Ban and several legacy middleware checks fail open by design. This carve-out covers request
  middleware only and must never be re-applied to live-layer audit writes, which are fail-closed
  by contract and by `make check-audit-swallow`. Global account lifecycle and security policy
  must not inherit this behavior accidentally.
- Build through `make build` / `go build ./...`; do not leave binaries at repository root.
- Toolchain is Go 1.26; module path is `github.com/aero/aero-id`.
