# aero-id — ROADMAP

> Advisory roadmap from an architect/PM review of `main` (as of 2026-05-25). The MVP is
> feature-complete and merged; this document proposes the next high-value increments. It is
> grounded in a scan of the current code, **not** a wish list — each item cites what exists today.
> No code was changed to produce this.

---

## ✅ Delivery status (updated 2026-05-29)

All five directions below have since been **implemented, tested (unit + testcontainers integration), and verified green** on branch `feat/roadmap`. gRPC transport parity was also completed for every endpoint that had been added HTTP-first.

| # | Direction | Status | Notes |
|---|-----------|--------|-------|
| 1 | Observability & operability | ✅ Done | per-dependency `/readyz`, dependency-free RED `/metrics`, OpenTelemetry tracing through the SSO hop, SSO/cache health gauges |
| 2 | SSO resilience & TLS | ✅ Done | serve-stale fallback, singleflight, circuit breaker, bounded retries+backoff, gRPC keepalive, TLS/mTLS credential path |
| 3 | File pipeline | ✅ Done | size cap + MIME allow-list, content-checksum dedup, streaming S3 uploads, presigned direct-to-S3 upload, GC/reconciliation sweep |
| 4 | Write-path correctness | ✅ Done | atomic reply-count txn, idempotency-key writes, transactional outbox |
| 5 | Product surface | ✅ Done | reactions, unread counts, explicit file sharing, comment reporting/moderation, typed/system messages, outbox-driven notifications + unread feed |

### Post-ROADMAP production hardening (also delivered)

Beyond the five directions, a production-readiness pass closed the remaining operational gaps:

- **Rate limiting** — per-actor (uid) / per-IP token-bucket on both HTTP (429 + Retry-After) and gRPC (ResourceExhausted), opt-in, with idle-bucket eviction.
- **gRPC ops surface** — `grpc.health.v1` health service (flips to NOT_SERVING on graceful shutdown) + config-gated server reflection.
- **Outbox observability** — published/failed counters, pending-count and oldest-event-age (lag) gauges, drain-duration histogram on `/metrics`.
- **Migration tooling** — `cmd/migrate` CLI (`up`/`down N`/`version`) + Makefile targets + opt-in `database.auto_migrate` on startup.

The original advisory text is retained below for provenance.

## Where the codebase stands

The service is cleanly layered (handler → service → repository, with storage + SSO as
side dependencies), well-tested, and deployable. It is solid as an MVP. The gaps below are the
ones that matter for **running it in production**, **not losing data**, and **growing the product** —
in that order.

A recurring theme surfaced during the scan: the data model already *anticipates* features the API
never exposes (`comments.like_count`, `comments.status`, `messages.kind`, `files.visibility`,
`files.checksum`). The original design intent is visible in the schema; much of the roadmap is
about *finishing what was started*.

---

## 1. Production observability & operability  ·  Priority P0

**Why.** This is a dual-transport (HTTP + gRPC) service whose every authenticated request depends on
an external SSO over gRPC — and today it emits **zero metrics and zero traces**. You cannot see
request latency, error rates, SSO call volume, or the token/user cache hit-ratio, which means you
cannot define an SLO, detect a regression, or diagnose an incident. Worse, `/readyz` probes the DB
only (`README` confirms it goes green "once Postgres is up") and **ignores SSO** — so the service can
advertise itself as ready to a load balancer while authentication is completely broken. That is a
false-positive readiness signal: a real, traffic-routing edge case.

**What exists today.** `request_id` + `access_log` middleware (per-request logging) and a DB-only
readiness checker. Nothing aggregated, nothing traced. (`go.opentelemetry.io/otel` is already in the
dependency graph transitively, so the on-ramp is short.)

**Proposed.**
- RED metrics (Rate / Errors / Duration) per HTTP route and gRPC method.
- OpenTelemetry traces spanning the inbound request **through the SSO gRPC hop**, correlated to the
  existing `request_id`.
- SSO health gauges: cache hit-ratio, upstream latency, validate/get-users error rate.
- Deepen `/readyz` into a per-dependency report (DB ping + a *lenient* SSO connectivity check) so a
  degraded dependency is visible without falsely failing the SSO-less dev compose.

**Impact / effort.** High impact, low–medium effort. Everything else in this roadmap needs to be
*measured*; this is the prerequisite.

---

## 2. Harden the SSO dependency (resilience & graceful degradation)  ·  Priority P0

**Why.** SSO is a hard dependency of every `/v1/*` request, yet the client has **no retry, no circuit
breaker, and no stale-cache fallback**. On a cold cache (e.g. right after a deploy) combined with an
SSO slowdown or outage, *every* request fails auth — SSO is effectively a total single point of
failure for aero-id. Separately, the client only wires an `insecure` credential path; a non-insecure
endpoint currently has **no TLS/mTLS transport credentials configured at all**, so secure SSO
connectivity is unimplemented — a production blocker and a security gap.

**What exists today.** `ValidateToken` is cache-first, then a single gRPC call guarded by a timeout;
on error it simply returns the error. `TokenCache` has a TTL ceiling and a background sweeper, but
`Get` returns "miss" the instant an entry expires — no grace window. `grpc.NewClient` uses no
keepalive and no retry service-config.

**Proposed.**
- **Serve-stale-on-failure:** keep a grace window so an expired-but-recent token can be honored when
  SSO is unreachable (availability over strict freshness, tunable).
- Bounded **retries with backoff** inside the per-call deadline budget, plus a **circuit breaker** to
  shed load when SSO is down rather than queueing.
- **singleflight** on validation so a cache-miss stampede collapses into one upstream call per token.
- Wire **gRPC keepalive** and a real **TLS/mTLS** credential path for production.

**Impact / effort.** High impact, medium effort. Converts "SSO blip = aero-id outage" into "SSO blip
= degraded but available."

---

## 3. File pipeline maturity — streaming, limits, validation, dedup, GC  ·  Priority P1

**Why.** This is a *files* service, but the upload path has four concrete problems found in the scan:
1. **Unbounded memory.** The S3 backend does `io.ReadAll` on the upload, buffering the entire object
   in RAM. The code comment even says "service should pre-bound size" — but the service does not.
   Concurrent large uploads are an OOM / memory-amplification DoS waiting to happen.
2. **No input limits.** `Create` validates only that a name and a reader are present — **no max size,
   no MIME allow-list** — which is a storage-cost, abuse, and malware-ingestion vector.
3. **Dedup designed but not built.** `files.checksum` and `idx_files_checksum` exist, but `Create`
   never computes a hash, so identical uploads are stored N times.
4. **Orphaned blobs.** Blob deletes are best-effort and the create path can leave a blob without a
   row; the code defers these "to a future storage GC sweep" that **does not exist** — storage grows
   unbounded.

**Proposed.**
- **Stream** uploads to storage (io.Pipe / multipart) instead of buffering whole objects.
- Enforce a configurable **max size** and **MIME allow-list** at the service boundary.
- Compute a content hash → **content-addressed dedup** (the index is already there).
- Offer **presigned direct-to-S3 upload URLs** so large files bypass the app process entirely.
- A **reconciliation / GC job** that purges soft-deleted rows past a retention window and sweeps
  orphaned blobs.

**Impact / effort.** High impact (correctness, cost, safety on the core asset type), medium–high
effort. The streaming + size-limit slice is small and should be pulled forward as a near-term fix.

---

## 4. Write-path correctness — transactions, idempotency, outbox  ·  Priority P1

**Why.** Three integrity gaps, all confirmed in code:
- **Non-atomic cross-aggregate writes.** `comment.Create` inserts the row and then calls
  `IncReplyCount` *outside a transaction* (there is an explicit `NOTE` to this effect, and no
  `db.Transaction` is used anywhere). Under failure or concurrency, `reply_count` drifts from reality.
- **No idempotency.** No POST endpoint supports an idempotency key, so a client retry or network
  hiccup silently creates duplicate files, messages, and comments.
- **No eventing.** Side effects (reply counts, future notifications, read receipts) are all forced
  into the synchronous request path because there is no async mechanism.

**Proposed.**
- Wrap multi-write operations in `db.Transaction` so counters and rows commit together.
- Add **idempotency-key** support on writes (a dedupe table keyed by `uid` + key + route).
- Introduce a **transactional outbox**: write domain events in the same transaction as the data, and
  drain them asynchronously. This makes counter updates reliable and is the substrate that unlocks
  Direction 5.

**Impact / effort.** High architectural leverage, medium effort. Sequence this **before** the
notification work below.

---

## 5. Complete the modeled-but-undelivered product surface  ·  Priority P2

**Why.** The schema reserves capability the API never exposes — these are the most obvious, lowest-
risk user-facing increments because the data model already anticipates them:
- `comments.like_count` → **reactions** (like/unlike) — column exists, no endpoint.
- `comments.status` → **moderation / reporting** lifecycle — column exists, no endpoint.
- `messages.kind` → **typed/system messages** (only the default kind is used today).
- `files.visibility` exists as binary public/private, but there is **no per-user sharing** (grant read
  to specific uids).
- Plus the natural complement once the outbox (Direction 4) lands: **notifications & unread feeds**
  ("you have a new message", "someone replied to your comment").

**Proposed (suggested sequence).**
1. **Notifications + unread counts**, driven by the outbox — highest user value, reuses existing
   `messages.read_at` / unread index.
2. **Comment reactions** — activate `like_count`.
3. **Comment moderation / report** — drive the `status` lifecycle; pairs well with abuse controls.
4. **Explicit file sharing** — beyond binary visibility, grant read to named uids.

**Impact / effort.** Direct user value; low–medium per feature *because the model already supports it*.
Do this last — it rests on the observability, resilience, and outbox foundations above.

---

## Suggested sequencing

| # | Direction | Type | Priority | Effort | Rationale to do (or defer) |
|---|-----------|------|----------|--------|----------------------------|
| 1 | Observability & operability | Perf / Ops | **P0** | Low–Med | Can't operate or measure anything else without it. |
| 2 | SSO resilience & TLS | Reliability / Security | **P0** | Med | Removes the auth single-point-of-failure. |
| 3 | File pipeline (stream/limits/dedup/GC) | Perf / Edge / Cost | **P1** | Med–High | Pull the size-cap + streaming slice forward; rest follows. |
| 4 | Write-path correctness (txn/idempotency/outbox) | Edge / Foundation | **P1** | Med | Fixes data drift; substrate for #5. |
| 5 | Product surface (notifications/reactions/moderation/sharing) | Features | **P2** | Low–Med each | Highest user value, but rests on 1–4. |

## Cheap near-term fixes worth doing immediately

These are small, high-value, and don't require committing to a full direction:
- Add an **SSO check to `/readyz`** (lenient) so readiness stops lying (part of #1).
- Enforce an **upload max-size** and reject oversized bodies early (part of #3).
- Wrap **`comment.Create` + `IncReplyCount` in a transaction** (part of #4).
