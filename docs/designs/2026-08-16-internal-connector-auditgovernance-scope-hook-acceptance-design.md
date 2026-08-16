# Design: Complete the phantom `scope_hook_test.go` acceptance (T-10.1e/f)

- **Date:** 2026-08-16
- **Module:** `internal/connector/auditgovernance`
- **Direction:** Complete the phantom `scope_hook_test.go` acceptance (T-10.1e/f): panic containment + re-entrancy guard for `OnScopeRejected` (direction 1 of 3, source analysis `docs/auto/analyses/internal-connector-auditgovernance-8461a3e7.json`)
- **Design reference:** `docs/plans/2026-08-07-auditgovernance-hook-hazard-resolution.md` (§5 test intent, §6 mutation evidence)
- **Change set:** purely additive — one new in-package test file with two regression tests, one truthfulness edit to a test-file header comment. **Zero production code changes.**
- **Status:** proposal (verification verdict in §1)

---

## 1. Verification verdict

The requirements evidence was treated as untrusted and checked against repo head. **All nine evidence claims verify** (two cosmetic line-drift notes; no substance is wrong). No production fix is needed; the missing artifact is the test file.

| # | Claim | Verdict | Evidence at repo head |
|---|---|---|---|
| E1 | `token_invalidation_test.go:7` is a phantom reference | ✅ Verified | Line 7: `// and scope-claim validation (REQ-4), hook safety (T-10.1e/f, scope_hook_test.go),`. Sole Go-code occurrence of `scope_hook_test` (grep over `internal/`). |
| E2 | `scope_hook_test.go` never existed in any commit | ✅ Verified | `git log --all --name-only -- internal/connector/auditgovernance/` lists exactly 5 files across 4 commits (`aafd7df`, `de20d78`, `33b2537`, `2ec2383`): `oauth.go`, `publisher.go`, `types.go`, `publisher_test.go`, `token_invalidation_test.go`. No path ever matched `scope_hook`. |
| E3 | `notifyScopeRejected` recover wrapper at `oauth.go:308-326` | ✅ Verified | Comment at 308–316; func at 317–326: `hook := s.onScopeRejected; if hook == nil { return }`, `s.hookRunning.Store(true)`, `defer s.hookRunning.Store(false)`, `defer func() { _ = recover() }()`, `hook(s.tenantID)`. Defer LIFO order = recover first, then guard clear. |
| E4 | `hookRunning` guard + call sites | ✅ Verified (line drift) | Field `oauth.go:41`. Guard at **`oauth.go:169-173`** (evidence cited 220–226 — earlier-revision drift; symbols verify exactly): `if s.hookRunning.Load() { … return "", ErrTokenUnavailable }`, after the cache fast path, immediately before the sole `DoChan` join. Call sites `s.notifyScopeRejected()` at **`oauth.go:231`** (400-gate) and **`oauth.go:244`** (scope-claim validation). |
| E5 | x/sync v0.20.0 re-raise semantics | ✅ Verified (line drift) | `go.mod:28` pins `golang.org/x/sync v0.20.0`. Module cache `singleflight.go:167-168` (`doCall`): `go panic(e)` + `select {} // Keep this goroutine around so that it will appear in the crash dump.` (evidence cited 184–219 — drift; mechanism verbatim). `DoChan` spawns the flight with `go g.doCall(c, key, fn)` — the flight fn (and hook) runs on a separate goroutine. |
| E6 | No test exercises `notifyScopeRejected`/`hookRunning`/`recover()` | ✅ Verified | `grep -rln "notifyScopeRejected\|hookRunning" internal/connector/auditgovernance/*_test.go` → no hits (exit 1). |
| E7 | Production fix already landed | ✅ Verified | `oauth.go:169-173` guard; call sites 231/244; recover wrapper 317–326; `types.go` `OnScopeRejected` field (~55–63) with the safety contract doc ("it is recover-wrapped and re-entrancy-guarded in-package, so it may panic safely, but it must not call back into the same tenant's AccessToken"); `publisher.go` `authorization` C-B propagation (~132–135): `errors.Is(err, ErrTokenScopeMissing)` returns the classified error through instead of collapsing to `ErrTokenUnavailable`. |
| E8 | Plan doc §5/§6 is the design source | ✅ Verified | `docs/plans/2026-08-07-auditgovernance-hook-hazard-resolution.md` exists. §5: "All three tests ship in the package as `scope_hook_test.go`"; T-10.1e/T-10.1f sketches. §6: both failure directions mutation-verified in the scratch harness (10/10 green, 5 repeats stable; recover removed → crash at `singleflight.go`; guard removed → bounded deadlock). |
| E9 | Test-file gate exemptions | ✅ Verified (cosmetic drift) | `.golangci.yml` `exclude-rules` exempts `.*_test\.go$` from `funlen`/`cyclop`/`lll`. `make check-file-sizes` excludes `*_test.go` and `*.pb.go` (the script is an inline Makefile target, not `scripts/check_file_sizes.sh` as the evidence named it — substance identical). |

**Additional facts pinned for the design** (beyond the evidence claims):

- Shared in-package helpers exist and are reusable: `scopeFact(tenantID)` (`token_invalidation_test.go:30`), `mustSource(t, tenantID)` (`:261`, hardcodes prefix `"aero-id"`), `auditBinding(baseURL, clientID)` (`publisher_test.go:267`, produces `TokenURL: baseURL + "/token"`).
- Constructors: `NewOAuthTokenSource(OAuthConfig{...}, client)` (nil client OK) and `NewPublisher(Config{...}, tokens, nil)`; `Publisher.Publish(ctx, fact) error` (`publisher.go:84`).
- Module watchdog convention: `time.After(5 * time.Second)` in `select` (multiple sites in `token_invalidation_test.go`); sibling tests use `t.Context()` (Go 1.26 toolchain, `t.Context()` available since 1.24).

**Conclusion:** the acceptance header at `token_invalidation_test.go:7` claims coverage that does not exist, and no test pins either safety property. This design completes the acceptance exactly as scoped: one panic-containment test (T-10.1e), one re-entrancy fail-fast test (T-10.1f), and a truthful header reference.

---

## 2. Problem statement

Two safety-critical paths in the auditgovernance module are claimed-but-untested:

1. **Panic containment (T-10.1e).** `OnScopeRejected` runs inside the singleflight flight fn (`fetchToken`). A composition-root hook panic escaping the flight fn is re-raised by x/sync v0.20.0 as `go panic(e); select {}` — an **unrecoverable process crash** of the whole aero-id binary (audit publication sits on the outbox drain path) — and coalesced waiters are **never delivered a result** (they starve). The in-package deferred `recover()` in `notifyScopeRejected` (`oauth.go:324`) is the only thing converting the panic into the classified `ErrTokenScopeMissing` for every waiter. If that recover regresses, the audit path takes down the process and audit facts are neither ledgered nor failed-fast.
2. **Re-entrancy guard (T-10.1f).** A hook calling back into the same tenant's `AccessToken` without the `hookRunning` guard would `DoChan` the in-flight flight and **self-join forever** — singleflight has no cancellation; the flight, the epoch key, and every subsequent same-tenant fetch hang permanently. The guard at `oauth.go:169-173` is the only thing converting that call into a fast `ErrTokenUnavailable`.

`token_invalidation_test.go:7` asserts both properties are covered by `scope_hook_test.go`; the file has never existed and no test exercises either path (E2, E6). CI is green while the acceptance claim is false — a regression in either safety property would land silently.

---

## 3. Design summary

| Artifact | Change |
|---|---|
| `internal/connector/auditgovernance/scope_hook_test.go` | **New.** In-package, non-integration tag. Two tests only: `TestScopeRejectedHookPanicIsContained` (R2/T-10.1e) and `TestScopeRejectedHookReentrancyFailsFast` (R3/T-10.1f). Header comment maps to the acceptance contract and the design source. |
| `internal/connector/auditgovernance/token_invalidation_test.go` | **Edit, one line.** Header sentence at line 7 becomes self-describing and still references the now-real `scope_hook_test.go` (R4). |
| Production code (`oauth.go`, `types.go`, `publisher.go`, …) | **No change.** Guard, recover wrapper, C-B propagation, marker error, and hook doc contract are already landed (E7). |
| `go.mod` / `go.sum` | **No change.** No new dependencies; the test imports only what the package's tests already use. |

The two tests are the minimal, deterministic projection of the plan's §5 T-10.1e/T-10.1f (the direction intentionally strips the plan's N=4 coalescing variant, gated handler, and follow-up publish; §13 non-goals).

---

## 4. API changes

**Production API: none.** The public surface — `TokenSource` interface, `OAuthConfig.OnScopeRejected`, `Config`, `OAuthBindingConfig`, `SourceBinding`, all sentinel errors — is untouched. The test file is in-package and defines no new exported symbols.

**Test-layer contract (what the change set adds):**

- Two exported-in-package test functions, names exactly as specified (they are the R2/R3 static-acceptance greps):
  - `TestScopeRejectedHookPanicIsContained`
  - `TestScopeRejectedHookReentrancyFailsFast`
- A file-level header comment that (a) names the T-10.1e/f ids, (b) references the design source `docs/plans/2026-08-07-auditgovernance-hook-hazard-resolution.md` (§5), and (c) references this design doc's acceptance mapping.
- Reuse of the existing shared helpers `scopeFact`, `mustSource`, `auditBinding` — no new fixtures beyond a minimal 400+`invalid_scope` token handler.

**Semantics pinned (the tests' observable contract):**

- A panicking hook must surface as `errors.Is(err, ErrTokenScopeMissing)` to `Publish`'s caller (the flight completes with `ErrTokenEndpointScopeMissing` wrapping `ErrTokenScopeMissing` — the 400-gate classification return runs after the recovered hook).
- A re-entrant hook call must surface as `errors.Is(err, ErrTokenUnavailable)` and must not block the outer publish.
- The hook fires exactly once per classification; the token cache stays empty on the scope-rejected path.
- The token-endpoint 400 + `invalid_scope`/`insufficient_scope` gate is the **only** hook-firing site reachable by these fixtures; 401/403 token-endpoint responses remain `ErrTokenUnavailable` (sibling FM-4 boundary) and must not be used.

---

## 5. `scope_hook_test.go` specification

### 5.1 File skeleton

```go
// Package auditgovernance — acceptance suite for the OnScopeRejected
// composition-root hook safety contract (T-10.1e/f; design
// docs/plans/2026-08-07-auditgovernance-hook-hazard-resolution.md §5, mapped
// to acceptance in
// docs/designs/2026-08-16-internal-connector-auditgovernance-scope-hook-acceptance-design.md):
// T-10.1e pins panic containment (a panicking hook must not escape the
// singleflight flight fn — x/sync v0.20.0 re-raises an escaping flight panic
// as an unrecoverable process crash), T-10.1f pins the re-entrancy guard (a
// hook calling back into the same tenant's AccessToken must fail fast with
// ErrTokenUnavailable, never self-join the in-flight flight). Both fixtures
// are in-package httptest loopback (AllowInsecureLoopback: true) reusing the
// module's shared helpers (scopeFact, mustSource, auditBinding).
package auditgovernance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)
```

Imports are a strict subset of what the package's tests already use (E9/R1.3). `internal/domain` and `internal/pkg/idgen` are **not** needed here because `scopeFact` (which uses them) lives in the sibling in-package file. No new import is introduced.

### 5.2 T-10.1e — `TestScopeRejectedHookPanicIsContained`

**Intent:** pin that a panicking `OnScopeRejected` hook is recover-contained inside the flight fn and converted into the classified error for the caller, invoked exactly once, with nothing cached.

**Fixture:** single tenant `tenant-a`; `httptest` server whose `/token` (the path produced by `auditBinding`) returns `400` with `{"error":"invalid_scope"}` and `Content-Type: application/json`; any other path → `http.NotFound` (the `/api/v1/events` endpoint is unreachable because the publish fails at authorization).

**Hook:** increments an `atomic.Int32` counter, records the received tenant id into an `atomic.Value`, then `panic("recorder hook boom")`.

**Flow:** build the source and publisher per §5.1 helpers, then `publisher.Publish(t.Context(), scopeFact(tenantID))` **synchronously on the test goroutine** — deliberate: this test's failure mode is a process crash, which is loud and immediate; no watchdog is needed (contrast R3, whose failure mode is a hang).

```go
// TestScopeRejectedHookPanicIsContained (T-10.1e): the OnScopeRejected hook
// runs inside the singleflight flight fn. x/sync v0.20.0 re-raises an
// escaping flight panic in a fresh goroutine (go panic(e); select {}) — an
// unrecoverable process crash — and never delivers a result to waiters. The
// in-package deferred recover in notifyScopeRejected must convert the hook
// panic into the classified ErrTokenScopeMissing for the caller, invoke the
// hook exactly once, and leave the token cache empty. Test completion IS the
// containment proof (a broken recover crashes the test binary at
// singleflight.doCall with "panic: recorder hook boom" — mutation-verified
// in the hazard-resolution plan §6).
func TestScopeRejectedHookPanicIsContained(t *testing.T) {
	const tenantID = "tenant-a"
	var (
		hookCalls atomic.Int32
		gotTenant atomic.Value // string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
	}))
	defer server.Close()

	tokens, err := NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
		OnScopeRejected: func(got string) {
			hookCalls.Add(1)
			gotTenant.Store(got)
			panic("recorder hook boom")
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(Config{
		Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
	}, tokens, nil)
	if err != nil {
		t.Fatal(err)
	}

	publishErr := publisher.Publish(t.Context(), scopeFact(tenantID))

	if !errors.Is(publishErr, ErrTokenScopeMissing) {
		t.Fatalf("Publish error = %v, want errors.Is(err, ErrTokenScopeMissing)", publishErr)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("hook invocations = %d, want 1", got)
	}
	if got, ok := gotTenant.Load().(string); !ok || got != tenantID {
		t.Fatalf("hook tenant = %v, want %q", got, tenantID)
	}
	if cached, ok := tokens.bindings[tenantID].cachedToken(); ok && cached != "" {
		t.Fatalf("token cache non-empty after scope rejection: %q", cached)
	}
}
```

**Why the classification assertion holds:** the 400 gate calls `s.notifyScopeRejected()` (`oauth.go:231`); after the recover swallows the panic, control returns and the gate returns `fmt.Errorf("%w: %w: HTTP %d", ErrTokenEndpointScopeMissing, ErrTokenScopeMissing, 400)`; `authorization`'s C-B propagation (`errors.Is(err, ErrTokenScopeMissing)`) returns it unwrapped to `Publish`'s caller.

**Why exactly-once holds:** `notifyScopeRejected` invokes the hook exactly once per classification (`hook := s.onScopeRejected; if hook == nil { return }` then one `hook(s.tenantID)` call).

**Determinism:** one synchronous publish, no coalescing, no handler gating, no timing element. The only goroutine besides the test goroutine is the singleflight flight goroutine (spawned by `DoChan`); the hook's atomic writes are race-safe.

### 5.3 T-10.1f — `TestScopeRejectedHookReentrancyFailsFast`

**Intent:** pin that a hook calling back into the same tenant's `AccessToken` fails fast with `ErrTokenUnavailable` (never self-joins the in-flight flight), that the outer `Publish` still completes with `ErrTokenScopeMissing`, that the hook ran exactly once, and that nothing was cached.

**Fixture:** same minimal 400 + `invalid_scope` server. Hook captures the `*OAuthTokenSource` (declare `var tokens *OAuthTokenSource` before `NewOAuthTokenSource`, assign after — the plan's established pattern) and `expectedSource := mustSource(t, tenantID)` (hardcodes prefix `"aero-id"`, which must match `SourcePrefix` so `AccessToken`'s `SourceSystem` validation passes).

**Hook:** calls `tokens.AccessToken(ctx, SourceBinding{TenantID: gotTenant, SourceSystem: expectedSource})` with `ctx := t.Context()` and sends the returned error on a **buffered channel** `innerResult := make(chan error, 1)` (cap 1 so the send never blocks).

**Flow:** the outer publish runs on a spawned goroutine delivering to `outerResult := make(chan error, 1)`. Both receipts are guarded by `select` + `time.After(5 * time.Second)` (the module's watchdog convention) converting a regression hang into `t.Fatal`. **The goroutine + watchdog is load-bearing:** a broken guard deadlocks the outer `Publish`; a synchronous call on the test goroutine could never reach `t.Fatal`.

```go
// TestScopeRejectedHookReentrancyFailsFast (T-10.1f): a hook that calls back
// into the same tenant's AccessToken must fail fast with ErrTokenUnavailable
// (hookRunning guard, checked before DoChan) instead of DoChan-ing the
// in-flight flight and self-joining forever — singleflight has no
// cancellation; the flight, the epoch key, and every future same-tenant fetch
// would hang permanently. The outer publish must still complete with
// ErrTokenScopeMissing, the hook must run exactly once, and nothing is
// cached. Pass criteria are exclusively the channel receipts; the watchdog
// only converts a regression hang into a bounded failure (mutation-verified
// in the hazard-resolution plan §6: removing the guard yields a bounded
// deadlock failure).
func TestScopeRejectedHookReentrancyFailsFast(t *testing.T) {
	const tenantID = "tenant-a"
	expectedSource := mustSource(t, tenantID)
	var (
		hookCalls atomic.Int32
		tokens    *OAuthTokenSource
	)
	innerResult := make(chan error, 1)
	outerResult := make(chan error, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_scope"})
	}))
	defer server.Close()

	tokens, err = NewOAuthTokenSource(OAuthConfig{
		SourcePrefix: "aero-id", AllowInsecureLoopback: true,
		Bindings: map[string]OAuthBindingConfig{tenantID: auditBinding(server.URL, "client")},
		OnScopeRejected: func(gotTenant string) {
			hookCalls.Add(1)
			_, innerErr := tokens.AccessToken(t.Context(), SourceBinding{
				TenantID: gotTenant, SourceSystem: expectedSource,
			})
			innerResult <- innerErr
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := NewPublisher(Config{
		Endpoint: server.URL, SourcePrefix: "aero-id", AllowInsecureLoopback: true,
	}, tokens, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { outerResult <- publisher.Publish(t.Context(), scopeFact(tenantID)) }()

	// Inner result first: the re-entrant call runs on the flight goroutine
	// inside the hook and completes before the hook returns, which precedes
	// the flight's result delivery — program order, not timing.
	select {
	case innerErr := <-innerResult:
		if !errors.Is(innerErr, ErrTokenUnavailable) {
			t.Fatalf("re-entrant AccessToken error = %v, want errors.Is(err, ErrTokenUnavailable)", innerErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("re-entrant AccessToken from hook did not fail fast (deadlock regression: guard missing?)")
	}

	select {
	case outerErr := <-outerResult:
		if !errors.Is(outerErr, ErrTokenScopeMissing) {
			t.Fatalf("outer Publish error = %v, want errors.Is(err, ErrTokenScopeMissing)", outerErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("outer Publish did not complete (deadlock regression: guard missing?)")
	}

	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("hook invocations = %d, want 1", got)
	}
	if cached, ok := tokens.bindings[tenantID].cachedToken(); ok && cached != "" {
		t.Fatalf("token cache non-empty after scope rejection: %q", cached)
	}
}
```

**Why the guard fires immediately:** `notifyScopeRejected` arms `hookRunning` before invoking the hook (`oauth.go:322`); the inner `AccessToken` → `accessToken` hits the guard at `oauth.go:169-173` (after the cache fast path, before the only `DoChan`) and returns `ErrTokenUnavailable`.

**Why inner precedes outer by program order:** the inner call and the innerResult send happen inside the hook on the flight goroutine; the hook return precedes `fetchToken`'s return, which precedes the flight's result delivery to the outer waiter. Ordering is causal, not timed.

**Hard requirement — channel-guarded completion:** pass criteria are exclusively the two channel receipts. The test fails if either channel is not received (broken guard → self-join deadlock → watchdog `t.Fatal`). No `time.Sleep` anywhere; the watchdog exists solely to convert a hang into a bounded, deterministic failure.

**Unwind safety in the regression case:** if the guard is removed, the inner `DoChan` coalesces into the in-flight flight and blocks on `select { ctx.Done(), result }`; `t.Context()` cancels at test end, so the inner goroutine unblocks (and even a hung outer flight self-unwinds after the failure). No permanent goroutine leak on a failing run.

---

## 6. R4 — truthful acceptance header

`token_invalidation_test.go:7` becomes self-describing while preserving the T-10.1e/f mapping and the exact filename (the `grep -q "scope_hook_test.go"` acceptance grep must keep matching):

```diff
-// and scope-claim validation (REQ-4), hook safety (T-10.1e/f, scope_hook_test.go),
+// and scope-claim validation (REQ-4), hook safety (T-10.1e/f — panic
+// containment + re-entrancy guard, scope_hook_test.go),
```

With R1–R3 landed, the sentence resolves to the real file; the phantom claim (E1/E2) ceases to hold. No other file changes; `docs/auto/SUMMARY.md` rows and `docs/auto/runs/*` artifacts are historical pipeline records and out of scope.

---

## 7. Compatibility constraints

1. **In-package test file only.** The cache-empty assertions access `tokens.bindings[tenantID].cachedToken()` (unexported), so the file must be `package auditgovernance`, not `auditgovernance_test`. Non-integration tag.
2. **Import whitelist.** Only imports already used by the package's tests (`context`, `encoding/json`, `errors`, `net/http`, `net/http/httptest`, `sync/atomic`, `testing`, `time`; `internal/domain` and `internal/pkg/idgen` come in transitively via the reused `scopeFact`). No new dependencies; `go.mod`/`go.sum` untouched.
3. **Prefix consistency.** `SourcePrefix: "aero-id"` must match between `OAuthTokenSource` config and `mustSource`'s hardcoded prefix, or the re-entrant call fails `SourceSystem` validation with `ErrTenantUnbound` instead of exercising the guard.
4. **Classification gate.** The token-endpoint 400 + `invalid_scope`/`insufficient_scope` gate is the only path firing the hook at the token endpoint. 401/403 token-endpoint fixtures stay `ErrTokenUnavailable` (sibling FM-4 boundary) and must not be used.
5. **No production edits.** If any gate fails for a production-file reason, stop and report — do not "fix" production code as part of this direction.
6. **Gate exemptions honored.** Test files are exempt from `funlen`/`cyclop`/`lll` (`.golangci.yml` exclude-rules) and from `check-file-sizes`; the file must still be gofmt/vet clean, and `go test -race ./internal/connector/auditgovernance/` must stay green (module passes today).
7. **Determinism rules.** R2: single synchronous publish, no timing. R3: passes are channel receipts only; the watchdog only converts hangs into failures; no `time.Sleep` as a pass or synchronization mechanism; no handler gating (no `sync.Once` release channels needed).
8. **No `t` use from foreign goroutines.** Both fixtures hand results over channels and assert on the test goroutine (`t.Context()` passed by value is not a `t` method call).
9. **File must not duplicate fixtures.** Reuse `scopeFact`, `mustSource`, `auditBinding`; the file contains exactly the two tests of §5 and nothing else.

---

## 8. Failure modes

| # | Failure | Mechanism | Pinned by |
|---|---|---|---|
| FM-1 | **Broken recover → process crash.** Hook panic escapes the flight fn; x/sync v0.20.0 re-raises (`go panic(e); select {}`) → unrecoverable aero-id binary crash; coalesced waiters starve (never delivered). | R2 runs the panic on a real flight; any escape crashes the test binary at `singleflight.doCall` (`panic: recorder hook boom`). Test completion is the proof. | R2 (AC-R2-2) |
| FM-2 | **Broken guard → permanent deadlock.** Re-entrant same-tenant call `DoChan`s the in-flight flight (no cancellation); the flight, the epoch key, and every future same-tenant fetch hang forever; tenant audit publication stalls with no surfaced error. | R3's inner call would never return → `innerResult` unreceived → watchdog `t.Fatal` (or, with a bounded inner ctx, a non-`ErrTokenUnavailable` error that fails the `errors.Is` assertion). | R3 (AC-R3-1) |
| FM-3 | **Both hazards regress.** Crash + deadlock are complementary; each test pins exactly one direction, so either regression fails independently. | R2 + R3 | R2, R3 |
| FM-4 | **Classification erosion.** A future change collapsing the token-endpoint marker or the C-B propagation would surface `ErrTokenUnavailable` instead of `ErrTokenScopeMissing`. | R2 asserts `errors.Is(publishErr, ErrTokenScopeMissing)`; R3 asserts the same for the outer result. | AC-R2-1, AC-R3-2 |
| FM-5 | **Hook invoked more than once / cache polluted / wrong tenant.** e.g. a hook hoisted outside the classification gate, or a 400 path that caches. | R2/R3 assert `hookCalls == 1`, tenant match, empty cache. | AC-R2-3/4, AC-R3-3/4 |
| FM-6 | **Acceptance staleness recurrence.** The header reference drifts from the real filename again (the E1/E2 condition). | R4's self-describing sentence + the static `grep -q "scope_hook_test.go"` acceptance grep fail closed. | R4 |
| FM-7 | **Non-deterministic/pass-by-timing tests** would reintroduce flakiness. | Design rule §7.7: passes are synchronous (R2) or channel receipts (R3); the watchdog cannot pass a test. | §7.7 |

---

## 9. Landing steps (migration of the change set)

Ordered, each step verifiable:

1. **Create** `internal/connector/auditgovernance/scope_hook_test.go` exactly per §5 (header + two tests). Verify: `test -f internal/connector/auditgovernance/scope_hook_test.go`.
2. **Edit** `token_invalidation_test.go:7` per §6. Verify: `grep -q "scope_hook_test.go" internal/connector/auditgovernance/token_invalidation_test.go`.
3. **Run the package gates first** (fast feedback): `go test ./internal/connector/auditgovernance/ -count=1 && go test -race ./internal/connector/auditgovernance/`.
4. **Run the full done gate** (see §10).
5. **Static acceptance greps** (see §10).

There is no data migration, no schema change, no config change, and no rollout sequencing — the change set is test-only. The production behavior being pinned shipped in `2ec2383` (hook guard/recover) and `de20d78` (classification machinery); this design only makes its acceptance executable.

---

## 10. Testable acceptance mapping

### 10.1 Requirements → acceptance checks → assertions

| Req | Acceptance check | Concrete assertion | Location |
|---|---|---|---|
| R2 (T-10.1e) | AC-R2-1 classification preserved | `errors.Is(publishErr, ErrTokenScopeMissing)` | `TestScopeRejectedHookPanicIsContained` |
| R2 | AC-R2-2 process must not crash | Test completion + green run (broken recover = crash at `singleflight.doCall`) | same test, by construction |
| R2 | AC-R2-3 hook exactly once, right tenant | `hookCalls.Load() == 1`; recorded tenant == `"tenant-a"` | same test |
| R2 | AC-R2-4 (recommended) cache stays empty | `tokens.bindings[tenantID].cachedToken()` → `(_, false)` / `""` | same test |
| R3 (T-10.1f) | AC-R3-1 inner fails fast, no deadlock, delivered first | `innerResult` received (watchdog-guarded); `errors.Is(innerErr, ErrTokenUnavailable)`; assert inner before outer | `TestScopeRejectedHookReentrancyFailsFast` |
| R3 | AC-R3-2 outer classification intact | `outerResult` received; `errors.Is(outerErr, ErrTokenScopeMissing)` | same test |
| R3 | AC-R3-3 cache stays empty | `tokens.bindings[tenantID].cachedToken()` → `(_, false)` / `""` | same test |
| R3 | AC-R3-4 hook exactly once | `hookCalls.Load() == 1` | same test |
| R3 | Hard requirement: channel-guarded completion, no time-based pass | Pass = the two channel receipts only; watchdog `t.Fatal` on hang; no `time.Sleep` | same test (design rule §7.7) |
| R4 | Header truthful + self-describing | Line 7 references real `scope_hook_test.go`, keeps T-10.1e/f mapping | `token_invalidation_test.go:7` |

### 10.2 Done gate

```bash
cd /home/u1/aero-id
make test              # go test ./... -count=1 — includes the two new tests, green
make lint              # go vet ./...
make lint-strict       # golangci-lint (test files exempt from funlen/cyclop/lll)
make check-file-sizes  # tests excluded; green
make build             # -> bin/aero-id
go test -race ./internal/connector/auditgovernance/   # module passes today; must stay green
```

Static acceptance greps (each must hold):

```bash
test -f internal/connector/auditgovernance/scope_hook_test.go                     # R1
grep -q "func TestScopeRejectedHookPanicIsContained" internal/connector/auditgovernance/scope_hook_test.go       # R2
grep -q "func TestScopeRejectedHookReentrancyFailsFast" internal/connector/auditgovernance/scope_hook_test.go    # R3
grep -q "scope_hook_test.go" internal/connector/auditgovernance/token_invalidation_test.go                       # R4
```

Behavioral verification of the safety claims (implied by green gates, stated for the record):

- AC-R2-1..3: proven by R2 running green; a removed recover crashes the test binary at `singleflight.go` (hazard plan §6 mutation).
- AC-R3-1..3: proven by R3 running green; a removed guard deadlocks into the watchdog `t.Fatal` (hazard plan §6 mutation).
- Header truthfulness (E1/E2 no longer hold): R4 + `test -f` prove the referenced file exists.

---

## 11. Mutation sensitivity (required property)

The two mutations below must each fail the corresponding test — this is the property that makes the tests real regression pins rather than documentation:

| Mutation | Expected effect | Mechanism |
|---|---|---|
| Delete the recover in `notifyScopeRejected` (`oauth.go:324`, `defer func() { _ = recover() }()`) | R2 test binary **crashes** with `panic: recorder hook boom` re-raised from `singleflight.(*Group).doCall` | Hook panic escapes the flight fn; x/sync v0.20.0 re-raises in a fresh goroutine (`go panic(e); select {}`) |
| Delete the `hookRunning` guard (`oauth.go:169-173`, `if s.hookRunning.Load() { … }`) | R3 fails **deterministically** (bounded): inner call never returns → `innerResult` unreceived → watchdog `t.Fatal`; outer `Publish` likewise hangs | Inner `DoChan` self-joins the in-flight flight (same epoch key); singleflight has no cancellation |

Both mutations were verified against this exact design in the hazard-resolution plan §6 scratch harness (10/10 green on the intact tree, 5 repeats stable, both failure directions red).

---

## 12. Risk register

| Risk | Mitigation | Residual |
|---|---|---|
| Race detector regression in the new tests | All shared state is `atomic`; cross-goroutine error handoffs are buffered channels; `tokens` variable write→read is ordered by the `DoChan` goroutine-spawn happens-before edge; no `t` calls from foreign goroutines | Low — module passes `-race` today and the fixtures add only this pattern |
| Watchdog flakiness | Watchdog is 5 s (module convention); pass never depends on it — it only converts a hang into a failure; pass paths are synchronous or causal | Negligible |
| R2 crash could take down a CI job if the recover regressed | That is the intent: a broken recover is a process crash in production; failing the test binary is the correct loud failure (fail-closed, visible in crash dumps) | None (by design) |
| `t.Context()` misuse from the flight goroutine | `t.Context()` is passed by value only; no `t` method invoked off the test goroutine | None |
| A reviewer "simplifies" the R3 watchdog away | The watchdog is load-bearing for hang conversion (broken guard = outer publish never returns); design §5.3 states the goroutine + watchdog necessity | Documented in code comment and this design |
| Acceptance claim silently goes stale again | R4 self-describing sentence + static grep acceptance in §10.2 | Low |

---

## 13. Non-goals (explicitly out of scope)

- **No production code changes** (guard, recover wrapper, C-B propagation, `ErrTokenEndpointScopeMissing` marker, `OnScopeRejected` doc contract are landed and verified — E7).
- **No coalesced-waiter panic variant** (plan's T-10.1e N=4 workers + post-panic follow-up publish). The direction's acceptance (a) is a single-publish containment proof.
- **No cross-tenant re-entrancy pin** (`TestScopeHookCrossTenantAccessIsNotBlocked`, plan §5.2's second half) — belongs to direction 2 (cross-tenant isolation) of the same analysis file.
- **No post-hook guard-clearing follow-up publish** (plan's T-10.1f asserted a subsequent successful publish). The direction's acceptance (b) is AC-R3-1..3 plus exactly-once; the follow-up is optional only if it does not alter the required fixture shape, and is omitted here to keep the fixture delta minimal.
- **No docs/plans or docs/auto edits.** `docs/auto/SUMMARY.md` run history and `docs/auto/runs/*` artifacts are pipeline records, not acceptance source.
- **No new packages, dependencies, or exported API.**

---

## Appendix A — evidence verification detail (file:line)

| Symbol | Location (repo head) |
|---|---|
| `hookRunning` field | `internal/connector/auditgovernance/oauth.go:41` |
| Re-entrancy guard | `oauth.go:169-173` (cache fast path at 157–159, guard, then `DoChan` at 177) |
| 400-gate hook call site | `oauth.go:231` (`s.notifyScopeRejected()`, inside `fetchToken`'s non-200 branch) |
| Scope-claim hook call site | `oauth.go:244` (after `validScopeClaim(wire)` false) |
| Recover wrapper | `oauth.go:308-326` (comment 308–316; func 317–326; defer order: `Store(false)` registered first, `recover()` second → recover runs first on LIFO) |
| Sentinel errors incl. marker | `types.go:22-33` (`ErrTokenEndpointScopeMissing` wraps `ErrTokenScopeMissing`; events-endpoint site must never emit the marker — `publisher.go:112`) |
| `OnScopeRejected` contract doc | `types.go` (OAuthConfig field ~55–63) |
| C-B propagation | `publisher.go:132-135` (`authorization`: `errors.Is(err, ErrTokenScopeMissing)` → return err) |
| x/sync pin | `go.mod:28` (`golang.org/x/sync v0.20.0`) |
| x/sync re-raise | module cache `golang.org/x/sync@v0.20.0/singleflight/singleflight.go:167-168` (`go panic(e)` + `select {}`); `DoChan` spawns `go g.doCall(c, key, fn)` |
| Phantom header reference | `internal/connector/auditgovernance/token_invalidation_test.go:7` |
| Shared helpers | `scopeFact` `token_invalidation_test.go:30`; `mustSource` `:261` (prefix `"aero-id"`); `auditBinding` `publisher_test.go:267` (TokenURL = `baseURL + "/token"`) |
| Constructors | `NewOAuthTokenSource(OAuthConfig, client)`; `NewPublisher(Config, tokens, client)` (`publisher.go:33`); `Publisher.Publish(ctx, fact) error` (`publisher.go:84`) |
| Watchdog convention | `time.After(5 * time.Second)` selects in `token_invalidation_test.go:237,355,365,380` |
