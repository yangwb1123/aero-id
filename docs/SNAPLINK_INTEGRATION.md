# Snaplink production integration

## Authority boundaries

Snaplink is the only authentication, authorization, tenant, organization, and
session authority. Aero ID owns tenant-scoped account profiles, source
references, aggregation snapshots, durable operations, and its local audit
outbox. It never creates a second password, tenant, organization, role, plan,
subscription, wallet, or payment model.

Every authenticated request must locally verify the Snaplink JWT signature and
the configured exact issuer and audience, as well as `exp`, `sub`, and a
non-empty tenant claim. Tenant scope is taken only from the verified token.
Body, query, and forwarding-header tenant values are not routing authority and
are either ignored or checked for equality. Production must configure
`sso.jwks_url`, `sso.issuer`, and `sso.audience`; the upstream validation mode is
retained only for compatibility deployments.

Permissions are resolved by Snaplink in production. Set
`sso.authz_required: true` and `sso.authz_legacy_fallback: false`. The local role
store is a migration aid, not a multi-tenant authorization authority.

## Source event binding

HTTP and gRPC source ingestion require a verified Snaplink machine token with
`source.event.ingest`. The signed `client_id` selects one server-configured
`event_ingest.bindings` entry. The verified token supplies the tenant; the
binding supplies source and source region. Corresponding wire fields are only
consistency assertions, and a missing, unknown, ambiguous, or mismatched
binding fails closed.

HTTP HMAC keys and timestamp checking provide an additional defense-in-depth
transport proof; the current HTTP route is disabled when no HMAC key is
configured. Production must require timestamp binding and should configure a
per-source key rather than the compatibility shared key. Keep the bearer client
credential and the HMAC key in independent secret-manager entries so either can
rotate without changing tenant routing.

Cross-region HTTP projection replication is the controlled exception to the
source-local tenant binding: its Snaplink machine client may use
`tenant_id: "*"` only with `source: aero-id`, and the binding still pins the
sending `source_region`. The event body carries the already-authorized tenant
and the HMAC signs the exact redacted envelope; no caller-supplied tenant or
source can widen that binding.

## Accounts and quota

Accounts and external identities are keyed by tenant. Migration `000048`
assigns existing records to the `legacy` sentinel because the old schema does
not contain enough evidence to infer a tenant. Before enabling tenant-scoped
APIs on an upgraded installation, export the authoritative mapping from
Snaplink, backfill both account tables in one controlled maintenance window,
and verify there are no sentinel rows.

When billing integration is enabled, Aero ID reads the current Snaplink
entitlement through the machine-bound, tenantless metering endpoint and stores
a monotonic local projection. A transient read failure may use a projection no
older than `billing.max_projection_staleness`; missing or stale state fails
closed. A finite grant with `hard: 0` means no accounts may be created.
Unlimited capacity is represented only by `unlimited: true` with zero numeric
limits.

Each verified tenant has a dedicated Billing OAuth client. Its configured
permission set must be exactly `billing:entitlement:read metering:write` and its
only resource must be `billing-api`; the client requests a one-scope token for
each operation, so entitlement reads never carry mutation authority. The
configuration stores only `client_secret_env`, and the referenced value must be
a distinct secret-manager injection of at least 32 bytes. Client IDs and actual
secret values must not be reused by another tenant or by Audit Governance.

`billing.timeout` defaults to five seconds and bounds a shared OAuth token
flight from request creation through response-body processing and cache
publication. A caller's context cancels only that caller's wait; a shared
flight continues independently and returns sanitized `billing: unavailable`
errors on its deadline. Only a safe trace link/traceparent may be copied for
observability; caller credentials, baggage, and other context values are not.
A positive injected `http.Client.Timeout` may make the effective deadline
earlier, but cannot extend the OAuth flight bound. These bounds are
cooperative: production uses the standard context-compliant
`net/http` transport, and any custom `RoundTripper` or response body supplied
as a test/trusted seam must stop `RoundTrip` and `Body.Read` when the request
context is canceled. Go cannot forcibly terminate a non-compliant transport;
such a transport must not be used where the timeout guarantee is required.

New-account creation uses Snaplink `Reserve`, local durable creation, then
`Commit`. The stable reservation and fact keys are derived from the verified
tenant, issuer, and subject. Migration `000051` persists the pending commit next
to the account, so crashes and retries complete the same saga idempotently.
Failed local creation records a fenced, durable release intent and makes one
bounded synchronous attempt; failures are retried by the reconciler. A process
crash before the attempt leaves the reservation pending until reconciliation or
Snaplink TTL expiry. **Rollout blocker:** the currently verified Snaplink
metering DELETE endpoint does not yet require or persist `Idempotency-Key`.
Aero ID compensation must remain disabled until that source contract is
shipped and verified. Entitlement cache state never authorizes a quota
mutation: Reserve, Commit, and Release always go to Snaplink and fail closed.

Existing accounts created before billing enablement require an explicit
Snaplink usage reconciliation before enforcing the account limit. Organization
creation/deletion and organization quota are reconciled in Snaplink itself;
Aero ID deliberately does not duplicate that aggregate.

The entitlement must also enable `billing.account_feature` (default `account`).
A positive user limit without that feature does not enable the account product.

## Audit governance

Aero ID first appends a redacted, tenant-scoped fact to its PostgreSQL hash
chain and creates an `audit_publications` row in the same transaction. A leased
worker then enqueues tenant-scoped callbacks and publishes to Audit Governance
with a stable fact ID. It asks Audit Governance to wait for `ledgered` and only
acknowledges a receipt whose event ID, verified tenant receipt, and durable
status match. Transport failures are retried and eventually dead-lettered;
they do not silently discard the local fact.

Before enabling publication, provision each derived source and one dedicated
OAuth client for that tenant in Audit Governance. Give it exactly
`audit:event:write` for the `audit-governance` resource and register the
configured schema ID, version, event type/classification rules, and retention
class. The audit client ID and secret must differ from every Billing client and
from every other tenant. Configuration stores only its `client_secret_env`
locator; plaintext credentials must come from the deployment secret provider.
An unregistered or incompatible schema correctly remains a retry/dead-letter
condition rather than being bypassed.

Startup fails if a non-legacy account or any pending, leased, or dead-lettered
publication belongs to a tenant without an audit binding. If a new runtime
tenant appears before configuration is rolled out, its publication remains
`pending` with bounded exponential backoff regardless of attempt count. It is
not acknowledged or dead-lettered merely because credentials are absent, and
becomes claimable again after that tenant binding is deployed.

The default v1 contract fixes both `schema_id` and `event_type` to
`aero.id.audit-fact` with classification `personal`. Individual account or
session actions remain in the top-level `action` field; they do not create an
unbounded set of event types or require one governance schema per action.

The Audit Governance source name is a configured prefix plus a base64url
SHA-256 digest of the verified tenant, avoiding raw tenant disclosure while
preserving deterministic isolation. The publisher uses OAuth client
credentials selected only from the durable, verified fact, fixed HTTPS
endpoints, no redirects, bounded JSON, and no request tenant field. Its token
cache and refresh coalescing are isolated per tenant.

Migration `000050` scopes new event receipts and audit event deduplication by
tenant. Existing append-only audit rows are intentionally not rewritten because
tenant and partition fields are covered by their record hashes. Verify and
archive pre-upgrade chains before cutover; old tenantless facts remain outside
tenant API results.

## Availability and disaster recovery

`/healthz` is process liveness. `/readyz` treats PostgreSQL as critical,
Snaplink according to `sso.readiness_critical`, and billing as a reported
non-critical dependency. A non-critical billing probe may leave reads ready,
but account creation still fails closed when central reservation is unavailable.

PostgreSQL is the Aero ID recovery source for accounts, snapshots, pending quota
sagas, operations, the audit chain, and publication outbox. Use managed PITR or
continuous WAL archiving plus a tested cross-region replica. Snaplink billing
remains authoritative for subscriptions, wallets, entitlement revisions, and
global usage; Audit Governance remains authoritative for its ledger and archive.
Do not independently restore these systems to unrelated points and reopen
writes.

Audit bindings are part of the recovery configuration, not database content.
Restore every tenant's distinct Billing and Audit Governance secret injection
before starting workers; the startup coverage gate intentionally keeps the
process closed while durable tenant state lacks a binding.

A recommended Full-tier objective is RPO at most five minutes and RTO at most
thirty minutes; a Minimal single-region deployment can advertise a wider target
only when its backup schedule and contract say so. These are operating targets,
not guarantees provided by the binary.

Recovery order is:

1. Restore Snaplink identity/tenant state and the billing database to a
   transactionally consistent point, but keep resource mutations closed.
2. Restore Audit Governance and Aero ID PostgreSQL, apply migrations, and pass
   database plus local JWT verification readiness.
3. Replay `audit_publications`, then retry pending account quota commits and
   reconcile absolute account usage against Snaplink.
4. Rebuild disposable caches and aggregation projections from durable source
   events; never restore Redis as an authority.
5. Run cross-tenant negative probes, entitlement hard-zero probes, audit receipt
   verification, and a canary account saga before reopening writes.

Backups must be encrypted and restore drills must verify hash chains, tenant
sentinel counts, projection monotonicity, pending saga age, and dead-letter
queues. Rotate leaked OAuth/HMAC credentials before replay rather than copying
them from a database backup.
