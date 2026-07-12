Now I have a thorough understanding of the codebase. Let me compile the full review.

---

# Staff Engineer Code Review: aero-id

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | ~5 avg (no golangci-lint running) | < 10 | ⚠️ unchecked |
| Function length | Most < 50 lines | < 50 lines | ✅ compliant |
| File size (non-test) | All ≤ 481 lines | ≤ 500 lines | ✅ compliant |
| Test coverage (unit) | 36%–98% per package, ~55% avg | > 80% | ❌ |
| Code duplication | **HIGH** (dual implementations) | < 5% | ❌ |
| Documentation coverage | Good for exported APIs | > 70% | ✅ |

> **Note**: `make lint` runs only `go vet ./...` despite the `.golangci.yml` configuration. `golangci-lint` is not installed, so cyclomatic complexity, function length, and `staticcheck`/`gosec`/`errorlint` checks are never enforced.

---

## Critical Issues

### 1. 🔴 CRITICAL: Dual Implementation — Dead Code Migration Left Incomplete

| Category | Organization |
|---------|------------|
| **Severity** | **Critical** |
| **Title** | Two parallel service implementations exist; only one is wired |
| **Location** | `internal/file/`, `internal/message/`, `internal/comment/`, `internal/notification/`, `internal/block/` vs. `internal/service/` |
| **Description** | The codebase has been partially migrated from the `internal/service/` package to domain-specific packages (`internal/file/`, `internal/message/`, `internal/comment/`, etc.). Both define their OWN service interfaces, repository interfaces, and implementations — but only `internal/service/` is wired in the composition root (`internal/app/new.go`). The new packages compile but are completely unreachable in the running application. |
| **Current State** | `internal/app/new.go` wires `service.NewFileService`, `service.NewMessageService`, etc. (`internal/service/` package). The new `internal/file/service_crud.go`, `internal/message/service.go`, etc. define identical interfaces and implementations that are never used (except `internal/search/` which depends on the new domain package interfaces but is itself NOT wired into the app). |
| **Recommended State** | Either: (a) Complete the migration by rewiring `app/new.go` to use the new domain packages and delete the old `internal/service/` code, or (b) Abandon the new packages and delete them. The current "both exist" state is a maintenance liability. |
| **Impact** | New developers will be confused about which implementation to extend. Bug fixes must be applied in two places. Code size is nearly doubled for the affected domains. Dead code is compiled, tested, and maintained for no benefit. |
| **Effort** | L (full migration) or M (delete dead code) |

### 2. 🔴 CRITICAL: `internal/search/` Package Has Unused HTTP/gRPC Handlers

| Category | Organization |
|---------|------------|
| **Severity** | **Critical** |
| **Title** | `internal/search/` handlers are compiled but never registered |
| **Location** | `internal/search/handler_http.go`, `internal/search/handler_grpc.go` |
| **Description** | `internal/search/handler_http.go` creates an HTTP handler that would serve search requests via the new domain packages. `internal/search/handler_grpc.go` does the same for gRPC. Neither is wired in `internal/app/new.go`. The actual HTTP and gRPC search handlers live in `internal/handler/http/search.go` and `internal/handler/grpc/search.go` and use `service.SearchService` (the old package). |
| **Current State** | Unreachable handler code is compiled into the binary, increasing binary size and confusing readers who expect handlers to be registered from their package. |
| **Recommended State** | Remove the unregistered handlers from `internal/search/` or wire them in the composition root and remove the old ones. |
| **Impact** | Confusion, dead code, wasted compilation time, and risk of a future engineer wiring both, causing route conflicts. |
| **Effort** | S |

### 3. 🔴 CRITICAL: Repository Package Has Zero Unit Test Coverage

| Category | Testing |
|---------|--------|
| **Severity** | **Critical** |
| **Title** | Repository layer has no unit tests |
| **Location** | `internal/repository/` (all files) |
| **Description** | The repository package has 0% statement coverage. There are integration test files (`*_integration_test.go`) scattered in adjacent packages (e.g. `internal/repository/block_integration_test.go`, `internal/repository/file_test.go`) but these are marked with `//go:build integration` and require Docker. The core repository logic has NO unit tests. |
| **Current State** | No unit tests for any repository. Integration tests exist but require Docker infrastructure. |
| **Recommended State** | Add unit tests for repository methods using GORM fake/in-memory SQLite or a mock DB. At minimum, test the pagination logic, error mapping, and edge cases in each repository. |
| **Impact** | Repository regressions can only be caught by integration tests, making TDD impossible and CI slower. |
| **Effort** | L |

### 4. 🟠 HIGH: Cross-Package Duplication of Actor Functions

| Category | Organization |
|---------|------------|
| **Severity** | **High** |
| **Title** | `WithActor`, `ActorFrom`, `MustActor` duplicated across `domain` and `service` packages |
| **Location** | `internal/domain/actor.go` (lines 16-34) and `internal/service/service.go` (lines 21-31) |
| **Description** | Both `internal/domain/actor.go` and `internal/service/service.go` define their own versions of `WithActor`, `ActorFrom`, and equivalent context key types. The service package has an unexported `mustActor()` that calls its own `ActorFrom`. The domain package exports `MustActor`. Handlers and middleware use `service.WithActor`/`service.ActorFrom`, while the new domain-oriented code (`internal/search/`) uses `domain.MustActor`. |
| **Current State** | Two independent implementations of the same context-key actor pattern, with different callers using different versions. |
| **Recommended State** | Consolidate into a single location — either `domain` or a new `internal/pkg/auth` package. The `domain` package is the semantically correct home since the actor is a domain concept. |
| **Impact** | Duplication risk: if the actor extraction logic changes in one place but not the other, auth leaks could happen. |
| **Effort** | S |

### 5. 🟠 HIGH: Duplicate ACL Functions

| Category | Quality |
|---------|--------|
| **Severity** | **High** |
| **Title** | `CanReadFile`/`CanReadMessage` defined in both `domain` and `service` |
| **Location** | `internal/domain/access.go` (lines 3-10) and `internal/service/service.go` (lines 128-135) |
| **Description** | The domain package exports `CanReadFile` and `CanReadMessage`. The service package defines identical unexported `canReadFile` and `canReadMessage` helper functions. Both are identical in logic but the service versions are duplicated and used internally by service implementations. |
| **Current State** | Two copies of the same ACL logic. Changes to the access model must be made in two places. |
| **Recommended State** | Remove the unexported helpers from `service/service.go` and have the service implementations call the domain functions directly. |
| **Impact** | If a future team adds a visibility level (e.g. "friends-only") but only updates one copy, access control will be inconsistent. |
| **Effort** | S |

### 6. 🟠 HIGH: Missing golangci-lint Gate Enforcement

| Category | Quality |
|---------|--------|
| **Severity** | **High** |
| **Title** | `make lint` only runs `go vet`; strict linters are never enforced |
| **Location** | `Makefile` (line 13: `lint: ; go vet ./...`) |
| **Description** | The `.golangci.yml` configuration enables `cyclop` (complexity), `funlen` (function length), `gosec` (security), `staticcheck`, `errorlint`, `bodyclose`, `nilerr`, `noctx`, and `unused`. However, `make lint` only runs `go vet ./...`. The `lint-strict` target exists but requires installing `golangci-lint`, and it's not run by CI. AGENTS.md references `make lint-strict` but the actual `lint` gate is weak. |
| **Current State** | No cyclomatic complexity, function length, or security checks are actually enforced. Code may violate the published constraints. |
| **Recommended State** | Install `golangci-lint` in CI, add `lint-strict` to the pre-merge gate, and update `make lint` to run the full suite (or at minimum add `staticcheck` and `gosec`). Fix any existing violations. |
| **Impact** | Engineering constraints (`funlen`, `cyclop`) documented in AGENTS.md are aspirational, not enforced. Code can silently degrade in quality. |
| **Effort** | M |

---

## Medium Findings

### 7. 🟡 MEDIUM: `internal/app/new.go` — 461 Lines, Approaching Functional Decomposition Limits

| Category | Quality |
|---------|--------|
| **Severity** | **Medium** |
| **Title** | Composition root near file size limit |
| **Location** | `internal/app/new.go` (461 lines) |
| **Description** | The composition root is 461 lines and responsible for: opening DB connections, building all repositories, building all services, configuring all middleware, constructing HTTP and gRPC servers, wiring idempotency, SSO client creation, outbox relay setup, GC sweeper startup, data retention cleaners, rate limiter setup, tracing init, and metrics. As features are added, this file will quickly exceed the 500-line limit. |
| **Current State** | One monolithic `New` function constructs the entire dependency graph. |
| **Recommended State** | Split into focused builders: `buildStorage()`, `buildSSO()`, `buildServices()`, `buildTransport()`, `buildBackgroundJobs()` following the existing but limited `buildStorage()` pattern. |
| **Impact** | Maintainability: changing one feature's wiring requires touching this massive file. Merge conflicts are likely. |
| **Effort** | M |

### 8. 🟡 MEDIUM: Mock File at 639 Lines — Approaching the 500-Line Limit (Exempted)

| Category | Testing |
|---------|--------|
| **Severity** | **Medium** |
| **Title** | Mock file should be split by domain |
| **Location** | `internal/service/mocks/mocks.go` (639 lines) |
| **Description** | The mock file contains test doubles for 12 interfaces in a single file: `FileRepo`, `FileShareRepo`, `MessageRepo`, `CommentRepo`, `NotificationRepo`, `BlockRepo`, `Storage`, `UserDir`, `Pusher`, `Auditor`, `PreferenceRepo`. This file is exempt from the 500-line check because it's a test helper, but it's still problematic. |
| **Current State** | All mocks in one monolithic file. |
| **Recommended State** | Split by domain: `mocks/file.go`, `mocks/message.go`, `mocks/comment.go`, `mocks/notification.go`, `mocks/storage.go`. |
| **Impact** | When adding a new repository method, developers must find the right spot in the 639-line file. Merge conflicts on the mock file block CI. |
| **Effort** | M |

### 9. 🟡 MEDIUM: Large Test Files (875, 742, 735 lines)

| Category | Testing |
|---------|--------|
| **Severity** | **Medium** |
| **Title** | Several test files exceed maintainable size |
| **Location** | `internal/file/service_test.go` (875), `internal/service/file_test.go` (742), `internal/comment/service_test.go` (735) |
| **Description** | While AGENTS.md exempts test files from size limits, file sizes >500 lines indicate poor test organization. Tests should be split by concern (e.g. `service_create_test.go`, `service_list_test.go`, `service_delete_test.go`). |
| **Current State** | Monolithic test files with hundreds of lines each. |
| **Recommended State** | Split each large test file into focused files matching the source package's split pattern (e.g. `service_crud_test.go`, `service_share_test.go`, `service_upload_test.go`). |
| **Impact** | Poor readability, harder to find relevant tests, longer CI feedback on failures. |
| **Effort** | M |

### 10. 🟡 MEDIUM: `GRPCError` in `internal/pkg/errors` Returns Error Without Stack Trace

| Category | Error Handling |
|---------|--------------|
| **Severity** | **Medium** |
| **Title** | gRPC error conversion discards diagnostic information |
| **Location** | `internal/pkg/errors/errors.go` (line 72: `GRPCError`) |
| **Description** | `GRPCError` converts `*AppError` to a gRPC `status.Error`, keeping only the `Message` field. The `Code` (machine-readable error code like `file.not_found`) and `Cause` (wrapped error chain) are discarded. Downstream clients see only a human-readable message and lose the structured error code for programmatic handling. |
| **Current State** | `GRPCError` returns `status.Error(ae.GRPC, ae.Message)` — only the message. |
| **Recommended State** | Include the error code in gRPC response details (e.g. via `status.New` + `details` with a `ErrorDetail` protobuf message), or at minimum prefix the message with the code. |
| **Impact** | gRPC clients cannot programmatically distinguish "file not found" from "user not found" — they must parse human-readable strings. |
| **Effort** | S |

### 11. 🟡 MEDIUM: Test Coverage Below 50% for Repository, Storage, Audit, Message Packages

| Category | Testing |
|---------|--------|
| **Severity** | **Medium** |
| **Title** | Several packages have inadequate test coverage |
| **Locations** | `internal/repository` (0%), `internal/storage` (25%), `internal/audit` (7%), `internal/message` (37%), `internal/idempotency` (38%), `internal/pkg/errors` (37%) |
| **Description** | These packages are critical infrastructure: repositories handle all database access; storage handles file persistence; audit tracks compliance-sensitive events. The low coverage means regressions in these areas are likely to go undetected by unit tests. |
| **Current State** | Repository: 0% unit test coverage. Storage: 25% (only tests local backend). |
| **Recommended State** | Target 80%+ coverage on all packages. Add tests for S3 storage backend, repository error mapping, edge cases (empty results, cursors, not-found). |
| **Impact** | Regressions on DB queries, file access, and audit logging can reach production unnoticed. |
| **Effort** | L |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| Dual implementation: old service vs. new domain packages | High | L | **P0** | Critical architectural debt — complete or abort the migration |
| Repository package has 0 unit test coverage | High | L | **P0** | Must fix for reliability; integration tests alone are insufficient |
| `internal/search/` has unregistered handlers | Medium | S | P1 | Dead code that compiles into the binary |
| Actor functions duplicated in `domain` and `service` | Medium | S | P1 | Consolidate to `domain` |
| ACL functions duplicated in `domain` and `service` | Medium | S | P1 | Service should call domain functions |
| golangci-lint not enforced in CI | Medium | M | P1 | Engineering constraints are aspirational |
| Monolithic composition root (461 lines) | Medium | M | P1 | Split builder functions |
| Mock file at 639 lines | Low | M | P2 | Split by domain |
| Large test files (875 lines) | Low | M | P2 | Split by concern |
| `GRPCError` discards error code | Low | S | P2 | gRPC clients lose structured errors |
| Low test coverage in 5+ packages | Medium | L | P1 | Target 80% |
| Claude worktree artifacts in `.claude/` directory | Low | S | P2 | 10+ worktrees should be cleaned up |
| Migration files not auto-applied (design choice) | Low | — | — | Documented in AGENTS.md |
| `domain/errors.go` only has `ErrNotFound` and `ErrDuplicate` | Low | S | P3 | Consider richer domain error types |

---

## Final Summary

### Overall Code Quality: **Needs Work**

The codebase demonstrates strong engineering fundamentals in many areas:

- **Architecture**: Clean layering (handler → service → repository), clear separation of concerns, proper error types, good use of dependency injection
- **Documentation**: Excellent package-level godoc comments, clear interface contracts, well-documented DTOs
- **Error handling**: Good `AppError` pattern with HTTP + gRPC dual mapping, proper wrapping
- **Testing patterns**: Smart default-behavior test mocks, good test structure where tests exist
- **Configuration**: Well-structured config with env var overriding

However, the codebase has **one critical architectural problem** that undermines everything: the **incomplete migration from the monolithic `service/` package to domain-oriented packages** (`internal/file/`, `internal/message/`, `internal/comment/`, etc.) has left the codebase in an incoherent state where:

1. **Two parallel service implementations exist**, each with their own interfaces, repository interfaces, and implementations
2. **Only the old implementation is wired in production** — the new domain packages are dead code
3. **Actor/ACL functions are duplicated** across packages
4. **The repository layer has zero unit test coverage** — only Docker-dependent integration tests exist
5. **Engineering constraints are aspirational** — `golangci-lint` with `cyclop`/`funlen`/`gosec` is never actually run

### Critical Quality Issues (Must Fix Before Production)

1. **🔴 Complete or abort the domain-package migration** — choose one architecture and delete the dead code
2. **🔴 Add unit tests for the repository layer** — the data access layer has 0% coverage
3. **🔴 Fix the `internal/search` dead handler problem** — either wire or delete

### Maintainability Concerns

- The dual-implementation technical debt will compound over time: every new feature must decide which service to extend, and bug fixes must be applied to both
- The composition root is approaching its size limit and will need splitting
- Test files are too large and should be split by concern

### Quick Wins (S Effort)

1. ✂️ Delete unregistered handlers from `internal/search/`
2. 🗑️ Consolidate actor functions — keep `domain/actor.go`, remove `service/service.go` duplicates
3. 🔧 Consolidate ACL functions — have service call `domain.CanReadFile`/`CanReadMessage`
4. 📝 Include error codes in gRPC error responses
5. 🧹 Clean up `.claude/` worktree artifacts (10+ worktrees)

### Recommendation

**Freeze feature work.** This is a critical point in the project lifecycle. The codebase grew rapidly through an AI-assisted pipeline (evidenced by the 100+ `.arch.md` and `.impl-plan.md` files in `docs/results/`), and the migration from `service/` to domain packages was started but never completed. The team should:

1. Decide whether to complete the migration to domain packages or revert to the monolithic `service/` package
2. Execute the chosen direction
3. Install and enforce `golangci-lint` in CI
4. Add repository-level unit tests
5. Only then resume feature development
