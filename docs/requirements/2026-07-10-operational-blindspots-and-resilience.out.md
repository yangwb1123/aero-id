Duplicate removed and code reference corrected. Here's a summary:

**File**: `docs/requirements/2026-07-10-operational-blindspots-and-resilience.md` (539 lines)

**Corrected**: Line 125 — replaced `Update("content", newContent)` with the actual call signature `messages.UpdateContent(ctx, id, content, now)` which matches `internal/service/message.go:268`.

I verified all key code-level claims against the actual source:

| Claim | Verification |
|-------|-------------|
| 6 background goroutines, no health/restart | ✅ `internal/outbox/relay.go:161–176`, `internal/storage/gc/sweeper.go:251`, `internal/pkg/ratelimit/ratelimit.go:104–120`, `internal/sso/token_cache.go:101`, `internal/idempotency/store.go:126` — all use `Start/RunCleaner → stop func` pattern with no health reporting |
| Message `Edit()` overwrites content | ✅ `internal/service/message.go:268` — `s.messages.UpdateContent(ctx, id, content, now)`, no history saved |
| Comment `Edit()` overwrites content | ✅ `internal/service/comment.go:213` — `s.comments.UpdateContent(ctx, id, newContent, now)`, no history saved |
| Global rate limiter, single instance | ✅ `internal/app/app.go` creates one `limiter`; `internal/middleware/ratelimit.go:55–57` keys only on `uid:` or `ip:` |
| No DB circuit breaker | ✅ `internal/pkg/db/db.go` configures pool params only; no breaker, no degradation logic |
| SSO client no user status propagation | ✅ `internal/sso/client.go` returns only validation result; no `user_status` field in proto |

The analysis is solid — all 5 directions represent genuine, code-evidenced gaps with no systematic coverage in prior documentation.
