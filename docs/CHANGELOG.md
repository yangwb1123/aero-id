# aero-id — CHANGELOG

## [Unreleased] — feat/roadmap (2026-05-25 → 2026-06-13)

All items on branch `feat/roadmap`, 33 commits ahead of `main`.

### Added

**Direction 1 — Observability & Operability**
- RED metrics (HTTP routes + gRPC methods) via Prometheus registry
- OpenTelemetry tracing through SSO gRPC hop; W3C TraceContext propagation
- SSO health gauges (cache hit-ratio, upstream latency, error rate)
- Extended `/readyz` with per-dependency checks (DB + lenient SSO probe)
- `internal/pkg/tracing`: OTLP/gRPC TracerProvider, opt-in

**Direction 2 — SSO Resilience & TLS**
- `TokenCache` stale grace window: serve expired-but-recent tokens when SSO is down
- `singleflight` stampede collapse on cache-miss validation
- Circuit breaker (closed/open/half-open) with injectable clock for testing
- Bounded retries + exponential backoff on availability errors
- gRPC keepalive params; TLS/mTLS credential path (`TLSConfig`)
- `isAvailabilityError`: classifies Unavailable/DeadlineExceeded as retryable

**Direction 3 — File Pipeline**
- Max upload size enforcement (`MaxUploadBytes`) + MIME allow-list
- Content-checksum dedup (`files.checksum`, `idx_files_checksum`)
- Streaming S3 uploads (io.Pipe / multipart — no full-body buffer)
- Presigned direct-to-S3 upload URLs (`PresignUpload` / `CompleteUpload`)
- Storage GC sweeper: purge soft-deleted rows past retention + orphan blob sweep
- `cmd/migrate` CLI (`up`/`down N`/`version`) + `database.auto_migrate` startup option

**Direction 4 — Write-path Correctness**
- `comment.Create` + `IncReplyCount` wrapped in DB transaction
- Idempotency-key support on writes (`idempotency_keys` table, GormStore)
- Transactional outbox (`outbox_events` table, Relay, Handler interface)

**Direction 5 — Product Surface**
- Comment reactions: like/unlike (`comment_likes`, `like_count`)
- Comment moderation: report queue, hide/unhide, resolve report
- Typed/system messages (`messages.kind`)
- Explicit file sharing: grant/revoke per-uid read access (`file_shares`)
- Outbox-driven notifications + unread feed (`notifications` table)
- Unread message count endpoint

**Post-ROADMAP Hardening**
- Per-actor rate limiting (token-bucket, both HTTP + gRPC), with idle-bucket eviction
- `grpc.health.v1` health service (flips NOT_SERVING on shutdown)
- Config-gated gRPC server reflection
- Outbox observability metrics: published/failed counters, lag gauge, drain histogram
- Migration tooling: `cmd/migrate`, Makefile targets, `database.auto_migrate`

**Content Lifecycle Wave**
- Author-only comment and message editing (`edited_at`)
- Message soft-delete + conversation mark-all-read
- File metadata PATCH (rename / visibility / meta without re-upload)
- User blocking: directional, suppresses inbound messages + notifications

**Full gRPC Parity**
- 38 RPCs across 5 services (FileService, MessageService, CommentService, NotificationService, BlockService)
- Only infra endpoints remain HTTP-only (`/healthz`, `/readyz`, `/metrics`, signed `/files/dl`)

### Migrations Added
- 000006: file_shares
- 000007: comment_reports
- 000008: outbox_events
- 000009: notifications
- 000010: comments.edited_at + comment_reports status columns
- 000011: messages.edited_at
- 000012: user_blocks
