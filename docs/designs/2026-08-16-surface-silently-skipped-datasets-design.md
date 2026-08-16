# Design — Surface silently-skipped datasets so an incomplete source summary can never be marked reconciled

- **Date:** 2026-08-16
- **Module:** `internal/connector` (shared `JSONSummaryConnector`), consumed by `internal/connector/aerovault`
- **Direction ID:** Surface silently-skipped datasets (value 8 / risk_reduction 8 / effort 5 / confidence 8)
- **Status:** Proposed — implementation and acceptance are deliberately NOT included in this repository change set; this document is the design deliverable.

---

## 1. Verification verdict

All claims in the requirements specification were checked against the live tree at HEAD
(no in-package probes were needed; every cited location was read directly). The one
cited file-location correction is itself confirmed: the skip logic lives in
`internal/connector/http_summary_request.go`, and `internal/connector/http_summary.go`
holds only the transport/decode layer (it calls `summaryFromPayload(&payload)` at
line 273).

| # | Claim | Verified location | Verification result |
|---|---|---|---|
| E1 | `appendDatasetSnapshots` silently `continue`s on unregistered or foreign-source datasets | `http_summary_request.go:148-163`; silent `continue` at line 155 (`if !ok \|\| descriptor.Source != string(c.source) { continue }`) | ✅ **Confirmed.** No record of the skip is produced. |
| E2 | `summaryFromPayload` passes `payload.Complete` through untouched regardless of drops | `http_summary_request.go:138` — `result := &Summary{..., Complete: payload.Complete}` built before `appendDatasetSnapshots` (called at line 140) | ✅ **Confirmed.** |
| E3 | `Summary` struct has no skipped-dataset reporting field | `connector.go:42-49` — fields `Source, SourceRegion, Version, Snapshots, Sources, Memberships, Complete` | ✅ **Confirmed.** |
| E4 | Reconcile marking gated on `summary.Complete` | `sync_projection.go:25` (`markReconciledSnapshots`) and `:49` (`markReconciledReferences`), both under `job.JobType == domain.SyncJobReconcile && summary.Complete` | ✅ **Confirmed.** |
| E5 | Reconcile job reports per-source `complete=true` and `SyncJobCompleted` unless hard failure | `sync_job.go:339-355` — per-source status `{"status": "synced", "count": ..., "complete": summary.Complete}` (line 340); `repaired_count = drift` only when `summary.Complete` (lines 343-346); job status `SyncJobCompleted` unless `failures > 0` (lines 354-357) | ✅ **Confirmed.** A skipped dataset increments neither `failures` nor any counter. |
| E6 | Vault dataset registry entries | `domain/aggregation.go:41-62` — `DatasetAeroVaultTenants/Buckets/Usage` (41-43), registry entries with `Source: "aero-vault"` (48-59); `DatasetDescriptorOf` (~line 63) | ✅ **Confirmed.** |
| E7 | `aerovault` is a thin delegation wrapper | `aerovault/aerovault.go` — `New(cfg) = connector.NewJSONSummaryConnector(connector.SourceAeroVault, cfg)` | ✅ **Confirmed.** Fix lands in the shared connector; the vault module inherits it. |
| E8 | Test infrastructure and hardcoded source labels | `http_summary_test.go:47,75` construct `SourceAeroVault` connectors explicitly; `fault_injection_test.go:173` `newTestConnector` hardcodes `SourceAeroIM` | ✅ **Confirmed.** New vault-labeled connector tests must construct explicitly or extend the helper with a source parameter. |
| E9 | Repository reconcile seams usable for the service-level regression test | `repository/account_snapshot.go:35-37` (`SnapshotReconciler.MarkUnavailable`), `repository/account_source.go:38-39` (`AccountReferenceReconciler.MarkMissingReferences`); both type-asserted at `sync_reconcile.go:56,82` | ✅ **Confirmed.** Recording fakes implementing them are directly observable. |

**Additional facts verified for the design (not in the spec, load-bearing for test C):**

- `reconcileMissingSnapshots` (`sync_reconcile.go:56`) returns `0, nil` if the repo does
  not implement `SnapshotReconciler` **or** if `version <= 0`; `markReconciledReferences`
  (`sync_reconcile.go:82`) returns `nil` when `version <= 0`. The positive control must
  therefore use `Version > 0` (spec says `5` — correct) and the fakes **must** implement
  the reconciler interfaces, or the positive control is vacuous.
- `AccountService.UpsertSource` / `UpsertMembership` are `account.go:34-35`;
  `persistSyncSummary` uses only the `snapshots`, `accounts`, and `accountRepo` fields of
  `syncJobService` (`sync_job.go:36-51`), so a `syncJobService{...}` literal with those
  three fields faked and everything else nil is a complete harness.
- `persistSyncSummary` dereferences `account.ID` in both marking calls; the test must
  pass a non-nil `*domain.Account`.
- Line counts (file-size gate headroom): `http_summary_request.go` = 193,
  `connector.go` = 127, `sync_projection.go` = 54. All far below 500.
- No `sync_projection_test.go` exists today; `http_summary_test.go` (95 lines) already
  carries vault-labeled summary cases; `http_summary_request_test.go` (49 lines) holds
  erase-path transport tests only.
- `summaryPayload` (`http_summary.go:276-284`) — `Complete bool json:"complete"`,
  `Datasets map[string]json.RawMessage json:"datasets"`. `AccountDataset` is a string
  type, so conversion of arbitrary raw keys always succeeds; every map key is a
  skip candidate.

**Verdict: verified.** The failure chain is real: a deployed Vault returning
`{"complete": true, "datasets": {"aero-vault.tenants": ..., "<unregistered>": ...}}`
causes E1 to drop the unprojectable datasets with no record, E2 to preserve
`Complete=true`, and E4/E5 to durably mark the dropped datasets reconciled (downgrading
previously projected snapshots to `unavailable`/missing-references status — an active
data-destruction consequence, not merely a reporting gap) while the job reports
`synced`. This violates the AGENTS.md dead-letter doctrine: consumers must not
acknowledge data they did not apply. The negative path (payload containing only the
three registered vault datasets) is untouched today; the fix must preserve that.

---

## 2. Problem statement

`appendDatasetSnapshots` silently drops received datasets that are (a) unregistered in
`domain.AccountDatasetRegistry` or (b) registered to a different source, and
`summaryFromPayload` copies `payload.Complete` into the result untouched. Because
`persistSyncSummary` gates `markReconciledSnapshots`/`markReconciledReferences` solely
on `summary.Complete`, and `sync_job.go` reports per-source `complete` verbatim, a
source-version drift (Vault returning a dataset aero-id does not yet register — exactly
the AGENTS.md "re-check the deployed source version" warning) or a misconfigured/foreign
endpoint durably marks dropped data as reconciled and reports "synced".

The fix makes the connector fail-closed at the shared boundary: any received-but-not-
projectable dataset downgrades the summary to incomplete, which suppresses reconcile
marking via the existing service gate. No service logic changes are required for
correctness; a regression test pins the combined behavior.

---

## 3. Design

### 3.1 API change — `connector.Summary`

`internal/connector/connector.go`, `Summary` struct (lines 42-49), add one field:

```go
type Summary struct {
	Source       Source
	SourceRegion string
	Version      int64
	Snapshots    []*domain.AccountSnapshot
	Sources      []*domain.AccountSource
	Memberships  []*domain.AccountMembership
	Complete     bool
	// SkippedDatasets lists dataset names received in a source payload that were
	// not projected, because they are not in the account dataset registry or are
	// registered to a different source. A non-empty list means the source summary
	// is incomplete from aero-id's perspective regardless of the source's own
	// "complete" flag. Raw names as received, ascending sorted, no duplicates.
	SkippedDatasets []string
}
```

- **Raw names, never reinterpreted:** values are the string keys of the `datasets`
  JSON object as received, including names that are not valid registry entries; they are
  not coerced into projected snapshots or registry lookups after recording.
- **Sorted ascending, deduplicated:** map keys are unique by construction; the slice is
  sorted once after the loop so downstream assertions and any future job-result
  surfacing are deterministic.
- **Backward compatible:** additive field on a struct; `Connector`/`CommandConnector`/
  `OperationStatusConnector` interfaces, constructors, protobuf, and OpenAPI are
  untouched. No callers construct `Summary` literals with all fields except tests.

### 3.2 Behavior change — `summaryFromPayload` / `appendDatasetSnapshots`

`internal/connector/http_summary_request.go`:

1. **Record skips (R1/R3).** In `appendDatasetSnapshots`, replace the bare `continue`
   at line 155 with a collect-and-continue:

   ```go
   for name, raw := range datasets {
       dataset := domain.AccountDataset(name)
       descriptor, ok := domain.DatasetDescriptorOf(dataset)
       if !ok || descriptor.Source != string(c.source) {
           result.SkippedDatasets = append(result.SkippedDatasets, name)
           continue
       }
       // ... unchanged projection path ...
   }
   sort.Strings(result.SkippedDatasets)
   ```

   Zero snapshots are projected for skipped datasets — the skip boundary itself is
   preserved (R3). The hard-error path for a **registered** dataset with unparseable
   JSON (`source.dataset_invalid`, lines 157-160) is untouched (R7). `sources`/
   `memberships` arrays are still processed by `sourceReferences`/
   `membershipReferences` after the skip loop, as today (R7).

   **Implementation choice:** populate `result.SkippedDatasets` in place and sort at the
   end of `appendDatasetSnapshots`, keeping the method signature and its single
   call site unchanged. (Returning `([]string, error)` is an equally valid alternative;
   it changes an unexported signature only. The in-place form is the smaller diff.)

2. **Downgrade `Complete` (R2).** In `summaryFromPayload`, after
   `appendDatasetSnapshots` succeeds (and before returning):

   ```go
   result.Complete = payload.Complete && len(result.SkippedDatasets) == 0
   ```

   Downgrade only: `payload.Complete == false` stays `false` regardless of skips;
   a `true` payload with ≥1 skip becomes `false`; a `true` payload with no skips stays
   `true`. The change is source-agnostic — every source served by the shared
   `JSONSummaryConnector` (Snaplink, Aero IM, Aero Vault via `aerovault.New`) gets the
   same fail-closed semantics at the shared boundary.

### 3.3 No service changes

`persistSyncSummary` (`sync_projection.go:25,49`) already gates
`markReconciledSnapshots`/`markReconciledReferences` on `summary.Complete`; with the R2
downgrade, an incomplete-due-to-skips summary correctly suppresses both. R5 is preserved
by construction: a genuinely complete payload (no skips) with requested-but-absent
datasets still runs `MarkUnavailable`/`MarkMissingReferences` unchanged, because
`Complete` is still `true` and `SkippedDatasets` is not consulted by the service gate.
No domain, repository, app-wiring, or audit changes. `sync_job.go` line 340 reports the
per-source `complete: false` verbatim — the required observable change; job-level
`SyncJobCompleted` vs `PartiallyComplete` classification stays failure-driven (non-goal).

---

## 4. Change set

| File | Change |
|---|---|
| `internal/connector/connector.go` | Add `SkippedDatasets []string` to `Summary` (3.1). |
| `internal/connector/http_summary_request.go` | `appendDatasetSnapshots`: collect skipped raw names + `sort.Strings`; `summaryFromPayload`: set field + apply R2 downgrade (3.2). Add `"sort"` import. |
| `internal/connector/http_summary_skip_test.go` (new) | Acceptance A and B (§6). |
| `internal/service/sync_projection_test.go` (new) | Acceptance C (§6). |

No domain, repository, app-wiring, protobuf, OpenAPI, migration, or audit changes.
File-size headroom confirmed in §1 (193 + ~10 lines and 127 + ~6 lines, both well
below 500).

---

## 5. Compatibility constraints

1. **Additive-only surface.** The new struct field breaks no interface, no constructor
   call, no protobuf contract, no JSON wire format, and no HTTP/gRPC API. Old binaries
   that ignore the field are behaviorally identical; the field is only read inside
   aero-id's sync path.
2. **Fully-registered payloads are byte-identical in behavior.** The skip condition
   never fires for a vault payload containing only `aero-vault.tenants`,
   `aero-vault.buckets`, `aero-vault.usage`; `Complete == payload.Complete` and
   `SkippedDatasets` is empty (R4). Reconcile marking, per-source status, and job status
   are unchanged for the healthy path.
3. **Fail-closed direction is conservative, not fail-open.** The new downgrade can only
   *suppress* reconcile marking and flip per-source `complete` to `false`; it can never
   mark something reconciled that was previously unmarked. Operational consequence
   (deliberate, see §7): when a source begins returning a new dataset, reconcile pauses
   for that source until the dataset is added to `AccountDatasetRegistry` — pause, not
   data destruction, which is the required behavior.
4. **Shared-connector reach.** The change applies to Snaplink and Aero IM connectors at
   the same time. This is correct per R2 (a silent drop at the shared boundary is
   equally load-bearing for all sources); it is not a vault-only carve-out. Existing
   connector contract tests (`contract_test.go`) do not construct summaries with
   skipped datasets and are unaffected.
5. **Error semantics unchanged.** Unparseable registered datasets still return
   `RemoteError{Kind: ErrorInvalid, Code: "source.dataset_invalid"}` (fail-closed).
   Skips only cover the previously-silent unregistered/foreign case.

---

## 6. Testable acceptance mapping

### A. Connector regression — skips surfaced, `Complete` downgraded
File: `internal/connector/http_summary_skip_test.go` (new), following the `httptest`
pattern of `http_summary_test.go`. Construct the connector explicitly:
`NewJSONSummaryConnector(SourceAeroVault, HTTPConfig{Endpoint: server.URL,
SummaryPath: "/summary", Client: server.Client()})` — the shared `newTestConnector`
helper at `fault_injection_test.go:173` hardcodes `SourceAeroIM` (E8).

Fixture: `{"source_region":"west","version":5,"generated_at":"<ts>","complete":true,
"datasets":{"aero-vault.new_thing":{...},"aero-im.profile":{...}}}` where
`aero-vault.new_thing` is unregistered and `aero-im.profile` is registered to
`aero-im`. Assert, in order:

1. `summary.Complete == false` (R2 downgrade);
2. `reflect.DeepEqual(summary.SkippedDatasets, []string{"aero-im.profile", "aero-vault.new_thing"})` — sorted, raw names (R1);
3. `len(summary.Snapshots) == 0` — zero snapshots for either skipped dataset (R3);
4. `summary.Source == "aero-vault"`, `summary.Version == 5`, `summary.SourceRegion == "west"` — non-dataset fields unaffected.

### B. Negative — no false downgrade
Same fixture style; payload contains only `aero-vault.tenants`, `aero-vault.buckets`,
`aero-vault.usage` with `complete: true`. Assert `summary.Complete == true`,
`len(summary.SkippedDatasets) == 0`, `len(summary.Snapshots) == 3`, each snapshot's
`Dataset` ∈ the three vault datasets and `Source == "aero-vault"` (R4).

### C. Service regression — reconcile marking suppressed when incomplete due to skips
File: `internal/service/sync_projection_test.go` (new), recording-fake style of
`sync_audit_export_test.go`.

Fakes (per §1 notes, all reconciler interfaces **must** be implemented or the positive
control is vacuous):

- `fakeSnapshotRepo`: embeds `repository.SnapshotRepository` (nil interface for
  `GetSnapshot`/`ListSnapshots`/`ListReplicableSnapshots`), defines `UpsertSnapshot`
  (records `upsertCalls`) and `MarkUnavailable` (records `markUnavailableCalls`,
  returns `(len(missing), nil)`).
- `fakeAccountRepo`: embeds `repository.AccountRepository` (nil), defines
  `MarkMissingReferences` (records `markMissingCalls`, returns nil).
- `fakeAccountService`: defines `UpsertSource`/`UpsertMembership` returning nil
  (per `AccountService` at `account.go:34-35`).
- Harness: `s := &syncJobService{accounts: fakeAccountService, accountRepo: fakeAccountRepo, snapshots: fakeSnapshotRepo}` — only the fields `persistSyncSummary` exercises (`sync_job.go:36-51`). Call `s.persistSyncSummary(ctx, job, account, connector.SourceAeroVault, requested, summary)` with a non-nil `account` carrying an ID.

**Case 1 (the gate):** `job := &domain.SyncJob{AccountID: <id>, JobType:
domain.SyncJobReconcile}`, `summary := &connector.Summary{Source:
connector.SourceAeroVault, Complete: false, SkippedDatasets:
[]string{"aero-vault.unknown"}, Version: 5}`, zero snapshots/sources. Assert
`upsertCalls == 0`, `markUnavailableCalls == 0`, `markMissingCalls == 0`, and the call
returned nil.

**Optional second negative within Case 1:** identical setup with `Complete: false` and
no skips — also suppresses marking, pinning that the gate is on `Complete`, not on
`SkippedDatasets` specifically.

**Positive control (proves wiring, prevents a vacuous pass):** `Complete: true`, no
skips, `requested = [aero-vault.tenants, aero-vault.buckets, aero-vault.usage]`,
`summary.Snapshots = [one snapshot, e.g. tenants, Source "aero-vault", Version 5]`,
plus one source reference. Assert `upsertCalls == 1`,
`markUnavailableCalls == 1` (missing = buckets + usage — the requested-but-absent
reconcile signal of R5), and `markMissingCalls == 1`. Both counters `> 0` proves the
fakes observe reconcile marking and only the `Complete` gate suppressed it.

### Acceptance ↔ requirement map

| Acceptance | Requirements pinned |
|---|---|
| A | R1 (field, raw names, sorted), R2 (downgrade), R3 (zero snapshots), non-dataset fields untouched |
| B | R4 (no false downgrade), R3 |
| C | R6 (downstream gate pinned), R5 (positive control exercises requested-but-absent), R2 (gate input) |
| A+B+C together | R7 unchanged failure modes (no test touches `source.dataset_invalid`; A/B fixtures use unregistered/foreign keys only) |

---

## 7. Failure modes and operational notes

| # | Scenario | Behavior before | Behavior after |
|---|---|---|---|
| 1 | Source returns unregistered dataset, `complete: true` | Silent drop; snapshots of previously projected rows downgraded to `unavailable`/references missing; job reports `synced` | `Complete=false`; reconcile marking suppressed; per-source `complete: false`; no rows downgraded |
| 2 | Source returns foreign-source dataset (endpoint misconfiguration) | Same as #1 | Same as #1 — the skip and downgrade fire regardless of which registered source owns the name |
| 3 | Registered dataset with unparseable JSON | Hard `source.dataset_invalid` error, job per-source `unavailable` | Unchanged (R7) |
| 4 | `complete: false` from the source, no skips | Reconcile suppressed (existing) | Unchanged; gate remains on `Complete` |
| 5 | `complete: true`, `datasets` empty | Requested-but-absent → `MarkUnavailable`/`MarkMissingReferences` (normal reconcile signal) | Unchanged (R5) — skips are distinct from absence |
| 6 | Source legitimately adds a new dataset (deployed before registry entry lands) | Silent drop + destructive reconcile marking | Reconcile paused for that source; per-source `complete: false`; no downgrades. **Operational contract:** registry entries must be deployed before/with the source rollout that introduces the dataset (AGENTS.md allow-list doctrine). This is a coordination note for the platform team, not a code change in this direction |
| 7 | Region mismatch | Hard `source.region_mismatch` before dataset processing | Unchanged — skip recording never runs (checked before `appendDatasetSnapshots`) |

Residual (informational, out of scope): the job-level result payload does not yet
surface `SkippedDatasets` per source; consumers of job results see `complete: false`
and `repaired_count: 0` but not the skipped names. Surfacing them in the job result is
a plausible follow-up, deliberately excluded here. The skip recording is source-
agnostic in the shared connector; the vault module is the mandated verification target,
but Snaplink and Aero IM are protected at no additional cost.

---

## 8. Migration steps

No database migration, no data migration, no wire/API migration:

1. Implement §3.1 (struct field) and §3.2 (collect + sort + downgrade).
2. Add acceptance A/B (`http_summary_skip_test.go`) and C (`sync_projection_test.go`).
3. Run gates: `make test && make lint && make lint-strict && make check-file-sizes &&
   make check-audit-swallow && make check-anchored-event-ids && make check-architecture
   && make check-composition-root && make check-proto-sync && make check-integration-compile
   && make build` (full done-gate).
4. Deploy coordination: when any source begins returning a dataset not yet in
   `AccountDatasetRegistry`, ship the registry addition (separate deliberate act) before
   or with that source's rollout, so legitimate new datasets do not pause reconcile.
   No rollback hazard: reverting this change restores prior behavior byte-for-byte for
   the healthy path; the only historical divergence is that skipped datasets will have
   been recorded (not projected) during the window.

---

## 9. Non-goals / out of scope

- No change to `Erase`/`OperationStatus` region propagation (separate direction).
- No change to job-level status classification in `sync_job.go` (`SyncJobCompleted` vs
  `PartiallyComplete` remains failure-driven); the per-source status now naturally
  reports `complete: false` via existing line 340.
- No new audit facts, no API/OpenAPI surface changes (the field is internal to the
  connector/service layer).
- No registry changes: adding a new vault dataset to `AccountDatasetRegistry` is a
  separate deliberate act, not part of this fix.
