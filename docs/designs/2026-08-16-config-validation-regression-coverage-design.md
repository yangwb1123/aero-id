# Design — Complete Regression Coverage for the Untested Startup-Validation Paths in `internal/config`

Status: **Proposed** (implementation-ready)
Date: 2026-08-16
Module: `internal/config` (test-only change set)
Spec: Requirements spec `docs/auto/runs/complete-regression-coverage-for-the-untested-st-8981f78c/artifacts/requirements-10762e10/requirements.md`
Baseline verified at: HEAD `7d83253` — every evidence claim re-verified against the live tree on 2026-08-16 (symbols, line ranges, error strings, coverage figures, and parser behaviors all reproduce; one FR-5 cell is **wrong** and corrected in §1 delta 1).

---

## 1. Verification verdict (untrusted evidence → tree)

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | `validate.go` function inventory and line ranges (`validateWebhook` 178-187, `validateEventBus` 287-298, `validateReplication` 300-320, `validateEventSourceKeys` 322-332, `validateTransport` 361-375, `validateSourceEndpoint` 377-391, `validateSourceURL` 393-405, `validSourceLimits` 407-410, `validateSourcePaths` 412-422, `isLocalEndpoint` 424-427; file = 427 lines) | **Confirmed** | `internal/config/validate.go` — every symbol at exactly the cited line; `wc -l` = 427. `validate()` wiring (`validateSourcesAndRegions` → `validateBindingRegions`, `validateRuntimeLimits` slice, final `validateTransport`) at 10-21 / 131-145. |
| E2 | Coverage figures (0.0% ×4, 22.2%, 18.2%, 28.6%, 33.3% ×2, 50.0%, 55.6%, module total 72.0%) | **Confirmed** | `go test ./internal/config/ -coverprofile` + `go tool cover -func` reproduced **exactly** (see §10.1). |
| E3 | All error strings in the FR tables | **Confirmed** | Every expected substring in FR-1..FR-5 matches `validate.go` verbatim (list at §10.2). |
| E4 | Parity-pin pattern `auditTokenURLCases()` + `TestAuditTokenURLGateParityWithConnector` at `commercial_test.go:242-346` | **Confirmed** | Helper at 242-280 (incl. `"trimmed https"` cell → the audit connector trims and accepts); parity test at 296-346. Matrix-style helpers are the established house pattern. |
| E5 | `TestAuditGovernanceKnobGateMatrix` direct-call style at `commercial_test.go:120-185` | **Confirmed** (minor line drift) | Function actually starts at line 134; 120-133 is the preceding helper/comment. Behavior identical; no impact on this direction. |
| E6 | Shipped-file positive leg: empty endpoints, transport bounds 10s / 4MiB, webhook `url: ""`, `event_bus.nats.enabled: false`, `replication.enabled: false`, `source_keys: {}` | **Confirmed** | `configs/config.yaml` lines 8-31 (sources), 52-72 (server), 142-153 (outbox/webhook + event_ingest), 155-172 (event_bus/replication). |
| E7 | Baseline gates green; `TestLoadShippedConfigFileAndEnvGate` at `config_test.go:502-557` | **Confirmed** | `make lint`, `make lint-strict`, `make check-file-sizes`, `make build` all PASS on the current tree; test at 502-557 (comment at 502, func at 511). |
| E8 | No test-name collisions; package sequential (no `t.Parallel`) | **Confirmed** | `grep -rn` of all five planned test names across `internal/config/` → none; `config_test.go:510` and `concurrency_test.go:16` document the no-`t.Parallel` convention. |
| E9 | Pinned Go 1.26 `url.Parse` behaviors (scheme lowercased; host **not** lowercased; bare `?` → `ForceQuery`, `RawQuery==""`; whitespace-padded → parse **error**; `https://`/`http://` → empty hostname; `not-a-url` → parse error) | **Confirmed** | Direct `go run` reproduction of every FR-1 matrix URL on the current toolchain (see §10.3). |
| E10 | `regionConfigured("")` returns true → blank binding region valid | **Confirmed** | `validate.go:123-129` — trims, empty → `true`. |
| E11 | Lint-strict limits funlen 50 / cyclop 10 | **Confirmed** | `.golangci.yml` — `funlen.lines: 50`, `cyclop.max-complexity: 10`, `tests: true`. |
| E12 | `check-file-sizes` exempts test files | **Confirmed** | `Makefile:64-80` — find excludes `*_test.go` (`! -name '*_test.go'`). |

**Evidence deltas found (all content-affecting corrections adopted into this design):**

1. **FR-5 "whitespace region" cell is wrong — the evidence table, if implemented as written, produces a failing test.** The evidence asserts `SourceRegion: "  local  "` → error `must be listed in account.regions` with rationale *"no trim → not in set"*. But `regionConfigured` (`validate.go:123-129`) **does** trim: `"  local  "` → `"local"` → member of `{"local"}` → `validateBindingRegions` returns **nil**. Verified by direct execution of the exact function copies (`/tmp/regioncheck.go`): `err=""`. **Correction adopted in §5 FR-5:** the whitespace-region cell is `wantErr: ""` (**valid**), and its comment pins the trim behavior at `validate.go:124` — a future regression that removes the trim flips it red. This is strictly a better pin (it tests a branch the evidence's wrong cell would never exercise).
2. **FR-1 "whitespace padded" cell rationale is imprecise.** `url.Parse("  https://sso.example/token  ")` errors with `first path segment in URL cannot contain colon`, so the rejection lands in the first guard (`err != nil`, `validate.go:395`), not in a distinct "no trim" comparison branch. The *outcome* (rejected) and the divergence pin vs. the audit connector (which trims → accepts, per `commercial_test.go:273` "trimmed https" cell) are exactly as the evidence states. Cell comment should cite `validate.go:395` (parse-error branch), not "no trim in validateSourceURL".
3. **§3's "funlen-50 / cyclop-10 without suppressions" constraint is softer than claimed.** `.golangci.yml` `exclude-rules` exempts `.*_test\.go$` from funlen/cyclop/lll. The shared-helper pattern remains the right call (house style, readability, per-matrix clarity), but lint-strict cannot fail on a long test function. The binding constraints are instead: no production change, no `//nolint` suppressions, and the Makefile gates.

No other evidence claim required correction. All error substrings, cell expectations (20 FR-1 rows, 12 FR-2 rows, 12 FR-3 rows, 31 FR-4 rows, 5 FR-5 rows), coverage thresholds, and the §6 done-gate command are adopted as specified.

---

## 2. Problem statement

The `Load`-time validation gates that protect production are the least-tested code in `internal/config`:

- **SSRF surface**: `validateSourceURL` (0.0%), `isLocalEndpoint` (0.0%) — the https-outside-localhost rule and the URL-shape guards (userinfo / raw query / fragment) are completely uncovered.
- **mTLS + DoS surface**: `validateSourceEndpoint` (22.2%), `validSourceLimits` (0.0%), `validateSourcePaths` (0.0%) — cert/key pairing XOR, limit negativity, absolute-path/control-character rejection are unpinned.
- **Transport bounds**: `validateTransport` (55.6%) — the `keepalive_min_ping_interval >= 1s`, `read_header_timeout >= 1s`, `max_recv/send_msg_size >= 1KB` floors are only incidentally covered.
- **Enablement contracts**: `validateEventBus` (28.6%), `validateReplication` (18.2%), `validateWebhook` (33.3%), `validateEventSourceKeys` (33.3%) — required-when-enabled, negative-rejection, transport-switch, and ingest-key gates are mostly uncovered.
- **Region family**: `validateBindingRegions` (50.0%).

The shipped `configs/config.yaml` exercises only the pass-through branches, so a regression that (a) relaxes https-outside-localhost, (b) breaks the mTLS XOR check, (c) weakens the transport floors, or (d) silently accepts a webhook/replication/ingest-key misconfiguration would be **invisible to the current done-gate**. The audit/commercial families are 80–100% covered and carry a cross-layer parity pin; the startup validators are the anomaly.

This direction closes the gap with a **test-only** change set: five table-driven tests + five shared matrix helpers in a new `internal/config/validate_test.go`, asserting **current** guard behavior only. No production code, no new validators, no behavior tightening.

---

## 3. Design overview

- **Single new file**: `internal/config/validate_test.go`, package `config`, unit tests, no `t.Parallel` (package is sequential), stdlib + package-internal symbols only (no cross-package import, no connector parity pin — avoids the import cycle the audit parity test needed a separate package seam for).
- **Direct-validator entry points** (mirroring `TestAuditGovernanceKnobGateMatrix`'s direct-call style), so each matrix exercises exactly one validator and cannot be short-circuited by an earlier failure in `c.validate()`:
  - `validateSourceURL(name, raw)`
  - `validateSourceEndpoint(name, SourceEndpointConfig)` (natural wrapper: URL → limits → mTLS → paths in one call)
  - `(&Config{Server: ...}).validateTransport()`
  - `validateEventBus(EventBusConfig)`, `validateReplication(ReplicationConfig)`, `validateWebhook(WebhookConfig)`, `validateEventSourceKeys(map[string]string)`
  - `validateBindingRegions(map[string]EventSourceBindingConfig, regions)`
- **One shared matrix helper per FR** returning `[]struct{...}` (`sourceURLCases`, `sourceEndpointCases`, `transportCases`, plus the four FR-4 sub-helpers and `bindingRegionCases`), in the `auditTokenURLCases()` idiom.
- **Two assertion styles**, matching the evidence: `wantValid bool` matrices assert `(err == nil) != wantValid`; `wantErr string` matrices assert `err != nil && strings.Contains(err.Error(), wantErr)` (substring, not exact equality — stable against prefix-only wrapping).
- **Regression canaries** are explicit cells, documented in the matrix comments (see §4).

---

## 4. API changes

### 4.1 Production API — none

`validate.go` (427 lines), `config.go`, `commercial.go`, `configs/config.yaml`, `Makefile`, CI: **untouched**. `git diff --stat` on the landed change must touch only `internal/config/validate_test.go`. There are no new exported symbols, no new error messages, no tightening: the tests pin the guards **as they exist today**.

### 4.2 Test surface — `internal/config/validate_test.go`

Five test functions; each ≤ ~30 lines thanks to the shared helpers:

| Test | Validator under test | Cells | Canary |
|---|---|---|---|
| `TestValidateSourceURLMatrix` | `validateSourceURL` | 20 | http non-loopback ×2 (https-required rule, `validate.go:401-403`); userinfo/raw-query/fragment (SSRF shape guards, `validate.go:395`) |
| `TestValidateSourceMTLSAndPaths` | `validateSourceEndpoint` | 12 | cert-only / key-only (mTLS XOR, `validate.go:389-391`); relative-path / control-character rows (`validate.go:412-422`) |
| `TestValidateTransportLimits` | `validateTransport` (method) | 12 | 500ms keepalive / 512-byte msg sizes (`value > 0 && value < floor` guards, `validate.go:362-373`) |
| `TestValidateEventBusReplicationWebhookEnablementContracts` | `validateEventBus`, `validateReplication`, `validateWebhook`, `validateEventSourceKeys` | 31 across four sub-helpers | disabled+garbage cells (enablement short-circuits); missing-field cells (required-when-enabled); negative cells; `tcp`/empty-transport switch cells |
| `TestValidateBindingRegions` | `validateBindingRegions` | 5 | unmapped-region cell; whitespace-region cell now pins the **trim** at `validate.go:124` (evidence-corrected) |

### 4.3 Exact expected substrings (copied verbatim from `validate.go` — the tests use these as `wantErr`)

```
sources.snaplink.endpoint must be a valid URL without userinfo      (validate.go:395)
sources.snaplink.endpoint must use http or https                    (validate.go:399-400)
sources.snaplink.endpoint must use https outside localhost          (validate.go:401-403)
sources.snaplink timeout, limits, and retry values must not be negative   (validate.go:382-383)
sources.snaplink mTLS requires both tls_cert_file and tls_key_file  (validate.go:389-391)
sources.snaplink.<field> must be an absolute path without control characters  (validate.go:412-422)
server.grpc.keepalive_min_ping_interval must be >= 1s               (validate.go:363-364)
server.http.read_header_timeout must be >= 1s when set              (validate.go:366-367)
server.grpc.max_recv_msg_size must be >= 1KB when set               (validate.go:369-370)
server.grpc.max_send_msg_size must be >= 1KB when set               (validate.go:372-373)
event_bus.nats url, stream, subject and durable are required when enabled   (validate.go:292-293)
event_bus.nats max_redeliveries and ack_wait must not be negative   (validate.go:295-296)
replication.nats_subject is required when transport=nats            (validate.go:304-305)
replication.http_endpoint, http_key and http_bearer_token are required when transport=http    (validate.go:307-308)
replication.transport must be nats or http                          (validate.go:311-312)
replication interval must be positive and batch_size must not be negative  (validate.go:315-316)
outbox.webhook.timeout must be a positive duration                  (validate.go:185-186)
event_ingest.source_keys contains unsupported source "slack"        (validate.go:326-327)
event_ingest.source_keys.snaplink must be at least 32 bytes         (validate.go:329-330)
event_ingest.bindings.client-a.source_region must be listed in account.regions  (validate.go:116-117)
```

### 4.4 Base fixtures (mirror the shipped `configs/config.yaml` so the positive legs prove the shipped file's shape)

- **FR-2 source**: `SourceEndpointConfig{Endpoint: "https://sso.example/", Timeout: 2s, MaxBodyBytes: 1048576, MaxConcurrency: 32, MaxAttempts: 3, RetryBackoff: 50ms, SummaryPath: "/internal/account-summary"}` — matches the shipped sources block.
- **FR-3 transport**: `Config{Server: ServerConfig{HTTP: HTTPConfig{ReadHeaderTimeout: ...}, GRPC: GRPCConfig{KeepaliveMinPingInterval, MaxRecvMsgSize, MaxSendMsgSize: ...}}}` — only the four guard-relevant fields set.
- **FR-4.1 bus**: NATS `{Enabled: true, URL: "nats://nats:4222", Stream: "aero_events", Subject: "aero.events.canonical", Durable: "aero-id-ingest", MaxRedeliveries: 5, AckWait: 30s}`.
- **FR-4.2 replication**: enabled `{Interval: 5s, BatchSize: 500, Transport: "nats", NATSSubject: "aero.events.canonical"}`; http cells add an HTTPS `HTTPEndpoint`, `HTTPKey` ≥ 32 chars, and a service `HTTPBearerToken`.
- **FR-4.4 keys**: `{"snaplink", "aero-im", "aero-vault"}` keys ≥ 32 bytes; boundary cell exactly 32 bytes.

---

## 5. Compatibility constraints

1. **Zero value = default, never asserted as error.** The FR-3 matrix covers zero, below-floor, at-floor, and shipped-value cells only. **Negative transport values (`KeepaliveMinPingInterval: -1`, etc.) are deliberately not asserted**: current code treats `<= 0` as unset (`value > 0 && value < floor`), and pinning "negative accepted" would hard-code behavior this direction does not bless; a future code-change direction may tighten it separately. Same for FR-4 zero cells (`MaxRedeliveries: 0`, `AckWait: 0`, `BatchSize: 0`, `Timeout: 0` in the source fixture) — all load valid, per the AGENTS.md `0 = code default` convention.
2. **No behavior change, period.** Every currently-loading configuration still loads; the shipped `configs/config.yaml` continues to pass `TestLoadShippedConfigFileAndEnvGate` unmodified. Tests assert current guard behavior only.
3. **Divergence from the audit connector is pinned, not equalized.** `validateSourceURL` does **not** trim (whitespace-padded URLs rejected via parse error at `validate.go:395`); the audit connector trims and accepts (`commercial_test.go:273`). The FR-1 "whitespace padded" cell pins this divergence deliberately — a future cleanup that adds trimming to `validateSourceURL` will flip this cell red and force an explicit decision.
4. **ForceQuery parity is pinned.** The FR-1 "https bare query" row (`https://sso.example/token?` → valid, because `RawQuery==""` when only `ForceQuery` is set) mirrors the audit matrix's bare-`?` row, so a future cleanup cannot break the two asymmetrically.
5. **Package is sequential** — no `t.Parallel` in the new file, matching `config_test.go:510` / `concurrency_test.go:16`.
6. **No new imports.** stdlib (`net/url` not even needed in tests — strings/time/fmt only) + package `config`. No connector import, so no import cycle and no integration build tag.
7. **Test-file gates don't apply** (funlen/cyclop/lll exempt `.*_test\.go$`; `check-file-sizes` excludes tests) — but the shared-helper pattern keeps each test readable and consistent with house style regardless.
8. **Go 1.26 parser behavior is the pin target.** Expected FR-1 values are observed behavior of the current toolchain (verified by direct execution, §10.3). A future toolchain that changes `url.Parse` whitespace/ForceQuery behavior will flip the two pinned cells and surface the change for an explicit decision — that is the intent.

---

## 6. Failure modes and mitigations

| Failure mode | Effect | Mitigation |
|---|---|---|
| Typo'd expected substring in a `wantErr` cell | Test fails with a clear cell name + got/want | Substrings are copied verbatim from `validate.go` in this design (§4.3); a CI failure prints the cell name and actual error for a one-line fix |
| Wrong entry point (e.g., calling `c.validate()` instead of the direct validator) | Earlier validators short-circuit; later targets never execute; coverage thresholds miss and the failure is silent | Each test calls exactly one validator with a fully-valid base fixture; the coverage gate (§10.1) mechanically proves every target function executed ≥80-90% |
| Regression that relaxes https-outside-localhost, mTLS XOR, transport floors, or enablement contracts | Guard cells flip green | Designated canary cells documented in matrix comments (§4.2); FR-1.1 comment text is mandatory per the evidence |
| FR-5 cell written per the *uncorrected* evidence table (`"  local  "` → error) | Test fails against current code — the tree trims | Design doc corrects the cell to `wantErr: ""` and repurposes it as a trim pin (§1 delta 1, §5 FR-5) |
| Go toolchain upgrade changes `url.Parse` (whitespace-padded, ForceQuery) | Two pinned cells flip | Intentional contract pin (§5.8); surfaces for an explicit decision, not silent |
| Accidental production edit (`validate.go`, `config.go`, `commercial.go`) | Scope creep; violates "test-only" | `git diff --stat` gate in the done-gate (§10.4) must show only `internal/config/validate_test.go`; `check-file-sizes` still green |
| New validator or tightened behavior added while writing tests | Tests document the new behavior instead of current behavior | Explicit non-goal; review gate: error substrings must match the §4.3 list exactly, nothing else |
| Test-name collision with future work | Duplicate symbol → compile error | Names `TestValidate*` are grep-verified collision-free today (§1 E8); a future direction adding a same-name test gets an immediate compile failure, not a silent merge |

---

## 7. Migration steps (landing plan)

1. **Write `internal/config/validate_test.go`** (single commit, test-only):
   - Five shared matrix helpers + five test functions per §4.2, expected values per the FR tables in the evidence as corrected by §1 delta 1 (the FR-5 whitespace cell → `wantErr: ""`).
   - Mandatory canary comments: FR-1 matrix header must state *"the http non-loopback rows are the regression canary for the https-outside-localhost rule at validate.go:401-403; a typo'd relaxation of this rule fails this test"*; FR-2 and FR-3 headers state their canaries analogously (§4.2).
2. **Prove the canary is live (development-only, then revert):** temporarily delete `parsed.Scheme != "https" &&` from `validate.go:401`; run `go test ./internal/config/ -run TestValidateSourceURLMatrix` → must go red on the `http non-loopback` rows; revert the mutation; re-run → green. (Same one-line-mutation spot-check for the mTLS XOR and transport floors if desired.)
3. **Run the module gates:** `make test`, `make lint`, `make lint-strict`, `make check-file-sizes`, `make build`.
4. **Run the coverage gate** (§10.1) and confirm every named function ≥ threshold.
5. **Confirm the shipped file still loads:** `go test ./internal/config/ -run TestLoadShippedConfigFileAndEnvGate`.
6. **Confirm scope:** `git diff --stat` shows only `internal/config/validate_test.go`; `wc -l internal/config/validate.go` still 427.
7. Optional follow-up (separate direction, not here): tighten negative transport values, add a cross-layer parity pin for `validateSourceURL` vs. the audit connector, or extend coverage to `validateConfiguredSources` / `validatedRegions`.

---

## 8. Out of scope (explicit non-goals)

- **No production-code change** — `validate.go`, `config.go`, `commercial.go`, `configs/config.yaml` untouched; `validate.go` stays 427 lines.
- No change to `Config.Load`, viper decoding, or defaults.
- No new validators, no new error messages, **no tightening of current behavior** (negative transport values explicitly not asserted — §5.1).
- No integration-tagged tests, no Docker, no migrations, no proto, no Makefile/CI edits.
- The billing `TokenURL` Load-gate, strict unknown-key rejection, and `CallbackConfig` validation directions from the same analysis file are separate directions and are not part of this one.
- No cross-layer parity pin with the audit connector (unlike `TestAuditTokenURLGateParityWithConnector`) — all entries are pure `internal/config` validators; a parity pin would need a new package seam and is out of scope.

---

## 9. Testable acceptance mapping

| Acceptance (evidence §5) | Testable assertion | Gate |
|---|---|---|
| AC-1: five table-driven tests in `internal/config/validate_test.go` with the specified matrices | `TestValidateSourceURLMatrix`, `TestValidateSourceMTLSAndPaths`, `TestValidateTransportLimits`, `TestValidateEventBusReplicationWebhookEnablementContracts`, `TestValidateBindingRegions` exist with the FR-1..FR-5 cell sets and exact substrings (§4.3) | `go test ./internal/config/ -count=1` |
| AC-2: regression canary live | The `http non-loopback` rows reject today; deleting `parsed.Scheme != "https" &&` from `validate.go:401` makes them accepted → test red (proven during development per §7 step 2) | canary spot-check + matrix red-on-mutation |
| AC-3: coverage thresholds | `validateSourceURL`, `validateSourcePaths`, `isLocalEndpoint`, `validSourceLimits` ≥ 90%; `validateTransport`, `validateReplication`, `validateEventBus`, `validateWebhook`, `validateEventSourceKeys` ≥ 80%; incidental lift on `validateSourceEndpoint` (22.2% → ~100%) and `validateBindingRegions` (50% → 100%) | coverage command §10.1 |
| AC-3: gates | `make test`, `make lint`, `make lint-strict`, `make check-file-sizes`, `make build` all green; `TestLoadShippedConfigFileAndEnvGate` green | §10.4 done-gate |
| AC-4: no behavior change | `git diff --stat` touches only `internal/config/validate_test.go`; `configs/config.yaml` unmodified and still loads | §10.4 step 2 |

### 10.1 Coverage gate

```bash
go test ./internal/config/ -coverprofile=/tmp/config.cover \
  && go tool cover -func=/tmp/config.cover | grep -E 'validateSourceURL|validateSourcePaths|isLocalEndpoint|validSourceLimits|validateTransport|validateReplication|validateEventBus|validateWebhook|validateEventSourceKeys'
```

Baseline reproduced on the current tree (2026-08-16, HEAD `7d83253`): 0.0% / 0.0% / 0.0% / 0.0% / 55.6% / 18.2% / 28.6% / 33.3% / 33.3% / total 72.0% — matching the evidence's table exactly. With the full matrices every named function is exercised on all branches (rejection + pass-through + short-circuit), so the thresholds are reached without instrumentation or suppressions.

### 10.2 Error-substring contract

Every `wantErr` value is a substring of the exact `validate.go` format strings listed in §4.3; matches are `strings.Contains` (stable under prefix wrapping). No cell asserts full-text equality.

### 10.3 Pinned parser behaviors (verified by direct execution on the current toolchain)

| Input | Observed | Design consequence |
|---|---|---|
| `HTTPS://sso.example/token` | scheme `https` (lowercased) | accepted |
| `http://LOCALHOST/token` | hostname `LOCALHOST` (not lowercased) | accepted only because `isLocalEndpoint` lowercases — pin |
| `https://sso.example/token?` | `ForceQuery=true`, `RawQuery==""` | accepted — ForceQuery parity pin |
| `  https://sso.example/token  ` | parse error `first path segment in URL cannot contain colon` | rejected at `validate.go:395` — divergence pin vs. audit connector (which trims) |
| `https://`, `http://` | empty hostname | rejected by `Hostname()==""` guard |
| `not-a-url` | parse error | rejected |

### 10.4 Done-gate command

```bash
make test && make lint && make lint-strict && make check-file-sizes && make build \
  && go test ./internal/config/ -coverprofile=/tmp/config.cover \
  && go tool cover -func=/tmp/config.cover | grep -E 'validateSourceURL|validateSourcePaths|isLocalEndpoint|validSourceLimits|validateTransport|validateReplication|validateEventBus|validateWebhook|validateEventSourceKeys'
# scope check:
git diff --stat            # must show only internal/config/validate_test.go
wc -l internal/config/validate.go   # must still be 427
```

The `check-audit-swallow`, `check-anchored-event-ids`, `check-audit-manifest`, `check-load-scenarios`, `check-migration-set`, `check-composition-root`, `check-proto-sync`, `check-integration-compile` gates are untouched by a test-only `internal/config` change; `make test-integration` is not required (no repository SQL, storage, connector, event-ingestion, callback, or multi-service transaction code changes).

---

## 10. Risks

- **Over-constraining cells** (fragile tests): mitigated — expected values are observed behavior of the pinned Go 1.26 toolchain (verified by direct execution, §10.3) or verbatim `validate.go` strings (§4.3).
- **Under-constraining cells** (test passes vacuously): mitigated — every target function has rejection + pass-through + short-circuit cells; the coverage gate (§10.1) mechanically proves branch execution, and the canary spot-check (§7 step 2) proves the guard is live.
- **Scope creep** toward a production tightening: the negative-transport non-assertion (§5.1) is the deliberate boundary; any tightening belongs to a separate code-change direction.
- **Evidence-embedded defect**: the FR-5 whitespace cell in the source spec would have shipped a failing test; this design corrects it (delta 1) and pins the trim behavior instead — strictly stronger coverage with no behavior change.
