Now I have a thorough understanding of the codebase. Here is my comprehensive Staff Engineer review.

---

# Code Review: aero-id — Staff Engineer Assessment

## Executive Summary

**Overall Code Quality: Needs Work**

The codebase shows strong engineering intent — clean layering, proper error handling patterns, good configuration management, and well-structured middleware. However, it suffers from a fundamental architectural problem: **two parallel implementations coexist** (old monolithic vs. new module-per-domain). The new modules are 90% dead code, creating confusion, duplication, and latent bugs. Crucially, a **context key collision** between `domain.WithActor` and `service.WithActor` means the new module services would silently fail if ever wired into the auth path.

---

## Critical Findings

### Finding 1: Dual Architecture — Massive Dead Code (CRITICAL)

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | **Critical** |
| **Title** | Two parallel architectural patterns coexist; new modules are unwired dead code |
| **Location** | Entire `internal/{block,comment,file,message,notification,search,audit}/` packages |
| **Description** | The codebase has TWO complete parallel implementations of the same business domains. The **old monolithic** pattern (`internal/service/` + `internal/repository/` + `internal/handler/{http,grpc}/`) is wired in `app.go`. The **new self-contained modules** (`internal/block/`, `internal/comment/`, `internal/file/`, `internal/message/`, `internal/notification/`, `internal/search/`, `internal/audit/`) each define their own `Service`, `Repository`, and `Handler` types, but **none** of these are imported or wired by the composition root (`internal/app/new.go`). |
| **Current State** | 7 complete modules (~5,000+ lines of Go) existing in parallel but never wired. They compile (no compiler warnings) so CI never catches them. Only `internal/notification/` imports `internal/block.BlockChecker` (an interface), and `internal/search/` imports `internal/comment` and `internal/file` for type aliases. The full service/repo/handler implementations are dead. |
| **Recommended State** | Either (a) wire the new modules and delete the old monolithic ones, or (b) delete the new module implementations and keep only interfaces/type aliases used by cross-cutting consumers. Carrying both creates a 2x maintenance burden for every change. |
| **Impact** | ~2× maintenance burden. Every bug fix or feature must consider both implementations. New developers are confused about which pattern to follow. Dead code rots and creates false signals in coverage reports. |
| **Effort** | L — requires careful migration plan (see TASK-001–036 in impl plan) |

### Finding 2: Context Key Collision — `domain.WithActor` vs `service.WithActor` (CRITICAL)

| Field | Value |
|-------|-------|
| **Category** | Error Handling / Quality |
| **Severity** | **Critical** |
| **Title** | Duplicate context key types create an invisible bug surface |
| **Location** | `internal/domain/actor.go` and `internal/service/service.go` |
| **Description** | Both files define their own `type ctxKey int` / `const actorKey ctxKey = iota`. Since they're in different packages, these are **different key types**. The auth middleware (`internal/middleware/auth.go:38`) calls `service.WithActor(ctx, uid)` to set the actor, but the new module services (e.g., `internal/comment/service.go:87`, `internal/block/block.go:52`) call `domain.MustActor(ctx)` to retrieve it. These use incompatible context keys — `domain.MustActor()` will **always return empty** when the actor was set via `service.WithActor()`. |
| **Current State** | This bug is **latent** because the new modules aren't wired (Finding 1). If someone wires `comment.NewService()` into `app.go`, every authenticated request immediately fails with `"missing authenticated actor"` Unauthorized. |
| **Recommended State** | Consolidate to a single `WithActor`/`ActorFrom`/`MustActor` in `internal/domain/actor.go`. Delete the duplicate in `internal/service/service.go`. Update all callers (middleware, old handlers, old services) to use the single `domain.*` version. |
| **Code Example** | **Current** (auth middleware sets via service key, new module reads via domain key): `middleware/auth.go: c.Request = c.Request.WithContext(service.WithActor(c.Request.Context(), uid))` vs `comment/service.go: actor, err := domain.MustActor(ctx)` — **different keys!** |
| **Impact** | When new modules are activated, ALL authenticated requests break with no compile-time warning. |
| **Effort** | S — search and replace, 2 files to modify, all consumers to update |

---

## High Severity Findings

### Finding 3: File Size Limit Violation — `mocks/mocks.go` (639 lines)

| Field | Value |
|-------|-------|
| **Category** | Quality / Testing |
| **Severity** | **High** |
| **Title** | Manual mock file exceeds 500-line hard limit specified in AGENTS.md |
| **Location** | `internal/service/mocks/mocks.go:639 lines` |
| **Description** | AGENTS.md mandates "File size (non-test, non-generated) ≤ 500 lines" enforced by `make check-file-sizes`. This file is 139 lines over the limit. |
| **Recommended State** | Split into domain-specific mock files (e.g., `mocks/file.go`, `mocks/message.go`, `mocks/comment.go`) or switch to a mock generator (mockgen, moq) to reduce maintenance burden. |
| **Impact** | Blocks CI gate. Makes the mock file hard to navigate — 639 lines of struct-method boilerplate. |
| **Effort** | M |

### Finding 4: 10 Files Approaching 500-Line Limit

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | **High** |
| **Title** | Multiple files approaching or exceeding the 400-line refactoring trigger |
| **Location** | 10 files (see list below) |
| **Description** | AGENTS.md specifies "File approaches 400 lines → plan a split; File exceeds 500 lines → stop and split now." The following files are at risk: |
| **At-Risk Files** | `internal/service/comment.go` (481), `internal/comment/service.go` (470), `internal/repository/comment.go` (463), `internal/app/new.go` (461), `internal/service/notification.go` (437), `internal/repository/message.go` (423), `internal/service/message.go` (417), `internal/comment/repository.go` (411), `internal/message/repository.go` (403), `internal/handler/grpc/server.go` (371) |
| **Recommended State** | Extract feature-specific code into sibling files (e.g., `service/comment_reactions.go` for the like/unlike path, `app/cleaners.go` for cleanup goroutine wiring). |
| **Impact** | Approaching the hard CI gate. Large files reduce maintainability and reviewability. |
| **Effort** | M |

### Finding 5: Zero Test Coverage in Critical Packages

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | **High** |
| **Title** | `internal/app`, `pkg/redis`, and `pkg/db` have 0% test coverage |
| **Location** | `internal/app/` (0%), `internal/pkg/redis/` (0%), `internal/pkg/db/` (0%) |
| **Description** | The composition root (`app/`) has no tests, meaning wiring errors (e.g., passing a nil dependency, wrong constructor arguments) are only caught at runtime. The Redis client has no tests at all. The DB package has no tests. |
| **Recommended State** | Add at least smoke tests: `TestNew` with a test config (table-driven with missing keys, bad values, etc.) for `app`; unit tests for `redis.NewClient` parsing; `db.Open` connection-string tests. |
| **Impact** | Wiring regression safety net missing. Redis client code changes are untested. |
| **Effort** | M |

---

## Medium Severity Findings

### Finding 6: Domain Types Re-exported and Duplicated in New Modules

| Field | Value |
|-------|-------|
| **Category** | Organization / Quality |
| **Severity** | Medium |
| **Title** | New modules re-export domain types and duplicate domain validation logic |
| **Location** | `internal/comment/comment.go`, `internal/message/message.go`, `internal/file/file.go`, etc. |
| **Description** | New modules declare `type Comment = domain.Comment` (type alias) and then copy-paste domain functions (`ValidateNewComment`, `NewComment`) with identical logic. This creates two sources of truth for validation that can drift. |
| **Code Example** | `internal/domain/comment.go:40-46` defines `ValidateNewComment`. `internal/comment/comment.go:35-42` defines an identical `ValidateNewComment`. If the rule changes (e.g., to 3-level nesting), both must be updated. |
| **Recommended State** | Keep domain validation in `domain/` only. New modules should call `domain.ValidateNewComment`. Remove duplicated functions from new modules. |
| **Impact** | Validation logic can drift. New developers don't know which version is authoritative. |
| **Effort** | S |

### Finding 7: Mock Proliferation — Duplicated Test Doubles

| Field | Value |
|-------|-------|
| **Category** | Testing / Quality |
| **Severity** | Medium |
| **Title** | New module tests define their own mock structs instead of reusing shared mocks |
| **Location** | `internal/comment/service_test.go`, `internal/block/block_test.go`, `internal/message/service_test.go`, etc. |
| **Description** | Each new module's `_test.go` files define their own hand-rolled mock structs with function fields. For example, `internal/comment/service_test.go` defines `mockCommentRepo` with 17 method fields, while `internal/service/mocks/mocks.go` already defines comprehensive mocks for the same repositories. The new module mocks are never reused across tests, leading to N× maintenance of identical mock structs. |
| **Impact** | Repeated mock code increases total test maintenance. Adding a method to an interface requires updating mocks in every test file. ~4,000+ lines of mock boilerplate across the codebase. |
| **Recommended State** | Use a mock generator (mockgen, moq, or testify mock) or extract the per-module mocks into a shared `testutil` package within each module. |
| **Effort** | M |

### Finding 8: Repository Package Has 0% Direct Test Coverage

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | `internal/repository/` shows 0% coverage because integration tests are in `_test` packages |
| **Location** | `internal/repository/` |
| **Description** | The repository's unit test files are only in `repository_test` packages (integration tests), not in `repository` package (white-box tests). While integration tests exist, the CI `make test` target excludes them (build tag `integration`), so the package shows as 0% covered. |
| **Recommended State** | Add Go build-tag `//go:build !integration` for unit-testable repo logic (e.g., query building, pagination helpers). Move these into testable functions within the `repository` package. |
| **Impact** | Developers see 0% coverage and may mistakenly think the repos are untested. CI gates based on coverage thresholds would flag this. |
| **Effort** | S |

### Finding 9: `new.go` Is a God Function

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Medium |
| **Title** | `App.New()` is a single 461-line function with ~30 dependency constructions |
| **Location** | `internal/app/new.go:461 lines` |
| **Description** | The `New()` function constructs nearly every dependency in the system inline — DB, storage, SSO client, tracing, metrics, outbox relay, storage GC, SSE hub, rate limiter, idempotency store, all services, all handlers, both servers, all cleanups. This is a god function that violates the Single Responsibility Principle. |
| **Impact** | Cannot unit-test dependency graph construction. Changing the wiring of one domain risks breaking another. Constructor order matters but isn't enforced by the compiler. |
| **Recommended State** | Extract into builder functions/callbacks per subsystem: `buildDependencies()` → returns `*Dependencies`, `buildServices(deps)` → returns `*Services`, `buildHTTPServer(services)` → returns `*http.Server`. Each builder is independently testable. |
| **Effort** | L |

### Finding 10: `service/service.go` Interface Pollution

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Medium |
| **Title** | 15+ service interfaces defined in a single file |
| **Location** | `internal/service/service.go:283 lines` |
| **Description** | The file defines `AdminService`, `FileService`, `MessageService`, `NotificationService`, `BlockService`, `CommentService` interfaces, plus 15+ DTO types, plus the `WithActor`/`ActorFrom` helpers, plus shared utility functions (`mustActor`, `canReadFile`, `canReadMessage`, `enrichUsers`). |
| **Impact** | Single file is the "god header" for the entire service layer. Changing a DTO or adding a service interface recreates a large diff. |
| **Recommended State** | Split interfaces to per-domain files (`service/file.go`, `service/message.go`, etc.) alongside their implementations. Move DTOs to their respective domain or a `dto` subpackage. |
| **Effort** | M |

### Finding 11: Go 1.26 `unique` Package Not Used for Context Keys

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Low |
| **Title** | Context keys use legacy typed-int pattern instead of `unique.Make` |
| **Location** | `internal/domain/actor.go`, `internal/service/service.go` |
| **Description** | Go 1.26 introduced `unique.Make[T]` for canonicalizing context keys safely. The current pattern `type ctxKey int; const actorKey ctxKey = iota` works but the duplicate in two packages is harder to detect than with `unique.Make` which would prevent cross-package key collisions by construction. |
| **Impact** | Finding 2 (context key collision) would be trivially prevented. |
| **Effort** | S |

### Finding 12: `mocks/mocks.go` Has Unused Default Fallbacks

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Low |
| **Title** | Mock methods silently default to no-op behavior when Fn is nil |
| **Location** | `internal/service/mocks/mocks.go` |
| **Description** | Several mock methods (e.g., `FindByChecksum`, `CountByStorageKey`, `ListSoftDeletedBefore`, `HardDelete`) return defaults when the function field is nil. This is documented as "feature inactive" but can mask test bugs where a test forgets to set a required mock function and silently passes. |
| **Recommended State** | Either panic on unset required methods (fail-fast in tests), or use strict mock mode via testify/mock or mockgen. |
| **Impact** | Tests can silently pass with unexercised mock methods, reducing coverage validity. |
| **Effort** | S |

### Finding 13: Hardcoded Constants in Multiple Places

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Low |
| **Title** | Duplicate magic constants for reservation TTL and record TTL |
| **Location** | `internal/idempotency/store.go:22` and `internal/idempotency/store_redis.go:18` |
| **Description** | Both `reservationTTL` (2 min, GormStore) and `redisReservationTTL` (2 min, RedisStore) are independently defined with the same value and no shared reference. Similarly `defaultIdempotencyTTL` (24h) is defined in `middleware/idempotency.go` and `recordTTL` (24h) in `store_redis.go`. |
| **Recommended State** | Define shared constants in `idempotency/store.go` or a `pkg/idempotency` package. |
| **Impact** | Changing the TTL requires updating multiple files; they can drift. |
| **Effort** | S |

### Finding 14: `Makefile` Not Evaluated

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Low |
| **Title** | CI gates referenced in AGENTS.md not found in Makefile |
| **Location** | AGENTS.md references `make check-file-sizes` and `make lint-strict` |
| **Description** | These targets don't appear to exist in the Makefile (based on file inspection). If the AGENTS.md contract says they're enforced but the targets don't exist, the gate is aspirational. |
| **Recommended State** | Implement `make check-file-sizes` (e.g., `find . -name '*.go' | xargs wc -l | awk '$1>500'`) and `make lint-strict` (golangci-lint with funlen/cyclop) or update AGENTS.md to reflect reality. |
| **Impact** | Design intent vs. reality gap. Files grow past 500 lines without enforcement. |
| **Effort** | S |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **Cyclomatic complexity** | Not measured (gocyclo not installed) | < 10 | ❓ |
| **File size (non-test)** | 1 file violates (639 lines) | ≤ 500 | ❌ |
| **Files approaching limit** | 9 files between 400–500 lines | < 400 trigger | ⚠️ |
| **Test coverage (unit, non-integration)** | Ranges 0%–98.7% across packages | > 80% overall | ⚠️ |
| **Zero-coverage packages** | app, db, redis, repository (non-integration), aeropb, ssopb, mocks | — | ❌ |
| **Code duplication** | Dual architecture doubles ~5,000 lines | < 5% | ❌ |
| **Dead code** | ~5,000+ lines of unwired module implementations | 0 | ❌ |
| **Integration test build tags** | Present on all integration tests | Present | ✅ |
| **`go vet ./...`** | Clean | Clean | ✅ |
| **`go test ./...`** | All passing | All passing | ✅ |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| **Dual architecture (new modules unwired)** | Critical | L | **P0** | Must resolve before adding any new features — decide direction |
| **Context key collision (domain vs service)** | Critical | S | **P0** | Fix before wiring new modules; breaks auth silently |
| **`mocks/mocks.go` exceeds 500 lines** | High | M | **P1** | Blocks CI gate, split or generate mocks |
| **10 files approaching 500-line limit** | High | M | **P1** | Refactor progressively; start with the worst offenders |
| **Zero test coverage in `app/`, `pkg/redis/`, `pkg/db/`** | High | M | **P1** | Add smoke tests and unit tests |
| **`new.go` god function (461 lines)** | Medium | L | **P1** | Extract into subsystem builders |
| **`service/service.go` interface file (283 lines)** | Medium | M | **P2** | Split by domain |
| **Mock proliferation across new module tests** | Medium | M | **P2** | Adopt mockgen or shared test mocks |
| **Domain validation duplicated in new modules** | Medium | S | **P2** | Remove duplicated functions, call domain.* |
| **Duplicate magic constants (TTL values)** | Low | S | **P2** | Centralize in `idempotency/` |
| **Repository package shows 0% coverage** | Medium | S | **P3** | Add build-tag excluded unit tests |
| **Unenforced CI gates (make check-file-sizes)** | Low | S | **P3** | Either implement targets or update docs |

---

## Quick Wins (Easy Improvements)

1. **Fix context key collision** (~30 min) — Remove `service.WithActor`/`ActorFrom`, switch all callers to `domain.*`. Single `git grep` + sed.

2. **Split `mocks/mocks.go`** (~1 hour) — Split into 3–4 domain files (file_mocks.go, message_mocks.go, etc.) or run `mockgen` over the repository interfaces.

3. **Centralize idempotency TTL constants** (~15 min) — Move `reservationTTL` and `recordTTL` to `idempotency/store.go`, reference from `store_redis.go`.

4. **Delete duplicated domain functions from new modules** (~30 min) — Remove `ValidateNewComment`, `NewComment` from `internal/comment/comment.go`; same for message/file if found.

5. **Add Makefile targets** (~30 min) — Implement `check-file-sizes` and `lint-strict` as referenced in AGENTS.md, or update docs.

---

## Final Recommendations for Next Phase

Given the state of the codebase, I recommend **before implementing any new features (TASK-001–036):**

### Step 1 (P0): Resolve the architectural direction

The existence of 7 complete-but-unwired module packages is the single largest quality issue. You have two choices:

**Option A: Commit to the new module pattern (recommended)**
- Wire the new modules into `app.go`
- Fix the context key collision (Finding 2)
- Delete the old `internal/service/*.go`, `internal/repository/*.go`, `internal/handler/http/*.go`, `internal/handler/grpc/*.go` implementations (keep interfaces if needed)
- This is the TASK-001–036 path — the implementation plan already assumes this module structure

**Option B: Revert to monolithic only**
- Delete the new module packages (keep only `internal/block/` interfaces if needed by notification dispatcher)
- Keep the well-tested old pattern

I strongly recommend **Option A** — the module-per-domain pattern is cleaner, follows Go convention, and the implementation plan is already designed around it.

### Step 2 (P0): Fix context key collision

Before any new modules are wired, ensure `domain.WithActor`/`domain.ActorFrom` is the single source of truth, and the auth middleware uses it.

### Step 3 (P1): Refactor largest files

Split `new.go`, `service.go`, `mocks/mocks.go`, and the comment/message service/repository files that are approaching 500 lines.

### Step 4 (P1): Start implementing TASK-014 (Outbox FOR UPDATE SKIP LOCKED) + TASK-026~030 (Conversation aggregation)

Once the architectural direction is settled, these are the highest-value technical and product tasks respectively.

---

The codebase has strong bones — clean layering, proper config, good middleware patterns, all tests passing. The dual architecture is the main impediment to velocity. Once resolved, the team can move fast on the remaining 33 implementation tasks.
