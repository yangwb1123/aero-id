# Design — Sweep-and-lock the in-tx audit conversion: manifest gate, standalone counter, atomicity matrix (B2-1 hardening)

| | |
|---|---|
| Source | `docs/auto/analyses/cmd-migrate-c1b3771d.json` direction 2; requirements `docs/requirements/2026-08-07-cmd-migrate-sweep-and-lock-in-tx-audit-conversion.md` |
| Sibling plan | `docs/plans/2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop-design.md`; sibling spec `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` |
| Status | Draft — evidence re-verified against working tree (head `13e4676` + uncommitted B2-5 changes, which do not touch this surface) |
| Module label | `cmd/migrate` (campaign slot; **no code lands in `cmd/migrate`** — verified: `cmd/migrate/main.go` is the migration CLI only, zero audit code) |

## 1. Verification of the requirements evidence (untrusted-claims gate)

Every citation in the requirements doc was re-checked against the tree. Verdict: **all load-bearing claims hold**; two cosmetic drifts noted.

| Claim | Verdict |
|---|---|
| 7 `WithAudit` repo surfaces at cited lines (`account_profile.go:67`, `account_provision_audit.go:31`+`:56` quota variant, `callback_audit.go:31/60/85`, `legacy_cutover.go:137`, `sync_job.go:86/292`, `audit_hold.go:127/154`, `event_receipt.go:89` interface `:39`) | ✅ exact |
| Service call sites incl. drift `callback.go:176`→`:174/226/263`; extras `event_ingest.go:141` | ✅ exact |
| **21 bare sites** = 18 cited + 3 omitted (`account_profile.go:62`, `legacy_cutover.go:102`, `file_share.go:114/148`) | ✅ exact — exhaustive `rg 's\.audit\.(Record|Log)\(' internal/service` (non-test) returns exactly 21 |
| `operation_state.go:39` documented A1 standalone ("operation.reconciled is a standalone fact… A1-class") | ✅ exact, wording confirmed |
| Swallow gate: P1 `_ =` ban **and** P2 bare-call ban exist; checked out-of-tx writes unclassified | ✅ partially stale as claimed; the residual gap (mutation-path classification) stands — nothing in the done-gate distinguishes a checked `s.audit.Record` on a mutation path from one on a read path |
| `service/audit_ledger.go:68` `Record` (dup→nil fold; `ObserveAuditIngestFailure` on failure only), `:136` `Log` routes through `Record`; **no success/standalone counter exists** | ✅ exact — `ControlPlaneMetrics` has 16 methods, none for out-of-tx success |
| `audit_wiring_integration_test.go:175` rollback atomicity, `:264-281` `fail_publication_insert` trigger, `:294` publication-insert rollback; file 328 lines | ✅ exact |
| Dedup: `checkAuditDuplicate` skips empty EventID; unique `(tenant_id, source, event_id)` in migration 000050 | ✅ semantics exact; symbol at `:174` (spec cited `:157-171` — cosmetic drift). EventID column is nullable (`*uuid.UUID`), so Postgres UNIQUE permits multiple NULLs ⇒ fresh-ID sites append one row per execution — the REQ-4 "fresh" assertion is physically sound |
| Class A (provision propagates, no swallow) / Class B (operation `:126`, callback `:44/69/97`, hold `:132`, cutover `:142`, sync `:43` swallow in-tx after domain write) | ✅ exact |
| `operationAuditFact` anchors via `idgen.Anchor(action, tenant, op, step, status)` | ✅ exact (`operation_audit.go:58-68`) |
| Stable vs fresh EventID per site (message `ID`, comment `reportID`, replay dry-run fresh `replayID`, replay requested `operation.ID`, admin/ban/block fresh) | ✅ exact |
| `TestRecordAnchoredEventID_Dedup` `:131`, `TestFirstFactDrainDeliversExactlyOnce` `:392`, metrics text assertion pattern `metrics_test.go:150` | ✅ exact |
| Makefile: `check-audit-swallow` + `check-anchored-event-ids` exist; **no `check-audit-manifest`** anywhere | ✅ exact — net-new gate confirmed |
| `scripts/testdata/audit-swallow/` self-test fixtures exist | ✅ exact |

## 2. Design overview

Three independent locks, no behavior change to any audit write:

1. **Manifest gate (REQ-1/REQ-2)** — `scripts/check-audit-manifest.sh` classifies every bare `s.audit.Record(`/`s.audit.Log(` in `internal/service` as `with-audit-fallback` (provably the non-atomic arm of a `WithAudit` mutation) or `standalone-fact` (documented by an enforced marker comment). A new bare site on a mutation path fails the gate until it is routed through `WithAudit` or documented and proven. Orphan rows (a row whose call was deleted by the sibling conversion) are warn-only, so the sibling spec can land without touching this gate.
2. **Standalone counter (REQ-3)** — `ObserveAuditStandaloneRecorded(action)` increments at the single out-of-tx seam (`Record`), on every completed execution including the `ErrAuditDuplicate` fold; in-tx `AppendInTx` paths never touch it. This is the testable observability proof.
3. **Atomicity matrix (REQ-5)** — repo-level integration tests extend the two existing mechanisms (trigger-injected rollback, crash-after-commit) to every `WithAudit` path, pinning each path's duplicate-retry assertion to its documented Class A/B.

The ten post-commit `Log` sites are **not** converted here (sibling spec REQ-3/REQ-4 owns that); this design enumerates, documents, gates, and proves them.

## 3. API changes

### 3.1 New interface method (Go API, additive)

`internal/service/control_plane_metrics.go` — `ControlPlaneMetrics` gains:

```go
ObserveAuditStandaloneRecorded(action string)
```

- **Registry** (`internal/pkg/metrics/control_plane.go`): new `counterMap` field `auditStandaloneRecorded` (bounded by the controlled action vocabulary — the 21 manifest actions, same shape as `auditIngestFail`); text output line `aero_control_audit_standalone_recorded_total{action=%q} %d` emitted in `writeControlPlaneText` beside the ingest-failure line.
- **noop** (`noopControlPlaneMetrics`): empty implementation — the default when no registry is wired; unit tests keep working.
- **Adapter** (`internal/pkg/metrics` → service wiring in `internal/app/new.go`): no new adapter method needed if the adapter forwards by name; verify the adapter implements the full interface (compile error is the fail-closed signal).

### 3.2 `Record` seam change (`internal/service/audit_ledger.go:68`)

```go
func (s *auditLedgerService) Record(ctx context.Context, input AuditFactInput) error {
    fact := buildAuditFact(ctx, input)
    if err := s.repo.Append(ctx, fact); err != nil {
        if errors.Is(err, repository.ErrAuditDuplicate) {
            s.metrics.ObserveAuditStandaloneRecorded(actionKey(input.Action)) // fold counts as a completed execution
            return nil
        }
        if s.metrics != nil {
            s.metrics.ObserveAuditIngestFailure(strings.TrimSpace(input.Source))
        }
        return err
    }
    s.metrics.ObserveAuditStandaloneRecorded(actionKey(input.Action))
    return nil
}
```

`actionKey(a)` = `a` trimmed; empty action (possible via `Log` with empty `Action`) maps to `"unknown"` — keeps the counter map keyed by the manifest vocabulary only. `Log` (`:136`) routes through `Record` unchanged, so all 21 sites are counted at one seam. **Error contract unchanged**: on append failure only `ObserveAuditIngestFailure` fires; the caller-visible error and error code are byte-identical to today.

### 3.3 New gate script + Makefile target (no wire-format API)

- `scripts/check-audit-manifest.sh` — bash + grep only (precedent: `check-audit-swallow.sh`, same P2 anchoring at line start so `if err :=`/`return` lines are excluded).
- `make check-audit-manifest` wired into the done-gate beside `check-audit-swallow`.
- No HTTP/gRPC/protobuf/DB-schema changes. The Prometheus text line is the only externally visible new surface.

### 3.4 Marker comments (REQ-2)

21 call sites gain, on the call's line or the line immediately above:

```go
// audit-site: with-audit-fallback — sibling: UpdateProfileWithAudit
// audit-site: standalone-fact — post-commit legacy-compat fact; no audit
//   atomicity contract with the domain write (sibling conversion owns this)
```

Class must match the manifest row. The gate enforces presence + class + proximity (see 4.3), so "documented" is mechanical.

## 4. Compatibility constraints

| # | Constraint |
|---|---|
| C1 | **Zero behavior change to audit writes.** `Record`/`Log` semantics, dedup fold, anchors, error codes, and `AppendInTx` in-tx paths are untouched. The counter is observe-only; the metrics interface extension is additive and every implementer is compile-checked. |
| C2 | **`make check-audit-swallow` stays green (0 matches).** All new code (counter plumbing, tests) uses checked `if err :=` calls; no `_ =` anywhere. Marker comments are comments — invisible to the P1/P2 patterns. |
| C3 | **Gate is line-drift-proof.** Manifest rows are keyed by `(file, key token)` where the token is the action string literal or symbol in the call (`"account.profile.updated"`, `syncJobFinishedAudit`, `eventAuditInput`, `domain.AuditMessageDelete`, …), never by line number. |
| C4 | **No conversion, no deletion, no new `WithAudit` methods.** Ten post-commit `Log`s and six fallback tails stay; REQ-5 reuses existing repo methods and the existing test mechanisms (`audit_wiring_integration_test.go` stays untouched at 328 lines; new tests go in a sibling file — file-size gate). |
| C5 | **Sibling-spec coexistence.** When `2026-08-06-cmd-server-complete-in-tx-audit-writes.md` lands its REQ-3/REQ-4/REQ-5 conversions and deletes bare calls, the corresponding manifest rows go with them; an orphan row (row whose call vanished) is warn-only (exit 0) so neither work order blocks the other. The reverse direction (call without row) is exit 1. |
| C6 | **No dedup-semantics change.** REQ-4 asserts current behavior: stable-ID sites dedup to exactly 1 ledger row (unique `(tenant_id, source, event_id)`; `checkAuditDuplicate` skips empty EventIDs); fresh-ID sites write exactly 1 row per execution (nullable EventID column ⇒ Postgres permits multiple NULLs). |
| C7 | **Counter vocabulary bounded.** Keyed by `input.Action` from the 21-site manifest vocabulary (plus `"unknown"`); no free-form user input reaches the label. Metrics test asserts the exact text line. |
| C8 | **`cmd/migrate` untouched**, no migration files, no config change, no runtime flags. Deployable as a single commit with the server; no rollout ordering. |
| C9 | **Scan scope unchanged**: manifest scan covers `internal/service` only (the acceptance's scope); the swallow gate keeps scanning `internal/service internal/repository internal/app cmd`. |

## 5. Failure modes

| # | Failure mode | Detection | Mitigation |
|---|---|---|---|
| FM-1 | **New mutation path regresses to a checked out-of-tx `Record`** (the direction's core risk) | `make check-audit-manifest` exits 1: a P2-pattern match in `internal/service` resolves to no manifest row | Developer must route through a `WithAudit` repo method (no bare call) or add a row + marker + REQ-4 test row; the gate's positive fixture proves the failure path |
| FM-2 | **Marker drift** — marker comment removed, class changed, or moved far from the call | Gate verifies `audit-site: <class>` within ≤2 lines of the call's line and class equality with the row | Exit 1; fixture `missing-marker/` covers it |
| FM-3 | **Fallback row lies** — `with-audit-fallback` row whose sibling symbol does not exist in the same file | Gate greps the sibling symbol in the file; missing ⇒ exit 1 | Positive fixture `orphan-fallback/` |
| FM-4 | **Orphan rows after sibling conversion** | Warn-only (exit 0) | Intentional (C5); a `--strict` flag can be added later if cleanup enforcement is wanted — not in scope |
| FM-5 | **Counter semantic drift** — a future edit increments on failure or misses the dup fold, breaking the REQ-4 "counter == executions" invariant | REQ-4 spy assertion `counter == audit executions` fails on first retry arm | Counter increment lives at exactly two points in `Record` (success, dup-fold), both asserted; failure path asserted to observe `ObserveAuditIngestFailure` only |
| FM-6 | **Cardinality blowup** — non-vocabulary action reaches the label | Metrics text test asserts the exact line; code review of `actionKey` | `"unknown"` fallback; bounded counter map (same shape as `auditIngestFail`) |
| FM-7 | **Class reclassification without test update** — repo comment changes A↔B (e.g., swallow added to profile) | REQ-5.2 pins each path's assertion to the documented class; the mismatch fails the test | Test failure is the intended tripwire |
| FM-8 | **Test flakiness from shared trigger** — `fail_publication_insert` leaking across tests | Trigger cleanup in `t.Cleanup` (existing `:285-286` pattern); each matrix subtest reinstalls/drops | New sibling test reuses the helper, never duplicates DDL |
| FM-9 | **Fresh-ID retry assumption breaks** — a site starts minting a stable EventID (or vice versa) | REQ-4 row asserts rows == executions (fresh) or == 1 (stable); mismatch fails | Row-level classification is part of the manifest table, so the change is visible in review |
| FM-10 | **Script self-test rot** — fixtures stop matching, gate silently vacuously-green | Script's self-test block (precedent: swallow gate) fails the script when fixtures stop matching | Positive + negative fixtures committed under `scripts/testdata/audit-manifest/` |
| FM-11 | **Integration tests not compiled** (interface breaks invisible) | `make check-integration-compile` already in the done-gate | New `_test.go` files are integration-tagged; the existing compile guard covers them |
| FM-12 | **Audit ingest failure counter double-counts** — a dup-fold must NOT be an ingest failure | REQ-4 failure arm asserts `ObserveAuditIngestFailure` unchanged (0 for folds) | Assert in the REQ-4 spy |

## 6. Migration steps (phased; each phase leaves the gate green)

**Phase 0 — baseline (no code):** `make check-audit-swallow && make test` on head `13e4676`; record 0 matches. Confirms the sweep starts clean.

**Phase 1 — manifest lock (REQ-1/REQ-2):**
1. Write `scripts/check-audit-manifest.sh` with the 21-row table (file + key token + class + sibling symbol + event-ID retry class), P2-identical scan pattern, marker-proximity check, fallback-sibling check, self-test block, and `scripts/testdata/audit-manifest/{positive,negative}/` fixtures (unmapped site; mapped-without-marker; marker-wrong-class; orphan-fallback; negative: fully-mapped file).
2. Add the 21 `audit-site:` markers across `account.go`×2, `account_profile.go`, `operation.go`, `operation_state.go`, `event_ingest.go`, `replay.go`×2, `sync_job.go`×2, `legacy_cutover.go`, `admin.go`×2, `ban.go`×2, `block.go`×2, `comment.go`, `message.go`, `file_share.go`×2.
3. Add `check-audit-manifest` to the Makefile and the done-gate list in `AGENTS.md`.
4. Gate: `make check-audit-manifest` green; `make check-audit-swallow` still green (markers are comments).

**Phase 2 — counter (REQ-3):**
1. `ControlPlaneMetrics` + `ObserveAuditStandaloneRecorded`; Registry `counterMap` + text line; noop method; adapter compile.
2. `Record` increments per 3.2.
3. Extend `internal/pkg/metrics/metrics_test.go` (register the new call, assert the exact text line) and add a unit test in `service/audit_ledger_test.go` (spy: success → 1, dup-fold → 1, failure → 0 + ingest-failure observed).
4. Gate: `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-audit-manifest`.

**Phase 3 — per-site integration proof (REQ-4):**
`internal/service/audit_standalone_sites_integration_test.go` (integration tag, real Postgres via `testpg.Start`+`testpg.Migrate`, recording metrics spy). Table-driven over the 15 standalone sites; each row: seed setup, invoke (service method; where a domain guard blocks the second invocation, drive the site's audit section directly — the audit section is the unit under test), expected action, event-ID class, expected ledger count. Iterates the same site list as the manifest (the script and the test share the site vocabulary; a new manifest site without a test row fails review because the test table enumerates the 21 rows explicitly).

**Phase 4 — atomicity matrix (REQ-5):**
`internal/repository/audit_wiring_atomicity_matrix_integration_test.go` — table over the 11 `WithAudit` paths (profile [keep covered], operation create, sync create, sync finish, hold put, hold remove, cutover set, callback create, callback delete, callback reset, provision both variants, event apply [keep covered]). Per path: trigger-injected rollback → 0 ledger/0 publication/0 domain rows; duplicate retry → exactly 1 ledger row with class-pinned assertion (A: whole tx rolls back, domain unchanged; B: tx commits, fact count 1); crash-after-commit (fresh connection, drain once) → fact + pending publication present, count still 1, exactly one settlement.

**Phase 5 — gate close:**
`make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-integration-compile && make build`, then `make test-integration`. Update `AGENTS.md` done-gate list and this plan's status.

## 7. Testable acceptance mapping

| Supplied acceptance | Requirement | Concrete assertion (file / check) |
|---|---|---|
| **T-5** manifest maps every bare Record/Log to WithAudit path or documented standalone fact; gate fails on unmapped site | REQ-1.2, REQ-2 | `scripts/check-audit-manifest.sh`: (a) every P2-pattern match in `internal/service` resolves to exactly one row — exit 1 otherwise (positive fixture proves it); (b) every fallback row's sibling symbol present in the same file — exit 1 otherwise; (c) every row's site carries `audit-site: <class>` within 2 lines, class equal — exit 1 otherwise. Wired as `make check-audit-manifest` in the done-gate. Table = the verified 21 sites (6 fallback, 15 standalone) |
| **T-5** integration case: each standalone site increments its counter and never double-writes the ledger row on retry | REQ-3, REQ-4 | `audit_standalone_sites_integration_test.go`: spy counter == audit-call executions ≥ 1 across the retry sequence for all 15 sites; stable-ID sites (message delete, comment resolve, operation.reconciled, replay.requested, sync finish/requested, cutover) → `count(*)` on `audit_ledger` for `(tenant_id, source, event_id)` == 1 and `audit_publications` == 1 per ledger row; fresh-ID sites (viewed/listed, dry-run, admin×2, ban×2, block×2, file share×2) → rows == executions; failure arm asserts `ObserveAuditIngestFailure` not incremented by folds |
| **T-10.1** rollback-after-remote-failure and crash-after-commit keep fact count exact | REQ-5 | `audit_wiring_atomicity_matrix_integration_test.go`: (1) trigger-injected rollback → 0 ledger / 0 publication / 0-or-unchanged domain rows for every path; (2) duplicate retry → exactly 1 ledger row, Class A (profile, provision both arms incl. `created=false` appends nothing) rolls back whole tx, Class B (operation, callback×3, hold×2, cutover, sync×2) commits with in-tx swallow; (3) crash-after-commit → fact + pending publication visible from a fresh connection; one drain ⇒ exactly one settlement, ledger count still 1 |
| **T-4** sweep introduces no `_ =` | REQ-6 | `make check-audit-swallow` green at 0 matches after the sweep; new gate self-tested with positive/negative fixtures; done-gate extended without weakening P1/P2 |

## 8. File inventory

| File | Change |
|---|---|
| `scripts/check-audit-manifest.sh` | new — 21-row manifest, marker/sibling checks, self-test |
| `scripts/testdata/audit-manifest/{positive,negative}/` | new fixtures |
| `Makefile` | `check-audit-manifest` target + done-gate entry |
| `AGENTS.md` | done-gate list |
| `internal/service/control_plane_metrics.go` | interface method + noop |
| `internal/pkg/metrics/control_plane.go` | `auditStandaloneRecorded` counter + text line |
| `internal/pkg/metrics/metrics_test.go` | new line assertion |
| `internal/service/audit_ledger.go` | `Record` increments (2 points) |
| `internal/service/audit_ledger_test.go` | spy unit test (success/fold/failure) |
| 13 service files × 21 sites | `audit-site:` markers |
| `internal/service/audit_standalone_sites_integration_test.go` | new (REQ-4) |
| `internal/repository/audit_wiring_atomicity_matrix_integration_test.go` | new (REQ-5) |
| `internal/app/new.go` (if adapter is name-forwarded) | compile-only |

## 9. Non-goals (unchanged from requirements §5)

No conversion of the ten post-commit `Log` sites; no fallback-tail deletion; no `cmd/migrate` or migration-file changes; no new `WithAudit` repo methods; no dedup/anchor/error-code changes; no manifest-scan expansion beyond `internal/service`; no broker/replay work.
