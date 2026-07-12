Now I have a thorough understanding. Let me compile my complete Staff Engineer review.

---

# Staff Engineer Code Review: aero-id — Subsystem Implementation

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Within limits (lint passes) | < 10 | ✅ |
| Function length | Within limits (lint passes) | < 50 lines | ✅ |
| File size | All ≤ 500 lines | < 500 lines | ✅ |
| Test coverage | Good, but gaps in probe transitions | > 80% | ⚠️ |
| Code duplication | 3 near-identical cleaner runners | < 5% | ⚠️ |
| Documentation coverage | Well documented | > 70% | ✅ |

---

## Finding Register

### Finding 1 — Documentation/Code Mismatch: gRPC Interceptor Chain Order

| Field | Value |
|-------|-------|
| Category | **Naming & Documentation** |
| Severity | **Medium** |
| Title | gRPC interceptor chain ordering comment is inaccurate |
| Location | `internal/handler/grpc/server.go:118-120` |
| Description | The comment states *"Degradation interceptor runs AFTER metrics/tracing and BEFORE recovery"*, but the actual chain order is: Tracing → Metrics → **Recovery** → Auth → RateLimit → Permission → **Degradation**. Degradation runs AFTER recovery, auth, rate limiting, and permission checking, not before. |
| Current State | `if o.degradation != nil { interceptors = append(interceptors, DegradationInterceptor(o.degradation)) }` — Degradation is appended last, making it the innermost interceptor. The comment contradicts reality. |
| Recommended State | Fix the comment to accurately read: *"Degradation interceptor runs as the innermost interceptor, closest to the handler, so it always sets the trailer before the handler executes. If an upstream interceptor (auth, rate limit) rejects the request, the trailer is not set — which is acceptable since auth failures don't need degradation metadata."* |
| Code Example | The comment at line 118 should be corrected to match the actual ordering. |
| Impact | **Low (documentation)** — Misleading comment may confuse future developers extending the interceptor chain. The actual behavior is correct; only the documentation is wrong. |
| Effort | **S** |

---

### Finding 2 — Degradation Contract Partially Implemented

| Field | Value |
|-------|-------|
| Category | **Technical Debt** |
| Severity | **High** |
| Title | Degradation-aware handler adaptation only wired in 2 of 10 handler domains |
| Location | `internal/handler/http/file.go`, `internal/handler/http/message.go` vs. `internal/handler/http/comment.go`, `internal/handler/http/notification.go`, `internal/handler/http/block.go`, `internal/handler/http/preference.go`, `internal/handler/http/search.go`, `internal/handler/grpc/*` |
| Description | `AbortIfDegraded` is only called in file and message HTTP handlers (8 call sites). The other 6 handler domains (comment, notification, block, preference, search, admin) and all gRPC handlers do **not** call `AbortIfDegraded`. The `X-Aero-Degraded` header is set on all responses (via middleware/interceptor), but handlers don't adapt behavior — they'll return a generic error when DB/SSO fails rather than a properly formatted 503 with degradation semantics. The DEGRADATION_MATRIX.md correctly documents this as 🟡 (partial) for 24 of 44 endpoint rows. |
| Current State | Header set on every response, but `AbortIfDegraded` only wired in file+message handlers. |
| Recommended State | Wire `AbortIfDegraded(c, DepsForOp(DepDB), readOnly)` at the top of every write endpoint across all handler domains. For read endpoints, implement stale-cache fallback paths. |
| Impact | **High (reliability)** — During actual degradation scenarios (DB outage, SSO failure), most endpoints won't return properly structured 503s or stale data. Clients relying on `X-Aero-Degraded` will see the header but get unexpected errors (500s, timeouts) instead of graceful degradation. |
| Effort | **L** (spans all handler domains, needs stale cache implementations) |

---

### Finding 3 — Non-Deterministic Test with No Assertions

| Field | Value |
|-------|-------|
| Category | **Testing** |
| Severity | **Medium** |
| Title | TestDegradationState_ProbeTransitions makes no state assertions |
| Location | `internal/middleware/degradation_test.go:85-103` |
| Description | The probe transition test uses `time.Sleep(150ms)` to wait for probe cycles, then only calls `_ = s.Status()` to "ensure no crash". It makes **zero assertions** about whether the state machine actually transitioned from healthy → degraded → healthy. The test will pass even if the prober is never called, the threshold is never crossed, or the recovery never happens. |
| Current State | A 150ms sleep with a 10ms probe interval (15 cycles) and a 2-failure / 2-success threshold. The attempt counter is shared across probes, making sequential analysis fragile. No assertions on actual state. |
| Recommended State | Use a poll-based assertion loop with timeout to verify state transitions: wait for `s.IsDegraded(DepDB) == true`, then continue probing until `s.IsDegraded(DepDB) == false`. Use `require.Eventually` if available, or a manual polling loop. |
| Impact | **Medium (testing rigor)** — This test creates a false sense of security. A regression in the probe state machine would not be caught. |
| Effort | **S** |

---

### Finding 4 — Cleaner Pattern Duplication

| Field | Value |
|-------|-------|
| Category | **Code Quality** |
| Severity | **Medium** |
| Title | Three near-identical background cleaner implementations |
| Location | `internal/repository/audit.go` (`RunAuditCleaner`), `internal/repository/notification.go` (`RunNotificationCleaner`), `internal/outbox/repository.go` (`RunOutboxCleaner`) |
| Description | Each cleaner follows the same ~30-line pattern: ticker loop, `context.Background()`, select on ticker+done, compute cutoff, call Purge method, log result/error, return stop function. The only differences are: which repository method to call, how many cutoffs to compute, and what to log. This is copy-paste with minor edits. |
| Current State | Three separate implementations in three files with ~90% structural code duplication. |
| Recommended State | Extract a shared `RunCleaner` helper in a common package (e.g., `internal/pkg/cleaner`): ```go type PurgeFunc func(ctx context.Context) (int64, error) func Run(name string, interval time.Duration, purge PurgeFunc, log *zap.Logger) func() ``` Or use the existing `periodic` helper pattern. Each cleaner file becomes a 5-line adapter. |
| Impact | **Medium (maintainability)** — Any bug fix or improvement to the cleaner pattern (e.g., context-aware sleep, metrics reporting) must be replicated in 3 places. Increases risk of missed updates. |
| Effort | **M** |

---

### Finding 5 — Graceful Shutdown Ordering Risk

| Field | Value |
|-------|-------|
| Category | **Code Quality** |
| Severity | **Medium** |
| Title | Background components stopped before HTTP/gRPC servers drain in-flight requests |
| Location | `internal/app/app.go:shutdown()` lines 122-155 |
| Description | The shutdown sequence stops the relay, GC, cleaners, and degradation prober **before** HTTP server shutdown and gRPC GracefulStop. In-flight HTTP requests being served during the shutdown window may find their dependencies (degradation state, outbox, audit logging) already terminated. Also, HTTP.Shutdown has a configurable timeout; gRPC.GracefulStop is called unconditionally after it, meaning if HTTP drain takes the full timeout, gRPC has zero time to drain. |
| Current State | ```go func (a *App) shutdown() { a.grpcHealth.SetServing(false) // stop background components... // then HTTP.Shutdown // then gRPC.GracefulStop // then traceShutdown } ``` |
| Recommended State | Reorder: (1) Set gRPC health NOT_SERVING + stop HTTP listener (with Shutdown), (2) Stop gRPC listener (GracefulStop), (3) Stop background components (relay, cleaners, etc.) only after both servers have drained. Alternatively, start background stoppage in parallel with a short delay after signalling servers to drain. |
| Impact | **Medium (reliability during rollouts)** — During rolling deployments, active requests may fail unexpectedly because background components (audit logger, outbox relay) are stopped while the server still accepts requests (until Shutdown/GracefulStop completes). |
| Effort | **M** |

---

### Finding 6 — DynamicSampler Recovery Race Condition

| Field | Value |
|-------|-------|
| Category | **Code Quality** |
| Severity | **Low** |
| Title | Recovery window and ratio-increment ticker can race with error-rate spike |
| Location | `internal/pkg/tracing/tracing.go` — `startMonitor` method |
| Description | The recovery mechanism uses two independent tickers: a 10s window ticker and a 2-minute recovery ticker. When `currentlyDegraded` flips to false (5 consecutive healthy windows), the recovery ticker can increment the ratio even if the error rate spikes in between recovery ticks. The next 10s window check catches this and re-degrades, but during the up-to-10-second gap, the trace volume has been unnecessarily increased. |
| Current State | Two tickers operate independently. The ratio can be increased by the recovery ticker moments before a new error window detects a spike. |
| Recommended State | Combine the logic: only increment the ratio in the window ticker after confirming the current window is healthy AND `currentlyDegraded` is false. Remove the separate recovery ticker and instead implement a "count consecutive healthy windows since last degraded" approach that increments by `ratioStep` per window after `recoveryWindows` healthy windows. |
| Impact | **Low** — Brief over-sampling (10s at most) during transient collector issues is acceptable. The rate-limiter on the collector side provides additional protection. |
| Effort | **S** |

---

### Finding 7 — `time.Sleep` in Batch Deletes is Non-Interruptible

| Field | Value |
|-------|-------|
| Category | **Error Handling** |
| Severity | **Low** |
| Title | Purge methods use `time.Sleep` which doesn't respect context cancellation |
| Location | `internal/repository/audit.go:113` (`time.Sleep(purgeInterval)`), `internal/repository/notification.go:232` (`time.Sleep(notificationPurgeInterval)`), `internal/outbox/repository.go:171` (`time.Sleep(outboxPurgeInterval)`) |
| Description | All three `PurgeOlderThan`/`PurgeDeadLetters` methods use `time.Sleep(100ms)` between batches. If the context is cancelled during a multi-batch purge, the current sleep is not interruptible. Given 100ms per batch and at most a few hundred batches, the maximum delay is < 30s, but on a server shutdown this could delay graceful termination. |
| Current State | `time.Sleep(purgeInterval)` between each batch. No select on ctx.Done(). |
| Recommended State | Replace with a context-aware sleep: ```go select { case <-time.After(purgeInterval): case <-ctx.Done(): return total, ctx.Err() } ``` |
| Impact | **Low** — 100ms per batch is short; unlikely to cause real-world issues. But adding context awareness is a 2-line change that improves correctness. |
| Effort | **S** |

---

### Finding 8 — Type Assertion Panic Risk in AccessLog

| Field | Value |
|-------|-------|
| Category | **Error Handling** |
| Severity | **Low** |
| Title | Unsafe type assertion on `sampled` context value |
| Location | `internal/middleware/access_log.go:25` |
| Description | `sampled.(bool)` is an unchecked type assertion. If another middleware sets `"sampled"` to a non-bool value, this will panic. While no current code path sets it to a non-bool, future middleware additions could. |
| Current State | `sampled, ok := c.Get("sampled"); if ok && !sampled.(bool) { return }` |
| Recommended State | Use a safe type assertion: `sampledBool, ok := sampled.(bool); if ok && !sampledBool { return }` |
| Impact | **Low** — Low probability, but a panic in access log middleware would crash the entire HTTP handler. |
| Effort | **S** |

---

### Finding 9 — Config Validation: gRPC Message Size Minimum Too Low

| Field | Value |
|-------|-------|
| Category | **Error Handling** |
| Severity | **Low** |
| Title | Minimum gRPC message size set to 1KB which is impractical |
| Location | `internal/config/config.go:236-238` |
| Description | The validation checks `MaxRecvMsgSize >= 1024` bytes. A value of exactly 1024 bytes would pass validation but is too small for real usage (file metadata requests, paginated lists, etc.). The gRPC default is 4MB. |
| Current State | `if c.Server.GRPC.MaxRecvMsgSize > 0 && c.Server.GRPC.MaxRecvMsgSize < 1024 { return error }` |
| Recommended State | Increase the minimum to a practical value: `4096` (4KB) or `65536` (64KB). Or document that values below 64KB will cause unexpected failures on large payloads. |
| Impact | **Low** — An operator would need to intentionally set this very low. Would fail at runtime quickly. |
| Effort | **S** |

---

### Finding 10 — Structured Logging: Error Context Strings Not Standardized

| Field | Value |
|-------|-------|
| Category | **Logging** |
| Severity | **Low** |
| Title | Inconsistent verb tense in error-wrapping context strings across repository methods |
| Location | All repository files (`internal/repository/*.go`, `internal/outbox/repository.go`) |
| Description | Some repository methods wrap errors with present tense ("create notification: %w", "append audit event: %w") while others use past tense ("purged audit events: %w" — wait, actually checking again...). Let me verify: `"append audit event: %w"`, `"create notification: %w"`, `"mark notification read: %w"`, `"increment collapsed count: %w"`, `"purge audit events: %w"`, `"purge read notifications: %w"`. OK, most are present tense, but some use different prepositional styles. More importantly, the error messages lack structured fields — they're flat strings that are hard to filter/query in log aggregation systems. |
| Current State | `return fmt.Errorf("purge audit events: %w", res.Error)` — flat strings |
| Recommended State | Standardize on `operation: %w` format and consider adding structured error types that carry operation name and metadata. |
| Impact | **Low** — Debugging is still possible, just slightly harder in aggregated logs. |
| Effort | **S** |

---

### Finding 11 — Metrics Output Silently Drops Partial Writes

| Field | Value |
|-------|-------|
| Category | **Error Handling** |
| Severity | **Low** |
| Title | Metrics WriteText ignores fmt.Fprintf return values |
| Location | `internal/pkg/metrics/metrics.go` — multiple lines in `WriteText`, `writeSSOText`, `writeOutboxText` |
| Description | Every `fmt.Fprintf(w, ...)` and `fmt.Fprintln(w, ...)` call's return value (bytes written + error) is discarded. If the underlying writer hits an error (e.g., connection reset by scrapers), the partial write is silently dropped and the /metrics endpoint reports success to the caller. |
| Current State | All fmt.Fprint calls ignore return values. |
| Recommended State | Aggregate the first write error and return it from `WriteText(w io.Writer) error`, let the HTTP handler check it: ```go if err != nil { http.Error(w, "metrics write error", 500); return } ``` |
| Impact | **Low** — Prometheus scrapers handle partial responses gracefully, so this is self-healing on the consumer side. |
| Effort | **M** (touches many lines, but mechanical) |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| Degradation adapter not wired for 6/8 handler domains | High | L | **P0** | Correctness during outages |
| Cleaner pattern duplication (3 copies) | Medium | M | **P1** | Adds maintenance burden |
| Graceful shutdown ordering | Medium | M | **P1** | Reliability during deployments |
| Non-deterministic probe test with no assertions | Medium | S | **P1** | Gaps in test coverage |
| gRPC interceptor chain comment mismatch | Low | S | **P2** | Documentation debt |
| time.Sleep not context-aware | Low | S | **P2** | Minor correctness issue |
| Metrics write errors silently dropped | Low | M | **P2** | Observability gap |
| DynamicSampler recovery race | Low | S | **P2** | Minor correctness edge case |
| AccessLog type assertion risk | Low | S | **P3** | Defensive coding |
| Config message size minimum too low | Low | S | **P3** | Safety net improvement |
| Error context strings not standardized | Low | S | **P3** | Debugging convenience |

---

## Final Summary

### Overall Code Quality: **Good**

The implementation is structurally sound with proper layering, clear responsibilities, and solid engineering discipline. All 25 tasks from the specification are implemented, all tests pass, lint is clean, and file size limits are respected. The codebase follows Go conventions reasonably well and the documentation (DEGRADATION_MATRIX.md, DATA_RETENTION.md) is comprehensive.

### Critical Quality Issues (P0)

1. **Degradation contract only partially wired** — Only file and message HTTP handlers call `AbortIfDegraded`. In a real DB or SSO degradation scenario, the remaining 6 handler domains (comment, notification, block, preference, search, admin) would return generic 500 errors or timeouts rather than properly structured 503 responses. The X-Aero-Degraded header is set, but handlers don't act on it. This should be resolved before relying on degradation in production.

### Maintainability Concerns

1. **Cleaner code duplication** — The three background cleaners share ~90% structural code. Any bug fix or feature addition must be replicated in all three places. Extract to a shared helper.

2. **Graceful shutdown ordering** — The current sequence stops background components before servers drain, risking interrupted in-flight requests during deployments. The sequence should be: signal health NOT_SERVING → drain servers → stop background components.

3. **gRPC interceptor comment mismatch** — A small documentation issue, but it signals that the interceptor ordering has not been explicitly reviewed and verified by the team.

### Technical Debt

- **P1 items**: Cleaner deduplication (M effort), shutdown ordering (M effort), probe test fix (S effort)
- **P2 items**: Comment fix (S effort), context-aware sleep (S effort), metrics error handling (M effort)
- Total estimated effort to clear all debt: ~5-8 days spread across the team

### Quick Wins (S effort items)

1. Fix the gRPC interceptor chain comment in `server.go:118-120`
2. Add context-aware sleep in all three Purge methods (4 lines total)
3. Fix AccessLog type assertion safety (2 lines)
4. Increase config minimum message size to 4KB
5. Make the probe transition test actually assert state transitions

The codebase shows thoughtful engineering (the atomic bitmask for degradation state, the `DynamicSampler` design, the independent outbox metrics lock) and is well-positioned for production deployment once the degradation wiring debt is addressed.
