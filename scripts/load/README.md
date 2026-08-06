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

Runs the five scenarios in `scripts/load/ghz/scenarios.yaml` (GetMe,
GetMeOverview, ListSources, legacy ListFiles, audit ListEvents) at 100/50/50/20/10
concurrency for 3 minutes each. Results land in `scripts/load/results/`.

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
make load-test-db PG_DSN_BENCH=postgres://...  # or: psql "$PG_DSN" -f scripts/load/db/partition-index-bench.sql
```

`EXPLAIN (ANALYZE, BUFFERS)` over the hot paths: audit ledger timeline /
operation DAG / partition chain, notification unread feed + count, outbox
drain (`FOR UPDATE SKIP LOCKED`), callback publication due queue, message
conversation and file keyset pages, projection freshness, plus a
`pg_stat_user_indexes` health summary. Override query parameters with
`psql -v ACCOUNT_ID=... -v CURSOR=...`.

## What to watch

- `aggregation_latency_ms`: overview must not degrade when a source is down
  (partial responses are 200 with `partial:true` + `source_errors`)
- per-source connector circuit state, projection lag, audit ingest failures,
  callback retry/DLQ depth (metrics on `/metrics`)
- DB pool saturation: watch `max_open_conns` vs `conns_in_use` under load
