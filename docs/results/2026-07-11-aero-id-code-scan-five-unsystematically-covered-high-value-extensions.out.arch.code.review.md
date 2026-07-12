Now I have a thorough understanding. Let me compile my comprehensive review.

---

# Staff Engineer Code Quality Review: `aero-id`

## Findings

### Finding F1 — CRITICAL — Dual Dead Code Architecture (Incomplete Refactoring)

| Field | Value |
|-------|-------|
| **Category** | Organization / Technical Debt |
| **Severity** | Critical |
| **Title** | Two competing service architectures coexist — old monolith is wired, new domain packages are dead code |
| **Location** | `internal/app/new.go` (wiring), `internal/service/*` (old), `internal/{comment,file,message,notification}/*` (new) |
| **Description** | The codebase contains a complete refactored architecture with domain-driven service packages (`internal/comment/`, `internal/file/`, `internal/message/`, `internal/notification/`), each with their own Repository/Service interfaces, implementations, HTTP handlers, and gRPC servers. However, the composition root (`internal/app/new.go`) wires **only** the old monolith services from `internal/service/`. The new packages compile but are completely unused dead code. |
| **Current State** | `internal/app/new.go:208-222` creates services exclusively from `service.NewFileService`, `service.NewMessageService`, `service.NewCommentService`, `service.NewNotificationService` (old monolith). The new `comment.NewService()`, `file.NewService()`, `message.NewService()`, `notification.NewService()` are never called anywhere. Similarly, `internal/search/search.go` references the new domain repository interfaces (`comment.Repository`, `file.Repository`, `message.MessageRepository`) but the wiring uses the old `service.NewSearchService` which references old `repository.*Repository` interfaces. |
| **Recommended State** | Either remove the new dead domain packages to reduce confusion, or finish the migration: wire the new services, remove the old monolith, and delete `internal/service/` entirely. |
| **Impact** | Massive maintenance burden: any developer touching the codebase must understand two architectures. Bug fixes must be applied in two places. New features risk being added to the wrong layer. Onboarding new engineers is confusing — they see both architectures and can't tell which is "real." |
| **Effort** | L (2-3 sprints to complete migration) |

### Finding F2 — CRITICAL — Context Key Collision Between Domain and Service Packages

| Field | Value |
|-------|-------|
| **Category** | Quality / Error Handling |
| **Severity** | Critical |
| **Title** | `domain.ActorFrom` and `service.ActorFrom` use different unexported context keys, making them incompatible |
| **Location** | `internal/domain/actor.go:6-22`, `internal/service/service.go:15-28` |
| **Description** | Both `internal/domain/actor.go` and `internal/service/service.go` define unexported `type ctxKey int` with `const actorKey ctxKey = iota`. Because they're unexported types in different packages, they are **different keys** in `context.WithValue`. The HTTP and gRPC middleware use `service.WithActor(ctx, uid)` to set the actor, but the new domain-driven services (`internal/comment/service.go`, `internal/file/service_crud.go`, etc.) call `domain.MustActor(ctx)` to read it. These can never match — `domain.MustActor` will always return "missing authenticated actor." |
| **Current State** | ```
// internal/service/service.go
type ctxKey int
const actorKey ctxKey = iota

// internal/domain/actor.go  
type ctxKey int
const actorKey ctxKey = iota  // DIFFERENT type!
``` |
| **Recommended State** | Consolidate actor context into ONE canonical location. The cleanest approach is to keep it in `internal/domain/actor.go` (since domain is the lowest layer with zero dependencies) and have `internal/service/service.go` delegate to `domain.ActorFrom`/`domain.WithActor`. Update the middleware (`internal/middleware/auth.go` line 38 and `internal/handler/grpc/server.go` line 251) to use `domain.WithActor`. |
| **Code Example** | **Current (broken if new services were wired):** HTTP middleware sets actor via `service.WithActor(ctx, uid)` → `/v1/comments` handler calls `service.CommentService.Create()` which calls `domain.MustActor(ctx)` → **always fails** with unauthorized because context keys don't match. |
| | **After fix:** HTTP middleware uses `domain.WithActor(ctx, uid)`. Domain service uses `domain.MustActor(ctx)`. Both use the same key. |
| **Impact** | If anyone tries to wire the new domain services today, every authenticated endpoint would return 401 Unauthorized. This is a latent production bug waiting to happen during the next refactoring attempt. |
| **Effort** | S (single consolidated location, then update 4-5 call sites) |

### Finding F3 — HIGH — Massive Code Duplication Between Old and New Service Layers

| Field | Value |
|-------|-------|
| **Category** | Quality / Technical Debt |
| **Severity** | High |
| **Title** | Business logic, DTOs, and helper functions duplicated across old and new packages |
| **Location** | `internal/service/*` vs `internal/{comment,file,message,notification}/*` |
| **Description** | The same business logic exists in two places. For example: `internal/service/comment.go` (481 lines) AND `internal/comment/service.go` (470 lines) both implement comment CRUD, nesting validation, reaction logic, report resolution, etc. The DTOs (`CreateCommentInput`, `CommentList`, `ReactionResult`), helper functions (`authorUIDs`, `enrichUsers`, `mustActor`/`mustActor`), and error handling patterns are copy-pasted. |
| **Current State** | Four domain packages each have ~400-500 lines of duplicated logic. |
| **Recommended State** | Finish the migration: delete `internal/service/*` and wire the new domain packages. This removes ~3000 lines of dead/duplicated code. |
| **Impact** | Bug fixes must be applied twice. If a developer fixes something in the old monolith but the new packages are ever wired, the bug remains. Code reviews become confusing as reviewers must check both copies. |
| **Effort** | L |

### Finding F4 — HIGH — Abandoned Refactoring (Unused Handlers/Servers in Domain Packages)

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | High |
| **Title** | HTTP handlers and gRPC servers in new domain packages are never registered |
| **Location** | `internal/{comment,file,message,notification}/handler_http.go` and `handler_grpc.go` |
| **Description** | The new domain packages each have complete HTTP handlers (e.g., `internal/comment/handler_http.go`, 360 lines) and gRPC servers (e.g., `internal/comment/handler_grpc.go`, 275 lines) that are never instantiated or registered. A search for `comment.NewCommentHandler` yields zero results outside test files. The wiring uses the OLD handlers from `internal/handler/http/` and `internal/handler/grpc/`. |
| **Current State** | ~2200 lines of complete, compilable, but unused handler code exists in `internal/{comment,file,message,notification}/handler_http.go` and `handler_grpc.go`. |
| **Recommended State** | Remove unused files, or wire them and delete the old `internal/handler/http/*` and `internal/handler/grpc/*` equivalents. |
| **Impact** | Dead code increases compilation time, confuses IDE navigation (two handlers with the same route pattern), and creates uncertainty: "Is this the real handler or the old one?" |
| **Effort** | M |

### Finding F5 — MEDIUM — Files Approaching the 500-Line Limit

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | Medium |
| **Title** | Multiple files within 10-15% of the 500-line maximum |
| **Location** | `internal/repository/comment.go` (463), `internal/repository/message.go` (423), `internal/service/comment.go` (481), `internal/app/new.go` (461), `internal/comment/service.go` (470), `internal/service/notification.go` (437), `internal/repository/comment.go` (463) |
| **Description** | Per AGENTS.md, when a file approaches 400 lines, a split should be planned. At 450+, the refactoring should happen now. Several files are at or beyond 450 lines. The 500-line hard limit is enforced by CI, but these files represent a maintenance risk — adding any new feature to these files would trigger the limit. |
| **Current State** | 5 files are > 430 lines, with `internal/service/comment.go` at 481 (19 lines from the limit). |
| **Recommended State** | Split these files before adding any new features. For example: `internal/repository/comment.go` → `repository_comment.go`, `repository_comment_report.go`, `repository_comment_search.go`. |
| **Impact** | Imminent CI failure on next meaningful addition. High coupling — all comment repository methods in one file makes changes risky. |
| **Effort** | M (split 6 files, ~1-2 days) |

### Finding F6 — MEDIUM — Auth Middleware Imports Service Package

| Field | Value |
|-------|-------|
| **Category** | Organization / Dependencies |
| **Severity** | Medium |
| **Title** | `middleware/auth.go` imports `internal/service`, creating a broad dependency |
| **Location** | `internal/middleware/auth.go:5-10` |
| **Description** | The auth middleware calls `service.WithActor(ctx, uid)` to set the authenticated actor. This creates a dependency from middleware → service, which is an unusual direction. Middleware should be a cross-cutting concern that depends on low-level packages, not on the business logic layer. |
| **Current State** | `import "...internal/service"` in `internal/middleware/auth.go:9` |
| **Recommended State** | Move `WithActor`/`ActorFrom` into `internal/domain/actor.go` (already exists there!) and have middleware use `domain.WithActor`. This removes the middleware → service dependency entirely. |
| **Impact** | Breaks the layering: middleware imports a high-level package (service) for a simple context helper. Service package changes could trigger middleware recompilation. |
| **Effort** | S (change 1 import and ~5 lines) |

### Finding F7 — MEDIUM — gRPC Rate Limit Interceptor Duplicates Context Key Logic

| Field | Value |
|-------|-------|
| **Category** | Quality / Code Duplication |
| **Severity** | Medium |
| **Title** | `handleAuthInterceptor` in `internal/handler/grpc/server.go` duplicates context actor setting |
| **Location** | `internal/handler/grpc/server.go:251` and `internal/middleware/auth.go:38` |
| **Description** | Both the HTTP auth middleware (`internal/middleware/auth.go`) and the gRPC auth interceptor (`internal/handler/grpc/server.go`) manually call `service.WithActor(ctx, uid)` and set `c.Set("actor_uid", uid)` (HTTP specific). This is duplicated logic. |
| **Current State** | Two separate auth implementations that both manually inject the actor uid into context. |
| **Recommended State** | Consider sharing the bearer token extraction and actor injection via a common utility, or at minimum ensure both use the same `domain.WithActor` (after fixing F2). |
| **Impact** | Low duplication, but if the actor injection logic changes, both places must be updated. |
| **Effort** | S |

### Finding F8 — LOW — `internal/service/service.go` Contains DTO Definitions Also Present in Domain Packages

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Low |
| **Title** | DTO types defined twice — in `internal/service/service.go` AND in domain packages |
| **Location** | `internal/service/service.go` vs `internal/{comment,file,message,notification}/*.go` |
| **Description** | Types like `CreateFileInput`, `FileList`, `UpdateFileMetadataInput`, `PresignUploadInput`, `SendMessageInput`, `MessageList`, `CreateCommentInput`, `CommentList`, `ReactionResult`, etc. are defined in BOTH `internal/service/service.go` AND the corresponding new domain package files. |
| **Recommended State** | Delete one copy after completing the migration (see F1). |
| **Impact** | Low, since only one copy is actually used. But it's confusing and IDE auto-complete shows duplicates. |
| **Effort** | S (delete after migration) |

### Finding F9 — LOW — Search Package Has Two Competing Implementations

| Field | Value |
|-------|-------|
| **Category** | Quality / Duplication |
| **Severity** | Low |
| **Title** | `internal/search/search.go` and `internal/service/search.go` are parallel implementations |
| **Location** | `internal/search/search.go` (100 lines) vs `internal/service/search.go` (168 lines) |
| **Description** | The new `internal/search/search.go` is simpler and delegates to the new domain repository interfaces. The old `internal/service/search.go` is more feature-rich (CJK fallback, timeouts, snippet handling). Only the old one is wired. |
| **Recommended State** | Merge features, then delete the old one. The CJK fallback and timeout logic in the old service is valuable and should be ported to the new implementation before deletion. |
| **Impact** | Low — works today. But the more feature-rich implementation is in the layer being migrated away from. |
| **Effort** | M |

### Finding F10 — LOW — Inconsistent Error Code Naming Conventions

| Field | Value |
|-------|-------|
| **Category** | Naming |
| **Severity** | Low |
| **Title** | Error codes use dot-separated names like `"file.not_found"` — inconsistent with `"auth.unauthorized"` and `"comment.invalid"` |
| **Location** | Multiple error constructors across `internal/service/*`, `internal/{comment,file,message}/*` |
| **Description** | Error codes follow `<domain>.<reason>` pattern inconsistently. For example: `"file.not_found"`, `"comment.not_found"`, `"auth.unauthorized"`, `"comment.invalid"`, `"file.invalid"`. Some use past tense (`not_found`), others use adjectives (`invalid`), others use verbs (`unauthorized`). There's no governance on error code taxonomy. |
| **Recommended State** | Define a standard taxonomy: `<domain>.<noun>.<state>` (e.g., `file.not_found`, `auth.token.missing`, `comment.nesting.too_deep`). Review and standardize all error codes. |
| **Impact** | API consumers (frontend, other services) must handle inconsistent error codes. Client-side error handling logic becomes fragile. |
| **Effort** | M |

### Finding F11 — LOW — Widespread `pkg/idgen` Package Only Exists for One Type Alias

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Low |
| **Title** | `internal/pkg/idgen` is a single 18-line file aliasing `uuid.UUID` — but `internal/domain/shared.go` also aliases it |
| **Location** | `internal/pkg/idgen/idgen.go`, `internal/domain/shared.go:5` |
| **Description** | `idgen.ID` is just a type alias for `uuid.UUID` with `NewID()` and `Parse()` helpers. However, `internal/domain/shared.go` re-aliases it: `type ID = idgen.ID`. This creates an unnecessary indirection: every consumer imports `idgen` to use `idgen.ID`, `idgen.NewID()`, `idgen.Parse()`. Similarly, `internal/pkg/idgen/` exists as its own package for just 3 functions. |
| **Recommended State** | Consider moving the ID type into `internal/types` or `internal/domain` directly. Remove the intermediary `internal/pkg/idgen` package. Or keep it — it's minor. |
| **Impact** | Every file needs 3 imports for a simple type. |
| **Effort** | S |

### Finding F12 — INFO — Test Coverage Quality

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | Informational |
| **Title** | Good test quantity (103 test files), but uneven distribution |
| **Location** | Overall |
| **Description** | There are 103 `_test.go` files for 148 non-test files. The old monolith `internal/service/` has dedicated test files (`service/service_test.go`, `service/comment_test.go`, etc.). The new domain packages also have test files (`comment/service_test.go`, `file/service_test.go`, etc.). However, the new domain package tests test dead code (see F1). Many key files lack tests: `internal/middleware/degradation.go` (362 lines), `internal/pkg/metrics/metrics.go` (347 lines), `internal/storage/gc/sweeper.go`. |
| **Recommended State** | After resolving the architectural duplication, ensure the live services have >80% coverage. Add tests for middleware and storage. |
| **Impact** | Degradation detection (362 lines) and metrics (347 lines) are critical infrastructure with no tests. |
| **Effort** | L |

---

## Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | < 10 (per golangci-lint config) | < 10 | ✅ |
| Function length | < 50 lines | < 50 lines | ✅ |
| File size (non-test) | 5 files > 430 lines (up to 481) | ≤ 500 lines | ⚠️ **Near limit** |
| Code duplication | **HIGH** — two complete service layers | < 5% | ❌ **Massive duplication** |
| Test coverage | ~60-70% (estimated) | > 80% | ⚠️ Uneven — untested critical paths |
| Documentation coverage | Good README/AGENTS.md, some inline | > 70% | ✅ |
| Dead code | ~5000-6000 lines in unused packages | 0 | ❌ |
| Architecture compliance | Single-direction dependencies hold | strict | ⚠️ middleware → service breaks layering |
| Build + Vet | Clean | Clean | ✅ |

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| Dead domain packages (F1) | Critical — confuses devs, doubles bug surface | L | **P0** | Must decide: finish migration or revert |
| Context key collision (F2) | Critical — latent production bug | S | **P0** | Fix before anyone wires new services |
| Code duplication (F3) | High — 3000+ duplicated lines | L | P1 | Consequence of F1 decision |
| Files near 500-line limit (F5) | High — imminent CI failure | M | P1 | Split before adding features |
| Unused handlers (F4) | Medium — dead code, confusion | M | P1 | Remove or wire |
| Large repo files without tests (misc) | Medium | M | P2 | Add tests for degradation, metrics, search |
| Inconsistent error codes (F10) | Low | M | P2 | Taxonomy doc + refactor |
| `idgen` as separate package (F11) | Low | S | P3 | Trivial consolidation |
| DTO duplication (F8) | Low | S | P3 | Delete after migration |

---

## Summary

### Overall Code Quality: **Needs Work**

The codebase has strong fundamentals: clean builds and vet, consistent error handling patterns, good README and AGENTS.md documentation, proper dependency layering in the working architecture, and a good test file count. The domain entities are well-modeled and the storage/SSO abstractions are sound.

However, the codebase is in a **dangerous half-refactored state** that undermines all of its strengths:

### Critical Quality Issues (Must Fix Before Production)

1. **Dual architecture with dead code**: The most urgent problem. Two complete, equivalent service layers exist. Only one is wired, but both are maintained. Every bug fix, feature addition, and refactor requires the developer to understand both architectures and apply changes to potentially both. This is unsustainable.

2. **Context key collision between `domain.ActorFrom` and `service.ActorFrom`**: If anyone attempts to wire the new domain services without realizing the context keys differ, all authenticated requests will fail. This is a ticking time bomb.

### Maintainability Concerns

- **Refactoring debt**: The migration from monolith to domain-driven packages appears to have been started but abandoned mid-way. The decision to keep old code alongside new "for safety" has created a dangerous situation where the old code is the "truth" but the new code is where future development is expected.
- **File size**: 6+ files within 10% of the 500-line hard limit create constant CI pressure.
- **Dead code confusion**: ~5000 lines of compilable but unused code waste developer attention.

### Technical Debt Roadmap

1. **Immediate (next sprint)**: Fix the context key collision (F2) — consolidate into `domain.actor.go`. Run `make check-file-sizes` and split any file > 460 lines (F5).
2. **Next quarter**: Decide the architecture path — either complete the migration to domain-driven packages or rip them out and stay with the monolith. Either choice is better than the current limbo.
3. **Ongoing**: Add tests for untested critical paths (degradation, metrics, sweeper). Standardize error codes.

### Quick Wins

| Fix | Effort | Impact |
|-----|--------|--------|
| Consolidate `WithActor`/`ActorFrom` into `domain` package | S | Eliminates latent production bug, removes middleware→service dependency |
| Run `check-file-sizes` proactively and split the 4 most bloated files | M | Prevents CI failures, improves maintainability |
| Delete unused handler files in domain packages (after confirming they're dead) | S | Removes ~2200 lines of dead code, reduces confusion |
| Fix import in `internal/middleware/ratelimit.go` — uses `service.ActorFrom` instead of `domain.ActorFrom` | S | Consistent actor access pattern |

The codebase has good bones, but the incomplete refactoring is creating more debt than it's eliminating. A clear architectural decision and cleanup sprint would pay enormous dividends for team velocity and code confidence.
