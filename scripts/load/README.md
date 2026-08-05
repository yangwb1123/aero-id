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

## What to watch

- `aggregation_latency_ms`: overview must not degrade when a source is down
  (partial responses are 200 with `partial:true` + `source_errors`)
- per-source connector circuit state, projection lag, audit ingest failures,
  callback retry/DLQ depth (metrics on `/metrics`)
- DB pool saturation: watch `max_open_conns` vs `conns_in_use` under load
