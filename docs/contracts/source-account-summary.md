# Source account-summary and command contract

This document is the aero-id-side contract for the HTTP seam intended for use by
the Snaplink, Aero IM, and Aero Vault connectors. It is intentionally a source
API contract, not a database integration contract. Each source team must expose
an adapter that enforces its own resource authorization and residency policy.

## Request

The configured summary path receives `GET` requests. The checked-in Aero Vault source snapshot exposes this seam at `/v1/internal/account-summary`; Aero IM exposes it at `/internal/account-summary`. Keep the path configurable for ingress deployments that mount either service under a different base.

- `account_id`: aero-id UUID, opaque to the source
- `region`: requested source region when present
- repeated `dataset`: allow-listed dataset names. Snaplink summary requests require a concrete, non-empty, source-owned dataset list. Aero Vault summary requests also carry a concrete list on the wire; an omitted selection expands locally in registry order.
- `reconcile=true` only for a bounded reconciliation call on sources that
  explicitly publish that parameter. The bound Aero IM path rejects this
  parameter; reconciliation is an aero-id workflow mode, not an IM wire field.

### Snaplink deployment verification and scope semantics

The checked-in Snaplink source snapshot (`ac5d367041bc6042a30b6695777dd8d600aeed4d`) does not expose
`GET /internal/account-summary`: it contains no account-summary route, handler, or dataset parser. The
Aero ID deployment manifests likewise use a placeholder Snaplink endpoint, and leave `erase_path` empty.
Therefore the deployed Snaplink API's response semantics for omitted or empty dataset scopes cannot be
confirmed from the available source and deployment artifacts; this document does not claim a Snaplink HTTP
status or error body for those cases.

Snaplink capabilities are explicit configuration, not path inference. The shipped adapter keeps `summary`,
`erase`, and `operation_status` disabled and leaves `erase_contract_version` empty. An enabled endpoint must
set `capabilities.summary: true`; erase additionally requires a source-owner-verified version in the compiled
adapter registry. The registry currently has no production Snaplink erase version, so no deployment may enable
erase or status until the owner contract covers authorization, idempotency, source operation IDs, status
semantics, and ambiguous/late responses.

The current **Aero ID connector contract**, pending deployment and owner confirmation of that Snaplink seam,
is fail-closed:

- Summary requires one or more concrete Snaplink registry datasets. An omitted `dataset` query, an explicit
  empty `dataset=` value, and nil or zero-length direct request scopes return local
  `source.dataset_scope_invalid` (`ErrorInvalid`, non-temporary), before token acquisition or HTTP.
- Erase likewise requires a concrete non-empty dataset array. An omitted, `null`, or empty JSON array is
  rejected locally with the same error before idempotency checks, credentials, or HTTP.
- Service compatibility paths may expand an empty legacy Snaplink summary selection before calling the
  connector. That compatibility behavior does not make an empty direct connector scope valid.

Before calling this a deployed Snaplink API contract, Snaplink owners must publish and verify the endpoint's
behavior for omitted `dataset`, `dataset=`, omitted erase `datasets`, and `datasets: []`/`null`, including its
status and stable error shape. Until then, callers must send explicit allow-listed datasets.

The connector also propagates `X-Request-ID`, `X-Aero-Operation-ID`,
`X-Aero-Account-ID`, `X-Aero-Canonical-UID`, `X-Aero-Actor-UID`,
`X-Aero-Tenant-ID`, `X-Aero-Region`, W3C trace headers, and a source-valid
`Authorization` bearer token when configured. For Aero Vault it additionally
sends `X-Aero-Tenant`, the source's active tenant-context header, with exactly
the same tenant value as `X-Aero-Tenant-ID`; it never lets a request tenant
select a different credential. For Aero IM, the connector must also send one
`X-Aero-Target-Assertion` compact JWS. The account, canonical UID, tenant, and
region headers are required exact consistency fields for that protocol; they
are not an authorization bypass on their own.

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
domain, not `AERO__OIDC__JWKS_URI` or the general integration JWKS. It must
use HTTPS even for loopback-looking hosts and must not redirect; Aero IM
rejects cleartext and redirect responses at this dedicated boundary. The
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
issuer/key ID/client ID, and an exact HTTPS `target_assertion_trust_domain`
that equals the issuer. The dedicated JWKS URL must use that same trust-domain
origin. The audience is `aero-im-account-summary`, the subject is
`aero-id-sync`, the OAuth scope is `aero.account.summary.read`, the OAuth
resource is `aero-im`, and the summary path is canonical. Deployments use a
different trust-domain/JWKS bundle for each Aero IM target environment and
region. Its source OAuth client ID must match the assertion `client_id`. The
shipped configuration leaves this block disabled; enabling it without the
external JWKS and matching Aero IM configuration is not a valid rollout.

## Deferred Operation and Sync-job authorization

Operation workers use the stored initiator and tenant/account scope, not the current
administrator or historical user credentials. Production rechecks Snaplink's configured
Authorizer.Check directly, without legacy local fallback, for the root command and the
actual forward/compensation action. These global grants are distinct from enqueue's
self-service ownership eligibility: source enablement must also verify initiator action
grants for the configured aero-id AuthZ client. The authority does not expose a policy
version or tenant-specific source resource ACL through this Check contract.

Checks run before claims and again before each effect/status query, with a shared
three-second admission deadline. Local live leases, account disposition and the durable
directory/epoch must also match before calling a source. Denials/outages pause the
original receipt with transactional audit; they never authorize fallback, a second
command or destructive compensation. A forward/compensation lease expiry remains
unknown until original-key/source-ID reconciliation. An unknown forward effect is not
rollback authorization. These local checks are not atomic with external authority or
source requests; the source must still enforce its own resource authorization/fencing.
Sync/Erase/Export/audit-export jobs also check the durable initiator directly, the
current tenant/account/home/disposition and exact live lease/directory fence after
claiming and before source calls, projection application, content pages, artifact
Put/recovery reads and publication. Audit exports require both `audit.export` and
`audit.read`; each page rechecks the grants. Each admission uses a shared three-second
budget. These checks neither fabricate a client/session grant nor supply source ACLs.

Audited deferral preserves original source operation IDs/results, stored summary
replays and original artifact key/digest/size/TTL. Only an untouched execution claim
with no durable recovery state may return to pending; a reconciliation or started
claim remains unknown. Background eligibility is locally paced for one minute;
manual checks do not bypass current authorization or leases. Expired Sync execution
leases commit unknown and expiry audit before returning the sentinel, never reissue
an erase. Status-query admission is not renewable permission to repeat erase;
even an explicit `not_accepted` result must pass new action/lease admission and keep
the original idempotency key. An absent summary recovery port does not authorize
another source read. Revoked export generation does not prevent retention disposal
of the original expired/invalidated object, and disposal never authorizes a new Put.
Real authority/resource-ACL, sends followed by revocation/lease loss, WAN, old-writer
and storage/retention acceptance remain external gates.

## Source authentication

Snaplink and Aero IM may use OAuth client credentials, a transitional static bearer token, or mTLS. The
static token is trimmed and control-character values are rejected. mTLS requires HTTPS and its configured
certificate is installed even when a custom HTTP client is injected; mTLS-only mode sends no bearer header,
while mTLS may accompany an explicitly selected bearer or OAuth mode.

The deployed Aero Vault account-summary handler is different: it requires a Snaplink-validated
`PrincipalService`. An enabled Vault connector therefore **must** use a Snaplink client-credentials RFC 9068
access-token JWT (`typ=at+jwt`) with `sub == client_id`, a `jti`, no human-session claims, exact request scope
`read`, resource/audience `aero-vault`, and an explicit `oauth_tenant_id`. Static API keys, local JWTs,
mTLS-only mode, and unauthenticated loopback are rejected before any HTTP call. The configured Snaplink client
must be mapped by Vault's `AUTH_JWKS_CLIENT_TENANTS` to that exact tenant; `*` is not a valid mapping for this
connector. `X-Aero-Tenant` is sent as Vault's active tenant header, while the legacy `X-Aero-Tenant-ID`
consistency header is also sent with the same value.

OAuth token authorities use an exact origin allow-list. A host-only entry is the explicit HTTPS:443 shorthand;
non-default ports and schemes must be listed exactly. The source and token endpoints use separate TLS profiles
(`tls_*` versus `oauth_tls_*`); CA roots, SNI, and client certificates are never shared implicitly. OAuth
acquisition is lazy and the client secret is sent only to the token authority, never to the source. The OAuth
dialer resolves an allow-listed name per new connection, rejects private/loopback/link-local answers outside the
explicit development tuple, and dials the selected address literally. Production deployments must still enforce
the same destination policy with DNS/network egress controls; DNS names alone are not an IP authorization
boundary.

Aero Vault's `region` must equal the aero-id cell's `account.region`, and the Vault deployment must set
`AERO_VAULT_ACCOUNT_SOURCE_REGION` to that same value. Requests for an account in another home region belong
in that account's home cell; the connector does not use a local Vault credential to cross a regional boundary.

Unauthenticated access is available only to Snaplink/Aero IM compatibility connectors with
`allow_insecure_loopback: true` in an explicitly approved non-production environment (`local`, `dev`,
`development`, `test`, `testing`, or `ci`) and only for the exact hosts `localhost`, `127.0.0.1`, or `::1`.
Unknown, blank, and production-like environment names are rejected. Vault never emits a loopback compatibility
metric because its deployed summary contract has no unauthenticated mode. Source credentials are supplied by
environment/secret provider only; non-empty bearer or OAuth secret values in YAML are rejected. Compatibility
metrics use only fixed source/mode labels; no endpoint, token, tenant, or request identity is a metric label.

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

At the aero-id connector boundary, erase requests require a concrete, non-empty dataset array owned by the target source. Snaplink, Aero IM, and Aero Vault reject an empty erase scope locally before idempotency validation, bulkhead acquisition, credentials, or HTTP execution. Invalid, duplicate, mixed-source, malformed, and oversized scopes are local `source.dataset_scope_invalid` failures and never trigger source authorization or network I/O.

The Aero Vault adapter defaults to the checked-in read-only summary seam at
`/v1/internal/account-summary`; an explicit `summary_path` is a compatibility
override and is validated as a path, never interpreted as a URL. Its erase and
status paths remain unset and its `capabilities.erase` and
`capabilities.operation_status` flags remain false in shipped configuration.
A path alone never enables a capability. Do not treat the optional command wire
shape below as a shipped Vault capability until the source command contract is
deployed and the matching capability flag and path are explicitly enabled.

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
