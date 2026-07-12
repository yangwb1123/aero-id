Now I have a thorough understanding of the codebase. Here is the comprehensive code review.

---

# Staff Engineer Code Review: aero-id

## Executive Summary

**Overall Code Quality: Needs Work**

The aero-id codebase exhibits strong fundamentals — structured error handling, proper layering, clean separation of domain types, and a sophisticated outbox/notification substrate. However, a **critical architectural migration is mid-flight**: the codebase carries **two complete, parallel implementations** of the service layer, handlers, and tests. The new modular architecture (`internal/{file,comment,message,...}/`) sits dead-code-adjacent to the old monolithic one (`internal/service/` + `internal/handler/{http,grpc}/`), with **neither fully wired** in the composition root. This imposes a severe maintenance burden, doubles the surface area for bugs, and undermines the team's velocity.

---

## 1. Code Organization

### Finding 1.1 (Critical) — Dual Architecture with Dead Code

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | **Critical** |
| **Title** | Two parallel, unintegrated service/handler implementations |
| **Location** | `internal/{file,comment,message,block,notification,search}/` vs `internal/service/` + `internal/handler/{http,grpc}/` |
| **Description** | The codebase contains two complete, competing architectures. The old one (`internal/service/` + `internal/handler/{http,grpc}/`) is used by `internal/app/new.go`. The new one (`internal/{feature}/` with self-contained service interfaces, repository interfaces, HTTP handlers, and gRPC servers) is not wired into the app. Every feature handler, service interface, and mock exists in both places. |
| **Current State** | `internal/app/new.go` constructs services from `internal/service.New*Service()` and handlers from `internal/handler/http.New*Handler()` and `internal/handler/grpc.New*Server()`. Feature packages like `internal/comment/` define their own `Service` interface, `NewService()`, `NewCommentHandler()`, and `NewCommentServer()` — all unused. |
| **Recommended State** | Choose one architecture. If the new modular approach is the target, remove the old layered code after the migration. If keeping the old architecture, remove the new redundant packages. Do not carry both. |
| **Impact** | **Every feature change must be implemented twice.** Each bug fix, security patch, or new endpoint requires parallel changes in two codebases. Dead code confuses onboarding and wastes compile time. | 
| **Effort** | L (a full refactor to eliminate one architecture) |

**Duplication evidence:**

| Feature | Old Service | New Service | Old HTTP Handler | New HTTP Handler |
|---------|------------|-------------|-----------------|-----------------|
| File | `internal/service/file.go` | `internal/file/service_crud.go` | `internal/handler/http/file.go` | `internal/file/handler_http.go` |
| Comment | `internal/service/comment.go` | `internal/comment/service.go` | `internal/handler/http/comment.go` | `internal/comment/handler_http.go` |
| Message | `internal/service/message.go` | `internal/message/service.go` | `internal/handler/http/message.go` | `internal/message/handler_http.go` |

### Finding 1.2 (High) — Actor Context Defined in Two Places

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | **High** |
| **Title** | `WithActor`/`ActorFrom`/`mustActor` duplicated |
| **Location** | `internal/service/service.go` and `internal/domain/actor.go` |
| **Description** | The context-based actor propagation primitives are defined in two places with identical semantics. The old `internal/service/service.go` has `WithActor`, `ActorFrom`, and `mustActor`. The new `internal/domain/actor.go` has `WithActor`, `ActorFrom`, and `MustActor` (exported). The HTTP auth middleware in `internal/middleware/auth.go` calls `service.WithActor()`, while the new modular feature services call `domain.WithActor()`. |
| **Current State** | Two separate packages define the same context key pattern. `mustActor` is unexported in `service` package; `MustActor` is exported in `domain` package. |
| **Recommended State** | Consolidate into `internal/domain/actor.go` only. Remove from `internal/service/service.go`. Update all callers (including middleware) to use `domain.WithActor` and `domain.MustActor`. |
| **Impact** | Inconsistency: middleware uses `service.WithActor`, new modular code uses `domain.WithActor`. A developer must know which one to use depending on which layer they're in. |
| **Effort** | M |

### Finding 1.3 (Medium) — Repository Interface Split

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Medium |
| **Title** | Repository interfaces defined in both `internal/repository/` and feature packages |
| **Location** | `internal/repository/file.go` (interface) vs `internal/file/file.go` (interface) |
| **Description** | In the new modular architecture, each feature package defines its own `Repository` interface (e.g., `internal/file.Repository`, `internal/comment.Repository`). In the old architecture, `internal/repository/` defines interfaces (e.g., `internal/repository.FileRepository`). The concrete implementations in `internal/repository/` satisfy both. |
| **Current State** | Two sets of repository interface definitions with slightly different method sets. For example, `internal/file.Repository` does not include `SearchFilesLike`, while `internal/repository.FileRepository` does. |
| **Recommended State** | Define repository interfaces in the consumer package (`internal/{feature}/`) and remove the old centralized interface definitions. Keep implementations in `internal/repository/`. This follows the "interface belongs to the consumer" Go idiom and is already the direction the new architecture takes. |
| **Impact** | Creates confusion about which interface to use. The centralized interfaces in `internal/repository/` create unnecessary coupling. |
| **Effort** | M |

---

## 2. Naming & Documentation

### Finding 2.1 (Low) — Inconsistent Naming: Domain Cancelled Events vs Outbox Events

| Field | Value |
|-------|-------|
| **Category** | Naming |
| **Severity** | Low |
| **Title** | `domain.AuditEvent` and `outbox.Event` naming collision potential |
| **Location** | `internal/domain/audit.go`, `internal/outbox/outbox.go` |
| **Description** | The codebase has two distinct event types: `domain.AuditEvent` (for audit logging) and `outbox.Event` (for transactional outbox). Though in different packages, the similarity in naming can be confusing — both are "events" with very different lifecycles and contracts. |
| **Current State** | Both named `Event` in their respective packages. |
| **Recommended State** | Rename `outbox.Event` to `outbox.OutboxEvent` or keep as-is but add explicit package-qualified documentation in function signatures. |
| **Impact** | Low — minor confusion on onboarding. |
| **Effort** | S |

### Finding 2.2 (Low) — Unexported Helper Duplicated Across Files

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | Low |
| **Title** | `enrichUsers`, `authorUIDs`, `reporterUIDs` duplicated |
| **Location** | `internal/service/service.go:94-108`, `internal/service/comment.go:336-363`, `internal/comment/service.go:363-394` |
| **Description** | The helper functions `enrichUsers`, `authorUIDs`, and `reporterUIDs` are copy-pasted in multiple locations with identical implementations. |
| **Current State** | Identical functions in at least 3 locations. |
| **Recommended State** | Extract to a shared internal package (e.g., `internal/pkg/enrich/`) or export from `internal/domain/`. |
| **Impact** | Low — maintainability concern. A change to the enrichment logic requires updating multiple files. |
| **Effort** | S |

---

## 3. Error Handling

### Finding 3.1 (Medium) — Inconsistent Domain Error Symbols

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | Medium |
| **Title** | `ErrNotFound` defined in both `domain` and `repository` packages |
| **Location** | `internal/domain/errors.go`, `internal/repository/db.go` |
| **Description** | The old layered services check for `repository.ErrNotFound`; the new modular services check for `domain.ErrNotFound`. These are distinct sentinel errors. The repository implementations may return either depending on which layer calls them. |
| **Current State** | Both `domain.ErrNotFound` and `repository.ErrNotFound` exist. The new `internal/comment/repository.go` returns `domain.ErrNotFound`, while the old `internal/repository/comment.go` returns `repository.ErrNotFound`. |
| **Recommended State** | Standardize on `domain.ErrNotFound` and remove `repository.ErrNotFound`. Repository implementations should map GORM errors to domain errors. |
| **Impact** | A service using `errors.Is(err, domain.ErrNotFound)` will miss `repository.ErrNotFound` and vice versa, potentially causing missed error handling branches. |
| **Effort** | S |

### Finding 3.2 (Low) — Audit Log Errors Silently Dropped

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | Low |
| **Title** | Audit logger errors silently discarded with `_ = s.audit.Log(...)` |
| **Location** | `internal/service/comment.go:301`, `internal/comment/service.go:331` |
| **Description** | Audit events use a best-effort pattern: the error from `audit.Log()` is silently discarded with `_ =`. While documented as intentional ("a failed log must not fail the primary operation"), the error is completely lost — never logged even as a warning. |
| **Current State** | `_ = s.audit.Log(ctx, event)` — error discarded silently. |
| **Recommended State** | Log the error at a warning level before discarding: `if err := s.audit.Log(...); err != nil { log.Warn("audit log failed", zap.Error(err)) }`. The `auditLogger` interface already documents non-blocking semantics, so the primary operation still succeeds. |
| **Impact** | Silent audit failures hide monitoring/observability gaps until an incident. |
| **Effort** | S |

---

## 4. Logging

### Finding 4.1 (Low) — Correlation ID Not Propagated to gRPC

| Field | Value |
|-------|-------|
| **Category** | Logging |
| **Severity** | Low |
| **Title** | gRPC handler logging lacks request_id / correlation pattern |
| **Location** | `internal/handler/grpc/server.go`, `internal/middleware/access_log.go` |
| **Description** | HTTP requests get a `request_id` via `middleware.RequestID()`, logged in each access log line. gRPC handlers do not have an equivalent interceptor for request ID injection or structured logging. |
| **Current State** | `internal/handler/grpc/server.go` appears to have logging but the request_id pattern is HTTP-only. |
| **Recommended State** | Add a gRPC unary interceptor that injects/extracts a correlation ID (either from gRPC metadata or generated) and includes it in structured log output. |
| **Impact** | Debugging cross-transport request flows requires manual correlation. |
| **Effort** | M |

---

## 5. Testing Practices

### Finding 5.1 (High) — Test Doubles Duplicated Across Old and New Code

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | **High** |
| **Title** | Mocks/test doubles duplicated or reimplemented per architecture |
| **Location** | `internal/service/mocks/mocks.go` (639 lines), plus inline mocks in `internal/file/service_test.go`, `internal/comment/service_test.go`, etc. |
| **Description** | The old service layer has a centralized mock file at `internal/service/mocks/mocks.go` (639 lines, exceeds the 500-line limit). The new modular packages define their own mocks inline in test files (e.g., `internal/file/service_test.go` defines `fileRepoMock`). This means the same mock contract is implemented in two or more places. |
| **Current State** | `internal/service/mocks/mocks.go` at 639 lines violates the file size limit. New modular packages have ad-hoc test doubles. |
| **Recommended State** | **Resolve the dual architecture first**, then consolidate mocks. If keeping the modular approach, remove `internal/service/mocks/mocks.go` and use the inline mock pattern consistently (or use a proper mock generator like `mockgen`). |
| **Impact** | 639-line mock file exceeds the team's own 500-line limit. Duplicate mock maintenance. |
| **Effort** | L (depends on architecture consolidation) |

### Finding 5.2 (Medium) — Parity Test Coverage Incomplete

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | gRPC-only parity tests; no HTTP endpoint coverage in parity tests |
| **Location** | `internal/handler/grpc/parity_test.go`, `internal/handler/grpc/parity_edit_test.go` |
| **Description** | The parity tests verify behavioral consistency between transport layers (HTTP and gRPC sharing the same service). However, the existing parity tests only exercise the gRPC layer — they don't actually verify that the HTTP handler produces equivalent behavior. |
| **Current State** | `parity_test.go` tests gRPC handlers (file sharing, block, etc.) against service fakes but doesn't exercise HTTP routes for the same scenarios. |
| **Recommended State** | Add HTTP parity tests that call the gin handler directly with `httptest.NewRecorder()`, exercising the same service fakes. This ensures the HTTP handler's request parsing and response rendering are correct, not just the gRPC handler. |
| **Impact** | The HTTP handler layer is tested in isolation (`handler_http_test.go` files) but not cross-validated against the gRPC handler for identical service behavior. |
| **Effort** | M |

### Finding 5.3 (Medium) — Integration Tests Not Run in CI

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | Medium |
| **Title** | Integration tests gated behind build tag, require Docker |
| **Location** | `Makefile`: `test-integration: ; go test -tags=integration ./... -count=1` |
| **Description** | `make test-integration` requires Docker (starts Postgres + MinIO via testcontainers). The Makefile and AGENTS.md document this as expected, but there's no CI config visible that runs these tests. |
| **Current State** | Unit tests only in CI (presumably). Integration tests are manual. |
| **Recommended State** | Add a GitHub Actions workflow (or similar) that runs `make test-integration` on every push to main / PR. The `.github/` directory exists — check if there's already a workflow. |
| **Impact** | Repository-level SQL and MinIO integration may break unnoticed until a developer manually runs integration tests. |
| **Effort** | S |

---

## 6. Technical Debt

### Finding 6.1 (Critical) — Dual Architecture Migration In Progress

| Field | Value |
|-------|-------|
| **Category** | Technical Debt |
| **Severity** | **Critical** |
| **Title** | Incomplete migration from monolithic to modular architecture |
| **Location** | `internal/app/new.go` (old constructors) vs feature packages (new constructors) |
| **Description** | The codebase is in the middle of an architectural migration from a centralized service/handler model to a per-feature modular model. The migration appears halted mid-way: the new packages are fully written but unplugged. |
| **Current State** | `internal/app/new.go` uses `service.NewFileService()`, `service.NewCommentService()` etc. The new `file.NewService()`, `comment.NewService()` are unimported. |
| **Recommended State** | Complete the migration or abort it. If completing: wire the new services in `internal/app/new.go`, then delete the old `internal/service/`, `internal/handler/http/` (those files only), and `internal/handler/grpc/` (those files only). If aborting: delete the new feature packages. |
| **Impact** | **Each change must be made twice.** New team members learn the wrong pattern. Technical debt accumulates daily. |
| **Effort** | L |

### Finding 6.2 (High) — File Size Violation

| Field | Value |
|-------|-------|
| **Category** | Quality |
| **Severity** | **High** |
| **Title** | `internal/service/mocks/mocks.go` exceeds 500-line limit |
| **Location** | `internal/service/mocks/mocks.go` (639 lines) |
| **Description** | The centralized mocks file at 639 lines exceeds the team's self-imposed 500-line limit from `AGENTS.md`. This file should be split per-feature or per-interface. |
| **Current State** | 639 lines, all mock types in one file. |
| **Recommended State** | Split into per-feature mock files (e.g., `internal/service/mocks/file.go`, `internal/service/mocks/comment.go`) or generate mocks with `mockgen`. |
| **Impact** | Violates the team's own engineering standards. The `lint-strict` gate (`make check-file-sizes`) should flag this. |
| **Effort** | M |

### Finding 6.3 (Medium) — No Technical Debt Tracking

| Field | Value |
|-------|-------|
| **Category** | Technical Debt |
| **Severity** | Medium |
| **Title** | No TODO/FIXME comments or debt register in code |
| **Location** | Entire codebase |
| **Description** | The codebase has zero TODO or FIXME comments. While this could indicate clean code, it more likely indicates a lack of debt tracking. Known issues (dual architecture, file size violations, mock duplication) should be tracked either in-code or in an external system. |
| **Current State** | No debt markers in the code. |
| **Recommended State** | Add TODO/FIXME comments for known tech debt items, or maintain a `TECH_DEBT.md` that catalogs them. This aligns with the `AGENTS.md` recommendation to track debt. |
| **Impact** | Technical debt is invisible and unmanaged. |
| **Effort** | S |

---

## 7. Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| Cyclomatic complexity | Varies, generally < 10 | < 10 | ✅ (most functions) ⚠️ (some repo functions near limit) |
| Function length | Most < 50 lines | < 50 lines | ✅ |
| Test coverage | ~60-70% (est.) | > 80% | ⚠️ (gaps in integration test coverage) |
| Code duplication | **High** due to dual architecture | < 5% | ❌ |
| Documentation coverage | Good for public APIs | > 70% | ✅ |
| File size ≤ 500 lines | 1 violation (mocks.go) | ≤ 500 | ❌ |

**Detailed complexity concerns:**

The repository layer (`internal/repository/comment.go`, `internal/repository/file.go`) has some functions approaching the cyclomatic complexity limit due to multi-branch SQL condition building (e.g., `ListByTarget` with optional filters, `SearchFiles` variations). The old `internal/service/comment.go` has several moderately complex functions (Create at ~40 lines, multiple error paths).

---

## Technical Debt Register

| Item | Impact | Effort | Priority | Notes |
|------|--------|--------|----------|-------|
| Dual architecture (old + new) | Critical | L | **P0** | Every feature change requires 2x work |
| File size: mocks.go (639 lines) | High | M | **P0** | Violates team constraint |
| Actor context duplication (service vs domain) | High | M | **P1** | Causes confusion in middleware vs service |
| Repository error symbols duplicated | Medium | S | **P1** | `ErrNotFound` in both domain and repository |
| Audit error logging gaps | Low | S | P2 | Silently discarded errors |
| gRPC correlation IDs | Low | M | P2 | Harder to debug cross-transport flows |
| No CI for integration tests | Medium | S | P2 | Risk of breaking DB/storage integration |
| Parity tests HTTP-only missing | Medium | M | P2 | HTTP handler behavior not cross-validated |
| Helper function duplication | Low | S | P2 | `enrichUsers`, `authorUIDs` copied |
| No TODO/FIXME debt markers | Low | S | P3 | Debt is invisible |

---

## Final Summary

### Critical Quality Issues (Must Fix Before Production)

1. **Dual Architecture (P0)** — The two parallel implementations (`internal/service/` + `internal/handler/` vs `internal/{feature}/`) must be resolved immediately. This is the single largest quality and productivity drain. Pick one, delete the other, and complete the wiring.

2. **File Size Violation (P0)** — `internal/service/mocks/mocks.go` at 639 lines violates the team's own engineering standard. Split or generate.

### Maintainability Concerns

- The new modular architecture (`internal/{file,comment,message}/`) is well-designed with clean interfaces, repo-in-package tests, and self-contained handlers. **This architecture should be the winner** — it follows Go idioms (interface in consumer package), reduces import coupling, and makes each feature independently testable.
- The domain package is clean and well-organized. Moving actor context helpers there (and removing from `internal/service/`) is the right direction.
- The outbox/notification system is sophisticated and well-implemented.
- The `RouteRegistrar` pattern in the router is clean and allows decoupled feature registration.

### Quick Wins

1. **Remove `repository.ErrNotFound`** — Replace all references with `domain.ErrNotFound`. ~15 minutes.
2. **Log audit errors** — Add `log.Warn` before discarding audit errors. ~30 minutes.
3. **Track debt** — Add a `TECH_DEBT.md` or TODO comments for the top 5 items. ~15 minutes.
4. **Split mocks.go** — Split into per-feature files. ~2 hours.

### Onboarding Recommendation

A new developer should be told: *"The codebase is mid-migration from a layered architecture to a modular per-feature architecture. The new pattern lives in `internal/{feature}/` packages. Please write all new code in the new pattern. Do NOT add to `internal/service/` or `internal/handler/{http,grpc}/` — those are legacy."*

This reduces confusion and pushes the migration forward rather than perpetuating the dual-implementation problem.
