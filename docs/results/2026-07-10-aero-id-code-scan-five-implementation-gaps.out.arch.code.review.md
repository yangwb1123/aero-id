Now I have a comprehensive picture of the codebase. Here's my full review.

---

# Staff Engineer Code Review: aero-id

## Executive Summary

This is an **actively developed** Go microservice that has undergone significant feature growth. The codebase shows solid engineering fundamentals: clean architecture layering, proper error handling patterns, good use of interfaces, and comprehensive infrastructure. However, **the project is in the middle of an incomplete architectural refactoring**, resulting in massive code duplication and several files approaching hard size limits. This is the single most critical issue that must be addressed before further feature work.

---

## 1. Code Organization

### Finding F-001: Massive Code Duplication — Dual Implementations

| Field | Value |
|-------|-------|
| **Category** | Organization |
| **Severity** | **Critical** |
| **Title** | Two complete, overlapping implementations exist side-by-side |
| **Location** | `internal/service/` (old) vs `internal/file/`, `internal/comment/`, `internal/message/`, `internal/block/`, `internal/notification/`, `internal/search/` (new) — and corresponding handler/repository packages |
| **Description** | The codebase has two parallel sets of service/handler/repository logic. The **old** monolithic layout (`internal/service/*.go`, `internal/repository/*.go`, `internal/handler/http/*.go`, `internal/handler/grpc/*.go`) totals **~18,000 lines** of production code. The **new** vertical-slice layout (`internal/file/`, `internal/comment/`, `internal/message/`, `internal/block/`, `internal/notification/`, `internal/search/`) adds a **further ~11,900 lines** of essentially duplicate logic. The composition root (`internal/app/new.go`) wires the **old** packages — the new packages are **dead code**, only consumed by `internal/search/` and `internal/notification/dispatcher.go`. |

**Current State:**
```go
// Composition root (internal/app/new.go) — wired with OLD packages:
fileSvc := service.NewFileService(fileRepo, store, cfg.Storage.Type, ...)
httpapi.NewFileHandler(fileSvc, downloader, presignTTL)
grpcsrv.NewFileServer(fileSvc)
```

**Dead code (new package, never wired):**
```go
// internal/file/service.go — NEW package, never instantiated by app
func NewService(files Repository, store storage.FileStorage, storageType string, opts ...Option) Service {
```

**Impact:**
- **Double the maintenance burden** — every bug fix, feature change, or API update must be made in both places (or is at risk of being missed in one)
- **Confusing onboarding** — new developers must determine which implementation is "real"
- **Risk of divergence** — the new packages may fall out of sync with evolving business logic

**Recommended State:**
1. **Declare the winning architecture**: Vertical-slice (domain-driven) or monolithic
2. **Delete the losing implementation** entirely
3. If vertical-slice is the target, complete the migration: wire the new packages in `app/new.go`, update the search package, and remove old `internal/service/`, old `internal/handler/http/`, old `internal/handler/grpc/`, old `internal/repository/` (except for non-duplicated files like `testpg/`, `webhook.go`, `unread.go`, `storage_ref.go`)
4. Run `make check-file-sizes` and `make lint-strict` after cleanup

| Field | Value |
|-------|-------|
| **Impact** | Maintainability, Team Productivity, Onboarding |
| **Effort** | **L** (requires careful planning and complete cleanup) |

---

### Finding F-002: Files Near the 500-Line Hard Limit

| Field | Value |
|-------|-------|
| **Category** | Organization / Quality |
| **Severity** | **High** |
| **Title** | 10 non-test files exceed or approach the 500-line limit |
| **Location** | See below |
| **Description** | AGENTS.md mandates a hard 500-line limit per file. Several files are approaching or near this limit. The trigger for refactoring is 400 lines. |

**Current State:**
| File | Lines |
|------|-------|
| `internal/service/mocks/mocks.go` | **639** ❌ (exceeds limit) |
| `internal/service/comment.go` | 481 ⚠️ |
| `internal/comment/service.go` | 470 ⚠️ |
| `internal/repository/comment.go` | 463 ⚠️ |
| `internal/app/new.go` | 461 ⚠️ |
| `internal/service/notification.go` | 437 ⚠️ |
| `internal/repository/message.go` | 423 ⚠️ |
| `internal/service/message.go` | 417 ⚠️ |
| `internal/comment/repository.go` | 411 ⚠️ |
| `internal/message/repository.go` | 403 ⚠️ |

**Recommended State:**
- `mocks.go` at 639 lines: Split into per-domain mock files (e.g., `mocks_file.go`, `mocks_message.go`, `mocks_comment.go`) or use a mock generator (mockgen, counterfeiter)
- `app/new.go` at 461 lines: Split wiring into domain-specific wiring functions (e.g., `wireFileDomain`, `wireMessagingDomain`)
- `service/comment.go` at 481 lines: Already split in the new layout — if we choose vertical slice, this is resolved
- Repository files: Each repository can be split by domain concern

---

## 2. Naming & Documentation

### Finding F-003: Duplicated Domain-Level Exports

| Field | Value |
|-------|-------|
| **Category** | Naming |
| **Severity** | **Medium** |
| **Title** | `domain.Comment` functions are duplicated in `internal/comment/comment.go` |
| **Location** | `internal/domain/comment.go` and `internal/comment/comment.go` |
| **Description** | Both `internal/domain/comment.go` and `internal/comment/comment.go` export identical functions (`ValidateNewComment`, `NewComment`) and variables (`ErrNestingTooDeep`). The domain package is the canonical source, but the new vertical-slice `internal/comment/` package re-exports them with identical signatures, creating ambiguity about which is authoritative. |

**Current State:**
```go
// internal/domain/comment.go — canonical
var ErrNestingTooDeep = errors.New("comment nesting must not exceed two levels")
func ValidateNewComment(parent *Comment) error { ... }
func NewComment(id ID, ownerUID string, ...) *Comment { ... }

// internal/comment/comment.go — duplicate
var ErrNestingTooDeep = errors.New("comment nesting must not exceed two levels")
func ValidateNewComment(parent *Comment) error { ... }
func NewComment(id domain.ID, ownerUID string, ...) *Comment { ... }
```

**Recommended State:**
- If vertical-slice layout wins, the domain package should be the single source of truth for core entity constructs
- The `internal/comment/comment.go` should import from `domain` and re-export only if there's a strong layering reason (e.g., `comment` package must not import `domain` — but it already does via `type Comment = domain.Comment`)
- Remove the duplicate exports from `internal/comment/` and reference `domain.ValidateNewComment` directly

---

### Finding F-004: Naming Inconsistency — `UserDirectory`

| Field | Value |
|-------|-------|
| **Category** | Naming |
| **Severity** | **Low** |
| **Title** | `UserDirectory` interface defined in two places |
| **Location** | `internal/service/service.go:33` and `internal/comment/service.go:36` |
| **Description** | The `UserDirectory` interface is defined in both the old `service` package and the new `comment` package. While both have the same `GetUsers` signature, they are different interface types, creating unnecessary coupling. |

**Current State:**
```go
// internal/service/service.go
type UserDirectory interface {
    GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error)
}

// internal/comment/service.go
type UserDirectory interface {
    GetUsers(ctx context.Context, uids []string) (map[string]sso.User, error)
}
```

**Recommended State:** Define once in a shared location (e.g., `internal/sso` or `internal/domain`) and import it.

---

## 3. Error Handling

### Finding F-005: Domain Error Package is Incomplete

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | **Medium** |
| **Title** | Only `ErrNotFound` and `ErrDuplicate` defined at domain level |
| **Location** | `internal/domain/errors.go` |
| **Description** | The domain error package only defines two sentinel errors. There are no domain-level errors for auth failures (`ErrUnauthorized`), forbidden access (`ErrForbidden`), validation failures (`ErrInvalid`), or conflict states. This means domain validation logic cannot express these conditions as domain errors — they must rely on `AppError` from the service layer, which leaks transport-layer concerns into domain logic. |

**Current State:**
```go
// internal/domain/errors.go
var ErrNotFound = errors.New("domain: not found")
var ErrDuplicate = errors.New("domain: duplicate")
```

**Recommended State:** Add domain-level errors for common failure modes:
```go
var (
    ErrNotFound     = errors.New("domain: not found")
    ErrDuplicate    = errors.New("domain: duplicate")
    ErrUnauthorized = errors.New("domain: unauthorized")
    ErrForbidden    = errors.New("domain: forbidden")
    ErrInvalid      = errors.New("domain: invalid input")
    ErrConflict     = errors.New("domain: conflict")
)
```

This would let domain constructors and validation functions return domain errors that the service layer can convert to `AppError` via type-switch.

---

### Finding F-006: Error Handling Pattern is Good but Inconsistent

| Field | Value |
|-------|-------|
| **Category** | Error Handling |
| **Severity** | **Low** |
| **Title** | Some service methods compare to `domain.ErrNotFound`, others use `repository.ErrNotFound` |
| **Location** | `internal/service/comment.go` vs `internal/comment/service.go` |
| **Description** | In the old monolithic service, repository errors are wrapped and compared to `repository.ErrNotFound`. In the new vertical-slice service, repository errors are wrapped and compared to `domain.ErrNotFound`. Both patterns work, but inconsistency is confusing. |

**Current State (old):**
```go
// internal/service/comment.go:59
if stderrors.Is(err, repository.ErrNotFound) {
```

**Current State (new):**
```go
// internal/comment/service.go:59
if stderrors.Is(err, domain.ErrNotFound) {
```

**Recommended State:** Standardize on `domain.ErrNotFound` as the canonical sentinel. Repositories should translate `gorm.ErrRecordNotFound` to `domain.ErrNotFound` before returning.

---

## 4. Logging

### Finding F-007: Business Logic Layers Lack Logging

| Field | Value |
|-------|-------|
| **Category** | Logging |
| **Severity** | **Medium** |
| **Title** | Service and repository layers have zero production logging |
| **Location** | All `internal/service/*.go` and `internal/repository/*.go` (and their new counterparts) |
| **Description** | The service and repository layers are completely silent — they do not log errors, decisions, or flow events. Error handling is done via return values only. This makes debugging production issues very difficult, especially without structured access to what decisions the service made. While the HTTP/gRPC middleware logs request-level data, there's no visibility into internal service operations. |

**Current State:**
```go
// internal/service/file.go — no logging at all
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*domain.File, error) {
    actor, err := mustActor(ctx)
    if err != nil { return nil, err }
    // ... silently decides what to do ...
}
```

**Recommended State:** Inject a `*zap.Logger` into service structs and log at appropriate levels:
- `log.Debug` for entry/exit of major operations (with actor, entity IDs)
- `log.Warn` for transient failures (e.g., dedup check failure, storage delete failure)
- `log.Error` for unexpected internal errors (only where recovered, since errors propagate up)

Example:
```go
func (s *fileService) Create(ctx context.Context, in CreateFileInput) (*File, error) {
    actor, err := domain.MustActor(ctx)
    if err != nil { return nil, err }
    s.log.Debug("file create", zap.String("actor", actor), zap.String("name", in.Name))
    // ...
    if err := s.files.Create(ctx, f); err != nil {
        s.log.Error("file persist failed", zap.Error(err), zap.String("file_id", f.ID.String()))
        if !reused { _ = s.store.Delete(ctx, key) }
        return nil, apperr.Internal("file.create_failed", "failed to persist file").Wrap(err)
    }
    return f, nil
}
```

---

### Finding F-008: No Correlation ID in Structured Logs

| Field | Value |
|-------|-------|
| **Category** | Logging |
| **Severity** | **Low** |
| **Title** | Request ID is set on gin context but not propagated to all log lines |
| **Location** | `internal/middleware/request_id.go` and `internal/middleware/access_log.go` |
| **Description** | The `RequestID` middleware sets a request ID on the gin context, and the `AccessLog` middleware includes it in the access log line. However, the request ID is not propagated to the service layer's context, so any service-level logging that might be added would not include it. |

**Recommended State:** Store the request ID in the Go context (not gin context) so that service-level logging can extract it. Use a `zap.Field` in the logger.With() chain.

---

## 5. Testing Practices

### Finding F-009: Test Coverage Around 50-60%, Below 80% Target

| Field | Value |
|-------|-------|
| **Category** | Testing |
| **Severity** | **High** |
| **Title** | Overall unit test coverage is 50-60%, with critical packages lower |
| **Location** | Various |
| **Description** | While there is extensive unit test infrastructure (mocks, integration tests), overall coverage is below the 80% target. Some packages are notably low: `internal/idempotency` at 38%, `internal/message` at 37%, `internal/pkg/migrate` at 35%, `internal/errors` at 37%. The `internal/repository` package reports 0% because all its tests are integration-tag-gated. |

**Current State (selected metrics):**
| Package | Coverage |
|---------|----------|
| `internal/idempotency` | 38.4% |
| `internal/message` | 36.6% |
| `internal/comment` | 52.9% |
| `internal/file` | 52.9% |
| `internal/handler/grpc` | 57.9% |
| `internal/handler/http` | 54.3% |
| `internal/service` | 63.9% |
| `internal/block` | 49.0% |
| `internal/notification` | 66.9% |

**Recommended State:** 
- Add unit tests for edge cases: empty input, invalid IDs, race conditions, concurrent operations
- Add tests for the error path branches (repository failure, storage failure, cache miss)
- Achieve >= 80% on all business-logic packages
- `internal/message` at 36% should be the highest priority

---

### Finding F-010: Manual Mocks File is Large and Fragile

| Field | Value |
|-------|-------|
| **Category** | Testing / Quality |
| **Severity** | **Medium** |
| **Title** | `internal/service/mocks/mocks.go` is 639 lines of hand-written mocks |
| **Location** | `internal/service/mocks/mocks.go` |
| **Description** | The mocks file is hand-maintained and already exceeds the 500-line limit. It defines all mock implementations for `FileRepo`, `Storage`, `MessageRepo`, `CommentRepo`, etc. Hand-written mocks are brittle — any interface change requires manual updates, and there's no guarantee the mocks stay in sync. |

**Current State:**
```go
// Hand-written mock — fragile and time-consuming
type FileRepo struct {
    CreateFn func(ctx context.Context, f *domain.File) error
    GetFn    func(ctx context.Context, id domain.ID) (*domain.File, error)
    ...
}
```

**Recommended State:** Use a mock generator:
```bash
go install github.com/vektra/mockery/v2@latest
```
Or use `mockgen` from `go.uber.org/mock`:
```bash
go install go.uber.org/mock/mockgen@latest
mockgen -source=internal/file/repository.go -destination=internal/file/mock_repository.go -package=file
```

This generates mocks automatically from interfaces, eliminating manual maintenance. Also resolves the file-size violation since generated mocks can be excluded from the check.

---

## 6. Technical Debt

### Technical Debt Register

| # | Item | Impact | Effort | Priority | Notes |
|---|------|--------|--------|----------|-------|
| TD-01 | **Code duplication** — two parallel implementations (old monolithic vs new vertical-slice) | **High** | **L** | **P0** | Single biggest maintainability issue. Must resolve before any significant feature work. |
| TD-02 | **10 files near/over 500-line limit** | **Medium** | **M** | **P1** | Multiple files trigger refactoring signals. Split per-domain. |
| TD-03 | **Low test coverage in `internal/message` (36%) and `internal/idempotency` (38%)** | **High** | **M** | **P1** | Core domain and reliability feature with insufficient coverage. |
| TD-04 | **Hand-written mocks (639 lines)** | **Low** | **S** | **P2** | Switch to mock generator; reduces maintenance and file size. |
| TD-05 | **No logging in service/repository layers** | **Medium** | **M** | **P2** | Makes production debugging harder. |
| TD-06 | **Incomplete domain error types** | **Low** | **S** | **P2** | Only `ErrNotFound` and `ErrDuplicate` defined. |
| TD-07 | **No automated architecture linting in CI** | **Low** | **S** | **P3** | `check-architecture.sh` exists but is not wired into Makefile CI target. |
| TD-08 | **`golangci-lint` not installed** (required by `make lint-strict`) | **Medium** | **S** | **P1** | CI gate cannot be enforced locally without installing the tool. Update setup docs or `Makefile` to auto-install. |
| TD-09 | **Two `UserDirectory` interface definitions** | **Low** | **S** | **P3** | Minor duplication; fix when doing vertical-slice cleanup. |
| TD-10 | **Older `.claude/worktrees/` directories contain duplicate code** | **Low** | **S** | **P3** | Should be gitignored or cleaned up. |

---

## 7. Code Quality Metrics

| Metric | Current | Target | Status |
|--------|---------|--------|--------|
| **Cyclomatic complexity** | Unknown (golangci-lint not installed) | < 10 | ⚠️ Cannot verify |
| **Function length** | Unknown (golangci-lint not installed) | < 50 lines | ⚠️ Cannot verify |
| **Test coverage** | ~50-60% (varies by package) | > 80% | ❌ **Below target** |
| **Code duplication** | **~40% estimated** (two parallel implementations) | < 5% | ❌ **Critically high** |
| **Documentation coverage** | Good: AGENTS.md, README.md, domain comments | > 70% | ✅ **Good** |
| **File size (non-test, non-generated)** | 1 file over limit (mocks.go at 639) | ≤ 500 lines | ⚠️ **1 violation** |
| **Architecture layering** | Clean — no circular deps, proper one-way dependency | Strict | ✅ **Good** |
| **TODO/FIXME count** | 0 | Minimal | ✅ **Excellent** |
| **Generated protobuf** | Committed as required | Committed | ✅ **OK** |
| **Integration tests** | Present with testcontainers | Present | ✅ **Good** |
| **Graceful shutdown** | Implemented with health gRPC draining | Present | ✅ **Good** |
| **SSO circuit breaker** | Implemented with configurable thresholds | Present | ✅ **Good** |

---

## Final Summary

### Overall Code Quality: **Needs Work**

The codebase demonstrates **solid engineering foundations**:
- ✅ Clean architecture layering with strict one-way dependency
- ✅ Proper error propagation (domain→service→handler pattern)
- ✅ Comprehensive integration tests with testcontainers
- ✅ Graceful shutdown with load balancer drain
- ✅ SSO circuit breaker, retry, caching
- ✅ No TODO/FIXME cruft
- ✅ Good domain documentation in `AGENTS.md`

However, there are **critical issues that must be addressed**:

### 🔴 Critical Quality Issues (Must Fix Before Production)

1. **Massive code duplication (TD-01)** — Two parallel implementations of the same business logic (~30,000 total lines). The new vertical-slice packages are dead code. Every feature change risks missed updates, and every bug fix must be applied twice. **Resolve this now** by choosing one architecture and deleting the other.

2. **Files approaching/over size limits (TD-02)** — 10 files near or over the 500-line limit. The refactoring trigger says "stop and split" at 500 lines — several files are already past 400.

3. **Low test coverage in core domains (TD-03)** — `internal/message` at 36% and `internal/idempotency` at 38% are critically low for core reliability features.

### 🟡 Maintainability Concerns

- The **mid-migration state** is the highest concern. If the vertical-slice architecture is the intended direction, complete the migration; if monolithic was the choice, delete the new packages. The current state is the worst of both worlds.
- **No logging in service/repository layers** makes production debugging reliant on middleware access logs alone
- **Hand-written mocks** are fragile and already violate the file-size limit
- **`golangci-lint` not locally installed** means the strict lint gate cannot be verified

### 🔵 Quick Wins (Easy Fixes)

| Issue | Fix | Effort |
|-------|-----|--------|
| TD-08 — `golangci-lint` not installed | Add install step to Makefile or dev setup docs | **S** |
| TD-04 — Hand-written mocks | Run `mockgen` on interfaces, delete `mocks.go` | **S** |
| TD-10 — Worktree dirs | Add `.claude/worktrees/` to `.gitignore` | **S** |
| TD-06 — Domain errors | Add 3-4 sentinel errors to `internal/domain/errors.go` | **S** |
| TD-09 — Duplicate `UserDirectory` | Consolidate to one definition | **S** |
| Add `golangci-lint` install to Makefile | Add `tools` target that installs lint deps | **S** |

### 📋 Recommended Action Plan

1. **Immediately**: Decide on target architecture (monolithic vs vertical-slice). This is a team/leadership decision.
2. **P0 (this sprint)**: Delete the losing implementation. If vertical-slice: wire new packages in `app/new.go`, update cross-references in `search` and `notification`, delete old `internal/service/`, old `internal/repository/`, old `internal/handler/http/`, old `internal/handler/grpc/`. Run `make test && make lint-strict`.
3. **P1 (next sprint)**: Split files approaching 500 lines (especially `app/new.go`, `service/comment.go`, `repository/comment.go`). Add unit tests for `internal/message`. Switch to generated mocks.
4. **P2**: Add service-level logging. Complete domain error types.
5. **P3**: Set up automated architecture linting in CI. Clean up worktrees.
