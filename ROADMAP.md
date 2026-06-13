# aero-id — ROADMAP

> Advisory roadmap from an architect/PM review of `main` (as of 2026-05-25). The MVP is
> feature-complete and merged; this document proposes the next high-value increments. It is
> grounded in a scan of the current code, **not** a wish list — each item cites what exists today.
> No code was changed to produce this.

---

## Phase 0 Discovery — 2026-06-13 (Autonomous Software Factory)

> Full-codebase scan after delivery of all original roadmap directions. No code written.
> Branch `feat/roadmap` is 33 commits ahead of `main` (unmerged — prerequisite action required).

### 当前状态

All five original ROADMAP directions and the post-ROADMAP production hardening wave are complete:
- 38 gRPC RPCs across 5 services (full HTTP↔gRPC parity)
- 12 migrations, reversible, tested up/down
- Unit tests: 27 packages / 0 failures; Integration tests (testcontainers): 21 packages / 0 failures
- Rate limiting, OTel tracing, outbox relay, storage GC, circuit breaker, migration CLI — all shipped

Schema↔API gap: **none remaining.** Every DB column that was designed-but-unexposed now has an endpoint.

**Prerequisite:** merge/PR `feat/roadmap` → `main` before starting new iteration.

---

### 推荐方向

#### P0 — 生产就绪补全

| # | 项目 | 描述 | 估算 |
|---|------|------|------|
| P0-1 | **HTTP handler timeouts** | `ReadTimeout`/`WriteTimeout` 设在 server 级别，但 handler 内无 `context` deadline。大文件上传 / 慢查询可能占满整个窗口。加 per-route `http.TimeoutHandler` 或中间件传 ctx deadline。 | 低 |
| P0-2 | **Middleware 单元测试** | `auth.go`、`ratelimit.go`、`metrics.go`、`recovery.go`、`cors.go`、`request_id.go` 完全无测试。这些是所有请求必经的横切关注点，当前只靠集成测试兜底。 | 低-中 |
| P0-3 | **gRPC handler 单元测试** | `comment.go`、`message.go`、`file.go`、`notification.go` gRPC handler 无独立测试；`parity_test.go` 仅做高层覆盖，序列化 / 错误码映射未验证。 | 中 |
| P0-4 | **Outbox relay 优雅关闭** | relay 使用 ticker 轮询，shutdown 时不主动 drain 队列。快速重启可能留下未发布事件直到下次轮询。需在 stop func 触发时做最后一次 drain。 | 低 |

#### P1 — 高价值功能扩展

| # | 项目 | 描述 | 估算 |
|---|------|------|------|
| P1-1 | **WebSocket / SSE 实时推送** | 通知和消息当前依赖轮询。加 WebSocket 或 SSE 端点，在 outbox relay 分发事件时同步推送给在线连接。这是 UX 最大提升点。 | 高 |
| P1-2 | **统一 gRPC 请求校验器** | 每个 handler 独立调用 `idgen.Parse`，错误码映射散落各处。引入中间件级别 validator，统一 ID/enum 校验 → 减少重复，保证一致的 `InvalidArgument` 响应。 | 低-中 |
| P1-3 | **消息已读回执通知** | `messages.read_at` 列已存在并在消息被读取时设置，但发送方不会收到通知。通过 outbox 在 `mark_read` 时发送 `message.read` 事件 → 通知发送方。 | 低 |
| P1-4 | **消息 Reactions** | `comment_likes` 完整实现了点赞。`messages` 没有对应机制。增加 `message_reactions` 表 + 服务层 + HTTP/gRPC 端点。 | 中 |

#### P2 — 长期演进

| # | 项目 | 描述 | 估算 |
|---|------|------|------|
| P2-1 | **全文搜索** | 消息/文件/评论无搜索端点。可用 PostgreSQL `tsvector` + `GIN` 索引实现，或集成外部搜索引擎。 | 高 |
| P2-2 | **审计日志** | 文件共享授权/撤销、评论举报处理等敏感操作无审计记录。增加 `audit_events` 表 + 写入钩子。 | 中 |
| P2-3 | **负载测试套件** | 无 k6 / wrk 脚本。需要在上线前验证吞吐量和 P99 延迟。 | 中 |
| P2-4 | **SSO 证书固定** | TLS/mTLS 路径已实现，但无 cert-pinning 选项。生产强化需要。 | 低 |

---

### 技术债

| 级别 | 项目 |
|------|------|
| High | 43% 的 internal/ 源文件无单元测试（domain、middleware、gRPC handler 层） |
| High | outbox relay 无优雅 drain（P0-4） |
| Medium | Background context 在 sweeper/SSO/relay 中使用，限制 trace 传播 |
| Medium | gRPC handler 错误码映射无统一层（P1-2） |
| Low | 无负载测试脚本（P2-3） |
| Low | 无审计日志（P2-2） |

---

### Edge Cases

- 并发点赞：`comment_likes` 有唯一约束，幂等。`message_reactions` 如需实现同理。
- 快速重启期间 outbox 事件丢失（当前已知缺陷，P0-4 修复）
- 大文件上传 + 慢网络 → handler timeout 失控（P0-1 修复）
- block 创建无限速 → 用户可刷大量 block 记录（rate limit 已全局启用，但 block API 无专属限额）

---

### 性能优化机会

- notification 列表查询：当前按 `created_at DESC` cursor 扫描；加 `(recipient_uid, read_at IS NULL, created_at DESC)` 复合索引可加速未读过滤
- outbox `FetchUnpublished`：当前全表扫描未发布事件；`(published_at IS NULL, created_at ASC)` 部分索引可降低轮询成本
- SSO cache 冷启动：无预热机制，第一批请求全部穿透到 SSO；可在 app 启动后异步预热常用 token

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

And a content-lifecycle + safety wave:

- **Editing** — author-only edit of comments and messages (`edited_at`), with validation and soft-delete safety.
- **Moderation queue** — triage/resolve over the `comment_reports` collected earlier (author-scoped; no role system).
- **Message lifecycle** — sender soft-delete + per-conversation mark-all-read.
- **File metadata update** — owner rename / visibility / meta change (PATCH) without re-upload.
- **User blocking** — directional blocks suppress inbound messages and notifications.
- **Full gRPC parity** — every data-plane HTTP route now has a matching gRPC RPC (38 RPCs across 5 services); only infra endpoints (`/healthz`, `/readyz`, `/metrics`, signed `/files/dl`) remain HTTP-only, with gRPC liveness covered by `grpc.health.v1`.

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
