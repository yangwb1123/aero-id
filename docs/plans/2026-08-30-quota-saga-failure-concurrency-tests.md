# Quota reservation failure-injection and concurrency tests

## Purpose

These tests exercise the account-provisioning saga across the two systems of record:

- local PostgreSQL account, audit, and quota rows;
- the remote billing reservation state machine.

A transport error is not treated as evidence that the remote operation did not happen.
Every test records the remote state and the local state independently, then runs the
recovery/retry path. Tests must use barriers and injected clocks, not sleeps, to select
the failure window.

The suite should not be enabled as a rollout gate against the current Snaplink build:
its DELETE release endpoint does not yet require or persist `Idempotency-Key`.
Use the fake billing server for local tests, and add the cross-service contract test only
after that source contract is shipped.

## Test harness

### Billing state machine

Use a concurrency-safe fake billing server (and a small in-process gateway fake for
service unit tests). A reservation record contains:

```text
(tenant, reservation_id, attempt/generation, reserve_key, commit_key, release_key,
 status, expires_at)
```

The legal transitions are `pending -> committed`, `pending -> released`, and
`pending -> expired`. `committed`, `released`, and `expired` operations are replay-safe.
Each transition has a linearization channel so a test can pause immediately before and
after the CAS. The fake records:

- request count and idempotency key for every operation;
- whether the operation applied before its response was lost;
- the winning transition and generation;
- all rejected stale/fenced operations.

The HTTP version should apply the state transition and then either close the connection
or block past the connector deadline. It must return valid response bodies for successful
replays. It must also be able to return a `committed` body to a release request; the
connector test must reject that body rather than treating any matching ID/tenant as a
successful release.

### Local persistence faults

Add a repository test adapter or SQL driver wrapper with these distinct outcomes:

```text
known rollback       transaction did not commit; no local rows exist
known commit         transaction committed and the caller received success
unknown commit       transaction committed, but Commit returned an acknowledgement error
```

The unknown outcome must be produced below GORM, not by returning an ordinary error from
the repository callback. A `database/sql/driver.Tx` wrapper can delegate `Commit()` to
the real transaction and return `errCommitAcknowledgementLost` only after the delegate
has committed. Queries must use a separate connection to prove what committed.

The service-facing error/result must preserve this distinction. If the implementation
still returns only `error`, the unknown-commit test is an expected blocker: releasing
on every repository error can release a reservation whose account is already durable.

### Crash harness

Crash tests run the provisioning worker in a child process. The child receives a test
DSN, billing endpoint, and one `CRASH_POINT` environment variable, then exits with a
non-zero status at the selected point. The parent owns the fake server and uses a
separate PostgreSQL connection to inspect state. Do not call `os.Exit` in the parent test
process.

Required crash points:

```text
after_reserve_before_local_tx
local_tx_committed_before_remote_commit
remote_commit_applied_before_local_mark
release_request_applied_before_release_response
before_compensation_request
```

The child must flush no test-only state; all evidence comes from PostgreSQL and the fake
billing server. A watchdog fails a hung child and every child is cleaned up with a
bounded context.

### Deterministic coordination

Use channels/events such as `reserveApplied`, `localCommitApplied`, `commitCASReady`,
and `releaseCASReady`. Every blocked operation has a test deadline. Run the race tests
with `go test -race`; use repeated runs for scheduling coverage, but never use a random
sleep to establish ordering.

## Service and repository tests

### 1. Commit acknowledgement loss

**Test:** `TestProvisionUnknownLocalCommitDoesNotReleaseReservation`

1. The fake billing server successfully applies `Reserve`.
2. The injected SQL transaction commits the account, identity, quota-pending row, and
   audit fact, then returns an acknowledgement error.
3. Provisioning returns an `unknown` persistence outcome.
4. Assert that synchronous compensation is **not** issued while the local outcome is
   unknown.
5. Query using a fresh connection and assert exactly one account, identity, quota row,
   and audit fact. The quota row is still pending.
6. Reconciliation reads the local row and remote reservation, retries `Commit` with the
   original deterministic commit key, and marks the row committed. A repeated recovery
   pass is a no-op.

This test fails the current `if err != nil { ReleaseAccount(...) }` implementation and
is a required safety gate, not a test to weaken to accommodate that behavior.

**Control:** `TestProvisionKnownRollbackReleasesExactlyOnce` uses an injected error before
SQL commit. It must issue one release, preserve the provisioning error, and leave no
local rows. A release error does not replace the original persistence error, but it must
create observable recovery work once durable compensation exists.

### 2. Commit applied remotely, response lost

**Test:** `TestCommitTimeoutIsReconciledWithoutRelease`

1. Persist the local account and pending quota row.
2. The billing fake applies `Commit`, then closes the HTTP connection.
3. The caller sees an unknown/timeout result; it must not call `Release`.
4. Retry reconciliation with the same reservation ID and commit key.
5. Assert remote status is `committed`, local status is `committed`, and no release was
   attempted. Repeating the reconciliation must not append another fact or create a
   second reservation.

This covers both a connector timeout and a worker retry after a process restart.

### 3. Reserve timeout after remote success

**Test:** `TestReserveTimeoutAfterRemoteApplyIsReconciled`

1. The fake applies `Reserve` and blocks before sending its response.
2. The client times out and reports an unknown reserve outcome.
3. Assert that provisioning does not immediately release an unconfirmed reservation
   and does not create a second logical reservation.
4. Reconcile by the deterministic reservation ID/key. The retry returns the existing
   pending reservation, after which local persistence and commit proceed.
5. Assert one remote reservation ID, one local account/quota row, and one committed
   final state.

If the source cannot make reserve idempotent, the recovery operation must query the
reservation by an operation key before retrying. A blind second POST is forbidden.

### 4. Release timeout after remote success

**Test:** `TestReleaseTimeoutAfterRemoteApplyIsRetriedWithSameKey`

1. Force a known local rollback after remote reserve.
2. The first release applies `pending -> released`, then loses its response.
3. The caller receives an unknown release outcome.
4. Assert a durable compensation/reconciliation record exists; an in-memory counter is
   insufficient.
5. Run the worker. It retries the exact same tenant, reservation ID, generation, and
   `usr-release-<stable>` key.
6. Assert the final remote status is `released`, the worker records success, and the
   original provisioning error remains the request result.

A repeated release of an already released or expired reservation is successful. A
release response containing `committed` is not successful and must be quarantined for
manual/reconciliation handling.

### 5. Reservation expiry with a pending local row

**Test:** `TestExpiredReservationDoesNotLeavePermanentPendingSaga`

Use an injected remote clock and a short TTL; do not wait five real minutes.

1. Crash after local transaction commit and before remote commit.
2. Advance the remote clock beyond `expires_at` and run reconciliation.
3. Assert that the remote reservation is `expired` and the local row is moved to an
   explicit terminal/recoverable state (or a durable repair task is created). It must
   not remain indefinitely eligible for an impossible commit.
4. Assert an auditable outcome and bounded retry behavior. A new reservation may be
   created only under a new fenced generation; an old release/commit must not affect it.

The current quota table only models `pending` and `committed`, and there is no recovery
worker. Until an explicit expired/reconciliation state is implemented, this test must
remain a rollout blocker rather than asserting that TTL alone repairs local state.

## Process-crash tests

The child-process tests use the same logical identity and a stable operation key across
restart. Expected outcomes are:

| Crash point | Initial durable state | Recovery assertion |
|---|---|---|
| after reserve | remote pending; no local rows | reconcile/release or safely resume the same reservation; never blind-create a second one |
| after local commit | local account + pending quota; remote pending | worker commits the existing reservation, then marks local committed |
| after remote commit | local pending; remote committed | retry/reconcile commit and mark local committed; never release |
| before compensation | known local rollback; remote pending | durable compensation releases exactly once logically |
| after release applied | local rollback; remote released | replay same release key; no error-induced re-reservation |

Each test restarts a fresh child with the same database and fake source state. It then
runs recovery at least twice to prove idempotence. The parent checks that no bearer token,
credential, or source response body is written to the audit record.

## Retry-race tests

### 6. Stale compensation versus retry

**Test:** `TestStaleCompensationCannotReleaseRetryGeneration`

Coordinate this exact sequence:

1. Attempt A reserves generation 1 and fails local persistence with a confirmed rollback.
2. Pause A immediately before its release.
3. Attempt B retries the same identity. It must either take a new fenced generation or
   safely join A's still-owned reservation; it persists its local pending row.
4. Let A release, then let B commit. Repeat with B's commit winning before A's release.
5. Assert the stale A release cannot release B's reservation. If A and B share a
   reservation, the source must reject the stale release using an ownership fence. If
   they use different generations, A's operation must identify generation 1.

Run both orderings and 100 concurrent retries under `-race`. In every successful final
state there is one account/identity and one committed quota row for the winning
reservation. There must be no local committed row backed by a released or expired
remote reservation.

The current `tenantID + reservationID` release contract has no generation/ownership
fence, so this test documents a correctness blocker, not a flaky timing case.

### 7. Commit versus release linearization

**Test:** `TestCommitAndReleaseHaveOneRemoteWinner`

Start a commit and a release concurrently, and pause both immediately before the remote
CAS. Release the barrier, then inspect the winner. Execute both winner cases:

- commit wins: release returns a stale/no-op result; local state is committed;
- release wins: commit returns a stale result; local state is not marked committed and
  recovery creates a new fenced attempt rather than pretending success.

The remote fake must implement the CAS; checking call order is insufficient. Assert one
and only one terminal transition and stable idempotency-key reuse on retries.

### 8. Concurrent same-identity provisioning

**Test:** `TestConcurrentProvisioningIsIdempotentUnderFaults`

Launch 32--100 callers with a start barrier. Inject one reserve response loss, one local
rollback, one remote commit response loss, and delayed release. Collect all results and
run recovery until quiescent. Assert:

- exactly one account and identity;
- at most one quota row per account and one committed winning generation;
- no duplicate audit fact for account creation;
- all retries use the same deterministic key for the same operation;
- stale release/commit operations are rejected or harmless;
- no goroutine remains blocked after the bounded test deadline.

Run this once with the real PostgreSQL repository and once with the service fakes to
separate database uniqueness/locking failures from saga fencing failures.

## Connector contract tests

In `internal/connector/billing`, add table-driven tests for each operation:

- timeout after remote apply returns an unknown/temporary error;
- retry uses the identical idempotency key and escaped reservation path;
- release requires a non-blank, valid reservation ID and key;
- release accepts only `released` (and explicitly supported `expired`) status;
- release rejects `pending` and `committed` responses;
- repeated release of an already released/expired reservation is successful;
- tenant and reservation ID mismatches remain invalid replies.

Use `request.URL.EscapedPath()` and capture every request header. A test that only checks
the final status cannot detect an unsafe new key or a second reservation.

## Execution and rollout gates

Run in this order:

```text
go test ./internal/service ./internal/connector/billing -count=1
go test -race ./internal/service ./internal/connector/billing -count=10
go test -tags integration -run 'Quota|Provision|Reservation|Saga' ./internal/repository ./internal/service
# process-crash tests require Docker/PostgreSQL and a bounded child timeout
go test -tags integration -race -run 'Crash|CommitAck|RetryRace' ./internal/service
```

The suite is green only when all of the following are true:

1. unknown persistence outcomes do not trigger release;
2. reserve, commit, and release retries are keyed and state-idempotent;
3. release failures and crashes leave durable recovery work;
4. expiry transitions local state out of an unrecoverable pending loop;
5. remote commit/release has an atomic transition and stale-attempt fence;
6. Snaplink's DELETE contract requires and persists the idempotency key.

Until items 1, 3, 4, or 5 exist in production code, keep the corresponding tests as
explicit expected-failure rollout blockers and do not hide them with sleeps, broad error
matching, or `t.Skip` in the normal integration job.
