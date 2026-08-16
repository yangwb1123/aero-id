# Design — Close event-quarantine audit-coverage gap and the `QuarantineEvent` swallow blind spot

- **Date:** 2026-08-16
- **Module:** `internal/event` consumer/ingest gateway (`internal/service/event_ingest.go`, `internal/repository/event_receipt.go`, gate `scripts/check-audit-swallow.sh`)
- **Direction:** value 8, risk_reduction 8, effort 5, confidence 9 (source analysis item 1, `docs/auto/analyses/internal-event-9ecfce27.json`)
- **Status:** proposed (no repository changes in this document; implement per the migration steps below)

---

## 1. Verification verdict

Every claim in the incoming specification was re-verified against the current tree. **All load-bearing claims confirmed.** One historical footnote corrected (no behavioral impact).

| Claim (evidence) | Verification |
|---|---|
| `internal/service/event_ingest.go:76` — region-quarantine branch does `_ = s.receipts.QuarantineEvent(ctx, envelope, "event.region_not_allowed")` with no audit fact; the only `recordAudit` in `Ingest` is the post-apply path (:95) | ✅ Confirmed. Branch is :74–79 (metric :77, `Forbidden` :78). Zero audit sites in the branch. |
| `quarantineInvalidProjection` (:117–125) swallows the receipt write at `_ = ...` (:120) then **unconditionally** records `recordAudit(ctx, envelope, nil, "quarantined")` (:121), returning `BadRequest` (:124) | ✅ Confirmed. False fact is emitted even when the receipt write failed. |
| `EventReceiptRepository.QuarantineEvent(context.Context, *event.Envelope, string) error` (event_receipt.go:33–36); impl :186–216 idempotent (OnConflict DoNothing insert → lookup by `(tenant_id, source, event_id)` → re-quarantine update for non-applied rows; `ErrEventReceiptBusy` for a live lease) | ✅ Confirmed. |
| `check-audit-swallow.sh` P1/P2 alternations lack `QuarantineEvent` | ✅ Confirmed. P1 = `(Record|Log|Publish|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead|MarkDelivery)`; P2 = the same minus `Publish`. Both live swallow sites are invisible to the gate. |
| `recordAudit` helper (:150–157) with `// audit-site: standalone-fact` marker (:154) → `s.audit.Record(ctx, eventAuditInput(...))`; `eventAuditInput` (:159–171) anchors `EventID` on `envelope.EventID` (:162), `Action: "source.event"`, status in payload | ✅ Confirmed. |
| `AuditLedgerService.Record` maps `ErrAuditDuplicate` → success (audit_ledger.go:68–85) | ✅ Confirmed. |
| NATS mapping: `transientDeliveryError` (nats.go:102–110) `HTTPStatus() >= 500` → `Nak()` (redelivery), else `Term()` (JetStream DLQ) | ✅ Confirmed. `apperr.Internal` → HTTP 500 (errors.go:55–56) → Nak. |
| Wiring: `internal/app/services.go:133–137` — `NewEventIngestService(..., WithEventAllowedRegions, WithEventIngestAudit(deps.auditLedger), WithEventIngestMetrics)` | ✅ Confirmed. `s.audit` non-nil in production; the region path simply never calls it. |
| Test infrastructure: `event_ingest_residency_test.go` (`fakeReceiptRepo`), `audit_ledger_test.go` (`auditLedgerRepoSpy`), `event_order_integration_test.go` (`testpg`, `//go:build integration`), `scripts/testdata/audit-swallow/positive/fixture.go` | ✅ Confirmed, all present. |
| `event_ingest.go` is 170 lines (≤ 500 gate) | ✅ Confirmed (170). |
| Ledger dedup key `uq_audit_ledger_tenant_event UNIQUE (tenant_id, source, event_id)` (migrations/000050:15) | ✅ Confirmed. |
| Manifest row `internal/service/event_ingest.go|eventAuditInput|standalone-fact|-` (check-audit-manifest.sh:40); gate enforces per-file `site_count == token_count` and marker presence | ✅ Confirmed. |
| Receipt dedup key `uq_event_receipt_source_event UNIQUE (source, event_id)` in migrations/000033 | ⚠️ **Corrected (historical).** Migration 000050 drops that constraint and adds `uq_event_receipt_tenant_source_event UNIQUE (tenant_id, source, event_id)`. The *current* dedup behavior is tenant-scoped; the `QuarantineEvent` lookup already keys on `(tenant_id, source, event_id)`. Dedup semantics claimed by the spec are unaffected. |
| Only two `QuarantineEvent` call sites exist in the live tree (event_ingest.go:76, :120), plus the interface/impl declarations | ✅ Confirmed — makes the gate-extension provably safe post-fix. |

**Defect (both halves reproduce exactly as claimed):**

1. **Coverage gap.** A residency-policy violation quarantine (`event.region_not_allowed`) produces no durable audit fact, while the invalid-projection quarantine (`event.invalid_projection`) produces an *unconditional* one. AGENTS.md's audit-coverage invariant exempts nothing in event ingest.
2. **Swallow blind spot.** Both sites discard the receipt-write error with `_ =`, and the gate's P1/P2 alternations cannot see `QuarantineEvent`, so this swallow class is invisible to CI.

**Consequences:** (a) `quarantineInvalidProjection` records a `'quarantined'` ledger fact even when the receipt write failed — a false fact plus duplicate reprocessing on retry; (b) a failed region quarantine is durably lost with only a metric.

---

## 2. Problem statement

The ingest gateway is the last line of defense for source events: envelopes that must not touch local projections are quarantined (receipt row `status='quarantined'`, `error_code` set) rather than applied. Quarantine is a security-relevant rejection (residency policy, poisoned projection) and per AGENTS.md every such operation produces a durable audit fact. Today:

- the region-disallowed branch writes a durable receipt but **no** audit fact — a compliance gap;
- `quarantineInvalidProjection` writes the ledger fact regardless of whether the receipt write actually succeeded — a false fact that survives retries as if the quarantine were durable;
- both sites swallow the receipt-write error, and the gate that bans audit-write swallows (`check-audit-swallow`) cannot match `QuarantineEvent`, so the class regresses silently.

---

## 3. Design overview

Two coordinated changes, no schema or transport changes:

1. **Behavioral fix** in `internal/service/event_ingest.go`: both quarantine paths persist the receipt first (propagating errors as transient 5xx), then record an anchored `'quarantined'` audit fact (reusing the existing `recordAudit`/`eventAuditInput` helper — same marker, same dedup anchor), then return the deterministic 4xx. A failed receipt write records **no** fact.
2. **Gate fix** in `scripts/check-audit-swallow.sh`: add `QuarantineEvent` to both P1 and P2 alternations plus a positive fixture line, so the swallow class is provably live-scanned and the two converted sites are provably clean.

The dedup chain (receipt `ON CONFLICT DO NOTHING` + ledger `uq_audit_ledger_tenant_event`) is preserved untouched and makes redelivery converge to exactly one receipt row and exactly one ledger fact.

---

## 4. API changes

### 4.1 `internal/service/event_ingest.go` (the only code change)

No public/external API changes: no new protobuf, no new HTTP/gRPC routes, no new migration, no repository-interface change. `EventIngestService.Ingest` signature and all returned statuses are preserved.

**New stable error code** (per AGENTS.md stable-dotted-codes rule):

| Code | HTTP | gRPC | Meaning | NATS effect |
|---|---|---|---|---|
| `event.quarantine_failed` (new) | 500 | `Internal` | the quarantine receipt write failed; the event is **not** durably quarantined and no fact was recorded | `Nak()` → redelivery → at-least-once |
| `event.audit_failed` (existing; reused) | 500 | `Internal` | the quarantine is durable but its ledger fact failed to append | `Nak()` → redelivery → ledger dedup fold |
| `event.region_not_allowed` (existing) | 403 | `PermissionDenied` | deterministic residency rejection, quarantine+fact durable | `Term()` → JetStream DLQ |
| `event.invalid_projection` (existing) | 400 | `InvalidArgument` | deterministic poisoned-projection rejection, quarantine+fact durable | `Term()` → JetStream DLQ |

**Shared helper** (extracts the pattern both paths now follow; keeps the file well under the 500-line gate):

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

`recordAudit` already no-ops when `s.audit == nil` (unit tests without `WithEventIngestAudit`), so the helper is safe in both configurations.

**Region-disallowed branch** (today :74–79) becomes:

```go
if !s.regionAllowed(envelope.SourceRegion) {
	// P6-04: residency policy — a disallowed source region is quarantined,
	// never applied to local projections (data stays in its authority region).
	if err := s.quarantineEvent(ctx, envelope, "event.region_not_allowed"); err != nil {
		return nil, err // 5xx -> Nak/redelivery; no fact on receipt failure
	}
	s.metrics.ObserveEventQuarantined(envelope.Source + "." + envelope.EventType)
	return nil, apperr.Forbidden("event.region_not_allowed",
		"event source region violates residency policy")
}
```

**Invalid-projection path** (today :117–125) becomes:

```go
func (s *eventIngestService) quarantineInvalidProjection(ctx context.Context, envelope *event.Envelope, cause error) error {
	if err := s.quarantineEvent(ctx, envelope, "event.invalid_projection"); err != nil {
		return err // 5xx -> Nak/redelivery; false fact suppressed
	}
	return apperr.BadRequest("event.invalid_projection", "event cannot update an approved projection").Wrap(cause)
}
```

**Deliberate micro-behavior notes (document, don't fight):**

- `ObserveEventQuarantined` now fires only on the success path (after receipt + fact are durable). Previously it fired even when the write failed. The non-goal "no changes to metrics" is honored in the narrow sense — the call stays at both sites with the same key — but its semantics become "durable quarantine observed". This is the more accurate meaning and is called out for review.
- Fact payload for both paths is `eventAuditInput(envelope, nil, "quarantined")` — `mutation == nil`, exactly as today's invalid-projection path builds it. Anchors: `EventID == envelope.EventID`, `Action: "source.event"`, `status == "quarantined"`.
- The helper keeps exactly **one** bare `s.audit.Record(` site in the file (inside `recordAudit`), so `check-audit-manifest` needs no new row (see §6.4).

### 4.2 `scripts/check-audit-swallow.sh`

- Add `QuarantineEvent` to **both** alternations:
  - `P1_PATTERN='^[[:space:]]*(_ = |_, _ = )[A-Za-z_][A-Za-z0-9_.]*\.(Record|Log|Publish|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead|MarkDelivery|QuarantineEvent)\('`
  - `P2_PATTERN='^[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*\.(Record|Log|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead|MarkDelivery|QuarantineEvent)\('`
- Update the header comment enumerating P1/P2 members to mention `QuarantineEvent` (the swallow shape: `_ = s.receipts.QuarantineEvent(` — the error covers a durable receipt; discarding it silently drops the quarantine state and any audit coupling).
- **Positive fixture** in `scripts/testdata/audit-swallow/positive/fixture.go` (inside `func positive()`), e.g.:
  ```go
  _ = s.receipts.QuarantineEvent(ctx, envelope, "event.region_not_allowed") // P1: quarantine receipt swallow
  ```
  The `pos_hits` self-test (grep over `$FIXTURES/positive`) then proves the new alternation member is live. Optional but recommended: a P2 bare-call line `s.receipts.QuarantineEvent(ctx, envelope, "event.invalid_projection")`.
- **No** pattern-anchoring changes, **no** receiver exemptions, **no** root-list changes: after the fix both live sites are `if err :=` forms, so the extended patterns match 0 live lines (verified: the only `QuarantineEvent` occurrences in the tree are the two swallow sites, the interface declaration `QuarantineEvent(context.Context, *event.Envelope, string) error` — no leading dot, does not match P2 — and the impl `func (r *eventReceiptRepo) QuarantineEvent(` — `func` prefix, does not match). All existing pinned self-tests (FM-12 guards, breaker exemption, `*.pb.go` exclude, root membership, cmd fixture) are untouched and stay green.

### 4.3 No changes elsewhere

- `internal/repository/event_receipt.go` — untouched (idempotent `QuarantineEvent` is the required behavior).
- `internal/event/nats.go` / `envelope.go` / `redact.go` — untouched (5xx→Nak mapping already exists and is tested).
- `internal/app/services.go` wiring — untouched.
- No migrations; no proto regeneration.

---

## 5. Compatibility constraints

| Constraint | Guarantee |
|---|---|
| Deterministic statuses preserved | Successful quarantine returns exactly the pre-existing 403 (`event.region_not_allowed`) / 400 (`event.invalid_projection`). Nothing else in the ingest flow changes. |
| NATS ack semantics untouched | 4xx → `Term()`/DLQ, 5xx → `Nak()`/redelivery. The new 500 codes slot into the existing `transientDeliveryError` path (`>= 500` → Nak) with zero bus changes. |
| Receipt idempotency preserved | `QuarantineEvent` keeps `ON CONFLICT (tenant_id, source, event_id) DO NOTHING` + re-quarantine update for non-applied rows + `ErrEventReceiptBusy` for live leases. Redelivery converges to one receipt row. |
| Ledger dedup preserved | `recordAudit` re-derives the identical fact; `Append` folds on `uq_audit_ledger_tenant_event` → `ErrAuditDuplicate` → `Record` returns nil. Redelivery converges to one ledger row. |
| Gate semantics preserved | Same P1/P2 statement anchoring; only the alternation members grow. No exemptions, no root shrinkage. |
| Manifest invariant preserved | Exactly one bare `s.audit.Record(` in `event_ingest.go` ↔ exactly one manifest token (`eventAuditInput|standalone-fact|-`). |
| File-size gate | `event_ingest.go` goes 170 → ~185 lines (helper + error branches), well under the 400-line split trigger. |
| Nil-audit configs | Unit tests without `WithEventIngestAudit` still get the deterministic status after a successful quarantine (`recordAudit` no-ops on nil). |
| No behavior change to applied-path audit | The post-apply `recordAudit(..., applied)` path at :95 is untouched. |

---

## 6. Failure modes and convergence

| # | Failure | First attempt | Retry outcome | Terminal state |
|---|---|---|---|---|
| F1 | Receipt write fails, region path | 500 `event.quarantine_failed` → Nak/redelivery; **no** fact recorded | `QuarantineEvent` re-runs; on success records the fact | Receipt durable + exactly one fact, then 403 → Term |
| F2 | Receipt write fails, invalid-projection path | 500 `event.quarantine_failed` → Nak/redelivery; false fact **suppressed** (fixes today's false-fact defect) | Same as F1; on success records fact, then 400 → Term | Receipt durable + exactly one fact, then 400 → Term |
| F3 | Audit append fails after durable receipt (either path) | 500 `event.audit_failed` → Nak/redelivery | Receipt `DO NOTHING` (already quarantined); `recordAudit` re-derives identical fact → `ErrAuditDuplicate` fold | Exactly one ledger row; deterministic 4xx → Term |
| F4 | Redelivery of an already-quarantined envelope (pre-existing receipt) | Receipt lookup: row is `quarantined`, not applied → idempotent re-quarantine update (no-op); fact re-recorded → dedup fold | — | One receipt row, one fact, deterministic 4xx |
| F5 | Busy lease (`ErrEventReceiptBusy`) during quarantine | Mapped to 500 (`event.quarantine_failed`) → Nak/backoff, same as `mapEventApplyError` treats busy as retryable | After lease expiry the re-quarantine path proceeds | Converges as above |
| F6 | Process crash between receipt commit and audit append | Receipt durable, no fact | Redelivery re-derives and folds the fact | Exactly one ledger row (no lost fact — at-least-once + dedup) |
| F7 | Poison event with an envelope that fails `Validate()` | Rejected before quarantine at `validateIngestEnvelope` (:59–67) with deterministic 4xx — unchanged, **not** in scope | — | `Term()`/DLQ, no receipt, no fact (pre-existing behavior) |

Timeout is never treated as proof of failure: every 5xx path above redelivers with the same EventID and relies on the two durable dedup keys, matching the AGENTS.md "persist unknown and reconcile by idempotency key" rule without a new mechanism.

**Invariant maintained:** a ledger `'quarantined'` fact for a given envelope exists **iff** its receipt row is durable. The false fact (receipt failed, fact recorded) is eliminated.

---

## 7. Migration steps

No data migration. The code + gate changes are atomic — the extended P1/P2 patterns are **red on today's tree** (the two `_ =` sites would match), so the gate change and the code change must land in the same commit (and the positive fixture with them):

1. **Edit** `internal/service/event_ingest.go`: add the `quarantineEvent` helper; convert the region branch (:74–79) and `quarantineInvalidProjection` (:117–125) to the error-propagating forms in §4.1. Keep `event.quarantine_failed` / `event.audit_failed` codes, marker comment, and the single bare `s.audit.Record(` site.
2. **Edit** `scripts/check-audit-swallow.sh`: extend P1/P2 with `QuarantineEvent`; update the header member enumeration; add the positive fixture line(s) in `scripts/testdata/audit-swallow/positive/fixture.go`.
3. **Add tests** (§8): unit tests in `internal/service/` (extend `event_ingest_residency_test.go` / add sibling file); integration test in `internal/repository/` (sibling of `event_order_integration_test.go`, `//go:build integration`).
4. **Run the acceptance mapping** (§9) in order; the done-gate last.
5. No rollout ordering concerns beyond the atomic commit: old binaries and new binaries can coexist (wire format unchanged), but CI is red in between, so land as one change-set.

---

## 8. Testable acceptance mapping

### 8.1 Unit tests (`make test`, no Docker) — `internal/service/`

Extend `fakeReceiptRepo` with a `quarantineErr error` field (`QuarantineEvent` returns it when set, still records the code). Wire `NewAuditLedgerService` over the existing `auditLedgerRepoSpy` and pass via `WithEventIngestAudit`. Envelope helpers already exist (`testEnvelope`); an invalid-projection envelope is one that passes envelope `Validate()` (payload non-nil, valid source/region/type/aggregate) but fails `Snapshot()` — e.g. payload missing `dataset` or `account_id` (envelope.go:121–148).

| REQ | Test | Assertion |
|---|---|---|
| REQ-1 | Region-disallowed with successful receipt | Exactly **one** fact: `Action == "source.event"`, payload `status == "quarantined"`, `EventID == envelope.EventID` (anchored), `TenantID`/`Source`/`ResourceID` propagated; `AppError.HTTPStatus() == 403`; `error_code` recorded once. |
| REQ-2 | Region-disallowed with `quarantineErr != nil` | `AppError.HTTPStatus() >= 500`, code `event.quarantine_failed`; spy appended **zero** facts; receipt code still recorded. |
| REQ-2 | Invalid-projection success | One anchored `'quarantined'` fact; `HTTPStatus() == 400`. |
| REQ-2 | Invalid-projection with `quarantineErr != nil` | `HTTPStatus() >= 500`; **zero** facts (false-fact suppression); no `BadRequest`. |
| REQ-3 | Redelivery dedup | Spy `appendErr = repository.ErrAuditDuplicate` on the second `Record` → `Ingest` returns nil error + deterministic status; assert every append attempt carries `EventID == envelope.EventID`. |
| REQ-6.6 | Existing residency/apply tests | All current tests stay green (nil-audit configs still return deterministic statuses). |

No NATS coupling in unit tests: the 5xx→Nak mapping is already pinned by `internal/event/nats_test.go` (`transientDeliveryError`); no change.

### 8.2 Integration tests (`make test-integration`, Docker via `testpg`)

Sibling of `event_order_integration_test.go` (`//go:build integration`): drive `NewEventIngestService` over `NewEventReceiptRepository(db, ledger)` + `NewAuditLedgerRepository(db, config)`.

| REQ | Scenario | Assertion |
|---|---|---|
| REQ-7.1 | Region-disallowed (`WithEventAllowedRegions` excluding the envelope region) | Exactly one `event_receipts` row (`status='quarantined'`, `error_code='event.region_not_allowed'`) **and** exactly one `audit_ledger` row for `(tenant_id, source, event_id)` with action `source.event` and payload status `quarantined`. |
| REQ-7.2 | Invalid projection (envelope whose `Snapshot()` fails) | One `event_receipts` row with `error_code='event.invalid_projection'` **and** one ledger row. |
| REQ-7.3 | Duplicate fold: redeliver the same EventID | Still exactly one `event_receipts` row and one `audit_ledger` row for the key; `Ingest` returns the deterministic status (403/400) with nil error. |

### 8.3 Gate tests

| REQ | Gate | Green condition |
|---|---|---|
| REQ-4 | `bash scripts/check-audit-swallow.sh` | Exit 0: positive fixture hit ≥ 1 (self-test exercises the new `QuarantineEvent` member), live-tree matches == 0, negative fixtures == 0, all pinned self-tests green. **Negative check:** reverting either live site to `_ = s.receipts.QuarantineEvent(` turns the gate red. |
| REQ-5 | `bash scripts/check-audit-manifest.sh` | Exit 0: `event_ingest.go` has exactly 1 bare `s.audit.Record(` site matching exactly 1 manifest token (`eventAuditInput|standalone-fact|-`), marker `audit-site: standalone-fact` present. |
| REQ-8 | `make lint && make lint-strict && make check-file-sizes && make build` | Green; `event_ingest.go` ≤ 500 lines (≈185 expected). |

### 8.4 Full acceptance

1. `make test` — green with §8.1 tests (anchored `'quarantined'` fact for `region_not_allowed`; `QuarantineEvent` failure → 5xx with zero facts; same-EventID retry dedups via `ErrAuditDuplicate`).
2. `make check-audit-swallow` — green with `QuarantineEvent` in P1/P2, positive fixture matched, negative fixtures at 0, all pinned self-tests green.
3. `make check-audit-manifest` — green; one bare site ↔ one token.
4. `make test-integration` — green with §8.2 row-level assertions (both quarantine paths + duplicate fold).
5. `make lint && make lint-strict && make check-file-sizes && make build` — green.
6. Full done-gate (`make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-load-scenarios && make check-migration-set && make check-composition-root && make check-proto-sync && make check-integration-compile && make build`) — green.

---

## 9. Non-goals (unchanged from the direction)

- No changes to NATS ack/redelivery semantics, envelope validation, redaction, the snapshot-apply path, metrics keys, or any other direction item.
- No new repositories, connectors, handlers, migrations, or protobuf surfaces.
- No change to the deterministic 403/400 statuses on success paths.
- `Validate()`-level poison events (F7) stay rejected at `validateIngestEnvelope` — out of scope.

---

## 10. Risks

- **Gate/code atomicity (low):** the extended patterns redden today's tree, so a partial landing breaks CI. Mitigated by the single change-set rule (§7).
- **Metric ordering (low):** `ObserveEventQuarantined` now fires only for durable quarantines. Dashboard drift on transient receipt failures only; deliberate and documented (§4.1).
- **Error-code surface (low):** `event.quarantine_failed` is a new stable dotted code. It is consumed only by the NATS error mapping (Nak) and by HTTP/gRPC handlers that already propagate `AppError` verbatim (`httpx.WriteError`, `grpcError`) — no client contract change because the ingest endpoints were already capable of 5xx (`event.apply_failed`, `event.audit_failed`).
- **File growth (low):** ~15 added lines; no split needed now; re-check at 400 per AGENTS.md.
