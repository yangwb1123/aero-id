# Load tests (P6-08)

Release-gate load scenarios for the aero-id control plane and legacy compat
surface. They are **not** run by `make test`; run them against a deployed
regional cell (or a local stack with seeded data).

## Prerequisites

- [`k6`](https://grafana.com/docs/k6/latest/set-up/install-k6/) for HTTP load
- [`ghz`](https://ghz.sh/docs/install) for gRPC load
- A running aero-id (local: `make run`, or a deployed cell) and a valid
  Snaplink bearer token for the load account

## HTTP (k6)

```bash
make load-test-http BASE_URL=http://localhost:8080 TOKEN=<token> VUS=50 DURATION=3m
```

Scenario mix (60/40 control-plane/legacy):

- `GET /v1/me`, `/v1/me/overview` (aggregation latency tracked separately),
  `/v1/me/sources`, `/v1/me/memberships`, `/v1/account-datasets`,
  `/v1/me/data?consistency=eventual` — all local-projection reads, no WAN fan-out
- `GET /v1/files`, `/v1/messages`, `/v1/comments` — legacy compat surface,
  asserting the `Deprecation: true` header (ADR-011)

Thresholds: `p95 < 250ms`, `p99 < 1000ms` globally; `p95 < 200ms` for the
control plane scenario. Calibrate to the SLO table in
`docs/plans/2026-08-04-distributed-account-aggregation.md` §5 (regional
snapshot read p95 <= 100ms, p99 <= 250ms).

## gRPC (ghz)

```bash
make load-test-grpc TOKEN=<token>
```

`scripts/load/ghz/scenarios.yaml` is the **single source of truth** for ghz
scenarios; `scripts/load/run-ghz.sh` is a pure executor that parses it — there
are no inline scenario arrays in the runner. Each entry defines `name`, `call`
(`aeropb.<Service>.<Method>`), `data`, `concurrency`, `duration`, and the
`p95`/`p99` SLO latency targets. Current scenarios: `account-getme`,
`aggregation-overview`, `account-list-sources`, `legacy-file-list`,
`audit-list-facts` (renamed from the historical `audit-list-events`; the call
is `aeropb.AuditLedgerService.ListAuditFacts`) at 100/50/50/20/10 concurrency
for 3 minutes each. Results land in `scripts/load/results/ghz-<name>.json`.

SLO enforcement is fail-closed: after each campaign the runner parses the ghz
JSON (`latencyDistribution` percentiles) and prints `SLO OK <name> ...` or
`SLO FAIL <name> p95=<actual>ms > target=<target>ms`; a breach (or
missing/unparsable result data — never treated as a pass) makes
`make load-test-grpc` exit non-zero after all scenarios have reported.
Override the YAML targets for **all** scenarios per run (CI/regression
against slower environments) without editing the file:

```bash
GZ_SLO_P95_MS=150 GZ_SLO_P99_MS=400 make load-test-grpc TOKEN=<token>
```

## Event ingest soak (k6)

```bash
make load-test-event TOKEN=<token> SOURCE=aero-im SOURCE_KEY=<hmac-key> BATCH=10 VUS=8 DURATION=5m
```

Drives `POST /internal/v1/events` with per-source HMAC-signed canonical event
batches (`scripts/load/k6/event-soak.js`): fresh batches must all apply, and a
redelivery VU re-posts real event IDs asserting stable `duplicate` receipts
(at-least-once without double-apply). The load account needs the
`source_event_ingest` permission and an event source binding for `SOURCE`
(config `event_ingest.bindings`); `SOURCE_KEY` must match
`event_ingest.source_keys[SOURCE]`.

## Callback soak (k6 + sink)

```bash
go run ./scripts/load/sink -addr :9091 &
make load-test-callback TOKEN=<token> RECEIVER_URL=http://127.0.0.1:9091 VUS=4 DURATION=5m
```

`scripts/load/k6/callback-soak.js` registers subscriptions against the sink
(challenge verification must succeed), triggers audited profile updates, and
asserts every triggered fact produces a signed delivery. When
`callback.allow_private_target` is false, `RECEIVER_URL` must resolve to a
public address from the cell. The load account needs the `callback.manage`
permission. The sink (`scripts/load/sink/main.go`) answers the verification
and delivery handshakes and exposes `/stats` + `/reset`; it is a load-test
tool and must never ship in a production cell.

## Database partition/index benchmark

```bash
make load-test-db-seed PG_DSN_BENCH=<bench-dsn>   # once per bench cell
make load-test-db PG_DSN_BENCH=<bench-dsn>        # run the benchmark
```

`EXPLAIN (ANALYZE, BUFFERS)` over the hot paths: audit ledger timeline /
operation DAG / partition chain, notification unread feed + count, outbox
drain (`FOR UPDATE SKIP LOCKED`), callback publication due queue, message
conversation and file keyset pages, projection freshness, plus a
`pg_stat_user_indexes` health summary. Override query parameters with
`psql -v ACCOUNT_ID=... -v CURSOR=...`.

`PG_DSN_BENCH` must be set explicitly — there is **no fallback to the app DSN
(`PG_DSN`)**: running EXPLAIN ANALYZE against the live app database is the
hazard this guard chain exists to prevent. Guards (all documented in the
Makefile):

1. explicit DSN — `make load-test-db` without `PG_DSN_BENCH` errors;
2. app-DSN refusal when `PG_DSN_BENCH` equals `PG_DSN`, unless the dangerous
   `BENCH_ALLOW_APP_DSN=1` override is set (deliberate operator action only;
   string equality is imperfect, which is why guard 3 is the substantive
   protection);
3. seed-sanity probe on the `aero_load_bench_seed` marker, unless
   `BENCH_SKIP_SEED_CHECK=1` (explicit escape hatch). The marker is created
   **only** by `make load-test-db-seed`, so the app DB never contains it — an
   accidental app-DSN run is refused by this guard even if guard 2 is
   bypassed. The marker records a schema version and per-table row counts;
   a stale seed (version < 1) produces a warning, not a failure.

`make load-test-db-seed` seeds the bench cell idempotently (re-runnable:
truncate + deterministic re-insert) at the volumes documented in
`scripts/load/db/seed-bench.sql` (audit_ledger ≥ 1M rows plus realistic rows
for notifications, outbox_events, audit_publications, messages, files,
account_snapshots, and FK-support accounts). Volumes are per-cell adjustable
via psql variables, e.g. `psql -v AUDIT_LEDGER_ROWS=2000000 -f
scripts/load/db/seed-bench.sql`. Never point either target at production.

## Gates

`make check-load-scenarios` runs in CI (`check-gates` job) and in the
done-gate chain. It is pure bash + grep (no Go, Docker, network, or new
binaries) and self-tests its fixtures on every invocation. It guards:

- **R1** every `call:` in `scenarios.yaml` resolves in the proto (dead/renamed
  RPCs redden);
- **R2** `run-ghz.sh` references the canonical YAML and carries no inline
  call literals; every scenario has all 7 fields with the documented formats;
- **R3** every user relation in `partition-index-bench.sql` exists in
  `migrations/*.up.sql` (system relations require an allowlist entry in the
  script header);
- **R4** the Makefile-referenced load assets (`k6/*.js`, `sink/main.go`) exist
  and are non-empty. R4's path list is hardcoded in the gate by design — a
  future Makefile reference change must update `scripts/check-load-scenarios.sh`
  too.

See the script header of `scripts/check-load-scenarios.sh` for the canonical
rule definitions and fixture map.

## What to watch

- `aggregation_latency_ms`: overview must not degrade when a source is down
  (partial responses are 200 with `partial:true` + `source_errors`)
- per-source connector circuit state, projection lag, audit ingest failures,
  callback retry/DLQ depth (metrics on `/metrics`)
- DB pool saturation: watch `max_open_conns` vs `conns_in_use` under load
