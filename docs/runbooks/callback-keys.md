# Callback master-key maintenance

Applies to builds containing `internal/service/callback_crypto.go` and the
`callback.delivery_paused` / `callback.secret_envelope_version` configuration.
This is an implementation/rollout contract, not evidence of production acceptance.
Do not enable these settings on an older binary; `config.Load` rejects unknown keys.

## Configuration and failure policy

Supply keys through environment or an approved secret provider, never a committed
ConfigMap, command-line argument, API response, log, or audit payload.

| Setting / environment                                                  | Contract                                                                                                                                                                                                          |
| ---------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `master_key` / `AERO_ID_CALLBACK_MASTER_KEY`                           | Active encryption key. Printable ASCII without spaces or commas, 32–4096 bytes; generate at least 32 random bytes and encode them. Required in production-like environments unless delivery is explicitly paused. |
| `previous_master_keys` / `AERO_ID_CALLBACK_PREVIOUS_MASTER_KEYS`       | Up to four retained keys, comma-separated with no padding. No duplicates or repetition of the active key. Same strength/format requirements.                                                                      |
| `secret_envelope_version` / `AERO_ID_CALLBACK_SECRET_ENVELOPE_VERSION` | `0` (default) writes the legacy-compatible format; `1` writes `v1:<key-id>:<ciphertext>` after every reader has upgraded. Both formats are readable by this implementation.                                       |
| `delivery_paused` / `AERO_ID_CALLBACK_DELIVERY_PAUSED`                 | `true` rejects registration with `callback.paused`/503 and stops callback claims and callback DLQ purge. Listing, deletion and audited replay remain available.                                                   |

Configured keys must remain valid even while paused. A non-empty
`previous_master_keys` always requires an active key, including during a pause.
To boot paused without an active key, leave both key fields empty and retain the
keys in the approved provider for restoration before unpausing.

The key identifier is a domain-separated HMAC of the AES key, **not** the key
itself. AES-GCM authenticates the version/key identifier as additional data.
The existing AES derivation is unchanged so legacy ciphertext remains readable.
Rotating the master key does not rotate a receiver's per-subscription signing secret.

An absent usable active key never claims deliveries. A missing retained key,
unsupported/malformed envelope or authentication failure makes no network call:
its claimed delivery is returned to `pending` for five minutes, without increasing
`attempt` or changing its event ID/payload. The update requires the current lease
owner, generation and unexpired lease, using the database clock. If the update
fails, the row remains leased until recovery; it is not falsely settled.

The audit ledger/publication drain and durable callback fan-out continue while
callback delivery is paused. A pause is **not** a queue-size limit: monitor database
capacity/backlog and bound the maintenance window. Destination failures still use
the normal bounded delivery-attempt/retry/DLQ path; an attempt counter alone does
not prove any HTTP bytes reached the receiver.

Upgraded workers also check the exact live claim and current enabled/verified
parent before endpoint processing and HTTP transport admission. Refusal preserves
the original leased row/attempt/Event ID; it does not reset a delivery. These
[local send checks](callback-deliveries.md#worker-send-admission-checks) cannot
recall already-admitted calls and are not a dynamic pause/acknowledgment protocol.
Every API/worker sender still needs the static configuration rollout below.

The upgraded [network-phase budget and destination policy](callback-deliveries.md#network-phase-budget-and-destination-policy)
shares the configured timeout across DNS, both send checks, dial/TLS and response
reads. Connection-time DNS is rechecked, special/shared-address ranges fail closed,
answers are capped at32 and response headers at64KiB; owned single-use sockets close
on deadline/cancellation even during TLS. This does not undo a receiver effect or
turn a network timeout into an eligibility-check refusal. Review production
DNS/CA/egress and all existing destinations before rollout; never widen the
private-target setting or change keys/Event IDs to bypass a network refusal.

## Safe staged rollout

1. Keep the current active key and `secret_envelope_version: 0`. Deploy the
   reader-capable binary to **every** API and worker process in every region that
   can read these subscription rows. This deployment currently starts callback
   workers in API instances too; checking only the dedicated worker Deployment
   is insufficient. Keep the old key in the approved secret provider.
2. On those upgraded binaries, retain the current active key and distribute the
   **future** key through `previous_master_keys`. Restart all readers and verify
   they can read both keys before any writer changes the active key. Configuration
   is static per boot, not hot-reloaded.
3. Change `master_key` to the future key and move the old key into
   `previous_master_keys`. During the rolling transition, both generations can
   decrypt ciphertext written by the other. Keep the complete retained-key set
   on every reader.
4. Once every reader supports envelopes, optionally set
   `secret_envelope_version: 1`. New registrations use the active key/versioned
   envelope; older subscriptions are not silently re-encrypted. Verify signed
   delivery and absence of `secret_unavailable`/`defer_failed` increases.
5. Retain old keys until no subscription requires them, including disabled
   subscriptions, restored backups, regional copies and rollback targets.
   **There is no automatic re-encryption or key-retirement tool in this slice.**
   Existing subscriptions must retain their keys or be deliberately replaced via
   the authenticated/audited subscription APIs. Do not edit ciphertext with SQL.

After versioned writes begin, do not roll back to a legacy-only reader. A safe
rollback uses a reader-capable binary with every key used by either generation;
changing the write format back to `0` does not convert existing envelopes.

A metadata-only inventory is safe; never select or print the ciphertext/key values:

```sql
SELECT CASE WHEN secret_ciphertext LIKE 'v1:%'
            THEN split_part(secret_ciphertext, ':', 2)
            ELSE 'legacy-unattributed' END AS envelope_key_id,
       count(*) AS subscriptions
FROM callback_subscriptions
GROUP BY 1;
```

A legacy row cannot be assigned to a particular key without authenticated
inspection; an inventory of versioned rows alone is not permission to retire keys.

## Recover from missing key / bad configuration

- If startup rejects the callback key configuration, restore the provider-backed
  keyring or explicitly pause delivery. The callback check performs no DB I/O;
  there is no new hard database startup ping.
- If running readers report secret failures, set `delivery_paused=true` on all
  readers while restoring the active/retained keyring. Pausing only one replica
  cannot stop other readers.
- Restart with the correct ring, then clear the pause. Deferred deliveries resume
  at `next_attempt_at` (normally within five minutes), retaining the remote retry
  budget. No manual replay is required for these pending rows.
- Malformed/tampered ciphertext is also deferred, not treated as a receiver outage;
  investigate integrity rather than ignoring the alarm or removing audit facts.
- Deliveries already dead-lettered by older binaries are not reset automatically.
  Use the separately authorized/audited replay flow after restoring keys.

## Metadata recovery during maintenance

[Delivery recovery](callback-deliveries.md) documents the owned-subscription
list/detail HTTP and gRPC reads. They need current authoritative `callback.manage`
and durable read audit, but do not decrypt/read ciphertext or require a delivery
master key. `callback.read` guidance therefore remains available while paused;
it is not a write grant. The Web console conservatively disables management writes
when delivery is paused/unconfigured. Existing authenticated deletion/replay APIs
remain available under their compatibility contract, including its cache/unknown
outcome limitations. Reading metadata does not resume workers or reset attempts.

## Observability and verification

The existing `aero_control_callback_deliveries_total{status=...}` counter adds
bounded labels `paused`, `key_unavailable`, `secret_unavailable`, `defer_failed`,
`send_ineligible` and `send_check_failed`.
They count skipped drain passes/deferred or refused outcomes, **not backlog gauges**.
`secret_unavailable` reports a successfully persisted local deferral;
`defer_failed` requires investigation of lease loss/DB or wiring failures.
`send_ineligible` is a failed local claim/parent eligibility check;
`send_check_failed` is unavailable wiring/DB/context/deadline evidence, not a
receiver outage. Both leave the existing claim untouched and prove no absence of
an older remote effect. No key, endpoint, ciphertext or receiver response is a metric label.

Unit tests cover legacy-format writes, old/new-key reads, non-key identifiers,
GCM tamper rejection, pause/config guards and detached bounded deferral contexts.
PostgreSQL tests cover attempt/dedup/audit preservation, expired/same-owner
reclaimed lease fencing, resumption with the original signing secret, and a
controlled loopback HTTPS lost-acknowledgment retry with the same signed Event ID.
The last exercise uses a local test CA and receiver dedup seam, not a deployed
receiver/production trust acceptance:

```bash
go test ./internal/config ./internal/service ./internal/repository ./internal/app -count=1
go test -tags integration -p 1 \
  ./internal/repository ./internal/repository/auditintegration ./internal/service \
  -run '^TestCallback(DeliveryDeferral|KeyRotation|SendAdmissionPostgres|NetworkPostgres)' -count=1 -timeout=3m
```

Run the backend done gate and `make test-integration` before release. Targeted tests,
compile-only checks and a run without the Docker reaper are not full integration,
external receiver, multi-region or rolling-deployment acceptance evidence.
