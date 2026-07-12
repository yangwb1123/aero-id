Based on my thorough codebase examination, I can see that **most of the implementation plan tasks are already implemented**. Let me summarize what's done and what's still outstanding:

## Current Status of the Implementation Plan (2026-07-12)

### ✅ Already Implemented

| Task | Status | Notes |
|------|--------|-------|
| **T006** - AccessLog sampling | ✅ | `middleware/sample.go` + wired in `router.go` |
| **T007** - Adaptive tracing | ✅ | `DynamicSampler` in `pkg/tracing/tracing.go` |
| **T008** - Relay log aggregation | ✅ | Aggregated `"outbox drain"` log in `relay.go` |
| **T009** - Metrics write amplification | ✅ | `ObserveOutboxDrain` with flush pattern |
| **T010** - Degradation matrix doc | ✅ | `docs/DEGRADATION_MATRIX.md` |
| **T011** - Degradation state manager | ✅ | `DegradationState` in `middleware/degradation.go` |
| **T012** - Degradation middleware (HTTP+gRPC) | ✅ | HTTP gin middleware + gRPC interceptor |
| **T014** - Degradation tests | ✅ | `degradation_test.go` + `server_test.go` |
| **T015-T021** - Transport hardening | ✅ | Config, validation, `new.go` wiring, tests |
| **T022** - audit_events cleaner | ✅ | `PurgeOlderThan` + `RunAuditCleaner` |
| **T023** - notifications cleaner | ✅ | `PurgeReadOlderThan` + `RunNotificationCleaner` |
| **T024** - outbox_events cleaner | ✅ | `PurgeDeadLetters` + `RunOutboxCleaner` |
| **T025** - Data retention doc | ✅ | `docs/DATA_RETENTION.md` |
| **T001-T005** - Release engineering | ✅ | `.goreleaser.yaml`, CI workflows, `cliff.toml` |

### ❌ Still Missing

| Task | Status | Notes |
|------|--------|-------|
| **T013** - Handler adaptation for degradation | ⬜ **NOT DONE** | `DegradationAction()` exists but **no handler calls it** |

---

**The one major code gap is T013**: the `DegradationAction` helper function is defined in `middleware/degradation.go` but no HTTP or gRPC handler actually uses it to adapt behavior during degradation.

Would you like me to implement **T013** — adapting all handlers to use `DegradationAction` for proper degradation behavior? This involves:

1. Adding a `DegradationState` reference to each handler's constructor
2. Calling `DegradationAction(state, deps, readOnly)` at the start of each handler
3. Returning 503 or serving stale data based on the result

Could you confirm this is what you want implemented, or should I focus on something else?
