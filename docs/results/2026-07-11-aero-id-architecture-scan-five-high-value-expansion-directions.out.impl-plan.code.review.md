Now I have a thorough understanding of the codebase. Let me compile the comprehensive code review.

---

# Code Quality Review — aero-id

## Overall Assessment

| Metric | Result |
|---|---|
| All tests passing | ✅ |
| `go vet` clean | ✅ |
| No TODO/FIXME/HACK in production code | ✅ |
| Structured logging (zap) | ✅ |
| Request ID propagation | ✅ |
| Dual architecture (unfinished migration) | ❌ Critical |
| Duplicate domain logic | ❌ High |
| File size approaching limit | ⚠️ Medium |
| `context.Background()` in background goroutines | ⚠️ Low |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Well-maintained | < 10 | ✅ |
| Function length | Good overall | < 50 lines | ✅ |
| Test coverage | ~102 test files, all passing | > 80% | ⚠️ Needs measurement |
| Code duplication | Significant (dual arch) | < 5% | ❌ |
| Documentation coverage | Excellent | > 70% | ✅ |
| File size compliance | 10 files > 400 lines | ≤ 500 | ⚠️ Approaching |

---

## Detailed Findings

### 1. Code Organization

| Category | Organization |
|---|---|
| Severity | **Critical** |
| Title | **Dual architecture with incomplete migration** |
| Location | `internal/service/*`, `internal/repository/*`, `internal/handler/{http,grpc}/*` vs `internal/{feature}/*` |
| Description | The codebase has two parallel implementations of the same business logic. The **old path** uses centralized `internal/service/`, `internal/repository/`, `internal/handler/` packages. The **new path** uses self-contained feature packages (`internal/comment/`, `internal/file/`, `internal/message/`, `internal/block/`, `internal/notification/`, `internal/search/`). Only the old path is wired in the composition root (`internal/app/new.go`). The new feature packages contain their own handlers that register routes via `RouteRegistrar` but are **never passed to the router**. |
| Current State | Two complete parallel implementations exist. The old path is live. The new feature packages are partially referenced by `search` and `notification/dispatcher` but not wired in `app.New()`. |
| Recommended State | Either complete the migration to the feature-package architecture or remove the unused feature-package code. The composition root should wire only one path. |
| Impact | **Confusion for new developers**: Which package to modify? Both? Double the maintenance burden. Risk of drift: a bug fix made in one path is missed in the other. |
| Effort | L — requires significant refactoring to complete the migration |

| Category | Organization |
|---|---|
| Severity | **High** |
| Title | **Architecture violation in HARNESS.md violated** |
| Location | `HARNESS.md`, `internal/notification/dispatcher.go`, `internal/search/*` |
| Description | HARNESS.md states feature packages must not import from other feature packages. However: `internal/search/` imports `internal/comment/`, `internal/file/`, and `internal/message/`. `internal/notification/dispatcher.go` imports `internal/block/`. This is a direct architecture rule violation. |
| Current State | Cross-feature imports exist in search (to build search hit union types) and notification dispatcher (to check blocks). |
| Recommended State | Define interfaces in a shared package (e.g., `internal/domain/` or `internal/pkg/`) that each feature implements. The search and notification packages should depend on interfaces, not concrete feature packages. |
| Impact | Creates coupling between features; violates the one-way dependency rule. |
| Effort | M — extract interfaces to shared package |

---

### 2. Duplicate Code

| Category | Naming / Duplication |
|---|---|
| Severity | **High** |
| Title | **Duplicate `WithActor` / `ActorFrom` / `MustActor` in domain and service** |
| Location | `internal/domain/actor.go` and `internal/service/service.go` |
| Description | Both packages define the same context-key-based actor extraction pattern. `domain.WithActor`, `domain.ActorFrom`, `domain.MustActor` are identical in semantics to `service.WithActor`, `service.ActorFrom`, `service.mustActor` (though the latter is unexported). This is a cut-and-paste duplication. |
| Current State | Two copies of essentially the same context extraction logic. The domain version is unused by the service layer because the service has its own. |
| Recommended State | Keep one canonical version. Either have the service package use `domain.ActorFrom` or remove the domain version entirely. The actor context is an orthogonal concern to domain entities. |
| Impact | Maintenance burden: changes to one (e.g., adding logging to actor extraction) must be hand-copied to the other. |
| Effort | S |

| Category | Quality / Duplication |
|---|---|
| Severity | **Medium** |
| Title | **Duplicate comment validation logic: `ValidateNewComment` / `NewComment` / `ErrNestingTooDeep`** |
| Location | `internal/domain/comment.go` vs `internal/comment/comment.go` |
| Description | The `ValidateNewComment` function, `NewComment` factory, and `ErrNestingTooDeep` sentinel are defined identically in two packages. The old path uses `domain.ValidateNewComment`. The new feature package defines its own `ValidateNewComment`. The feature package already type-aliases `Comment = domain.Comment`, so it should reuse the domain functions rather than redefining them. |
| Current State | Three domain functions exist in two places with identical behavior. The feature package's `comment.go` has its own copy. |
| Recommended State | Delete `internal/comment/comment.go`'s copies and have the feature service call `domain.ValidateNewComment` directly. |
| Impact | Bug fix in one would be missed in the other. |
| Effort | S |

---

### 3. Dependency Issues

| Category | Organization |
|---|---|
| Severity | **High** |
| Title | `newEvent` helper defined in `message.go`, used by `comment.go` |
| Location | `internal/service/message.go:103` (definition), `internal/service/comment.go:102, 128` (usage) |
| Description | The `newEvent` function creates an `outbox.Event` from component parts. It is defined inside `service/message.go` but used by `service/comment.go`. Since they're in the same package this compiles, but it creates an implicit dependency where the comment service relies on a function defined alongside the message service. A developer might not notice this when splitting files. |
| Current State | `newEvent` in `message.go` is used by both `message.go` and `comment.go` within the same package. |
| Recommended State | Extract `newEvent` into its own file (e.g., `service/events.go`) so it's clearly a package-level shared helper, not "owned by" the message service. |
| Impact | Readability: confusing package structure. Fragile: moving `message.go` to a feature package would break `comment.go`. |
| Effort | S |

---

### 4. File Size Approaching Limits

| Category | Quality |
|---|---|
| Severity | **Medium** |
| Title | **10 files approaching the 500-line hard limit** |
| Location | Multiple files (see table) |
| Description | While no non-test file exceeds 500 lines, several are dangerously close to the AGENTS.md-stated hard limit: |

| File | Lines | % of Limit |
|---|---|---|
| `internal/service/mocks/mocks.go` | 639* | 128% |
| `internal/service/comment.go` | 481 | 96% |
| `internal/comment/service.go` | 470 | 94% |
| `internal/repository/comment.go` | 463 | 93% |
| `internal/app/new.go` | 461 | 92% |
| `internal/service/notification.go` | 437 | 87% |
| `internal/repository/message.go` | 423 | 85% |
| `internal/service/message.go` | 417 | 83% |
| `internal/comment/repository.go` | 411 | 82% |
| `internal/message/repository.go` | 403 | 81% |

*`mocks.go` is auto-generated, but at 639 lines it still warrants attention — consider splitting into per-feature mock files.

The non-generated files at 461-481 lines are well past the **400-line refactoring trigger** specified in AGENTS.md.

| Current State | Files approach but don't breach the 500-line limit. |
| Recommended State | Split files that exceed 400 lines: `internal/service/comment.go` → `*_crud.go`, `*_report.go`, etc. `internal/app/new.go` → break into `wire_handler.go`, `wire_repo.go`, `wire_service.go` |
| Impact | Maintainability: large files are harder to reason about and review. |
| Effort | M |

---

### 5. Error Handling

| Category | Error Handling |
|---|---|
| Severity | **Low** |
| Title | **Best-effort audit logging swallows errors silently** |
| Location | `internal/service/file.go:133`, `internal/service/comment.go:290-296`, `internal/service/message.go:224` |
| Description | The `AuditLogger` is documented as "must not fail the primary operation." However, the `_ = s.audit.Log(...)` pattern silently discards errors. While this is intentional, there's no fallback such as logging the audit failure itself. If the audit store is down, operators get no indication audits are dropping. |
| Current State | Audit errors are discarded with `_ =`. |
| Recommended State | At minimum add a `log.Warn("audit log failed", zap.Error(err))` when audit logging fails. The `AuditLogger` interface should stay non-blocking but failures should be observable. |
| Impact | Operational: silent audit failures could mask compliance gaps. |
| Effort | S |

---

### 6. Concurrency & Context

| Category | Logging / Context |
|---|---|
| Severity | **Medium** |
| Title | **Multiple uses of `context.Background()` in background goroutines** |
| Location | `internal/outbox/relay.go:182`, `internal/outbox/repository.go:185`, `internal/repository/audit.go:144`, `internal/repository/notification.go:278`, `internal/storage/gc/sweeper.go:255`, `internal/pkg/redis/redis.go:60`, `internal/sso/client.go:236` |
| Description | Background goroutines that perform DB queries, SSO calls, and Redis operations use `context.Background()` which provides no cancellation or deadline propagation. If the application shuts down, these goroutines won't be interrupted promptly. |
| Current State | Relay, sweepers, and cleaners create detached `context.Background()` for their work. |
| Recommended State | Use a derived context from the application's lifecycle context. The `App.Run` method receives a `ctx` from the caller — pass a cancel-able derived context to each background goroutine so shutdown cancels in-flight work. |
| Impact | Graceful shutdown may be delayed by in-flight background operations completing. |
| Effort | M |

---

### 7. Logging & Observability

| Category | Logging |
|---|---|
| Severity | **Low** |
| Title | **Access log sampling logs actor_uid with no PII scrub** |
| Location | `internal/middleware/access_log.go:31` |
| Description | Access log lines include `zap.String("actor", c.GetString("actor_uid"))`. The `actor_uid` is an SSO user identifier. Depending on the SSO implementation, this could be a PII-bearing value (email, username). There's no annotation or configuration to scrub/redact PII from logs. |
| Current State | Actor UID is logged in every request log. |
| Recommended State | Add a configuration option to redact or hash the actor UID in production access logs. Document that the field may contain PII. |
| Impact | Privacy compliance (GDPR, CCPA) if logs are shipped off-cluster. |
| Effort | S |

---

### 8. Testing

| Category | Testing |
|---|---|
| Severity | **Medium** |
| Title | **Feature-package tests may not exist** |
| Location | Feature packages: `internal/comment/`, `internal/file/`, `internal/message/`, `internal/block/`, `internal/notification/`, `internal/search/` |
| Description | The newer feature packages have their own service, repository, and handler implementations. However, some have limited test coverage. For example, `internal/comment/` has only `domain_test.go` (testing `ValidateNewComment`), but no service-level tests for `internal/comment/service.go`. Meanwhile, `internal/service/comment_test.go` tests the old path. If the migration to feature packages completes, the tests need to move too. |
| Current State | Tests exist for the old centralized path. Feature packages have minimal or no tests. |
| Recommended State | Ensure the feature packages have equivalent test coverage before the old path is deprecated. Add `service_test.go` to each feature package. |
| Impact | Risk: if the migration completes, business logic in feature packages is untested. |
| Effort | L |

| Category | Testing |
|---|---|
| Severity | **Low** |
| Title | **Mock file at 639 lines** |
| Location | `internal/service/mocks/mocks.go` |
| Description | The auto-generated mock file is 639 lines. While generated code is exempt from the 500-line limit, this file combines mocks for every dependency (FileRepo, MessageRepo, CommentRepo, etc.) in one file. |
| Current State | One mammoth mocks file. |
| Recommended State | Split into per-feature mock files: `mocks/file_repo.go`, `mocks/message_repo.go`, `mocks/comment_repo.go`, etc. |
| Impact | Compilation speed: modifying one mock forces recompilation of all. Developer experience: hard to find the relevant mock. |
| Effort | S |

---

### 9. Configuration & Security

| Category | Quality |
|---|---|
| Severity | **Low** |
| Title | **Webhook signing and encryption-at-rest concerns** |
| Location | `internal/domain/webhook.go:28-29`, `internal/outbox/webhook.go` |
| Description | `WebhookSubscription.SigningKey` is documented as "plaintext stored, encrypted at rest" but `domain.go:29` says `SigningKey string // HMAC-SHA256 key (plaintext stored, encrypted at rest)`. No actual encryption-at-rest mechanism is visible in the repository layer. Similarly, the `Secret` field is described as "optional additional secret for custom auth headers." |
| Current State | Signing keys stored in plaintext in the database (at-rest encryption is commented but not enforced). |
| Recommended State | Either implement transparent column-level encryption or clarify in documentation that encryption is a deployment-level concern (e.g., encrypted disk / TDE). Consider storing only a hash of the signing key. |
| Impact | Security: signing key exposure compromises webhook authenticity. |
| Effort | M |

---

### 10. Documentation

| Category | Naming / Documentation |
|---|---|
| Severity | **Low** |
| Title | `internal/repository/` vs feature-package `internal/{feature}/repository.go` dual documentation |
| Location | `internal/repository/comment.go` and `internal/comment/repository.go` |
| Description | Both the old centralized repository and the new feature-package repository define interfaces and implementations for the same entities. The documentation in both needs to be kept in sync. |
| Current State | Two parallel repository interfaces exist for comments, files, messages, etc. |
| Recommended State | Decide on one architecture and deprecate the other. |
| Impact | Developers must update both when changing a repository interface. |
| Effort | L (blocked on architecture decision) |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|---|---|---|---|---|
| Dual architecture (old centralized vs new feature packages) | Critical | L | P0 | The single biggest issue. Must resolve before new feature work. |
| Cross-feature imports (search→file/comment/message, notif→block) | High | M | P1 | Violates HARNESS.md one-way dependency rule. |
| Duplicate WithActor/ActorFrom (domain vs service) | Medium | S | P2 | Cut-and-paste duplication. |
| Duplicate ValidateNewComment/NewComment (domain vs comment) | Medium | S | P2 | Bug risk from divergent implementations. |
| newEvent in message.go used by comment.go | Low | S | P2 | Extract to shared helper file. |
| Files approaching 500-line limit (10 files > 400 lines) | Medium | M | P2 | Refactoring trigger already hit per AGENTS.md. |
| context.Background() in background goroutines | Medium | M | P2 | Delays graceful shutdown. |
| Best-effort audit silently drops errors | Low | S | P3 | Add warning log on audit failure. |
| No PII scrub in access logs | Low | S | P3 | Compliance risk. |
| Feature-package tests incomplete | Medium | L | P1 | Tests exist only on old path. |
| Webhook signing key stored in plaintext | Low | M | P3 | Security hardening. |
| Mock file at 639 lines | Low | S | P3 | Convenience improvement. |

---

## Quick Wins (S-effort fixes)

1. **Extract `newEvent` to `service/events.go`** — visible in 5 minutes, removes cross-file dependency.
2. **Log audit failures** — add `log.Warn("audit log failed", ...)` alongside every `_ = s.audit.Log(...)`.
3. **Remove duplicate domain functions** — have `internal/comment/comment.go` delegate to `domain.ValidateNewComment` and `domain.NewComment` instead of redefining them.
4. **Split mocks into per-feature files** — better developer experience and faster compilation.
5. **Add a `context.Background()` → lifecycle context refactoring plan** — start by threading the app context through background goroutine constructors.

---

## Summary

**Overall Code Quality: Good but with Critical technical debt**

The codebase demonstrates excellent engineering practices in many areas: thorough documentation, clean error handling with the `AppError` pattern, good interface design with functional options, structured logging, request ID propagation, comprehensive migrations, proper separation of concerns at the package level, and a strong testing culture with 102 test files all passing.

**However**, the **dual architecture** (old centralized path vs new feature packages) is the most critical issue. This represents an incomplete migration that has created significant code duplication, confusion about which path is canonical, and potential for divergent bug fixes. Every new feature addition currently requires touching code in two places. This must be resolved before any significant new feature work.

The 10 files approaching the 500-line hard limit are also concerning per AGENTS.md's own refactoring triggers. Several of these are well past the 400-line "plan a split" threshold.

**Critical Quality Issues:**
1. **Dual architecture with incomplete migration** — must resolve before new features
2. **Cross-feature imports** — violates the one-way dependency rule

**Maintainability Concerns:**
- 10 files near the 500-line limit
- Duplicate domain logic across packages
- Feature packages not wired into the composition root
- Tests exist only on the old code path

**What's done well:**
- Excellent documentation and naming
- Clean error handling with proper `AppError` wrapping
- Strong testing culture with both unit and integration tests
- Well-structured middleware chain with clear responsibilities
- Thorough configuration validation
- Good use of Go idioms (functional options, interfaces, type aliases)
- No TODO/FIXME/HACK comments in production code
- All tests pass; `go vet` is clean
