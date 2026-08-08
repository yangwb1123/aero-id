# Design — B2-1: route hold/cutover audit writes into the business transaction, close the no-swallow gate hole, prove anchored-ID dedup fold (T-5, T-4)

| | |
|---|---|
| Requirements | `docs/requirements/2026-08-06-internal-audit-b2-1-in-tx-audit-and-dead-loop.md` (REQ-1..REQ-5) |
| Module (batch label) | `internal/audit` — live machinery in `internal/service` + `internal/repository`; the `internal/audit` package itself is the legacy unwired tree (no work there) |
| Head verified | `13e4676` + uncommitted working tree (39 modified files) |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery; gate G3: T-5, T-4 joint) |
| Sibling spec | `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` + design `docs/plans/2026-08-06-cmd-server-complete-in-tx-audit-writes-design.md` (owns the ten `Log` sites and the four fallback tails; shares the gate edit and the hold/cutover repo APIs) |
| Date | 2026-08-06 |
| Status | Design — every cited symbol re-verified against the working tree |

## 1. Evidence verification (untrusted claims vs. repository)

All evidence claims re-checked against the live tree. Verdicts:

| # | Claim | Verdict (proof) |
|---|---|---|
| E1 | Dead inline publisher loop already removed: `Record` calls `repo.Append` only; `NewAuditLedgerService` single-arg; zero `_ = publisher.Publish` repo-wide | ✅ `internal/service/audit_ledger.go:48-56` struct has only `repo` + `metrics`; constructor `:56` single-arg; `Record` `:68-82` appends only (doc comment `:52-53`); `grep -rn "_ = publisher.Publish" internal/` → 0 hits. Keep-enforced (REQ-1) |
| E3 | 9 swallow sites resolved; only `audit_hold` Put/Remove and `legacy_cutover` Set remain out-of-tx; `account.go:155,186` are read-path | ✅ `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go` → 0 hits. Hold/cutover confirmed: `service/audit_hold.go:66-81` (`Put`), `:89-107` (`Remove`) — domain commit, then **second-tx** checked `s.audit.Record` (error → `apperr.Internal("audit_hold.audit_failed")`); `service/legacy_cutover.go:91-108` (`Set`) — same shape (`cutover.audit_failed`). `account.go:154-158,181-188` are sensitive-read audits, checked (`apperr.Unavailable("audit.ingest_failed")`), no business write exists — correctly out-of-tx |
| A1 | 10 swallowed `_ = s.audit.Log(...)` in admin/ban/block/comment/file_share/message, invisible to the gate | ✅ Exactly 10 in the live layer: `admin.go:93,120` · `ban.go:83,119` · `block.go:54,77` · `comment.go:415` · `message.go:313` · `file_share.go:114,146` (plus 6 more in the unwired tree, excluded). `Log` adapts to `Record`→`Append` (`audit_ledger.go:135-141`), so these swallow durable-ledger writes. **Owned by the sibling spec** — this design only closes the gate hole they depend on |
| E7 | `check-audit-swallow` gate exists, green, wired into done-gate; `Log` hole documented | ✅ `Makefile:52-54` → `scripts/check-audit-swallow.sh`; P1 `_ = ….(Record\|Publish\|FinishPublication\|ClaimPublication)`, P2 bare-call `Record\|FinishPublication\|ClaimPublication`; self-test fixtures under `scripts/testdata/audit-swallow/`; the negative fixture (`negative/fixture.go:15`) literally contains `_ = s.audit.Log(ctx, event) // legacy diagnostics surface: excluded`; the script header documents the exclusion and the stale baseline ("audit_ledger.go:79 inline `_ = publisher.Publish`", long removed). Wired into the done-gate alongside `check-anchored-event-ids` (`Makefile:60-62`) |
| E9 | T-4 anchoring landed: `idgen.Anchor` at hold `:69,96`, cutover `:97`, `operation_audit.go:65`; sole C2 fresh-ID site `callback.go:261` | ✅ with one-line drift — the anchors sit at the **service** layer: `audit_hold.go:70` (`"audit_hold.set"`, 6-part), `:97` (`"audit_hold.removed"`, 4-part), `legacy_cutover.go:98` (`"legacy.cutover.set"`, 4-part incl. previous status), `operation_audit.go:65`. `idgen.Anchor`/`RenderAnchorTime` at `internal/pkg/idgen/idgen.go:35,54` (uncommitted). C2 allow-list confirmed at `callback.go:260-267` (comment + `idgen.NewID()`). `scripts/check-anchored-event-ids.sh` (R-1/R-2 + self-tests) wired into done-gate |
| E2/E6 | `AppendInTx` + `XxxWithAudit` precedent pattern | ✅ `repository/audit_ledger.go:106`; pattern verified in `callback_audit.go:31-55,60-84,87-112` (service pre-builds `*domain.AuditFact` via `buildAuditFact`, repo runs one gorm tx: domain write → `AppendInTx`, `ErrAuditDuplicate` swallowed **in-tx after the domain write**), `operation_audit.go:119-136` (deterministic re-claim rationale), `account_provision_audit.go:45,74`; fail-closed head `requireAuditLedger` (`repository/audit_wiring.go:14-28`, panics on nil/typed-nil) |
| E8 | Rollback-atomicity and publication-rollback tests exist | ✅ `TestAuditWiringRollbackAtomicity` at `audit_wiring_integration_test.go:175` (duplicate append → whole tx rolls back; Class B step path swallows and commits); `TestAuditWiringPublicationInsertRollsBackLedgerRow` at `:268` (BEFORE INSERT trigger on `audit_publications` → 0 ledger + 0 publication + 0 domain rows). Coverage: profile/operation/event only — **hold/cutover not covered** |
| E10 | Dedup contract `(tenant_id, source, event_id)` | ✅ `checkAuditDuplicate` `:157-171` + `uq_audit_ledger_tenant_event` (migration 000050); 23505 → `ErrAuditDuplicate` (`:183-193`; sentinel declared `:49`); `Record` maps duplicate → nil (`:70-73`); tests `TestAppendConcurrentDuplicateAcrossPartitions`, `TestAnchoredEventIDAppendsOnce`, `TestPutHoldCopyBack` (`anchor_dedup_integration_test.go:365`) |

### Material amplifications the design must carry

- **F1 — copy-back contract lands in-tx.** `PutHold` already returns the FINAL stored `id/created_at/expires_at` via `RETURNING` (uncommitted diff on `repository/audit_hold.go:69-90`; pinned by `TestPutHoldCopyBack`). `PutHoldWithAudit` must keep this inside its transaction so the response reflects the committed row and the anchor inputs equal the persisted values. The anchor (`audit_hold.set`, tenant, scopeType, scopeID, reason, `RenderAnchorTime(expiresAt)`) is built from the post-normalization **argument** values; `RenderAnchorTime` truncates to the microsecond exactly like `timestamptz` storage, so the pre-built fact and a post-copy-back fact are byte-identical — the service can build the fact before the repo call and still satisfy REQ-2.2.
- **F2 — the error codes per mutation must survive the merge.** Hold exposes **three** stable codes today, not two (F-A): `audit_hold.save_failed` (Put domain write, `service/audit_hold.go:56`), `audit_hold.remove_failed` (Remove domain write, `:85`), and `audit_hold.audit_failed` (both append arms, `:73`/`:100`). Cutover exposes two: `cutover.save_failed` (`legacy_cutover.go:85`) and `cutover.audit_failed` (`:101`). Only the `*.audit_failed` arms are pinned today (`TestAuditHoldAuditFailureFailsClosed` at `audit_hold_test.go:114`, `TestCutoverAuditFailureFailsClosed` at `legacy_cutover_test.go:152`); the merge is the moment to freeze the domain-write codes too (§6 step 2). Once the append moves inside the domain transaction, a raw tx error is ambiguous (domain write vs append). The design preserves every code with a small repo-level sentinel wrap (§3.3) — the callback/operation precedents merge to one code per mutation, but they never had three.
- **F3 — hold/cutover are Class B sites; the REQ-5.2 "duplicate-append → rollback" arm is not exercisable for them.** Their anchors are a bijective encoding of the **anchor-relevant** mutation inputs (identical anchor ⟺ identical tenant/scope/reason/expiry/previous-status inputs; non-anchored columns such as `created_by` and `note` overwrite on fold — byte-for-byte today's `Record`→duplicate→nil behavior, F-B), so a duplicate append can only occur on a crash-retry with identical anchor-relevant inputs — the in-tx swallow (Class B, contract §4.2) then **commits** the idempotent domain write with one fact. The forced-rollback proof for hold/cutover therefore uses the trigger-injection mechanism (`TestAuditWiringPublicationInsertRollsBackLedgerRow` shape); the duplicate injection proves the retry-fold commit side (1 ledger row), not a rollback. The operation `appendAudit` precedent (`operation_audit.go:125-136`) is the exact same classification.

## 2. Target architecture

```text
auditHoldService.Put/Remove · legacyCutoverService.Set
   │  s.audit != nil → type-assert holds.(repository.AuditHoldAuditRepository)   (fail-closed head)
   ▼
repo PutHoldWithAudit/RemoveHoldWithAudit/SetCutoverWithAudit:
   one gorm tx { domain write (upsert/delete, RETURNING copy-back) + r.audit.AppendInTx(ctx, tx, fact) }
   │  ErrAuditDuplicate swallowed in-tx AFTER the domain write (Class B)
   ▼
audit_ledger row + pending audit_publications row commit with the domain row
   ▼
drainPublications (callbackService.Start ticker) — sole delivery path (unchanged)
```

Post-migration, the only out-of-tx `s.audit.Record` calls in `internal/service` are the justified read-path/standalone set (`account.go:154-158,181-188`, operation state, replay, event ingest) — all checked, never `_ =`.

## 3. API changes

### 3.1 Repository constructors (2 signatures change; aligned with sibling design §3.1)

| Constructor | Old | New |
|---|---|---|
| `NewAuditHoldRepository` | `(db *gorm.DB)` | `(db, audit AuditLedgerTransactionalRepository)` |
| `NewLegacyCutoverRepository` | `(db *gorm.DB)` | `(db, audit)` |

Each struct gains `audit AuditLedgerTransactionalRepository` and the constructor calls `requireAuditLedger("NewAuditHoldRepository", audit)` / `("NewLegacyCutoverRepository", audit)` (panic on nil = fail at startup; precedent `audit_wiring.go:14-28`). Plain methods `PutHold`/`RemoveHold`/`SetCutover` stay for tests and legacy callers — the `WithAudit` variants are additive, like `Finish` vs `FinishWithAudit`.

> Coordination: the sibling design specifies the same two constructor changes; the shared call sites are `internal/app/audit.go:147`, `internal/app/new.go:228`, `internal/app/new.go:398` (the duplicate cutover seam inside `NewAccountHandlerWithExtensions`), plus the repo integration tests that construct them. One implementation satisfies both specs.

### 3.2 New repository audit interfaces (sibling files, one cohesive concern per file)

`repository/audit_hold_audit.go`:

```go
type AuditHoldAuditRepository interface {
	PutHoldWithAudit(ctx context.Context, hold *AuditHold, fact *domain.AuditFact) error
	RemoveHoldWithAudit(ctx context.Context, tenantID string, scopeType AuditHoldScope, scopeID string, fact *domain.AuditFact) error
}
```

`repository/legacy_cutover_audit.go`:

```go
type LegacyCutoverAuditRepository interface {
	SetCutoverWithAudit(ctx context.Context, cutover *LegacyCutover, fact *domain.AuditFact) error
}
```

Method contracts (mirror `callback_audit.go:31-55`):

- **`PutHoldWithAudit`** — one gorm tx: the existing upsert (`clause.OnConflict` on the unique triple, `DoUpdates` on `reason, created_by, expires_at, created_at`) **with the `RETURNING id, created_at, expires_at` copy-back preserved** (F1), then `AppendInTx`. `ErrAuditDuplicate` swallowed after the upsert (Class B, anchored retry-fold). The copy-back happens inside the tx before commit, so the returned `hold` reflects the committed row even on the conflict path.
- **`RemoveHoldWithAudit`** — one gorm tx: the existing `DELETE` (no `RowsAffected == 0` guard — today `RemoveHold` of an absent hold returns nil and still records the fact; preserve that semantic), then `AppendInTx`. Duplicate swallowed in-tx.
- **`SetCutoverWithAudit`** — one gorm tx: the existing `clause.OnConflict` upsert on `tenant_id` (`DoUpdates` on `status, decided_at, decided_by, note, updated_at`), then `AppendInTx`. Duplicate swallowed in-tx.

The concrete repos implement the interfaces; the service type-asserts (pattern: `CallbackAuditRepository`, `AccountProfileAuditRepository`).

### 3.3 Sentinel + service-layer shapes

**New exported sentinel** in `repository/audit_ledger.go` next to `ErrAuditDuplicate`:

```go
// ErrAuditAppend marks an in-tx audit append failure (as opposed to a domain
// write failure) so services can preserve their pre-merge error codes
// (audit_hold.audit_failed / cutover.audit_failed).
var ErrAuditAppend = errors.New("audit ledger: in-transaction append failed")
```

The three `WithAudit` methods wrap the append failure with `errors.Join(ErrAuditAppend, err)` (preserves the cause chain, `errors.Is` works). Domain-write errors keep their existing wraps (`fmt.Errorf("create hold: %w", err)` style). The service maps:

- `errors.Is(err, repository.ErrAuditAppend)` → existing `apperr.Internal("audit_hold.audit_failed", …)` / `("cutover.audit_failed", …)` — **code preserved**
- otherwise → **per mutation, matching today's arms (F-A):** `audit_hold.save_failed` (Put), `audit_hold.remove_failed` (Remove — today's domain-write arm is `remove_failed` at `service/audit_hold.go:85`, NOT `save_failed`), `cutover.save_failed` (Set) — **codes preserved**

**`auditHoldService.Put`** becomes (Remove analogously — with the non-append error arm mapped to `audit_hold.remove_failed` per F-A, never `save_failed`):

```go
hold := &repository.AuditHold{…} // unchanged normalization (trimmed scope_id)
if s.audit != nil {
	repo, ok := s.holds.(repository.AuditHoldAuditRepository)
	if !ok {
		return nil, apperr.Internal("audit_hold.audit_unavailable",
			"audit persistence is unavailable for legal holds")
	}
	tenant := tenantID
	if fromCtx := TenantFrom(ctx); fromCtx != "" {
		tenant = fromCtx
	}
	fact := buildAuditFact(ctx, AuditFactInput{Source: "aero-id",
		EventID:  idgen.Anchor("audit_hold.set", tenant, string(scopeType), hold.ScopeID, reason, idgen.RenderAnchorTime(expiresAt)),
		ActorUID: actor, TenantID: tenant, Action: "audit_hold.set",
		ResourceType: string(scopeType), ResourceID: hold.ScopeID,
		Payload: map[string]any{"reason": reason, "expires_at": expiresAt}}) // byte-identical to today's Record input
	if err := repo.PutHoldWithAudit(ctx, hold, fact); err != nil {
		if errors.Is(err, repository.ErrAuditAppend) {
			return nil, apperr.Internal("audit_hold.audit_failed", "failed to record the legal hold audit").Wrap(err)
		}
		return nil, apperr.Internal("audit_hold.save_failed", "failed to record the legal hold").Wrap(err)
	}
	return hold, nil
}
return s.holds.PutHold(ctx, hold) // s.audit == nil: byte-for-byte today's plain path (unit tests)
```

**`legacyCutoverService.Set`** — same shape: the fact keeps `EventID: idgen.Anchor("legacy.cutover.set", tenant, string(status), string(current))` with `current` from the existing `Get` pre-read, `Payload` `{"status","previous","note"}` unchanged; `SetCutoverWithAudit(ctx, entry, fact)`; `errors.Is(err, ErrAuditAppend)` → `cutover.audit_failed`, else `cutover.save_failed`; `!ok` → new `cutover.audit_unavailable`. The `s.actor` field and `entry.DecidedBy` are unchanged.

The type-assert head mirrors the landed `callback.audit_unavailable` head (`callback.go:258-261`); it is unreachable in production (concrete repos always implement the audit interfaces) and only fires for fakes/mocks.

### 3.4 No-swallow gate closure (REQ-3; joint with sibling design §3.4)

`scripts/check-audit-swallow.sh`:

- **P1** gains a receiver-based alternative: `^[[:space:]]*_ = s\.audit\.` (any method, **including `Log`**), kept in union with the existing `_ = <recv>.(Record|Publish|FinishPublication|ClaimPublication)(` (which also catches `_ = publisher.Publish(` and `_ = s.publications.FinishPublication(`).
- **P2** gains `^[[:space:]]*s\.audit\.` (any method) at line start — bare `s.audit.Log(ctx, e)` statements also discard errors. The `d.pusher.Publish` SSE exclusion is untouched (`d.pusher` is not `s.audit`; `Publish` stays out of the P2 method list).
- **Fixtures**: the positive fixture gains `_ = s.audit.Log(ctx, event)` and `_ = publisher.Publish(ctx, fact)` (self-test fails without them); the negative fixture **drops** the documented `_ = s.audit.Log(ctx, event) // legacy diagnostics surface: excluded` line (`scripts/testdata/audit-swallow/negative/fixture.go:15`).
- Update the stale "Baseline on today's tree" header comment (it still names the removed `audit_ledger.go:79` inline loop): baseline is now 10 `_ = s.audit.Log` hits (invisible to the old gate) + 0 others; target 0.
- The gate scan root stays `internal/service`; the legacy unwired tree, comment lines, and `if err :=`/`return`/`err :=`/`defer`/`go` forms remain excluded.

**Sequencing constraint (coordination with sibling):** the gate edit and the ten `Log`-site conversions must land together (or gate-last). With the closed gate and unconverted sites, `make check-audit-swallow` reports exactly the 10 known hits — that is the proof the hole existed — and it must be green (0 hits) before the done-gate runs.

### 3.5 Composition root (`internal/app`)

- `buildAuditComplianceServices` (`app/audit.go:145-147`): the `ledgerRepo` parameter type must widen from `repository.AuditLedgerRepository` to `repository.FullAuditLedgerRepository` — today it is only the read interface and cannot satisfy the new constructor's `AuditLedgerTransactionalRepository` parameter (compile break; the caller `new.go:366` already passes the `FullAuditLedgerRepository` from `buildAuditStack`). Then `repository.NewAuditHoldRepository(gdb, ledgerRepo)`.
- `new.go:228` and `new.go:398`: `repository.NewLegacyCutoverRepository(gdb, auditLedgerRepo)` — both sites (the duplicate seam inside `NewAccountHandlerWithExtensions` is load-bearing, per the sibling hardening doc).
- `buildAuditStack`/`wireAuditServices`/`callbackService.Start` unchanged — REQ-1 is keep-enforced.

## 4. Compatibility constraints

1. **HTTP/gRPC API surfaces unchanged.** Same request/response shapes. Error codes for the same causes are preserved (F2 sentinel): append failure → `audit_hold.audit_failed`/`cutover.audit_failed`; domain-write failure → `audit_hold.save_failed` (Put) / `audit_hold.remove_failed` (Remove) / `cutover.save_failed` (Set) — the F-A three-code inventory. One new code per service (`*.audit_unavailable`) for the unreachable-in-production type-assert head.
2. **Anchored EventIDs preserved byte-identical** (`audit_hold.set` 6-part, `audit_hold.removed` 4-part, `legacy.cutover.set` 4-part with previous status) — the B2-4 constraint. The service builds the identical `AuditFactInput` it builds today; `buildAuditFact` applies the same context binding, normalization, and redaction. Crash-retry re-derives the same anchor; the in-tx Class B swallow commits the idempotent domain write — exactly today's `Record` + `ErrAuditDuplicate→nil` semantics **minus the commit-to-append crash window**.
3. **Fact content parity.** Payloads, actor, tenant, resource fields identical. `OccurredAt` normalizes at fact-build time (before the tx) instead of after the domain commit — sub-millisecond drift, not part of the dedup key, no consumer-visible change.
4. **Dedup fold semantics unchanged.** Re-put with a different reason/expiry derives a different anchor → new fact (G9a≠G9b); remove-of-absent-hold still records the fact and returns nil; second remove of a re-created hold folds into the duplicate (today it already folds via `Record`→nil).
5. **Plain repo methods and service constructors/options retained** (`PutHold`, `RemoveHold`, `SetCutover`, `NewAuditHoldService`, `NewLegacyCutoverService`, `With*AuditLogger`-style options where present). Only the two repo constructors gain a mandatory ledger parameter — compile-time breaking; all call sites are listed in §3.5 and the sibling design §6.
6. **`s.audit == nil` behavior unchanged.** Unit tests and any future wiring without an audit service take the plain repo path byte-for-byte.
7. **Class B classification is safe by construction.** Hold/cutover anchors biject over the semantically-distinguishing inputs (tenant, scope type/id, reason, canonical expiry, previous status); non-anchored columns (`created_by`, `note`) overwrite on fold, exactly as today's post-commit `Record`→`ErrAuditDuplicate`→nil path does (F-B). Swallowing after the write inside the same tx can therefore never mask a failed domain write (contract §4.2).
8. **No new migrations, no protobuf changes.** `audit_ledger`/`audit_publications` + UNIQUE `(fact_id)` (000045), `uq_audit_ledger_tenant_event` (000050), and `AppendInTx`'s publication-row creation already exist.
9. **Engineering gates.** New methods live in sibling `*_audit.go` files (≤500-line rule); each new function stays ≤50 lines / cyclop ≤10 (straight-line tx wrappers). Tests follow the split.

## 5. Failure modes

| Failure | Behavior after migration | Recovery |
|---|---|---|
| Crash between domain commit and audit append | **Eliminated** for hold put/remove and cutover set — the commit-to-append gap no longer exists (REQ-2's purpose) | n/a |
| Ledger append fails in-tx (DB error, signing-key misconfig) | Whole mutation rolls back: 0 ledger rows, 0 publication rows, 0 domain rows; service maps the sentinel wrap to `audit_hold.audit_failed`/`cutover.audit_failed` | `validateAuditLedgerConfig` + `requireAuditLedger` catch config/wiring before traffic; transient DB errors retry at the client (idempotent upserts) |
| `ErrAuditDuplicate` in-tx (crash-retry with identical inputs) | Swallowed after the domain write; the idempotent upsert/delete commits; exactly one fact exists | Ledger dedup on `(tenant_id, source, event_id)`; drain delivers the single publication row at least once (idempotent) |
| Mis-wired repo (nil ledger) | Constructor panic at startup (fail fast) | `requireAuditLedger` + composition-root test |
| Non-concrete repo at runtime lacking the audit interface | Fail-closed `audit_hold.audit_unavailable`/`cutover.audit_unavailable`; no silent plain-write fallback | Unreachable in production (concrete repos always implement); unit fakes either implement `*WithAuditFn` or the service takes the `s.audit == nil` path |
| Cutover previous-status TOCTOU (Get before Set) | Unchanged from today: the anchor records the read value; the upsert is unconditional (no DB-level transition check exists); a concurrent transition produces the same distinct-fact behavior as today (G11≠G12≠G13) | n/a — semantics preserved, not introduced |
| Ledger partition lock contention | Each mutation takes one `pg_advisory_xact_lock` on its partition; hold/cutover are single-fact mutations (one lock) | Short critical sections; read path never appends |
| Delivery outage (governance endpoint down) | Facts durable in `audit_publications` (pending); `drainPublications` retries with backoff/jitter, dead-letters after cap | Manual replay (existing); callback failure never erases the fact |
| Gate regression (reintroduced `_ = s.audit.Log`/`_ = s.audit.Record`/`_ = publisher.Publish`) | `make check-audit-swallow` turns red (positive fixtures + live scan) | Gate is in the done-gate (AGENTS.md) |
| SSE false positive | Impossible: `d.pusher.Publish` is not an `s.audit.` receiver and `Publish` stays out of P2 | n/a |

## 6. Migration steps (each step leaves the tree green)

1. **Repo layer (additive, green).** Add `repository.ErrAuditAppend`; add the `audit` field + constructor param + `requireAuditLedger` to `auditHoldRepo`/`legacyCutoverRepo`; add `AuditHoldAuditRepository` (`audit_hold_audit.go`) and `LegacyCutoverAuditRepository` (`legacy_cutover_audit.go`) with the three `WithAudit` methods (upsert/delete + `RETURNING` copy-back + `AppendInTx`, duplicate swallowed after the domain write). Update the constructor call sites (compile break): `app/audit.go:147`, `new.go:228`, `new.go:398`, and the repo integration tests (`audit_hold_integration_test.go:24`, `legacy_cutover_integration_test.go:21`, `anchor_dedup_integration_test.go:368` — the cutover test already builds `ledger` at `:20`) — each passes a real ledger repo (`NewAuditLedgerRepository(db, cfg)`). Also widen `buildAuditComplianceServices`'s `ledgerRepo` parameter to `repository.FullAuditLedgerRepository` (`app/audit.go:146`) — the only other signature-level compile break the sweep found (§3.5).
2. **Service conversions (green).** Rewrite `auditHoldService.Put`/`Remove` and `legacyCutoverService.Set` to the §3.3 fail-closed shape; update the unit fakes (`fakeHoldRepo`/`fakeCutoverRepo` gain `*WithAuditFn` fields implementing the new interfaces) and the affected tests: `TestAuditHoldAuditFailureFailsClosed` and `TestCutoverAuditFailureFailsClosed` keep asserting `*.audit_failed` (now via the fake's WithAudit fn returning a sentinel-wrapped error) and gain a raw-error arm pinning `audit_hold.save_failed` (Put) / `audit_hold.remove_failed` (Remove) / `cutover.save_failed` (Set) — freeze the domain-write codes at the merge (F-A, §7); the anchor-discriminator tests (`TestHoldActionDiscriminator`, `TestCutoverAnchorTransitions`, `TestPutHoldCopyBack`-adjacent unit assertions) keep asserting the same derived event IDs — now captured from the fact argument of the WithAudit fn instead of the ledger spy's appended facts.
3. **Wiring (green).** `app/audit.go:147` and `new.go:228,398` pass `auditLedgerRepo`. `go build ./...`, `make test`, `make lint`, `make lint-strict`, `make check-file-sizes`.
4. **Integration proofs (REQ-4/REQ-5).** In `internal/repository/audit_wiring_integration_test.go` (or per-repo siblings): commit-side and crash-side cases for hold put/remove and cutover set (§7).
5. **Gate closure (REQ-3, joint with sibling).** Apply §3.4 pattern/fixture/comment changes **in the same change set as the sibling's ten `Log`-site conversions** (or after them), so the gate is green at 0 hits; verify it fails on a reintroduced `_ = s.audit.Log(` via the positive fixtures.
6. **Full done-gate.** `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make build` + `make test-integration` (repository SQL and callback delivery touched).

## 7. Testable acceptance mapping

| Supplied acceptance | Requirement | Test artifact / pass criterion |
|---|---|---|
| T-5: for each of the 9 sites, inject repo failure → either the business operation fails or `ObserveAuditIngestFailure` increments (no silent path) | REQ-2, REQ-5.1 | `go test ./internal/service/` — hold/cutover: `TestAuditHoldAuditFailureFailsClosed`/`TestCutoverAuditFailureFailsClosed` updated to the WithAudit-fake shape assert the operation error with the preserved codes — `*.audit_failed` for a sentinel-wrapped append error, `audit_hold.save_failed`/`audit_hold.remove_failed`/`cutover.save_failed` for a raw non-sentinel repo error (F-A); new `TestAuditHoldUnavailableWhenRepoLacksWithAudit`/`TestCutoverUnavailableWhenRepoLacksWithAudit` pin the `!ok` head (`audit_hold.audit_unavailable`/`cutover.audit_unavailable`) with a plain fake lacking the WithAudit interfaces, mirroring `TestOperationAuditUnavailableWhenRepoLacksWithAudit` (`operation_audit_test.go:89`); read-path sites (`account.go:155,186`) keep their existing operation-error assertion (spy pattern `account_test.go:72`); callback/provisioning in-tx sites already covered by repo integration (`callback_audit_integration_test.go`). **0 sites** where the operation succeeds with the failure unobserved. `grep -rnE '^[[:space:]]*_ = s\.audit\.' internal/service/*.go` → 0 |
| Forced rollback shows zero `audit_ledger` rows (mirrors `TestAuditWiringRollbackAtomicity`) | REQ-5.2 | `go test -tags=integration ./internal/repository/` — new `TestAuditWiringHoldCutoverPublicationInsertRollsBack`: BEFORE INSERT trigger on `audit_publications` → `PutHoldWithAudit`/`RemoveHoldWithAudit`/`SetCutoverWithAudit` return an error and **0 audit_ledger rows + 0 audit_publications rows + 0 domain rows** survive (F3: trigger injection, not duplicate injection — hold/cutover are Class B). Existing `TestAuditWiringRollbackAtomicity` (`:175`) and `TestAuditWiringPublicationInsertRollsBackLedgerRow` (`:268`) stay green |
| New gate greps `_ =` against audit calls incl. `Log` and is green repo-wide | REQ-3 | `make check-audit-swallow` — positive fixtures include `_ = s.audit.Log(` + `_ = publisher.Publish(` (self-test fails without them); negative fixture no longer excludes `Log`; live scan of `internal/service` = 0 hits; reintroduced swallow turns the gate red; runs in the done-gate alongside `make check-anchored-event-ids` |
| T-10.1-side: grep proves `publisher.Publish` unreachable outside `publishAuditFact` | REQ-1.3 | `grep -rn "\.Publish(ctx, fact)" internal/service/` → only `callback_audit.go:113`; `grep -rn "drainPublications(" internal/` → definition (`callback_audit.go:47`) + `Start` ticker (`callback.go:282`) only; `NewAuditLedgerService(` has no second argument in non-test Go; `TestAuditLedgerRecordEnqueuesOnlyIntegration` stays green (1 Record → 1 ledger row + 1 pending publication row, no delivery) |
| T-4: anchored event_ids fold into `ErrAuditDuplicate` and exactly one ledger row; hold/cutover re-execution | REQ-4, REQ-5.3 | `make check-anchored-event-ids` green; `grep -rn "EventID: idgen.NewID()"` in non-test Go matches only the C2 allow-list (`callback.go:261`). New integration cases (extend `anchor_dedup_integration_test.go`): `TestAuditHoldPutRetryFold` / `TestAuditHoldRemoveRetryFold` / `TestCutoverSetRetryFold` — run the same `WithAudit` mutation twice with identical inputs → exactly 1 `audit_ledger` row, domain row reflects the second execution (upsert refresh / absent after remove), no error on the second call (Class B swallow); `TestPutHoldCopyBack` stays green and is extended: the in-tx variant returns the committed `id/created_at/expires_at` on both the insert and conflict path. `TestAnchoredEventIDAppendsOnce`, `TestAppendConcurrentDuplicateAcrossPartitions`, `TestAppendConcurrentDuplicateBarrier` stay green |
| Fact parity + anchor preservation | REQ-2.2, §4.2 | Integration commit-side cases assert the ledger row's `event_id` equals `idgen.Anchor(...)` over the persisted values (scope trimmed, canonical expiry) and the payload equals today's fact content; unit tests capture the fact argument from the WithAudit fake and assert `Action`/`ResourceType`/`ResourceID`/payload/`EventID` byte-identical to the pre-conversion spy assertions |

### Commit-side matrix (REQ-5.2 counterpart)

New integration cases asserting the positive side of each migrated mutation: `PutHoldWithAudit` → 1 `audit_holds` row + 1 ledger row (anchored `event_id`, `action=audit_hold.set`) + 1 `pending` publication row, all in one commit; `RemoveHoldWithAudit` → hold row absent + 1 ledger row (`audit_hold.removed`) + 1 publication row; `SetCutoverWithAudit` → cutover row updated + 1 ledger row (`legacy.cutover.set`, previous status recorded) + 1 publication row.

## 8. Coordination with the sibling spec (explicit boundaries)

| Surface | Owner | Notes |
|---|---|---|
| `NewAuditHoldRepository(db, audit)` / `NewLegacyCutoverRepository(db, audit)` constructor changes | This design (§3.1) + sibling design §3.1 | Same edit; call sites shared (`app/audit.go:147`, `new.go:228,398`) |
| `AuditHoldAuditRepository` / `LegacyCutoverAuditRepository` interfaces + `WithAudit` methods | This design (§3.2) | Sibling design §3.2 lists identical signatures; tx-body details (copy-back, no-ErrNotFound on remove, Class B swallow) specified here |
| Ten `_ = s.audit.Log` conversions + four fallback tails | Sibling spec only | Precondition for the gate's 0-hit target |
| Gate closure (patterns, fixtures, baseline comment) | This design (§3.4) + sibling design §3.4 | One script edit; must land with/after the sibling conversions |
| Rollback/retry-fold/commit-side integration tests for hold/cutover | This design (§7) | Sibling owns the 16-class matrix for its own mutations |
| REQ-1 keep-enforced proofs (single delivery path) | This design (§7, T-10.1 row) | No code change; mechanical greps + existing test |

Non-goals (unchanged from the requirements): the legacy `internal/audit` tree and `internal/{file,message,comment,block,notification,search}`; `callbackService.Publish` and the C2 replay site (`callback.go:261`); `projection_replication.go` (event envelopes, not audit); TokenSource/401, dead-letter replay, activation-gate directions; no migrations.
