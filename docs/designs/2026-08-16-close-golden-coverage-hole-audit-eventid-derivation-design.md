# Design — Close the golden-coverage hole in the audit EventID derivation contract (region.promote + preference.set have no byte-level pin)

**Date:** 2026-08-16
**Source analysis:** `docs/auto/analyses/internal-pkg-idgen-ef49e180.json` (untrusted requirements evidence), direction #1 of 3
**Module scope boundary:** `internal/pkg/idgen` golden test set + `scripts/check-anchored-event-ids.sh` (+ its `scripts/testdata/anchored-event-ids/` fixtures). **Zero runtime code, zero service, zero repository, zero migration, zero config changes.** The gate and the golden tests are the entire change surface.

---

## 1. Verification verdict (untrusted evidence → tree)

Every substantive claim in the requirements evidence was re-checked against the live tree at HEAD. **No corrections required — the evidence is accurate on all verified points, and all proposed fixture values reproduce exactly from an independent RFC 4122 v5 implementation.**

| # | Claim | Verdict | Live-tree location |
|---|---|---|---|
| V1 | `Anchor` primitive: NUL-join, zero-part panic, NUL-in-part panic, fixed arity ≥ 4 doc | **VERIFIED** | `internal/pkg/idgen/idgen.go:31-45` — `strings.Join(parts, "\x00")` at `:44`; both panics at `:34-41`; arity contract in the doc comment `:25-27` |
| V2 | Namespace constant `38248e3a-32f0-4929-9b56-b2dbd75aa849` | **VERIFIED** | `idgen.go:18` — `var AnchorNamespace = uuid.MustParse("38248e3a-32f0-4929-9b56-b2dbd75aa849")`; doc comment states it is constant forever (`:15-17`) |
| V3 | Golden set G1–G13 + S1/S2; **no `region.promote`, no `preference.set`** | **VERIFIED** | `internal/pkg/idgen/idgen_test.go:69-128` — case table ends at S2 (`:122-123`); `goldenTenant`/`goldenOP`/`goldenST`/`goldenJB` constants at `:52-56` |
| V4 | `region.promote` shape = 6 parts incl. CAS epoch (decimal string) | **VERIFIED** | `internal/service/region_fencing.go:110-111` — `idgen.Anchor("audit", "region.promote", tenantID, accountID.String(), targetRegion, strconv.FormatInt(expectedEpoch, 10))` |
| V5 | `preference.set` shape = 5 parts incl. sha256 payload digest | **VERIFIED** | `internal/service/preference.go:131` — `idgen.Anchor("audit", "preference.set", tenantID, uid, preferenceSetDigest(prefs))`; `preferenceSetDigest` at `:134-152` excludes `UID`/`CreatedAt`/`UpdatedAt` (retry byte-identity) |
| V6 | Service tests are self-referential (same function under test on both sides) | **VERIFIED** | `internal/service/region_fencing_test.go:217` — `wantID := idgen.Anchor("audit", "region.promote", account.TenantID, account.ID.String(), "us-east", "0")`; `preference_test.go:68+` zero-check only, `:120+` retry-equality/distinctness derived at test time |
| V7 | Gate has R-1/R-2 only; **no shape-coverage check; zero occurrences of the namespace byte literal** | **VERIFIED** | `scripts/check-anchored-event-ids.sh` (150 lines) — R-1 `EventID: idgen.NewID()` ban (`:62-72`), R-2 block (`:74-101`); `grep -c "38248e3a-..."` = 0; `var AnchorNamespace` count check at `:94-99` (anchored path `$SELF_DIR/../internal/pkg/idgen/idgen.go`) |
| V8 | Makefile invocation `internal cmd` | **VERIFIED** | `Makefile:106-108` — `check-anchored-event-ids: @bash scripts/check-anchored-event-ids.sh internal cmd` |
| V9 | Ledger dedup = DB uniqueness on `(tenant_id, source, event_id)` | **VERIFIED** | `migrations/000050_scope_events_and_audit_by_tenant.up.sql:8,16` — `UNIQUE (tenant_id, source, event_id)` on `audit_ledger` and `event_store`; pre-tenant precedent `000034_create_audit_ledger.up.sql:27` — `UNIQUE (source, event_id)` |
| V10 | Live anchor-site inventory (6 literal + 1 dynamic) | **VERIFIED** | `legacy_cutover.go:144` (`legacy.cutover.set`), `region_fencing.go:110` (`audit`+`region.promote`), `preference.go:131` (`audit`+`preference.set`), `audit_hold.go:165` (`audit_hold.set`), `audit_hold.go:185` (`audit_hold.removed`), `sync_audit.go:44` (`sync.job.finished`), `operation_audit.go:68` (dynamic `action`); zero `cmd/` sites |
| V11 | Negative fixture contains the two literal anchors; positive fixture reddens via R-1/R-2 only | **VERIFIED** | `scripts/testdata/anchored-event-ids/negative/negative.go:19,23` — `idgen.Anchor("audit_hold.set", …)` / `idgen.Anchor("legacy.cutover.set", …)` (plus a dynamic-arg `goodOperation` at `:26-28` that extraction must skip); `positive/positive.go` has no anchor literal (R-1/R-2 shapes only) |
| V12 | idgen_test.go is 290 lines (limit 500) | **VERIFIED** | `wc -l` = 290 |
| V13 | Proposed fixture values G14/G14b/G15 (and optional G14c) reproduce from an independent implementation | **VERIFIED EMPIRICALLY** | Python `uuid5` reproduction in §8 — G1 calibration `d0551f9f-d7e9-5773-aa06-fe022460ba93` matches the committed golden; G14 `0c9b9631-6da3-5c20-813d-4ec76e5dc961`, G14b `efd6375b-4c86-57b1-ac7b-0e821537ce47`, G15 `52d8beb8-b5fb-55ba-ad4b-e3ff011c37e5`, G14c(epoch 2) `6b15c036-15c8-581b-b9c4-03c6b0f2d01b` all reproduce |
| V14 | Baseline gate green | **VERIFIED EMPIRICALLY** | `bash scripts/check-anchored-event-ids.sh internal cmd` → `✓` exit 0; `go test ./internal/pkg/idgen/ -count=1` → `ok` |
| V15 | `u-1` / `us-east` / epoch `0` are the established fixture identities | **VERIFIED** | `region_fencing_test.go:67,190,217` — `CanonicalUID: "u-1"`, actor `"u-1"`, `"us-east"`, epoch `0`; consistent with the G14 fixture choice |

**Secondary precision notes (no substance impact):**

- **P-1 (REQ-4 extraction mechanics):** `region_fencing.go:110-111` is a **two-line call**; the leading-literal run `"audit", "region.promote"` terminates on line 110 before the newline, so a line-oriented `grep -oE` extraction still yields `audit.region.promote`. Verified against the actual byte layout.
- **P-2 (REQ-6 negative-fixture safety):** `negative.go:26-28` (`goodOperation`) has a **dynamic first argument** — the leading-literal run is empty, so R-3 extraction skips it entirely. The negative fixture stays green with only its two literal anchors in the manifest.
- **P-3 (REQ-5 scope):** the namespace byte-literal count is anchored to the real file `internal/pkg/idgen/idgen.go` (precisely the precedent of the existing `var AnchorNamespace` count check at `check-anchored-event-ids.sh:94-99`). The literal also appears in `positive.go` and in `idgen_test.go` comments, but neither is in scope for the count.
- **P-4 (evidence §3.4 claim):** the byte literal `38248e3a-32f0-4929-9b56-b2dbd75aa849` appears **zero** times in the current script — a hex-literal reassignment of `AnchorNamespace` would evade both the `MustParse` grep and the count check today. REQ-5 closes exactly this path.

---

## 2. Problem statement (confirmed)

The ledger-dedup contract — retries of the same logical audit fact must re-derive the **same** v5 EventID so the `UNIQUE (tenant_id, source, event_id)` constraint swallows the duplicate; distinct intents must derive **distinct** IDs — is pinned byte-for-byte only by the G1–G13/S1–S2 golden fixtures in `internal/pkg/idgen/idgen_test.go`. Two live, load-bearing anchor shapes are **not** in that set:

1. **`region.promote`** (`region_fencing.go:110-111`, 6 parts incl. the CAS fencing epoch). The durable `(tenant, source, event_id)` dedup is what collapses a retried promotion attempt instead of double-recording it; the epoch part is what keeps two *distinct* attempts (epoch 0 → 1 → …) from ever colliding. This is the security-relevant fencing fact.
2. **`preference.set`** (`preference.go:131`, 5 parts incl. the sha256 payload digest). The digest part is what keeps a later legitimate Set by the same user distinct from an earlier one (the multi-occurrence no-silent-drop invariant) while a retry of the same payload re-derives the same ID.

Their service tests compare the derived EventID against a **fresh `idgen.Anchor(...)` computed at test time** (`region_fencing_test.go:217`; `preference_test.go` zero-check + retry tests). Both sides of those comparisons invoke the same function under test, so **any** drift — part order, arity, separator, canonicalization, discriminator spelling — passes the tests while silently breaking historical ledger dedup across a rollout (duplicate facts on retry, or collisions between distinct intents). The gate `scripts/check-anchored-event-ids.sh` bans random minting and derivation-source misuse (R-1/R-2) but has **no shape-coverage check** and **no byte-level pin of the namespace literal**, so it cannot notice a changed shape or a new unpinned shape.

---

## 3. Design overview

Six coordinated changes (R-1…R-6), each independently testable. **No runtime API, service, repository, migration, or config change anywhere.**

- **R-1 — Golden pin for `region.promote` (G14).** Extend `TestAnchor_GoldenConstants` in `internal/pkg/idgen/idgen_test.go` with the case `G14 region.promote` using the existing `goldenTenant`/`goldenOP` constants:

  ```go
  {"G14 region.promote", []string{"audit", "region.promote", goldenTenant, goldenOP, "us-east", "0"},
      "0c9b9631-6da3-5c20-813d-4ec76e5dc961"},
  ```

  Locks: the two-part discriminator `"audit"` + `"region.promote"`, part order, arity 6, the epoch as a decimal string, the account ID as `accountID.String()` form.

- **R-2 — Golden pin for `preference.set` (G15).** Add:

  ```go
  {"G15 preference.set", []string{"audit", "preference.set", goldenTenant, "u-1", strings.Repeat("a", 64)},
      "52d8beb8-b5fb-55ba-ad4b-e3ff011c37e5"},
  ```

  (or the literal 64-`a` string inline). Locks: the two-part discriminator, part order, arity 5, the uid slot, and the digest slot — the multi-occurrence no-silent-drop invariant.

- **R-3 — Wrong-epoch differentiation case (G14b, plus optional G14c).** Add:

  ```go
  {"G14b region.promote wrong epoch", []string{"audit", "region.promote", goldenTenant, goldenOP, "us-east", "1"},
      "efd6375b-4c86-57b1-ac7b-0e821537ce47"},
  ```

  plus an explicit `G14b != G14` assertion (belt-and-braces alongside the existing seen-map, which already proves cross-collision-freedom over the whole table). Optional strengthening: `G14c` with epoch `"2"` → `6b15c036-15c8-581b-b9c4-03c6b0f2d01b`, and a pairwise-distinctness assertion across the epoch set. This pins the CAS-per-attempt dedup semantics: different epochs never collide; same epoch retries re-derive the same ID.

- **R-4 — R-3 shape-coverage check in `scripts/check-anchored-event-ids.sh`.** Add a third rule class. For every scanned root, in non-test, non-`*.pb.go`, non-`idgen.go` Go files, extract the **leading run of consecutive string-literal arguments** of every `idgen.Anchor("…", …)` call; join the literals with `.` to form the discriminator; dedupe; assert each discriminator is in the golden manifest. Violations fail with an `R-3 no golden pin for shape <d>` message.

  **Extraction semantics (spec decision, from the evidence):** the leading-*run* interpretation, not first-arg-only — both new shapes use the two-part prefix `"audit"` + `"<shape>"`, and first-argument-only extraction would reduce both to `audit`, making the gate vacuously green for exactly the shapes it exists to cover.

  Concrete extraction (implementation detail; the fixtures are the oracle):
  - `grep -oE 'idgen\.Anchor\("[^"]*"(, "[^"]*")*' <file>` captures the literal run (matches `region_fencing.go:110`'s two-line call correctly — the run ends before the newline; see P-1).
  - Strip the `idgen.Anchor(` prefix, split on `","`, join with `.` → `audit.region.promote`, `audit.preference.set`, `audit_hold.set`, `legacy.cutover.set`, `sync.job.finished`.
  - Calls with a dynamic first argument (`idgen.Anchor(parts...)` at `operation_audit.go:68`; `goodOperation` in the negative fixture) produce no match — skipped by construction.
  - Exclusions: `_test.go`, `*.pb.go`, and `internal/pkg/idgen/idgen.go` (R-2 file-exclusion precedent; defense-in-depth — that file contains no `idgen.`-qualified call).

  **Golden manifest** (declared at the top of the script, mirroring the golden case set in `idgen_test.go`):

  ```
  audit.region.promote      (G14)   audit.preference.set      (G15)
  audit_hold.set            (G9/G9b) audit_hold.removed       (G10)
  legacy.cutover.set        (G11-G13) sync.job.finished       (G8)
  # documentation entries only — the live operation site passes the action
  # dynamically, so these are never extracted, but they complete the mirror:
  operation.started / operation.step.finished / operation.reconciled /
  operation.compensating / operation.compensation.finished /
  operation.compensation.reconciled / operation.finished      (G1-G7)
  ```

  Live-tree extraction must yield exactly the six checkable discriminators, all present in the manifest → gate green on the unmodified tree.

- **R-5 — Namespace byte-literal pin (C2 extension of R-2).** In the R-2 block, count occurrences of the byte literal `38248e3a-32f0-4929-9b56-b2dbd75aa849` in the real file `internal/pkg/idgen/idgen.go` (anchored path, exactly like the existing `var AnchorNamespace` count check) and require exactly 1:

  ```bash
  ns_literal="$(grep -c "38248e3a-32f0-4929-9b56-b2dbd75aa849" "$SELF_DIR/../internal/pkg/idgen/idgen.go" 2>/dev/null || true)"
  if [ "$ns_literal" != "1" ]; then
    violations="${violations}R-2 AnchorNamespace byte literal count = ${ns_literal}, want exactly 1
  "
  fi
  ```

  A reassignment to a hex-literal form (`uuid.UUID{0x38, 0x24, …}`) deletes the literal, drops the count to 0, and reddens the gate — closing the evasion path where neither the `MustParse` grep nor the count check fires today.

- **R-6 — Gate self-tests (fixtures).** Extend `scripts/testdata/anchored-event-ids/`:
  - **Negative fixture — unchanged, must stay green.** Its two literal anchors (`audit_hold.set`, `legacy.cutover.set`) are manifest entries; the dynamic-arg `goodOperation` is skipped by extraction. The existing `scan_root "$FIXTURES/negative" 0` self-test must remain green.
  - **Positive fixture — must redden with an R-3-attributable message.** Add `_ = idgen.Anchor("shape.not.pinned", "tenant", "x")` to `positive/positive.go`. The positive-scan self-test currently only asserts the scan fails; upgrade it to **capture the output and assert the `R-3` marker appears**, proving the new check fires (the fixture already reddens for R-1/R-2, so output-level attribution is required). A dedicated `r3_positive/` fixture containing only the unknown-discriminator anchor is an acceptable equivalent that makes attribution trivial.
  - **cmd positive fixture — unchanged.** It reddens via R-1 under the `allow=1` scan and has no anchor literals, so R-3 adds nothing there.

---

## 4. API changes

| Surface | Change | Type |
|---|---|---|
| `idgen.Anchor(parts ...string) ID` | **None** — signature, namespace, encoding, panic behavior unchanged | Runtime Go API (none) |
| `idgen.AnchorNamespace` | **None** — same constant, same value | Runtime Go API (none) |
| All seven anchor call sites (`region_fencing.go`, `preference.go`, `audit_hold.go`, `sync_audit.go`, `legacy_cutover.go`, `operation_audit.go`, `projection_replication.go`) | **None** — byte-identical | Runtime |
| `TestAnchor_GoldenConstants` case table | +3 cases (G14, G14b, G15; optional G14c) + one explicit `G14b != G14` assertion | Test API (additive) |
| `scripts/check-anchored-event-ids.sh` | + R-3 rule class + golden manifest (new script-level constant) + R-5 byte-literal pin; CLI unchanged (same positional roots, same exit-code contract, same self-test wiring) | Gate interface (additive) |
| `scripts/testdata/anchored-event-ids/positive/positive.go` | +1 unknown-discriminator anchor (or new `r3_positive/` dir) | Fixture (additive) |
| `scripts/testdata/anchored-event-ids/negative/negative.go` | **None** | Fixture (none) |
| Makefile / config / migrations / protobuf | **None** | — |

There is **no wire/API contract change** for any consumer: the derived EventIDs for existing facts are already deterministic and are **not** re-derived by this change — the goldens merely *observe* and pin the existing derivation. Nothing that reads `audit_ledger` or `event_store` sees a different value.

---

## 5. Compatibility constraints

1. **Zero call-site churn (REQ-7 invariant).** `region_fencing.go`, `preference.go`, `audit_hold.go`, `sync_audit.go`, `legacy_cutover.go`, `operation_audit.go`, and every service test stay byte-identical. If any new golden fails against the live code, the correct resolution is to fix the golden's *parts* only after re-verifying the call site byte-for-byte (the call site is authoritative) — never to change a call site to fit a golden. Changing a call site would alter historically derived EventIDs and silently disable ledger dedup across a rollout.
2. **Contract constants stay fixed.** The namespace value and the NUL-join encoding are never rotated or extended by this change. The goldens assert the current semantics.
3. **No R-1/R-2 semantic change.** R-3 is a new, purely additive rule class; R-1/R-2 checks, the C2 allowlist, and their messages remain byte-identical in behavior. The R-5 literal pin lives inside the R-2 block but only adds a violation condition.
4. **Gate parameterization unchanged.** Roots are positional (Makefile passes `internal cmd`); the `allow` flag semantics are untouched; self-tests run before the live scan exactly as today.
5. **Negative fixture stays green.** Its anchors are manifest entries; its dynamic-arg anchor is unextractable by construction (P-2).
6. **File-size limit.** `idgen_test.go` must stay ≤ 500 lines (290 today; G14/G14b/G15 add ~10 lines). The script grows by ~30–40 lines and is not Go-file-limited.
7. **Manifest/golden single-source discipline.** The script manifest and the test case table must be updated in the same commit; the script header documents the cross-reference. (Optional hardening: a comment near the manifest listing G-tags per entry, as in §3.)
8. **Seen-map collision proof.** The existing `seen` map in `TestAnchor_GoldenConstants` must continue to prove G14/G14b/G15 collide with no existing golden and with each other.

---

## 6. Failure modes

| # | Failure mode | Detection | Consequence & resolution |
|---|---|---|---|
| F1 | Pre-existing drift: a golden fails against the current call site | R-1/R-2/R-3 goldens red at implementation time | The call site is authoritative (REQ-7). Re-verify the call-site byte layout; correct the golden *parts*; re-cross-check with the independent v5 reproduction (§8). Do **not** change the call site — that would break historical dedup. If the call site genuinely changed recently, this gate reddening is the intended alert and the derivation contract must be reviewed before any rollout. |
| F2 | Shape drift after this change (part order, arity, discriminator spelling, added/removed part) | Golden mismatch (G14/G15) and/or R-3 discriminator change | Gate reddens before merge. A new discriminator without a golden also reddens via R-3 ("no golden pin for shape …"). |
| F3 | Namespace rotation / hex-literal reassignment evasion | R-5 count ≠ 1 (plus the existing `var AnchorNamespace` count and `MustParse`-ban) | Gate reddens. Any namespace change is contract-breaking and must never ship (doc comment in `idgen.go:15-17`). |
| F4 | R-3 false positive: the regex matches a comment or doc string mentioning `idgen.Anchor("…")` | Gate reddens on a non-call line | Accepted class of false positive, consistent with the existing grep-based R-1/R-2 checks (they also match comments); the fixtures are the oracle and a false positive is a loud, fixable signal (adjust the comment or add the discriminator to the manifest only if a real golden exists). |
| F5 | R-3 false negative: discriminator built from concatenation (`"audit" + ".region.promote"`) or an escaped quote inside a literal | Extraction sees no literal run | Documented limitation of a grep-based guardrail. The gate is not a security boundary; the goldens pin the shapes that exist, and R-2 still bans the underlying primitive misuse. A future structural check (AST-based) is noted as out of scope. |
| F6 | Multi-line call breaks extraction | R-3 misses `region.promote` | Not a live risk: `region_fencing.go:110-111`'s literal run terminates on line 110 (verified, P-1); the regex stops at the first non-literal argument regardless of line breaks. Fixtures cover the single-line and (via `region_fencing.go` itself) multi-line shapes. |
| F7 | Manifest/golden divergence (one updated, not the other) | Either a red gate (manifest stricter) or an uncaught shape (manifest looser) | Mitigated by the single-commit discipline (C7) and by the script header cross-referencing the test. |
| F8 | Rollout ordering | None | This change has **no production data or runtime path**; it is tests + a gate. There is no deployment ordering concern, no DB migration, and historical EventIDs are untouched. The only rollout note: because goldens must match *current* call-site semantics, land goldens and gate in the same commit as any (hypothetical, here: none) derivation-contract fix. |
| F9 | Retry/collision semantics regression (e.g. someone makes the digest slot optional, collapsing distinct payloads) | G15 no longer reproduces; retry tests still pass (self-referential) but the golden reddens | This is exactly the hole being closed: the golden is the only independent oracle. Gate reddens pre-merge. |

---

## 7. Migration / rollout steps

There is **no data migration and no runtime rollout**. The change is source-only:

1. **Add the goldens (R-1/R-2/R-3):** extend `TestAnchor_GoldenConstants` with G14, G14b, G15 (+ optional G14c) and the explicit `G14b != G14` assertion. Run `go test ./internal/pkg/idgen/ -run 'TestAnchor_GoldenConstants|TestAnchor_' -count=1` — must pass against the unmodified call sites.
2. **Add the gate rules (R-4/R-5):** extend `scripts/check-anchored-event-ids.sh` with the R-3 extraction + manifest and the R-5 literal pin; keep the CLI and existing self-tests intact.
3. **Update fixtures (R-6):** add the unknown-discriminator anchor to `positive/positive.go` (or add `r3_positive/`); upgrade the positive self-test to assert the `R-3` marker in the output; leave `negative/` and `cmd_positive/` unchanged.
4. **Run the gates:** `make check-anchored-event-ids`, `make test`, `make lint`, `make lint-strict`, `make check-file-sizes`, `make build`. All green.
5. **Regression proof (reviewers, non-committed):** temporarily delete G14 from the manifest (or rename `region.promote` at `region_fencing.go:110` in a scratch copy) and observe the gate redden with an R-3 message; restore.
6. **Ship as one commit** containing goldens + gate + fixtures (C7 single-source discipline). No version bump, no config, no migration file.

---

## 8. Testable acceptance mapping

| Acceptance | Requirement | Verification (commands / assertions) |
|---|---|---|
| **A1 — Golden regression tests** | REQ-1, REQ-2, REQ-3 | `internal/pkg/idgen/idgen_test.go` contains G14 (`0c9b9631-6da3-5c20-813d-4ec76e5dc961`), G15 (`52d8beb8-b5fb-55ba-ad4b-e3ff011c37e5`), G14b (`efd6375b-4c86-57b1-ac7b-0e821537ce47`); explicit `G14b != G14`; seen-map proves no collision. `go test ./internal/pkg/idgen/ -run 'TestAnchor_GoldenConstants|TestAnchor_' -count=1` and `make test` green. |
| **A2 — Gate shape coverage + namespace pin** | REQ-4, REQ-5 | Script contains the R-3 manifest check and the R-5 literal pin. `make check-anchored-event-ids` green on the live tree (`internal cmd`). Self-test: positive fixture reddens with an R-3-attributable message (output asserted); negative fixture stays green. Regression proof: manifest without G14 (or renamed call-site discriminator) → red with `R-3`. |
| **A3 — Unchanged-green gates, no collateral** | REQ-7 | `make lint && make lint-strict && make check-file-sizes && make build` green; `idgen_test.go` ≤ 500 lines; `region_fencing.go`, `preference.go`, `audit_hold.go`, `sync_audit.go`, `legacy_cutover.go`, `operation_audit.go`, and all service tests byte-identical (git diff shows only `idgen_test.go`, the script, and fixtures). |
| **A4 — Cross-verification evidence** | REQ-1…REQ-3 (values) | The §9 reproduction script regenerates G14/G14b/G15 (and the committed G1 as calibration) from an independent RFC 4122 v5 implementation; all values match. Seen-map assertion in `TestAnchor_GoldenConstants` proves no golden in the set collides with another. |

---

## 9. Verification appendix — independent v5 reproduction

Executed against the live tree's constants (`uuid5` from Python's standard library — an independent implementation, not Go's `google/uuid`):

```python
import uuid
NS = uuid.UUID("38248e3a-32f0-4929-9b56-b2dbd75aa849")
def v5(parts): return str(uuid.uuid5(NS, "\x00".join(parts)))

# calibration: committed golden G1
assert v5(["operation.started","tenant-alpha","0195b2c6-9f40-7c01-8000-000000000001","","running"]) == "d0551f9f-d7e9-5773-aa06-fe022460ba93"

assert v5(["audit","region.promote","tenant-alpha","0195b2c6-9f40-7c01-8000-000000000001","us-east","0"]) == "0c9b9631-6da3-5c20-813d-4ec76e5dc961"   # G14
assert v5(["audit","region.promote","tenant-alpha","0195b2c6-9f40-7c01-8000-000000000001","us-east","1"]) == "efd6375b-4c86-57b1-ac7b-0e821537ce47"   # G14b (!= G14)
assert v5(["audit","preference.set","tenant-alpha","u-1","a"*64]) == "52d8beb8-b5fb-55ba-ad4b-e3ff011c37e5"                                        # G15
assert v5(["audit","region.promote","tenant-alpha","0195b2c6-9f40-7c01-8000-000000000001","us-east","2"]) == "6b15c036-15c8-581b-b9c4-03c6b0f2d01b"  # G14c (optional)
```

All assertions pass at HEAD. The independent implementation agrees with Go's `uuid.NewSHA1` on namespace, separator (NUL), and encoding — the proposed fixture values are trustworthy pins of the current call-site semantics.

---

## 10. Explicitly out of scope

- Any change to an `idgen.Anchor` call site, service, repository, migration, or audit-write path (REQ-7).
- The other two directions from the source analysis: (a) making Anchor's NUL-rejection unreachable from user input at the `audit_hold` surface; (b) `NewID()` same-millisecond burst monotonicity and its failure-mode doc.
- Any expansion of the golden set beyond G14/G14b/G15 (+ optional G14c), and any change to R-1/R-2 semantics.
- AST-based structural shape checking (noted in F5 as a possible future hardening).
