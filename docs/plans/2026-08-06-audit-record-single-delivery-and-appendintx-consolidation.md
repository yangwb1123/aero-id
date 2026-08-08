# Design — Audit write-path consolidation: single delivery, in-tx append, swallow ban

| | |
|---|---|
| Source | `docs/requirements/2026-08-06-configs-audit-record-single-delivery-and-appendintx-consolidation.md` |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery + swallowed-call ban) |
| Status | **REVISED** — every evidence claim independently re-verified against the live tree (2026-08-06); adversarial-review findings C1, C2, A5, F1, F2–F5, D, E, B1 folded in below |
| Done gate | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build` + `make test-integration` |

## 0. Evidence verification verdict

Every cited symbol in the requirement was re-checked against the live tree. **All E1–E9 and N1–N5
claims are confirmed exact.** Four additional facts (A1–A4) found in the first review pass and **one
new fact (A5, backfill) from the coverage review** shape the design.

| # | Claim | Verdict (live-tree evidence) |
|---|---|---|
| E1 | `Record` = standalone-tx `Append` + inline swallowed `Publish` loop | ✅ `internal/service/audit_ledger.go` — `Record` :66, `s.repo.Append(ctx, fact)` :67, `ErrAuditDuplicate → return nil` :70-72, `ObserveAuditIngestFailure` :73-75, `for _, publisher := range s.publishers` :77-78, `_ = publisher.Publish(ctx, fact)` :79 |
| E2 | `drainPublications` claims `audit_publications` | ✅ `internal/service/callback_audit.go` — :47; `ClaimPublication(ctx, s.worker, 5*time.Minute, 50)` :52-54; `finishPublicationAttempt` :61-77; `publishAuditFact` :97-113 |
| E3 | Worker ticker invokes `drainPublications` | ✅ `internal/service/callback.go:267` — `s.drainPublications(workerCtx)` then `s.drain(workerCtx)` in `Start` |
| E4 | Six swallowed `_ = s.audit.Record(` sites | ✅ `account.go:155` (`account.viewed`), `:184` (`account.listed`); `account_provision.go:168` (`account.provisioned`); `callback.go:160` (`callback.registered`), `:221` (`callback.deleted`), `:247` (`callback.delivery.replayed`) — all exact, all `_ = s.audit.Record(ctx, ...)` |
| E5 | `AppendInTx` exists; both rows atomic | ✅ `internal/repository/audit_ledger.go` — interface :38-40; `Append` (own `Transaction`) :107-121; `AppendInTx` :123; `appendInTx` :140; `createAuditRows` :189-201 creates ledger row + `status='pending'` publication row in same tx |
| E6 | In-tx append precedents | ✅ `event_receipt.go:180` (`AppendInTx` ignoring `ErrAuditDuplicate`); `account_profile.go:101` (`return r.audit.AppendInTx(...)` at end of profile-update tx) |
| E7 | Operation `Record` fallback is dead code | ✅ `internal/service/operation_audit.go` — type-assert fallbacks in `finishStep` :42, `setOperationStatus` :71, `finishCompensationStep` :86 → `recordOperationAudit` :89-95 → `s.audit.Record(...)` :94; `internal/repository/operation.go` implements `CreateWithAudit` :135, `FinishStepWithAudit` :417, `FinishCompensationStepWithAudit` :430, `SetStatusWithAudit` :448 |
| E8 | No Makefile audit gate | ✅ Makefile `audit` lines are only the proto enumeration (:58) and a soak comment (:114) |
| E9 | One publication row per fact; atomic with ledger | ✅ `createAuditRows` (:189-201); `migrations/000045_audit_publications.up.sql` — `fact_id uuid NOT NULL REFERENCES audit_ledger(id) ON DELETE CASCADE`, `CONSTRAINT uq_audit_publication_fact UNIQUE (fact_id)` |
| N1 | Inline loop unwired in production; double delivery latent | ✅ `internal/app/new.go:146` `NewAuditLedgerService(auditLedgerRepo)` — zero publishers; `configs/config.yaml:174` `audit_governance.enabled: false`; `buildAuditPublishers` (`internal/app/audit.go:38`) gated on that flag; publishers wired only into callback service via `wireAuditServices` (`audit.go:17-31`); `callback_audit.go:19-25` nil-publication guard |
| N2 | `account.viewed`/`listed` are read-path facts with no account-row write | ✅ `GetAccount` (`account.go:155` region) and `ListAccounts` (`:184`) perform no account-row write; only `account.provisioned` has a joinable write |
| N3 | Gate flags 4 extra same-class sites | ✅ `internal/service/audit_hold.go:59` (`audit_hold.set`), `:74` (`audit_hold.removed`); `internal/service/legacy_cutover.go:88` (`legacy.cutover.set`); `internal/service/callback_audit.go:81` (`_ = s.publications.FinishPublication(...)`) — all four confirmed |
| N4 | Fail-closed wiring precedent | ✅ `requireAuditLedger` (`internal/repository/audit_wiring.go`) panics on nil/typed-nil; `NewAccountRepository` (`repository/account.go:48-49`); `NewCallbackRepository` (`repository/callback.go:36`) and `NewAuditHoldRepository` (`repository/audit_hold.go:66`) take no ledger today |
| N5 | Test infrastructure exists | ✅ `audit_wiring_integration_test.go` — `countAuditLedgerRows` :39, `TestAuditWiringHappyPath` :51, `TestAuditWiringRollbackAtomicity` :163 (rollback via duplicate `event_id` → `ErrAuditDuplicate`); `audit_ledger_test.go:47` `TestAuditLedgerFailClosedWithoutSigningKey` and `audit_ledger_integration_test.go:225` `TestAuditLedgerAppendAndVerifyFailClosedWithoutKey` prove the signing-key fail-closed mechanism used as the new rollback injection (C2) |

### Additional verified facts (A1–A5)

- **A1 — eight non-swallowed `Record` call sites remain and are unaffected.** `operation.go:136`,
  `event_ingest.go:154`, `replay.go:145,240`, `sync_job.go:163,467`, `account_profile.go:62`, and the
  `Log` delegate (`audit_ledger.go:143`). All eight use `if err :=` / `return ...Record(` — verified
  non-swallowed. `Record` is **not** deleted by REQ-1 — it stays as the standalone-append API for facts
  without a business transaction; only its publisher loop is removed.
- **A2 — `account.profile.updated` already uses the dual-path pattern** (`account_profile.go:50-66`:
  `atomicAudit` chooses `UpdateProfileWithAudit` or plain `UpdateProfile` + fallback `Record`), the same
  latent-fallback shape as E7. REQ-5 removes only the operation fallback; the profile fallback is
  out-of-scope but must not regress (its `Record` call is already non-swallowed).
- **A3 — the metered provisioning path needs its own WithAudit variant.** `EnsureByIdentityWithQuota`
  (`internal/repository/account_quota.go:38-51`) opens its **own** transaction wrapping
  `ensureAccountIdentityTx` + `ensureAccountQuotaTx`. REQ-3's table only names `EnsureByIdentity`
  (`account_identity.go:20`); without an `EnsureByIdentityWithQuotaWithAudit` variant the metered path
  keeps the standalone append and the orphan hazard.
- **A4 — the created-signal exists at the row level.** `bindAccountIdentity`
  (`account_identity.go:75-92`) returns the new account only when `insert.RowsAffected == 1`; otherwise
  `useExistingAccountIdentity` deletes the candidate and returns the existing account. The in-tx fact
  append must be gated on this signal so a repeat `ensure` (which touches `last_seen_at`) does not hit
  `ErrAuditDuplicate` inside its own tx and roll back the touch.
- **A5 — the admin backfill endpoint mutates without any audit fact (new finding).**
  `internal/service/account_backfill.go:97` (`backfillOne`) calls plain `EnsureByIdentity` on the live,
  permission-guarded admin endpoint `POST /v1/admin/accounts/backfill/legacy-identities`
  (`handler/http/account.go:53-57`, `AccountManagePermission`, wired at `new.go:409`-region
  `NewAccountHandlerWithExtensions`). It records **no fact at all** — not in A1, not a swallow, a
  missing fact on a state-changing administrative path. Converted to `EnsureByIdentityWithAudit`
  (§2.4) with `account.provisioned` and A4 gating; repeat-ensure and concurrent-provision coverage in
  §6.

## 1. Problem statement (confirmed)

All account-domain audit facts flow through `AuditLedgerService.Record`, which appends in a **standalone
transaction** (E1) and then runs a synchronous inline publisher loop with swallowed errors, while
`drainPublications` concurrently claims and delivers the same fact's publication row (E2/E3). The six
named sites swallow `Record`'s error (E4), so a business mutation can commit while its audit append fails
(fact lost), and an audit append can commit while the business mutation rolls back (orphan ledger +
publication rows; E9 — the two rows commit atomically *with each other* but not with the mutation). The
in-tx pattern that fixes this is proven (`AppendInTx` + `XxxWithAudit`, E5/E6, fail-closed N4) but the
account/callback surfaces do not use it, the operation service keeps a latent second path (E7), no gate
enforces single delivery or bans swallowed audit calls (E8), and the admin backfill writes without any
fact (A5). Double delivery is **latent** (N1): production wires zero publishers into
`NewAuditLedgerService`; the **live** defects are the standalone-tx append (orphan rows), the six
swallowed errors, and the missing backfill fact.

## 2. API changes

### 2.1 Service layer — `internal/service/audit_ledger.go`

- **Delete** the `publishers []AuditPublisher` field, the inline `for _, publisher := range s.publishers`
  loop (:77-78, :79 swallow), and the variadic `publishers ...AuditPublisher` parameter of
  `NewAuditLedgerService` (:54). New signature: `NewAuditLedgerService(repo repository.AuditLedgerRepository) AuditLedgerService`.
- **Keep** `Record`'s behavior otherwise: `Append` (standalone tx), `ErrAuditDuplicate → return nil`,
  `ObserveAuditIngestFailure`, and the `Record`/`Log`/`List`/`VerifyChain` interface unchanged.
- **Keep** the `AuditPublisher` interface and `callbackService.SetAuditPublishers` — they serve
  `publishAuditFact` on the drain path (`callback_audit.go:97-113`), which is the only delivery path
  after this change.
- Compile-checked removal: verified zero call sites pass publishers (`new.go:146`,
  `audit_integration_test.go:19`, `audit_ledger_test.go:43,56`).

### 2.2 Repository layer — new WithAudit variants (mirror `operation.go:135,417,430,448`)

New **separate interfaces** (pattern precedent: `AccountProfileAuditRepository` at `account.go:39-41`,
`OperationAuditRepository`/`OperationStateAuditRepository` at `operation.go:35-43`). The existing
`CallbackRepository` and `AccountRepository` interfaces are **unchanged**, so existing fakes keep
compiling; the service type-asserts (see §2.4) exactly like `account_profile.go:50-53`.

```go
// internal/repository/callback_audit.go (new file — size gate, constraint 8)
type CallbackAuditRepository interface {
    CreateSubscriptionWithAudit(ctx context.Context, value *domain.CallbackSubscription, fact *domain.AuditFact) error
    DeleteSubscriptionWithAudit(ctx context.Context, owner, tenantID string, id domain.ID, fact *domain.AuditFact) error
    ResetDeliveryWithAudit(ctx context.Context, id domain.ID, fact *domain.AuditFact) error
}

// internal/repository/account_provision_audit.go (new file)
type AccountProvisionAuditRepository interface {
    // accountID is pre-assigned by the service (callback.go:74-76 ID-when-empty precedent); the
    // repo threads it into ensureAccountIdentityTx and appends the fact ONLY when created.
    EnsureByIdentityWithAudit(ctx context.Context, tenantID, issuer, subject, canonicalUID, homeRegion string,
        accountID domain.ID, fact *domain.AuditFact) (*domain.Account, error)
    EnsureByIdentityWithQuotaWithAudit(ctx context.Context, tenantID, issuer, subject, canonicalUID, homeRegion,
        reservationID string, accountID domain.ID, fact *domain.AuditFact) (*domain.Account, error)
}
```

Mechanics, per method:

1. Open `r.db.WithContext(ctx).Transaction(...)`.
2. Perform the existing domain write inside `tx` (refactor the body of the existing method into a
   `...Tx(tx, ...)` helper so the plain method and the `WithAudit` variant share it).
3. When the write actually happened, call `r.audit.AppendInTx(ctx, tx, fact)`; **propagate its error**
   (a non-nil error rolls back the whole tx — REQ-4). `ErrAuditDuplicate` is propagated as-is (the
   service maps it; see §2.4).
4. `tx.Commit` via the `Transaction` callback return.

Per-method specifics:

- **Account variants (A3/A4/D):** refactor `ensureAccountIdentityTx` (`account_identity.go:66-74`) to
  **return `(account *domain.Account, created bool, err error)`** — the created signal is the crux of
  A4/A3 and is part of the shared helper's contract. Both `EnsureByIdentityWithAudit` and
  `EnsureByIdentityWithQuotaWithAudit` consume it; the pre-assigned `accountID` is threaded as the
  candidate row ID (zero value → `idgen.NewID()` for the plain callers). Append only when `created`
  (`bindAccountIdentity` `RowsAffected == 1`); the loser's tx takes the existing-identity path
  (touch `last_seen_at`, delete its candidate) with **no append**. The quota variant appends after
  `ensureAccountQuotaTx` in the same tx.
- **`CreateSubscriptionWithAudit`:** insert the subscription (ID-when-empty preserved for plain
  callers; the service pre-assigns, §2.4); append on insert success — no gate needed (no ON CONFLICT).
- **`DeleteSubscriptionWithAudit`:** append **only when the delete's `RowsAffected == 1`** (matches the
  `ErrNotFound` short-circuit at `callback.go:121-124` — "only when a row was deleted").
- **`ResetDeliveryWithAudit`:** append **only when the reset UPDATE's `RowsAffected == 1`** (A4-style
  symmetry; the service's `GetDelivery` pre-check already makes the phantom case unreachable, but the
  gate prevents an orphan fact if a delivery is deleted between check and reset).
- **Facts are never cached/reused:** `appendInTx` mutates the fact in place (ID, chain sequence,
  hashes — `audit_ledger.go:150-165`); each WithAudit call must pass a freshly built fact from the
  service input (reviewer note D).

Constructor changes (fail-closed, N4 precedent):

- `NewCallbackRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) CallbackRepository` —
  calls `requireAuditLedger("NewCallbackRepository", audit)`. Verified blast radius: production call
  site is only `new.go:135`; **no test file constructs `NewCallbackRepository`**.
- `accountRepo` already takes the ledger via `NewAccountRepository` (fail-closed) — the two new
  `accountRepo` methods need no constructor change.

The plain non-audit methods (`CreateSubscription`, `DeleteSubscription`, `ResetDelivery`,
`EnsureByIdentity`, `EnsureByIdentityWithQuota`) remain for compatibility (tests, callers that
intentionally skip audit).

**C1 fix — the unique constraint becomes a true final guard (new).** `checkAuditDuplicate`
(`audit_ledger.go:171-182`) is a pre-check; `uq_audit_ledger_tenant_event UNIQUE (tenant_id, source,
event_id)` (migration 000050) is the final guard. Two **concurrent** appends of the same triple with
**different `partition_key`s** take different `pg_advisory_xact_lock(hashtext(partition_key))` locks
(`audit_ledger.go:145`), so the loser's pre-checks cannot see the winner's uncommitted row; the loser's
INSERT then blocks on the global unique index and fails with a raw `23505`. Verified: nothing in
`internal/` maps it today — the service then 500s instead of succeeding. Fix, inside `createAuditRows`
(`audit_ledger.go:189-201`) **at the ledger-insert error site only**:

```go
if err := tx.Create(auditLedgerFactToRow(fact, payload)).Error; err != nil {
    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_audit_ledger_tenant_event" {
        return ErrAuditDuplicate
    }
    return fmt.Errorf("append audit ledger: %w", err)
}
```

- GORM's driver translation (`gorm.io/driver/postgres@v1.5.9` maps 23505 → `gorm.ErrDuplicatedKey`) is
  gated on `db.Config.TranslateError`, which is **not enabled anywhere** (`internal/pkg/db/db.go:21` —
  verified), so the raw `*pgconn.PgError` (pgx v5) surfaces. Add `github.com/jackc/pgx/v5/pgconn` as a
  direct go.mod dependency (already in the module graph via the driver). Defensive fallback: also map
  `errors.Is(err, gorm.ErrDuplicatedKey)` at the ledger site if `TranslateError` is ever enabled — safe
  positionally, since the only reachable 23505 on the ledger insert is `uq_audit_ledger_tenant_event`
  (`uq_audit_ledger_chain_seq` collisions are impossible under the partition advisory lock).
- **Do not** map the publication-insert site (`createAuditRows` second `tx.Create`): a
  `uq_audit_publication_fact` violation there is internal corruption and must stay raw.
- Side effect worth noting: `event_receipt.go:180` already ignores `ErrAuditDuplicate` from
  `AppendInTx`; the C1 mapping closes the same latent 500 on that path under concurrency.

### 2.3 Operation service — `internal/service/operation_audit.go`

- **Delete** `recordOperationAudit` (:89-95) and the three fallback tails: `finishStep` :42 region,
  `setOperationStatus` :71 region, `finishCompensationStep` :86 region.
- Replace each fallback with fail-closed semantics: if `s.audit != nil` **and** `s.repo` does not
  implement the required `WithAudit` interface, return
  `apperr.Internal("operation.audit_unavailable", "operation audit persistence is unavailable")` — never
  a silent standalone append. If `s.audit == nil`, keep the plain repo call (audit deliberately
  disabled wiring, unchanged).
- `operation.go:136` (`operation.requested` via `persistCreatedOperation`) keeps its non-swallowed
  fallback `Record` — it is the one fact whose tx (operation creation) already has `CreateWithAudit`
  with a non-swallowed fallback; out of the direction's scope, unchanged. Note: this is the same shape
  as A2; a follow-up may convert it, but it is not a gate match and not a swallow.

### 2.4 Service-layer conversions of the six sites + N3 sites + A5

Write-path sites (join the tx — call the WithAudit repo method, pass the fact built with the same
`AuditFactInput` content as today). All five use the **type-assert pattern** of
`account_profile.go:50-53`; the repo constructor guarantees the interface in production, so a failed
assert is a test-wiring error and returns a fail-closed error (never a silent plain write):

| Site | Fact | Service change |
|---|---|---|
| `account_provision.go:168` (both metered :116-123 and unmetered :127-134 callers) | `account.provisioned`, `EventID: accountID` | **Pre-assign** `accountID := idgen.NewID()` in the service, build the complete fact (EventID/AccountID = accountID, PartitionKey normalizes to `tenant:accountID` via `buildAuditFact`, payload `home_region` — exactly today's `recordAccountProvisioned` content), then call `EnsureByIdentityWithAudit` (unmetered) / `EnsureByIdentityWithQuotaWithAudit` (metered); delete `recordAccountProvisioned`. `ErrAuditDuplicate` → return success (defense-in-depth; unreachable due to A4 gating). |
| `callback.go:160` | `callback.registered`, `EventID: subscription.ID` | **Pre-assign** `subscription.ID = idgen.NewID()` in the service before building the fact (the repo's ID-when-empty precedent at `callback.go:74-76` makes this safe); call `CreateSubscriptionWithAudit`. |
| `callback.go:221` | `callback.deleted`, `EventID: id` | Call `DeleteSubscriptionWithAudit`; `ErrNotFound` → 404 (unchanged); `ErrAuditDuplicate` → success (defense; doubly unreachable — a retried delete hits `RowsAffected==0 → ErrNotFound` before any append). |
| `callback.go:247` | `callback.delivery.replayed`, **`EventID: idgen.NewID()`** (C2) | Call `ResetDeliveryWithAudit`. **C2 fix:** fresh EventID per replay (precedent: `operationAuditFact` at `operation_audit.go:45-49`), and **drop the dup→success mapping for replay** — any error (including `ErrAuditDuplicate`) → `apperr.Internal("callback.delivery_replay_failed", ...)`. Rationale: with `EventID: id` (old design), replay #2 of a re-failed delivery hit the dup inside the tx, rolled back **its own reset**, and the service mapped the error to success — a silent no-op where the user's new intent (re-reset) never applied. With a fresh EventID every replay commits its reset and appends its own fact. |
| `account_backfill.go:97` (A5) | `account.provisioned`, `EventID: accountID` | Same pre-assignment + fact build as the provision path (`ActorUID: uid`, `TenantID: s.backfillTenant`, payload `home_region`); call `EnsureByIdentityWithAudit`. Errors → `result.Skipped++` (unchanged run semantics). The `FindByIdentity` pre-check (:76-84) already short-circuits repeat runs, so repeat-ensure appends nothing at the service level; the repo's A4 gate covers the concurrent race. |

The `s.audit != nil` guards at `callback.go:156,219,245` become vestigial (the repo owns the ledger) —
**remove them in step 4** along with the now-unused `callbackService.audit` field and
`SetAuditLogger` + its `wireAuditServices` block (`internal/app/audit.go:21-25`; internal symbols
only). The callback service keeps `SetAuditPublicationRepository`/`SetAuditPublishers` (drain path).

Read-path sites (no tx to join, N2 — non-swallow only):

| Site | Fact | Service change |
|---|---|---|
| `account.go:155` | `account.viewed` | Replace `_ = s.audit.Record(...)` with explicit handling: on error, `return nil, apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)`. |
| `account.go:184` | `account.listed` | Same conversion (`audit.ingest_failed`, 503). |

N3 sites (gate-driven follow-through, same mechanical change; admin facts — fail closed):

| Site | Conversion |
|---|---|
| `audit_hold.go:59` (`audit_hold.set`) | `if err := s.audit.Record(...); err != nil { return nil, apperr.Internal("audit_hold.audit_failed", ...).Wrap(err) }` |
| `audit_hold.go:74` (`audit_hold.removed`) | Same → `apperr.Internal("audit_hold.audit_failed", ...)` |
| `legacy_cutover.go:88` (`legacy.cutover.set`) | Same → `apperr.Internal("cutover.audit_failed", ...)` |
| `callback_audit.go:81` (`FinishPublication` swallow) | `if err := s.publications.FinishPublication(...); err != nil { s.metrics.ObserveAuditPublicationFinishFailure(status, err) }` — **no retry**: on `ErrAuditPublicationLeaseLost` the row is re-claimed after lease expiry (at-least-once); on DB error the lease expires and the row is re-claimed. The lease-guarded `UPDATE ... WHERE id=? AND status='delivering' AND lease_owner=?` (`audit_ledger.go:251-259`) already prevents double-finish. |

New metric: add `ObserveAuditPublicationFinishFailure(status string, err error)` to
`ControlPlaneMetrics` (`internal/service/control_plane_metrics.go:15`), the `noopControlPlaneMetrics`
(:29), `RegistryControlPlaneMetrics` (:55), and the underlying `metrics.Registry`
(`internal/pkg/metrics/control_plane.go` — the adapter must forward; the registry method is new). The
interface change is additive; verified only two implementations exist.

**Composition-root hardening (FM-8):** validate `cfg.AuditLedger.SigningKey` is non-empty in
`internal/app` **before** `NewAuditLedgerRepository` (`new.go:134`); fail startup with an explicit
error. Today an empty key turns every mutation into a runtime failure; AGENTS.md mandates startup
config validation and this is the canonical case. The dev placeholder (`configs/config.yaml:172`
`change-me-please-very-long-audit-key`) is non-empty, so the out-of-box config still boots.

### 2.5 Gate — `make check-audit-swallow`

New script `scripts/check-audit-swallow.sh [ROOT]` (precedent: `scripts/check-architecture.sh`, wired
at `Makefile:26-28`):

```
P1 (assign-swallow): grep -rnE '^[[:space:]]*(_ = |_, _ = )[A-Za-z_][A-Za-z0-9_.]*\.(Record|Publish|FinishPublication|ClaimPublication)\(' "$ROOT" --include='*.go'
P2 (bare-call):      grep -rnE '^[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*\.(Record|FinishPublication|ClaimPublication)\(' "$ROOT" --include='*.go'
```

- `ROOT` defaults to `internal/service`. **The script first runs a self-test against
  `scripts/testdata/audit-swallow/`** (positive fixture must match ≥1, negative fixture must match 0),
  then scans `$ROOT`; exits non-zero on any match. The fixture therefore **lives outside the scanned
  tree** (`scripts/testdata/audit-swallow/{positive,negative}/`) and `make check-audit-swallow` never
  scans it (F4).
- **P2 closes the bare-statement false-negative hole (F3):** `s.audit.Record(ctx, f)` as a bare
  statement discards the error and matches nothing today (errcheck is not enabled in `.golangci.yml` —
  verified). P2's anchoring (`^[[:space:]]*`) naturally excludes `if err :=`, `return`, `err :=`,
  `defer`, `go`, and comment lines (`//`, `*`) — the F5 comment concern is solved by anchoring alone.
  `Publish` is deliberately **not** in P2: `notification.go:281,294` bare `d.pusher.Publish(...)` calls
  are the best-effort SSE hub (AGENTS.md), not audit calls; a future bare audit `Publish` swallow is
  still caught by P1's `_ =` form.
- Exclusions (documented in the script header): calls to `Log(` (legacy diagnostics surface, per
  AGENTS.md); the unwired parallel tree `internal/{file,message,comment,block,notification,search,
  audit}` (checked by only scanning `internal/service/`).
- **Baseline (F2, corrected):** **11 matches on today's tree** — the six E4 sites, the three `Record`
  N3 sites, the `FinishPublication` N3 site, and `audit_ledger.go:79` (`_ = publisher.Publish`, the
  inline loop). **10 after step 1 lands; 0 after step 4.** The step-5 "10-match baseline" is a
  post-step-1 measurement (§5).
- Makefile: add `check-audit-swallow` to `.PHONY` and a target; update the done-gate line in AGENTS.md
  (step 5).

## 3. Compatibility constraints

1. **`Record` semantics change from "append + deliver" to "append only".** Production wiring passes zero
   publishers (N1), so no live delivery behavior changes; tests that constructed the service with
   publishers must be updated (verified: none do). The enqueue-only contract is the point of REQ-1.
2. **`Record`, `Log`, `AuditLedgerService`, `AuditPublisher`, `SetAuditPublishers`, and migration 000045
   are unchanged** (except `NewAuditLedgerService`'s variadic parameter, an internal symbol). The 8
   non-swallowed `Record` callers (A1) keep compiling and behaving as today.
3. **Repo constructor change is internal and low-blast-radius.** `NewCallbackRepository` gains one
   parameter; production call site is `new.go:135` only, no test call sites (verified). `new.go` passes
   `auditLedgerRepo` (which implements `FullAuditLedgerRepository` ⊇ transactional interface) — the
   same value already passed to `NewAccountRepository` (`new.go:162`), `NewOperationRepository`
   (`:170`), and `NewSyncJobRepository` (`:171`). The panic path is unreachable in production wiring
   (the ledger is constructed unconditionally at `new.go:134`, regardless of `audit_governance.enabled`).
4. **Dedupe/duplicate semantics preserved — with the C1 and C2 corrections.** (a) Sequential retries:
   `ErrAuditDuplicate` from a WithAudit method → service success for `account.provisioned` and
   `callback.deleted` (retried idempotent commands stay idempotent); repo-level integration tests still
   observe the raw error (rollback proof). (b) **C1:** concurrent cross-partition dups now surface as
   `ErrAuditDuplicate` (23505 mapping, §2.2) instead of a 500 — same success mapping applies. (c) **C2:**
   replay facts get a fresh EventID and **no** dup→success mapping; a second replay is a new user
   intent and re-applies the reset with its own fact.
5. **`account.provisioned` timing on the metered path shifts earlier** (A3/A5): the fact now commits with
   the account-creation tx instead of after the external billing quota commit. This is a deliberate
   improvement — previously a quota-commit failure after account creation silently lost the fact. The
   fact's meaning is "account created" (it already fires for the unmetered path where no quota exists).
   The pending-saga completion path (`completePendingAccountQuota` → `commitAccountQuota` for an
   existing account) appends nothing (created-signal false).
6. **Read-path availability trade-off.** `account.viewed`/`account.listed` fail with 503
   (`audit.ingest_failed`) when the ledger is down instead of succeeding silently. This is the
   fail-closed position for sensitive-read audit mandated by the direction; it is scoped to these two
   admin/read endpoints and does not affect general list/query endpoints. Note (E): the partition
   advisory lock is held until commit, so a long business tx delays appends to the same partition —
   the 503 can occur under partition contention, not only when the ledger is down (latency, not
   correctness).
7. **Fail-closed additions do not change production wiring.** `operationRepo` already implements all four
   `WithAudit` interfaces (E7), `callbackRepo`/`accountRepo` get the ledger at construction; production
   behavior is identical except for the fixes. **Behavioral delta documented (F1):** with the C2 fix, a
   *second* replay of an in-flight delivery now commits a new reset (status `pending`,
   `error_code=manual_replay`, lease cleared) plus a new fact, where the pre-conversion code also
   re-reset but silently dropped the dup fact — the audit trail is now complete for every replay, and
   the stale worker's `MarkDelivery` still fails with `ErrCallbackDeliveryLeaseLost`.
8. **File-size gate.** Touched files stay ≤500 lines: `internal/service/callback.go` (465) and
   `internal/repository/operation.go` (454) are near the limit — put new repository WithAudit code in
   new sibling files `internal/repository/callback_audit.go` and
   `internal/repository/account_provision_audit.go`, and keep service-side edits to existing functions
   where possible.
9. **Drain-safety corollary (E):** `ClaimPublication` commits its claim tx **before** reading the fact
   row (`audit_ledger.go:238-253`) — safe only because a publication row can never be visible without
   its committed ledger row. The two-row atomicity is load-bearing for the drain, not just for rollback
   proof; never split the rows. Also: `DeleteSubscriptionWithAudit` cascades to `callback_deliveries`
   (000036 `ON DELETE CASCADE`), so a delete can block on an in-flight `FetchDueDeliveries` claim —
   bounded by the claim tx's short duration, no cycle. `UNIQUE(fact_id)` + cascade do **not** make
   event_id re-append idempotent after a ledger purge (a purge allows a new fact for the same
   event_id) — consistent with deterministic event_id anchoring being out of scope.
10. **The default deployment gets stricter ledger-outage semantics** even with
    `audit_governance.enabled: false`: the five write-path conversions apply unconditionally (append is
    not flag-gated), so mutations fail when the ledger is down instead of committing without a fact.

## 4. Failure modes and handling (revised FM table — each row maps to a proof test in §6)

| # | Failure mode | Handling | Proof test (§6) |
|---|---|---|---|
| FM-1 | `AppendInTx` fails inside a converted mutation | Whole tx rolls back; domain row + ledger row + publication row all absent; service returns mapped `*.audit_failed`-class error (write paths) | T-10.1a/b + `TestCallbackAuditRollbackWithoutSigningKey`, `TestAccountProvisionAuditRollbackWithoutSigningKey` |
| FM-2 | Publication-row insert fails after ledger insert (same tx) | Both roll back together (single tx, E9) — no orphan publication | `TestAuditWiringPublicationInsertRollsBackLedgerRow` (BEFORE-INSERT trigger injection) |
| FM-3 | Inline loop would double-deliver with the drain worker | Impossible after REQ-1: `Record` has no publisher reference (compile-time); single delivery path is `drainPublications` | T-10.2a + `TestAuditLedgerRecordEnqueuesOnly` |
| FM-4 | `FinishPublication` fails (lease lost / DB error) | Explicit handling: metric observed, no swallow; row re-claimed after lease expiry → at-least-once preserved; `UNIQUE(fact_id)` + lease-guarded UPDATE prevent double-finish/dup rows | `TestFinishPublicationFailureObservesMetricNoRetry` + drain-cycle integration (REQ-2) |
| FM-5 | Repeat `ensure` on an existing account | Created-signal (A4) gates the append — no dup append, no `ErrAuditDuplicate` inside the touch tx, `last_seen_at` touch commits | `TestEnsureByIdentityWithAuditRepeatAppendsNothing` |
| FM-6 | Concurrent double provisioning (same identity) | Unmetered: only the winner (`RowsAffected == 1`) appends; loser's tx commits factless; exactly one ledger + one publication row. **Metered (corrected):** same-reservation losers commit factless; **different-reservation losers fail in `ensureAccountQuotaTx` ("account quota reservation mismatch", `account_quota.go:68-69`) — pre-existing behavior, tx fully rolls back, no fact** | `TestEnsureByIdentityWithAuditConcurrentProvision` (both legs) |
| FM-7 | Retried idempotent command hits duplicate `event_id` in-tx | **Sequential:** pre-check → `ErrAuditDuplicate` (rollback of the retry's own write — harmless, the first write stands); service maps to success for `account.provisioned`/`callback.deleted` (C2 removed it for replay). **Concurrent cross-partition (C1):** raw 23505 now mapped to `ErrAuditDuplicate` in `createAuditRows` — same success contract; no 500 | T-10.1 (extended), `TestAppendConcurrentDuplicateAcrossPartitions` (C1), `TestAuditLedgerDuplicateMapsToSuccess` |
| FM-8 | `signing_key` unset | `Append`/`AppendInTx` fail closed (existing `audit_ledger.go:108-110/:124-126`); now fails the *mutation* rather than being swallowed. **New:** startup validation in the composition root (§2.4). Also the **new rollback injection (C2)** for replay/account/callback tests (the old dup-event_id injection is unusable for replay now that facts are fresh) | Existing `audit_ledger_test.go:47` + `audit_ledger_integration_test.go:225` + `TestCompositionRootValidatesAuditSigningKey` + FM-1 keyless tests |
| FM-9 | Operation repo/fake lacks `WithAudit` interfaces | Fail-closed `operation.audit_unavailable` error instead of silent fallback `Record` (second write path eliminated) | `TestOperationAuditUnavailableWhenRepoLacksWithAudit` |
| FM-10 | Ledger outage on read-path facts | `account.viewed`/`listed` return 503 `audit.ingest_failed` (documented availability trade-off, constraint 6; partition-contention clause per E) | `TestAccountViewedListedFailClosedOnLedgerOutage` |
| FM-11 | Governance publisher tenant-unbound / slow | Unchanged: `errAuditTenantUnbound` → `pending` + `audit.binding_unavailable`; bulkhead P6-06 bounds in-flight publishes | Existing `callback_audit_test.go:9,19` |
| FM-12 | Gate false-negative (multiline swallow, renamed method, bare call) | Pattern classes documented and **self-tested**; P1+P2 anchored; gate runs on every CI/done-gate invocation; fixture outside the scanned tree | T-10.2b (self-test + fixture) |

## 5. Migration steps (each independently shippable; keep the tree green)

1. **REQ-1** — delete the inline loop, field, and variadic parameter in `internal/service/audit_ledger.go`.
   Compile-checked; `make test` (audit_ledger_test.go:43,56 pass zero publishers already). Behavior
   neutral in production (N1). **Record the gate baseline here: 11 `_ =` matches on today's tree → 10
   after this step (F2).** Step done-gate: `make test && make lint && make build`.
2. **REQ-5** — remove the operation `Record` fallback + `recordOperationAudit`; fail closed on missing
   interface. Run operation tests (verified: `WithOperationAudit` appears in zero test files — no fake
   relies on the fallback; §6 FM-9 fakes are genuinely new). Step done-gate as above.
3. **Repository layer (REQ-3 + C1)** — add the five `WithAudit` variants + the two new interfaces
   (§2.2); change `NewCallbackRepository` signature and `new.go:135`; add the **C1 23505→
   ErrAuditDuplicate mapping** in `createAuditRows` + the `pgconn` direct dependency; add the
   **startup signing-key validation** (§2.4); add `internal/repository/callback_audit.go` and
   `internal/repository/account_provision_audit.go` (size gate, constraint 8). Land with repository
   integration tests (T-10.1a, A3/A4/A5, FM-5/FM-6, C1). `make test-integration` (Postgres).
4. **Service layer (REQ-3 + N3 + A5 + C2)** — convert the six sites + four N3 sites + the backfill
   (A5); C2 fresh-EventID replay with no dup→success; remove the vestigial `s.audit != nil` guards +
   `SetAuditLogger`/`wireAuditServices` block; add the `ObserveAuditPublicationFinishFailure` metric
   (interface + noop + registry adapter + `metrics.Registry`). Service unit tests land with it,
   including the enumerated fake updates (§6 "compile blast radius"). **Gate is green after this step**
   (0 matches).
5. **REQ-6 gate** — `scripts/check-audit-swallow.sh` (parameterized ROOT + self-test + fixtures),
   Makefile target, AGENTS.md done-gate line. **Baseline note (D1/F2):** the "10 matches" number is a
   post-step-1 measurement; measure it on the pre-step-4 tree (e.g., `git worktree`/fixture checkout)
   or accept that step 1's baseline was recorded when it landed. The step-5 done gate verifies **green**
   (0 matches) — its precondition is steps 1 + 4.
6. **REQ-7 finalize** — acceptance tests (§6: C1 service leg, F1 in-flight double replay, A5
   concurrent-provision, FM-2 trigger test); full done gate incl. `make test-integration`.

**Rollback contract (corrected):** steps 1, 2, 5 are individually revertible (no migration SQL — 000045
untouched, no new migration). **Steps 3↔4 are a coupled pair: revert 4 first, then 3** (reverting 3
alone with 4 live is a compile break). Step 6's acceptance tests ride with {3,4}.

## 6. Testable acceptance mapping (revised — every test compiles against the §2.2 API)

### 6.1 Compile blast radius of the revised API (enumerate before implementation)

- **Interfaces unchanged:** `CallbackRepository`, `AccountRepository`, `AuditLedgerRepository`,
  `AuditLedgerService` — existing fakes keep compiling.
- **New interfaces:** `CallbackAuditRepository`, `AccountProvisionAuditRepository` (§2.2).
- **Service fakes that must gain the audit methods** (their code paths are converted in step 4):
  - `callbackFakeRepo` (`internal/service/callback_delivery_test.go:33-74`) — add
    `CreateSubscriptionWithAudit`/`DeleteSubscriptionWithAudit`/`ResetDeliveryWithAudit` (record the
    fact into a slice, delegate to the plain impl).
  - `fakeBackfillRepo` (`internal/service/account_backfill_test.go`) — add
    `EnsureByIdentityWithAudit` (A4-shaped: return created on first call, existing thereafter).
  - `pendingAccountQuotaRepo` (`internal/service/account_provision_test.go:12-23`, embeds
    `AccountRepository`) — add the two account-provision audit methods **only if** a new test
    exercises metered provisioning (verified: no existing test does — `TestExistingAccountCompletesPendingQuotaSaga` takes the existing-account path).
- **No production constructor changes** outside `new.go:135` (verified).

### 6.2 Test matrix

| Requirement / supplied check | Concrete proof test (file) | Mechanism / assertions |
|---|---|---|
| REQ-1 / T-10.2 "no governance HTTP call from Record" | `TestAuditLedgerRecordEnqueuesOnly` (`internal/service/audit_ledger_test.go`, extend) + T-10.2a (`internal/repository/audit_ledger_integration_test.go`, extend) | Unit: spy repo (`auditLedgerRepoSpy` precedent) — `Record` → exactly 1 `Append`, **no** publish calls (the publisher type is gone from the service). Integration: one `Record` → exactly 1 `audit_ledger` row + 1 `audit_publications` row (`status='pending'`), zero `Publish` invocations. Compile-time leg: `audit_ledger_test.go:43,56` + `audit_integration_test.go:19` construct with a single arg. |
| REQ-2 / single delivery path | Drain-cycle test (`internal/repository/audit_ledger_integration_test.go`, extend) | `ClaimPublication` → failing publisher → `FinishPublication(pending, next=past)` → re-claim (attempt incremented) → `FinishPublication(completed)` → empty claim; exactly one publication row per fact across the cycle (`UNIQUE(fact_id)`), no second delivery path. |
| REQ-3 / T-10.1 "same tx as account row" | `TestEnsureByIdentityWithAuditCreatesAccountAndFact` (new, `internal/repository/account_provision_integration_test.go`) | Success: exactly 1 ledger + 1 publication row, `event_id = accountID` (pre-assigned), partition = `tenant:accountID`; forced failure (keyless ledger, FM-8) rolls back the account row. Metered leg via `EnsureByIdentityWithQuotaWithAudit` (A3). |
| REQ-3 / repeat ensure (A4, FM-5) | `TestEnsureByIdentityWithAuditRepeatAppendsNothing` (new, same file) | Second `EnsureByIdentityWithAudit` on the same identity → existing account returned, `last_seen_at` touched (assert updated timestamp), ledger + publication counts still exactly 1. Same assertions for the quota variant. |
| REQ-3 / concurrent provisioning (FM-6) | `TestEnsureByIdentityWithAuditConcurrentProvision` (new, same file) | N goroutines, same identity, start barrier. Unmetered: exactly 1 account + 1 identity + 1 ledger + 1 publication row; all callers return the same account. Metered, same reservation: identical. Metered, **different** reservations: exactly 1 account + 1 fact; losers error (pre-existing "account quota reservation mismatch" → mapped `account.ensure_failed`), zero facts from losers. |
| REQ-3 / A5 backfill | `TestBackfillRecordsAccountProvisionedFact` + `TestBackfillRepeatRunAppendsNothing` + `TestBackfillConcurrentProvisionSingleFact` (new, `internal/service/account_backfill_test.go`, extend) | Unit with audit-capable `fakeBackfillRepo`: run 1 creates shadow accounts → 1 `account.provisioned` fact per created uid (fake records facts); run 2 → all `Existing`, zero new facts; concurrent provision+backfill of the same identity → exactly 1 fact (fake simulates the A4 arbitration: one `created=true`, one `created=false`). |
| REQ-3 / T-10.1 read facts (N2 decomposition, FM-10) | `TestAccountViewedListedFailClosedOnLedgerOutage` (new, `internal/service/account_test.go`) | `WithAccountAudit` + failing audit repo → `GetAccount`/`ListAccounts` return 503 `audit.ingest_failed` instead of silent success. (No `account_test.go` exists today — new file.) |
| REQ-3 / callback facts in-tx | `TestCallbackAuditHappyPath` + `TestCallbackAuditRollbackWithoutSigningKey` (new, `internal/repository/callback_audit_integration_test.go`) | Happy: `CreateSubscriptionWithAudit`/`DeleteSubscriptionWithAudit`/`ResetDeliveryWithAudit` → exactly 1 subscription/delivery change + 1 ledger + 1 publication row. **Rollback (C2 injection):** a second `callbackRepo` wired to a **keyless** ledger (`AuditLedgerConfig{SigningKey: ""}`) — each mutation errors (`callback.audit_append_failed`-class), domain rows absent (subscription not inserted / not deleted / delivery reset not applied — assert `status`, `lease_owner`, `lease_until`, `error_code` byte-identical), zero ledger + zero publication rows. This replaces the old dup-`event_id` recipe, which is **unusable for replay** after C2 (fresh EventIDs) — signing-key unset is the new injection and still proves rollback because the domain write shares the tx with `AppendInTx`. |
| REQ-3 / C1 concurrent dup (new) | `TestAppendConcurrentDuplicateAcrossPartitions` (new, `internal/repository/audit_ledger_integration_test.go`) | **Deterministic handshake** forcing the post-pre-check race window: (1) coordinator session takes `pg_advisory_lock(hashtext('p-2'))`; (2) goroutine L runs `Append` with triple T on partition `p-2` — pre-check#1 passes, blocks on the xact lock; (3) poll `pg_stat_activity` until L waits on the advisory lock (5s timeout); (4) winner session `BEGIN`s and raw-inserts the ledger + publication rows for triple T on partition `p-1` (**uncommitted**); (5) release the session lock; (6) poll until L waits on the unique index (`wait_event_type='Lock'`, `transactionid`); (7) commit the winner; (8) L's INSERT now fails raw `23505` → mapped: **assert `errors.Is(err, repository.ErrAuditDuplicate)`**; assert exactly 1 ledger + 1 publication row for T. Because the commit happens strictly after L's post-lock pre-check (observed via the poll), the assertion provably exercised the 23505 mapping, not the pre-check. **Service leg:** `service.NewAuditLedgerService(ledger).Record(ctx, input)` with the same triple → `nil` (dup→success). Complement: 8-goroutine barrier variant asserting "all errors `ErrAuditDuplicate` or nil, exactly 1 row" as the regression net. |
| REQ-3 / FM-7 sequential dup | `TestAuditWiringRollbackAtomicity` (`audit_wiring_integration_test.go:163`, extend) + `TestAuditLedgerDuplicateMapsToSuccess` (`internal/service/audit_ledger_test.go`, extend) | Existing :163 mechanism (dup `event_id` → `ErrAuditDuplicate` → whole tx rolled back) stays for `UpdateProfileWithAudit`/operation steps; extend with `countAuditPublicationRows` (new helper beside `countAuditLedgerRows` :39) asserting both counts 0 after rollback and both 1 on happy path (:51). Unit: `Record` on a repo returning `ErrAuditDuplicate` → `nil`. |
| REQ-3 / FM-2 publication-insert failure (new) | `TestAuditWiringPublicationInsertRollsBackLedgerRow` (new, `internal/repository/audit_wiring_integration_test.go`) | Install `CREATE FUNCTION fail_publication_insert() ... RAISE EXCEPTION` + `BEFORE INSERT ON audit_publications` trigger; run one WithAudit mutation (`UpdateProfileWithAudit` precedent or `CreateSubscriptionWithAudit`) → error; assert **0 ledger rows** (ledger insert rolled back with the publication failure) + 0 publication rows; drop trigger. |
| REQ-4 / rollback fails mutation | T-10.1a/b + the keyless-leg tests above | After forced failure: `count(*) FROM audit_ledger WHERE event_id=?` **and** `count(*) FROM audit_publications ap JOIN audit_ledger l ON l.id=ap.fact_id WHERE l.event_id=?` are both 0; happy path asserts both 1. |
| REQ-4 / F1 in-flight double replay (new) | `TestReplayDeliveryInFlightDoubleReplay` (new, `internal/service/callback_integration_test.go`, integration tag) | Setup: subscription + delivery via repo, `FetchDueDeliveries` claim → in-flight (`delivering`, lease held). (1) `ReplayDelivery` → success; delivery `pending`/`manual_replay`, lease cleared; exactly 1 `callback.delivery.replayed` ledger row (`resource_id = delivery.ID`) + 1 publication row, EventID fresh. (2) Re-claim → in-flight again. (3) `ReplayDelivery` again → **success**; delivery re-reset (`pending`/`manual_replay`); exactly 2 ledger rows with **distinct** EventIDs + 2 publication rows — no rollback, no 500, no silent no-op. (4) Stale worker `MarkDelivery` → `ErrCallbackDeliveryLeaseLost` (the delivery state is not double-applied). **Companion leg (state untouched):** keyless-ledger callback repo + service → `ReplayDelivery` errors `callback.delivery_replay_failed`; delivery row **byte-identical** (status/lease/error_code untouched), zero ledger + publication rows — the failed append rolled back the reset. |
| REQ-5 / operation fallback removed (FM-9) | `TestOperationAuditUnavailableWhenRepoLacksWithAudit` (new/updated, `internal/service/operation_audit_test.go`) | Repo fake without the WithAudit interfaces + audit set → `operation.audit_unavailable`, **zero** `Record` calls on the audit fake; real-shape fake → `FinishStepWithAudit`/`SetStatusWithAudit`/`FinishCompensationStepWithAudit` receive the fact, zero `Record` calls. |
| REQ-6 / gate (FM-12, F3/F4/F5) | T-10.2b: `scripts/check-audit-swallow.sh` self-test + fixtures (`scripts/testdata/audit-swallow/{positive,negative}/`) | **positive/** (must fail): the six `_ = s.audit.Record(` shapes, `_ = s.publications.FinishPublication(`, `_ = publisher.Publish(`, a bare `s.audit.Record(ctx, f)` statement (P2), and `_, _ = s.audit.Record(...)` (P1 variant). **negative/** (must pass): converted `if err := s.audit.Record(...)`, `return s.audit.Record(...)`, `err := s.audit.Record(...)`, comment lines `// _ = s.audit.Record(...)`, `_ = s.audit.Log(...)` (excluded), `d.pusher.Publish(...)` (non-audit, P2 excludes `Publish`). The fixture is outside `internal/service` and the script's ROOT is parameterized — `make check-audit-swallow` scans only `internal/service` and **stays green** with the fixture present (F4). Baseline story asserted in the script header: 11 → 10 (post-step-1) → 0 (post-step-4). |
| N3 conversions | Unit: `audit_hold.Put`/`Remove` and `legacyCutover.Set` with failing ledger → mapped `audit_hold.audit_failed`/`cutover.audit_failed`; `finishPublicationAttempt` with failing `FinishPublication` → `ObserveAuditPublicationFinishFailure` observed, no panic, no retry (FM-4) | `internal/service/audit_hold_test.go`, `legacy_cutover_test.go`, `callback_audit_test.go` (extend) |
| FM-8 startup validation | `TestCompositionRootValidatesAuditSigningKey` (new, `internal/app`) | Composition-root validation helper rejects empty `AuditLedger.SigningKey`; existing fail-closed tests (`audit_ledger_test.go:47`, `audit_ledger_integration_test.go:225`) remain the runtime proof. |
| Done gate | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build` + `make test-integration` | Makefile + AGENTS.md |

Out of scope (unchanged from the requirement, plus B1): dead-letter replay tooling and deterministic
`event_id` anchoring (sibling directions); legacy `_ = s.audit.Log(...)` swallows
(ban.go:83/119, admin.go:93/120 delegate to the durable ledger via `Log` — scope-compliant legacy
diagnostics, flagged for follow-up); `drainPublications` / `ClaimPublication` / `FinishPublication`
semantics; migration 000045; the `account.profile.updated` and `operation.requested` non-swallowed
fallbacks (A1/A2) — flagged for a follow-up but not gate matches; **B1 (named):** the
`sync_job.go:454-467` `finishJob` dual-path shape (`FinishWithAudit` at :287 is dead in production,
plain `FinishJob` + non-swallowed `Record` fallback) — the exact E7 shape; not a silent path (the
error propagates to the worker) but REQ-5's "latent second write path" rationale applies unevenly if
it is not converted with `operation.audit_unavailable`-style semantics in the same follow-up as A2.
