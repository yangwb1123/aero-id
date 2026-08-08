# Design — internal/outbox: TokenSource Invalidate on 401 with epoch-guarded single-flight backfill and scope-missing alerting

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-07-internal-outbox-tokensource-invalidate-on-401.md` (REQ-1..REQ-6) |
| Module (batch label) | `internal/outbox` (effective scope `internal/connector/auditgovernance`) |
| Source analysis | `docs/auto/analyses/internal-outbox-f6bb61c6.json` (direction #2) |
| Status | Design — evidence re-verified against the working tree (dirty at 139 files: 69 modified + 70 untracked) and against head `13e4676` on 2026-08-07; swept against the merged interface plan, the requirements spec, and the hook-hazard companion on 2026-08-07 (M1 ≡ C1 pin, 400-gate, T-10.1d.1 rewritten-by-C-C, concurrency constraints, FM-9, T-10.1b.2, M0 enumeration) |

## 1. Evidence verification result (untrusted claims re-checked)

The requirements spec's claims were re-checked symbol-by-symbol against both the committed head `13e4676`
and the working tree. **Substantive claims hold in the working tree; three material corrections and one
accuracy defect are folded into this design.**

| # | Cited claim | Verdict |
|---|---|---|
| E1 | oauth.go: singleflight `DoChan("client", ...)`, `cachedToken`/`fetchToken`/`cacheToken`, no `Invalidate`, no epoch | ✅ **True at HEAD and in the tree** (oauth.go is unmodified). `tenantTokenSource` :30-41, `accessToken` :122-145 (`DoChan` :126, constant key), `cachedToken` :147-151, `fetchToken` :153-173, `cacheToken` :210-217. `grep Invalidate internal/` = 0 hits |
| E2 | publisher.go `validateReceipt` → opaque `'audit governance: HTTP %d'` | ✅ **True.** `validateReceipt` :220-245, error :224, body discarded :223, `maxReceiptBytes` :18. ⚠️ The tree additionally has uncommitted conflict-as-success semantics (HEAD still rejects conflicts via `!receipt.Conflict` in `receiptMatches`) — baseline pinned to the tree (§3.7) |
| E3 | `CanPublishTenant`/`HasBinding` seam | ✅ **True** (`CanPublishTenant` publisher.go:99-100; `HasBinding` oauth.go:108-110; interface types.go:58-61) |
| E4 | publisher_test.go: 7 tests, token tests at :221-276, no 401/revoke/epoch coverage | ✅ **True** (tests at :17/:102/:152/:198/:214/:221/:257; file has uncommitted +103 lines of conflict-receipt tests, still 7 test funcs) |
| E5 | app/audit.go boot-time binding validation only | ✅ **Substance true; line numbers are tree-only.** HEAD has no `buildAuditStack`; `wireAuditServices` HEAD:17, `buildAuditPublishers` HEAD:38, `NewOAuthTokenSource` HEAD:50 (tree :99), `validateAuditBindingCoverage` HEAD:85 (tree :134). No runtime 401 reaction anywhere (E1) |
| E6 | "The dead inline `_ = publisher.Publish` loop no longer exists; `Record` is pure `repo.Append` + `ObserveAuditIngestFailure`; `publishAuditFact` is the sole `Publish` caller" | ❌ **False at HEAD, true only in the uncommitted tree.** `git show HEAD:internal/service/audit_ledger.go` :76-80 still contains the inline loop; HEAD `callback_audit.go` also has a public service `Publish` (:19). The spec's "verified at head 13e4676" overclaims. The tree state (loop removed, `enqueueFact`/`publishAuditFact` sole `Publish` caller at callback_audit.go:120) is the working baseline; the loop removal must be committed before this design's behavior is load-bearing (§5, M0) |
| E7 | "Single `TokenSource` implementer; no test fakes implement it in-repo" | ⚠️ **Incomplete.** True for the default gate, but `staticTokenSource` (internal/service/callback_integration_test.go:64-75, `//go:build integration`) implements `TokenSource`. The interface change breaks `make test-integration` compilation unless extended — invisible to the spec's done gate. `internal/connector/billing/oauth.go:28` `tokenSource` is unrelated (verified) |
| E8 | No scope-missing metric family exists | ✅ **True** (`ControlPlaneMetrics` control_plane_metrics.go:13-24; registry control_plane.go:77-89; zero scope-missing symbols) |
| — | Analysis JSON | ⚠️ `docs/auto/analyses/internal-outbox-f6bb61c6.json` is markdown with an embedded JSON array, not pure JSON (strict parse fails). Direction #2 content present; cosmetic |
| — | Sibling signature collision | ✅ **Confirmed, and already resolved on 2026-08-07** by `docs/plans/2026-08-07-auditgovernance-tokensource-merged-interface.md`: `Invalidate(tenantID string)` (internal-connector spec) vs `Invalidate(SourceBinding) error` (cmd-server design D3) vs `Invalidate(SourceBinding)` void (cmd-migrate + this spec) **cannot coexist**; the merged plan forbids a third signature. This design adopts the merged form (§2.1) |
| — | Hook safety | ✅ **Confirmed hazard** (`docs/plans/2026-08-07-auditgovernance-hook-hazard-resolution.md`): the alert hook runs inside the singleflight flight fn; x/sync v0.20.0 re-raises a flight panic as an unrecoverable process crash and starves coalesced waiters; a hook that self-joins the same tenant's flight parks forever. The spec's "nil-safe hook" (REQ-4) is insufficient; the design adds recover-wrap + re-entrancy guard (§2.3) |

Three design-level corrections the spec needs (each makes an acceptance check satisfiable):

- **C-A (interface signature):** adopt `Invalidate(SourceBinding) error` (merged superset) instead of void.
  Void call sites compile unchanged (`_ = p.tokens.Invalidate(binding)`); the spec's own §5 merge note is
  thereby resolved without a third signature.
- **C-B (error propagation):** `Publisher.authorization` (publisher.go:113-117) collapses every
  `AccessToken` error into `ErrTokenUnavailable`. REQ-4's token-endpoint `invalid_scope` classification is
  therefore **unreachable through `Publish`** and T-10.1d.2 is unsatisfiable as written. `authorization`
  must propagate `ErrTokenScopeMissing` by `errors.Is` before the generic wrap.
- **C-C (403 alert seam):** the merged plan routes events-endpoint 403 to the service-side
  `ObserveAuditScopeMissing` counter (sibling C1), and the connector hook (`OnScopeRejected`) fires only for
  token-endpoint scope rejections — "no double counting". The spec's REQ-4/T-10.1d.1 hook-on-403 is
  reconciled — **T-10.1d.1 is marked rewritten-by-C-C in the acceptance mapping (§6)**: events-403 returns
  `ErrTokenScopeMissing` without a connector hook; the alerting requirement moves to the sibling's service
  counter on the same `aero_control_audit_scope_missing_total{source=...}` family (spec §5 already defers
  that wiring).

## 2. API changes

### 2.1 types.go — interface, sentinels, hook field

```go
var (
    // existing Err* set unchanged (:18-22)
    ErrTokenRejected     = errors.New("audit governance: token rejected")      // events-endpoint 401
    ErrTokenScopeMissing = errors.New("audit governance: token scope missing") // invalid_scope / events 403
)

type TokenSource interface {
    AccessToken(context.Context, SourceBinding) (string, error)
    HasBinding(string) bool
    Invalidate(SourceBinding) error // 401: clear cached token + bump epoch; best-effort, always nil today
    // MarkBindingUnhealthy / BindingUnhealthy land in the SAME joint commit as Invalidate —
    // M1 ≡ C1 (merged plan §6, pinned in §3.1 and in the merged plan): the interface is fixed
    // exactly once, by both campaigns jointly.
}

type OAuthConfig struct {
    // ... existing fields unchanged ...
    OnScopeRejected func(tenantID string) // optional; nil-safe, recover-wrapped, re-entrancy-guarded (§2.3)
}
```

`Config` (publisher) gains **no** hook field (C-C): events-403 alerting is the sibling's service counter.

### 2.2 oauth.go — epoch, invalidate, epoch-fenced singleflight

- `tenantTokenSource` gains `epoch uint64` and `tenantID string` (for the hook; set in
  `newTenantTokenSource`, which gains the `tenantID` parameter — sole construction site is
  `NewOAuthTokenSource`).
- `OAuthTokenSource.Invalidate(binding)`: validate like `AccessToken` (`TenantSourceID` + `SourceSystem`
  match + binding exists); unbound/mismatched is a **no-op returning nil** (mirror oauth.go:113-120).
  Per-tenant `invalidate()` under `mu.Lock`: `s.token = ""; s.epoch++`. Always returns nil today.
- `accessToken`: after the `cachedToken` miss and the `hookRunning` join guard (§2.3), read `s.epoch`
  under `RLock` **and release the lock before `DoChan`** — hard constraint #1: `s.mu` must never be held
  across `DoChan`/wait. The flight closure's `cacheToken` takes `Lock`; an RLock held across the wait
  would block it forever (writer starvation → deadlock). Key the flight as
  `fmt.Sprintf("client-%d", epoch)` (replaces the constant `"client"`, oauth.go:126). The closure captures
  that epoch, **retains the existing inner `cachedToken` re-check (oauth.go:128-130, unchanged — without
  it a caller whose entry check raced a completed flight performs a redundant HTTP fetch, repeated under
  a storm)**, and calls `fetchToken(ctx, epoch)`; it never re-reads `s.epoch` inside the closure (the
  closure must pass the epoch it was keyed with — key/epoch identity is the anti-poisoning invariant,
  FM-1). On `errEpochDiscarded` from the flight, re-enter against the current epoch (join any fresh
  flight) **with an explicit attempts counter** — hard constraint #2: `attempts := 0; ...; if attempts
  >= 2 { return ErrTokenUnavailable }`, never a bare `for errors.Is(err, errEpochDiscarded)` loop (a
  counterless loop performs one full token-endpoint fetch per epoch bump and is unbounded under a
  sustained 401 storm, REQ-2). A discard implies a bump, so the re-entry key (newer epoch) can never
  equal the completed flight's key (deleted on completion, x/sync v0.20.0 singleflight.go:203-205) — no
  self-loop.
- `fetchToken(ctx, epoch)`: non-200 path reads the error body with `maxTokenBodyBytes` (replaces the
  discard at :166) and best-effort parses `{"error": "<code>"}` via a small helper
  `classifyTokenError(status, body) error` (≤ 50 lines, cyclomatic ≤ 10). **Gate (pinned to the merged
  plan §3 table, superseding REQ-4's looser "non-200" wording): scope classification applies only to
  `status == 400` with a parseable body.** `invalid_scope` / `insufficient_scope` →
  `fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing, status)` (status == 400 under the gate) +
  `notifyScopeRejected` (exactly once, inside the flight, so coalesced waiters share one firing). Any
  other token-endpoint status (incl. 401/403), and 400 with an unparseable or unknown error code, stays
  `ErrTokenUnavailable: HTTP %d` as today — the sibling FM-4 boundary: no invalidate, no mark, no hook
  (FM-10; pinned by a T-10.1d.3 negative, §6). On success, `cacheToken(wire, epoch)`.
- `cacheToken(wire, epoch)`: under `Lock`, discard when `epoch != s.epoch` (stale pre-invalidation fetch can
  never repopulate the cache); return a bool; on discard `fetchToken` returns the unexported
  `errEpochDiscarded` sentinel instead of the token.

### 2.3 Hook safety (from the hook-hazard resolution)

- `notifyScopeRejected(tenantID)`: nil check → arm → invoke, with the arm/clear **pinned inside
  `notifyScopeRejected`** in exactly this shape (hazard-resolution companion §2.1):

  ```go
  s.hookRunning.Store(true)
  defer s.hookRunning.Store(false)
  defer func() { _ = recover() }()
  hook(s.tenantID)
  ```

  Defers run LIFO: on a hook panic `recover()` runs first, then the flag clears — the guard is released on
  both panic and normal return. Hard constraint #3: arm/clear live *inside* `notifyScopeRejected`, never in
  `accessToken` (arming around `DoChan` would refuse every concurrent join for the flight's duration and
  break T-10.1b.1's N=8 coalescing), and the clear is always the deferred `Store(false)` above, never a
  non-deferred call after `hook(...)` (a panicking hook would leave the flag armed forever → permanent
  tenant wedge, FM-4's second half). Composition-root-injected code only is recovered; package-owned
  flight code stays panic-transparent (fail-closed).
- `tenantTokenSource` gains `hookRunning atomic.Bool`; `accessToken` checks it **after the cache fast
  path, immediately before the only join point** (cache hits remain available during a hook) and returns
  `ErrTokenUnavailable` when set (a hook calling `AccessToken` for the same tenant cannot self-join its own
  flight — no permanent park). The guard is per-tenant-source: a hook may call `AccessToken` for a
  *different* tenant (a different flight) without tripping it.
- Documented residuals (pathological composition-root code, FM-4/FM-5 rows): a `runtime.Goexit`-ing hook
  runs the defers (flag cleared ✓) but v0.20.0's `errGoexit` branch skips the waiter fan-out
  (singleflight.go:207-209) → coalesced waiters park until their own ctx deadline — transient starvation,
  not a wedge (the key is deleted, later callers start fresh). A *blocking* hook parks coalesced waiters
  for the hook's duration, bounded only by caller ctx deadlines; the `OnScopeRejected` doc comment must
  say "keep the hook short".

### 2.4 publisher.go — 401/403 classification + propagation

In `Publish` (publisher.go:83-97), after `client.Do`, before `validateReceipt`:

- `StatusUnauthorized`: drain body (bounded, `maxReceiptBytes`), `Close`, call
  `_ = p.tokens.Invalidate(SourceBinding{TenantID: fact.TenantID, SourceSystem: source})` (`source` is
  already returned by `authorization`), return `fmt.Errorf("%w: HTTP %d", ErrTokenRejected, 401)`.
  Status-driven; body need not parse (REQ-3). No retry loop inside `Publish` — the drain's retry ladder
  (callback_audit.go:96-107) is the redelivery mechanism.
- `StatusForbidden`: drain body (bounded), `Close`, call `p.tokens.MarkBindingUnhealthy(binding)` (the
  merged C1 switch — cooldown, never `Invalidate`), return `fmt.Errorf("%w: HTTP %d", ErrTokenScopeMissing,
  403)`. **No `Invalidate`, no connector hook** (C-C); cache retained; `epoch` unchanged. The service-side
  `ObserveAuditScopeMissing` counter fires in the sibling routing (merged plan §4), not here.
- `authorization`: `if err != nil { if errors.Is(err, ErrTokenScopeMissing) { return "", "", err }; return "",
  "", ErrTokenUnavailable }` (C-B); the token-empty/CRLF guard stays.
- `validateReceipt` untouched (202-only, conflict-as-success in the tree, idempotency key `fact.ID.String()`
  at :185 — REQ-5).

## 3. Compatibility constraints

1. **Interface fixed exactly once — M1 ≡ C1 is a single joint commit** (merged plan §6, pinned in both
   docs): the full five-method merged interface (`AccessToken`, `HasBinding`, `Invalidate(SourceBinding)
   error`, `BindingUnhealthy`, `MarkBindingUnhealthy` — merged plan §2) plus the
   `ErrTokenRejected`/`ErrTokenScopeMissing` sentinels land in **one commit authored jointly by both
   campaigns** (this design contributes `Invalidate` + the sentinels; the sibling contributes
   `BindingUnhealthy`/`MarkBindingUnhealthy`). The joint commit also carries `OAuthTokenSource` implementing
   all three new methods, the extended `staticTokenSource`, the shared 401 machinery (M2), and the
   `Publish` 401/403/unhealthy switch + service routing + metrics + wiring (M3, merged plan C1). Any other
   reading — M1 as a separate earlier or later interface commit, or "sibling folds the cooldown methods
   immediately after" — re-opens FM-12: the interface would be touched twice, and both branches adding
   `ErrTokenRejected`/`ErrTokenScopeMissing` produce a loud merge conflict. No third signature may be
   introduced; this campaign never re-touches the interface.
2. **Superset signature:** `Invalidate(SourceBinding) error` accepts every void call site; behavior and
   tests in the spec are signature-independent (§4 of the spec).
3. **Integration compile gate:** `staticTokenSource` gains `Invalidate` (and, when the merged interface
   lands, the cooldown methods) in the same commit as the interface, or `make test-integration` fails to
   compile — a failure the default done gate cannot see. The gate is the Makefile target
   `make check-integration-compile` (`go test -tags integration -run '^$' ./... && go vet -tags integration
   ./...`), wired into M1/M5 and the AGENTS.md done gate. Note: `go build -tags integration ./...` is
   **inert** for this purpose — `go build` never compiles `_test.go`; `go test -run '^$'` and `go vet` both
   type-check integration test files (verified: build stays silent on a missing
   `staticTokenSource.Invalidate`, test/vet exit 1 with the precise `missing method` diagnostic).
4. **No service/repository/metrics changes in this campaign's delta:** `service.AuditPublisher`
   (audit_ledger.go:24-25), `tenantBoundAuditPublisher`, `publicationOutcome`, drain routing, and
   `ControlPlaneMetrics` are untouched by M4. Under the M1 ≡ C1 joint commit the sibling's routing lands
   in the *same commit* that introduces the sentinels (merged plan §4), so **no transient
   generic-ladder window exists**: `ErrTokenScopeMissing` is routed to
   `errAuditTenantUnbound` (pending/`audit.binding_unavailable`, never dead-letter) from the commit that
   adds it; `ErrTokenRejected` follows the generic ladder (`pending`/`audit.publication_failed` →
   `dead_letter` at `maxAttempts` = 8) — deliberately, per the merged plan: the refetched token
   self-heals, DLQ is the correct terminal for a persistent 401.
5. **202/receipt contract byte-unchanged** (REQ-5): identical `event_id`/`idempotency_key` across
   invalidation/refetch; governance dedup undisturbed.
6. **No new config keys, no migrations, no proto, no `internal/pkg/metrics` import** (REQ-6): connector
   imports remain stdlib + `golang.org/x/sync` + `internal/domain`.
7. **Baseline pinning:** the tree's uncommitted conflict-as-success (`validateReceipt`), the
   `audit_ledger.go` loop removal, and claim-based dispatch are the behavioral baseline this design assumes
   (E2/E6). They must be committed before this work ships (M0); at HEAD, E6's "sole `Publish` caller"
   precondition does not hold and direction #1's removal is a prerequisite.
8. **File/function limits** (REQ-6): oauth.go 217 → ~265 (epoch + invalidate + classify + hook helpers,
   all ≤ 50 lines / cyclomatic ≤ 10); publisher.go 272 → ~300; types.go 121 → ~150. All under 400; split
   into `oauth_errors.go` only if oauth.go crosses 400.
9. **TTL policy unchanged** (REQ-1): 24 h cap and `refreshSkew` untouched; the epoch is an invalidation
   guard, not fencing; process restart resets it harmlessly (cache is process-local and empty).

## 4. Failure modes

| # | Scenario | Behavior | Design guard |
|---|---|---|---|
| FM-1 | In-flight fetch (epoch e0) completes after `Invalidate` bumped to e1 | `cacheToken(wire, e0)` sees `e0 != s.epoch`, discards; e0 waiters get `errEpochDiscarded` → re-enter once against e1, coalescing into the fresh flight | `cacheToken` Lock-guarded epoch compare is the authoritative anti-poisoning guard (REQ-2) |
| FM-2 | Concurrent 401s from N publishes | N `Invalidate` calls are idempotent; epoch monotonic; all post-invalidation callers join the latest key → **at most one fetch per distinct epoch key** ("exactly one refetch" is the barrier-synchronized steady state, T-10.1b.1's `/token == 2`); a spread-out storm may start intermediate refetches that later invalidations discard — harmless, and each `accessToken` stays ≤ 2 joins | epoch-keyed `DoChan`; T-10.1b |
| FM-3 | 401 storm (repeated invalidations) | Re-entry bound of one; second consecutive discard → `ErrTokenUnavailable`; drain ladder (8 attempts) bounds total work; no livelock | **counter-based** re-entry (REQ-2, §2.2 hard constraint #2); T-10.1b.3 negative |
| FM-4 | Composition-root hook panics | Without recover-wrap: x/sync v0.20.0 re-raises in a fresh goroutine → **process crash** + waiter starvation. Residual: a `runtime.Goexit`-ing hook skips the waiter fan-out (singleflight.go:207-209) → transient starvation to ctx deadline, not a wedge; a blocking hook parks waiters for its duration (bounded by caller ctx deadlines — "keep the hook short" contract) | `notifyScopeRejected` arm/clear + defer-recover, exact order pinned in §2.3 |
| FM-5 | Hook calls `AccessToken` for the same tenant (self-join) | Without guard: parks on its own in-flight flight forever → permanent tenant outage. Guard failure mode: a non-deferred clear after a hook panic leaves the flag armed forever → permanent tenant wedge (fail-closed) | `hookRunning` atomic check at the join point; arm/clear pinned inside `notifyScopeRejected` (§2.3) |
| FM-6 | Events-endpoint 403 storm | No invalidate; cache retained; each attempt is one HTTP call; drain ladder caps at dead_letter until sibling routing moves it to `binding_unavailable`; scope fix self-heals | 403 branch (no `Invalidate`), REQ-4 |
| FM-7 | Token-endpoint scope rejection swallowed | Without C-B: caller sees `ErrTokenUnavailable`, drain retries uselessly to dead_letter (hook still fires — it runs in `fetchToken`, before the wrap) | `authorization` `errors.Is` propagation |
| FM-8 | `Invalidate` for unbound tenant / mismatched `SourceSystem` | No-op, nil (mirrors `AccessToken` `ErrTenantUnbound` validation) | validation before lookup (§2.2) |
| FM-9 | ~~`singleflight.Group` retains last result per key~~ — **premise false at v0.20.0** | `doCall` deletes the key on completion (`if g.m[key] == c { delete(g.m, key) }`, singleflight.go:203-205): no retained last result per key in this version (that was pre-`Forget` behavior). The row's "optional `Forget` hygiene" is moot; no `Forget` call anywhere | none needed |
| FM-10 | Token-endpoint error body unparseable, unknown code, or status ≠ 400 | `ErrTokenUnavailable: HTTP %d`; no hook, no invalidate, no mark (merged plan §3 table; sibling FM-4 boundary). Events-endpoint 401 classification remains status-driven (`ErrTokenRejected`, §2.4) | `classifyTokenError` best-effort parse, **400-gated** (REQ-4 narrowed by the merged plan); pinned by the T-10.1d.3 negatives |
| FM-11 | Interface commit without `staticTokenSource` update | `make test-integration` compile break, invisible to default gate and to `go build -tags integration` (no `_test.go` compiled) | `make check-integration-compile` in M1/M5 gates and AGENTS.md done gate (§3.3) |
| FM-12 | `Invalidate` lands with a non-merged signature | Compile conflict with sibling campaigns on merge | §3.1 fixed-once interface |

## 5. Migration steps (ordered)

- **M0 — commit the working-tree prerequisites (prerequisite, not this campaign), with an explicit file
  enumeration:** (1) `internal/service/audit_ledger.go` — direction #1's loop removal in
  `auditLedgerService.Record` (:66-82 becomes repo.Append + `ObserveAuditIngestFailure` only); (2)
  `internal/connector/auditgovernance/publisher.go` — conflict-as-success in `validateReceipt` (the
  runtime half); (3) `internal/connector/auditgovernance/publisher_test.go` — the +103 lines of
  conflict-receipt tests (`TestPublisherConflictReceiptIsIdempotentSuccess`, :102,
  `TestPublisherConflictReceiptMustEchoTheFact`, :152): M0 must commit **both** halves of publisher.go +
  publisher_test.go together, or the "7 tests unchanged" baseline (§6) stays dirty and undefined; (4)
  `internal/service/callback_integration_test.go` — **untracked at HEAD**; it anchors `staticTokenSource`
  (:64-75), so M1's integration-fake extension has no committed anchor without it; (5) the claim-based
  dispatch reorganization (`internal/service/callback_audit.go` + untracked
  `internal/repository/audit_publication.go`). After M0, `publishAuditFact` (callback_audit.go:109-125) is
  the sole `Publish` caller and E6 holds at HEAD. **Hygiene:** the tree is 139 dirty entries (69 modified
  + 70 untracked), incl. an untracked compiled `sink` binary and `.pi-batch.lock` at the repo root plus
  tooling dirs (`.claude/`, `.qwen/`, `.skills/`, `ai-dev/`); M0 is a **scoped `git add`** (the enumerated
  modified + untracked files only), never `git add -A`; gitignore `sink` and `.pi-batch.lock`.
  Gate: `make test && make lint && make build && make check-integration-compile`.
- **M1 — joint interface commit, M1 ≡ C1 (pinned here §3.1 and in the merged plan §6):** the full
  five-method merged interface (`Invalidate(SourceBinding) error` + `ErrTokenRejected`/`ErrTokenScopeMissing`
  from this design; `BindingUnhealthy`/`MarkBindingUnhealthy` from the sibling) + `OAuthTokenSource`
  implementing all three new methods + `staticTokenSource` extended — **one commit authored jointly by both
  campaigns; the interface is touched exactly once, never again**. This design's files in the joint
  commit: types.go + oauth.go + internal/service/callback_integration_test.go (untracked today; M0
  commits it or M1 adds it — an atomic commit is the only state where `check-integration-compile` is
  green AND the default gate is green); the sibling contributes the `Publish` 401/403/unhealthy switch,
  service routing, metrics, and app wiring in the same commit (merged plan C1). The shared 401 machinery
  (M2) and the 401/403 switch (M3) fold into this commit; `OAuthConfig.OnScopeRejected` is **not** part of
  M1 — it lands with the classification step (M4 ≡ C2).
  Gate: `make test && make lint && make check-integration-compile` + `go test -race
  ./internal/connector/auditgovernance/...`; run `make test-integration` once (the joint commit touched
  the integration fake).
- **M2 — epoch machinery — folded into M1 ≡ C1:** the merged plan's C1 explicitly carries the epoch /
  epoch-keyed singleflight / `cacheToken` discard guard (the two specs' 401 machinery is identical and
  needs no reconciliation), so there is no separate epoch commit. This campaign's in-package verification
  of it (T-10.1a/b tests) lands with M4's suite per merged plan §8. The §2.2 hard constraints (RLock
  released before `DoChan`; counter-based re-entry; closure captures the keyed epoch and keeps the inner
  `cachedToken` re-check) are implementation rules for the joint commit's machinery, applied by whichever
  campaign authors it — and they are exactly the surface `-race` exists for (`go test -race
  ./internal/connector/auditgovernance/` at M1/M5; epoch reads in tests only in quiescence).
- **M3 — Publish 401/403 branch — folded into M1 ≡ C1:** the joint commit's `bindingFor`/`Publish`
  401/403/unhealthy switch covers 401 → `Invalidate` + `ErrTokenRejected` and 403 →
  `MarkBindingUnhealthy` + `ErrTokenScopeMissing` (cache retained). The `authorization` propagation (C-B)
  moves to M4: it is load-bearing only for token-endpoint classification, which lands in M4 (hazard
  companion §2.2).
- **M4 — design delta ≡ merged plan C2 (purely additive):** 400-gated `classifyTokenError` (§2.2);
  `OAuthConfig.OnScopeRejected` field; `notifyScopeRejected` (recover-wrap + `hookRunning` guard, exact
  defer order per §2.3); `newTenantTokenSource` gains `tenantID`; `authorization` propagation (C-B); the
  full in-package test suite (T-10.1a/b/c/d adjusted per merged plan §3/§4/§8, T-10.1e/f per the hazard
  companion, plus the §6 negatives). No interface, signature, or routing-table change.
  Gate: `make test` with the entire §6 suite green (+ `-race` at M5).
- **M5 — full done gate:** `make test && make lint && make lint-strict && make check-file-sizes && make
  check-integration-compile && make build` plus `go test -race ./internal/connector/auditgovernance/...`
  (the T-10.1b concurrency tests need the race detector to earn their keep; local, no Docker).
  `make test-integration` run once after M1 (interface touched the integration fake) — the spec's
  "no test-integration needed" is correct only for the *runtime* (no SQL/storage change), not for
  *compilation*.
- **M6 — verification no-op:** all sibling content (cooldown methods, service routing, `ObserveAuditScopeMissing`
  metrics wiring, `SetControlPlaneMetrics` extension) lands inside M1 ≡ C1, not after this campaign.
  Remaining follow-up: the composition-root hook binding (merged plan §5.1 — `OnScopeRejected` closure over
  `controlMetrics.ObserveAuditScopeMissing`), which consumes M4's field. M6 re-runs the M5 gate on the
  merged tree and confirms the interface was touched exactly once (M1) with zero further edits and no third
  signature.

## 6. Testable acceptance mapping

All tests in-package (`package auditgovernance`), `httptest` loopback (`AllowInsecureLoopback: true`),
mutex-guarded/`atomic.Int32` counters on `/token` and `/api/v1/events` handlers, in-package cache/epoch
assertions via `tokens.bindings[tenantID]`, `now` injection (oauth.go:79-84). **"Existing 7 tests
unchanged" is counted against the M0-pinned tree (5 at HEAD `13e4676` + the 2 M0 conflict-receipt tests,
publisher_test.go:102/:152); against HEAD alone the claim would read "5 tests" — M0 must land first, and
a cheap regression guard is `git diff M0..M5 -- publisher_test.go` showing only added hunks.** Use distinct
token values across responses (T1/T2) so the bearer assertions are non-vacuous (hazard companion §5.3).
Under `-race` (M1/M5 gates), epoch reads are lock-free and must only be read in quiescence.

| Acceptance | Test | Assertions (all concrete, no time-travel) |
|---|---|---|
| T-10.1a | `TestPublish401InvalidatesAndRefetchesExactlyOnce` | 1st `Publish` → `errors.Is(err, ErrTokenRejected)`, events counter == 1; immediately `cachedToken()` == `("", false)` and `epoch` incremented by exactly 1 (stall eliminated); 2nd `Publish` → nil, `/token` counter == **2** total, `Authorization` == `Bearer <refetched>`, `event_id`/`idempotency_key` == `fact.ID.String()` on both requests |
| T-10.1b | `TestConcurrentPublishesAfter401CoalesceToOneFetch` (N = 8) + `TestStaleInflightFetchDiscardedByEpoch` (**rewritten, two-stage handler**) + `TestSecondConsecutiveDiscardIsTerminal` (FM-3) | (1) all N succeed, `/token` == 2 total, all N requests carry the refetched token (distinct T2 value); (2) **two-stage blocking `/token` handler** (POST #1 gated on `ch1`, POST #2 on `ch2`): the cache must be empty first — an explicit `Invalidate` before the blocking call, because with a primed cache `accessToken` returns the cached token and never reaches `DoChan`; start one `Publish` → POST #1 in flight → `Invalidate` mid-flight (epoch e0→e1) → release `ch1` → the stale e0 result is discarded (`errEpochDiscarded`; the e0 token never enters the cache) → re-entry issues POST #2, blocked on `ch2` → **now** assert `cachedToken() == ("", false)` deterministically → release `ch2` → the `Publish` returns with the e1 token; `/token` == 2 total; final cache == T2 (never T1); epoch +2 with the mid-flight delta exactly +1; (3) same two-stage fixture, FM-3 terminal bound: with POST #2 blocked, `Invalidate` again (e1→e2), then release `ch2` → second consecutive discard → `Publish` returns `ErrTokenUnavailable`; `/token` == 2 total (no third POST); cache empty; epoch +2 |
| T-10.1c | `TestGovernance401TriggersImmediateRefreshAndRedelivery` | stateful events handler 401 → 202; first `Publish` → `ErrTokenRejected`, cache empty; second `Publish` → nil, `/token` == 2, identical `event_id`/`idempotency_key`; no `refreshAt` manipulation |
| T-10.1c (new negative) | `TestPersistentEvents401DoesNotRetryInsidePublish` | stateful always-401 events handler: every `Publish` → `ErrTokenRejected`, `epoch` +1 per publish, exactly one token POST and one events call per `Publish` — no in-`Publish` retry (REQ-3); the drain ladder (service-side, out of scope) bounds the rest |
| T-10.1d | `TestEvents403IsScopeMissingWithoutInvalidate` + `TestTokenEndpointInvalidScopeFiresHookOnce` + `TestScopeClassificationNegatives` | **T-10.1d.1 rewritten-by-C-C** (the requirements' hook-on-403 assertion is superseded — §1 C-C, merged plan §8): events 403 → `errors.Is(err, ErrTokenScopeMissing)`, `!errors.Is(err, ErrTokenRejected)`; cache retained, epoch unchanged, **no connector hook firing** (the alerting requirement moves to the sibling's service-side `ObserveAuditScopeMissing` counter on `aero_control_audit_scope_missing_total{source=...}`); additionally `BindingUnhealthy(binding)` == true (cooldown set by the joint-commit switch). T-10.1d.2: token 400 `{"error":"invalid_scope"}` → `ErrTokenScopeMissing` via `Publish` (C-B propagation), hook recorder == 1 firing with tenant ID. T-10.1d.3 negatives: `/token` 500 → `ErrTokenUnavailable`, no hook; **token 401 + `{"error":"invalid_scope"}` body → `ErrTokenUnavailable`, no hook (400-gate pin, FM-10)**; **token 400 + valid JSON with unknown code (e.g. `{"error":"unauthorized_client"}`) → `ErrTokenUnavailable`, no hook**; nil hook no panic; events 401 → `ErrTokenRejected`, no scope hook |
| T-10.1e/f (new; numbered per the hazard companion §5.1/§5.2 and merged plan §8) | `TestPublisherSurvivesPanickingScopeHook` + `TestScopeHookReentrantAccessFailsFast` + `TestScopeHookCrossTenantAccessIsNotBlocked` | panicking hook → `Publish` returns `ErrTokenScopeMissing`, process survives, coalesced waiters all receive the error, source functional afterwards; hook calling `AccessToken` same tenant → `ErrTokenUnavailable` fail-fast (`hookRunning`), no permanent park; cross-tenant `AccessToken` from a hook is not blocked (guard scope pinned) |
| T-10.1g (new) | `TestInvalidateUnboundAndMismatchedBindingAreNoOps` (FM-8, REQ-1) | unbound tenant → nil, no panic; mismatched `SourceSystem` → nil **and token retained, epoch unchanged**; valid binding with empty cache → still bumps epoch (guards the in-flight race) |
| Regression | `TestAuthorizationPropagatesScopeMissing` (C-B): stub `TokenSource` returning `ErrTokenScopeMissing` → `Publish` returns it, not `ErrTokenUnavailable` | `errors.Is` distinctness across all three sentinels |
| Compile | `var _ TokenSource = (*OAuthTokenSource)(nil)` + integration fake update (M1 ≡ C1, one atomic joint commit) | default gate green **and** `make check-integration-compile` green — the latter is the FM-11 gate |

## 7. Out of scope (unchanged from the spec §5)

Service-layer routing/metrics wiring, binding-unhealthy short-circuit/cooldown, direction #1/#3 items,
retry loops inside `Publish`, `internal/connector/billing`, and any third `Invalidate` signature.
