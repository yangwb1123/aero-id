# Agent Hub membership projection check

`POST /internal/v1/agent/membership-check` is an optional, read-only eligibility
check for the Agent Hub. It is registered only when `agent_membership.bindings`
contains at least one complete service-principal binding. It reuses the existing
strict Control Plane authentication chain and requires verified `issuer`,
`subject`, `tenant_id`, `client_id`, and the configured audience. It does not add
a second token verifier or accept a caller-selected tenant or membership scope.
It also requires the signed OAuth `scope` claim to contain the exact
`aero-id.membership:check` scope. Missing, malformed, or unrelated scopes are
denied before the membership projection is queried. Existing routes continue
to authenticate as before; scope enforcement is local to this endpoint.

```yaml
agent_membership:
  bindings:
    - issuer: https://identity.example
      subject: agent-hub
      client_id: agent-hub
      tenants:
        - tenant_id: tenant-a
          source: snaplink
          scope_type: tenant
          scope_id: tenant-a
          max_age: 10m
```

Each binding fixes the service's issuer, subject, client, allowed tenants, and
the Aero-ID projection scope used for each tenant. Configuration rejects
duplicate identities/tenants, incomplete scopes, and freshness windows outside
`1s..30d`. No bindings means the route is absent. The request accepts only an
existing Aero-ID `account_id`:

```json
{"account_id":"00000000-0000-0000-0000-000000000001"}
```

The result is `{"data":{"eligible":true,"tenant_id":"tenant-a","account_id":"..."}}`.
The response uses `Cache-Control: no-store`. Unknown fields, repeated JSON keys,
invalid account IDs, and oversized requests are rejected. A principal or tenant
without an exact configured binding is forbidden. The endpoint looks up the
account only inside the tenant carried by the verified token and never calls
account provisioning.

Every authorized membership decision, including `eligible:false`, appends one
durable `agent.membership.checked` audit fact before returning the result. The
fact contains the tenant, authenticated subject/client/issuer, account ID,
eligibility, required OAuth scope, and the fixed membership source/scope. It
does not contain the bearer token or arbitrary request data. If the audit
append fails, the endpoint returns 503 and withholds the eligibility result.

## Snaplink client and scope registration

Snaplink's client-credentials grant mints `sub == client_id == client.ID` and
uses `client.TenantID` for `tenant_id` (omitted when empty). Its signed `scope`
claim is the granted OAuth scope list encoded as one space-separated string.
The `aud` claim is emitted only when the request includes a resource. These
facts are implemented by `internal/handler/tokengrant/token_client_credentials.go`
and `infrastructure/defaultimpl/issue_payload.go` in Snaplink. The Aero-ID
binding must therefore use the exact client ID for both `subject` and
`client_id`, a non-empty client tenant that matches `tenant_id`, the exact
Snaplink issuer configured for Aero-ID, and a requested resource matching the
Aero-ID verifier audience.

Register `aero-id.membership:check` in Snaplink's global OAuth scope registry
when that registry is enabled, and include it in the machine client's
`allowed_scopes`; bind `aero-id` as an allowed/requested resource so the token
has the required audience. The relevant Snaplink config shape is:

```yaml
# Relevant client fields only; register its authentication secret/mTLS material
# through Snaplink's normal client-registration process.
clients:
  - id: agent-hub
    active: true
    token_strategy: jwt
    tenant_id: tenant-a
    allowed_scopes: [aero-id.membership:check]
    allowed_resources: [aero-id]

oauth:
  scope_registry:
    enabled: true
    extra_scopes: [aero-id.membership:check]
```

The sample `issuer` and `tenant_id` above must be replaced with the deployed
values; the default issuer in Snaplink's `cmd/sso-server/config.yaml` is
`sso-server`. Never assume a token has `tenant_id` or `aud`: both are omitted
when the machine client is not tenant-bound or does not request a resource.

Eligibility requires the local account to be active and at least one matching
membership projection row to be active, error-free, and have a non-null
`last_synced_at` within the configured age window. Missing, stale, future-dated,
or inactive projection data returns `eligible:false`; a local dependency error
returns 503. The caller cannot choose `source`, `scope_type`, `scope_id`, or
`max_age`.

This is a bounded-stale projection check, not an authoritative live revocation
query. In the current account-summary ingestion path, `last_synced_at` records
when Aero-ID applied the projection. A source change may remain visible until a
new summary or reconcile is applied; once the configured age window expires,
stale data denies. Hub resource grants remain the authority for instance and
operation permissions; this endpoint only gates whether a mapped account is
currently eligible to use those grants.

## Hub account mapping and freshness

OIDC subject semantics belong to the configured issuer. Hub must configure an
explicit `(tenant_id, subject, client_id) -> Aero-ID account_id` mapping; it
must not assume that `sub`, `uid`, email, or a tenant name is a global Aero-ID
account key. Use the exact signed Snaplink values above. This endpoint does not
add a legacy-token bypass. It checks a bounded-stale Aero-ID projection, so
membership changes remain subject to the configured freshness window.

## Verification

```bash
go test ./internal/config ./internal/repository ./internal/service ./internal/handler/http ./internal/app
go test -tags integration ./internal/service -run '^TestAgentMembershipDecisionPersistsDurableAuditFact$' -count=1
```
