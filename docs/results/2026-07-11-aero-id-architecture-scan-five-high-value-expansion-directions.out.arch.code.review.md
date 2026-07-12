Now I have a complete picture. Let me produce the comprehensive Staff Engineer review.

---

# Staff Engineer Review: aero-id

## Code Quality Analysis

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity (per func) | ~4-7 avg | < 10 | ✅ |
| Function length | ~10-40 lines avg | < 50 lines | ✅ |
| File size (test/mocks excluded) | 4 files > 400 lines | < 500 lines | ⚠️ (approaching) |
| Code duplication | **Significant** — dual arch | < 5% | ❌ |
| Documentation coverage (public API) | ~70% | > 70% | ✅ |
| `go vet` | Clean | Clean | ✅ |

---

## Detailed Findings

### 1. Code Organization

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|--------------|-------------------|--------|--------|
| Organization | **Critical** | Dual parallel architectures | `internal/comment/`, `internal/file/`, `internal/message/`, `internal/notification/`, `internal/block/`, `internal/search/` vs `internal/service/`, `internal/repository/`, `internal/handler/{http,grpc}/` | Two complete, independent implementations coexist. Modular packages (`internal/comment/` etc.) each contain their own handler, service, and repository; layered packages (`internal/service/` etc.) provide a separate parallel implementation. The app wiring (`internal/app/new.go`) uses only the layered arch. The modular packages are dead code — they compile but are never imported by the runtime. | 55+ non-test Go files across 6 modular packages duplicate the layered architecture's functionality. Tests in modular packages import from layered packages (creating confusing cross-references). | Remove the unused modular packages entirely, or complete the migration to one canonical architecture. The codebase should have ONE architecture, not two. | Massive maintenance drag: every feature must be implemented twice, bugs may exist in dead code, new developers spend weeks understanding which arch is "real". | L |
| Organization | High | Interface-defined context key collision | `internal/domain/actor.go:14` and `internal/service/service.go:39` | `actorKey` context key type and `WithActor`/`ActorFrom`/`mustActor` functions are defined in BOTH the domain and service packages, doing essentially the same thing. Domain's `MustActor` returns `*AppError`; service's `mustActor` is identical. This violates DRY and creates confusion about which to use. | Two separate implementations of the same context-based actor pattern. No cross-references between them. | Consolidate into one canonical source (likely `internal/service/` since that's the active architecture). Keep only ONE `WithActor`, `ActorFrom`, `mustActor` implementation. | Confusion, subtle bugs if one is updated without the other, unnecessary code bloat. | S |
| Organization | Medium | `canReadFile`/`canReadMessage` duplicated | `internal/domain/access.go:3-10` and `internal/service/service.go:284-293` | The ACL helper functions `canReadFile` and `canReadMessage` are defined in both the domain package and the service package. The domain versions are never imported by the service layer. | Domain has `CanReadFile`/`CanReadMessage` (exported) with same logic. Service has `canReadFile`/`canReadMessage` (unexported). | Remove from domain (it's unused) or have service delegate to domain. Pick ONE home for ACL logic. | Violates "single source of truth" principle for ACL rules. | S |
| Organization | Medium | `authorUIDs` / `reporterUIDs` scattered | `internal/service/comment.go:441-461` + similar in message/notification services | The `authorUIDs` dedup helper appears in `internal/service/comment.go`, and similar patterns exist across message and notification services. These are nearly identical but independently maintained. | Each service implements its own uid-dedup loop. | Extract to a shared utility in `internal/service/service.go` or `internal/pkg/`. | Small duplication risk if the pattern evolves differently across services. | S |

### 2. Naming & Documentation

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|--------------|-------------------|--------|--------|
| Naming | Low | `ID` type alias hides package identity | `internal/domain/shared.go:6` | `type ID = idgen.ID` re-exports `uuid.UUID` as `domain.ID`. While convenient, this obscures that IDs are UUIDs throughout the codebase. New contributors must discover that `domain.ID` is just `uuid.UUID`. | `domain.ID` used everywhere (files, messages, comments, etc.) as a type alias for `uuid.UUID`. | Consider a named type (not alias) if behavioral methods are needed later. Current approach is acceptable for MVP but worth noting. | Minimal — cosmetic. | XS |
| Naming | Low | `MessageKindSystem` available but unvalidated sender-side | `internal/domain/shared.go:26` | `MessageKindSystem` exists but relies on external callers (`SendSystem`) to use it. The `IsSystem()` method exists but is not enforced in the `Send` path. | `MessageKind` has `IsSystem()` but the user-facing `Send` method relies on the service layer to reject system-kind sends rather than domain-level protection. | Consider adding domain-level validation: `func ValidateSendKind(MessageKind) error` that rejects system kind. | Low — prevents a mis-wired service from accidentally forging system messages. | XS |

### 3. Error Handling

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|--------------|-------------------|--------|--------|
| Error Handling | Low | `AppError.Cause` field name overlaps with `Unwrap()` convention | `internal/pkg/errors/errors.go:17` | The `Cause` field is named differently from Go's error wrapping convention (`Unwrap` returns `e.Cause`). This is fine mechanically but inconsistent with modern Go idioms where errors typically use `%w` wrapping with `fmt.Errorf`. | Manual `Wrap(err)` method creates a shallow copy, sets `Cause`. No `%w` verb usage. | Consider whether `fmt.Errorf("...: %w", err)` patterns would be cleaner for some wrapping cases. Current approach is acceptable but verbose. | Low — style preference. | XS |
| Error Handling | Low | Repository errors duplicated across packages | `internal/repository/db.go:11` and `internal/domain/errors.go:3` | Both `internal/repository.ErrNotFound`/`ErrDuplicate` and `internal/domain.ErrNotFound`/`ErrDuplicate` exist. The domain errors appear unused by actual code (service uses repository errors). | Two sets of identical sentinel errors. | Remove `internal/domain/errors.go` if domain errors are not referenced by any production code path. Eliminates ambiguity. | Minimal risk of confusion. | XS |

### 4. Logging

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|--------------|-------------------|--------|--------|
| Logging | Low | No structured context propagation in all middleware | `internal/middleware/access_log.go` | Access logging uses `c.Request.Context()` for trace fields. The middleware chain propagates request IDs, but not all handlers include the authenticated actor UID in structured logs. | Request ID is propagated. Actor UID is available in context but not consistently logged with zap fields on every request. | Ensure every logged event includes `request_id`, `actor_uid` (when available), and any other tracing context as structured fields. | Minor — debugging production issues becomes slightly harder. | S |
| Logging | Low | Degradation state transitions not logged | `internal/middleware/degradation.go` | When `StartProbing` marks a dependency as degraded or restored, it doesn't log the transition. Operators have no visibility into degradation events. | State transitions happen silently. Only the X-Aero-Degraded header reflects state. | Log state transitions at WARN level when degraded and INFO level when restored. | Operational — helps operators understand intermittent failures. | XS |

### 5. Testing Practices

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|--------------|-------------------|--------|--------|
| Testing | Medium | Mock file size exceeds 500-line limit | `internal/service/mocks/mocks.go:639 lines` | The mocks file is 639 lines. While AGENTS.md excludes mocks from `check-file-sizes`, this file is still too large and contains 11 separate mock structs. It's a maintenance burden because any interface change in repository/service/storage requires updating this file. | 639 lines, 11 mock structs, 80+ mock methods. Each mock has sensible default behaviors (no-op methods return nil/empty). | Split mocks into separate files per domain (e.g., `mocks_comment.go`, `mocks_file.go`, `mocks_message.go`). This aligns with the single-responsibility principle and makes diffs clearer when interfaces change. | High — every interface change touches this one file, making PRs noisy and merge conflicts likely. | M |
| Testing | Medium | Notification service test coverage gaps | `internal/service/notification_test.go` (375 lines) | The notification service test file is relatively small compared to comment (601 lines) and message (607 lines) service tests. Given the complexity of the notification dispatcher, rate limiter, collapse logic, and preference/block checking, the test coverage appears incomplete. | Notification service tests cover basic List/MarkRead/MarkAllRead/UnreadCount flows but may miss edge cases in dispatcher rate limiting, collapse merging, and preference integration. | Add service-layer tests for: dispatcher rate limiter behavior, notification collapse merging, preference-filtered dispatch, block-checked dispatch. | Medium — notification substrate is critical for UX; bugs are hard to detect without comprehensive testing. | M |
| Testing | Low | No `assertAppCode` helper in a shared test package | `internal/service/comment_test.go:591+` pattern repeated across service tests | `assertAppCode` helper (or equivalent) is defined locally in each test file rather than in a shared test utilities package. Each service test package re-implements it. | Each `_test.go` file has its own error-checking helper. | Extract to a shared `internal/service/testutil/` or use a consistent testify pattern across all tests. | Low — duplicate test helper logic. | XS |
| Testing | Low | Integration tests appear sparse for some modules | Various `*_integration_test.go` files | Integration tests exist for block, file_share, notification, outbox, reaction_audit, search, and file. But there's no `message_integration_test.go` or `comment_integration_test.go`. | Repository integration tests cover: block, file, file_share, notification, outbox, reaction_audit, search. Missing: comment, message. | Add integration tests for comment and message repositories to verify SQL semantics with real PostgreSQL. | Medium — repository-level bugs in comment/message queries won't be caught by unit tests. | M |

### 6. Technical Debt

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|--------------|-------------------|--------|--------|
| Technical Debt | **Critical** | Dead code — modular architecture packages | `internal/comment/`, `internal/file/`, `internal/message/`, `internal/notification/`, `internal/block/`, `internal/search/` | These modular packages are NEVER imported by the main app. They compile but are dead code. They add ~25 non-test source files with full handler/service/repository layers that are never wired into the runtime. Every code navigation, search, and refactoring operation has to consider both architectures. | 55+ non-test Go files in modular packages, none used by `cmd/server/main.go`. Tests in these packages reference the layered packages. | **Option A**: Delete the modular packages entirely if they are abandoned. **Option B**: Complete the migration so the modular packages BECOME the canonical architecture and delete the layered packages. Either way, there MUST be ONE architecture. | Massive — new developers waste weeks understanding which arch to use. Every PR must consider both. Maintenance burden doubles. | L |
| Technical Debt | High | `internal/service/mocks/mocks.go` is a single-file monolith | `internal/service/mocks/mocks.go:639 lines` | 11 mock implementations in one file. Every time a repository, storage, or service interface changes, this file changes. The compile-time interface satisfaction checks at the bottom (`var _ repository.FileRepository = ...`) are good, but the file is fragile. | All mocks in one file with sensible default behaviors. | Split by domain into separate files within the mocks package. Keeps diff scopes small when interfaces evolve. | Medium — merge conflicts are likely when multiple PRs touch service interfaces. | M |
| Technical Debt | Medium | `internal/service/comment.go` approaching file-size limit | `internal/service/comment.go:481 lines` | At 481 lines this is only 19 lines below the 500-line limit. The file handles: Create, List, Replies, Edit, Delete, Report, Hide/Unhide, ListReports, ResolveReport, and four helper functions. | One file with ~15 public/private methods. | Consider splitting into `comment_crud.go` (Create, List, Replies, Edit, Delete) and `comment_moderation.go` (Report, Hide/Unhide, ListReports, ResolveReport) once the file exceeds 450 lines. | Maintainability — one more feature addition will trigger the 500-line gate. | S |
| Technical Debt | Medium | `internal/repository/comment.go` approaching file-size limit | `internal/repository/comment.go:463 lines` | Similar to the service file, this repository file handles full comment CRUD, replies, likes, reports, moderation, and search. | One file with ~20 methods. | Split into `comment_crud.go` and `comment_moderation.go` (or `comment_reports.go`). | Same as above — approaching the 500-line limit. | S |
| Technical Debt | Medium | `internal/app/new.go` is the largest non-test file | `internal/app/new.go:461 lines` | The composition root handles 26 distinct dependency instantiations. While clear, this file is very long and any new dependency adds to it. | One massive wire-up function. | Consider grouping dependency wiring into package-level build functions (e.g., `buildSSO`, `buildRepos`, `buildServices`) to keep the file manageable and improve readability. | Maintainability — function is already 461 lines; growing with every feature. | M |
| Technical Debt | Medium | GORM raw SQL expressions in repository | `internal/repository/comment.go:196-198` | `gorm.Expr("GREATEST(like_count - 1, 0)")` embeds raw SQL in Go code. Similar in file repo and message repo. While functional, this couples repository logic to PostgreSQL-specific syntax. | GORM expressions used for decrement-like operations. | Consider functional impact: If the project ever needs to support another database dialect, these expressions must be found and replaced. Acceptable for now. | Low — PostgreSQL is the only target. | XS |
| Technical Debt | Low | `WebhookSubscription.SigningKey` plaintext storage | `internal/domain/webhook.go:19` | The `SigningKey` and `Secret` fields in `WebhookSubscription` are stored as plaintext strings. The comment says "encrypted at rest" but no encryption implementation is visible. | Fields stored as plain strings. Comment mentions encryption at rest but no code implements it. | Implement encryption at the repository layer (transparent encryption/decryption of the fields) or update the comment to reflect actual behavior. | Security — signing keys in plaintext in the database is a credential exposure risk. | M |

### 7. Code Quality Metrics

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|--------------|-------------------|--------|--------|
| Quality | Low | Domain event creation pattern inconsistency | `internal/service/comment.go:143-167`, `internal/outbox/outbox.go:76-88` | Domain events are created using `newEvent()` in the service layer but the outbox package offers `outbox.NewEvent()` constructor. Both produce the same struct but use different paths. | Two ways to create outbox events. | Standardize on `outbox.NewEvent()` everywhere so the outbox package controls its own construction API. | Low — inconsistent pattern, programming error risk when creating events. | XS |
| Quality | Low | `storage.ErrNotSupported` vs `storage.ErrNotFound` naming collision potential | `internal/storage/storage.go:9-12` | `ErrNotSupported` and `ErrNotFound` are distinct but could be confused in handler code. The `PresignPut` return path relies on callers checking `ErrNotSupported`. | Two similar-looking error values. | Consider having `ErrNotSupported` wrap a typed struct or use a specific code like `storage.ErrOperationNotSupported` for clarity. | Low — cosmetic clarity improvement. | XS |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| **Dual architecture (dead modular packages)** | High | L | **P0** | 55+ source files duplicated. Must resolve before any new feature work. |
| **Mock file monolith** (639 lines) | Medium | M | **P1** | Every interface change touches it. Split by domain. |
| **comment.go approaching 500-line limit** (layer + repo) | Medium | S | **P1** | Pre-emptive split before the gate triggers. |
| **Missing integration tests for comment/message repos** | Medium | M | **P1** | Repository SQL correctness not verified for these critical domains. |
| **Notification service test gaps** | Medium | M | **P1** | Rate limiter, collapse, preference/block dispatch untested at service level. |
| `app/new.go` monolithic wiring | Medium | M | **P2** | 461 lines; growing with every feature. Extract builders. |
| Webhook signing key plaintext storage | Medium | M | **P2** | Security concern, but impact depends on deployment security model. |
| Context key / ACL / authorUIDs duplication | Low | S | **P2** | Consolidate DRY violations across service + domain packages. |
| No structured actor_uid in all log events | Low | S | **P2** | Debugging quality-of-life improvement. |
| Degradation transitions not logged | Low | XS | **P2** | Operational visibility improvement. |

---

## Summary

### Overall Code Quality: **Needs Work**

The codebase has a solid architecture **on paper** — clear layered separation, clean error handling, good use of composition root with DI, and well-designed domain entities. The engineering constraints (file sizes, complexity limits) are sensible. The `go vet` is clean, and there are no TODO/FIXME/HACK comments.

However, there is **one critical issue that overshadows everything**: the codebase contains **two complete parallel implementations** (modular and layered architectures) with ~55+ dead source files in the modular packages. This is confusing for new team members, doubles the maintenance surface, and creates a constant cost when searching, navigating, and refactoring.

### Critical Quality Issues (Must Fix Before Production)
1. **Dual architecture** — resolve the modular vs layered duplication. Decide on one canonical architecture and delete the other.
2. **Mock file monolith** — split into domain-scoped files to reduce merge conflicts.
3. **Comment/repo files approaching 500-line limit** — pre-emptive split before they trigger the gate.

### Maintainability Concerns
- The `internal/app/new.go` composition root is already 461 lines and grows with every new dependency. Consider extracting builders.
- Feature additions require changes in both Handler (HTTP + gRPC), Service, Repository, Domain, Tests, Mocks, and potentially Migration layers — the surface area is wide but predictable.

### Quick Wins (S effort, high impact)
1. Consolidate `actorKey`/`WithActor`/`ActorFrom`/`mustActor` into one package (remove from `internal/domain/actor.go`)
2. Consolidate `canReadFile`/`canReadMessage` into one location
3. Remove `internal/domain/errors.go` if unused
4. Add degradation state transition logging
5. Standardize domain event creation on `outbox.NewEvent()`
6. Start splitting `internal/service/mocks/mocks.go` by domain

### Key Strengths
- Clean layered architecture with one-way dependency enforcement
- Excellent mock pattern (function-field mocks with sensible defaults)
- No technical debt artifacts (no TODOs, FIXMEs, HACKs, or BUGs)
- Comprehensive error handling with AppError wrapping
- Good use of transactional outbox pattern for reliability
- Data retention, rate limiting, circuit breakers, and graceful shutdown are well-implemented
- Configuration is comprehensive, documented, and env-overridable
- Storage abstraction is clean with local/S3 backends
- Tests are well-structured and readable
