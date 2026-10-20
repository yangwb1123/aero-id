# Callback delivery metadata and recovery

This is implemented local behavior, not real receiver/browser/WAN acceptance.
Use the matching API/Web release and apply the shipped migrations through `000075` (the current guarded startup minimum).
Source account APIs and Vault/IM authorization are not involved in these reads.

## Read the original subscription and delivery

```text
GET /v1/admin/callback-subscriptions
GET /v1/admin/callback-subscriptions/:id/deliveries?status=dead_letter&limit=50&cursor=<UUID>
GET /v1/admin/callback-subscriptions/:id/deliveries/:delivery_id
```

Internal equivalents: `CallbackService.ListCallbackDeliveries` and
`CallbackService.GetCallbackDelivery` in `api/proto/aero_id/v1/aero_id.proto`.

- Use verified actor/issuer/subject/client/tenant claims and current Snaplink
  `callback.manage`. The service uses the direct checker, not legacy local roles
  or ownership as a fallback. Authorization, metadata and audit share a three-second
  budget. Snaplink Check is not a policy-version/original-session proof or a
  distributed transaction with this database.
- Subscription ownership and verified tenant are both required, including for
  administrators. A supplied `tenant_id` never selects another tenant. Wrong owner,
  tenant, subscription or delivery is `404 callback.delivery_not_found`.
- Lists support only `pending`, `delivering`, `success`, `dead_letter` (or no status
  filter). UUID cursors must advance in descending ID order; default20/max100,
  Web50, `limit + 1`, no offset. Empty owned subscriptions return an audited empty
  list; missing/foreign subscriptions do not become empty successful reads.
- Responses are `data.deliveries` + `next_cursor` or `data.delivery`, HTTP `no-store`.
  Only IDs/action, local status/attempt, positive all-writer `revision`, claim generation/expiry, next eligibility,
  recorded HTTP code, fixed safe error code and timestamps are selected. There is
  **no** payload, receiver response, endpoint, ciphertext or worker ID lookup.
- Each successful page/detail has its own `callback.deliveries.read` /
  `callback.delivery.read` fact, committed in the read transaction while the parent
  ownership/deletion fence is held. Audit failure returns no metadata with
  `503 callback.delivery_read_failed`. Denial is403; missing/outage/late authority
  is503, never permission via a stale cache or a local role fallback.

These are current local rows, not durable per-attempt history, receiver business
receipts, a global point-in-time view or causal watermarks. Worker settlement can
race pagination. A previously admitted read may arrive after deletion; its bytes
cannot be recalled. A missing row does not prove an earlier delivery had no effect.

## Interpret the evidence conservatively

| Local state/code                     | Operator boundary                                                                                                                                                   |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pending`                            | Eligible according to local timing; do not start a second replay just because it is waiting.                                                                        |
| `callback.secret_unavailable`        | Restore approved keyring/configuration; no endpoint call or remote attempt was consumed by this local deferral. Missing/corrupt envelopes are not receiver outages. |
| `delivering` / expired lease         | May already have receiver effects or a lost acknowledgment. Expiry is not proof of failure or permission to reset.                                                  |
| `success` / HTTP2xx                  | Recorded transport response, not proof of receiver business completion.                                                                                             |
| `dead_letter`                        | Local retry exhaustion/failure, not proof of no remote effect. Verify receiver idempotency and the original event before any new manual intent.                     |
| `unknown` / `callback.error_unknown` | Unsupported historical metadata is sanitized. Investigate original controlled audit/receiver evidence; never display raw diagnostics or infer an executable state.  |

[Key maintenance](callback-keys.md) describes deployment-wide pause and staged
master-key rotation. Metadata reads require no delivery key and remain available
while paused. `callback.read` is a separate **guidance feature**, backed by the same
current authoritative permission; it is not a write grant or a new Snaplink role.
The Web conservatively disables registration/delete/replay guidance while delivery
is paused/unconfigured; separately authorized compatibility APIs retain their
existing management contract.

## Worker send-admission checks

The upgraded dispatcher checks the original claim before endpoint validation/DNS
and again immediately before entering the existing HTTP transport. Each check
receives a three-second sub-budget; both also inherit the original shared network-phase
deadline described below. A late return never authorizes dispatch; a context is not
a hard interrupt for an implementation that ignores cancellation.
Missing/typed-nil checking ports, database failures, canceled contexts and late
successful checks fail closed. The production repository implements the port on
the existing Callback repository; there is no new sender or execution API.

The single existence query selects no payload, response, endpoint or ciphertext.
It checks delivery/subscription/event/action/attempt, the exact worker/generation/
lease deadline, an unexpired **PostgreSQL-clock** lease, original owner/tenant and
an enabled, verified parent. No HTTP call is admitted on a refused check. In
particular, a queued claim cannot borrow its eligibility from before a bulkhead
wait, and a parent disabled/deleted between the checks is rejected at the second.

A refused check does **not** settle, increment attempts, clear/renew the lease,
return the row to pending, queue a replacement or rewrite stored diagnostics.
The original `delivering` row remains for existing lease recovery with its original
Event ID. This says only this invocation was not admitted to the underlying
transport; an earlier sender may already have effects. Independently record
`send_ineligible` versus `send_check_failed` in the existing status counter; these
observations are not a durable attempt-history or exception-center record.
An actual admitted HTTP timeout still follows the existing remote-attempt policy;
`net/http` masking a pre-transport check timeout must not consume that budget.

These are local point checks, **not** a transaction with the receiver, a new
Snaplink grant/session/policy-version proof, dynamic coordinated pause/resume or
a guarantee that an admitted call stops when a later state change commits. The
separate network budget below bounds this invocation, not a durable pause barrier.
Already-admitted calls/bytes cannot be recalled; lease loss after admission leaves settlement uncertain and
receiver deduplication by the original Callback Event ID remains mandatory. Older
binaries do not run these checks: upgrade every sender and retain rolling/WAN
acceptance. Configuration pause is still static per boot and must cover every
API/worker replica; do not treat SQL subscription edits as an audited pause API.

## Network-phase budget and destination policy

`callback.request_timeout` (default5s, capped at5m; an earlier caller deadline wins)
is one shared network-phase budget, not a fresh timeout for each lookup or HTTP
step. Registration starts it before the first endpoint/DNS validation and shares it
with challenge construction, revalidation and the challenge POST. Delivery starts
it after local secret validation and the bulkhead wait, before the first send check;
endpoint validation, the second check, connection-time resolution, dial, TLS,
response headers and bounded response-body reads consume that same deadline.
Queue waits, decryption, database claim/settlement, the entire drain batch and
publication/governance calls are not covered by this new network-phase bound.
It neither renews a claim nor guarantees a queued batch fits its lease: an already
admitted call can outlive that lease and fail settlement, still requiring original
Event ID deduplication.

- Every resolution receives that context and rejects empty, failed, malformed or
  more-than32-address results. This is a cap on **returned answers/connection
  candidates**, not resolver-internal memory, OS threads, DNS wire messages or a
  global concurrency quota. Late successful answers never authorize a connection.
  A missing resolver/dialer fails closed; there is no unbounded fallback lookup.
- Re-resolve and validate the **whole** answer at connection time, then dial only
  validated literal IPs under the remaining budget. A mixed public/disallowed
  answer is rejected, never filtered down to its public members. This closes a
  prevalidation-to-dial rebinding path; it does not attest routing or a receiver.
- Production destinations remain HTTPS without userinfo, redirects or ambient
  HTTP proxies. Reject localhost/local names (including trailing-dot forms), scoped
  IPv6 addresses, private/shared/link-local, protocol/documentation/benchmark,
  multicast/reserved and IPv6 transition ranges. IPv4-mapped addresses are unwrapped;
  IPv6 is restricted to2000::/3 with explicit special-use exclusions. This is a
  conservative code-reviewed policy, **not** a complete/dynamically refreshed IANA
  registry or proof that every public route is safe. Keep independent egress/DNS
  controls; validate dual-stack/NAT64/VPN behavior before rollout. Do not enable the
  development-only private-target option in production to bypass a refusal.
- Each client owns its connection: no keepalive reuse, at most64KiB response headers,
  and the existing response-body budget (default64KiB, capped at16MiB). Preserve
  normal TLS certificate/hostname verification. Retain the owning deadline despite
  net/http's detached dial context; set the socket deadline and close it on owner
  cancellation, including after dial/during TLS or body reads. Close the response
  body/client idle transport on exit. This is cooperative cancellation plus owned
  socket cleanup, not a process-level interrupt for arbitrary noncooperating code
  or a guarantee that resolver-internal background work has stopped.
- Never adopt a challenge or successful body returned after the original deadline.
  A refused **send check** still leaves the claim/attempt untouched as above.
  Destination validation/DNS/TLS/header/body failures retain the existing bounded
  delivery-attempt/retry/DLQ policy, even if no HTTP bytes reached the receiver;
  that counter is not proof of a receiver call or business effect. An admitted
  timeout/2xx/lost body can follow a real receiver effect, so use the original
  Event ID to deduplicate. No new key/event/delivery, automatic write retry,
  dynamic pause API, receiver acknowledgment or remote rollback is introduced.

The deterministic network/connection/race tests and PostgreSQL plus controlled
loopback HTTPS lost-acknowledgment exercise are local implementation evidence,
not deployed receiver, production DNS/CA/egress, browser, WAN or rolling acceptance.
Existing destinations in newly rejected special-use ranges now fail closed;
review approved endpoint/network policy before deploying every sender.

## Durable version-bound replay through CommandIntent

The backend reuses the existing preview/confirmation/disposition engine and signed
Callback dispatcher. It does not call the endpoint while preparing or confirming.
First obtain an owned, current `dead_letter` metadata row and verify receiver deduplication.
Use your own active canonical account as `account_id`, not the receiver or another owner's account.

```text
POST /v1/command-intents
Idempotency-Key: <persisted original intent key>
```

```json
{
  "account_id": "<original caller account UUID>",
  "plan": {
    "kind": "callback_replay",
    "datasets": [],
    "callback": {
      "subscription_id": "<original subscription UUID>",
      "delivery_id": "<original delivery UUID>",
      "event_id": "<original Callback event UUID>",
      "revision": 7,
      "lease_generation": 4
    }
  }
}
```

Confirm only that returned intent with `POST /v1/command-intents/:id/execute`
and its original version/digest. The five-minute preview TTL is only an admission
budget, never cleanup/replacement-key authority. Preparation and execution require
verified principal/client/tenant binding, the direct current `callback.manage`
check, enabled delivery configuration, an enabled/verified owned subscription,
and local account/directory/writer fencing. Each request has a three-second budget.

- The row must still match the original subscription/event/revision/generation,
  be `dead_letter`, and have **no owner and no lease timestamp**, even an expired one.
  Every delivery UPDATE, including compatibility resets and worker settlement,
  advances the database-enforced revision. Changed metadata is stale, not permission
  to silently renew or make another intent. PostgreSQL time is rechecked after child-lock waits.
- Queue modification, generation advance, fresh `callback.delivery.replayed` fact,
  executed intent and its immutable receipt commit together. Audit failure rolls back all.
  Event ID, payload, response evidence and attempt count are preserved. The new path
  never selects payload, endpoint, ciphertext, response or worker identity.
- `status=executed`, `receipt_kind=callback_replay`, `receipt_id=intent_id` prove
  **local queuing only**. Subsequent confirmation of that original intent returns
  the original receipt even after settlement or subscription deletion. It never resets
  again and is not a delivery-completed/receiver-business acknowledgment.
- Recover by original intent ID or `GET /v1/command-intents/by-key` with the original
  header key; never auto-retry or mint a replacement key. Explicit `abandon` can close
  only a prepared intent, atomically competing with Execute. It does not cancel a
  queued send, retract bytes, roll back receiver effects or release Legal Hold.
- This is local CAS, not a transaction with Snaplink or the receiver. An old expired
  sender may already have effects; callback event deduplication remains required.

## Web preview, confirmation and durable-pointer recovery

The page now uses the CommandIntent path above, never the old replay endpoint.
Select an owned subscription/delivery detail, then explicitly request the original
CAS preview. Only a validated `dead_letter` with no lease timestamp (even expired)
has a candidate prompt. Input both the exact original Delivery ID and Callback Event ID
before Execute; current guidance/identity is checked again after hashing/before a
new POST and at confirmation. Neither report, metadata, nor manual confirmation is
an execution grant or proof of receiver idempotency.

- Save the original UUID key/hash pointer before the first POST. Unknown preparation
  recovers with original-key GET; the returned tuple must match the original hash.
  A known intent ID recovers only with GET plus immutable principal/account/metadata
  hash/receipt matching. Refresh never auto-Prepares/Executes. While the mounted
  page retains the exact tuple it may explicitly retry that original prepare/key;
  the tuple is not stored across refresh. NotFound/expiry/prepared are not permission
  to replace the key. Corrupt/excess pointers fail closed.
- A valid queue receipt stays in the bounded scope-specific pointer store through
  refresh. Enter its exact intent ID and click the explicit acknowledgment action
  to **GET**-verify its original executed receipt before allowing another selection.
  This records only local operator acknowledgment, not receiver completion/cancellation,
  delivery evidence cleanup or remote rollback. It never sends a command.
- Before any later new preview, all relevant abandoned/acknowledged pointers are
  re-read serially with one overall 15-second GET budget and matched to caller/account,
  kind/ID/immutable hash and terminal receipt. Local flags, missing/changed/prepared
  replies, outage or a spent budget do not authorize another key. Recheck current
  action eligibility after these reads and before POST. Maximum 16 pointers includes
  closed and acknowledged entries; no automatic eviction or retention is implemented.
- Original-intent recovery stays mounted even when subscription reads fail or writing
  is paused/denied. Recovery is not permission to write. Identity change/unmount
  rejects late reads/writes and aborts GETs, but cannot recall a submitted command,
  endpoint call or bytes. Existing `abandon` closes only prepared admission or returns
  the execute-won original queued receipt.

Registration retains its separate HTTP cache/in-memory-key boundary. No old unknown
legacy replay/key is automatically migrated. Real browser/receiver/WAN acceptance
remains open. There is no new command-intent gRPC replay RPC; old
`ReplayCallbackDelivery` is unchanged.

## Existing manual replay is still a limited compatibility command

```text
POST /v1/admin/callback-subscriptions/deliveries/:id/replay
Idempotency-Key: <original intent key>
```

This endpoint remains for compatibility callers, not the current Web replay button.
It can reset changed states without the new intent's revision/state/lease CAS.
**This compatibility path has no durable version-CAS guarantee**, and new metadata
reads or a safer Web button do not upgrade it or prove absence of receiver effects.

- Verify receiver deduplication by the original Callback event ID first: metadata
  `event_id`, the actual sender's `X-Aero-Callback-Event-ID` header and the signed
  body's top-level `event_id` agree; `X-Aero-Event-ID` in older notes is not this sender's header;
  nested source/request/operation/resource IDs are not interchangeable dedup keys. The subscription
  signing secret/event ID stay unchanged; signatures/timestamps are generated by
  the normal signed-delivery worker. Public replay preserves attempt count; CLI
  dead-letter requeue resets its retry budget under a different audited contract.
- `data.status=queued` confirms only local queuing. It does not confirm delivery,
  business success, cancellation, rollback or absence of another in-flight send.
- The old internal gRPC `ReplayCallbackDelivery` has no durable business key or
  generic HTTP response cache; the new metadata RPCs do not make replay retries safe.
- The key replays an HTTP response only within the configured generic idempotency
  retention window. Cache loss/overflow/expiry may re-execute; there is no durable
  body/client/version-bound replay receipt. Current GET/NotFound/expiry cannot repair
  that guarantee. Never automatically retry or mint a replacement key.
- Unknown legacy registration/replay must retain its original input/key;
  registration metadata must match the original endpoint/event set/tenant before
  the key is cleared. Registration's mounted UI/in-memory recovery does not guarantee refresh recovery.
  One-time secrets are never stored in response-replay bodies or browser persistence.
- Current guidance denies/unknown/outage disable confirmation without discarding
  the original input. Authorization errors cause metadata capability GET refresh,
  not another write. Closing/unmounting a view does not cancel a submitted command.

Dynamic coordinated pause/resume, hold-aware callback history/deletion, backlog gauges,
real browsers/receivers/rolling deployments and
WAN/old-writer acceptance remain open. Reading metadata does not purge old stored
payloads/responses/ciphertext, remove immutable facts or release Legal Hold.
