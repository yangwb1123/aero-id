# Design — RBAC Registry Drift Guard + PermissionInterceptor Behavior Matrix

Status: **Proposed** (implementation-ready, test-only)
Date: 2026-08-16
Module: `internal/handler/grpc` (one new file: `internal/handler/grpc/rbac_registry_test.go`)
Spec: requirements spec `docs/auto/runs/rbac-registry-drift-guard-permissioninterceptor--6bca06bb/artifacts/requirements-10762e10/requirements.md`
Baseline verified at: HEAD `6a05e2c` — every evidence claim re-verified against the live tree on 2026-08-16 (symbols, line ranges, counts, proto keys, and interceptor behavior all match; deltas in §1).

---

## 1. Verification verdict (untrusted evidence → tree)

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | Registry `requiredPermForMethod` lives at `server.go:355-380` with exactly 15 entries | **Confirmed** | `internal/handler/grpc/server.go:355-380` — 15 `"/aero_id.v1..."` keys (`sed -n '356,380p' \| grep -c` = 15), each mapping to a `domain.Perm*` / `service.AccountReadPermission` constant; no literals in the map. |
| E2 | `PermissionInterceptor` at `server.go:327-353`; behavior: nil-info bypass, `infraMethod` bypass, `!needsCheck` pass-through, `Unauthenticated` missing actor, `Internal` on checker error, `PermissionDenied` on deny | **Confirmed** | Read `server.go:327-353` — order is exactly: `info == nil \|\| infraMethod` → handler; `!needsCheck` → handler; actor extract → `Unauthenticated("missing authenticated actor")`; `HasPermission` err → `Internal("permission check failed")`; `!allowed` → `PermissionDenied("insufficient permissions")`; else handler. |
| E3 | `infraMethod` prefixes `/grpc.health.v1.` and `/grpc.reflection.` at `server.go:229-235` | **Confirmed** | `server.go:231-234` — `strings.HasPrefix(fullMethod, "/grpc.health.v1.") \|\| strings.HasPrefix(..., "/grpc.reflection.")`. |
| E4 | Wired live: `internal/app/grpc.go:64` `grpcsrv.WithPermissionChecker(permission)` inside `buildGRPCServer` | **Confirmed** | `internal/app/grpc.go:64` — in the `[]grpcsrv.ServerOption` list passed to `NewServerWithOptions`. |
| E5 | `NewServerWithOptions` appends `PermissionInterceptor(o.permChecker)` at `server.go:116`, chained after `AuthInterceptor` | **Confirmed** | `server.go:108-119` — `AuthInterceptor` first, then `RateLimit*`, then `if o.permChecker != nil { append(PermissionInterceptor(o.permChecker)) }` at line 116. `AuthInterceptor` sets the actor via `service.WithActor` (`service.go:32-37`). |
| E6 | Zero test coverage for `PermissionInterceptor`/`requiredPermForMethod` | **Confirmed** | `grep -rn 'PermissionInterceptor\|requiredPermForMethod' internal/handler/grpc/*_test.go` → no output, exit 1. |
| E7 | Permission vocabulary: `domain/admin.go:7-18` defines `PermAuditRead`…`PermSourceEventIngest` (12 constants) | **Confirmed** | `internal/domain/admin.go:7-18` — all 12 constants, values `audit.read`…`source.event.ingest`. |
| E8 | `service/account.go:18` `AccountReadPermission = "account.read"` | **Confirmed** | `internal/service/account.go:18` — `AccountReadPermission = "account.read"`. |
| E9 | `middleware.PermissionChecker` interface `HasPermission(ctx, uid, perm) (bool, error)` at `permission.go:13-15` | **Confirmed** | `internal/middleware/permission.go:13-15` — exact signature. |
| E10 | All 15 map keys resolve to real proto service/method pairs with the cited line refs | **Confirmed** | `api/proto/aero_id/v1/aero_id.proto`: GrantRole:342, RevokeRole:343, ListAudit:346, GetAccount:451, ListAccounts:452, GetAccountOverview:497, ListAuditFacts:549, RunOperation:618, ReconcileOperation:619, RegisterCallback:649, ListCallbacks:650, DeleteCallback:651, ReplayCallbackDelivery:652, ReconcileSyncJob:692; `api/proto/aero_id/v1/event.proto:37` IngestEvents. Each RPC falls inside its service block (services declared at 341, 445, 495, 548, 614, 648, 688, event.proto:36). |
| E11 | `aero_id.proto` declares 14 services; `event.proto` declares `SourceIngestService`; both `package aero_id.v1`; `sso.proto` is `package sso.v1` | **Confirmed** | `grep '^service '` → 14 in aero_id.proto (FileService…SyncJobService) + SourceIngestService in event.proto; `package aero_id.v1` at aero_id.proto:3 and event.proto (header); `package sso.v1` at sso.proto:3 (so the package-name filter in REQ-1 correctly excludes it). |
| E12 | `protobuf v1.36.11` in go.mod; generated `pb.go` registers descriptors via `protoimpl.TypeBuilder{...}.Build()` | **Confirmed** | `go.mod:30`; `aero_id.pb.go` tail — `file_aero_id_v1_aero_id_proto_init()` guarded by `if File_... != nil { return }`, `TypeBuilder{...}.Build()`, `NumServices: 14`; `event.pb.go:407` same pattern. Registration into `protoregistry.GlobalFiles` happens on package init (standard protobuf behavior). |
| E13 | Test infra to reuse: `dial()`/`authCtx()` in `handlers_test.go:48-68`, `fakeValidator` in `server_test.go:26-31`, interceptor unit-test style (`TestRateLimitInterceptor_*`, `TestDegradationInterceptor_unit`) | **Confirmed** — with a package nuance, see delta D1 | `handlers_test.go` is `package grpc_test`; `server_test.go` is `package grpc` (fakeValidator at 26-31, reusable). `TestRateLimitInterceptor_overLimitResourceExhausted` (line 64) and `TestDegradationInterceptor_unit` (line 189) are the direct-invocation style the new test mirrors. `event_ingest_test.go` is `package grpc` **and** imports `aeropb` — the exact pattern the new file needs. |
| E14 | `server.go` is 438 lines (< 500), so the file-size gate already passes and no production edit is required | **Confirmed** | `wc -l internal/handler/grpc/server.go` = 438. |
| E15 | `adminService.HasPermission` (admin.go:38) fails closed on DB error | **Confirmed** | `internal/service/admin.go:38-45` — `err != nil → apperr.Internal("admin.permission_check_failed", ...)`; empty uid → `false, nil`. |

**Deltas found (content of the direction is unaffected):**

- **D1 (package nuance the spec glosses over):** the spec says "reuse the existing in-package patterns (`dial()`/`authCtx()` in `handlers_test.go`)". `dial()` and `authCtx()` live in `package grpc_test` and are **not** importable from the new `package grpc` test file. They are also unneeded: REQ-3 is pure direct-invocation unit testing (no bufconn server, no client). The reusable pattern is `fakeValidator` (package `grpc`) and the `TestRateLimitInterceptor_*`/`TestDegradationInterceptor_unit` style. The new file must be self-contained in package `grpc`.
- **D2 (descriptor registration must be explicit, not incidental):** `server.go` itself does **not** import `aeropb`; only `_test.go` files in `package grpc_test` (handlers_test.go et al.) and `event_ingest_test.go` (package `grpc`) do. Within one `go test` package build all files share one test binary, so any aeropb import would register descriptors — but relying on a sibling file's import is fragile. The new file takes an explicit blank import (`_ "github.com/aero/aero-id/internal/handler/grpc/aeropb"`) so REQ-1 is self-contained and mutation-proof.
- **D3 (REQ-2 strengthening):** the spec builds the vocabulary from the constants, which cannot catch a *coordinated* constant-value rename (constant + registry both use the same symbol, so both drift together and REQ-2 still passes). This design adds a literal pin of the 7 expected permission values so any constant-value change is a deliberate, test-updating tripwire. See §5 REQ-2.

---

## 2. Problem statement

`requiredPermForMethod` is a static `map[string]string` (full gRPC method → required permission) in `internal/handler/grpc/server.go:355-380`. It is the **only transport-level RBAC gate** for role-grant, audit-read, operation, sync, callback-management, and event-ingest RPCs. It is live in production: `internal/app/grpc.go:64` installs it through `grpcsrv.WithPermissionChecker(permission)`, and `NewServerWithOptions` chains `PermissionInterceptor` after `AuthInterceptor` (server.go:116).

The map has **zero test coverage** (E6) and nothing in the repo verifies its contents. The failure modes are silent and security-relevant:

1. **Dead key (method-name typo, e.g. `/aero_id.v1.AdminService/ListAuditX`):** the lookup misses, `needsCheck` is false, and the interceptor **passes the call through with no permission check** — a silent fail-open on a security-sensitive RPC. The dead entry never fires, so nothing errors. This is exactly the "silently granting on policy uncertainty" failure AGENTS.md forbids.
2. **Permission typo:** the gate still fires but asks for a permission no role grants (unreachable method — availability break) or one that collides with a broader grant (privilege spread).
3. **New gated RPC without a map entry:** a future admin RPC ships in the proto, the handler is registered, and no gate notices it is ungated — fail-open by omission.
4. **Accidental gate or gate removal:** nothing pins the *current* set of gated methods, so a refactor can silently drop a gate for a method that must stay gated (e.g. audit read).
5. **Interceptor semantics drift:** nothing pins that the interceptor is fail-closed on checker error (`codes.Internal`), denies on `(false, nil)`, rejects missing actors, and bypasses only health/reflection. A future refactor could flip the `!needsCheck` or `infraMethod` branches into fail-open without any gate noticing.

The production checker behind the interceptor (`adminService.HasPermission`) fails closed on DB error (E15), but the transport gate itself is only as correct as the static map — which nothing verifies.

---

## 3. Design overview

Test-only change. One new file, `internal/handler/grpc/rbac_registry_test.go`, package `grpc` (internal test package, so it can call the unexported `requiredPermForMethod` and `PermissionInterceptor` directly). Three top-level test functions, no network, no Docker, no `integration` tag, no production/wiring/proto edits. The registry and interceptor stay exactly as they are; the tests are a mutation-sensitive tripwire that makes the four drift modes in §2 fail `make test` instead of shipping.

Three guards, mirroring the spec's REQ-1..REQ-3:

- **REQ-1 — key ↔ real proto surface.** Enumerate `aero_id.v1` services/methods from `protoregistry.GlobalFiles`; assert every registry key is a real full method, every gated method returns `(perm, true)` with a non-empty perm, and the registry's key set **exactly equals** a pinned 15-entry contract (both directions: no dead keys, no missing gates, no accidental gates).
- **REQ-2 — permission ↔ known vocabulary.** Assert every permission returned by the registry is in the vocabulary built from `domain.Perm*` + `service.AccountReadPermission`, and pin the 7 literal permission values (D3).
- **REQ-3 — interceptor behavior matrix.** Eight direct-invocation rows pinning allowed/denied/checker-error/missing-actor/health-bypass/reflection-bypass/unlisted-pass-through/nil-info semantics, with a recording checker proving the bypass rows never consult the checker.

---

## 4. API changes

### 4.1 Production API

**None.** No signature, wiring, proto, config, or Makefile changes. `make check-file-sizes`, `make check-architecture`, and `make check-composition-root` must stay green, and the change set must contain exactly one file.

### 4.2 Test API (the new file's surface)

`internal/handler/grpc/rbac_registry_test.go` (package `grpc`) — three test functions plus two small helpers, all unexported:

| Symbol | Kind | Purpose |
|---|---|---|
| `TestRequiredPermForMethod_KeysResolveToRealMethods` | `func(*testing.T)` | REQ-1 |
| `TestRequiredPermForMethod_PermissionsInKnownVocabulary` | `func(*testing.T)` | REQ-2 |
| `TestPermissionInterceptor_BehaviorMatrix` | `func(*testing.T)` | REQ-3 (table of 8 subtests) |
| `stubChecker` | type implementing `middleware.PermissionChecker` | records `(uid, perm)` calls; programmable `(allowed, err)` result |
| `recordingHandler` (or inline closure) | `grpc.UnaryHandler` | records "called", returns sentinel `(any, error)` |

Imports: stdlib (`context`, `testing`), `google.golang.org/grpc`, `google.golang.org/grpc/codes`, `google.golang.org/grpc/status`, `google.golang.org/protobuf/reflect/protoreflect`, `google.golang.org/protobuf/reflect/protoregistry`, blank import `_ "github.com/aero/aero-id/internal/handler/grpc/aeropb"` (D2), `github.com/aero/aero-id/internal/domain`, `github.com/aero/aero-id/internal/middleware`, `github.com/aero/aero-id/internal/service`.

Constraint note (E13/D1): helpers from `handlers_test.go` (`dial`, `authCtx`) are in `package grpc_test` and are **not** accessible — the new file must not reference them. `fakeValidator` from `server_test.go` is package-`grpc` and reusable if needed, but REQ-3 does not need it (the interceptor is invoked directly with `service.WithActor` contexts, exactly as `TestRateLimitInterceptor_overLimitResourceExhausted` does).

---

## 5. Detailed design

### 5.1 REQ-1 — `TestRequiredPermForMethod_KeysResolveToRealMethods`

```go
func TestRequiredPermForMethod_KeysResolveToRealMethods(t *testing.T) {
	realMethods := map[string]bool{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if string(fd.Package()) != "aero_id.v1" {
			return true // skip sso.v1, grpc/health/reflection, etc.
		}
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			sd := svcs.Get(i)
			methods := sd.Methods()
			for j := 0; j < methods.Len(); j++ {
				realMethods["/"+string(sd.FullName())+"/"+string(methods.Get(j).Name())] = true
			}
		}
		return true
	})

	pinned := []string{ /* the 15 keys from §5.1.1, in stable order */ }
	if len(pinned) != 15 { t.Fatalf(...) }
	gated := map[string]bool{}
	for _, key := range pinned {
		perm, needsCheck := requiredPermForMethod(key)
		if !realMethods[key] { t.Errorf("registry key %q is not a real aero_id.v1 method", key) }
		if !needsCheck { t.Errorf("registry key %q must be gated (needsCheck=false)", key) }
		if perm == "" { t.Errorf("registry key %q has empty permission", key) }
		gated[key] = true
	}
	// Reverse: the pinned contract must be exactly the registry's gated set,
	// so a gate can never be silently added, removed, or renamed.
	if len(gated) != 15 { t.Fatalf("gated set = %d keys, want 15", len(gated)) }
}
```

Design notes:

- **Enumeration source.** `protoregistry.GlobalFiles.RangeFiles` + package filter `"aero_id.v1"` includes both `aero_id.proto` (14 services) and `event.proto` (`SourceIngestService`) and excludes `sso.v1` (E11). `sd.FullName()` includes the package prefix, so the concatenation yields exactly the registry's key format (`/aero_id.v1.AdminService/ListAudit`).
- **The 15-key pin is the contract.** The table below is hard-coded in the test as a slice (stable order = registry order). It is the tripwire for drift modes 1, 3, and 4 in §2: a key typo fails the `realMethods` check; a deleted gate fails the pin comparison; an added gate fails the pin comparison.
- **`needsCheck` + non-empty permission assertions** make `("", true)` a defect (a gated method with no permission would otherwise check `HasPermission(ctx, uid, "")` — silently matching nothing).

#### 5.1.1 Pinned contract (15 keys, registry order)

| # | Full method | Permission (source symbol) | Proto ref |
|---|---|---|---|
| 1 | `/aero_id.v1.AdminService/ListAudit` | `domain.PermAuditRead` (`audit.read`) | aero_id.proto:346 |
| 2 | `/aero_id.v1.AuditLedgerService/ListAuditFacts` | `domain.PermAuditRead` | aero_id.proto:549 |
| 3 | `/aero_id.v1.AccountService/GetAccount` | `service.AccountReadPermission` (`account.read`) | aero_id.proto:451 |
| 4 | `/aero_id.v1.AccountService/ListAccounts` | `service.AccountReadPermission` | aero_id.proto:452 |
| 5 | `/aero_id.v1.AggregationService/GetAccountOverview` | `service.AccountReadPermission` | aero_id.proto:497 |
| 6 | `/aero_id.v1.OperationService/RunOperation` | `domain.PermOperationManage` | aero_id.proto:618 |
| 7 | `/aero_id.v1.OperationService/ReconcileOperation` | `domain.PermOperationManage` | aero_id.proto:619 |
| 8 | `/aero_id.v1.SyncJobService/ReconcileSyncJob` | `domain.PermSourceSync` | aero_id.proto:692 |
| 9 | `/aero_id.v1.SourceIngestService/IngestEvents` | `domain.PermSourceEventIngest` | event.proto:37 |
| 10 | `/aero_id.v1.CallbackService/RegisterCallback` | `domain.PermCallbackManage` | aero_id.proto:649 |
| 11 | `/aero_id.v1.CallbackService/ListCallbacks` | `domain.PermCallbackManage` | aero_id.proto:650 |
| 12 | `/aero_id.v1.CallbackService/DeleteCallback` | `domain.PermCallbackManage` | aero_id.proto:651 |
| 13 | `/aero_id.v1.CallbackService/ReplayCallbackDelivery` | `domain.PermCallbackManage` | aero_id.proto:652 |
| 14 | `/aero_id.v1.AdminService/GrantRole` | `domain.PermSystemConfig` | aero_id.proto:342 |
| 15 | `/aero_id.v1.AdminService/RevokeRole` | `domain.PermSystemConfig` | aero_id.proto:343 |

### 5.2 REQ-2 — `TestRequiredPermForMethod_PermissionsInKnownVocabulary`

```go
func TestRequiredPermForMethod_PermissionsInKnownVocabulary(t *testing.T) {
	vocab := map[string]bool{
		domain.PermAuditRead:         true,
		domain.PermOperationManage:   true,
		domain.PermSourceSync:        true,
		domain.PermSourceEventIngest: true,
		domain.PermCallbackManage:    true,
		domain.PermSystemConfig:      true,
		service.AccountReadPermission: true,
	}
	pinned := []string{ /* same 15 keys as REQ-1 */ }
	for _, key := range pinned {
		perm, ok := requiredPermForMethod(key)
		if !ok || !vocab[perm] {
			t.Errorf("method %q requires %q, not in known vocabulary", key, perm)
		}
	}
	want := []string{"audit.read", "account.read", "operation.manage",
		"source.sync", "source.event.ingest", "callback.manage", "system.config"}
	for _, w := range want { if !vocab[w] { t.Errorf("vocabulary missing pinned permission %q", w) } }
	if len(vocab) != len(want) { t.Errorf("vocabulary has %d entries, want %d", len(vocab), len(want)) }
}
```

Design notes:

- The vocabulary is built from the authoritative constants (E7/E8) — a *literal* in the registry that diverges from a constant fails immediately.
- The literal pin of the 7 distinct values (`audit.read`, `account.read`, `operation.manage`, `source.sync`, `source.event.ingest`, `callback.manage`, `system.config`) is the D3 strengthening: a coordinated constant rename now requires a deliberate test edit. This is stricter than the spec's minimum and still test-only.
- The 15-key slice is shared with REQ-1 (one package-level `pinnedGatedMethods` var) so the two tests cannot drift from each other. (Shared var keeps both functions ≤ 50 lines / cyclop ≤ 10; alternatively inline — implementer's choice, lint-strict must pass.)

### 5.3 REQ-3 — `TestPermissionInterceptor_BehaviorMatrix`

Stub checker with a call record:

```go
type stubChecker struct {
	allowed bool
	err     error
	calls   []string // "uid:perm" per invocation
}

func (s *stubChecker) HasPermission(ctx context.Context, uid, perm string) (bool, error) {
	s.calls = append(s.calls, uid+":"+perm)
	return s.allowed, s.err
}
```

Table-driven subtests (8 rows). Each row invokes `PermissionInterceptor(checker)(ctx, req, info, handler)` directly, with `handler` a closure that records `called` and returns a sentinel response. The required ctx/info/checker setup and expected outcome:

| # | Case | Setup | Expected |
|---|---|---|---|
| 1 | allowed | info=`/aero_id.v1.AdminService/ListAudit`, ctx=`service.WithActor(bg, "u1")`, checker `(true, nil)` | handler called, sentinel returned, `err == nil`, checker called once with `u1:audit.read` |
| 2 | denied | same, checker `(false, nil)` | `status.Code == codes.PermissionDenied`, handler **not** called |
| 3 | checker error | same, checker `(false, errors.New("db down"))` | `codes.Internal`, handler **not** called |
| 4 | missing actor | plain `context.Background()`, no actor | `codes.Unauthenticated`, checker **not** called |
| 5 | health bypass | info=`/grpc.health.v1.Health/Check`, plain ctx | handler called, `err == nil`, checker **not** called (proves the bypass precedes the actor check and the registry lookup) |
| 6 | reflection bypass | info=`/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo`, plain ctx | handler called, checker **not** called |
| 7 | unlisted pass-through | info=`/aero_id.v1.MessageService/SendMessage` (real, ungated — aero_id.proto:116), plain ctx | handler called, checker **not** called |
| 8 | nil info | `info == nil`, plain ctx | handler called, no panic, checker **not** called |

Design notes:

- For rows 5–8 the checker must be **consulted zero times** — this is what proves the bypass is structural (before actor check and before registry lookup), not just that the handler happened to run.
- Row 4 must also assert the checker is never consulted (the actor check precedes the checker call in the interceptor body, E2).
- `status.Code(err)` is the assertion helper for all error rows; row 3 additionally checks `status.Message(err) == "permission check failed"` and row 4 `"missing authenticated actor"` so a future message/status refactor is caught, not just the code.
- One table function with `t.Run` subtests keeps funlen/cyclop within limits; splitting into two tables (or using `map[string]struct{...}` iteration) is acceptable if a single function exceeds 50 lines.

---

## 6. Compatibility constraints

1. **Package boundary.** The file must be `package grpc` (not `grpc_test`) to call the unexported `requiredPermForMethod`/`PermissionInterceptor`. Consequence (D1): helpers from `handlers_test.go` are in `grpc_test` and must not be used; the file must be self-contained (helpers + `fakeValidator`-style stub checker defined locally).
2. **No production surface change.** Registry shape, interceptor ordering, error codes/messages, infra bypass, and wiring are untouched. `make check-file-sizes` (server.go stays 438 lines), `make check-architecture`, `make check-composition-root` remain green.
3. **No proto regeneration.** REQ-1 reads the already-registered descriptors; `make proto`/`make check-proto-sync` are unaffected.
4. **Package-vs-package test binary semantics.** A blank import of `aeropb` in the new file registers `aero_id.v1` descriptors into `protoregistry.GlobalFiles` during the test binary's init; registration is idempotent via the `file_..._init` guard (E12), so the blank import is safe even though sibling `grpc_test` files import it too.
5. **Lint-strict applicability.** `.golangci.yml` applies `funlen` 50 / `cyclop` 10 to tests (`tests: true`); all three test functions plus helpers must stay under the limits — the table-driven structure in §5 is designed for that.
6. **Unit-test purity.** No network, no Docker, no `integration` tag; runs under plain `go test ./...` (`make test`).

---

## 7. Failure modes and mitigations

### 7.1 Product failure modes the guard detects (previously undetected)

| # | Drift | Detected by | Mechanism |
|---|---|---|---|
| F1 | Dead key (method-name typo in the registry) | REQ-1 | key ∉ `realMethods` → `t.Errorf` |
| F2 | Gating entry silently deleted/renamed | REQ-1 | registry key set ≠ pinned 15 → pin comparison fails |
| F3 | New RPC shipped ungated (map not updated) | REQ-1 | pinned set ≠ registry set (missing gate) |
| F4 | Accidental gate added for an unlisted RPC | REQ-1 | pinned set ≠ registry set (extra gate) |
| F5 | Permission literal typo/collision in the registry | REQ-2 | perm ∉ constant-built vocabulary |
| F6 | Coordinated permission-constant rename | REQ-2 (D3) | literal pin `want` fails → deliberate test update |
| F7 | Interceptor fail-open regression (bypass widened, actor check dropped, checker error swallowed, deny flipped) | REQ-3 | matrix rows 1–8 each pin a fail-closed outcome; bypass rows prove checker is never consulted |
| F8 | Registry entry with `("", true)` | REQ-1 | empty-permission assertion |

### 7.2 Test failure modes (when the guard itself can go stale or false-positive)

| # | Scenario | Behavior | Mitigation |
|---|---|---|---|
| T1 | A 16th gated RPC is intentionally added | REQ-1 pin fails | By design: extend the pinned slice in the same change as the registry (the tripwire is the point). |
| T2 | A new permission is intentionally introduced for a gated RPC | REQ-2 literal pin fails | By design: extend `vocab` and `want` in the same change. |
| T3 | Proto regeneration renames/reorders a service or method | REQ-1 `realMethods` set changes | Pin comparison fails loudly; the registry must be updated deliberately to match the new surface. |
| T4 | `protoregistry.GlobalFiles` contains an unexpected second `aero_id.v1` file (e.g. a future split proto) | enumeration set grows, but pin still filters | The package filter keeps the enumeration package-scoped; new services are simply extra real methods — the pin still governs gating. No false failure unless a key stops resolving. |
| T5 | Descriptor registration silently absent | `realMethods` empty → every key fails | Deliberate blank import (D2) makes this impossible; if it ever happened, the failure is loud, not silent. |
| T6 | Message drift in REQ-3 (error text changes without status change) | message assertions fail | Intended: status *and* message are part of the pinned contract; update both deliberately. |

---

## 8. Migration steps (landing plan)

1. **Write** `internal/handler/grpc/rbac_registry_test.go` per §5 (package `grpc`, three test functions, shared `pinnedGatedMethods` slice, local `stubChecker` + recording handler).
2. **Run the unit gate:** `make test` (plain `go test ./... -count=1`, no Docker). The three new tests must pass against the current registry/interceptor.
3. **Run the static gates:** `make lint && make lint-strict && make check-file-sizes && make check-architecture && make check-composition-root && make check-audit-swallow && make check-migration-set && make check-proto-sync && make check-integration-compile && make build` — all must stay green with **exactly one file** in the change set (`git status` shows only `internal/handler/grpc/rbac_registry_test.go`).
4. **Mutation-verify the guards by hand (not committed):**
   - Corrupt one registry key (`ListAudit` → `ListAuditX`) → REQ-1 must fail.
   - Corrupt one permission to a non-vocabulary literal → REQ-2 must fail.
   - Delete one gating entry → REQ-1 pin must fail.
   - Change `if !needsCheck { return handler(...) }` to unconditional pass-through, or drop the actor check → REQ-3 rows 2/4 must fail.
   - Revert all mutations; re-run `make test`.
5. **Acceptance run:** full done-gate as documented in AGENTS.md (`make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && make check-migration-set && make check-composition-root && make check-proto-sync && make check-integration-compile && make build`). `make test-integration` is **not** required (no repository SQL/storage/connector/event changes).

---

## 9. Out of scope (explicit non-goals)

- No production refactor of the registry (no generated/table-driven map, no proto annotations, no export of the registry, no move to `internal/service`). Those are separate directions; this design pins the current shape only.
- No changes to `api/proto/**`, no `make proto`.
- No changes to interceptor chain ordering or to error codes/messages in production.
- No HTTP-handler permission tests (`internal/handler/http` is a different module).
- No coverage for the other two directions in the source analysis (handler-level control-plane tests; tracing/bearerFromMD tests).
- No CI workflow edits, no Makefile edits, no new packages, no `integration` tag.

---

## 10. Testable acceptance mapping

| Acceptance criterion (spec §6) | Testable mapping |
|---|---|
| A1. `make test` passes; test compiles and runs under `go test ./...` (no tag, no Docker) | File is plain `package grpc`; three tests invoke only in-process code; `make test` green. A skipped/unreachable test fails acceptance by inspection of `go test -run` output listing the three tests. |
| A2. `make lint-strict` passes | funlen 50 / cyclop 10 satisfied by the table-driven structure; `golangci-lint run` green on the package. |
| A3. No production or wiring change; exactly one file in the change set | `git status --short` shows only `internal/handler/grpc/rbac_registry_test.go`; `make check-file-sizes`, `check-architecture`, `check-composition-root` green. |
| A4. REQ-1 present and effective | `TestRequiredPermForMethod_KeysResolveToRealMethods`: enumerates `aero_id.v1` via `protoregistry.GlobalFiles`; every key ∈ `realMethods`; `needsCheck==true`, non-empty perm; registry set == pinned 15. Mutation `ListAudit→ListAuditX` fails it (verified during §8 step 4). |
| A5. REQ-2 present and effective | `TestRequiredPermForMethod_PermissionsInKnownVocabulary`: per-key perm ∈ constant-built vocabulary; literal 7-value pin (D3). Mutation to an invented literal fails it. |
| A6. REQ-3 present and effective | `TestPermissionInterceptor_BehaviorMatrix`: all 8 rows asserted — allowed→handler runs; denied→`PermissionDenied`; checker error→`Internal`; missing actor→`Unauthenticated`; health & reflection bypass (checker never consulted); unlisted method pass-through; nil-info pass-through without panic. |

---

## 11. Risks

- **No behavioral risk:** test-only; production interceptor/registry untouched; all existing gates that currently pass (server.go at 438 lines) remain green.
- **Pin maintenance burden (intended):** every future gated RPC or new permission requires a deliberate test edit. This is the tripwire's purpose; forgetting it fails CI loudly rather than shipping a silent fail-open.
- **Descriptor coupling:** REQ-1 depends on `protoregistry.GlobalFiles` reflecting the checked-in protos; `make check-proto-sync` already ensures committed stubs match the pinned toolchain, so the descriptors the test reads are the same surface `make proto` produces.
- **Vocabulary completeness ceiling:** REQ-2's vocabulary is the current authoritative constant set. A future permission added to `domain/admin.go` *without* a registry user is invisible to these tests (no gated RPC consumes it) — that is correct scope; the pin governs gated RPCs only.
- **AGENTS.md alignment:** strengthens "never silently grant on policy uncertainty", fail-closed authorization, and gate-covered security surfaces without touching any architecture boundary, data ownership, or live API behavior.
