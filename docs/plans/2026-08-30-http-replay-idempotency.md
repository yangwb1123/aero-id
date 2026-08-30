# HTTP callback replay idempotency

## Contract

`POST /v1/admin/callback-subscriptions/deliveries/{id}/replay` requires a
non-empty `Idempotency-Key` header (maximum 200 bytes). The key identifies one
replay command and is scoped by the authenticated actor, HTTP method, concrete
request path, and query string.

The first request executes the replay, which resets the delivery and appends
one `callback.delivery.replayed` audit fact. The HTTP idempotency middleware
stores the safe `200 {"data":{"status":"queued"}}` response. A retry with
the same key and delivery ID replays that response without invoking the service,
so it does not append another audit fact. A concurrent retry receives
`idempotency.in_progress` (409).

A key must not be reused for another delivery or another command. Keys are
retained only for the configured idempotency TTL (24 hours by default). After
the TTL, the key is a new command and the caller must not assume replay
protection. A missing or overlong key is rejected before the service runs.

The existing service behavior remains intentional for non-HTTP callers:
direct calls to `ReplayDelivery` represent distinct user intents and mint a
fresh audit event each time. Operator requeue APIs retain their existing
status-scoped, transactional semantics.

## Lost-response behavior

If the response is lost after the reset and audit fact commit, retrying the
same HTTP request with the same key returns the stored response and performs no
second reset or audit append. Response settlement uses a short detached
context, so a client disconnect does not cancel persistence of the completed
receipt. The guarantee depends on the idempotency store being available and
the retry arriving before retention expires; store failures remain observable
through idempotency metrics.

The middleware scopes records to the concrete URL path rather than Gin's route
template. This prevents a key used for delivery A from replaying a response for
delivery B, while preserving the existing actor isolation.
