Now I have a complete picture. Let me compile the comprehensive Staff Engineer review.

---

# Staff Engineer Code Quality Review — aero-id

**Review Date:** 2026-07-12  
**Repository:** `github.com/aero/aero-id`  
**Reviewer:** Staff Engineer (automated analysis)

---

## Executive Summary

The aero-id codebase is the work of a skilled team that has invested heavily in architecture, testing, and operational concerns (tracing, metrics, rate limiting, graceful degradation, idempotency). The code is well-structured with clear layering, consistent error handling, and comprehensive tests. However, the codebase has one **critical architectural issue** — a dual-architecture migration that has left ~4,800 lines of dead/migrated code that's still compiled and tested but never wired into production. This represents significant technical debt and maintenance burden.

---

## Key Findings

| # | Category | Severity | Title | Location | Effort |
|---|----------|----------|-------|----------|--------|
| 1 | Organization | **Critical** | Dual architecture — two parallel implementations | `internal/{service,repository,handler}/` vs `internal/{comment,file,message,block,notification,search}/` | L |
| 2 | Organization | High | Dead code: 4,785 lines of un-wired vertical-slice modules | `internal/{comment,file,message,block,notification,search}/` except Repository interfaces | M |
| 3 | Technical Debt | High | File sizes approaching the 500-line split threshold | See Finding 3 | M |
| 4 | Testing | Medium | Tests exercising only dead code paths | `internal/comment/service_test.go`, etc. | M |
| 5 | Organization | Medium | Hand-crafted 639-line mock file instead of generated mocks | `internal/service/mocks/mocks.go` | M |
| 6 | Quality | Medium | Package-average complexity risk in gRPC server | `internal/handler/grpc/server.go` (371 lines) | S |
| 7 | Naming | Low | Inconsistent struct naming (old vs new modules) | `commentService` vs `serviceImpl` | S |
| 8 | Error Handling | Low | Duplicated error sentinel definitions | `internal/domain/errors.go` vs `internal/comment/comment.go` | S |
| 9 | Logging | Low | App-level logging not using structured context IDs | `internal/app/app.go` | S |
| 10 | Quality | Low | Metrics package has 3 mutexes with complex locking | `internal/pkg/metrics/metrics.go` (347 lines) | S |

---

## Detailed Findings

### Finding 1 [CRITICAL] — Dual Architecture / Half-Migrated Refactoring

| Field | Detail |
|-------|--------|
| **Category** | Code Organization |
| **Severity** | Critical |
| **Title** | Two complete parallel implementations — only one wired |
| **Location** | `internal/{service,repository,handler/grpc,handler/http}/` (old) vs `internal/{block,comment,file,message,notification,search}/` (new) |
| **Description** | The codebase has two complete, nearly identical implementations of the same business logic. The old 4-layer architecture is actively wired in the composition root (`internal/app/new.go`). The new vertical-slice modules contain complete handlers (HTTP + gRPC), services, repositories, and domain types — all unused in production. Only the Repository *interface definitions* from the new modules are used by the search package. |
| **Current State** | `internal/app/new.go` imports only from `internal/service/`, `internal/repository/`, and `internal/handler/{http,grpc}/`. The new vertical slices in `internal/{comment,file,message,block,notification,search}/` are compiled but never instantiated or wired. Their test suites exercise dead code. |
| **Recommended State** | Choose one architecture and remove the other. Either (a) complete the migration to vertical slices and decommission the old layered services/repositories/handlers, or (b) remove the un-wired vertical-slice modules. |
| **Impact** | ~4,785 lines of dead code; dual maintenance burden; every feature change must be made twice; confusing for new developers onboarding; wasted test effort. |
| **Effort** | L — requires architectural decision, careful migration, and comprehensive testing |

**Evidence:**
- `internal/app/new.go` uses `service.NewCommentService(...)` (old) — not `comment.NewService(...)`
- `internal/app/new.go` uses `httpapi.NewCommentHandler(commentSvc)` from `internal/handler/http/` — not `comment.NewCommentHandler(...)` from `internal/comment/handler_http.go`
- `grep -rn "internal/comment" internal/app/` returns no matches
- The new vertical-slice modules are only imported by: `internal/search/search.go` (for Repository interface types) and their own test files

### Finding 2 [HIGH] — Dead Code in Vertical-Slice Modules

| Field | Detail |
|-------|--------|
| **Category** | Code Organization |
| **Severity** | High |
| **Title** | Un-wired vertical-slice services, handlers, and repositories |
| **Location** | `internal/comment/service.go` (470L), `internal/comment/handler_http.go` (360L), `internal/comment/handler_grpc.go` (275L), `internal/comment/repository.go` (411L), and same pattern in `internal/{file,message,block,notification,search}/` |
| **Description** | Beyond the Repository interfaces (which are legitimately used as types), the complete service implementations, HTTP handlers, and gRPC handlers in the new modules are never instantiated. Each module's `NewService()`, `NewCommentHandler()`, `NewXxxServer()` functions are never called from production code. |
| **Impact** | Code continues to compile (no dead code detection in CI), tests pass but exercise dead code, maintenance regression risk if someone edits only the old or only the new path |
| **Effort** | M |

### Finding 3 [HIGH] — File Size Approaching Limits

| Field | Detail |
|-------|--------|
| **Category** | Quality / Technical Debt |
| **Severity** | High |
| **Title** | 7+ non-test files over 400 lines approaching the 500-line ceiling |
| **Location** | See table |
| **Description** | The AGENTS.md constraint states "File approaches 400 lines → plan a split" and "exceeds 500 lines → stop and split now". While no non-test, non-generated file currently exceeds 500 lines, several are uncomfortably close and should be refactored proactively. |

Files requiring attention:

| File | Lines | Split Needed? |
|------|-------|---------------|
| `internal/service/comment.go` | 481 | ⚠️ Plan split |
| `internal/comment/service.go` | 470 | ⚠️ Plan split |
| `internal/repository/comment.go` | 463 | ⚠️ Plan split |
| `internal/app/new.go` | 461 | ⚠️ Plan split |
| `internal/service/notification.go` | 437 | ⚠️ Plan split |
| `internal/repository/message.go` | 423 | ⚠️ Plan split |
| `internal/service/message.go` | 417 | ⚠️ Plan split |
| `internal/comment/repository.go` | 411 | ⚠️ Plan split |
| `internal/message/repository.go` | 403 | ⚠️ Plan split |

These files represent a significant refactoring surface area. Splitting them would be easier if the architecture duplication is resolved first (Finding 1).

**Recommendation:** The old `internal/service/comment.go` (481L) and new `internal/comment/service.go` (470L) together represent ~950 lines of near-identical code. Choosing one and removing the other would instantly resolve the file size issue for both packages.

### Finding 4 [MEDIUM] — Massive Hand-Crafted Mock File

| Field | Detail |
|-------|--------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | 639-line hand-crafted mock file instead of generated mocks |
| **Location** | `internal/service/mocks/mocks.go` |
| **Description** | The old service layer uses a single monolithic mock file containing hand-written mock types for every interface. This file is 639 lines, manually maintained, and error-prone. The new vertical-slice modules use inline mock types per test file (lighter weight). |
| **Current State** | Single `mocks.go` with 10+ mock types (FileRepo, MessageRepo, CommentRepo, UserDir, Storage, etc.), each with function fields for every interface method. Default nil-safety logic is baked in for optional methods. |
| **Recommended State** | Use `go-mockgen` or `mockery` for generated mocks; or adopt the inline pattern from the new modules. |
| **Impact** | Manual mock maintenance is error-prone and discourages interface changes; 639 lines of boilerplate |
| **Effort** | M |

### Finding 5 [MEDIUM] — Tests Exercising Dead Code

| Field | Detail |
|-------|--------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | New vertical-slice module tests exercise un-wired code paths |
| **Location** | `internal/comment/service_test.go` (735L), `internal/file/service_test.go` (875L), `internal/message/service_test.go` (298L), `internal/notification/service_test.go` (203L), `internal/block/block_test.go` (104L) |
| **Description** | The new modules have comprehensive tests, but they test code that never runs in production. The tests pass, but they test `comment.NewService()` which is never called — the production path uses `service.NewCommentService()`. |
| **Impact** | False sense of security; test coverage numbers are inflated; production changes to the old services may break without corresponding test failures |
| **Effort** | M |

### Finding 6 [LOW] — Inconsistent Naming Conventions

| Field | Detail |
|-------|--------|
| **Category** | Naming |
| **Severity** | Low |
| **Title** | Inconsistent struct and header response naming |
| **Location** | Various |
| **Description** | The old layered code uses `commentService`, `fileService` as struct names; the new vertical slices use `serviceImpl`. HTTP handler response types: the old package defines response types in the handler file; the new package defines identical response types in the package's `comment.go`/`file.go` files. Some helper functions are exported (new) vs unexported (old). |
| **Impact** | Minor confusion for developers working across both architectures |
| **Effort** | S |

### Finding 7 [LOW] — Sentinels Duplicated

| Field | Detail |
|-------|--------|
| **Category** | Error Handling |
| **Severity** | Low |
| **Title** | Error sentinels defined in two places |
| **Location** | `internal/domain/errors.go` vs `internal/comment/comment.go` |
| **Description** | `domain.ErrNotFound` and `domain.ErrDuplicate` are defined in domain. The new comment module also defines `ErrNestingTooDeep` in its own package rather than in domain where it could be reused. |
| **Impact** | Minor: the error is only relevant to the comment package currently, but if other modules need the same check, the sentinel isn't centrally discoverable |
| **Effort** | S |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **Cyclomatic complexity** (per function) | < 10 enforced by CI | < 10 | ✅ (linter-enforced) |
| **Function length** | < 50 lines enforced by CI | < 50 lines | ✅ (linter-enforced) |
| **File size (non-test, non-generated)** | Largest: 481 lines (`internal/service/comment.go`) | < 500 lines | ⚠️ (several approaching 400-line threshold) |
| **Code duplication** | **~35% estimated** (two parallel implementations) | < 5% | ❌ (critical issue) |
| **Test coverage** | ~70% estimated | > 80% | ⚠️ (tests exist but some cover dead code) |
| **Documentation coverage** | Good — most public APIs documented | > 70% | ✅ |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| Dual architecture (old layered + new vertical-slice) | High — 4,785 lines dead code, dual maintenance | L | **P0** | Must decide and clean up |
| File size refactoring (7+ files > 400 lines) | Medium — code health | M | P1 | Should be done after resolving dual architecture |
| Giant hand-crafted mock file (639 lines) | Medium — fragility | M | P1 | Switch to generated mocks |
| Tests on dead code (new module tests) | Medium — false confidence | M | P1 | Must fix after architecture decision |
| Metrics package complexity (3 mutexes, 347 lines) | Low — maintainability | S | P2 | Could be simplified |
| `internal/app/new.go` (461 lines) | Medium — difficult to reason about wiring | M | P2 | Extract smaller builder functions |

---

## Final Summary

### Overall Code Quality: **Needs Work** ⚠️

The codebase shows strong engineering discipline in many areas:
- ✅ **Exemplary error handling** — consistent `AppError` pattern with code, message, HTTP+gRPC mapping
- ✅ **Strong testing culture** — unit tests alongside code, integration tests with testcontainers  
- ✅ **Operational maturity** — tracing, metrics, rate limiting, idempotency, graceful degradation
- ✅ **Linter enforcement** — cyclomatic complexity, function length, file size limits
- ✅ **Clean architecture principles** — dependency direction, separation of concerns
- ✅ **Comprehensive logging** — structured zap logging with request IDs

However, the **critical architectural issue** overshadows all of these strengths.

### Critical Quality Issue (Must Fix)

**Dual architecture migration that was started but never completed.** The codebase has ~4,785 lines of un-wired vertical-slice modules that duplicate the old layered architecture. This is the single biggest quality issue and must be resolved before any other feature work:

1. **Decision required**: Complete the migration to vertical slices (remove old `internal/service/`, `internal/repository/`, old handlers) OR revert: remove the new un-wired modules.

2. **If completing the migration**: Wire the new services in `internal/app/new.go`, ensure all interfaces align, test the end-to-end path, then delete the old layered code and its tests.

3. **If reverting**: Remove all un-wired vertical-slice modules except their Repository interfaces (used by search) and their `ToPB` conversion functions (used by `internal/search/handler_grpc.go`).

### Maintainability Concerns

- The dual architecture means every feature change must consider both paths — highly error-prone
- Onboarding new developers is confusing when two identical implementations exist
- The 639-line manual mock file is brittle and discourages interface evolution
- Several non-test files are approaching the 500-line file size limit

### Quick Wins (Can fix immediately, independent of architecture decision)

1. **Move `internal/comment/comment.go:ErrNestingTooDeep`** to `internal/domain/` for consistency
2. **Extract `internal/search/handler_grpc.go`** ToPB conversions to avoid importing from both architectures
3. **Add `make check-file-sizes`** to CI pre-commit (if not already) to prevent any new files from exceeding 500 lines
4. **Add a linter rule or CI check** to detect unused packages (though Go compilation won't catch unused code that's still compiled)
5. **Normalize naming**: choose either `commentService` (old) or `serviceImpl` (new) pattern consistently
