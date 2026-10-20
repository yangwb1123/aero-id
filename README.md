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
make migrate-up        # shipped runner; explicit DDL through migration 000075
make migrate-verify    # deployed relay and command-receipt shape
make run               # configs/config.yaml + AERO_ID_* overrides
make test              # no Docker or real source calls
make test-integration  # real disposable PostgreSQL/MinIO fixtures
```

For fixture ownership, phase timings, bounded termination and pinned-image pull
failures, see [integration diagnostics](docs/runbooks/integration-fixtures.md).
Helper/compile passes do not replace the complete real-storage gate.
`make test-integration-bounded` is a separate full-suite `-p 2` budget diagnostic,
not a replacement for the original serial gate or permission to skip S3/source
acceptance; it retains the default timeouts and independent fixtures. The tagged
`internal/repository/auditintegration` package holds the original black-box audit
contracts; a reviewed source/inventory guard preserves all 185 repository tests.

For a Docker-free SSE replay/cancellation race regression:

```bash
go test -race ./internal/handler/http -run '^(TestEventsHandler_Stream_(Replay|ReplayInvalidCursor)|TestSSETestStreamCancelUnconsumedFlush)$' -count=100 -timeout=3m
```

The test harness uses bounded flush/subscription handshakes and joins the handler
before reading mutable recorder/fetcher state; no sleep-based readiness polling.
It covers header/query cursor precedence, replay-before-live ordering and cancellation,
not gap-free delivery, real browser/proxy behavior or deployed identity acceptance.
SSE remains best-effort; durable notification state/recovery remains necessary.

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
OAuth scopes alone do not grant administrator permissions. Sync/Export/Erase,
administrator account tasks and Operation creation use explicit server previews and
confirmation. Uncertain responses retain original scoped keys/intent IDs; known intents
are recovered with GET, never automatic writes or a replacement command on expiry.
Current per-action capability guidance is refreshed separately; backend/worker grants
and source resource authorization remain authoritative. Refresh guidance stays unavailable
until the new data/error is published, including the callback-to-view handoff.

Task-list selections and accepted creation/recovery receipts now update the original
`#/jobs/<UUID>` or `#/operations/<UUID>` URL. Copy/refresh/back/forward retain that
receipt; close/return clears the detail route. Selected results precede long lists,
and independent list failures no longer hide readable details. Navigation is GET-only,
never a retry, grant, key replacement or one-time artifact consumption; real login/source
acceptance remains separate. See the [Web guide](web/README.md).

Job/Operation results also offer original-ID audit links: they prefill Request ID
or Operation ID separately, wait for an explicit query, and retain an exact return
to the original receipt even on audit failure. Refresh restores the filter, not a
command or automatic ledger read. Missing Request IDs are not replaced by Job or
source operation IDs; a matching request is not the whole subsequent history.
Current audit permission, tenant filtering and durable read audit remain authoritative.

The Profile form admits saves only while mounted: captured submit handlers refuse
new calls after unmount, even if another same-account view or session is mounted.
This does not cancel an already-admitted PATCH or prove rollback; GET current
Profile before deciding on another explicit save. It also ignores repeated submits
while its existing PATCH is pending, including response-body reading.
It does not queue or automatically retry saves;
after completion, another save requires an explicit submit. Only the four existing
text fields are sent. Success adopts server values only for fields still matching
that request's submitted snapshot; newer input, including explicit clears, stays
unsaved, with a separate remaining-edits notice. Drafts remain in mounted-page memory,
not in the adopted server result or browser storage. This is neither profile-version
CAS/conflict resolution, durable idempotency, a cross-tab lock nor authorization;
server identity/fencing and transactional audit remain unchanged. A lost response
or closing the page does not prove rollback; read the current profile before deciding
whether to save again. Refresh/navigation does not promise draft recovery.
Profile GET/PATCH responses must contain all four root text fields as strings;
empty strings are valid. Invalid text shape rejects the whole result with a fixed
`response.invalid`, without coercion, adopting partial values or retrying a save.
A bad PATCH response may follow a committed update; keep the draft and GET current
state before deciding on another explicit save. This minimum shape check is not
account/tenant binding, complete metadata validation, authorization or version CAS.

The WORM Anchor confirmation handler, not just its button, requires the current
exact `ANCHOR ALL CHAINS` phrase before calling the existing client. Empty, changed,
case/spacing variants do not send a request or clear the original in-memory key/input;
correcting the phrase never runs automatically. Opening and confirmation handlers
also refuse new calls after panel unmount, before changing intent state, creating a
key or calling the client; a fresh panel/session cannot lend admission to old listeners.
This does not cancel an admitted POST or promise cross-refresh intent recovery.
These are local admission checks, not an authorization grant or durable business
idempotency. Server `system.config` and pre-archive intent audit remain unchanged. Partial writes/timeouts are not rollback;
inspect the original audit/Manifest before deciding on an explicit same-key retry.
No real WORM/storage acceptance is implied by component tests.
The Manifest query handler also rejects new submits after panel unmount, before
changing query state or calling the original GET client. New panels/sessions cannot
lend admission to old listeners. This local guard neither grants `audit.read` nor
revokes admitted reads/their audit facts; explicit reads, source/partition encoding,
30-second/2MiB transport bounds and server pre-return audit remain unchanged.
While that GET/client promise is pending, including response-body reading, the
handler ignores repeated submits before validating new input or changing query
state. It never queues changed targets or retries automatically; after success or
failure another read needs an explicit submit. The guard is panel-local, not a
global lock or grant, and does not renew the original deadline. Cancellation is
cooperative; a fired deadline alone does not release an unfinished client call.
Manifest GET adopts a response only when its root `source` and `partition` are exact
strings matching the submitted request, not later form edits. Missing, wrong-type
or mismatched targets reject the whole result with a fixed `response.invalid` and
no rejected evidence/target in the error. Recovery is another explicit GET of the
intended chain, never automatic querying, anchoring or repair. This is response
correlation, not full document/hash/signature validation, current permission,
archive completeness, a global point in time or WORM/storage acceptance. A rejected
response does not undo an already-recorded access fact.

Legal Hold list GET requires a root `holds` object array. Missing/non-array lists
or any non-object member reject the entire response with fixed `response.invalid`,
not a filtered or empty success. The page uses its read-error/explicit GET retry
path without automatic requests or mutations. A valid empty query means no visible
active Hold in that query, not global absence or permission to lift a hold or clean
evidence. This is basic list shape validation, not full row/tenant/expiry checking,
current authorization, retention/Legal Hold acceptance or a global snapshot; server
permissions, scope filtering, hold mutations and transactional audit are unchanged.
The list client accepts an optional AbortSignal, and the page forwards its existing
read hook's signal through fetch/body reads on unmount or read replacement. The
15-second/2 MiB budgets, explicit GET recovery and late-result guards remain;
cancellation is cooperative, not a global concurrency cap, withdrawal of an
already-admitted read, hold release or rollback. It does not cancel unrelated reads.
Release selection requires a supported `scope_type` and a nonempty string `scope_id`;
non-string targets stay read-only, without coercion, aliases or normalization.
Valid opaque strings pass unchanged only after explicit exact confirmation. This
is local target typing, not full row validation, permission or Hold-version CAS.
The Hold-release dialog and its original command handler both require the current
confirmation to equal the selected Scope ID exactly, without trimming or case
conversion. Wrong/cleared input refuses before client invocation or command-state
changes; correcting it still needs an explicit confirmation. This local intent
check is not permission, Hold-version CAS or retention approval. It cannot withdraw
an admitted DELETE; timeout/lost response requires checking the original scope and
facts before another manual decision, never automatic retry or inferred rollback.
Hold-release confirmation also refuses while the original list is loading (including
its response body) or has a read error, even through a retained listener with the
exact Scope ID. Rejection preserves the original selection/input; a successful
explicit list GET does not submit it, and release still needs a separate explicit
confirmation. This is local read admission, not current permission, Hold-version
CAS or proof that the selected Hold is unchanged; it cannot withdraw an already
admitted DELETE. Other compliance commands keep their existing contracts.
Compliance command preparation/opening and confirmation handlers also reject new
calls after page unmount, before generating intent keys, changing command state
or invoking the client. A fresh page/session cannot lend admission to an old
listener. This covers Hold set/release and audit-export creation without changing
their confirmation, scope or server permission/audit contracts. It is local
lifecycle admission, not a grant or durable idempotency; in-memory keys do not
promise refresh recovery. Unmount/read cancellation does not withdraw an admitted
DELETE/POST, and late results do not apply to the new page. No automatic retry,
replacement key, hold release or export is introduced.
Audit-export creation adopts only the response's root non-nil `job_id` UUID,
without an `id` alias, coercion or trimming. An invalid ID keeps the original
in-memory intent/key and any previously adopted detail; it neither opens that
invalid target nor claims success. Check the original task list and audit first;
any later explicit same-intent request retains its original key. Rejection may
follow a committed job, not a rollback or permission to generate another export.
This page-local check is not full receipt/tenant/authorization validation or
artifact availability. The actual HTTP creation status is 201, now documented in
OpenAPI; server behavior and other creation clients remain unchanged.

The existing Operation route loads its code on demand after normal shell admission;
all 16 entries and original deep links remain. A separate code-loading/error state
never retries commands or discards original intent pointers. The 15-second indication
limit does not cancel a browser import: late code still uses the current route/session.
Static chunk separation is tested, not proof of browser latency or deployment acceptance.
Deployments must verify matching index/chunks and availability for existing sessions.

`make check-k8s-manifest` requires Go but no cluster/client credentials: all five
release renders are checked against complete, vendored SHA-256-pinned strict schemas
and rejection tests. A passing static baseline does not replace the target cluster's
server dry-run/admission/rollout or live startup. See [Kubernetes deployment](deployments/k8s/README.md).

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
minting a new key. Migrations through 000075 are the guarded startup minimum. Migration 000074 adds
Callback UUID keyset indexes; 000075 adds all-writer delivery revisions and durable
callback replay receipt guards. Apply the complete shipped set.
`migrate verify` and startup check the actual receipt and command-intent schema. The shipped
`preflight` also shares startup's claims-capable JWKS configuration guard; it
does not prove live Snaplink login/AuthZ or perform database migrations.

`POST /v1/command-intents` creates a caller/client/tenant-bound five-minute preview;
`GET /v1/command-intents/:id` retrieves it. Explicit dataset scope and normalized
ordered steps are persisted without source calls. Execute accepts only the original
version and confirmation digest, reauthorizes/replans, and atomically checks local
metadata/region fencing while creating the existing Operation/Sync receipt and audit.
Confirmed retries return that receipt; they do not automatically run or roll back a
command. Original-key GET lookup recovers lost preview IDs without another POST;
NotFound/expiry alone never proves an old request cannot commit. Explicit
`POST /v1/command-intents/:id/abandon` competes atomically with Execute: it closes
only unexecuted receipt admission, or returns the already-created original receipt.
It does not cancel a job, purge metadata, renew TTL or release Legal Hold. Original
plan/key/audit are retained; a new preview requires a separate explicit action and
current authorization. Migration 000073 fences terminal intents against late/older
local database writers and refuses rollback if abandonment proof exists.
Local versions are not causal watermarks; Snaplink policy versions and
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

Job/Operation list GETs require their root `jobs`/`operations` object array and,
when present, a string `next_cursor`; empty arrays and absent/empty cursors remain
valid. Missing lists, invalid members or cursor types reject the whole page with
fixed `response.invalid`, rather than silently filtering rows or declaring the end.
First-page errors are not successful empty results; continuation errors retain
viewed rows and the original cursor for explicit GET retry. This is basic shape
checking, not complete row/ID/status validation, current authorization or a global snapshot.
Job/Operation list continuations forward the table's cancellation signal through
fetch and response-body reads. Unmounting or replacing the table aborts that read;
late results cannot merge into the replacement. Existing cursor/limit50, 15-second
and 2MiB request budgets remain unchanged. Errors retain viewed rows and the original
cursor for an explicit GET retry, with no automatic read/write retry or recovery-key
changes. Cancellation requires dependency cooperation and does not cancel a task,
prove remote rollback or grant access.

Job/Operation details distinguish the exact persisted `sync.authorization_denied`,
`sync.authorization_unavailable`, `operation.authorization_denied` and
`operation.authorization_unavailable` codes from execution failures only for known
nonterminal statuses without completion evidence. These historical warnings can
outlive a worker's resumed checks; they are not current grants, proof of no remote
effects or permission to replace the original key/receipt. Terminal, unrecognized,
scope and lease diagnostics retain their existing error presentation. Status refresh
remains GET-only; explicit actions retain their existing current-authorization gates.
Task-detail GET responses must carry the exact requested root `job_id` or
`operation_id` string and an exact root string status from the corresponding family
before display or use by actions. GET and Run/Reconcile adoption share the per-family
status rules below; missing, non-string, unrecognized or cross-family states reject the whole
response with a fixed `response.invalid`. Read errors pause automatic metadata GETs,
without clearing original recovery keys or retrying commands; explicit read retry still
queries the original ID. Foreground detail reloads, including body reads, also pause
polling: a new automatic GET cannot cancel that read. An adopted active result resumes
with the existing delay; errors, unknown and terminal results remain stopped. Neither
the mounted poller's 60-request/five-minute budget nor the GET deadline is renewed.
Explicit reload may still supersede an existing background read under the original
cancellation/late-result guards. This is per-detail UI coordination, not a global
concurrency cap, audit-read coordination or command authorization.
This is correlation/basic shape checking, not current authorization,
complete metadata/transition validation or proof of remote completion.
Before adopting Run/Reconcile responses, the detail action also requires the exact
route-family root ID on both the original detail and the returned receipt, alongside
its existing account/tenant/initiator/type checks. Aliases and the other family's ID
cannot substitute. The root status must also be an exact string from that family's
OpenAPI/domain enum, without coercion, nested fallback or a shared cross-family list;
neither family currently defines `canceled`. Rejection retains the last observed
state/result and original recovery key, emits no success, and never retries automatically. First GET the original
receipt to investigate; a rejected response does not prove rollback or no remote effects.
This local adoption check does not strengthen the legacy HTTP idempotency cache.
Operation Run entry and final confirmation also require a successful local detail read,
exact `requested`/`waiting` status and no completion timestamp, alongside existing
current-action guidance. A foreground reload, failed/rejected/stale GET or newly
observed non-runnable state suspends an already-open dangerous confirmation without
rewriting its input or original keys. Close it explicitly, then GET the original detail;
no automatic Run/reconciliation is added. Last-observed metadata and absent completion
evidence are not a grant, proof of no effects or a replacement for backend lease/fencing.
Both Run entry and final-confirmation handlers reject new calls after unmount; even a fresh
same-ID view cannot lend its mount/client to an old listener. This does not cancel an
already-admitted POST or adopt its late response. Mounted explicit actions keep the original
Operation ID/stable Run key, current guidance and readable-state checks; no automatic Run,
new key or stronger legacy-cache idempotency guarantee is introduced.
Job/Operation Reconcile also rechecks a successfully read, exact `unknown` state and current
local action guidance inside its handler, before creating or persisting a key. Foreground
reloads, read errors, another observed state or an unmounted view cannot reuse an old
listener to send a POST. GET-only recovery does not automatically reconcile; a later
explicit action retains the original ID/key. This is local UI admission, not a current
initiator grant, source outcome or global-state proof; admitted effects cannot be recalled.
The Operation audit timeline distinguishes loading and failed first-page reads from
successful empty queries. Explicit read retry GETs the original Operation's audit
page only; later-page errors retain already-viewed facts and retry the same cursor.
An empty query proves neither absent history nor absent remote effects. Existing
server-side read authorization, tenant scope, read audit and cancellation guards
remain unchanged; no automatic Run/reconciliation or recovery-key changes are added.
Each audit page must match the original root `operation_id`, contain an `events`
array of objects and, if present, a string `next_cursor`. Invalid pages are rejected
whole, not silently filtered or displayed as empty; explicit retry keeps the original
ID/cursor. This is response correlation/basic shape validation, not full fact-field,
signature, authorization or history-completeness verification. OpenAPI describes the
actual `OperationAuditEnvelope`, separate from Operation details/steps.

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
lock; one-time consumption and successful-read audit commit atomically. Downloads
require the current verified reader's direct Snaplink `account.export`, or both
`audit.export` and `audit.read` for audit artifacts, before object IO and again
before local admission. Each check shares a three-second budget across its grants;
missing authority, outage, cancellation and late success fail closed, without a
legacy/owner/initiator fallback. Context deadlines cannot hard-interrupt a
noncooperating dependency; its late success is still rejected. This is not a
whole-download deadline or a Snaplink policy-version/session transaction. Download-handler responses use `no-store`.
Web one-time download entry and final confirmation also require a successful local
detail read, exact `completed` status and current-reader guidance. Foreground metadata
reloads (including body reads), read errors or another observed status suspend the
confirmation; a retained listener cannot start a download after unmount. GET-only
metadata recovery never automatically consumes the artifact. This local admission
neither renews the original artifact/TTL nor proves current authority or availability;
it cannot undo an already-admitted one-time consumption or guarantee another download.
A read admitted before erase or revocation can still arrive afterwards; already-sent
bytes cannot be recalled. These local guards do not prove distributed erasure.
All API replicas must upgrade; old download handlers do not inherit these checks.

Local erased-account locks fence profile/projection writes and quarantine late
source events without their content. Cross-region disposition propagation,
historical retention cleanup, source deletion proof and Legal Hold acceptance
remain distinct follow-ups—not a claim of complete distributed erasure.

Implementation progress and remaining acceptance work:
[`docs/plans/control-plane-production-readiness.md`](docs/plans/control-plane-production-readiness.md).

## Callback operational reads

Owned subscriptions expose `GET /v1/admin/callback-subscriptions/:id/deliveries`
and `GET .../:id/deliveries/:delivery_id` (also internal gRPC). These are bounded,
owner/verified-tenant-scoped current-row metadata reads, not signed payloads, receiver
responses, secrets, endpoints, worker identities or attempt history. Each read checks
current Snaplink `callback.manage` directly and commits a fresh redacted audit fact;
audit failure returns no metadata. Read guidance `callback.read` remains available
without delivery keys/while paused; it cannot authorize writes. The Web console selects
IDs from validated pages/details, applies current action guidance and never auto-replays.
The page's replay button now uses durable `callback_replay` CommandIntent previews,
exact original Delivery/Event confirmation, revision/state/lease CAS and atomic local
queue/audit receipts. Original ID/key GET recovery and queued receipt pointers survive
refresh; explicit GET-only receipt acknowledgment is not receiver completion or cancellation.
New keys require live retained-pointer proof and current guidance, never automatic renewal.
Registration and old HTTP/gRPC replay retain their limited cache/non-durable contracts;
no old unknown result/key is automatically upgraded. Upgraded workers check the exact
live lease and current parent scope/state before endpoint processing and again at HTTP
transport admission. Unavailable/ineligible checks leave the original claim and attempts
untouched; these local checks cannot recall admitted sends or provide dynamic coordinated
pause. Registration/delivery now share one configured network-phase deadline across
DNS, checks, dial/TLS and bounded responses, rather than renewing it per phase.
Connection-time DNS revalidation rejects whole mixed/special-address answers; cap32
addresses/64KiB response headers and close single-use owned sockets on cancellation.
This is not a whole-drain/global limit or remote rollback; destination failures keep
the existing bounded retry/DLQ policy. Original Event ID receiver deduplication,
production DNS/CA/egress/receiver and rolling acceptance remain required. Details:
[Callback delivery recovery](docs/runbooks/callback-deliveries.md).

## Legacy compatibility HTTP API (all under `/v1`, Bearer token required)

The table below describes the currently shipped compatibility surface. New
account-control-plane endpoints are defined by the OpenAPI contract linked in
the Configuration section; source systems remain authoritative for messaging
and object storage.

| Method | Path                                   | Description                                |
| ------ | -------------------------------------- | ------------------------------------------ |
| POST   | `/v1/files`                            | Upload a file (multipart `file`)           |
| GET    | `/v1/files`                            | List your files                            |
| GET    | `/v1/files/:id`                        | File metadata                              |
| GET    | `/v1/files/:id/url`                    | Presigned download URL                     |
| DELETE | `/v1/files/:id`                        | Delete a file                              |
| POST   | `/v1/messages`                         | Send a message                             |
| GET    | `/v1/messages`                         | Inbox (`?unread=true`)                     |
| GET    | `/v1/messages/:id`                     | Get a message                              |
| POST   | `/v1/messages/:id/read`                | Mark read                                  |
| GET    | `/v1/conversations/:uid`               | Conversation with a peer                   |
| POST   | `/v1/comments`                         | Create a comment (`parent_id` for replies) |
| GET    | `/v1/comments?target_type=&target_id=` | Top-level comments                         |
| GET    | `/v1/comments/:id/replies`             | Replies under a root comment               |
| DELETE | `/v1/comments/:id`                     | Delete your comment                        |

`GET /healthz` and `GET /readyz` are unauthenticated. Local presigned downloads
are served at `GET /files/dl` (signature-verified, no token).

The gRPC API (`api/proto/aero_id/v1/aero_id.proto`) mirrors these operations for
internal callers.

## Comment nesting

Comments support exactly two levels (top-level and one reply layer, 楼中楼).
Replying to a reply is rejected with `comment.nesting_too_deep`.
