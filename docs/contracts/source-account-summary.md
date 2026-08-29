# Source account-summary and command contract

This document is the aero-id-side contract for the published HTTP seam used by
the Snaplink, Aero IM, and Aero Vault connectors. It is intentionally a source
API contract, not a database integration contract. Each source team must expose
an adapter that enforces its own resource authorization and residency policy.

## Request

The configured summary path receives `GET` requests. The checked-in Aero Vault source snapshot exposes this seam at `/v1/internal/account-summary`; Aero IM exposes it at `/internal/account-summary`. Keep the path configurable for ingress deployments that mount either service under a different base.

- `account_id`: aero-id UUID, opaque to the source
- `region`: requested source region when present
- repeated `dataset`: allow-listed dataset names. Aero Vault summary requests always carry a concrete, source-owned dataset list; an empty summary selection expands locally in registry order.
- `reconcile=true` only for a bounded reconciliation call on sources that
  explicitly publish that parameter. The bound Aero IM path rejects this
  parameter; reconciliation is an aero-id workflow mode, not an IM wire field.

The connector also propagates `X-Request-ID`, `X-Aero-Operation-ID`,
`X-Aero-Account-ID`, `X-Aero-Canonical-UID`, `X-Aero-Actor-UID`,
`X-Aero-Tenant-ID`, `X-Aero-Region`, W3C trace headers, and a source-valid
`Authorization` bearer token when configured. For Aero IM, the connector must
also send one `X-Aero-Target-Assertion` compact JWS. The account, canonical UID,
tenant, and region headers are required exact consistency fields for that
protocol; they are not an authorization bypass on their own.

## Aero IM target binding

Aero IM accepts the summary only when the bearer is a validated RFC 9068
client-credentials token **and** the dedicated `X-Aero-Target-Assertion` is a
valid EdDSA compact JWS. The assertion has protected `typ`
`aero.account-target+jwt`, an explicit `kid`, and claims `iss`, `aud`, `sub`,
`client_id`, `account_id`, `canonical_uid`, `tenant_id`, `region`, `datasets`,
`method`, `path`, `request_hash`, `scope`, `jti`, `iat`, `nbf`, and `exp`.
`client_id` must equal the validated bearer client; the signed account and
identity fields must equal the authoritative account loaded by Aero ID. The
request is accepted only for `GET /internal/account-summary`, with the exact
canonical query fields and source region. Datasets are unique and UTF-8-byte
sorted. `jti` is one-time cluster state in Aero IM Redis and is consumed only
after every signature, claim, query, and header check.

Aero ID does not publish this public key set. Deployment therefore owns an
explicit external JWKS contract: `AERO__ACCOUNT_SUMMARY__TARGET_ASSERTION_JWKS_URI`
in Aero IM must point to an HTTPS endpoint managed for this dedicated trust
domain, not `AERO__OIDC__JWKS_URI` or the general integration JWKS. The
endpoint must publish a bounded RFC 7517 document containing only verification
keys such as:

```json
{"keys":[{"kty":"OKP","crv":"Ed25519","x":"<base64url>","kid":"<explicit-id>","use":"sig","key_ops":["verify"],"alg":"EdDSA"}]}
```

Every `kid` must be unique. During rotation publish old and new public keys
for at least the assertion lifetime plus clock skew, JWKS cache TTL, and
propagation margin. The Ed25519 private key remains an Aero ID secret and is
never placed in this document, an OAuth secret field, a log, or a request
payload. If the dedicated JWKS cannot be obtained, Aero IM fails closed before
any account lookup.

The Aero ID signer is enabled only with one safe private-key reference (an
absolute `0600`-style file or a named secret environment variable), explicit
issuer/key ID/client ID, audience `aero-im-account-summary`, subject
`aero-id-sync`, OAuth scope `aero.account.summary.read`, OAuth resource
`aero-im`, and the canonical summary path. Its source OAuth client ID must
match the assertion `client_id`. The shipped configuration leaves this block
disabled; enabling it without the external JWKS and matching Aero IM
configuration is not a valid rollout.

## Source authentication

An enabled Aero IM connector must use OAuth client credentials, a transitional static bearer token, or mTLS.
The static token is trimmed and control-character values are rejected. mTLS requires HTTPS and its configured
certificate is installed even when a custom HTTP client is injected; mTLS-only mode requires HTTPS. Unauthenticated access is available only
with `allow_insecure_loopback: true` in a non-production-like environment (`prod`, `production`, or `staging`
are never allowed) and only for the exact hosts `localhost`, `127.0.0.1`, or `::1`. The corresponding
configuration override is `AERO_ID_SOURCES_AERO_IM_ALLOW_INSECURE_LOOPBACK`; it is intended only for local/test
compatibility and emits a bounded construction metric.

## Workload isolation

Each source connector reserves independent bounded capacity and circuit state for
`query` (summary), `command` (erase/status), and `health` (health probes).
`max_concurrency` is the compatibility aggregate; its default partition
reserves most capacity for queries and non-zero command/probe pools. The
optional `query_max_concurrency`, `command_max_concurrency`, and
`health_max_concurrency` settings override a class without allowing probes or
commands to consume the interactive query pool. Circuit openings are likewise
class-scoped, so a source health outage does not make account reads fail fast.

## Commands and scope rules

Erase requests require a concrete, non-empty dataset array owned by the target source. Aero Vault rejects an empty erase scope locally before idempotency validation, bulkhead acquisition, credentials, or HTTP execution. Invalid, duplicate, mixed-source, malformed, and oversized scopes are local `source.dataset_scope_invalid` failures and never trigger source authorization or network I/O.

The currently deployed Aero Vault adapter publishes the read-only summary seam
only; its erase and status paths remain unset in the checked-in deployment
manifest. Do not treat the optional command wire shape below as a shipped Vault
capability until the source command contract is deployed and the paths are
explicitly enabled.

## Summary response

```json
{
  "source_region": "us-east-1",
  "version": 42,
  "generated_at": "2026-08-20T12:00:00Z",
  "complete": true,
  "coverage": {
    "datasets": ["aero-im.profile"],
    "sources": true,
    "memberships": true
  },
  "datasets": {
    "aero-im.profile": {"display_name": "Ada"}
  },
  "sources": [
    {
      "source_account_id": "opaque-source-id",
      "scope_type": "workspace",
      "scope_id": "opaque-scope-id",
      "status": "active",
      "data": {"role": "member"}
    }
  ],
  "memberships": [
    {
      "scope_type": "workspace",
      "scope_id": "opaque-scope-id",
      "source_member_id": "opaque-member-id",
      "role": "member",
      "status": "active",
      "data": {}
    }
  ]
}
```

`version` is an immutable, globally monotonic source revision for the
`account/source/coverage` stream; a source-region move must not reset it.
`generated_at` and `source_region` are mandatory for production responses.
The optional `coverage` object explicitly declares which dataset, source-reference,
and membership classes the revision covers. When present, aero-id never infers
omission authority for an undeclared class. Legacy responses without `coverage`
use the requested datasets and source-wide reference compatibility semantics.
`complete=false` means the source itself could not produce the requested set. aero-id also marks
the result incomplete when it receives an unknown or wrong-source dataset.
Payload data is allow-listed and redacted at the connector boundary; sources
must never return passwords, tokens, cookies, MFA/recovery material, object
bytes, unrestricted message bodies, or source credentials.

## Commands and reconciliation

The optional erase command is `POST` to the configured `erase_path` with a JSON
body containing `account_id`, `canonical_uid`, `datasets`, `operation_id`, and
`idempotency_key`. It carries the same attribution headers and
`Idempotency-Key`. A successful response contains `status` and, when available,
`source_operation_id`.

The optional `status_path` is a `GET` accepting `account_id`, `operation_id`,
and `source_operation_id`. It returns a terminal or non-terminal `status` and
the source operation ID. A lost response is `unknown`, never an inferred
failure. Commands must be safe to repeat with the same idempotency key.

`idempotency_key` is mandatory for erase commands. Empty or whitespace-only
keys are rejected locally before request construction, token acquisition, or
any source call. Automatic retries and reconciliation reuse the exact same key
in both the JSON field and `Idempotency-Key` header. A timeout, transport
failure, or exhausted retry budget remains an unknown outcome until the source
status endpoint confirms it.

## Failure and version rules

- `2xx` with a valid JSON body is a source result.
- `401/403` map to authorization errors and are not retried.
- `429` and `5xx` are temporary; reads may be retried within the configured
  budget. Commands are retried only when `Idempotency-Key` is present.
- Timeouts are unknown outcomes for commands.
- Oversized, malformed, wrong-region, or unsupported-version responses are
  invalid and must not be projected.
- Source events remain the preferred freshness path; periodic reconciliation
  repairs missed events and is always bounded.
