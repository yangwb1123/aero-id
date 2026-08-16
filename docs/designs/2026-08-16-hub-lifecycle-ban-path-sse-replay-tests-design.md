# Design — Close hub lifecycle and ban-path test holes + SSE replay regression tests

- **Date:** 2026-08-16
- **Direction slug:** hub-lifecycle-ban-path-sse-replay-tests
- **Source analysis:** `docs/auto/analyses/internal-hub-8b6971ca.json` (entry 1 of 3; entries 2 and 3 — same-ms SSE-ID collision, hub metrics — are **out of scope**)
- **Value / Risk / Effort / Confidence:** 8 / 8 / 3 / 9
- **Kind:** test-only regression-net direction. Zero production-code changes.

---

## 1. Verification verdict on the evidence

Every citation in the source analysis was re-checked against the tree at `/home/u1/aero-id` (verified 2026-08-16). **Status: VERIFIED.** The two line-number corrections claimed by the evidence are themselves confirmed, and the evidence's own correction of the `internal/app/new.go:405` citation is accurate. No new discrepancies found beyond those already flagged.

| Claimed evidence | Repository reality (verified) | Verdict |
|---|---|---|
| `hub.go:113-125` Disconnect closes channels + deletes uid entry | Doc comment 113-115; `func (h *Hub) Disconnect` at **116-129**: `subs.Load` → early return if absent; under `us.mu` close+delete each entry; `h.subs.Delete(uid)` after unlock | VERIFIED (body 116-129, per evidence's own correction) |
| `hub.go:133-152` Close | `Close` at **133-146**: `closed.Store(true)` (134), `subs.Range` closing+deleting every entry, `h.subs.Delete(key)`; second call ranges an empty map → no-op | VERIFIED (func 133-146; evidence's correction accurate) |
| `hub.go:80-110` unsub close-under-lock contract | unsub closure at **74-91**: under `us.mu`, `if c, ok := us.subs[id]; ok { delete; close(c) }` (map-membership guard, no double-close), deletes uid entry when last subscriber. Non-blocking pub send path at **96-111** (send under `us.mu`, `default:` drop) | VERIFIED (unsub 74-91; send 96-111) |
| `admin.go:18-34,179` HubDisconnector; Disconnect after Ban persists | Interface comment 18-19, `type HubDisconnector` at **20**, `hub` field at 28, `NewAdminHandler` at **34** (accepts `[]HubDisconnector`, nil-safe). `BanUser`: `h.bans.Ban(...)` first, then `if h.hub != nil { h.hub.Disconnect(req.UID) }` at **179** | VERIFIED (interface type line is 20, trivially inside the cited 18-34 window) |
| `internal/app/new.go:405` (hub wired into NewAdminHandler) | **Confirmed incorrect, as the evidence says.** `new.go` is 308 lines; no line 405. Hub created at `new.go:158` (`sseHub := hub.New()`); wiring at `internal/app/handlers.go:73` (`NewEventsHandler(deps.sseHub, deps.notifRepo)`) and **75-77** (`NewAdminHandler(deps.svcs.adminSvc, []service.BanService{...}, []httpapi.HubDisconnector{deps.sseHub}, deps.svcs.permissionChecker)`). `deps.notifRepo` satisfies `MissedNotifFetcher` via `repository.NotificationRepository.ListSince` | VERIFIED w/ correction (evidence's fix accurate) |
| `hub_test.go` 156 lines, six tests, no Disconnect/Close-twice/after-close-Publish | 156 lines. Tests: `TestHub_PubSub`, `TestHub_MultipleSubscribers`, `TestHub_Unsubscribe`, `TestHub_PublishNoSubscriber`, `TestHub_PublishSlowConsumerDrops`, `TestHub_Close`. Gap confirmed | VERIFIED |
| `events_test.go` 61 lines, no replay coverage | 61 lines. Only `TestEventsHandler_NewAndRegister`, `TestEventsHandler_Stream_Unauthenticated`. `Stream` (44-91) and `replayMissed` (93-113) uncovered | VERIFIED |
| `events.go` replay semantics | `Stream` at 44-91: `Last-Event-ID` header (61) → `?last_event_id` fallback (63) → `replayMissed` (65-68) before live loop. `replayMissed` at 93-113: `strconv.ParseInt`; silent return on parse error or `millis <= 0`; `ListSince(ctx, uid, since, 50)`; emits `id: <CreatedAt.UnixMilli()>\nevent: <Type>\ndata: <json>\n\n` per notification in fetcher order | VERIFIED |
| `repository/notification.go` `ListSince` | Lines ~157-172: `WHERE recipient_uid = ? AND created_at > ?` (strict), `ORDER BY created_at ASC, id ASC`, `LIMIT limit`; signature matches `MissedNotifFetcher` | VERIFIED |
| `router_test.go:19` `okValidator` | `okValidator` defined in `package http_test` (same package as `events_test.go`), `ValidateToken` returns `"u1", nil`; directly reusable | VERIFIED |
| `middleware.Timeout(0)` no-op; empty `RouterConfig{}` → no rate limiting | `timeout.go`: `if d <= 0 { return func(c *gin.Context) { c.Next() } }`. `router.go:121-129` `rateLimitMiddleware` returns `nil` for empty config; `NewRouter` forces `gin.ReleaseMode` | VERIFIED |
| `service.ActorFrom`, `hub.Publish(uid, eventType, data string, eventID int64)`, `domain.Notification{CreatedAt, Type}` | `service.ActorFrom` at `service/service.go:37`; `*Hub.Publish` in `hub/adapter.go` (4-arg, satisfies `service.NotificationPusher`); `domain.Notification` has `CreatedAt time.Time`, `Type string` (untagged JSON fields) | VERIFIED |
| Analysis file: 3 entries, entry 1 in scope | `internal-hub-8b6971ca.json` contains 3 entries; entry 1 title matches; entries 2 (cursor lossless/collision) and 3 (observability) are separate | VERIFIED |

**Net verdict:** evidence is accurate; the only substantive correction (the `new.go:405` citation) was already made by the evidence itself and is confirmed. This design is a faithful refinement of the requirements spec with no open verification risk.

---

## 2. Problem and target behavior (unchanged from spec)

`hub.Disconnect` (the admin-ban force-close seam reached via `admin.go:179`) has zero test coverage; `hub_test.go` also leaves Close-twice, unsub-after-Close/Disconnect, and Publish-vs-Close (send-on-closed-channel panic class) untested. Separately, the SSE `Last-Event-ID` replay logic in `events.go` (`Stream`/`replayMissed`) is entirely untested.

The design pins the **existing** contract only. No production behavior changes; every assertion documents the current semantics. If a new test exposes a genuine defect, the defect is recorded as a finding and the fix becomes a separate follow-up direction — this direction stops at the finding.

### Contract under test (source anchors verified in §1)

| # | Contract | Anchor |
|---|---|---|
| C1 | `Disconnect(uid)` closes every subscriber channel for uid and removes the uid entry; a later `Subscribe(uid)` yields a fresh, **open** channel (hub not closed) | `hub.go:116-129` |
| C2 | `Disconnect(uid)` with no active subscribers is a no-op (no panic) | `hub.go:117-119` |
| C3 | A subscriber's `unsub` after its channel was closed by `Disconnect`/`Close` is safe (map-membership guard skips the close) | `hub.go:74-78` |
| C4 | `Close()` is idempotent (registry drained, `closed` already true on second call) | `hub.go:133-146` |
| C5 | `Publish` after `Close` must not panic and must deliver to no one (registry drained) | `hub.go:96-111`, `hub.go:133-146` |
| C6 | `replayMissed` with valid positive unix-millis cursor calls `fetcher.ListSince(ctx, uid, since, 50)` with `since = UnixMilli(cursor)`, writes one SSE block per notification in fetcher order (`id:` = `CreatedAt.UnixMilli()`) **before** live-stream events | `events.go:61-68, 93-113`; repo contract `created_at ASC, id ASC` |
| C7 | Non-numeric or `<= 0` cursor skips replay silently (no fetch call, connection continues) | `events.go:95-97` |

---

## 3. API changes

### Production API surface
**None.** No changes to exported or unexported symbols, routes, protobuf, OpenAPI, config, or wire formats. `hub.go`, `hub/adapter.go`, `events.go`, `admin.go`, `internal/app/*`, `internal/repository/*`, `internal/service/*` are untouched.

### Test-surface "API" (the only additions)
Confined to two test files; all new identifiers are package-scoped test helpers/test functions:

| New identifier | File / package | Kind |
|---|---|---|
| `TestHub_Disconnect` | `internal/hub/hub_test.go` (`hub_test`) | test func |
| `TestHub_DisconnectNoSubscriber` | same | test func |
| `TestHub_CloseTwice` | same | test func |
| `TestHub_UnsubAfterClose` | same | test func |
| `TestHub_PublishAfterClose` | same | test func |
| `TestEventsHandler_Stream_Replay` | `internal/handler/http/events_test.go` (`http_test`) | test func |
| `TestEventsHandler_Stream_ReplayInvalidCursor` | same | test func |
| `fakeNotifFetcher` (name TBD by implementer) | `internal/handler/http/events_test.go` | package-private fake implementing `httpapi.MissedNotifFetcher` |

`TestEventsHandler_Stream_Replay` and `..._ReplayInvalidCursor` reuse the existing `okValidator` from `router_test.go` (same package) — **do not redeclare it** (compile error). `gin.SetMode(gin.TestMode)` is mirrored from sibling tests for convention only; `NewRouter` forces `ReleaseMode` internally.

---

## 4. Test design (concrete)

### 4.1 Hub lifecycle tests — `internal/hub/hub_test.go`

All five are fully sequential (no goroutines) and deterministic. Pass criterion for the panic-class tests is "test process did not panic" — Go's runner fails on panic; **no recover/assert-panic scaffolding**.

1. **`TestHub_Disconnect`** (pins C1 + C3-Disconnect):
   1. `h := hub.New()`; `ch, unsub := h.Subscribe("alice")`.
   2. `h.Disconnect("alice")`.
   3. Assert `ch` closed: `select { case _, ok := <-ch: ok == false; case <-time.After(time.Second): fail }`.
   4. `unsub()` must not panic (C3: guard skips close of already-closed channel).
   5. `ch2, unsub2 := h.Subscribe("alice")`; `h.Publish("alice", "msg", "hello", 0)`; assert value arrives within 1s with `Type == "msg"`, `Data == "hello"` — observable proof the uid entry was deleted (C1), not merely closed (a closed-but-present entry could not deliver).
   6. `defer unsub2()`; `h.Close()` at the end.
2. **`TestHub_DisconnectNoSubscriber`** (pins C2): `h := hub.New()`; `h.Disconnect("nobody")`; `h.Disconnect("")` — must complete without panic; optionally follow with `h.Publish("nobody", ...)` to prove the registry still functions.
3. **`TestHub_CloseTwice`** (pins C4): `h := hub.New()`; subscribe one uid (first Close has work); `h.Close()`; `h.Close()` — no panic; assert the pre-close channel is closed after the first `Close` (receive → `ok == false`).
4. **`TestHub_UnsubAfterClose`** (pins C3-Close): `h := hub.New()`; `_, unsub := h.Subscribe("dave")`; `h.Close()`; `unsub()` — no panic (double-close class).
5. **`TestHub_PublishAfterClose`** (pins C5): `h := hub.New()`; `ch, _ := h.Subscribe("frank")`; `h.Close()`; `h.Publish("frank", "evt", "data", 1)` — no panic; assert no delivery: receive on `ch` within 200ms yields closed-channel zero value with `ok == false` (never a live event); optionally `h.Publish("other-uid", ...)` after Close (no-op, no panic).

Why these are deterministic: every close in the implementation happens under `us.mu` with a map-membership guard; every send happens under the same mutex; `Close`/`Disconnect` delete registry entries so post-teardown `pub` iterates nothing. Tests must **prove absence of panic and correct teardown semantics**, never expect a panic.

### 4.2 SSE replay tests — `internal/handler/http/events_test.go`

**Fake fetcher** (package `http_test`):

```go
type fakeNotifFetcher struct {
    notifs   []*domain.Notification
    err      error
    gotUID   string
    gotSince time.Time
    gotLimit int
    calls    int
}
func (f *fakeNotifFetcher) ListSince(ctx context.Context, recipientUID string, since time.Time, limit int) ([]*domain.Notification, error) {
    f.calls++
    f.gotUID, f.gotSince, f.gotLimit = recipientUID, since, limit
    if f.err != nil { return nil, f.err }
    return f.notifs, nil
}
```

For the invalid-cursor test the fake records `called` (or fatals inside `ListSince`) so any fetch is a hard test failure.

**`TestEventsHandler_Stream_Replay`** (pins C6):
1. Notifications `n1` (CreatedAt `t1`) and `n2` (CreatedAt `t2`), `t1 < t2` in unix-millis, distinct types (`"a.b"` / `"a.c"`), distinct non-empty JSON payloads. Use exact millisecond values, e.g. `t1 = time.UnixMilli(1700000000123)`, `t2 = time.UnixMilli(1700000000456)`; cursor `1700000000000` strictly before `t1`. Avoid `time.Now()`-derived values to keep cursor/t1/t2 ordering airtight (timezone-independent because all comparisons are on `UnixMilli()`).
2. Router: `httpapi.NewRouter(zap.NewNop(), httpapi.RouterConfig{}, okValidator{}, httpapi.NewHealthHandler(), handler)` — `okValidator` gives actor `"u1"`.
3. `ctx, cancel := context.WithCancel(context.Background())`; `req := httptest.NewRequest("GET", "/v1/events", nil).WithContext(ctx)`; headers `Authorization: Bearer t`, `Last-Event-ID: 1700000000000`.
4. Serve in a goroutine (`go router.ServeHTTP(w, req)`); poll `w.Body.String()` under a 2s deadline until it contains both `"id: 1700000000123"` and `"id: 1700000000456"`.
5. Ordering with `strings.Index`: n1's block precedes n2's; each block carries `event: <n.Type>` and `data: <json>` where the expected data is `string(json.Marshal(n))` computed in-test (handler uses the same marshal, so this is deterministic).
6. Assert fetcher recorded `gotUID == "u1"`, `gotSince.UnixMilli() == 1700000000000`, `gotLimit == 50`.
7. "Before live stream": `h.Publish("u1", "live", "live-data", 9000)`; poll until body contains `"id: 9000"`; assert its index is **after** the n2 replay index.
8. `cancel()`; join the goroutine (bounded wait on a `done` channel); assert `w.Code == http.StatusOK`.

**`TestEventsHandler_Stream_ReplayInvalidCursor`** (pins C7):
1. Fake whose `ListSince` fails the test if invoked (`called` flag asserted, or `t.Fatal` inside).
2. Same router setup; run one request per cursor `"abc"`, `"0"`, `"-5"` — each with cancellable context, served in a goroutine.
3. Per cursor: sleep ~100ms (handler reaches live loop), assert `called == false`, assert body contains no `"id: "` block (no pings within the window — heartbeat is 30s, so the body stays empty), `cancel()`, join, assert `w.Code == http.StatusOK` (stream established; replay skipped without error).

**Determinism and leak rules (both tests):**
- `Stream` never returns on its own (30s heartbeat) → every request must be driven by a cancellable context and every poll bounded by a deadline; a blocking test is a failure.
- Every goroutine is joined after `cancel()` (the handler returns on `c.Request.Context().Done()`). Use `done := make(chan struct{}); go func(){ ...; close(done) }()` + `select` on `done` with a timeout.
- `w.Code` is stable once the handler calls `c.Status(http.StatusOK)` (write happens before the live loop), so the 200 assertion is valid after join.

---

## 5. Compatibility constraints

1. **No production behavior change:** all seven tests assert the existing contract; they must pass against the current implementation as-is. They are regression nets, not behavior drivers.
2. **No symbol collisions:** the five `TestHub_*` names do not collide with the six existing hub tests; the two `TestEventsHandler_*` names do not collide with the two existing events tests (verified by enumeration in §1).
3. **No new packages, no new dependencies, no migrations/SQL:** `check-migration-set` and `check-integration-compile` are unaffected by construction. No non-test files added (so `check-file-sizes` is unaffected; test files are exempt).
4. **Import surface:** test files may import stdlib (`context`, `encoding/json`, `net/http`, `net/http/httptest`, `strings`, `testing`, `time`), `github.com/gin-gonic/gin`, `go.uber.org/zap`, `internal/domain`, `internal/hub`, `internal/handler/http` — all already used by sibling tests in the same packages; `check-architecture` (dead-tree import ban, domain purity) is not implicated because no new production imports are added.
5. **`okValidator` reuse:** `events_test.go` is in `package http_test` alongside `router_test.go`; the validator is reused, not redeclared.
6. **Out-of-scope boundary honored:** no changes to `SSEEvent.ID` semantics, `ListSince` `>`-vs-`>=` boundary, relay batch size, or metrics wiring (analysis entries 2/3).

---

## 6. Failure modes

### 6.1 Failure modes the tests pin (production code under test)
| Class | Mechanism today | Test that pins it |
|---|---|---|
| Panic: double-close on `unsub` after `Disconnect` | map-membership guard under `us.mu` (`hub.go:74-78`) | `TestHub_Disconnect` step 4, `TestHub_UnsubAfterClose` |
| Panic: send on closed channel from `Publish` after `Close`/`Disconnect` | registry drained; `pub` iterates nothing (`hub.go:96-111, 133-146`) | `TestHub_PublishAfterClose` |
| Panic: `Close` twice | second call ranges empty map | `TestHub_CloseTwice` |
| Panic: `Disconnect` on absent uid | `subs.Load` early return | `TestHub_DisconnectNoSubscriber` |
| Stale registry entry: `Subscribe` after `Disconnect` returns a dead channel | entry deleted in `Disconnect` (`hub.go:129`) | `TestHub_Disconnect` step 5 |
| Replay regression: wrong `since`/uid/limit propagation, wrong ordering, replay after live events, or replay of invalid cursors | `events.go:93-113`, repo `created_at ASC, id ASC` | both events tests |

Any future refactor of `hub.go`/`events.go` that removes a guard, adds an unguarded close/send, changes cursor parsing, or reorders replay-vs-live output fails these tests — that is the intended regression net.

### 6.2 Failure modes of the tests themselves (flakiness/leak risks)
| Risk | Mitigation |
|---|---|
| Test hangs (Stream never returns; 30s heartbeat) | cancellable `context.WithCancel` per request; bounded poll deadlines (2s) and bounded join waits; fail on timeout |
| Goroutine leak (handler still running after test ends) | every goroutine joined after `cancel()`; `go test` leak detection would also hang, so bounded join is mandatory |
| Poll reading `httptest.ResponseRecorder` while handler writes | single writer + polling reader is benign under the repo's `make test` (no `-race`); note: a future `-race` run on the events tests would flag recorder/fake access — documented limitation, out of scope (acceptance does not enable `-race`); the hub tests are goroutine-free and `-race`-clean |
| Cursor arithmetic drift | use fixed `time.UnixMilli(...)` constants, never `time.Now()`-derived millis, so ordering and equality assertions are exact |
| 200-status race | handler writes the status via `c.Status(http.StatusOK)` before the live loop; assert after join |
| Fake-field read/write ordering | reads of `gotSince`/`gotUID`/`gotLimit` happen only after the body poll proved the fetch ran; for strictness the implementer may re-read after join (post-`cancel()`) — same values, no timing change |
| Unsubscribe-on-cancel double-close | implementation's `unsub` is guard-safe (C3); tests already pin this; no test-side `recover` scaffolding |

---

## 7. Migration steps

**None.** This is a test-only change set:

1. Add five tests to `internal/hub/hub_test.go` (append; keep existing six untouched).
2. Add fake fetcher + two tests to `internal/handler/http/events_test.go` (append; keep existing two untouched).
3. No SQL migrations, no config, no proto regeneration, no deployment/rollout steps. `check-migration-set`, `check-integration-compile`, `check-proto-sync`, and `check-file-sizes` are unaffected by construction.
4. Rollback = revert the two test files; `git status` must show only these two files modified.

---

## 8. Testable acceptance mapping

| Acceptance (from direction, made testable) | Verification |
|---|---|
| A1: `make test` green — all 8 pre-existing + 7 new tests pass | `make test` (done-gate gate #1) |
| A2: `TestHub_Disconnect` — channel closed, uid removed (fresh open channel delivers), pre-Disconnect `unsub()` no panic | `go test ./internal/hub/ -run '^TestHub_Disconnect$' -count=1 -v` |
| A3: `TestHub_DisconnectNoSubscriber` — no-op, no panic | same, `-run '^TestHub_DisconnectNoSubscriber$'` |
| A4: `TestHub_CloseTwice` — no panic; pre-close channel closed | same, `-run '^TestHub_CloseTwice$'` |
| A5: `TestHub_UnsubAfterClose` — no panic | same, `-run '^TestHub_UnsubAfterClose$'` |
| A6: `TestHub_PublishAfterClose` — no panic, no delivery | same, `-run '^TestHub_PublishAfterClose$'` |
| A7: `TestEventsHandler_Stream_Replay` — replay in created_at order before live events; correct `id:`/`event:`/`data:` blocks; `since`/`uid`/`limit` propagation (`"u1"`, cursor millis, 50); 200 | `go test ./internal/handler/http/ -run '^TestEventsHandler_Stream_Replay$' -count=1 -v` |
| A8: `TestEventsHandler_Stream_ReplayInvalidCursor` — `"abc"`, `"0"`, `"-5"` skip replay (fetcher not called), 200, stream stays up | `go test ./internal/handler/http/ -run '^TestEventsHandler_Stream_ReplayInvalidCursor$' -count=1 -v` |
| A9: lint/architecture gates stay green; no production files changed | `make lint && make lint-strict && make check-architecture`; `git diff --stat` shows only the two test files |

**Done-gate command for this direction:**

```bash
make test && make lint && make lint-strict && make check-architecture
go test ./internal/hub/ -run 'TestHub_(Disconnect|DisconnectNoSubscriber|CloseTwice|UnsubAfterClose|PublishAfterClose)' -count=1 -v
go test ./internal/handler/http/ -run 'TestEventsHandler_Stream_(Replay|ReplayInvalidCursor)' -count=1 -v
```

**Definition of done:** all seven new tests exist with the exact names above and pass; only `internal/hub/hub_test.go` and `internal/handler/http/events_test.go` are modified; the four gates stay green; scope remains entry 1 of the analysis (no SSE-ID collision work, no metrics work, no cursor-semantics change). If any test exposes a genuine defect, stop, record it as a finding, and defer the fix to a follow-up direction.
