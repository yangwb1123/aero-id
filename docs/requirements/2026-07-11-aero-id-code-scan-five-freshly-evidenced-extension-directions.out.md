Confirmed: `validateUpstream(token)` drops the caller's context entirely. The outer `ValidateToken(ctx, token)` receives a proper context from the request path (carrying trace ID + timeout), but on cache miss it calls `c.validateUpstream(token)` which ignores the context and creates `context.Background()`. The `ctx` on line 175 is only used to cancel the `select` — the actual upstream RPC inside `sf.DoChan` runs with a bare context.

**This is actually more impactful than the sweeper/relay context issue**, because it's on the hot path (every token cache miss), not a background maintenance loop. The fix would be:

```go
// Instead of:
return c.validateUpstream(token)
// Should be:
return c.validateUpstream(ctx, token)
```

And `validateUpstream` accepting `ctx context.Context` instead of creating `context.Background()`.

---

## Overall Assessment

Your document is **production-grade analysis** — the code evidence is precise, the edge-case tables are thorough, and the sprint prioritization is sound. A few observations from cross-referencing the actual codebase:

### What I'd suggest refining

1. **Direction 4**: Consider splitting into two sub-problems with different severities:
   - **(P1) Hot-path context loss**: `validateUpstream` ignoring caller's context (affects every cache-miss auth RPC)
   - **(P2) Background worker context**: relay + sweeper using `context.Background()` (affects async maintenance)

2. **The `validateUpstream` finding** arguably belongs as its own direction or as a higher-priority item within Direction 4. It's a real bug, not just an architectural gap.

3. **Direction 1**: There's a subtlety about S3 presigned URLs — S3 supports `response-content-disposition` query parameter, but the current `PresignGet` doesn't pass it. The local backend has no equivalent capability. So the handler-level fix is necessary **regardless** of storage backend.

### Minor corrections

- The `idempotency/store.go` and `ratelimit` cleanup workers (`stopRateLimit`, `stopIdemClean`) are called in shutdown but I didn't check whether they also use `context.Background()`. Worth a quick look if this gets prioritized.
- The notification dispatcher's `createCollapsed` logic (5-min window, same type/actor/subject) is correct as described. Your assessment that it can't cross-type-aggregate is accurate.

### What the document does exceptionally well

- **Architecture impact tables** — the scoping per component (handler/service/repository/storage) is exactly what's needed for sprint planning
- **Edge case tables** — especially the non-ASCII filename RFC 5987 and the 416 Range response, which are easy to miss
- **Evidence cross-referencing** — every claim has a file:line anchor

This reads like a well-structured RFC that could move directly into implementation tickets. Are you planning to convert these into GitHub issues, or would you like me to help with anything else based on the analysis?
