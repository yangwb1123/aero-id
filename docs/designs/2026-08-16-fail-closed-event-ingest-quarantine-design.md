# Design — Fail-closed event-ingest quarantine: propagate `QuarantineEvent` errors and record the anchored durable audit fact

- **Date:** 2026-08-16
- **Module:** `internal/service` (event-ingest surface: `eventIngestService`, `internal/service/event_ingest.go`); gate `scripts/check-audit-swallow.sh`
- **Direction:** Fail-closed event-ingest quarantine — propagate `QuarantineEvent` errors and record the durable audit fact on residency rejections (analysis source `docs/auto/analyses/internal-service-2bd58324.json`, direction 1)
- **Status:** Implemented and verified on the current tree (implementation landed in `6a05e2c`; current HEAD `df4f3de` carries the identical state). This document is the as-shipped design record plus the regression contract. The pre-implementation proposal is `docs/designs/2026-08-16-close-event-quarantine-audit-gap-design.md`.
- **Contract anchors:** `AGENTS.md` audit-coverage invariant and fail-closed audit contract (`make check-audit-swallow`), stable dotted error codes, at-least-once delivery with durable dedup receipts

---

## 1. Verification verdict (evidence claims vs. current tree)

Every load-bearing claim in the incoming specification was re-verified against the current tree on 2026-08-16. **All confirmed.** Two non-behavioral drifts noted in the table.

| Evidence claim | Verification | Result |
|---|---|---|
| Both quarantine paths route through a shared `quarantineEvent` helper that propagates receipt-write errors and records the anchored audit fact (`event_ingest.go:76`, `:122`, `:134-142`); historical `_ = s.receipts.QuarantineEvent(...)` shapes gone | `read` — helper at `:134-142` maps receipt errors to `apperr.Internal("event.quarantine_failed", ...)`; call sites `:76` (region) and `:122` (invalid-projection) propagate; grep confirms the only non-test `QuarantineEvent` call is the `if err :=` form at `:135` | ✅ exact |
| `recordAudit` at `:167`; `eventAuditInput` at `:170-180`; invoked with status `"quarantined"` at `:138` | `read` — `recordAudit` at `:167` with `// audit-site: standalone-fact` marker; `eventAuditInput` starts at `:176` (drift: +6 lines from the citation, same function); status `"quarantined"` passed at `:138` | ✅ (line drift only) |
| `QuarantineEvent` present in both `P1_PATTERN` and `P2_PATTERN` of `check-audit-swallow.sh`, with documented rationale and positive fixtures | `grep` — `P1_PATTERN` at `:143`, `P2_PATTERN` at `:144`, both include `QuarantineEvent`; header comments `:6`, `:17`, `:34-35` document the rationale; positive fixture lines at `scripts/testdata/audit-swallow/positive/fixture.go:17-18` (P1 and P2 shapes) | ✅ exact |
| Regression tests in `internal/service/event_ingest_quarantine_audit_test.go` (264 lines) | `wc -l` = 264; all six named tests present and passing; full-package `go test ./internal/service/ -count=1` → ok | ✅ exact |
| Manifest enumerates the site at `scripts/check-audit-manifest.sh:40` | `grep` — `internal/service/event_ingest.go\|eventAuditInput\|standalone-fact\|-` at `:40` | ✅ exact |
| Repository port `QuarantineEvent` at `internal/repository/event_receipt.go:186-208`: own transaction, `OnConflict DoNothing`, redacted payload, existing-row transition, `ErrEventReceiptBusy` | `read` — impl at `:186`, own `Transaction`, `OnConflict{DoNothing:true}`, `event.RedactPayload`, busy-lease guard | ✅ exact |
| Wiring at `internal/app/services.go:133-136` (`WithEventAllowedRegions`, `WithEventIngestAudit(deps.auditLedger)`, `WithEventIngestMetrics`); consumer `internal/app/new.go:239` | `grep` — `NewEventIngestService` at `:133`, consumer at `new.go:239` | ✅ exact |
| Ledger dedup key `uq_audit_ledger_tenant_event UNIQUE (tenant_id, source, event_id)` | `grep` — migrations/000050 up/down both reference it | ✅ exact |
| Current tree commit is `6a05e2c` (2026-08-16) | `git log` — `6a05e2c` is the landing commit (`event_ingest.go` +23, 264-line test file, swallow-gate +23, fixture +2, integration test +170) but the current HEAD is `df4f3de`, 29 commits later. Implementation state for this direction is identical | ⚠️ stale commit ref only |
| Gates: swallow/manifest/anchored-event-ids exit 0; `event_ingest.go` 170 lines | Re-run — swallow exit 0, manifest exit 0, anchored exit 0; `event_ingest.go` = 187 lines (≤ 500) | ✅ exact |
| Analysis source exists | `ls docs/auto/analyses/internal-service-2bd58324.json` | ✅ exact |

**Defect claimed by the direction (verified as fixed):** the region-not-allowed path previously discarded the receipt-write error (`_ =`) and recorded no audit fact, while the invalid-projection path recorded a fact unconditionally even when the receipt write failed. Both halves are closed on the current tree.

---

## 2. Problem statement (as designed)

The ingest gateway is the last line of defense for source events: envelopes that must not touch local projections are quarantined (durable `event_receipts` row, `status='quarantined'`, `error_code` set) rather than applied. Quarantine is a security-relevant rejection (residency policy P6-04, poisoned projection) and per AGENTS.md every such operation produces a durable audit fact. The gap had two halves:

1. **Coverage gap** — the region-disallowed branch wrote a durable receipt but no audit fact; the only audit write in `Ingest` was the post-apply path.
2. **Swallow blind spot** — both quarantine sites discarded the receipt-write error, and `check-audit-swallow.sh` could not match `QuarantineEvent`, so the swallow class was invisible to CI. The invalid-projection path additionally emitted a ledger fact even when the receipt write had failed (false fact).

---

## 3. Design overview (as landed)

Two coordinated changes, no schema or transport changes:

1. **Behavioral fix** in `internal/service/event_ingest.go`: both quarantine paths route through the shared `quarantineEvent` helper — persist receipt first (propagating failures as transient 5xx), then record an anchored `'quarantined'` audit fact (reusing `recordAudit`/`eventAuditInput` — same `standalone-fact` marker, same dedup anchor), then return the deterministic 4xx. A failed receipt write records **no** fact.
2. **Gate fix** in `scripts/check-audit-swallow.sh`: `QuarantineEvent` added to both P1 and P2 alternations plus positive fixture lines, so the swallow class is provably live-scanned.

The dedup chain (receipt `ON CONFLICT DO NOTHING` + ledger `uq_audit_ledger_tenant_event`) is untouched and makes redelivery converge to exactly one receipt row and exactly one ledger fact.

---

## 4. API changes

### 4.1 `internal/service/event_ingest.go` (the only code change)

No public/external API changes: no new protobuf, no new HTTP/gRPC routes, no migration, no repository-interface change. `EventIngestService.Ingest` signature and all returned statuses on success paths are preserved.

**Stable error codes in effect after the change:**

| Code | HTTP | gRPC | Meaning | NATS effect |
|---|---|---|---|---|
| `event.quarantine_failed` (new) | 500 | `Internal` | the quarantine receipt write failed; the event is **not** durably quarantined and no fact was recorded | `Nak()` → redelivery → at-least-once |
| `event.audit_failed` (existing; reused) | 500 | `Internal` | the quarantine is durable but its ledger fact failed to append | `Nak()` → redelivery → ledger dedup fold |
| `event.region_not_allowed` (existing) | 403 | `PermissionDenied` | deterministic residency rejection, quarantine + fact durable | `Term()` → JetStream DLQ |
| `event.invalid_projection` (existing) | 400 | `InvalidArgument` | deterministic poisoned-projection rejection, quarantine + fact durable | `Term()` → JetStream DLQ |

**Shared helper (as landed, `event_ingest.go:134-142`):**

```go
// quarantineEvent persists the quarantine receipt and its anchored audit fact.
// The receipt write is authoritative: a failure returns a transient 5xx error
// (Nak/redelivery) and records NO audit fact — a ledger fact must never exist
// without a durable receipt. An audit failure is also 5xx: the retry re-derives
// the identical fact anchored on envelope.EventID and folds on the ledger dedup
// key (uq_audit_ledger_tenant_event), so exactly one fact survives.
func (s *eventIngestService) quarantineEvent(ctx context.Context, envelope *event.Envelope, code string) error {
	if err := s.receipts.QuarantineEvent(ctx, envelope, code); err != nil {
		return apperr.Internal("event.quarantine_failed", "failed to quarantine event").Wrap(err)
	}
	if err := s.recordAudit(ctx, envelope, nil, "quarantined"); err != nil {
		return apperr.Internal("event.audit_failed", "failed to record event audit").Wrap(err)
	}
	return nil
}
```

`recordAudit` already no-ops when `s.audit == nil` (unit-test configs without `WithEventIngestAudit`), so the helper is safe in both configurations.

**Fact contract (via `eventAuditInput`, `event_ingest.go:176`; anchored dedup):**

- `Action = "source.event"`; `EventID = envelope.EventID` (anchor — deterministic, allow-listed by `check-anchored-event-ids`); payload `{"event_type", "schema_version", "aggregate_version", "status": "quarantined"}`.
- `Source`, `SourceRegion`, `OperationID`, `CorrelationID`, `CausationID`, `TraceID`, `ActorUID`, `TenantID`, `ResourceType = AggregateType`, `ResourceID = AggregateID`, `OccurredAt = envelope.OccurredAt` carried; `AccountID` only when `mutation != nil` (nil for quarantine paths).

**Deliberate micro-behavior note:** `ObserveEventQuarantined` now fires only after the quarantine is durable (receipt + fact) — previously it fired even when the receipt write failed. The metric key is unchanged; its semantics become "durable quarantine observed". Documented, not a non-goal violation.

### 4.2 `scripts/check-audit-swallow.sh`

- `QuarantineEvent` added to **both** alternations: `P1_PATTERN` (:143, assign-swallow `_ = ` / `_, _ = ` form) and `P2_PATTERN` (:144, bare-call form).
- Header comments (:6, :17, :34-35) document the rationale: the receipt write carries the quarantine state and its audit coupling; discarding it silently drops both.
- Positive fixtures pin both shapes (`scripts/testdata/audit-swallow/positive/fixture.go:17-18`); the script's `pos_hits >= 1` self-test proves the pattern member is live and cannot silently drop out.
- No anchoring, receiver-exemption, or root-list changes. The only `QuarantineEvent` occurrences in the live tree are the `if err :=` form (:135), the interface declaration (no leading dot — no P2 match), and the `func` impl declaration — so the extended patterns match 0 live lines.

### 4.3 No changes elsewhere

`internal/repository/event_receipt.go` (idempotent `QuarantineEvent` is the required behavior), NATS/envelope/redaction, `internal/app/services.go` wiring (already passes `deps.auditLedger`), migrations, proto — all untouched.

---

## 5. Compatibility constraints

| Constraint | Guarantee |
|---|---|
| Deterministic statuses preserved | Successful quarantine returns exactly the pre-existing 403 `event.region_not_allowed` / 400 `event.invalid_projection`. Nothing else in the ingest flow changes. |
| NATS ack semantics untouched | 4xx → `Term()`/DLQ, 5xx → `Nak()`/redelivery. The new 500 codes slot into the existing `transientDeliveryError` path (`>= 500` → Nak) with zero bus changes. |
| Receipt idempotency preserved | `QuarantineEvent` keeps `ON CONFLICT (tenant_id, source, event_id) DO NOTHING` + re-quarantine update for non-applied rows + `ErrEventReceiptBusy` for live leases. Redelivery converges to one receipt row. |
| Ledger dedup preserved | `recordAudit` re-derives the identical fact; append folds on `uq_audit_ledger_tenant_event` → `ErrAuditDuplicate` → `Record` returns nil. Redelivery converges to one ledger row. |
| Gate semantics preserved | Same P1/P2 statement anchoring; only the alternation members grow. No exemptions, no root shrinkage. Self-tests (`pos_hits >= 1`, negative fixtures = 0, FM-12 guards, breaker exemption, `*.pb.go` exclude) all stay green. |
| Manifest invariant preserved | Exactly one bare `s.audit.Record(` site in `event_ingest.go` ↔ exactly one manifest token (`eventAuditInput|standalone-fact|-`). |
| File-size gate | `event_ingest.go` = 187 lines, well under the 500-line gate and the 400-line split trigger. |
| Nil-audit configs | Unit configs without `WithEventIngestAudit` still get the deterministic status after a successful quarantine (`recordAudit` no-ops on nil). |
| Applied-path audit untouched | The post-apply `recordAudit(..., applied)` path and the in-tx `ApplySnapshotEventWithAudit` path are unchanged. |
| Residency policy tests unchanged | Existing `event_ingest_residency_test.go` policy tests (quarantine code + no-apply assertions) remain valid; `fakeReceiptRepo` gained a `quarantineErr` field that defaults nil. |

---

## 6. Failure modes and convergence

| # | Failure | First attempt | Retry outcome | Terminal state |
|---|---|---|---|---|
| F1 | Receipt write fails, region path | 500 `event.quarantine_failed` → Nak/redelivery; **no** fact recorded | `QuarantineEvent` re-runs; on success records the fact | Receipt durable + exactly one fact, then 403 → Term |
| F2 | Receipt write fails, invalid-projection path | 500 `event.quarantine_failed` → Nak/redelivery; false fact **suppressed** (fixes the pre-fix false-fact defect) | Same as F1; on success records fact, then 400 → Term | Receipt durable + exactly one fact, then 400 → Term |
| F3 | Audit append fails after durable receipt (either path) | 500 `event.audit_failed` → Nak/redelivery | Receipt `DO NOTHING` (already quarantined); `recordAudit` re-derives identical fact → `ErrAuditDuplicate` fold | Exactly one ledger row; deterministic 4xx → Term |
| F4 | Redelivery of an already-quarantined envelope (pre-existing receipt) | Receipt lookup: row is `quarantined`, not applied → idempotent re-quarantine update (no-op); fact re-recorded → dedup fold | — | One receipt row, one fact, deterministic 4xx |
| F5 | Busy lease (`ErrEventReceiptBusy`) during quarantine | Mapped to 500 (`event.quarantine_failed`) → Nak/backoff (consistent with `mapEventApplyError` treating busy as retryable) | After lease expiry the re-quarantine path proceeds | Converges as above |
| F6 | Process crash between receipt commit and audit append | Receipt durable, no fact | Redelivery re-derives and folds the fact | Exactly one ledger row (at-least-once + dedup) |
| F7 | Poison envelope failing `Validate()` | Rejected at `validateIngestEnvelope` with deterministic 4xx — unchanged, out of scope | — | `Term()`/DLQ, no receipt, no fact (pre-existing) |

Timeout is never proof of remote failure: every 5xx path redelivers with the same EventID and relies on the two durable dedup keys — no new mechanism needed.

**Invariant maintained:** a ledger `'quarantined'` fact for a given envelope exists **iff** its receipt row is durable. The false-fact shape (receipt failed, fact recorded) is eliminated.

---

## 7. Migration steps

No data migration, no rollout ordering concerns (wire format unchanged; old/new binaries coexist). The code + gate changes were atomic by necessity — the extended P1/P2 patterns are red against the pre-fix `_ =` sites, so the gate change and the code change must land in the same commit (as they did in `6a05e2c`):

1. **`internal/service/event_ingest.go`** — add the `quarantineEvent` helper; convert the region branch and `quarantineInvalidProjection` to the error-propagating forms (§4.1). Keep `event.quarantine_failed` / `event.audit_failed` codes, the `standalone-fact` marker, and the single bare `s.audit.Record(` site.
2. **`scripts/check-audit-swallow.sh`** — extend P1/P2 with `QuarantineEvent`; update the header member enumeration; add the positive fixture line(s) in `scripts/testdata/audit-swallow/positive/fixture.go`.
3. **Tests** — `internal/service/event_ingest_quarantine_audit_test.go` (unit, deterministic fakes; landed 264 lines) and `internal/service/event_ingest_quarantine_integration_test.go` (integration tag).
4. **Run the acceptance mapping** (§8); full done-gate last.

For the current tree this is a record, not an action list: steps 1-3 are already landed and green. The remaining obligation is to keep the done-gate green on every subsequent change (the regression contract, §9).

---

## 8. Testable acceptance mapping

### 8.1 Named tests (landed in `6a05e2c`, all green on current HEAD)

Fakes: `fakeReceiptRepo` (`event_ingest_residency_test.go:16-36`, `quarantineErr` field at `:20-21`), `quarantineAuditAppendSpy` (`event_ingest_quarantine_audit_test.go:24-44`; appends every attempt, folds duplicates with `ErrAuditDuplicate` mirroring `uq_audit_ledger_tenant_event`), `requireAppError` helper.

| REQ | Test | Assertion |
|---|---|---|
| REQ-1 | `TestEventIngest_RegionQuarantineReceiptFailureReturns5xxNoFact` (:103) | `Ingest(region-not-allowed, quarantineErr set)` → `*AppError` with `HTTPStatus() >= 500`, `Code == "event.quarantine_failed"`; exactly 1 quarantine attempt; **zero** ledger facts |
| REQ-2 | `TestEventIngest_RegionQuarantineRecordsAnchoredAuditFact` (:59) | 1 fact: `Action == "source.event"`, `fact.EventID == envelope.EventID`, `payload.status == "quarantined"`, tenant/source/`ResourceType`/`ResourceID` carried; event not applied; deterministic 403 |
| REQ-2/3 | `TestEventIngest_InvalidProjectionQuarantineRecordsAnchoredAuditFact` (:129) | `Snapshot()` failure → 1 anchored `"quarantined"` fact, deterministic 400 `event.invalid_projection` |
| REQ-2/3 | `TestEventIngest_InvalidProjectionReceiptFailureSuppressesFalseFact` (:167) | Receipt failure → `>= 500`, **zero** facts (false-fact suppression) |
| retry | `TestEventIngest_QuarantineRetryFoldsAtLedger` (:195) | Duplicate append folded via `ErrAuditDuplicate` → still 403 (never a 5xx audit failure) |
| retry | `TestEventIngest_QuarantineRetryAfterAuditFailure` (:235) | `event.audit_failed` 5xx on first attempt → 403 on retry; retried fact EventID anchored; receipt idempotent (2 attempts) |

### 8.2 Gate tests (re-run 2026-08-16, all exit 0)

| REQ | Gate | Green condition (verified) |
|---|---|---|
| REQ-4 | `bash scripts/check-audit-swallow.sh` | Exit 0: `QuarantineEvent` in P1+P2, positive fixture matched (`pos_hits >= 1`), live-tree matches == 0, negative fixtures == 0, all pinned self-tests green. Negative check: reverting either site to `_ = s.receipts.QuarantineEvent(` turns the gate red |
| REQ-4 | `bash scripts/check-audit-manifest.sh` | Exit 0: `event_ingest.go` has exactly 1 bare `s.audit.Record(` site matching exactly 1 manifest token (`eventAuditInput|standalone-fact|-` at `:40`), marker present |
| REQ-2 | `bash scripts/check-anchored-event-ids.sh` | Exit 0: `source.event` fact anchors on `envelope.EventID` (deterministic, golden-pinned anchor shape) |

### 8.3 Test and build gates

| Check | Result (2026-08-16) |
|---|---|
| `go test ./internal/service/ -count=1` | ok |
| `go test ./internal/service/ -run 'EventIngest' -count=1` | ok |
| `wc -l internal/service/event_ingest.go` | 187 (≤ 500 gate; ≤ 400 split trigger) |
| `go vet ./internal/service/` | clean |

Full done-gate (`make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && make check-migration-set && make check-composition-root && make check-proto-sync && make check-integration-compile && make build`) remains the release gate; nothing in this direction touches migrations, protos, load scenarios, or file-size boundaries (test files are `*_test.go`, exempt from `check-file-sizes`).

---

## 9. Non-goals / residual risk (regression contract)

**Non-goals (unchanged from the direction):** no changes to `repository.EventReceiptRepository` or `QuarantineEvent` persistence semantics; no new envelope fields, apply-path behavior, metrics surface, or config keys; no change to `event_ingest_residency_test.go`'s policy tests; no audit-coverage expansion beyond the two quarantine paths.

**Residual risk is regression, not gap:**

- A refactor reintroducing `_ = s.receipts.QuarantineEvent(...)` or a bare `s.receipts.QuarantineEvent(...)` is red under `check-audit-swallow` (REQ-4), pinned by positive fixtures at `fixture.go:17-18` and the `pos_hits >= 1` self-test.
- Any change dropping the anchored fact, the receipt-before-fact ordering, or the fail-closed 5xx mapping is red under REQ-1/REQ-2 tests (§8.1).
- Removing `QuarantineEvent` from either pattern or deleting the positive fixture lines trips the script's self-test.
- Keeping the done-gate green on every subsequent change is the acceptance of this contract.
