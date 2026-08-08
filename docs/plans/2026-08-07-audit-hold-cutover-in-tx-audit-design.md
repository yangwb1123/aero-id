# Design — REQ-4..7: migrate `audit_hold` put/remove and `legacy_cutover` set into the business transaction; T-5 rollback tests; read-path counter test

| | |
|---|---|
| Source spec | `docs/requirements/2026-08-07-internal-connector-aeroim-b2-1-b2-2-in-tx-audit-single-delivery.md` (REQ-4/5/6/7; REQ-1/2/3/8 keep-enforced, no code) |
| Module label | `internal/connector/aeroim` (campaign label only; surface is `internal/service` + `internal/repository` + `internal/app`) |
| Head | `13e4676` + working tree |
| Gate | G3 (T-5), done gate per AGENTS.md |

## 0. Evidence verification record (untrusted-claim check)

All claims in the evidence were re-checked against the repo; **no material drift found**. Table of checked claims and results:

| Claim | Check | Result |
|---|---|---|
| aeroim connector is a 7-line adapter, no audit code | `wc -l internal/connector/aeroim/aeroim.go` | ✅ 7 lines |
| Dead inline publisher loop deleted; `NewAuditLedgerService(repo)` single-arg; `Record` appends only | `service/audit_ledger.go:53-79`; `grep '_ = publisher.Publish'` | ✅ 0 hits; `Record` = `repo.Append` + `ErrAuditDuplicate→nil` + `ObserveAuditIngestFailure` on other errors (`:75`) |
| Only `AuditPublisher.Publish` call is `callback_audit.go:114` | `grep '\.Publish(' internal/service/*.go` | ✅ other 3 sites are `SSEHUBPusher` (`notification.go:281,294`) and `event.EventPublisher` (`projection_replication.go:82`) — different interfaces |
| `make check-audit-swallow` exists and green | ran it | ✅ exit 0, self-test ok |
| Zero `_ = s.audit.*` swallow sites in `internal/service` | `grep -rnE '^[[:space:]]*_ = s\.audit\.'` | ✅ 0 hits |
| No `PutHoldWithAudit`/`RemoveHoldWithAudit`/`SetCutoverWithAudit` | `grep -rn 'WithAudit' repository/{audit_hold,legacy_cutover}.go` | ✅ 0 hits |
| Hold/cutover `Record` still post-commit, second tx | `service/audit_hold.go:69,96`; `service/legacy_cutover.go:97` | ✅ both checked (not swallowed), anchored EventIDs, fact inputs all pre-known pre-tx (verified field-by-field: tenant, scopeType, trimmed scopeID, raw reason, caller `expiresAt` param — RETURNING copy-back values are never anchor/payload inputs) |
| Constructors take no ledger | `app/audit.go:187`, `app/new.go:228,411` | ✅ `NewAuditHoldRepository(gdb)` / `NewLegacyCutoverRepository(gdb)` |
| `AppendInTx` exists with advisory lock + dedup + atomic publication row | `repository/audit_ledger.go:106-124` → `appendInTx` → `createAuditRows:172` | ✅; file is 442 lines (under the 500 gate) |
| `requireAuditLedger` fail-closed | `repository/audit_wiring.go:14-28` | ✅ nil + typed-nil panic |
| In-tx `ErrAuditDuplicate` swallow precedent | `repository/callback_audit.go:36-40` | ✅ swallow runs after the domain write, inside the same tx |
| Rollback-injection tests exist for 5 of 7 paths | grep | ✅ callback/provision/audit-wiring/sync; none for hold/cutover |
| `ObserveAuditIngestFailure` wired but never asserted | `service/audit_ledger.go:75`; `control_plane_metrics.go:15,32,61-62` | ✅ `metricSpy` (`callback_audit_test.go:79`) is a no-op for it; only registry-level assertion in `pkg/metrics/metrics_test.go:133` |

Delta confirmed exactly as specced: REQ-4 (hold), REQ-5 (cutover), REQ-6 (rollback tests), REQ-7 (counter test).

## 1. API changes

### 1.1 New repository surface — `internal/repository/audit_hold.go`

```go
// AuditHoldAuditRepository is the in-transaction audit surface for legal
// holds. Each method appends the fact in the SAME transaction as the domain
// write: an append failure rolls the whole mutation back. The concrete hold
// repo implements it; the service type-asserts (pattern: CallbackAuditRepository).
type AuditHoldAuditRepository interface {
    PutHoldWithAudit(ctx context.Context, hold *AuditHold, fact *domain.AuditFact) error
    RemoveHoldWithAudit(ctx context.Context, tenantID string, scopeType AuditHoldScope, scopeID string, fact *domain.AuditFact) error
}
```

- `auditHoldRepo` gains field `audit AuditLedgerTransactionalRepository`, set via `requireAuditLedger("audit_hold", audit)` (nil + typed-nil panic at wiring time).
- `NewAuditHoldRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) AuditHoldRepository` — **breaking constructor change** (all 3 call sites updated: `app/audit.go:187`, plus the 2 integration-test sites).
- `PutHoldWithAudit`: existing upsert (with `RETURNING` copy-back, `audit_hold.go:77-102`) + `AppendInTx(ctx, tx, fact)` inside one `db.Transaction`, append **last**, `ErrAuditDuplicate` swallowed in-tx only (callback precedent, `callback_audit.go:49-54`). The RETURNING copy-back to `hold` still happens before the append.
- `RemoveHoldWithAudit`: existing delete (`:94-98`) + `AppendInTx` in one tx. **The fact records even when 0 rows are deleted** — current `Remove` behavior is preserved (REQ-4.4); no `RowsAffected` gate (a gate would be a behavior change).

### 1.2 New repository surface — `internal/repository/legacy_cutover.go`

```go
type LegacyCutoverAuditRepository interface {
    SetCutoverWithAudit(ctx context.Context, cutover *LegacyCutover, fact *domain.AuditFact) error
}
```

- `legacyCutoverRepo` gains the ledger via `requireAuditLedger("legacy_cutover", audit)`.
- `NewLegacyCutoverRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) LegacyCutoverRepository` — breaking constructor (call sites `app/new.go:228,411`).
- `SetCutoverWithAudit`: existing upsert (`:96-107`) + `AppendInTx` in one tx, append last, `ErrAuditDuplicate` in-tx only.

### 1.3 Service changes — `internal/service/audit_hold.go`

`Put`/`Remove` restructured to the sync two-phase shape (`service/sync_job.go:150-175` + `service/sync_audit.go:20-32` precedent), preserving every existing error code and test:

```go
// putWithAudit returns (atomicAudit, err): atomicAudit=true when the fact was
// appended in the same tx as the hold write.
func (s *auditHoldService) putWithAudit(ctx context.Context, hold *AuditHold, fact *domain.AuditFact) (bool, error) {
    if s.audit == nil {
        return false, s.holds.PutHold(ctx, hold) // 0 = disabled convention
    }
    repo, ok := s.holds.(repository.AuditHoldAuditRepository)
    if !ok {
        // Fail closed: a repo without the WithAudit surface must not silently
        // drop the fact (sync.audit_unavailable precedent).
        return false, apperr.Internal("audit_hold.audit_unavailable", "audit hold persistence is unavailable")
    }
    return true, repo.PutHoldWithAudit(ctx, hold, fact)
}
```

`Put` becomes:

1. Validate (unchanged); normalize (trim scopeID, resolve `tenant` from ctx-or-arg) — same values as today.
2. **Pre-build** the fact with the current anchored EventID and payload, byte-identically: `idgen.Anchor("audit_hold.set", tenant, string(scopeType), hold.ScopeID, reason, idgen.RenderAnchorTime(expiresAt))`, `Payload: {"reason": reason, "expires_at": expiresAt}`. (Verified: every anchor/payload input is known pre-tx; the RETURNING copy-back touches only `hold.ID/CreatedAt/ExpiresAt`, none of which feed the fact.)
3. `atomicAudit, err := s.putWithAudit(ctx, hold, fact)`; on error → `apperr.Internal("audit_hold.save_failed", ...)` (unchanged wrapper; with the WithAudit surface an atomic failure means the hold was not recorded, so `save_failed` is truthful — sync precedent wraps `CreateWithAudit` errors the same way).
4. `if s.audit != nil && !atomicAudit { s.audit.Record(...) }` → `audit_hold.audit_failed` — the legacy fallback tail, reachable only by non-audit test fakes; concrete repo always returns `atomicAudit=true`.

`Remove` mirrors this: `removeWithAudit` with `RemoveHoldWithAudit`; pre-built fact `idgen.Anchor("audit_hold.removed", tenant, string(scopeType), scopeID)`; `audit_hold.remove_failed` / `audit_hold.audit_failed` preserved.

### 1.4 Service changes — `internal/service/legacy_cutover.go`

`Set` mirrors REQ-4: `setWithAudit(ctx, entry, fact) (bool, error)`; pre-built fact `idgen.Anchor("legacy.cutover.set", tenant, string(status), string(current))`, `ActorUID: s.actor(ctx)`, `Payload: {"status", "previous", "note"}` — all pre-known (verified). Codes `cutover.save_failed` / `cutover.audit_failed` / new `cutover.audit_unavailable` preserved/added. The `Get` pre-check and `ValidCutoverTransition` ordering is unchanged — the transition validation still happens before any write (a race between `Get` and the upsert is unchanged from today).

### 1.5 Composition root

- `app/audit.go:187`: `NewAuditHoldRepository(gdb, auditLedgerRepo)` — the param type is `repository.AuditLedgerRepository` at that call site; the function signature must accept it (the concrete repo returned by `NewAuditLedgerRepository` is `FullAuditLedgerRepository`, which embeds the transactional interface — pass the already-available `ledgerRepo` value; widen the helper signature or pass the full-typed value).
- `app/new.go:228,411`: `NewLegacyCutoverRepository(gdb, auditLedgerRepo)` — `auditLedger` is a `service.AuditLedgerService` at those sites; the transactional repo value must be threaded (check which variable is in scope; `buildAuditStack`/`new.go` already holds the repo for `WithSyncJobAuditLedger` at `new.go:260`, reuse it).

No wire/proto/HTTP/SQL/migration changes. No new transport error codes — the three new internal codes (`audit_hold.audit_unavailable`, `cutover.audit_unavailable`, plus the reuse of `save_failed` for atomic failures) map to existing HTTP 500 semantics.

## 2. Compatibility constraints

1. **Wire/proto/API:** zero changes. `audit_hold.*` and `cutover.*` codes keep their transport meanings; the only new codes are internal-only `*_unavailable` (same HTTP status class as the codes they guard).
2. **Fact parity:** hold/cutover facts keep byte-identical content and anchored EventIDs when moved in-tx (verified §0). Retry-fold via `ErrAuditDuplicate` (in-tx) preserved — a crash-retry re-derives the same event_id.
3. **Semantics parity:** `Remove` records the fact even on 0 rows deleted (REQ-4.4); `Set` records only after a validated transition; `Put` upsert/RETURNING behavior unchanged.
4. **Error-code reachability:** `save_failed`/`remove_failed` remain the domain-write codes and now also wrap atomic audit failures (sync precedent); `audit_failed` remains reachable via the fallback tail (non-audit fakes). Both documented code sets stay live — no client that matches on these codes breaks.
5. **Fallback tails:** the post-commit `Record` fallback survives only for repositories without the audit surface (test fakes); concrete repos always satisfy the audit surface, so the crash window is closed in the composition root. `requireAuditLedger` panics at wiring time if the ledger is missing — a wiring regression stops the process, never silently degrades to plain writes.
6. **`0 = disabled` convention:** `s.audit == nil` still routes to the plain repo methods (unconfigured-audit unit tests keep working).
7. **File-size gate:** `repository/audit_hold.go` (126 → ~165 lines) and `repository/legacy_cutover.go` (230 → ~265 lines) stay well under 500; `service/audit_hold.go` (121 → ~150) and `service/legacy_cutover.go` (153 → ~180) too. No splits.
8. **Constructor breakage is internal-only:** `NewAuditHoldRepository`/`NewLegacyCutoverRepository` are called from `internal/app` and integration tests only (verified); no external consumer.
9. **Dead-tree swallows** (6 sites in `internal/{file,block,comment,message}`) remain out of scope; gate scans `internal/service` only.

## 3. Failure modes

| Failure | Before (current) | After |
|---|---|---|
| Crash between domain commit and audit append | Fact silently lost; `audit_holds`/`legacy_cutover` row committed without ledger evidence | Impossible: fact + pending `audit_publications` row commit with the domain row, or not at all |
| Domain write fails after audit append | Phantom durable fact claims an operation that never committed (e.g. delete failed, `audit_hold.removed` already durable) | Whole tx rolls back: 0 domain + 0 ledger + 0 publication rows |
| Ledger down / signing key unconfigured (`AppendInTx` guard `audit_ledger.go:108-109`) | Domain write committed; checked `Record` error surfaced as `audit_failed` but the mutation stood | Mutation fails atomically (`save_failed`); nothing commits; operator sees the hold/cutover never recorded — no false success |
| Duplicate retry (same anchor, e.g. re-put or re-set after crash) | `ErrAuditDuplicate → nil` in the post-commit `Record` | Same, in-tx: duplicate swallow after the domain write, no second ledger row |
| Wiring regression (repo constructed without ledger) | Silent non-audited mutation (nil ledger) | `requireAuditLedger` panic at startup (fail-closed) |
| Service-level fake without audit surface | fallback `Record` (checked) | unchanged — fallback tail preserved for fakes; `audit_hold.audit_failed` reachable |
| Future regression re-adds a publish/append bypass | — | Gate P1/P2 red; `TestAuditLedgerRecordEnqueuesOnly` red (keep-enforced REQ-1/2/3) |

## 4. Migration steps (ordered, each green before the next)

1. **Baseline proof** (all verified today): `make check-audit-swallow` exit 0; zero `_ = s.audit.*`; zero `WithAudit` in the two repo files; `make test` green.
2. **REQ-6 tests first (red):** add `internal/repository/audit_hold_integration_test.go` / `legacy_cutover_integration_test.go` cases (happy path, keyless-ledger rollback, duplicate fold — §5) and the service-level cases (fallback fires with non-audit fake; never fires with audit fake). Compile-fail red (no WithAudit symbols) → confirms the acceptance criteria bind.
3. **Repository surface (REQ-4/5):** add the two interfaces + WithAudit methods + ledger field via `requireAuditLedger`; change both constructors.
4. **Service (REQ-4/5):** restructure `Put`/`Remove`/`Set` to the `(atomicAudit, err)` shape with pre-built facts; update the two error-code doc comments (`audit_failed` now fires only on the fallback tail; `save_failed` covers atomic failures).
5. **Composition root:** `app/audit.go:187`, `app/new.go:228,411` (and any integration-test constructors) pass the ledger repo; fix helper signatures as needed.
6. **REQ-7 test:** add the counting-spy unit test in `internal/service/audit_ledger_test.go` (red before, green after — it pins an existing but unasserted path).
7. **Gate:** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build`; then `make test-integration` (repository/SQL-boundary tests changed — mandatory per AGENTS.md).

**Rollback:** revert steps 3-6 (tests + code); no data migration exists, no SQL changes, no applied-migration edits.

## 5. Testable acceptance mapping (REQ-6/7 → supplied T-5)

### Acceptance (a) — hold/cutover cases (delta; 5/7 paths already covered)

Repository integration tests (`//go:build integration`, `testpg.Start` + `testpg.Migrate`, mirroring `TestCallbackAuditHappyPath`/`TestCallbackAuditRollbackWithoutSigningKey`):

| Case | Injection | Assertions |
|---|---|---|
| Put happy (signed key) | none | 1 `audit_holds` row + exactly 1 `audit_ledger` row + 1 `pending` `audit_publications` row; `hold.ID`/`CreatedAt` copied back from RETURNING |
| Put rollback (keyless ledger — `NewAuditLedgerRepository(db, AuditLedgerConfig{})` → `AppendInTx` guard fails) | keyless ledger | `PutHoldWithAudit` returns error; `count(audit_holds)=0`, `count(audit_ledger)=0`, `count(audit_publications)=0` |
| Put duplicate fold | re-execute same anchored fact (second `PutHoldWithAudit` with identical inputs) | succeeds; still exactly 1 ledger + 1 publication row; 1 `audit_holds` row |
| Remove happy | pre-insert hold, then `RemoveHoldWithAudit` | 0 `audit_holds` rows + 1 ledger row (`audit_hold.removed`) + 1 pending publication |
| Remove rollback (keyless) | keyless ledger + pre-existing hold | error; pre-existing hold row byte-identical (same `expires_at`, `reason`, `created_by`); 0 ledger + 0 publication rows |
| Set happy | none | 1 `legacy_cutover` row + 1 ledger row + 1 pending publication |
| Set rollback (keyless) | keyless ledger + pre-existing `cut_over` row | error; pre-existing row byte-identical; 0 ledger + 0 publication rows |
| Set duplicate fold | re-execute same anchored fact (same `status`, same `current`) | succeeds; 1 ledger + 1 publication row |

Row-count helper: same shape as `countCallbackAuditRows` — count by `(tenant_id, source, event_id)` for the ledger and join on `audit_publications.fact_id` (or count `pending` rows for the event).

### Acceptance (a) — service level

- `service/audit_hold_test.go` / `legacy_cutover_test.go`: fake repo **without** the audit surface + failing `auditLedgerRepoSpy` (`appendErr`) → `Put`/`Remove`/`Set` return `audit_hold.audit_failed` / `cutover.audit_failed` (existing `TestAuditHoldAuditFailureFailsClosed` / `TestCutoverAuditFailureFailsClosed` keep passing unchanged — the fallback tail keeps them green).
- New: fake repo **with** the audit surface (recording spy) + failing ledger append inside the WithAudit method → `save_failed`; the recording spy proves the fallback `Record` was never invoked (zero calls).

### Acceptance (b) — unchanged (keep-enforced): `make check-audit-swallow` exit 0, 0 matches; negative control red/green.

### Acceptance (c) — unchanged (keep-enforced): grep invariants — only `callback_audit.go:114` calls `AuditPublisher.Publish`; only `callback.go:281` calls `drainPublications`; `enqueueFact` only from `publishAuditFact`; `audit_ledger.go` contains no publish symbol.

### Acceptance (d) — REQ-7 (delta)

New unit test in `internal/service/audit_ledger_test.go`:

- Counting metrics spy (`ingestFailures []string` recording `ObserveAuditIngestFailure(source)`; the existing `metricSpy` at `callback_audit_test.go:79` is a no-op for this method — add a dedicated spy or extend it) + `auditLedgerRepoSpy{appendErr: errors.New("ledger down")}` → `Record` returns the error **and** the counter fired exactly once with the trimmed fact source.
- `appendErr = repository.ErrAuditDuplicate` → `Record` returns nil **and** counter not incremented (duplicate fold is not an ingest failure).
- Pins the read-path sites `account.go:154-161,181-190` which surface `audit.ingest_failed` and rely on this counter.

## 6. Explicitly out of scope

- Dead-tree swallows (`internal/{file,block,comment,message}` — unwired).
- The `account.go` read-path `Record` sites (correctly out-of-tx; covered by REQ-7 counter only).
- SSE/event `.Publish` sites (different interfaces).
- Any SQL/migration/proto change; any change to `Record`, `drainPublications`, or the wiring of publishers.
- The `Remove` 0-rows fact (preserved by design, REQ-4.4).

## 7. Implementation and hardening verification (2026-08-07, head 13e4676 + working tree)

Implemented as designed (§1.1–§1.5) with one deviation and one extension, then verified
against the six hardening properties of the fail-closed contract.

**Deviation from §1.3 (resolved design contradiction):** the `!ok` branch (repository without
the WithAudit surface) does NOT return `*_audit_unavailable` — it falls through to the plain
write + post-commit `Record` tail. The two design statements were contradictory: §1.3 step 3
fail-closed with `audit_unavailable` vs. the acceptance mapping that pins
`TestAuditHoldAuditFailureFailsClosed`/`TestCutoverAuditFailureFailsClosed` to `audit_failed`
via the fallback tail. The acceptance mapping wins (the user requirement: fallback tail
preserves `audit_failed` reachability without swallowing facts). The `*_audit_unavailable`
codes were therefore NOT added — an unreachable code would be worse than absent. The sync
`createWithAudit` precedent's fail-closed `!ok` branch is not mirrored because sync has no
pinned fallback-tail tests; hold/cutover do.

**Extension beyond the design:** hold/cutover services gained `AuditLoggerWired()` and are
asserted in the composition root via `assertComplianceAuditWiring` (app/audit.go). This closes
the one remaining production path where a mutation could commit without a fact — a future
composition root forgetting `auditLedger` in `NewAuditHoldService`/`NewLegacyCutoverService`
would have silently hit the `s.audit == nil` skip guard (the repo `requireAuditLedger` panic
only covers the repo-side ledger). The facade cutover instance (account handler) is now built
by `buildAuditComplianceServices` and asserted too; `new.go` stayed under the 500-line gate
(498).

### Property-by-property confirmation

| Property | Mechanism | Evidence |
|---|---|---|
| No path commits without a durable fact | In-tx append last (append failure rolls the domain write back); production composition root wires the concrete repos (always WithAudit) + asserts the services' audit service non-nil; repo constructors panic on nil/typed-nil ledger | `TestAuditHoldPutWithAuditHappyPath` (1 hold + 1 ledger + 1 publication in one tx); `assertComplianceAuditWiring` in app/audit.go + app/new.go; `requireAuditLedger` in both constructors; zero stale single-arg constructor calls (grep) |
| Keyless ledger fails the mutation atomically | `AppendInTx` signing-key guard runs inside the same tx; error (not `ErrAuditDuplicate`) aborts the tx | `TestAuditHoldPutWithAuditRollbackWithoutSigningKey`, `TestAuditHoldRemoveWithAuditRollbackWithoutSigningKey`, `TestLegacyCutoverSetWithAuditRollbackWithoutSigningKey` — all assert 0 hold/cutover rows + 0 ledger + 0 publication rows, and byte-identical pre-state (expires_at/reason/created_by; status/decided_by/note) |
| Typed-nil panic at startup | `requireAuditLedger` reflect check for every nil-able kind; both new constructors enumerated in the wiring tests | `TestConstructorsRejectNilAudit`/`TestConstructorsRejectTypedNilAudit` now include `NewAuditHoldRepository` and `NewLegacyCutoverRepository` (nil + typed-nil pointer) |
| `save_failed`-vs-`audit_failed` not misread as fail-open | With the WithAudit surface, an append failure is an ATOMIC failure — `save_failed`/`remove_failed`/`cutover.save_failed` mean nothing committed; the fallback `Record` is provably never invoked on that path; doc comments on both services state the mapping | `TestAuditHoldAtomicAuditFailureFailsClosed` (`audit_hold.save_failed` + `ledger.appended == nil`), `TestCutoverAtomicAuditFailureFailsClosed` (`cutover.save_failed` + no fallback) |
| Fallback tail preserves `audit_failed` reachability without swallowing facts | Non-WithAudit repos (test fakes only) take plain write + checked `Record`; Record error surfaces as `audit_failed` (never `_ =`); on success the fact IS appended | Pinned `TestAuditHoldAuditFailureFailsClosed`/`TestCutoverAuditFailureFailsClosed` unchanged and green; `TestAuditHoldAtomicFactMatchesFallbackRecord`/`TestCutoverAtomicFactMatchesFallbackRecord` assert the fallback path appends (`fallbackLedger.appended != nil`) |
| Byte-identical, redaction-safe facts | Both paths build the fact via the SAME `buildAuditFact(ctx, input)` pipeline (bind → normalize → redact); anchor inputs identical to the pre-change post-commit Record (verified field-by-field; golden anchor tests unchanged) | `TestAuditHoldAtomicFactMatchesFallbackRecord`/`TestCutoverAtomicFactMatchesFallbackRecord` deep-compare EventID/TenantID/ActorUID/ResourceID/PartitionKey/Action/Payload across the atomic and fallback paths; golden tests G9/G9b/G10/G11/G12/G13 all green unchanged; `TestAuditHoldAtomicFactRedactionSafe` pins the redaction pipeline in the atomic path |

**Additional verification:** REQ-7 counter pinned — `TestRecordObservesAuditIngestFailure`
(counting spy: exactly one increment with the trimmed source on failure; zero on
`ErrAuditDuplicate` fold). `RemoveHoldWithAudit` preserves the 0-rows fact (REQ-4.4).
`check-audit-swallow` and `check-anchored-event-ids` stay green; full done-gate
(`make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow
&& make check-anchored-event-ids && make build`) and `make test-integration` (all packages)
pass at head 13e4676 + working tree.

## 8. Risks / open decisions

1. **`save_failed` for atomic audit failures** (decision, sync precedent): a keyless ledger makes `Put` fail with `audit_hold.save_failed` instead of `audit_hold.audit_failed`. Rationale: the mutation did not commit, so "failed to record the legal hold" is accurate; `audit_failed` remains reachable via the fallback tail and keeps its documented meaning ("fact append failed after a committed domain write"). If reviewers prefer `audit_failed` for atomic failures, classify by `errors.Is` on the append error — rejected because the keyless guard error is not a sentinel and classification would be brittle.
2. **Constructor signature break** is contained to `internal/app` + tests (verified no other callers); the app/audit.go helper `buildAuditComplianceServices` currently receives `ledgerRepo repository.AuditLedgerRepository` — the concrete value satisfies `FullAuditLedgerRepository`, so pass the concrete-typed variable or widen the parameter type.
3. **Integration test count cost:** 8 new repository test cases need Docker (`make test-integration`); acceptable per the gate (repository/SQL work).
