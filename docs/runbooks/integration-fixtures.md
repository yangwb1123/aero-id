# Integration fixture diagnostics

`make test-integration` runs real, isolated PostgreSQL and MinIO fixtures. Docker
availability, compilation and lifecycle helper tests do not establish storage or
full-suite acceptance. Keep the shipped images, readiness checks, per-test
isolation, assertions and default budgets when diagnosing failures.

## Capture the existing gate

With the same Ryuk-disabled environment as CI, add JSON output **only**:

```bash
workdir=$(mktemp -d)
GOFLAGS=-json TESTCONTAINERS_RYUK_DISABLED=true make test-integration \
  > "$workdir/integration.jsonl" 2>&1
status=$?
printf '%s\n' "$status" > "$workdir/status"
printf 'gate status=%s, diagnostics=%s\n' "$status" "$workdir"
```

Read the saved status and final output before reporting a result. This does not
change the default 20-minute outer/15-minute package deadlines, `-p 1`, test
selection or `-count=1`. CI uses the same JSON-only addition with `pipefail` and
retains `integration.jsonl` for seven days; tee must not mask the gate failure.
Artifact collection is not guaranteed after runner cancellation. The Make
command echo/error lines are not JSON. With jq
available, these are diagnostic filters, **not** substitutes for the gate status:

```bash
jq -R 'fromjson? | select(.Action == "output") |
  select((.Output // "") | contains("fixture=")) | [.Package, .Test, .Output]' \
  "$workdir/integration.jsonl"
jq -R 'fromjson? | select(.Test == null) |
  select(.Action == "pass" or .Action == "fail") | [.Package, .Action, .Elapsed]' \
  "$workdir/integration.jsonl"
```

PostgreSQL reports `owned` (exact container ID), `run`, `connection_string`,
`open_pool`, `migrate`, `close_pool` and `terminate`; MinIO reports `owned`, `run`
and `terminate`. Timed phases include elapsed duration and `ok`/`failed`. SQL
pool opening is lazy, not a separate connectivity proof. The composition-root
fixture shares the container ownership/run/cleanup helper but keeps its own
host/port/configuration assembly. Logs identify test fixtures, not production
readiness; do not publish secrets or dump Docker container environments.

## Bounded two-package full-suite diagnostic

The original `make test-integration` and its CI integration job remain the serial
reference gate. Do not rewrite an earlier timeout as success. When cumulative
fixture startup leaves later packages unreported, compare the separate full-suite
entry point:

```bash
workdir=$(mktemp -d)
TESTCONTAINERS_RYUK_DISABLED=true make test-integration-bounded \
  > "$workdir/integration.jsonl" 2>&1
status=$?
printf '%s\n' "$status" > "$workdir/status"
```

This invokes Go's native package scheduler with fixed `-p 2`, `./...`, integration
build tags and `-count=1`. It keeps the same default 20-minute outer/15-minute
package budgets, all original tests, fixture images, readiness strategies and
per-test databases. It does not shard/filter tests, reuse containers, retry failed
packages or increase timeouts. Two package processes are **not** a global cap on
containers, goroutines, nested Go commands or remote calls inside those processes.

The new entry emits JSON explicitly, clears `GOFLAGS` so ambient `-run`, `-skip`,
`-exec` or cached-count options cannot quietly select a subset, disables ambient
Go workspaces and uses the installed toolchain with readonly modules. Docker info
has a separate 30-second admission budget. A denied/unavailable daemon stops the
command before test execution and is a failed admission, not a test pass. Ordinary
verified module provisioning may still need the network on a cold cache.

`make check-integration-runner` exercises the actual Make recipes with private
recording commands, pins exact full-suite argv/default budgets, rejects inherited
selection flags for the new entry and checks failure propagation/no retry and
bounded Docker denial. These Docker-free probes also run in the standard unit
suite and CI check-gates; they are **not** real fixture/storage acceptance. The
serial reference retains its original environment behavior; use the JSON-only
invocation above when comparing it.

Treat a nonzero complete diagnostic as failure even when every other package
passes. Reaching the real S3 tests and seeing a pinned-image pull failure is useful
coverage evidence, not a green storage gate. Built-in deployed-contract skips do
not establish real-source acceptance. Adoption as a replacement release gate
requires an explicit decision and measured runner capacity; this additional
entry does not remove the serial gate or close a readiness stage.

Measured [Increment 14](../plans/control-plane-production-readiness.md) is a
**failed experiment, not a validated optimization**: all 48 packages reported
terminal results within about 19m28s, but Repository exhausted its unchanged
15-minute package limit (149 of 185 top-level tests passed, one unfinished and 35
never started). Both real S3 tests also failed the unchanged pinned-image pull.
The timeout stack was in a new fixture's readiness at three seconds of that
individual test, not evidence that the test itself ran for 15 minutes. Do not
infer complete test-case execution from complete package-result coverage or
replace the original gate on this evidence.

## Test-only audited repository boundary

`internal/repository/auditintegration` is a tagged, black-box contract test package,
not a second repository/audit service or a new production binary. It owns the
existing audit/anchored receipt, publication/Callback replay, Legal Hold and
legacy evidence tests. White-box ledger and unrelated account/projection/command
tests remain in `internal/repository`. Both complete Make entries still select
`./...`; no custom shard launcher, run filter or exclusion is introduced.

The relocation preserves 185 original top-level tests: 132 in the root package
and 53 in the audit contract package. Four small private fixture functions are
copied unchanged so the tests do not need cross-package database/credential
sharing. Every existing test still creates its own fixture with the original
image, readiness and project migrations. No TestMain, pooled fixture or additional
parallel test call is introduced.

The ordinary unit suite and `make check-integration-runner` validate
`internal/config/testdata/repository-test-relocation.json`: original full-file
SHA-256 after normalizing only the package identifier, absence of old copies,
exact combined name inventory, and equivalent copied function ASTs. Negative
probes cover changed assertions/tags/package/name, omitted/duplicate tests,
old copies, TestMain, production Go files and altered copied fixtures. Updating
this reviewed baseline is not permission to delete/skip/weaken a regression.
Before/after native tagged `go test -list` must also match; source inventory and
compile/negative probes alone do not establish execution or real storage.

Package boundaries affect the native per-package clocks, but **not** the original
20-minute whole-process budget or fixed two-package diagnostic scheduler. A pass
for each package in separate invocations is not combined acceptance. The serial
reference/CI gate and previous failed runs remain intact; use one complete
invocation and its actual terminal test/package receipts when judging the result.
Deployed Vault/Forge skips and S3 image failure remain distinct from PostgreSQL
assertion coverage. See the [package notes](../../internal/repository/auditintegration/README.md).

Measured Increment 15 completes the original185 tests (root132 + audit53) in
**one** complete p2 invocation within about13m12s, without a package/outer timeout.
All49 package results match the tagged package inventory; both real S3 tests
still fail the unchanged pinned-image pull, so the command remains failed. The
six existing deployed Vault/Forge skips prove no source acceptance. All284
recorded owned Docker IDs were explicitly not-found after completion; no manual
removal was needed, and unobserved/lost-create IDs remain outside that proof.
This is a local observation, not a CI/hardware/load guarantee or a serial-gate
pass. The serial reference was not rerun or replaced by that experiment.

Increment16 subsequently runs the **original serial reference** with JSON output
only and unchanged p1/count1/full selection/default budgets. All49 packages report
terminal results (41 PASS/7 no-test-file skips/1 FAIL), with all original185
repository tests passed in that same invocation (root132/audit53). There is no
package/outer timeout: first-to-last event span is about19m55s, with whole-command
timestamps spanning about1196s. This very narrow local margin is **not** a stable
capacity/CI/load guarantee or a controlled comparison against p2. Service also
completes, but both actual S3 tests still fail the unchanged pinned MinIO pull,
so the full serial command remains failed (Make status2/Error1). Six existing
deployed-contract skips still prove no source acceptance. All287 recorded exact
owned IDs independently inspect explicitly not-found; no manual deletion was
needed and unobserved IDs remain outside this proof. Keep earlier failed runs
and the approved-image/storage/Compose/release gates open.

Increment17 preserves that historical result but the next **original serial** run
again fails the20-minute outer bound (Make status2/Error124), not a package timeout.
Only37/49 package terminals arrive; root132 tests PASS616.883s, but audit35 pass,
one remains unfinished and17 original tests never start:167/185 passes is not
exhaustive acceptance. The final audit-matrix subcase is only about5s old during
fixture startup when interrupted, not proof of a business hang. Service/storage
are not reached, so this run does not newly reproduce S3 success or failure.
The failed whole invocation cannot be repaired by separate remaining-package
passes, and Increment16's narrow margin is not stable CI capacity evidence.

After the driver/test processes disappear, exact-ID inspection finds233 of235
recorded containers absent and two still running. Bounded removal touches only
those two hook-recorded IDs; independent inspections then prove both, and all235
known IDs, explicitly not-found. This is manual owned-ID recovery, **not** automatic
cleanup or proof about unobserved/lost-create IDs. Preserve the raw failed run,
package/original-test inventories and before/removal/after evidence; do not
increase budgets, share fixtures, weaken assertions or change MinIO to claim green.

A separate unfiltered six-package diagnostic completes in about250.747s under
8-minute outer/7-minute package bounds: full service PASS172.946s, testpg59.485s,
SSO0.294s, migrations9.067s and storage/gc0.004s. Actual storage FAIL6.500s because
both original S3 tests reproduce the unchanged pinned-image pull denial; this
invocation remains status1. All35 recorded exact IDs inspect explicitly not-found,
without manual removal. These are diagnostic subgroup results, not a repaired
combined gate or execution proof for the17 never-started audit tests.

## Ownership and cleanup

Testcontainers v0.32's PostgreSQL and MinIO modules return a **nil container** on
startup errors, even when the underlying generic provider already created one.
The fixtures now register cleanup in `PostCreates`, before startup/readiness,
using only that exact container reference. No shared container/database reuse or
host-wide discovery/deletion is performed.

PostgreSQL closes the current SQL pool before termination and reports both
errors; a close failure does not suppress the termination attempt. Termination
uses a fresh one-minute context, independent of canceled setup. `sql.DB.Close`
does not accept a context, so this is not a hard interrupt guarantee for the
pool-close call. Existing package/outer deadlines remain the final bounds.
PostgreSQL startup and endpoint lookup retain their existing one-minute contexts.
The project migration runner, migration set and readiness strategies are unchanged.

MinIO has a one-minute startup context, a two-minute whole-test operation context
and a fresh one-minute termination context. Endpoint lookup, object body reading,
body close and termination errors are no longer ignored. Image retrieval failure
still **fails** both existing S3 tests; it is not skipped or replaced with a mock.

The real PostgreSQL startup-failure regression deliberately cancels setup after
Docker assigns an ID, exercises the module's nil-container error path, runs the
registered cleanup and verifies **that exact ID** is not found. Deterministic
lifecycle/race tests also check ordering, fresh deadlines and error propagation.
Those helper tests do not prove MinIO retrieval, real S3 behavior or complete
cleanup after process termination.

Never infer successful cleanup or business assertions from a disappeared process,
missing package result, or outer timeout. A create request with a lost response
before `PostCreates` has no known ID; the new hook cannot prove its disposition.
Do not use `docker system prune`, delete unrelated host containers, or retag an
unverified image to make the gate pass.

## Pinned MinIO retrieval remains a release prerequisite

The fixture and Compose image remains
`minio/minio:RELEASE.2025-01-20T14-49-07Z`. A bounded public manifest lookup can
help distinguish retrieval failures:

```bash
timeout --foreground --kill-after=5s 45s \
  docker manifest inspect minio/minio:RELEASE.2025-01-20T14-49-07Z
```

Access denial, authentication failure or no manifest is **not** permission to
switch tags, use `latest`, borrow another registry or skip S3 assertions. Resolve
access and approved image provenance with the deployment owner. Any replacement
requires an explicit reviewed fixture/Compose image decision, verified digest,
compatibility/security review and actual S3/Compose acceptance. Docker/PostgreSQL
success alone is insufficient.
