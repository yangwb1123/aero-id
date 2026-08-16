# Design — Close `internal/pkg/httpx` test-coverage holes at its transport boundary

- **Date:** 2026-08-16
- **Status:** Design (proposal)
- **Module:** `internal/pkg/httpx`
- **Direction:** Test-only additions to `internal/pkg/httpx/httpx_test.go`; zero production-code changes
- **Input spec:** `docs/auto/runs/close-internal-pkg-httpx-test-coverage-holes-at--0787c617/artifacts/requirements-10762e10/requirements.md`
- **Contract anchors:** `AGENTS.md` L195 (`{"data": ...}` envelope), AGENTS.md never-leak error contract, `.golangci.yml:43–46` (test-file lint exemptions), `Makefile` `check-file-sizes` (`*_test.go` exempt)

## 1. Verification verdict

Every evidence claim was re-checked against the working tree on 2026-08-16:

| Claim | Check | Result |
|---|---|---|
| `httpx.go` = 76 lines; `WriteError` L30–41, `OK`/`Created` L43–45, `PageFromQuery` L50–61 (category block L57–59), `ParseIDFromParam` L66–76 | `wc -l` + read | ✓ exact |
| `httpx_test.go` = 117 lines, 8 tests, `ParseIDFromParam` untested | `wc -l` + read | ✓ exact |
| `errors.go` `Cause` L18; `Error()` renders cause L21–26; `Wrap` exists | read | ✓ exact |
| `Page` at `domain/shared.go:74–86`; `DefaultPageLimit=20`, `MaxPageLimit=100`; `Normalize` clamps `<=0 → 20`, `>100 → 100` | read | ✓ exact |
| `FileCategory` is a string type; plain conversion in `PageFromQuery` | read (`domain/file.go:31`) | ✓ |
| 15 `ParseIDFromParam` call sites, line-exact (account:297, aggregation:70, audit_ledger:75, callback:60/73, operation:87/101/115, replay:136, sync_job:56/65/74/102/129/143) | grep | ✓ all 15 |
| 22 non-test importers (17 handler + 5 middleware) + 2 httpx files = 24 | grep | ✓ |
| `domain.ID = idgen.ID = uuid.UUID`; `idgen.Parse` returns `(ID, error)` — valid-case comparison is exact-typed | read (`domain/shared.go:6`, `idgen.go:70`) | ✓ (spec omitted this; confirms REQ-1 mechanics) |
| `request_id` set via `c.Set("request_id", rid)` in `middleware/request_id.go` | read | ✓ |
| gin v1.10.0 `WriteJSON` = `json.Marshal(obj)` with **no trailing newline** (go.mod pins v1.10.0) | module cache read | ✓ (REQ-7 byte-pins are stable under default build) |
| `.golangci.yml:43–46` excludes `funlen`/`cyclop`/`lll` for `.*_test\.go$` | read | ✓ |
| `check-file-sizes` excludes `*_test.go` (`Makefile:69` recipe) | read | ✓ |
| Repository owns normalization + category filter (`page.Normalize()` then `WHERE category = ?`) | read (`repository/file.go:80–90`) | ✓ (REQ-6 layering rationale confirmed) |
| `apperr` constructors `BadRequest`/`NotFound`/`Conflict`/`Internal` + `.Wrap(cause)` exist | read | ✓ |

**Verdict: evidence is accurate.** The design below implements the spec as verified.

## 2. Design decision

No production API changes. The change set is a single test-file diff
(`internal/pkg/httpx/httpx_test.go`) that pins the existing transport contract at the
four exported helpers (`WriteError`, `OK`/`Created`, `PageFromQuery`, `ParseIDFromParam`).
The design treats these helpers as the pinned contract surface; the tests are the
mechanical enforcement of three documented invariants that today have no pin:

1. **Never-leak on error rendering** — `WriteError` must render only `Code`/`Message`
   (plus echoed `request_id`) and must never render `AppError.Cause` text or internal
   details, including for `nil` errors (which must fall to the generic 500 branch).
2. **`{"data": ...}` success envelope** — `OK`/`Created` must be byte-exact, not merely
   valid JSON.
3. **Layered pagination ownership** — `PageFromQuery` returns an *unnormalized*
   `domain.Page`; defaulting/clamping lives only in `domain.Page.Normalize()` at the
   repository layer.

Per-requirement test designs (all in `package httpx_test`, existing package):

### REQ-1 — `ParseIDFromParam` table test (new `TestParseIDFromParam`)

Three rows against `gin.CreateTestContext(w)`:

- **valid** — `c.Params = gin.Params{{Key: "id", Value: raw}}` with a canonical UUIDv7
  string; expected value precomputed as `expected, _ := idgen.Parse(raw)`; assert
  `got == expected` and `err == nil`.
- **missing** — params left empty (or populated under a different key); assert
  `stderrors.As(err, &ae)` is true, `ae.Code == "request.invalid_id"`, and
  `strings.Contains(ae.Message, "missing id param")`.
- **invalid** — `c.Params` holds `"not-a-uuid"`; assert same code and
  `strings.Contains(ae.Message, "invalid id param")`.

Error-type assertion via `stderrors.As` pins the `*AppError` contract; the message
substring is the discriminator between the two shared-code branches.

### REQ-2 — `WriteError(c, nil)` (new `TestWriteError_nilError`)

Assert `w.Code == 500`, body decodes to `error.code == "internal.unexpected"` and
`error.message == "internal server error"`. `stderrors.As(nil, &ae)` is false, so this
pins the generic-500 fall-through (`httpx.go:36–41`).

### REQ-3 — `WriteError` never leaks `Cause` (new `TestWriteError_doesNotLeakCause`)

```go
const causeText = "secret internal detail: dial tcp 10.0.0.5:5432"
appErr := apperr.Conflict("audit.chain_invalid", "chain verification failed").Wrap(errors.New(causeText))
```

Assert `w.Code == appErr.HTTP` (409), decoded `code == appErr.Code`, `message == appErr.Message`,
and `bytes.Contains(w.Body.Bytes(), []byte(causeText)) == false`. `causeText` is chosen to
**not** be a substring of `"chain verification failed"` so the negative containment is
unambiguous. This distinguishes `WriteError`'s JSON rendering from `AppError.Error()`
(which does render cause — `errors.go:21–26`).

### REQ-4 — `WriteError` `request_id` passthrough (new `TestWriteError_echoesRequestID`)

`c.Set("request_id", "req-test-1234")` (mirrors `middleware/request_id.go:28`), then
`WriteError(c, apperr.NotFound("test.not_found", "nope"))`; assert decoded
`error.request_id == "req-test-1234"`. One branch suffices — `rid` is read once at
`httpx.go:31` for both the AppError and nil paths.

### REQ-5 — `PageFromQuery` category mapping (new `TestPageFromQuery_categoryMapping`)

`c.Request = httptest.NewRequest(http.MethodGet, "/?category=docs", nil)`;
assert `p.Category == domain.FileCategory("docs")` (plain typed conversion, no enum
validation at this layer). Pin the httpx conversion contract only; the repository
`WHERE category = ?` filter is out of scope here.

### REQ-6 — unnormalized limit passthrough (new `TestPageFromQuery_unnormalizedLimitPassthrough`)

Table rows `?limit=-5 → p.Limit == -5` and `?limit=999 → p.Limit == 999`, asserting the
raw `strconv.Atoi` value with **no** `Normalize` applied. Complements the existing
`?limit=abc → 0` test (Atoi-failure branch). This pins the documented layering: clamping
(`DefaultPageLimit = 20`, `MaxPageLimit = 100`) is repository-owned.

### REQ-7 — byte-pin the `{"data": ...}` envelope (augment `TestOK_returnsData`, `TestCreated_returns201`)

Replace the `json.Valid`/status-only assertions with exact byte pins (keep the status
assertions; drop `json.Valid` since byte equality subsumes it):

- `httpx.OK(c, gin.H{"key": "value"})` → body `{"data":{"key":"value"}}`, status 200.
- `httpx.Created(c, "created")` → body `{"data":"created"}`, status 201.

Stability argument (verified): gin v1.10.0 `render.WriteJSON` writes `json.Marshal`
bytes directly with no trailing newline; `gin.H` is a map, so `encoding/json` sorts the
single keys deterministically; payloads are small and single-keyed. The pins are stable
for the default build (see §3 constraint C4).

## 3. Compatibility constraints

- **C1 — No production behavior change.** `httpx.go`, `errors.go`, handlers, and
  middleware are untouched; HTTP and gRPC behavior is bit-identical. `make build` output
  is unchanged.
- **C2 — Existing tests keep passing.** REQ-7 augments the two existing well-formedness
  tests in place (strictly stronger assertions); the other six tests are additive
  (`t.Run` subtests and new top-level functions). No existing assertion is weakened.
- **C3 — Byte-pins are pinned to gin v1.10.0** (go.mod). A future gin upgrade that
  changes the JSON renderer (e.g. appends a newline or switches to an indented encoder)
  will trip REQ-7 — that is the intended tripwire; note it in the upgrade PR rather than
  silently relaxing the pin.
- **C4 — Default build tags only.** gin's `internal/json` dispatches to
  sonic/jsoniter/go_json under build tags; the byte-pins are verified under the default
  `encoding/json` build, which is what `make test` runs. Tagged builds are out of scope.
- **C5 — `request_id` `omitempty`.** The absent-request_id case (field omitted) is not
  tested; the acceptance list covers only the present branch.
- **C6 — Cause-string choice.** The REQ-3 cause text must not be a substring of the
  message text (see REQ-3) to keep the containment negative meaningful.
- **C7 — File-size gate.** `httpx_test.go` is currently 117 lines; the additions total
  ≈ 140–160 lines → ≈ 260–280 lines final, well under the 500-line gate (and test files
  are exempt from `check-file-sizes` anyway). `httpx.go` remains 76 lines.

## 4. Failure modes (what each test guards)

| Failure mode | Guard |
|---|---|
| `WriteError` regresses to render `Error()` (leaks cause/stack/URL internals) | REQ-3 — fails at the one seam where AppErrors cross the transport boundary |
| `WriteError` receives `nil` (programming error) and starts leaking/panicking instead of the generic 500 | REQ-2 |
| `WriteError` drops `request_id` echo (log-correlation/observability regression) | REQ-4 |
| `ParseIDFromParam` changes code/message contract — 15 live call sites (account/aggregation/audit/callback/operation/replay/sync) and any client error handling depend on `request.invalid_id` + `missing|invalid <name> param` | REQ-1 |
| `PageFromQuery` grows a second, divergent clamp (violates the documented unnormalized contract; silently diverges from repository-owned defaults if `Normalize` or `DefaultPageLimit` changes) | REQ-6 |
| `PageFromQuery` category mapping drifts from the plain conversion contract | REQ-5 |
| Success envelope renames `data`, drops it, or gains a trailing newline — `json.Valid` alone cannot catch this | REQ-7 (AGENTS.md L195) |

Note on REQ-6's guard strength: today a hypothetical clamp in `PageFromQuery` would
produce the same final values as repository `Normalize` for `-5`/`999`. The pin is
against the *documented layering contract* (httpx must stay unnormalized) — it trips the
moment the layer duplicates normalization, which is exactly the drift the AGENTS.md
"`0` = code default unless documented" boundary is meant to prevent.

## 5. Migration / rollout steps

Test-only change; no data migration, no deployment, no rollout.

1. Edit `internal/pkg/httpx/httpx_test.go` (single-file diff): add imports
   (`bytes`, `errors` (std), `strings`, `stderrors` if aliasing, `idgen`, `domain`),
   add `TestParseIDFromParam`, `TestWriteError_nilError`, `TestWriteError_doesNotLeakCause`,
   `TestWriteError_echoesRequestID`, `TestPageFromQuery_categoryMapping`,
   `TestPageFromQuery_unnormalizedLimitPassthrough`; augment
   `TestOK_returnsData`/`TestCreated_returns201` with byte pins.
2. `make test` — unit only, no Docker, no network.
3. `make lint && make lint-strict` — test files are exempt from `funlen`/`cyclop`/`lll`;
   `vet` must stay clean.
4. `make check-file-sizes && make build` — pass-through guarantees (no production change;
   test files exempt from the size check).
5. Commit with a `test:`-scoped message. No compatibility/data migration steps apply.

## 6. Acceptance mapping (testable)

| Spec requirement | Acceptance check | Test function / assertion |
|---|---|---|
| REQ-1 | A.1 | `TestParseIDFromParam` — 3 rows: valid (`got == idgen.Parse(raw)`), missing (`*AppError`, `request.invalid_id`, msg ⊇ `missing id param`), invalid (`request.invalid_id`, msg ⊇ `invalid id param`); `stderrors.As` type pin |
| REQ-2 | A.2 | `TestWriteError_nilError` — 500, `internal.unexpected`, `internal server error` |
| REQ-3 | A.3 | `TestWriteError_doesNotLeakCause` — status/code/message from `appErr`; cause bytes absent |
| REQ-4 | A.4 | `TestWriteError_echoesRequestID` — `error.request_id == "req-test-1234"` |
| REQ-5 | A.5 | `TestPageFromQuery_categoryMapping` — `p.Category == domain.FileCategory("docs")` |
| REQ-6 | A.6 | `TestPageFromQuery_unnormalizedLimitPassthrough` — `p.Limit == -5` and `== 999`, unnormalized |
| REQ-7 | A.7 | `TestOK_returnsData` → body `{"data":{"key":"value"}}`; `TestCreated_returns201` → body `{"data":"created"}` (status 200/201 kept) |

Gate checklist: `make test && make lint && make lint-strict && make check-file-sizes && make build`.

## 7. Out of scope (explicit)

- No production-code changes (`httpx.go`, `errors.go`, handlers, middleware).
- No change to the file-domain `category` filter contract (separate direction).
- No `NoContent` behavior change (existing 204 test stands).
- No request_id-absent / `omitempty` case.
- No integration-tagged tests, no network, no Docker, no new dependencies.
