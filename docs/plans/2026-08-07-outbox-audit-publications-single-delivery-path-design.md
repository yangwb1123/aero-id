# Design — Single audit delivery path: `drainPublications` as the sole publisher, in-tx appends, no-swallow gate

| | |
|---|---|
| Status | Design (implementation already landed in uncommitted working tree on top of `13e4676`) |
| Spec | `docs/requirements/2026-08-06-internal-outbox-audit-publications-single-delivery-path.md` |
| Contract | `docs/proposals/audit-contract-batch-aero-id.md` (§B2-1 in-tx audit, §B2-2 single delivery; gate G3) |
| Scope | `internal/service/{audit_ledger,callback_audit,callback,account,audit_hold,legacy_cutover,account_provision}.go`, `internal/repository/{audit_ledger,audit_publication,operation,sync_job,event_receipt,callback,account_provision_audit,account_profile,audit_hold_audit,legacy_cutover_audit}.go`, `internal/app/{new,audit}.go`, `Makefile`, `scripts/check-audit-swallow.sh` |
| Out of scope | 10 `_ = s.audit.Log` sites and the `Log` gate hole (sibling spec `2026-08-06-cmd-server-complete-in-tx-audit-writes.md`); T-4 anchored event IDs (sibling `2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop.md`); dead-letter replay tooling; claim/lease machinery internals (`2026-08-06-internal-outbox-claim-based-dispatch.md`) |

## 0. Verification verdict on the supplied evidence

All evidence claims were re-derived from the repository, not taken on trust. HEAD is `13e4676`; the working
tree is 69 files, +3762/−371. Every claim checks out except the two already flagged by the evidence itself:

| Evidence claim | Verdict | How verified |
|---|---|---|
| Inline `_ = publisher.Publish` loop in `Record` at HEAD `audit_ledger.go:76-80`; removed in working tree | ✅ | `git show HEAD:internal/service/audit_ledger.go` lines 76–80 (`for _, publisher := range s.publishers { if publisher != nil { _ = publisher.Publish(ctx, fact) } }`); working tree: no `publishers` field, `NewAuditLedgerService(repo)` single-arg, doc comment "Record only appends, it never publishes"; `grep -rn "_ = publisher.Publish" internal/` → 0 (only checked `if err := publisher.Publish` at `callback_audit.go:114` drain + `projection_replication.go:82` SSE envelope path) |
| Wiring: `new.go:147` no-publisher ctor; `wireAuditServices` fail-closed panics at `app/audit.go:65-75` | ✅ | HEAD `new.go:147` `service.NewAuditLedgerService(auditLedgerRepo)`; working tree `app/audit.go` `buildAuditStack` + `wireAuditServices` with `panic(...)` on missing `SetAuditPublicationRepository`/`SetAuditPublishers`/`AuditPublicationRepository`; `new.go:134` calls `buildAuditStack` |
| `drainPublications` (`callback_audit.go:47`) + ticker (`callback.go:267`) sole call site | ✅ | HEAD `callback_audit.go:47`, `callback.go:261` ticker / `:267` call; working tree `:38` / `:275` / `:281`; `grep -rn drainPublications` → exactly one non-test call site (the ticker) |
| `AppendInTx` :123, `checkAuditDuplicate` :172, `createAuditRows` :206 (mints pending row) | ✅ (line drift only) | HEAD `:123/:174/:189`; working tree `:106/:157/:172`; `createAuditRows` inserts ledger row then `auditPublicationRow{ID: idgen.NewID(), FactID: fact.ID, Status: "pending", NextAttemptAt: now, ...}` in the same tx; constraint-precise 23505→`ErrAuditDuplicate` mapping keyed on `uq_audit_ledger_tenant_event` |
| 8 swallowed sites, all exact at HEAD, all converted in working tree | ✅ | All 8 `_ = s.audit.Record(` sites at the cited lines at HEAD; working tree: 0 `_ = s.audit.` sites in `internal/service`; conversions to `registerWithAudit`/`DeleteSubscriptionWithAudit`/`ResetDeliveryWithAudit`/`putWithAudit`/`removeWithAudit`/`setWithAudit`/`provisionAccountWithAudit` (in-tx) and checked `apperr.Unavailable("audit.ingest_failed")` at `account.go:158,188` (read path) |
| "27 `_ =` sites"; "no check-audit-swallow target" | ⚠️ counts wrong, gate claim superseded | Measured 19 live (`internal/service`) + 6 dead-tree (`internal/file` 2, `internal/block` 2, `internal/comment` 1, `internal/message` 1) = 25 at HEAD; gate **exists** in working tree: `Makefile:52-54` → `scripts/check-audit-swallow.sh internal/service`, P1+P2 patterns, `scripts/testdata/audit-swallow/{positive,negative}` fixtures, wired into done-gate `AGENTS.md:52`; runs green ("self-test ok", 0 matches) |
| REQ-3.2 delta: gate scans `internal/service` only; `cmd/`+`internal/app` are 0-site | ✅ | `grep` of the P1/P2 shape over `cmd/` + `internal/app` (non-test) → 0 lines; Makefile passes only `internal/service` — the extension is safe and is the only open code delta |
| Acceptance tests landed | ✅ | All named tests exist at cited lines: `TestAuditLedgerRecordEnqueuesOnly` (audit_ledger_test.go:48), `TestRecordObservesAuditIngestFailure` (:173), `TestAuditHoldAuditFailureFailsClosed` (audit_hold_test.go:235), `TestCutoverAuditFailureFailsClosed` (legacy_cutover_test.go:233), `TestAuditWiringRollbackAtomicity` (audit_wiring_integration_test.go:175), `TestAuditWiringHappyPath` (:60, asserts exactly 1 publication row per fact), `TestFirstFactDrainDeliversExactlyOnce` (callback_integration_test.go:378, step (1) asserts `len(publisher.facts) == 0` before first drain — the "no inline loop" guard), `TestAuditPublicationDuplicateConflictReceiptSettlesCompleted` (:83). Unit tests pass; gate green |
| Migration for `audit_publications` | ✅ (supplementary) | `migrations/000045_audit_publications.{up,down}.sql` exists; `audit_publications` column set: `id, fact_id, status, attempt, lease_owner, lease_until, ...` |

Conclusion: the evidence is trustworthy; the spec is an accurate contract-to-keep plus one open delta
(REQ-3.2 gate scope) and one required step (commit + full done-gate). The design below is the engineering
record of what the landed change *is*, so it can be reviewed, rolled back, and extended.

## 1. Target architecture

```text
business tx (service → repo XxxWithAudit)
   │   one gorm tx { domain write + r.audit.AppendInTx(ctx, tx, fact) }        (atomic; §3)
   ▼
audit_ledger row + pending audit_publications row commit together               (createAuditRows)
   ▼
callbackService.Start ticker → drainPublications            ── SOLE delivery path (REQ-1/REQ-6)
   │   ClaimPublication(worker, 5m lease, batch 50) → publishAuditFact → FinishPublication
   │     ├─ enqueueFact: durable dedup receipt delivery row (callback_deliveries)
   │     └─ governance publisher(s): tenant-bound check, checked error propagation
   ▼
completed | pending(backoff) | dead_letter                                 (publicationOutcome)
   ▼
metrics: ObserveAuditPublicationOutcome / FinishFailure / DrainSkipped / ClaimFailed; ObserveAuditIngestFailure
```

Design invariants:

1. **One writer, one delivery path.** `Record` appends exactly one ledger row and never publishes.
   Delivery is the drain worker's job, keyed on the durable `audit_publications` row. The invariant is
   compile-time (`NewAuditLedgerService(repo)` has no publisher parameter) *and* gate-time
   (`check-audit-swallow` P1/P2).
2. **Atomicity of fact + mutation.** Every in-tx site appends inside the business transaction. A non-duplicate
   append failure rolls the whole transaction back; a duplicate append (Class B, in-tx) folds and the domain
   write commits idempotently.
3. **Never silent.** Every site either fails the operation (`audit.ingest_failed` / `*.audit_failed`) or
   observes the failure on a metric. There is no path where the business operation succeeds and an audit fact
   is lost unobserved.
4. **Fail closed at composition.** A wiring regression (no publication repo, no publishers) panics at startup
   rather than pending facts silently; the runtime `ObserveAuditPublicationDrainSkipped` counter is a
   belt-and-braces signal if the guard is ever bypassed.
5. **At-least-once delivery with lease-guarded settlement.** Duplicate/out-of-order drains are normal; the
   dedup receipt row and the lease-guarded `FinishPublication` UPDATE make double-enqueue and double-finish
   impossible. A duplicate governance publish after re-claim is legal by design (at-least-once); the dedup
   receipt keeps exactly one delivery row per event.

## 2. API changes

### 2.1 Service layer (`internal/service`)

| Symbol | Before (HEAD) | After (working tree) | Compatibility |
|---|---|---|---|
| `NewAuditLedgerService(repo, publishers ...AuditPublisher)` | variadic publishers | `NewAuditLedgerService(repo)` — single arg | Breaking for any caller passing publishers; the only callers are the composition root (`new.go` → `app/audit.go`) and tests. Compile-time single-path invariant. |
| `auditLedgerService.publishers` field | present, used by dead loop | removed | Removed with the loop. |
| `Record(ctx, input)` | append + dead publish loop | append-only; duplicate → `nil`; other error → `ObserveAuditIngestFailure` + error | Caller signature unchanged; behavior change: no longer delivers. |
| `CallbackService` | — | + `SetAuditPublicationRepository(repo)` and `SetAuditPublishers(...AuditPublisher)` (interface assertions in `wireAuditServices`) | Additive; consumed only by `wireAuditServices`. |
| `drainPublications(ctx)` | exists | exists; sole call site = worker ticker (`callback.go:281`); `nil` publications → `ObserveAuditPublicationDrainSkipped` | Unchanged externally. |
| New private helpers | — | `registerWithAudit`, `putWithAudit`/`removeWithAudit`, `setWithAudit`, `provisionAccountWithAudit`, `finishPublicationAttempt`, `publicationOutcome` | Private. |

Behavior of the converted 8 sites (REQ-2):

| Site | Append location | Failure behavior |
|---|---|---|
| `account.viewed` / `account.listed` (`account.go:155,184` HEAD) | out-of-tx (sensitive read; no business tx exists) | checked `if err :=` → `apperr.Unavailable("audit.ingest_failed", ...)` — read fails closed |
| `callback.registered` (`callback.go:160`) | in-tx `CreateSubscriptionWithAudit` | append error rolls back subscription creation |
| `callback.deleted` (`:221`) | in-tx `DeleteSubscriptionWithAudit` | rollback |
| `callback.delivery_reset` (`:247`) | in-tx `ResetDeliveryWithAudit` | rollback |
| `audit_hold.put/remove` (`audit_hold.go:59,74`) | in-tx `PutHoldWithAudit`/`RemoveHoldWithAudit`; checked `Record` fallback tail only for repos without the WithAudit surface | atomic failure when WithAudit surface present |
| `legacy_cutover.set` (`legacy_cutover.go:88`) | in-tx `SetCutoverWithAudit`; checked fallback tail | atomic failure |
| `account.provisioned` (`account_provision.go:168`) | in-tx `EnsureByIdentityWithAudit`/`EnsureByIdentityWithQuotaWithAudit` | rollback |

### 2.2 Repository layer (`internal/repository`)

| Symbol | Change |
|---|---|
| `AppendInTx(ctx, tx, fact)` (`audit_ledger.go:106`) | New; same-tx ledger INSERT + pending `audit_publications` INSERT via `createAuditRows` (:172). `checkAuditDuplicate` (:157) is a pre-check under the partition advisory lock; the unique constraint `uq_audit_ledger_tenant_event` is the final guard, mapped constraint-precisely: only that constraint's 23505 becomes `ErrAuditDuplicate`; any other 23505 stays raw so the tx rolls back (silent-drop invariant). |
| `AuditPublicationRepository` (`audit_publication.go`, new file) | `ClaimPublication(ctx, worker, lease, limit)` (lease-guarded claim, `delivering` status), `FinishPublication(ctx, id, worker, status, code, next)` (lease-guarded UPDATE, statuses `completed`/`pending`/`dead_letter`), plus `SetAuditPublicationRepository` setter on the callback service. |
| `XxxWithAudit` family | `CreateSubscriptionWithAudit`, `DeleteSubscriptionWithAudit`, `ResetDeliveryWithAudit` (callback); `PutHoldWithAudit`, `RemoveHoldWithAudit` (audit_hold_audit.go); `SetCutoverWithAudit` (legacy_cutover_audit.go); `EnsureByIdentityWithAudit`, `EnsureByIdentityWithQuotaWithAudit` (account_provision_audit.go); `UpdateProfileWithAudit` (account_profile.go) — all take a pre-built `*domain.AuditFact` and run `AppendInTx` inside the domain-write tx. Precedents: `operation.go:135`, `sync_job.go:86`, `event_receipt.go:89`. |
| `auditPublicationRow` | New GORM row type (`audit_publication.go:14`): `id, fact_id, status, attempt, lease_owner, lease_until, ...`. |

### 2.3 Composition root (`internal/app`)

- `new.go:134` → `buildAuditStack(cfg, gdb, controlMetrics)` replaces the inline ledger/publisher wiring; returns `(CallbackService, AuditLedgerService, FullAuditLedgerRepository, error)`.
- `wireAuditServices` (app/audit.go:65-75): interface-asserts the callback service accepts the publication repo + publishers and the ledger repo implements `AuditPublicationRepository`; **panics** otherwise. Unreachable with shipped types; insurance against future regressions.

### 2.4 Tooling and observability

- `make check-audit-swallow` (Makefile:52-54) → `scripts/check-audit-swallow.sh [ROOT]`:
  - P1: `_ = <expr>.(Record|Log|Publish|FinishPublication|ClaimPublication)` and `_, _ =` form;
  - P2: bare-call `Record|Log|FinishPublication|ClaimPublication` (not `Publish` — the best-effort SSE hub `d.pusher.Publish` at `notification.go:281,294` is a documented exclusion; AGENTS.md);
  - documented exclusions: unwired parallel tree `internal/{file,message,comment,block,notification,search,audit}`; fixtures under `scripts/testdata/audit-swallow/` are never scanned;
  - self-test first (positive must match, negative must not), then live scan; green only at 0;
  - wired into done-gate `AGENTS.md:52`.
- Metrics added on `ControlPlaneMetrics`: `ObserveAuditIngestFailure`, `ObserveAuditPublicationOutcome(status)`, `ObserveAuditPublicationFinishFailure(status, err)`, `ObserveAuditPublicationDrainSkipped`, `ObserveAuditPublicationClaimFailed(err)`.

### 2.5 Schema

- `migrations/000045_audit_publications.{up,down}.sql`: new `audit_publications` table (FK to `audit_ledger.id` via `fact_id`; `uq_audit_publication_fact UNIQUE (fact_id)`; status/attempt/lease columns). No change to `audit_ledger` columns; no backfill needed. Note: 000045 is **already committed at HEAD** (shipped atomically with `audit_ledger` 000034, the minting code, and `drainPublications` in `aafd7df`), so it is not part of the 69-file working-tree commit; see §5. Claim-scan indexes (`idx_audit_publications_due` on `(status, next_attempt_at, id)`, `idx_audit_publications_lease` on `(status, lease_until, next_attempt_at)`) exactly match the two OR branches of the `ClaimPublication` WHERE and the `ORDER BY`; terminal statuses never appear in any scan so the indexes never carry completed/dead-letter rows.

## 3. Compatibility constraints

1. **No transport/API surface change.** HTTP and gRPC handlers, request/response schemas, and error codes are untouched. The only externally visible behavior change is *more* correctness: failures that used to be swallowed now fail operations or are metered.
2. **`Record` callers keep their call shape.** The `Record(ctx, AuditFactInput)` signature is unchanged; only the constructor changed (internal callers only).
3. **`ErrAuditDuplicate` semantics are context-dependent and must not be blurred.** Out-of-tx `Record` maps duplicate → `nil` (idempotent append). In-tx (Class B) duplicate folds and the domain write commits. Every *other* append error — including a 23505 on `uq_audit_ledger_chain_seq` or a publication PK — must roll back the transaction. The constraint-precise mapping in `createAuditRows` is load-bearing; do not "simplify" it to a broad `gorm.ErrDuplicatedKey` check.
4. **The drain worker's ordering with callback delivery.** Audit publications share the callback service's worker identity and bulkhead; `FinishPublication` is lease-guarded so a slow governance endpoint cannot double-settle. New drain work must not bypass the bulkhead (P6-06) or the claim lease.
5. **Dead-tree carve-outs do not migrate.** The gate excludes the unwired tree and the SSE `Publish`; the fail-open middleware carve-out (Ban etc.) never applies to audit writes. Any future move of dead-tree code into the live layer must convert its `_ = s.audit.Log` sites first (sibling spec owns the 10 live `Log` sites).
6. **Compatibility with the sibling specs.** The gate is shared (`Log` is a first-class P1+P2 citizen — no exclusion); the hold/cutover repo APIs are shared with `2026-08-06-cmd-server-complete-in-tx-audit-writes.md`; T-4 anchored `event_id` derivation is owned elsewhere and must not be re-implemented here.
7. **Publishers stay flag-gated and tenant-bound.** Governance publishers remain behind `buildAuditPublishers` config; `tenantBoundAuditPublisher.CanPublishTenant` failure → `errAuditTenantUnbound` → `pending` + `audit.binding_unavailable` (a bounded-stale condition, not a swallow).

## 4. Failure modes and handling

| # | Failure | Detection | Handling | Observable |
|---|---|---|---|---|
| FM-1 | Governance publisher returns error during drain | `publishAuditFact` returns error | `publicationOutcome`: retry with backoff (`pending`, `audit.publication_failed`) until `MaxAttempts`, then `dead_letter` (`audit.publication_failed`); row stays `delivering` between attempts | `ObserveAuditPublicationOutcome` on committed settlement |
| FM-2 | Lease lost (worker crash, DB failover, WAN partition) | `ErrAuditPublicationLeaseLost` — **from `FinishPublication` only**: the `(id, status='delivering', lease_owner)` guard UPDATE matches zero rows when another worker re-claimed. The claim never returns it; a stale finish hits the guard | Row re-claimed after lease expiry; the lease-guarded UPDATE prevents double-finish; outcome decision is re-made on the re-claim | finish-failure metric on the lost update |
| FM-3 | DB error on `FinishPublication` | error return | **No retry and no swallow**: row stays `delivering`, lease expires, decision re-made after re-claim; settlement not counted until committed (avoids counting phantom outcomes) | `ObserveAuditPublicationFinishFailure(status, err)` |
| FM-4 | Duplicate append racing on another partition (advisory lock split) | raw 23505 on `uq_audit_ledger_tenant_event` | Constraint-precise mapping → `ErrAuditDuplicate` → fold per context (out-of-tx nil; in-tx commit with fold). **The fold path skips the publication-row insert**: both fold exits (pre-check and the constraint-mapped ledger-INSERT error) return before `createAuditRows` reaches `tx.Create(publication)`. The fold only fires after the winner's ledger row is visible (READ COMMITTED), so the winner's committed publication row already owns delivery of the folded fact; `uq_audit_publication_fact UNIQUE (fact_id)` DB-enforces at-most-one row per fact. A publication-site 23505/23503 is unreachable from app code (a publication row for `fact_id` implies the ledger PK insert would have collided first) and stays raw → rollback — the correct fail-closed outcome, never a fold | none (by contract) |
| FM-5 | Non-duplicate in-tx append failure (PK collision, chain-seq violation, publication FK) | raw error from `createAuditRows` | Whole business tx rolls back; operation returns the translated `*.audit_failed`/`audit.ingest_failed` | `ObserveAuditIngestFailure` on the standalone path |
| FM-6 | Read-path audit failure (`account.viewed`/`listed`) | `Record` error | Checked: `apperr.Unavailable("audit.ingest_failed")` — the read fails closed | HTTP 503, error propagated |
| FM-7 | Unwired outbox (wiring guard bypassed) | `s.publications == nil` in drain | `ObserveAuditPublicationDrainSkipped` counter; facts pend with no delivery attempt | metric; plus startup panic guard (FM-8) |
| FM-8 | Wiring regression at composition | `wireAuditServices` interface assertion fails | **Panic at startup** — never pend facts silently | process exit |
| FM-9 | Governance endpoint slow / unavailable | bulkhead acquire blocks | In-flight publishes bounded (P6-06); backpressure stalls the drain tick, not the request path; claim lease bounds staleness | bulkhead metrics |
| FM-10 | Tenant unbound for a publisher | `CanPublishTenant` false | `pending` + `audit.binding_unavailable`, retried with backoff — a binding problem is not a publisher outage | outcome metric |
| FM-11 | Poison/malformed fact in outbox (delivery stage) | delivery error from `publishAuditFact` | Retry budget → dead-letter; no acknowledgement-as-success; replay is sibling-scoped (out of scope here) | `dead_letter` outcome |
| FM-12 | Claim-stage failure (poison row / schema mismatch / DB outage inside `ClaimPublication`) | `ClaimPublication` errors | Whole claim transaction rolls back atomically — no partial claim, no row stranded `delivering`; facts pend with no attempt and the next tick retries the claim (not bounded by `MaxAttempts` — this is the claim path itself being broken, not a per-row delivery outcome) | `ObserveAuditPublicationClaimFailed(err)` — non-zero rate is an alert |

Timeout is never treated as proof of remote failure: an unknown publish outcome leaves the row `delivering`
and re-claims it after lease expiry (durable retry, no double delivery thanks to the dedup receipt row
written by `enqueueFact` before the publisher call).

## 5. Migration steps

**Step 0 — verify state.** Confirm HEAD `13e4676`, working tree 69 files; run the gate
(`make check-audit-swallow` → green). (Done in §0.)

**Step 1 — land the working tree as one commit.** The 69-file change is the unit; splitting it would break
the compile-time single-path invariant mid-history. Commit message should cite the spec and the done-gate.

**Step 2 — close the only open delta (REQ-3.2).** Extend the Makefile invocation from
`scripts/check-audit-swallow.sh internal/service` to `scripts/check-audit-swallow.sh internal/service internal/app cmd`
(or make the script loop over roots). Verified safe: `cmd/` and `internal/app` contain 0 matching lines today.
Add a `cmd/` fixture to the self-test if the script's root handling changes shape.

**Step 3 — done-gate.** `make test && make lint && make lint-strict && make check-file-sizes &&
make check-audit-swallow && make check-anchored-event-ids && make build`. Then
`make test-integration` (Postgres+MinIO) — required because repository SQL, migrations, storage, connectors,
event ingestion, callback delivery, and multi-service tx boundaries changed.

**Step 4 — deploy order.** (a) The migrate-before-binary rule is **inherited, not introduced, by this
change**: `000045` shipped at HEAD in `aafd7df` together with `audit_ledger` (000034), the in-tx
publication-row minting, and `drainPublications`. Every environment that runs HEAD already has the
table; no shipped binary ever writes ledger rows without publication rows (pre-`aafd7df` binaries do
not write ledger rows at all). The only residual hazard is a fresh environment starting a binary ≥
`aafd7df` before migrations run: every in-tx append fails 42P01 (fail-closed — writes fail loudly;
note the drain's claim error was previously silent and is now observed via
`ObserveAuditPublicationClaimFailed`, so monitor that counter alongside business-write failures). Apply
migrations before first binary start there; the working-tree change adds no new ordering
requirement. (b) Deploy
the binary. (c) Monitor `ObserveAuditPublicationOutcome` for `dead_letter` growth and
`ObserveAuditPublicationDrainSkipped` + `ObserveAuditPublicationClaimFailed` for zero (both must stay
0 in production).

**Step 5 — rollback.** Revert the working-tree commit only (code → HEAD). `000045` is already committed at
HEAD, so the table stays; delivery continues exactly as at HEAD (in-tx ledger+publication minting,
drain-driven delivery — the HEAD inline `_ = publisher.Publish` loop is dead code, zero publishers
wired). No data migration in either direction. **Do not run the `000045` down-migration as part of this
rollback**: HEAD's binary still inserts the publication row in every append, so dropping the table makes
every in-tx append fail 42P01 and every audit-write business operation fail closed (availability
outage). The down-migration is reserved for a full reversion to a pre-`aafd7df` codebase (which writes no
ledger rows at all) and must be documented as unsafe with any binary ≥ `aafd7df` live.

**Step 6 — coordination.** Notify the sibling spec owners (10 `Log` sites; T-4 anchoring) that the gate and
the hold/cutover repo APIs are shared and landed; the REQ-3.2 extension must not add a `Log` exclusion.

## 6. Testable acceptance mapping

| Requirement | Testable check | Command / artifact |
|---|---|---|
| REQ-1 single-path invariant (compile-time) | `NewAuditLedgerService` has exactly one arg; no `publishers` in `audit_ledger.go` | `grep -n "publishers" internal/service/audit_ledger.go` → 0; `grep -rn "_ = publisher.Publish" internal/` → 0 |
| REQ-1 (runtime) | `TestAuditLedgerRecordEnqueuesOnly` (audit_ledger_test.go:48): Record appends exactly one row, nothing else; `TestFirstFactDrainDeliversExactlyOnce` step (1): 1 ledger row, 1 pending publication row, 0 delivery rows, `len(publisher.facts) == 0` before first drain | `go test ./internal/service/ -run TestAuditLedgerRecordEnqueuesOnly -count=1` |
| REQ-2 eight sites converted | 0 `_ = s.audit.` lines in `internal/service` (non-test); every in-tx site uses a `XxxWithAudit` repo call; read path uses checked `audit.ingest_failed` | `make check-audit-swallow`; `grep -rnE '^\s*_ = s\.audit\.' internal/service --include='*.go' \| grep -v _test` → 0 |
| REQ-3.1 gate exists and is green | Makefile target, P1+P2, fixtures self-test, 0 live matches | `make check-audit-swallow` → "self-test ok", exit 0 |
| REQ-3.2 gate scope (open delta) | scan covers `internal/service`, `internal/app`, `cmd` | grep over the three roots → 0 lines (after Makefile extension) |
| REQ-3.3 gate self-tests | `scripts/testdata/audit-swallow/positive` must match, `negative` must not | run inside the script; also `bash scripts/check-audit-swallow.sh scripts/testdata/audit-swallow/positive` → nonzero, `.../negative` → zero matches |
| REQ-3.4 fail-closed, no carve-outs | no `Log(` exclusion in the script; done-gate includes the target | read `scripts/check-audit-swallow.sh`; `AGENTS.md:52` |
| REQ-4 never-silent (metric arm) | `TestRecordObservesAuditIngestFailure` (audit_ledger_test.go:173) | `go test ./internal/service/ -run TestRecordObservesAuditIngestFailure -count=1` |
| REQ-4 never-silent (operation-fails arm) | `TestAuditHoldAuditFailureFailsClosed` (audit_hold_test.go:235), `TestCutoverAuditFailureFailsClosed` (legacy_cutover_test.go:233) | `go test ./internal/service/ -run 'TestAuditHoldAuditFailureFailsClosed\|TestCutoverAuditFailureFailsClosed' -count=1` |
| REQ-4 atomic rollback | `TestAuditWiringRollbackAtomicity` (audit_wiring_integration_test.go:175): duplicate e1 → `ErrAuditDuplicate`, profile keeps first values, ledger keeps exactly one row; `TestAuditWiringPublicationInsertRollsBackLedgerRow` (:294): injected publication failure rolls back the ledger row | `make test-integration` (Postgres) |
| REQ-5 exactly-one publication row | `TestAuditWiringHappyPath` (audit_wiring_integration_test.go:60): 1 ledger row + exactly 1 publication row (`publication rows for e1 = 1`) | `make test-integration` |
| REQ-5 exactly-once delivery + receipt | `TestFirstFactDrainDeliversExactlyOnce` (callback_integration_test.go:378): drain → `completed`, 1 publisher fact with matching `event_id`/`tenant_id`/`action`, 1 dedup receipt delivery row, second drain no-op; `TestAuditPublicationDuplicateConflictReceiptSettlesCompleted` (:83): httptest governance endpoint, 202 receipt echoing `event_id` → `completed`; conflict receipt → idempotent success | `make test-integration` |
| REQ-4/FM-3 not-counted semantics, integration level | `TestFinishPublicationRawDBErrorNotCountedUntilCommitted` (callback_integration_test.go): injected trigger fails the finish UPDATE with a raw DB error → finish-failure metric observed with the decided status, **zero** outcomes counted, row stays `delivering`/leased; after trigger removal + lease expiry the re-claim settles `completed` and the outcome is counted exactly once; exactly one publish per attempt (no inline retry), one dedup receipt row; `TestFinishPublicationRawDBErrorReturned` (audit_consolidation_integration_test.go): raw error (not `ErrAuditPublicationLeaseLost`) returned, row byte-identical | `make test-integration` |
| Out-of-order drain settlement | `TestDrainSettlesClaimedBatchInReverseOrder` (callback_integration_test.go): two rows claimed in one batch settled in reverse claim order — both complete exactly once, one dedup receipt per event, stale finish after settlement fenced `ErrAuditPublicationLeaseLost`, second drain a no-op; `TestDrainStaleReclaimInterleavesFreshRow`: drain skips a live stale lease, settles the fresh row first, then re-claims the expired stale row — settlement order B-then-A (reverse of creation order), stale worker's finish fenced mid-lease, duplicate re-publish legal (at-least-once) | `make test-integration` |
| Claim-stage poison row (FM-12) | `TestDrainClaimPoisonRowObservesMetricRecovers` (service): trigger raises inside `ClaimPublication` → claim tx rolls back atomically (both rows `pending`/attempt 0, nothing stranded `delivering`), `ObserveAuditPublicationClaimFailed` observed with the raw error (never silent), zero outcomes; after trigger removal the next drain claims and settles both rows; `TestClaimPublicationPoisonRowReturnsRawError` (repo): raw error returned, never remapped to lease-lost | `make test-integration` |
| REQ-6 wiring fails closed | `TestWireAuditServicesFailClosedOnMissingSetters` (audit_wiring_test.go:31) + `TestAssertAuditWiringFailsFastOnNilAuditLogger` (:84): `wireAuditServices`/`assertAuditWiring` panic on unwirable wiring; `wireAuditServices` panics reviewed at `app/audit.go:65-75` | **unit** (no build tag; fake repos, no Postgres): `go test ./internal/app/ -run 'TestWireAuditServicesFailClosedOnMissingSetters\|TestAssertAuditWiringFailsFastOnNilAuditLogger' -count=1`. (The earlier `-run TestAuditWiring` pattern matched no test in `internal/app` — a silent no-op — and the "(integration)" label was false; corrected here.) |

## 7. Open items and risks

1. **REQ-3.2 (only code delta):** extend the gate scan to `cmd/` + `internal/app` (§5 step 2).
2. **Commit:** 69 files uncommitted; do not treat the working tree as shipped.
3. **Two-test composition vs single e2e:** the G3 acceptance is proven by
   `TestAuditWiringHappyPath` ∘ `TestFirstFactDrainDeliversExactlyOnce`; a single chained test is optional.
4. **Residual risk — dead-tree drift:** the 6 dead-tree `_ = s.audit.Log` sites are excluded by design but
   are a live-gate blind spot if that tree is ever wired in; the sibling spec owns the 10 live `Log` sites.
5. **Resolved — publication-row orphan policy.** The orphan population is **provably zero**: `audit_ledger`
   (000034), `audit_publications` (000045), the in-tx minting code, and `drainPublications` shipped
   atomically in `aafd7df`, so any ledger row written by any shipped binary has its publication row in the
   same committed transaction. Orphans can only appear via a partial/rolling deploy, manual inserts, or a
   restore from a mid-deploy backup. Policy: (a) runbook check after migrate and after any rollback/re-apply
   — `SELECT count(*) FROM audit_ledger l LEFT JOIN audit_publications p ON p.fact_id = l.id WHERE
   p.fact_id IS NULL` must be 0; (b) if non-zero, ledger remains authoritative and delivery for orphans is
   foregone by default; an idempotent backfill (`INSERT INTO audit_publications … SELECT … ON CONFLICT
   (fact_id) DO NOTHING`, status `pending`) is safe thanks to the unique constraint but must be a **new
   migration or ops query, never an edit to the applied 000045** (AGENTS.md: never edit applied
   migrations); (c) the down/up cycle cannot create orphans as long as the down-migration is blocked for
   binaries ≥ `aafd7df` (appends fail while the table is down, so no new ledger rows accrue).
