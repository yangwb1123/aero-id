Now I have a thorough understanding of the entire codebase. Here is my complete review.

---

# Staff Engineer Code Review: aero-id

## 1. Code Organization

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Organization | **Critical** | Parallel dead-code packages exist | `internal/file/`, `internal/comment/`, `internal/message/`, `internal/notification/`, `internal/block/`, `internal/search/`, `internal/audit/` | These entire packages (thousands of lines) are **never imported by the composition root** (`internal/app/new.go`). They are only referenced by their own test files and a few search handlers. Meanwhile the old `internal/service/` monolith package is what gets wired at startup. This is an abandoned or partial refactoring effort left in place. | ~7000+ lines of production code in dead packages with zero production usage across 7 sub-packages | Either wire the new packages into the composition root or remove them entirely. Keep only one implementation of each domain concern. | **High** — Unused code is a maintenance burden that confuses onboarding, adds build time, and risks bit-rot. Dead codepaths can accumulate security vulnerabilities. | **L** |
| Organization | High | Monolithic service package | `internal/service/comment.go` (481 lines), `service/message.go` (417 lines), `service/notification.go` (437 lines) | The `internal/service` package contains business logic for files, messages, comments, notifications, blocks, search, admin, ban, preferences, reactions, and rate limiting all in the same package. This violates the single-responsibility trigger in AGENTS.md. | All business logic lives in one package with 14+ source files | Split into domain-oriented sub-packages (`service/file`, `service/message`, `service/comment`, etc.) | **Medium** — Bloated packages reduce discoverability. New engineers must scan many files to understand a single feature. | **L** |
| Organization | Medium | Repository DB type declarations mixed with repositories | `internal/repository/db.go` (339 lines) | `db.go` contains GORM row types, domain converters, and sentinel errors for files, messages, comments, notifications, audit events, file shares, comment reports, reactions, and more — all in one file. | All row types, conversion functions, and sentinel errors live in one file | Split into per-domain files (e.g., `file_row.go`, `message_row.go`, `comment_row.go`) or co-locate row types with their repository files | **Medium** — `db.go` is a god file that every repository package depends on, making changes high-risk | **M** |
| Organization | Low | Imports named inconsistently | `internal/app/new.go` | Several packages are imported with custom aliases (`httpapi`, `grpcsrv`, `redislib`, `storagegc`, `grpclib`) while others use their package name directly (`middleware`, `migrate`, `service`, `sso`). | Inconsistent aliasing convention | Establish a convention: alias only when the package name conflicts or is unclear | **Low** — Readability friction for new contributors | **S** |

## 2. Naming & Documentation

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Naming | High | Domain sentinel errors are dead code | `internal/domain/errors.go` (line 5–6) | `domain.ErrNotFound` and `domain.ErrDuplicate` are defined but **never used** outside tests. The codebase consistently uses `repository.ErrNotFound` and `repository.ErrDuplicate` instead. | Two unused sentinel errors in the domain package | Remove unused errors. The domain layer should define only errors that domain logic produces (like `ErrNestingTooDeep`). | **Medium** — Dead code in the domain layer sends confusing signals about error handling strategy | **S** |
| Naming | High | Domain error defined but never used anywhere | `internal/domain/comment.go` line 24 | `domain.ErrNestingTooDeep` is defined but the service layer bypasses it entirely, using `apperr.BadRequest("comment.nesting_too_deep", ...)` instead. No production code imports or references `ErrNestingTooDeep`. | Domain error exists but is dead code | Use the domain error in the service layer (`return apperr.BadRequest(...).Wrap(domain.ErrNestingTooDeep)`) or remove the domain error | **Medium** — Domain invariants should be enforced at the domain layer, not the service layer | **S** |
| Documentation | Medium | Missing doc on `nolint:nilnil` | `internal/pkg/redis/redis.go` line 25 | `return nil, nil //nolint:nilnil` lacks a comment explaining why returning nil,nil is safe/expected here. The comment only says "nolint:nilnil". | Minimal suppression comment | Add explanatory doc: "No Redis configured; caller is expected to check for nil and fall back to in-memory/GORM alternatives." | **Low** — Future maintainers may misinterpret the nil return | **S** |
| Naming | Low | Inconsistent newEvent helpers | `internal/service/message.go` line 151; `internal/outbox/outbox.go` line 68 | Both packages define an event factory: `outbox.NewEvent(aggregateType, aggregateID, eventType, payload any)` and `newEvent(aggregateType, aggregateID, eventType string, payload map[string]any)`. The service version duplicates logic and uses a stricter payload type. | Duplicate event factories | Replace the private `newEvent` in message.go with `outbox.NewEvent` and adjust callers to use `any` payload | **Low** — Code deduplication, minor API alignment | **S** |

## 3. Error Handling

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Error Handling | **Critical** | Repository errors returned directly by new subpackages | `internal/file/repository.go` line 95, 134, 145, 208; `internal/comment/repository.go` line 152, 226, 243, 328, 365, 385 | The new sub-package repositories (`internal/file/`, `internal/comment/`) return `domain.ErrNotFound` and `domain.ErrDuplicate` directly, NOT `repository.ErrNotFound/ErrDuplicate`. Since the domain errors are never caught by the service layer's `stderrors.Is(err, repository.ErrNotFound)` checks, this creates an untested error path. | Two different sentinel error families for the same condition | Standardise on one sentinel error family across all repository implementations. Either remove `domain.ErrNotFound`/`domain.ErrDuplicate` and use `repository.ErrNotFound`/`repository.ErrDuplicate` throughout, or vice versa. | **High** — If the new sub-packages are ever wired in, errors will not be caught correctly by the service layer, leading to 500s instead of 404s | **M** |
| Error Handling | Low | Missing error wrapping in some repository methods | `internal/repository/file.go` (multiple locations) | Some repository methods wrap errors with `fmt.Errorf("...: %w", err)` while others (e.g., `ListByOwner`, `Delete`) do not. | Inconsistent error wrapping | Wrap all database errors with context about which operation failed | **Low** — Debuggability of production issues | **S** |

## 4. Logging

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Logging | Medium | No correlation ID in background goroutines | `internal/app/warmup.go`, `internal/outbox/relay.go`, `internal/repository/audit.go`, `internal/repository/notification.go`, `internal/middleware/degradation.go` | Background goroutines for warmup, relay, GC, and data retention cleaners all use `context.Background()` which lacks request correlation IDs and tracing context. | Isolated background tasks with no trace context | Use a derived context with a background correlation ID (e.g., a dedicated tracer for each background component). At minimum, add a component-name attribute to the context. | **Medium** — Debugging production issues across async boundaries is harder without trace propagation | **M** |
| Logging | Medium | Audit logging swallows errors silently | `internal/service/comment.go` line 400, `internal/service/file_share.go` | Audit log failures use `_ = s.audit.Log(...)` to silently swallow errors. This is intentional (non-blocking audit) but undocumented. | Silent discard of audit errors | Add a comment explaining why audit errors are best-effort; optionally log the audit failure at warn level | **Low** — Future maintainers may not realize the contract | **S** |

## 5. Testing Practices

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Testing | **Critical** | Domain error test coverage mismatches | `internal/file/service_test.go` line 45, 257, 771; `internal/comment/service_test.go` line 93, 222, 388, 504, 636 | Tests for the new sub-packages use `domain.ErrNotFound` but the corresponding service layer checks for `repository.ErrNotFound`. These tests pass because mocks are set up to return the right error, but they test the wrong error path. | Tests return `domain.ErrNotFound` while production code checks `repository.ErrNotFound` | Align test mocks with whatever sentinel error the service layer actually checks for | **High** — Tests provide false confidence: they pass but would not catch real error-handling bugs | **M** |
| Testing | High | Overly large test files | `internal/service/file_test.go` (742 lines), `internal/service/comment_test.go` (601 lines), `internal/service/message_test.go` (607 lines), `internal/file/service_test.go` (875 lines) | Test files are extremely large, making them hard to navigate, understand, and maintain. They test all aspects of a service in a single file. | Monolithic test files | Split test files by concern (e.g., `file_create_test.go`, `file_share_test.go`, `file_delete_test.go`) | **Medium** — Large test files reduce developer productivity and test readability | **M** |
| Testing | Medium | Mock file exceeds 500-line limit | `internal/service/mocks/mocks.go` (639 lines) | The mock file violates the AGENTS.md file size constraint of ≤500 lines for non-test, non-generated files. | 639 lines in a single file | Split mocks by domain into separate files (e.g., `file_mocks.go`, `message_mocks.go`, `comment_mocks.go`) | **Medium** — Violates hard-engineering constraint from AGENTS.md | **M** |
| Testing | Medium | No HTTP handler tests for health/timeout edge cases | `internal/handler/http/health_test.go` | The health handler test exists but is minimal (59 lines). No tests cover timeout context cancellation, concurrent health checks, or degraded-mode behaviour. | Minimal health endpoint coverage | Add tests for context cancellation, timeout propagation, and degraded mode response headers | **Medium** — Health endpoints are critical for production orchestration | **M** |

## 6. Technical Debt

| Category | Severity | Title | Location | Description | Current State | Recommended State | Impact | Effort |
|----------|----------|-------|----------|-------------|---------------|-------------------|--------|--------|
| Technical Debt | **Critical** | Abandoned sub-package refactoring | `internal/file/`, `internal/comment/`, `internal/message/`, `internal/notification/`, `internal/block/`, `internal/search/`, `internal/audit/` | A large-scale refactoring effort was started to split the monolith `internal/service/` into domain-specific sub-packages, but was never completed and never wired into the composition root. The old and new implementations coexist with slightly different error handling and conventions. | 7 dead packages with ~7,000 lines of code | Either: (a) Complete the refactoring by wiring the new packages into `internal/app/new.go` and deleting the old `internal/service/` implementations; or (b) Delete the unused sub-packages entirely. Either way, eliminate the split-brain. | **High** — Confusing architecture, duplicated maintenance surface, risk of divergent bug fixes | **XL** |
| Technical Debt | High | Domain error consistency | `internal/domain/errors.go` + `internal/repository/db.go` | Two families of sentinel errors (`domain.ErrNotFound` vs `repository.ErrNotFound`) for the same condition. The old `internal/service/` checks `repository.ErrNotFound`; the new sub-packages return `domain.ErrNotFound`. | Dual error semantics | Unify on one sentinel error family. Remove the unused domain errors. | **High** — Potential for silent mis-identification of error types | **M** |
| Technical Debt | Medium | Several production files approach 400-line split threshold | `internal/service/comment.go` (481), `internal/comment/service.go` (470), `internal/repository/comment.go` (463), `internal/app/new.go` (461), `internal/service/notification.go` (437) | Per AGENTS.md: "File approaches 400 lines → plan a split". These files exceed or approach this threshold. | Files over/approaching the splitting threshold | Split large files as per AGENTS.md splitting strategy guidelines. For example, `internal/app/new.go` could split by concern (DB setup, SSO setup, transport wiring). | **Medium** — Large files increase cognitive load and merge conflict risk | **L** |

## 7. Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | < 10 (inferred from code quality) | < 10 | ✅ (no obvious violations) |
| Function length | < 50 lines (inferred) | < 50 lines | ✅ (no obvious violations) |
| File size (non-test, non-generated) | **639 lines** in `mocks/mocks.go` violates 500-line limit | ≤ 500 | ❌ (mocks file violates) |
| File size (test files) | 875, 742, 735, 639, 607, 601 lines | < 500 (suggested) | ⚠️ (large test files) |
| Code duplication | Parallel implementation of file/comment/message/block/notification/search logic in old + new packages | < 5% | ❌ (~7000 lines of duplicated logic) |
| Documentation coverage | Good package-level and public-method docstrings | > 70% | ✅ (well-documented overall) |

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| Dead-code sub-packages (7 packages, ~7K lines) | High | XL | P0 | Either complete the refactoring or delete dead code. This is the single biggest quality issue. |
| Dual sentinel error families (`domain.ErrNotFound` vs `repository.ErrNotFound`) | High | M | P0 | Inconsistency will cause real production bugs if new code paths are activated. |
| Mocks file exceeds 500-line hard limit | Medium | M | P1 | Violates AGENTS.md; must fix. |
| Large production files approaching 400 lines | Medium | L | P1 | 9 non-test files exceed or approach the refactoring trigger. |
| `domain.ErrNestingTooDeep` unused | Low | S | P2 | Minor dead code, easy to fix. |
| `newEvent` duplication (outbox.go vs message.go) | Low | S | P2 | Minor deduplication opportunity. |
| Silent audit error swallowing | Low | S | P2 | Add documentation comment. |
| No correlation IDs in background goroutines | Medium | M | P2 | Helpful for debugging production issues. |

## Final Summary

### Overall Code Quality: **Needs Work**

The architecture is well-conceived (strict layering, domain-driven, good documentation), but the execution has a critical structural problem and several accumulating quality issues.

### Critical Quality Issues (Must Fix Before Production)

1. **Abandoned sub-package refactoring**: `internal/file/`, `internal/comment/`, `internal/message/`, `internal/notification/`, `internal/block/`, `internal/search/`, `internal/audit/` are all dead code — never wired into the composition root. This is a binary decision: either complete the migration or delete them all. The worst possible outcome is leaving them as-is, which creates maintenance drag, confusion, and risk of divergent bug fixes.

2. **Dual sentinel error families**: `domain.ErrNotFound` / `domain.ErrDuplicate` vs `repository.ErrNotFound` / `repository.ErrDuplicate` — the old service layer checks for the `repository` variants, while the new sub-packages return `domain` variants. If the new sub-packages are ever activated, error handling silently breaks.

3. **Mock file violates hard limit**: At 639 lines, `internal/service/mocks/mocks.go` exceeds the 500-line hard constraint from AGENTS.md.

### Maintainability Concerns

- **9 production files** are approaching or exceeding the 400-line refactoring trigger from AGENTS.md. The refactoring strategy already exists in the project docs — it needs to be executed.
- **Test files are monolithic**: Several test files exceed 600 lines, making them hard to navigate and maintain. Tests should be split by concern.
- **`internal/repository/db.go`** is a growing god file at 463 lines containing row types and converters for every domain entity. This should be split into per-domain files.

### Technical Debt Summary

| Category | Items |
|----------|-------|
| **P0** | Dead sub-packages (finish or delete) + Dual error families (unify) |
| **P1** | Mocks file size violation + Large production files (split on threshold) + Overly large test files |
| **P2** | Dead domain errors + newEvent duplication + Silent error handling + Background context propagation |

### Quick Wins (Easy Improvements)

1. Update `internal/service/mocks/mocks.go` to split into domain-specific mock files (e.g., `file_mocks.go`, `message_mocks.go`)
2. Remove the unused `domain.ErrNestingTooDeep`, `domain.ErrNotFound`, `domain.ErrDuplicate` sentinel errors once error unification is decided
3. Replace the private `newEvent` in `internal/service/message.go` with `outbox.NewEvent`
4. Add doc comments for the audit-log silent error pattern and the `nolint:nilnil` in redis.go
