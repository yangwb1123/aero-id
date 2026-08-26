# Source account-summary and command contract

This document is the aero-id-side contract for the published HTTP seam used by
the Snaplink, Aero IM, and Aero Vault connectors. It is intentionally a source
API contract, not a database integration contract. Each source team must expose
an adapter that enforces its own resource authorization and residency policy.

## Request

The configured summary path receives `GET` with:

- `account_id`: aero-id UUID, opaque to the source
- `region`: requested source region when present
- repeated `dataset`: allow-listed dataset names
- `reconcile=true` only for a bounded reconciliation call

The connector also propagates `X-Request-ID`, `X-Aero-Operation-ID`,
`X-Aero-Account-ID`, `X-Aero-Canonical-UID`, `X-Aero-Actor-UID`,
`X-Aero-Tenant-ID`, `X-Aero-Region`, W3C trace headers, and a source-valid
`Authorization` bearer token when configured. Sources must re-check their own
resource permissions; these headers are attribution and correlation context,
not an authorization bypass.

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
