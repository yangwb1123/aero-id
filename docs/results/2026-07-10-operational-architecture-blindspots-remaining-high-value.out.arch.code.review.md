Excellent. Now I have all the data I need for a comprehensive review. Here's the full engineering analysis:

---

# Staff Engineer Code Quality Review: aero-id

## Category 1: Code Organization — CRITICAL Issues

### Finding 1: Massive Dead Code — 5,529 Lines Across 31 Files (CRITICAL)

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | **Critical** |
| **Title** | 6 domain-oriented packages are completely dead code |
| **Location** | `internal/comment/`, `internal/message/`, `internal/file/`, `internal/block/`, `internal/notification/`, `internal/search/` — 31 non-test files, 24 test files |
| **Description** | These packages contain duplicate type aliases (`type Comment = domain.Comment`), duplicate `Service` interfaces, duplicate `Repository` interfaces, duplicate service implementations, duplicate HTTP/gRPC handlers, and duplicate GORM row structs + conversion functions. **Nothing wires these into the application.** The composition root (`internal/app/new.go`) exclusively uses `internal/service/*`, `internal/handler/http/*`, `internal/handler/grpc/*`, and `internal/repository/*`. |
| **Current State** | 6 fully-featured packages compiling and even passing tests, but none of their service/handler/repository code is ever called. The only active usage is `comment.CommentToPB()`, `message.MsgToPB()`, `file.FileToPB()` called by `internal/search/handler_grpc.go`, but even `internal/search/` itself is never imported. *Every line is dead.* |
| **Recommended State** | Remove all 6 packages entirely. Extract the 3 PB conversion functions (`CommentToPB`, `MsgToPB`, `FileToPB`) into `internal/handler/grpc/` alongside their live equivalents. Tests for the removed code should be archived. |
| **Impact** | **Maintainability:** A developer onboarding to add a feature (e.g., Group Messaging) must discover which of the TWO parallel architectures is live, wasting hours. **Code review:** Every PR must consider both copies. **Build time:** 5.5k lines of dead Go are still compiled and tested. **False confidence:** 24 tests passing on dead code inflates coverage metrics. |
| **Effort** | L (clean removal + refactoring live PB converters) |

### Finding 2: Architecture Duality — Two Competing Designs (CRITICAL)

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | **Critical** |
| **Title** | Two parallel architectural patterns co-exist |
| **Location** | Live: `internal/service/*` → `internal/repository/*` → GORM + `internal/handler/{http,grpc}/*`. Dead: `internal/{domain}/*` with embedded handler/service/repository. |
| **Description** | The codebase has two competing architectures: (A) a centralized service/repository/handler layer (AGENTS.md-conformant), and (B) domain-oriented vertical slices (never finished, never wired). This duality makes it unclear where new code should go, and any developer adding a feature risks implementing in the wrong layer. |
| **Current State** | Both exist, both compile, both have tests, but only (A) is wired. The composition root imports only (A). |
| **Recommended State** | Decision: commit to (A) — the centralized layer that is actually wired. Remove (B). If the team later wants domain-oriented packages, they should do a proper migration of *all* code at once, not leave a half-finished copy. |
| **Impact** | Confusion during onboarding, risk of adding features to the wrong layer, wasted CI cycles compiling dead code |
| **Effort** | L |

### Finding 3: Composition Root Approaching Burndown Limit (HIGH)

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | **High** |
| **Title** | `internal/app/new.go` is 461 lines — single function |
| **Location** | `/home/u1/aero-id/internal/app/new.go` |
| **Description** | The entire dependency graph is wired in a single `New()` function. This function opens the DB, constructs repositories, builds services, creates handlers, configures middleware, sets up Redis, rate limiters, GC sweeper, degradation state, both HTTP and gRPC servers — all in one function body. The AGENTS.md constraint says < 50 lines per function. |
| **Current State** | One 461-line function. |
| **Recommended State** | Split into builder methods: `buildRepositories()`, `buildServices()`, `buildHTTPServer()`, `buildGRPCServer()`, `buildBackgroundWorkers()`. Each returns its component. The `New()` function composes them. |
| **Impact** | Readability for onboarding; testability (e.g., test the HTTP server build without the DB); incremental cost as more components are added |
| **Effort** | M |

---

## Category 2: Naming & Documentation — MEDIUM Issues

### Finding 4: Duplicate Error Sentinels (MEDIUM)

| Field | Value |
|-------|-------|
| **Category** | Naming / Quality |
| **Severity** | **Medium** |
| **Title** | `domain.ErrNotFound` and `repository.ErrNotFound` are separate variables |
| **Location** | `internal/domain/errors.go:6`, `internal/repository/db.go:15` |
| **Description** | Two sentinel errors for "record not found" exist: `domain.ErrNotFound` and `repository.ErrNotFound`. The live repository returns `repository.ErrNotFound`; the domain-level sentinel is only used by dead code repos. The domain-level version is confusing — a new developer might check `domain.ErrNotFound` against a repo error and it would never match. |
| **Current State** | Two separate error variables with different string representations (`"domain: not found"` vs `"repository: not found"`). |
| **Recommended State** | Remove `domain.ErrNotFound` and `domain.ErrDuplicate` (they are dead after removing the 6 packages). The repository layer's error sentinels are the canonical ones. |
| **Impact** | Can cause silent runtime failures if someone checks for the wrong sentinel |
| **Effort** | S |

### Finding 5: Duplicate ACL Helpers (MEDIUM)

| Field | Value |
|-------|-------|
| **Category** | Naming / Quality |
| **Severity** | **Medium** |
| **Title** | `canReadFile` / `canReadMessage` duplicated across packages |
| **Location** | `internal/domain/access.go` (exported), `internal/service/service.go` (private), `internal/message/message.go` (private, dead code) |
| **Description** | The same ACL logic is written 3 times: as `domain.CanReadFile`/`domain.CanReadMessage` (exported), as private `canReadFile`/`canReadMessage` in the live service layer, and again in the dead message package. The live service code duplicates the domain exports for no reason. |
| **Current State** | Three copies; the live service layer ignores the domain exports and reimplements them. |
| **Recommended State** | Delete `canReadFile`/`canReadMessage` from the service layer. Have the service use `domain.CanReadFile`/`domain.CanReadMessage` instead. |
| **Impact** | Violates DRY; fixes to ACL logic must be applied in multiple places |
| **Effort** | S |

### Finding 6: `mustActor` / `ActorFrom` Duplicated in Domain and Service (MEDIUM)

| Field | Value |
|-------|-------|
| **Category** | Naming / Quality |
| **Severity** | **Medium** |
| **Title** | Actor context helpers duplicated |
| **Location** | `internal/domain/actor.go`, `internal/service/service.go`, `internal/message/message.go` (dead) |
| **Description** | `domain.ActorFrom`, `domain.MustActor`, `domain.WithActor` exist in the domain package, but the service layer also has its own `ActorFrom` and `mustActor`. The live service layer's `mustActor` duplicates `domain.MustActor` exactly. |
| **Current State** | Service layer reimplements what domain provides. |
| **Recommended State** | Remove `ActorFrom` and `mustActor` from `service/service.go`. Have all service code use `domain.ActorFrom`/`domain.MustActor`. |
| **Effort** | S |

---

## Category 3: Error Handling — GOOD

The error handling pattern is well-done:

- **`AppError`** with dual HTTP/gRPC code mapping is a clean pattern
- **Service layer** translates raw repo errors to `AppError` using `.Wrap()` — correct separation
- **Handler layer** renders errors without business logic — correct separation
- **Consistent error codes** with dot-notation (`comment.not_found`, `message.forbidden`)

**Minor concern:** `GRPCError()` in `internal/pkg/errors/errors.go` and `grpcError()` in `internal/handler/grpc/` are the same concept defined in two places. The handler wrapper delegates to `apperr.GRPCError()`, but the comment handler in the dead code has its own copy. After removing dead code, only one should remain.

---

## Category 4: Logging — GOOD

- Structured zap logger with JSON encoding
- Request IDs via middleware
- Tracing integration via OpenTelemetry
- Access log sampling
- Compile-time type-safe fields (`zap.String`, `zap.Error`)
- Sensitive data not leaked in log contexts

---

## Category 5: Testing Practices — MEDIUM Concerns

### Finding 7: Dead Code Tests Create False Confidence (HIGH)

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | **High** |
| **Title** | 24 test files covering dead code inflate perceived coverage |
| **Location** | `internal/comment/*_test.go`, `internal/message/*_test.go`, `internal/file/*_test.go`, `internal/block/*_test.go`, `internal/notification/*_test.go`, `internal/search/*_test.go` |
| **Description** | All tests pass, but they test code that is never executed in production. The `go test ./...` output shows `ok` for all dead code packages. A developer seeing "all tests pass" gets false confidence. |
| **Current State** | ~24 test files exercising dead code paths |
| **Recommended State** | Move these test files to an `_archive/` directory or delete them alongside the dead code. If the PB conversion functions are extracted, only keep tests for those. |
| **Impact** | False confidence, wasted CI cycles, confusion about what is actually exercised |
| **Effort** | S (alongside dead code removal) |

### Finding 8: No Unit Test Coverage Visibility (MEDIUM)

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | **Medium** |
| **Title** | No `-cover` flag in Makefile, no coverage thresholds |
| **Location** | Makefile (referenced in AGENTS.md) |
| **Description** | There is no `go test -coverprofile` or coverage threshold in the CI pipeline (or Makefile). The AGENTS.md specifies `>80%` target but there's no enforcement. |
| **Current State** | Tests run but coverage is not measured or reported. |
| **Recommended State** | Add `-coverprofile=coverage.out` to `make test`, enforce a minimum coverage percentage for live code, and add a CI gate. Exclude dead code packages from coverage computation. |
| **Impact** | Regressions in coverage go unnoticed; untested new code can be merged |
| **Effort** | M |

---

## Category 6: Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| 6 dead code packages (5,529 lines) | High | L | **P0** | Requires immediate cleanup before any new feature work |
| Composition root single 461-line function | Medium | M | P1 | Prevents testability of wiring logic |
| Duplicate ACL helpers (3 copies) | Low | S | P1 | Simple inline-to-domain refactor |
| Duplicate error sentinels (domain vs repo) | Low | S | P1 | Clean up after dead code removal |
| Duplicate `mustActor`/`ActorFrom` | Low | S | P1 | Service should reuse domain functions |
| 24 dead code tests inflating metrics | Low | S | P1 | Remove alongside dead code |
| No coverage thresholds | Medium | M | P2 | Add after dead code cleanup stabilizes |
| Files approaching 400-500 line limit (9 files) | Medium | M | P2 | Add linting + split before hitting limit |
| `GRPCError()` defined in two places | Low | S | P2 | Consolidate after dead code removal |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity (per function) | ~OK (no obvious violations) | < 10 | ✅ |
| Function length (< 50 lines) | `new.go:New()` is 461 lines ⚠️ | < 50 | ❌ (one violation) |
| File size (non-test, non-generated, ≤ 500) | 9 files > 400 lines, 0 > 500 | < 500 | ⚠️ (trending toward limit) |
| Test coverage (live code only, estimated) | ~60-70% (rough estimate) | > 80% | ⚠️ |
| Code duplication (dead code packages) | ~5.5k lines (31% fat) | < 5% | ❌ |
| Documentation coverage | Good README, AGENTS.md, code comments | > 70% | ✅ |

---

## Final Summary

| Dimension | Rating |
|-----------|--------|
| **Overall Code Quality** | **Needs Work** |
| **Critical Quality Issues** | **Must fix before adding any new features** — 5,529 lines of dead code, two competing architectures |
| **Maintainability Concerns** | Composition root at the limit; 9 files approaching the 500-line burndown; duplicated error sentinels and helpers |
| **Technical Debt** | Moderate. The dead code accounts for ~31% of the non-test Go surface. Once removed, the live codebase (~12k lines) is well-organized and follows good patterns |
| **Quick Wins** | Delete all 6 dead packages (S effort, P0), deduplicate ACL helpers (S effort), remove duplicate `mustActor`/`ActorFrom` (S effort) |

### Recommended Action Plan

1. **P0 — Remove dead code first** (before any Direction 1-5 implementation): Delete `internal/comment/`, `internal/message/`, `internal/file/`, `internal/block/`, `internal/notification/`, `internal/search/`. Extract PB conversion functions into `internal/handler/grpc/convert.go`. This eliminates the architecture confusion that would cripple any new feature work.

2. **P1 — Deduplicate and clean**: Remove duplicate `canReadFile`/`canReadMessage` from `internal/service/service.go` (use `domain.*`). Remove duplicate `ErrNotFound`/`ErrDuplicate` from domain. Have service use `domain.MustActor` instead of its own copy.

3. **P1 — Split composition root**: Break `new.go`'s 461-line `New()` into builder methods.

4. **P2 — Add quality gates**: Enforce `-coverprofile`, `golangci-lint` with `funlen` and `cyclop`, and the existing `make check-file-sizes` in CI.

**Bottom line:** The codebase has good bones (error handling, logging, testing patterns are solid) but suffers from an incomplete architectural migration that left 5.5k lines of dead code behind. **Clean that up first, then proceed with Direction 1 (Group Messaging).** The dead code will actively mislead any developer implementing new features — they will inevitably ask "which message service do I extend?"
