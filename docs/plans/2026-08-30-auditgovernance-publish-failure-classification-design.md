# Design — governance publish failure classification

| Field | Value |
|---|---|
| Scope | `internal/connector/auditgovernance`, `internal/service/callback_audit.go`, lease-fenced publication/callback settlement |
| Status | Implemented; lease-fencing hardening included |
| Compatibility | Internal Go error contract plus additive migration `000064`; no HTTP, gRPC, or protobuf change |

## 1. Verification of supplied evidence

The claims were checked against the current tree rather than accepted as authoritative:

| Claim | Result |
|---|---|
| `Publisher.Publish` routes 401 and 403 specially and otherwise calls `validateReceipt` | Confirmed in `internal/connector/auditgovernance/publisher.go` |
| Non-202 `validateReceipt` errors are currently unclassified | Confirmed: `fmt.Errorf("audit governance: HTTP %d", ...)` |
| Non-scope 403 is currently unclassified | Confirmed in `handleForbidden` |
| Invalid 202 receipts use `ErrInvalidReceipt` | Confirmed; receipt parsing/matching rules are already bounded and body-closing |
| No event-rejection sentinel exists | Confirmed in `types.go` |
| Publication settlement only gives special treatment to tenant binding | Confirmed in `publicationOutcome`; `classifyScopeMissing` converts scope failures to `errAuditTenantUnbound` |
| Default and maximum attempts are 8 and 10 | Confirmed in `callback.go` |
| Replay code already exists in the current tree | Confirmed: `AuditPublicationRepository` already exposes replay/purge methods and replay integration tests exist |

The last item matters: the existing dead-letter replay implementation is not part of this change and must not be redesigned or duplicated.

## 2. Design

### 2.1 Connector error API

Add this exported sentinel to `internal/connector/auditgovernance/types.go` with a GoDoc comment:

```go
ErrEventRejected = errors.New("audit governance: event rejected")
```

It is an events-endpoint classification only. It must be wrapped only with safe status metadata, for example:

```go
fmt.Errorf("%w: HTTP %d", ErrEventRejected, response.StatusCode)
```

It must never be joined with or wrapped around `ErrTokenScopeMissing`, `ErrTokenUnavailable`, or `ErrTokenRejected`. The existing token sentinels and their token-endpoint semantics remain unchanged.

### 2.2 Publisher decision flow

Keep `Publish` as one request attempt; do not add retry or cooldown logic there.

1. Validate the fact and tenant binding as today.
2. Fetch the token as today. Preserve token-source error mapping, including propagation of token-endpoint scope rejection.
3. Execute the events request with the existing timeout, no-redirect client, and bearer handling.
4. Preserve the existing 401 and 403 branches:
   - 401: close and bounded-read the body, invalidate the token, then return `ErrTokenScopeMissing` for a scope-coded body or `ErrTokenRejected` otherwise.
   - 403: close and bounded-read the body; scope-coded responses mark cooldown and return `ErrTokenScopeMissing`.
   - non-scope 403 returns `fmt.Errorf("%w: HTTP %d", ErrEventRejected, 403)`.
5. In `validateReceipt`, retain all receipt rules. For a non-202 response, drain at most `maxReceiptBytes`, close the body, and classify only 4xx responses as event rejection:

```go
if response.StatusCode >= http.StatusBadRequest && response.StatusCode < http.StatusInternalServerError {
    return fmt.Errorf("%w: HTTP %d", ErrEventRejected, response.StatusCode)
}
return fmt.Errorf("audit governance: HTTP %d", response.StatusCode)
```

The 5xx and other non-202 behavior remains the existing generic error behavior. A valid 202 remains success; any malformed, mismatched, unsupported, wrong-content-type, oversized, or otherwise invalid receipt remains `ErrInvalidReceipt`.

The response body, bearer token, credentials, URL, stack trace, and topology must not enter any returned error. Existing bounded reads and `defer response.Body.Close()` remain mandatory on every response path.

### 2.3 Settlement API and ordering

`publicationOutcome` remains an internal function and `AuditPublisher` is unchanged. Publication and callback settlement APIs now carry the claim's `lease_generation`; this is an internal repository contract change. Add classification branches in this order:

```text
nil                                             -> completed, ""
errAuditTenantUnbound or ErrTokenScopeMissing  -> pending, audit.binding_unavailable
ErrEventRejected                                -> dead_letter, audit.event_rejected
ErrInvalidReceipt                               -> pending/dead_letter, audit.invalid_receipt
attempt >= MaxAttempts                         -> dead_letter, audit.publication_failed
otherwise                                       -> pending, audit.publication_failed
```

The binding branch precedes exhaustion, preserving the existing never-dead-letter behavior for tenant binding and scope failures. In normal drain execution, `classifyScopeMissing` already maps `ErrTokenScopeMissing` to `errAuditTenantUnbound`; the explicit sentinel check in `publicationOutcome` protects the function contract and direct callers without changing scope-missing metrics.

For `ErrEventRejected`, return `dead_letter` on the first attempt regardless of `MaxAttempts`. The `next_attempt_at` value passed to `FinishPublication` is not claimable for a dead-letter row; retain the existing delay calculation for that field to avoid unrelated scheduling changes.

For `ErrInvalidReceipt`, use `audit.invalid_receipt` both before and at exhaustion. It remains retryable because a 202 may mean governance accepted the event despite an unusable receipt; the existing event id/idempotency key makes retries safe under the established conflict-receipt behavior.

For all other failures, preserve the current attempt increment, delay, lease, bulkhead, and `MaxAttempts` behavior.

Each claim increments a durable `lease_generation`. `FinishPublication` and `MarkDelivery` require the claimed generation, owner, delivering status, and a live database-clock lease (`lease_until >= now()`). This rejects both an expired finisher before reclaim and a stale finisher after the same worker ID reclaims the row. The attempt counter remains settlement-based: a crash after remote success but before settlement leaves `attempt` unchanged, and the re-claimed service attempt settles as the next committed attempt.

## 3. Compatibility constraints

- No public HTTP/gRPC/protobuf or transport response changes.
- No configuration changes; default `MaxAttempts` remains 8 and the upper bound remains 10.
- No retry loop is added to `Publisher.Publish`.
- The all-non-auth-4xx rule is status-based: HTTP 408/425/429 responses are permanent governance rejections, while a transport-level or context timeout remains transient and is retried by the durable drain.
- No changes to OAuth token endpoint classification, 401 invalidation, 403 scope cooldown, receipt matching, or conflict receipt success.
- Existing rows with `audit.publication_failed` are not rewritten. New permanent rejections use `audit.event_rejected`; invalid receipts use `audit.invalid_receipt`.
- Existing dead-letter replay remains operator initiated and unchanged; its existing code/payload handling can distinguish the new settlement code.
- No audit fact, audit persistence, or new write path is added. Migration `000064` additively introduces non-null `lease_generation bigint DEFAULT 0` on `audit_publications` and `callback_deliveries`.
- Every new `Publish` call in tests must check its error.
- The sentinel is an internal-package Go API. Consumers must use `errors.Is`, never compare error strings.

## 4. Failure-mode matrix

| Condition | Publisher result | Settlement |
|---|---|---|
| Any non-auth 4xx (including 400/408/425/429, 404/413/422, and non-scope 403) | wraps `ErrEventRejected` | immediate `dead_letter`, `audit.event_rejected` |
| 401 with scope marker | wraps `ErrTokenScopeMissing`; token invalidated | pending, `audit.binding_unavailable` |
| 401 without scope marker | wraps `ErrTokenRejected`; token invalidated | pending, `audit.publication_failed` |
| 403 with scope marker | wraps `ErrTokenScopeMissing`; cooldown marked | pending, `audit.binding_unavailable` |
| token endpoint scope rejection | existing scope sentinel/marker | pending, `audit.binding_unavailable` |
| transport/client failure | `ErrTokenUnavailable` | existing transient settlement |
| 5xx or other unclassified non-202 | generic HTTP error | existing transient settlement |
| 202 invalid receipt | `ErrInvalidReceipt` | retry with `audit.invalid_receipt`, then dead-letter with that code |
| valid or matching conflict 202 receipt | `nil` | completed |

A permanent rejection is still subject to the normal lease-guarded `FinishPublication` update. If that update fails, the row remains recoverable under the existing lease-expiry path; the connector must not retry inline.

## 5. Implementation sequence

1. Add and document `ErrEventRejected` in `types.go`.
2. Change only the non-202 4xx branch of `validateReceipt` and the non-scope branch of `handleForbidden`.
3. Extend `publicationOutcome` with the binding, event-rejection, and invalid-receipt classifications in the order above.
4. Add connector tests, then service settlement tests.
5. Add the additive lease-generation migration and update repository/service settlement contracts and tests.
6. Run the repository gates, including integration compilation and the lease-fencing integration tests.

Rollback of classification remains a source-only revert. If the fencing change is rolled back, first deploy a binary that no longer requires `lease_generation`, then revert migration `000064`; settlement codes are historical row values.

## 6. Test plan and acceptance mapping

### Connector unit tests — `internal/connector/auditgovernance/publisher_test.go`

Use an `httptest` server with a valid token response and an events response containing a deliberately unique body marker. Table-test 400, 404, 413, and 422, and separately test non-scope 403. Assert:

- `errors.Is(err, ErrEventRejected)` is true;
- `ErrTokenScopeMissing` and `ErrTokenUnavailable` are false;
- the error contains the status but not the body marker, token, or endpoint URL;
- the response body is closed and bounded.

Keep existing scope-coded 403 tests unchanged and add assertions that those errors do not satisfy `ErrEventRejected`.

Add a 202 table covering malformed JSON, mismatched event ID, mismatched tenant ID, missing `accepted_at`, and unsupported status. Assert `ErrInvalidReceipt` and explicitly assert it does not satisfy `ErrEventRejected`. Retain valid and matching-conflict receipt tests.

Add a 5xx test asserting the error remains generic and does not satisfy `ErrEventRejected`; retain or extend transport and token regression tests.

### Service unit tests — `internal/service/callback_audit_test.go`

Extend the existing recording publication repository/fakes for the lease-generation parameter. Add:

1. `publicationOutcome` cases for event rejection at attempts 1 and `MaxAttempts`, both producing `dead_letter/audit.event_rejected`.
2. Invalid receipt at attempt 1 and at `MaxAttempts`, producing `pending/audit.invalid_receipt` and `dead_letter/audit.invalid_receipt` respectively.
3. A drain test with a publisher returning `fmt.Errorf("%w: HTTP 422", auditgovernance.ErrEventRejected)` and an item at attempt zero. Assert exactly one publish, exactly one finish, persisted attempt 1, `dead_letter`, `audit.event_rejected`, and no retry state.
4. Persistent generic/5xx and `ErrTokenUnavailable` tests that reach `MaxAttempts` and settle `dead_letter/audit.publication_failed`, never `audit.event_rejected`.
5. Existing tenant-unbound and scope-missing tests at both ordinary and exhaustion-edge attempts, proving they remain pending with `audit.binding_unavailable`.

### Integration coverage

In the existing tagged callback/repository integration harness, use real `audit_publications` and `callback_deliveries` rows and the real claim/settlement paths. Verify committed row state, attempt, lease clearing, and error code. Include expiry-before-reclaim and same-worker-ID reclaim cases for both row types. The crash-after-governance-success test must run the service settlement path and assert remote deliveries = 2 while the committed-settlement attempt is 1.

### Gate mapping

| Acceptance | Evidence |
|---|---|
| 4xx is permanent and safe | Publisher table tests for 400/404/413/422/non-scope 403 |
| First-attempt terminal settlement | Drain test: one publish and one `FinishPublication`, code `audit.event_rejected` |
| Transient compatibility | Existing exhaustion test plus explicit 5xx/transport/token-unavailable cases |
| Invalid receipt remains distinct and retryable | Publisher invalid-202 tests plus two `publicationOutcome` cases |
| Authentication regressions | Existing 401 invalidation, token rejection, scope, cooldown, valid/conflict receipt tests |
| No leak | Error-string assertions for body, token, URL, and credentials |
| Audit safety | `make check-audit-swallow` and `make check-audit-manifest` |
| Repository quality | `make test`, `make lint`, `make lint-strict`, `make check-file-sizes`; run `make test-integration` for settlement changes |

The change is accepted only when all rows in this mapping are green and no existing replay, lease, or publication-attempt invariant has changed.
