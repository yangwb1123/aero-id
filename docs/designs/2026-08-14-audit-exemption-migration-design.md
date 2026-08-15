# Design — Audit-Exemption Migration to Durable, Fail-Closed Audit Facts (B3 Gap-4)

- Date: 2026-08-14
- Working tree at HEAD: `0560d0a docs(campaign): audit-exemption migration pipeline (B3 Gap-4)`
- Scope: `internal/service` live layer — every remaining audit-exempt mutation gains a durable,
  fail-closed audit fact. **No protobuf, migration, config, HTTP/gRPC surface, or op-semantics change.**
- Status: design (implementation spec + acceptance mapping)

---

## 1. Verification verdict

Every claim in the runner-captured requirements spec was re-checked against the live tree. Findings:

| Claim | Verdict | Evidence |
|---|---|---|
| AGENTS.md exemption bullet at 278–288, exact text | **Verified** | `AGENTS.md` 278–288; names file create/delete/update, message send/edit/react, preferences, region promotion, projection replication/replay/sync as zero-site exempt surfaces; file *share* audited in-tx via `buildLegacyAuditFact` |
| `fileService.Create` file.go:87 zero sites | **Verified** | `file.go` Create persists `f` (ID minted by `idgen.NewID()`) and returns; no `Record`/`Log` |
| `fileService.CompleteUpload` file_presign.go:62 zero sites; `PresignUpload` writes no row | **Verified** | `file_presign.go` — `PresignUpload` returns `{FileID, UploadURL, StorageKey}` with no DB write; `CompleteUpload` persists the row with `ID: in.FileID` |
| Handler flow: `POST /files` → Create; `POST /files/complete` → CompleteUpload | **Verified** | `internal/handler/http/file.go` — `r.Auth.POST("/files", h.Upload)` → `h.files.Create`; `r.Auth.POST("/files/complete", h.CompleteUpload)`; `POST /files/upload-url` → `PresignUpload` (no row). Direct and presigned paths are mutually exclusive materializations |
| `UpdateMetadata` file_share.go:21 zero sites, empty-mask early return | **Verified** | `file_share.go` — `len(fields) == 0 → return f, nil` before any write |
| Message service: Send 47 / SendSystem 122 / Edit 254 / ReactToMessage 427 / UnreactMessage 462 / Delete 296 zero sites except delete path | **Verified** | `message.go` = 477 lines; only delete path audited: in-tx `SoftDeleteWithEventAudit` (B2-1) + residual `deleteAlreadyGone` → `auditMessageDelete` (`// audit-site: standalone-fact`, anchored `ID: id`, fail-closed `apperr.Unavailable("audit.ingest_failed", ...)`) |
| `SendSystem` has zero production callers | **Verified** | Only definition (`service.go:258` interface, `message.go:122` impl) in the live tree; no handler route; test fakes only |
| Preference `Set` preference.go:66 zero sites, no audit field | **Verified** | `preference.go` — struct `{prefs repository.PreferenceRepository}` only |
| Region `Promote` region_fencing.go:44 zero sites, CAS single-tx | **Verified** | `region_fencing.go` — `PromoteWithFencing` CAS in one gorm tx; errors mapped Conflict/NotFound/Internal |
| Replication `ReplicateOnce` projection_replication.go:64 zero sites; deterministic envelope EventID | **Verified** | `replicationEnvelope` derives EventID via `uuid.NewSHA1(projectionReplicationNamespace, ...)` per `(tenant, source, account, dataset, version)`; cursor saved after each publish; `projection_replication.go` is the R-2-allowlisted `uuid.NewSHA1` site |
| Replay `ReplayProjection` projection_replay.go:65 zero sites; zero-event early return | **Verified** | `projection_replay.go` — early return `result.Completed = true` before any audit |
| Sync projection `persistSyncSummary` sync_projection.go:11 zero sites; job lifecycle audited | **Verified** | `sync_projection.go`; `sync_job.go` `FinishWithAudit` (in-tx, `syncJobFinishedAudit`) at the job layer |
| Domain constants block stdlib-only | **Verified** | `internal/domain/audit.go` — `const` block, imports `time` only; fail-closed wording |
| `AuditEvent` shape | **Verified** | `domain.AuditEvent{ID, ActorUID, Action, SubjectType, SubjectID, TargetUID, Meta, CreatedAt}` |
| Ledger validation requires TenantID + dedup key `(tenant, source, event_id)` | **Verified** | `repository/audit_ledger.go` `validateAuditFact` (Source, SourceRegion, PartitionKey, TenantID, Action, OccurredAt); unique `uq_audit_ledger_tenant_event`; `ErrAuditDuplicate` folded to success on the standalone `Record` path (`service/audit_ledger.go`) |
| `idgen.Anchor` exists; arity ≥ 4 contract; gate rules | **Verified** | `internal/pkg/idgen/idgen.go` `Anchor(parts ...string) ID` (UUIDv5, NUL-separated, panics on 0 parts/NUL); `scripts/check-anchored-event-ids.sh` R-1 bans `EventID: idgen.NewID()` (C2 allowlist `callback.go|audit_fact.go|audit_publication_replay.go|callback_audit.go`), R-2 bans `uuid.NewSHA1/NewHash` outside idgen + projection_replication, zero-part `Anchor()`, namespace constants |
| Manifest gate (8 rows), marker rule, per-file balance | **Verified** | `scripts/check-audit-manifest.sh` — per-file bare-site count == token count; token in-file; `audit-site: <class>` marker in-file; 8 current rows |
| Swallow gate P1/P2 | **Verified** | `scripts/check-audit-swallow.sh` — `_ = ...Record/Log` and statement-start bare calls banned; new sites must use `if err := ...` or `return s.audit.Log(...)` |
| new.go wiring points (prefSvc ~198, account region dir 215, regionFencingSvc 217, replication 233, replay handler ~408, `assertAuditWiring` 6 services, FM-8 comment) | **Verified** | `internal/app/new.go` = 485 lines; `buildAuditStack` returns `(CallbackService, AuditLedgerService, FullAuditLedgerRepository, error)`; `buildProjectionReplication(cfg, gdb, log, metricsReg) (stop func(), err error)` |
| Region directory repo constructor + integration test call sites | **Verified** | `NewRegionDirectoryRepository(db *gorm.DB)`; callers only at new.go:215, new.go:217, `region_directory_integration_test.go:26,89` |
| Test scaffolding (mocks.Auditor, auditLedgerRepoSpy, assertAuditIngestFailed, standalone harness) | **Verified** | `mocks.go:647`; `audit_ledger_test.go:16`; `file_test.go:34`; `audit_standalone_sites_integration_test.go` `startStandaloneHarness`/`standaloneFactCount`/`standaloneCount` |
| Tenant/actor in ctx on HTTP paths | **Verified** | `middleware/auth.go:44` sets `WithIdentity`; `TokenIdentity{UID, TenantID, ...}`; `TenantFrom`/`actorFromContext` in `service/service.go` |

**Divergences from the evidence spec** (task constraints override the runner's choices):

1. **preference.set and region.promote EventID**: the evidence proposed zero-EventID (preference) and
   fresh-v7 (region promote, via C2). The task mandates deterministic `idgen.Anchor` derivation for both
   (§4.3, §4.5). The design derives both; region promote stays in-tx (S3) with the derived EventID.
2. **`UnreactMessage`**: kept **in scope** (the evidence already had it in scope; the task asks for an
   explicit evidence-backed decision — §2 row 9).
3. **Message/file multi-occurrence ops (update/edit/react/unreact)**: kept as **zero EventID, fresh
   per-execution** rows (evidence S2). The task's "entity-ID anchoring where a natural ID exists"
   is applied to the single-occurrence create/send/delete sites where anchoring is *correct*; for
   multi-occurrence sites entity anchoring would silently drop legitimate later facts via
   `uq_audit_ledger_tenant_event` (documented in §4.2). This is the gate-sanctioned third option
   ("leave the ID zero", `account.viewed` precedent).

---

## 2. Exact site list (fixed, with evidence)

File-create decision: **both `Create` and `CompleteUpload` emit `file.create`** — each is a
materialization of a file row, the two paths are mutually exclusive (a file is created through exactly
one of them), and `PresignUpload` writes no row. Emitting on only one path would leave half the
materializations unaudited; emitting on both is exactly-one-per-file. Handler flow verified in
`internal/handler/http/file.go` (`POST /files` → Create; `POST /files/complete` → CompleteUpload).

| # | Op | Method (verified line) | Scope | Pattern | Action | EventID | Actor / subject |
|---|---|---|---|---|---|---|---|
| 1 | file create (direct) | `fileService.Create` (file.go:87) | **IN** | S1 | `AuditFileCreate` = `file.create` | `f.ID` (entity anchor) | actor = owner; subject `file` / `f.ID.String()` |
| 2 | file create (presigned) | `fileService.CompleteUpload` (file_presign.go:62) | **IN** | S1 | `AuditFileCreate` | `f.ID` (= `in.FileID`; retry of the same presigned FileID dedups) | actor = owner; subject `file` / `f.ID.String()` |
| 3 | file delete | `fileService.Delete` (file.go:227) | **IN** | S1 | `AuditFileDelete` = `file.delete` | `id` (entity anchor) | actor = owner; subject `file` / `id.String()` |
| 4 | file update | `fileService.UpdateMetadata` (file_share.go:21) | **IN** (empty-mask no-op emits **no** fact) | S2 | `AuditFileUpdate` = `file.update` | zero (fresh per-execution) | actor = owner; subject `file` / `id.String()` |
| 5 | message send | `messageService.Send` (message.go:47) | **IN** | S1 | `AuditMessageSend` = `message.send` | `m.ID` (entity anchor) | actor = `m.FromUID`; subject `message` / `m.ID.String()`; `TargetUID = m.ToUID` |
| 6 | message send (system) | `messageService.SendSystem` (message.go:122) | **OUT — documented exemption** | — | — | — | No authenticated actor **and no tenant** (ledger `validateAuditFact` rejects tenant-less facts); server-internal emit path with **zero production callers** (interface + impl only, no handler route). Non-user action with no legitimate ledger row. Pinned by a test + AGENTS.md note. |
| 7 | message edit | `messageService.Edit` (message.go:254) | **IN** | S2 | `AuditMessageEdit` = `message.edit` | zero (fresh per-execution) | actor = sender (ctx); subject `message` / `m.ID.String()`; `TargetUID = m.ToUID` |
| 8 | message react | `messageService.ReactToMessage` (message.go:427) | **IN** | S2 | `AuditMessageReact` = `message.react` | zero (fresh per-execution) | actor = `actorUID` param (handler passes ctx actor, message.go handler:191–196); subject `message` / `messageID.String()`; `TargetUID = peerOf(actorUID, m)` |
| 9 | message unreact | `messageService.UnreactMessage` (message.go:462) | **IN** (symmetric mutation; idempotent repo op; invariant completeness) | S2 | `AuditMessageUnreact` = `message.unreact` | zero (fresh per-execution) | actor = `actorUID` param; subject `message` / `messageID.String()`; `TargetUID = peerOf(actorUID, m)` |
| 10 | preferences set | `preferenceService.Set` (preference.go:66) | **IN** | S2 + anchored derivation | `AuditPreferenceSet` = `preference.set` | `idgen.Anchor` (5 parts, §4.3) | actor = `uid` (ctx); subject `preference` / `uid`; payload `{"count": len(prefs)}` |
| 11 | region promotion | `regionFencingService.Promote` (region_fencing.go:44) | **IN** | S3 (in-tx WithAudit) + anchored derivation | `AuditRegionPromote` = `region.promote` | `idgen.Anchor` (6 parts, §4.5) | actor = ctx; subject `region_directory` / `accountID.String()`; payload `{target_region, expected_epoch}` |
| 12 | projection replication publish | `projectionReplicationService.ReplicateOnce` (projection_replication.go:64) | **IN** | S2 + reuse of deterministic ID | `AuditProjectionReplicationPublish` = `projection.replication.publish` | `envelope.EventID` (already deterministic per `(tenant, source, account, dataset, version)`) | actor = `"system"`; `TenantID = item.TenantID`, `AccountID = item.Snapshot.AccountID`; payload `{target, dataset, version}` |
| 13 | projection replay run | `projectionReplayService.ReplayProjection` (projection_replay.go:65) | **IN** | S2 | `AuditProjectionReplay` = `projection.replay.run` | zero (fresh per-execution) | actor = ctx; `TenantID = input.TenantID`, `AccountID = input.AccountID`; payload `{source, limit, replayed_events, completed}` |
| 14 | sync projection summary | `syncJobService.persistSyncSummary` (sync_projection.go:11) | **OUT — documented exemption** | — | — | — | Disposable/replayable projection-state writes (AGENTS.md data ownership); high-frequency worker-leg writes; the sync job lifecycle is already audited in-tx (`sync_job.go` `FinishWithAudit`). Noise-budget decision. |

Pattern classes:

- **S1 — standalone-fact, entity-anchored EventID** (message-delete model, `message.go:369–380`):
  post-mutation `s.audit.Log(...)`; failure → `apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)`;
  anchored `EventID = resource ID` so a retry dedups at the ledger `(tenant, source, event_id)`. Used for
  single-occurrence create/send/delete facts where a retry re-derives the identical fact.
- **S2 — standalone-fact, zero EventID (fresh per-execution row)** (`account.viewed` model,
  `account.go:154–161`): multi-occurrence mutation facts. **Rationale (load-bearing):** entity
  anchoring would let `uq_audit_ledger_tenant_event` silently dedup a *later legitimate occurrence*
  (edit #2 of the same message collides with edit #1 — same message ID, different intent), a compliance
  loss worse than a retry duplicate. Zero EventID = one row per execution, distinguishable by
  `occurred_at`; **no retry-dedup contract**, documented. This is the gate-sanctioned "leave the ID
  zero" option and preserves the AGENTS.md promise "never a silently dropped fact".
- **S3 — in-tx WithAudit** (file-share/ban/admin model): region promotion, where post-mutation
  standalone audit leaves an unrecoverable residual (the fencing epoch is consumed by the CAS; a
  post-commit retry would Conflict). The fact commits in the same tx as the CAS; on audit failure the
  op rolls back and a retry is safe.

---

## 3. API changes

**No transport/protobuf/migration/config changes.** Public HTTP routes, status codes, gRPC methods,
protobuf stubs, SQL migrations, and config keys are untouched. Go signature changes are all internal:

| Surface | Change | Compatibility |
|---|---|---|
| `internal/domain/audit.go` | 11 new `const` strings (`AuditFileCreate`, `AuditFileDelete`, `AuditFileUpdate`, `AuditMessageSend`, `AuditMessageEdit`, `AuditMessageReact`, `AuditMessageUnreact`, `AuditPreferenceSet`, `AuditRegionPromote`, `AuditProjectionReplicationPublish`, `AuditProjectionReplay`) | Additive; stdlib-only (domain purity preserved, `check-architecture` rule (b)) |
| `service.NewPreferenceService` | becomes variadic: `NewPreferenceService(prefs repository.PreferenceRepository, opts ...func(*preferenceService))`; new `WithPreferenceAuditLogger(a AuditLogger)`; struct gains `audit AuditLogger // nil => audit inactive` | Existing callers unaffected (variadic) |
| `service.NewRegionFencingService` | becomes variadic: `NewRegionFencingService(directory repository.RegionDirectoryRepository, opts ...func(*regionFencingService))`; new `WithRegionFencingAuditLogger(a AuditLogger)`; struct gains `audit AuditLogger` | Existing callers unaffected (variadic) |
| `service.NewProjectionReplicationService` | already variadic; new option `WithProjectionReplicationAudit(a AuditLedgerService)`; struct gains `audit AuditLedgerService // nil => audit inactive` | Additive |
| `service.NewProjectionReplayService` | becomes variadic: `NewProjectionReplayService(events, snapshots, opts ...func(*projectionReplayService))`; new `WithProjectionReplayAudit(a AuditLedgerService)`; struct gains `audit AuditLedgerService` | Existing callers unaffected (variadic) |
| `repository.NewRegionDirectoryRepository` | gains a required param: `NewRegionDirectoryRepository(db *gorm.DB, audit AuditLedgerTransactionalRepository) RegionDirectoryRepository` (`NewFileShareRepository(db, audit)` precedent; nil never in production) | 4 call sites updated: `new.go:215`, `new.go:217`, `region_directory_integration_test.go:26,89` |
| `repository` | new interface `RegionDirectoryAuditRepository` with `PromoteWithFencingWithAudit(ctx, tenantID, accountID, targetRegion, expectedEpoch, fact) (int64, error)`; `regionDirectoryRepo` gains `audit` field; CAS body extracted to private `promoteWithFencingTx` | Separate interface per `MessageAuditRepository`/`BanAuditRepository` convention; `RegionDirectoryRepository` unchanged |
| `internal/app/replication.go` | `buildProjectionReplication(cfg, gdb, log, metricsReg, auditLedger service.AuditLedgerService) (service.ProjectionReplicationService, func(), error)` — returns the service alongside the stop func | Internal-only |
| `internal/app/new.go` | wire the four services + repo param + extend `assertAuditWiring` | 485 → ≈493 lines (< 500) |
| `internal/service/audit_wiring.go` | 4 new `AuditLoggerWired()` methods; header comment refreshed | Additive |

### 3.1 Service wiring (composition root, `internal/app/new.go`)

```go
prefSvc := service.NewPreferenceService(prefRepo, service.WithPreferenceAuditLogger(auditLedger))
// accountSvc: WithAccountRegionDirectory(repository.NewRegionDirectoryRepository(gdb, auditLedgerRepo), cfg.Account.HomeRegionVersion)
regionFencingSvc := service.NewRegionFencingService(
    repository.NewRegionDirectoryRepository(gdb, auditLedgerRepo),
    service.WithRegionFencingAuditLogger(auditLedger))
replicationSvc, stopReplication, err := buildProjectionReplication(cfg, gdb, log, metricsReg, auditLedger)
projectionReplaySvc := service.NewProjectionReplayService(
    repository.NewEventStoreRepository(gdb), snapshotRepo, service.WithProjectionReplayAudit(auditLedger))
// pass projectionReplaySvc to NewReplayHandlerWithProjection
```

`assertAuditWiring` gains `preference`, `region_fencing`, `projection_replay` unconditionally and
`projection_replication` **conditionally** (the service is nil when `cfg.Replication.Enabled` is
false — a nil interface would panic the assertion; skip then). `buildAuditStack` already returns
`auditLedger` (AuditLedgerService) and `auditLedgerRepo` (FullAuditLedgerRepository) before every
construction site, so the C-1/C-3 composition-root pins are untouched.

---

## 4. Fail-closed contract

- **Every standalone site** (`Create`, `CompleteUpload`, `Delete`, `UpdateMetadata`, `Send`, `Edit`,
  `ReactToMessage`, `UnreactMessage`, `Set`, `ReplicateOnce`, `ReplayProjection`) propagates the audit
  error as `apperr.Unavailable("audit.ingest_failed", "audit record unavailable").Wrap(err)` (503).
  Swallow-gate shape: `if err := s.audit.Log(...); err != nil { return ..., apperr.Unavailable(...).Wrap(err) }`
  or a helper `return s.audit.Log(...)` — never `_ =` and never a bare statement-start call
  (`check-audit-swallow` P1/P2).
- **Region promote (in-tx)** surfaces the append failure through the shared error mapper as
  `apperr.Internal("region.promotion_failed", ...)`; the tx rolls back, so the CAS never commits
  without its fact (ban/admin precedent).
- **Nil-audit convention unchanged**: `nil => audit inactive, no failure`. Unit tests construct
  services without an auditor; the composition root asserts every guarded service is wired
  (`AuditLoggerWired()`), so the skip guards are provably unreachable in production.

### 4.1 Failure modes

| Op | Commit point vs audit | On audit failure (503) | Retry behavior |
|---|---|---|---|
| file.create (direct/presigned) | mutation commits first | 503; file exists, fact missing | retry re-runs create → **second entity** (op semantics unchanged; documented residual); presigned retry reuses `FileID` → ledger dedup |
| file.delete | soft delete + ref cleanup commit first | 503; file soft-deleted, fact missing | retry reads NotFound → no re-append (the message-delete FM-1 residual fix is **not** replicated — `FileRepository.GetIncludingDeleted` noted as follow-up, out of scope) |
| file.update / message.edit / react / unreact / preference.set / replay.run | mutation commits first | 503; mutation applied, fact missing | retry appends a **duplicate row** (S2 contract: distinguishable by `occurred_at`, never silently dropped); identical preference.set retries dedup via the digest anchor; react/unreact are idempotent at the repo |
| region.promote | CAS + fact commit in ONE tx | `region.promotion_failed`; CAS rolled back | retry with same `expectedEpoch` is safe (no epoch consumed) |
| replication publish | publish + count commit; cursor saved after | run errors; cursor stays behind | next run re-publishes the same snapshot; deterministic `envelope.EventID` dedups the fact — consistent with at-least-once |
| message.send | mutation commits first | 503; message exists, fact missing | retry creates a **second message** (documented residual; op semantics unchanged) |

Documented residuals of the standalone pattern (kept, with follow-up notes): non-idempotent
creates/sends after a 503, and file-delete fact loss on retry (no `GetIncludingDeleted` for files).

---

## 5. Migration steps (ordered, each lands with its tests)

1. **Domain**: add the 11 action constants to `internal/domain/audit.go` (live const block).
2. **Message** (new sibling `internal/service/message_audit.go`, keeping message.go 477 → ≈489 < 500):
   - `auditMessageSend(ctx, m)` — `ID: m.ID` anchor, `ActorUID: m.FromUID`, `TargetUID: m.ToUID`.
   - `auditMessageEdit(ctx, m)` — zero ID, `ActorUID: m.FromUID`, `TargetUID: m.ToUID`.
   - `auditMessageReact(ctx, actorUID, messageID, m)` / `auditMessageUnreact(...)` — zero ID,
     `TargetUID: peerOf(actorUID, m)`; extract `peerOf` (the existing from/to peer pick, 1 statement).
   - Each carries `// audit-site: standalone-fact` on its doc comment (auditMessageDelete model) and
     returns `s.audit.Log(...)`.
   - Call sites: `Send` after `s.messages.Create`; `Edit` after `UpdateContent`; `ReactToMessage`
     after the reaction repo call **before** the best-effort `AppendEvent`; `UnreactMessage` after the
     repo call. `SendSystem` unchanged (documented exemption, pin test).
3. **File**: `Create`/`Delete` (file.go) and `CompleteUpload` (file_presign.go) inline S1 sites
   (anchored `f.ID`/`id`); `UpdateMetadata` (file_share.go) S2 site (zero ID) after the write and
   after `f.UpdatedAt = now`, inside the `len(fields) > 0` path. Markers on each call line.
4. **Preference**: field + variadic constructor + `WithPreferenceAuditLogger`; `Set` site after
   `Upsert` with the derived EventID (see §5.1); digest helper in preference.go.
5. **Region fencing**: service field/option + audited arm (type-assert `RegionDirectoryAuditRepository`,
   else `apperr.Internal("region.audit_unavailable", ...)`); extract `mapRegionPromotionError` shared by
   both arms; repository: audit field, required constructor param, `PromoteWithFencingWithAudit` via
   extracted `promoteWithFencingTx`, `AppendInTx` folding `ErrAuditDuplicate` (ban pattern); update
   `region_directory_integration_test.go` call sites with a test ledger repo.
6. **Replication**: field + `WithProjectionReplicationAudit`; `ReplicateOnce` site after
   `publisher.Publish` + `count++`, **before** `SaveCursor`; reuse `envelope.EventID` (no new
   derivation code; R-2 file stays the sole `uuid.NewSHA1` site). `Record` folds `ErrAuditDuplicate`
   to success at the service layer, so re-publish dedups cleanly.
7. **Replay**: field + variadic constructor + `WithProjectionReplayAudit`; single shared helper
   `auditProjectionReplay` (one bare site) called from both the zero-event branch and the normal
   tail; each call site wraps failure as `apperr.Unavailable("audit.ingest_failed", ...)`.
8. **audit_wiring.go**: 4 new `AuditLoggerWired()` methods; refresh the "10 fail-closed Log sites"
   header comment to reflect the full site set.
9. **new.go + replication.go**: wiring per §3.1; extend `assertAuditWiring` (conditional replication);
   refresh the FM-8 comment.
10. **Manifest**: 11 new rows (§6) + `audit-site: standalone-fact` markers at every site.
11. **AGENTS.md**: rewrite the exemption bullet (§7).
12. **Tests**: unit suite (§8.1) + integration suite (§8.2).
13. **Done gate** (§9).

### 5.1 EventID derivation (task constraint b — `idgen.Anchor`)

`idgen.Anchor` is the sanctioned deterministic derivation (UUIDv5 over NUL-separated parts, fixed
arity ≥ 4 per the `check-anchored-event-ids` contract). No `EventID: idgen.NewID()` is introduced
outside the C2 allowlist; no new `uuid.NewSHA1` site.

```go
// preference.set — deterministic per (tenant, uid, payload). Arity 5.
// A retry of the same payload re-derives the same EventID (ledger dedup);
// a distinct payload derives a different EventID (no silent drop of a later
// legitimate Set — the multi-occurrence compliance invariant).
func preferenceSetEventID(tenantID, uid string, prefs []*domain.NotificationPreference) domain.ID {
    return idgen.Anchor("audit", "preference.set", tenantID, uid, preferenceSetDigest(prefs))
}

// preferenceSetDigest = sha256 over the canonical JSON of the normalized
// preference list (NotificationType/ScopeType/ScopeID/Enabled/QuietHoursStart/
// QuietHoursEnd/Timezone, stable field order; excludes UID — already a part —
// and CreatedAt/UpdatedAt, which would break retry stability).
```

```go
// region.promote — deterministic per promotion attempt. Arity 6.
// Distinct promotions have distinct expected_epoch (CAS guarantees each epoch
// is consumed exactly once), so successive promotions never collide; a retry
// of the same attempt re-derives the same EventID.
anchorID := idgen.Anchor("audit", "region.promote", tenantID, accountID.String(),
    targetRegion, strconv.FormatInt(expectedEpoch, 10))
```

Rationale recorded: a 4-part `("audit", "preference.set", tenant, uid)` anchor (no digest) was
considered and **rejected** — it would silently dedup every later Set by the same user regardless of
content, violating the AGENTS.md rewrite promise "never a silently dropped fact". The digest part
keeps distinct calls distinct while preserving retry dedup.

---

## 6. Manifest rows + per-file balance

11 new rows in `scripts/check-audit-manifest.sh`:

```
"internal/service/file.go|AuditFileCreate|standalone-fact|-"
"internal/service/file.go|AuditFileDelete|standalone-fact|-"
"internal/service/file_presign.go|AuditFileCreate|standalone-fact|-"
"internal/service/file_share.go|AuditFileUpdate|standalone-fact|-"
"internal/service/message_audit.go|auditMessageSend|standalone-fact|-"
"internal/service/message_audit.go|auditMessageEdit|standalone-fact|-"
"internal/service/message_audit.go|auditMessageReact|standalone-fact|-"
"internal/service/message_audit.go|auditMessageUnreact|standalone-fact|-"
"internal/service/preference.go|AuditPreferenceSet|standalone-fact|-"
"internal/service/projection_replication.go|AuditProjectionReplicationPublish|standalone-fact|-"
"internal/service/projection_replay.go|auditProjectionReplay|standalone-fact|-"
```

Per-file balance after the change (bare `s.audit.Record|Log(` count == manifest token count):

| File | Sites | Tokens |
|---|---|---|
| file.go | 2 | `AuditFileCreate`, `AuditFileDelete` |
| file_presign.go | 1 | `AuditFileCreate` |
| file_share.go | 1 | `AuditFileUpdate` |
| message.go | 1 (unchanged) | `auditMessageDelete` |
| message_audit.go | 4 | `auditMessageSend`, `auditMessageEdit`, `auditMessageReact`, `auditMessageUnreact` |
| preference.go | 1 | `AuditPreferenceSet` |
| projection_replication.go | 1 | `AuditProjectionReplicationPublish` |
| projection_replay.go | 1 | `auditProjectionReplay` |
| account.go / operation_state.go / replay.go / event_ingest.go / legacy_cutover.go | 2 / 1 / 2 / 1 / 1 (unchanged) | existing rows |

Region promotion is in-tx → **no** service-layer bare site → no row. Every new site carries the
`// audit-site: standalone-fact` marker (call line or helper doc comment); the marker check is
per-file, one marker suffices for files with multiple sites.

Line-budget check: message.go ≈489, new.go ≈493, file.go ≈308, file_presign.go ≈118, file_share.go
≈198, preference.go ≈168, region_fencing.go ≈129, region_directory.go ≈237, projection_replication.go
≈163, projection_replay.go ≈166, message_audit.go ≈90 — all < 500 (`check-file-sizes`); modified
methods stay ≤ 50 lines / cyclop ≤ 10 (`Create`, `Send` already nolint'd; `Edit`, `ReactToMessage`,
`UnreactMessage`, `UpdateMetadata`, `Set`, `Promote`, `ReplicateOnce`, `ReplayProjection` gain ≤ 2
statements and ≤ 1 branch).

---

## 7. AGENTS.md rewrite

Replace the exemption bullet (current AGENTS.md 278–288) with (historical framing + dated-record
rule; no present-tense `AuditLogger` references — FM-16 pin):

> - The audit-coverage invariant covers every operation that emits an audit fact, not every mutation.
>   Until the 2026-08-15 B3 Gap-4 migration, the former legacy compatibility surfaces were exempt
>   (verified per op: zero `Record`/`Log` sites) but are now fully audited with durable facts: file
>   create/delete/update (`internal/service/file.go`, `file_presign.go`, `file_share.go`; file *share*
>   has been audited in-tx since B2-1 via `buildLegacyAuditFact`), message send/edit/react/unreact
>   (`internal/service/message.go`, `message_audit.go`; delete has been audited since B2-1,
>   `auditMessageDelete`), preferences (`internal/service/preference.go`), region promotion
>   (`internal/service/region_fencing.go`, in-tx with the fencing CAS), projection replication
>   publishes and projection replay runs (`internal/service/projection_replication.go`,
>   `projection_replay.go`). Create/send/delete facts anchor their EventID on the resource ID so
>   retries dedup at the ledger; preference.set and region.promote derive a deterministic
>   `idgen.Anchor` EventID; replication publishes reuse the deterministic snapshot-envelope EventID.
>   Multi-occurrence ops (update/edit/react/unreact/replay) record fresh per-execution rows with no
>   retry-dedup contract — a retry may append a duplicate row distinguishable by `occurred_at`, never
>   a silently dropped fact. Two documented carve-outs remain: `SendSystem` (server-internal emit path
>   with no tenant or actor attribution — the ledger rejects tenant-less facts — and no production
>   callers) and per-job sync projection summaries (`persistSyncSummary`; disposable replayable
>   projection writes whose lifecycle is already audited at the job layer). The comment tree was
>   quarantined on 2026-08-08 and live comment report is audited in-tx (`ResolveReportWithAudit`,
>   `internal/service/comment.go`), so comment ops are not exempt. `make check-audit-manifest`
>   enumerates every bare site; `make check-audit-swallow` bans swallowed/bare writes.

---

## 8. Test plan

### 8.1 Unit tests (deterministic fakes, no network/DB; `mocks.Auditor` with `LogFn`,
`auditLedgerRepoSpy` + `NewAuditLedgerService` for Record-capable fakes,
`service.WithActor`/`WithIdentity` for actor+tenant)

| File | Tests | Key assertions |
|---|---|---|
| `file_test.go` | `TestFileService_CreateAuditFactCommitted` / `...CreateAuditFailureFailsClosed`; `TestFileService_DeleteAuditFactCommitted` / `...DeleteAuditFailureFailsClosed`; `TestFileService_UpdateMetadataAuditFactCommitted` / `...UpdateMetadataAuditFailureFailsClosed`; `TestFileService_UpdateMetadataNoOpEmitsNoFact` | Fact `EventID == file.ID` (create/delete), action constants, `SubjectType "file"`, actor = owner; failure → `assertAuditIngestFailed` (503, `audit.ingest_failed`) and the repo mutation was performed (fail-closed after commit); empty-mask update → auditor not called |
| `file_test.go` | `TestFilePresign_CompleteUploadAuditFactCommitted` / `...CompleteUploadAuditFailureFailsClosed` | Fact `EventID == in.FileID` (anchored, retry-dedup contract), action `file.create` |
| `message_audit_test.go` (new) | `TestMessageService_SendAuditFactCommitted` / `...SendAuditFailureFailsClosed`; `...EditAuditFactCommitted` / `...EditAuditFailureFailsClosed`; `...ReactToMessageAuditFactCommitted` / `...ReactToMessageAuditFailureFailsClosed`; `...UnreactMessageAuditFactCommitted` / `...UnreactMessageAuditFailureFailsClosed`; `TestMessageService_SendSystemNoAuditFact` (pin) | Send: `EventID == m.ID`, `TargetUID == toUID`, actor = sender. Edit/React/Unreact: zero EventID (fresh per-execution), correct action, actor (react/unreact from the `actorUID` param). Failures → `assertAuditIngestFailed`. **SendSystem pin: auditor not invoked** (no tenant, no actor — the documented exemption) |
| `preference_test.go` | `TestPreferenceService_SetAuditFactCommitted`; `TestPreferenceService_SetAuditFailureFailsClosed`; `TestPreferenceService_SetAnchoredEventIDStableAcrossRetries` | Action `preference.set`; actor from ctx; `EventID == preferenceSetEventID(tenant, uid, prefs)`; **same payload retried → identical EventID** (dedup contract); **different payload → different EventID** (no silent drop); payload count; failure → `assertAuditIngestFailed` |
| `region_fencing_test.go` | `TestRegionFencing_PromoteAuditFactCommitted`; `TestRegionFencing_PromoteAuditFailureFailsClosed` | Extend `fakeRegionDirectory` with `PromoteWithFencingWithAudit` (records fact + injectable append error). Success: fact recorded (action `region.promote`, `TenantID`, subject `region_directory`/account, payload `target_region`/`expected_epoch`, EventID == derived anchor), epoch advanced. Failure: `apperr.Internal("region.promotion_failed", ...)`, fake state shows promotion **not** committed (no epoch bump) |
| `projection_replication_test.go` | `TestReplicateOnceAuditFactPerPublishedItem`; `TestReplicateOnceAuditFailureFailsRun`; `TestReplicateOnceAuditEventIDDeterministic` | 2 items → 2 `Record` calls with `EventID == envelope.EventID` (deterministic per `(tenant, source, account, dataset, version)`), per-item TenantID/AccountID, actor `"system"`; audit failure → `ReplicateOnce` errors and `SaveCursor` not called |
| `projection_replay_test.go` | `TestReplayProjectionAuditFactCommitted` (incl. zero-event run); `TestReplayProjectionAuditFailureFailsClosed` | Fact per run with TenantID/AccountID from input; failure → `assertAuditIngestFailed` |

### 8.2 Integration tests (`-tags integration`, real ledger; extend
`audit_standalone_sites_integration_test.go` — `startStandaloneHarness`, `standaloneFactCount` by
`event_id`, `standaloneCount` by action)

- **File create dedup**: direct `Create` → 1 row (`action='file.create'`, `event_id = file id`);
  `CompleteUpload` retried twice with the same `FileID` → exactly 1 row.
- **Message send**: `Send` with `WithIdentity` (tenant) → 1 row `message.send`; re-`Send` of a new
  message → 2 rows (distinct anchors).
- **Preference retry dedup**: same payload twice → 1 row (anchored); different payload → 2 rows.
- **Region promote**: real `RegionDirectoryRepository` + real ledger → `Promote` commits promotion
  AND fact in one tx; a forced append failure rolls back the CAS (epoch unchanged, 0 rows).
- **Replication dedup**: two `ReplicateOnce` runs publishing the same snapshot version → exactly 1 row
  (`event_id = deterministic envelope ID`).
- **Replay**: two `ReplayProjection` runs → 2 rows (fresh per-execution).

---

## 9. Testable acceptance mapping

| Constraint | Acceptance |
|---|---|
| (a) exact site list, no double facts | `go test ./internal/service/ -count=1 -run 'TestFileService_CreateAudit\|TestFilePresign_CompleteUploadAudit'` — direct and presigned creates each emit exactly one `file.create`; `PresignUpload` emits none; `SendSystem` pinned no-fact; `UnreactMessage` in scope with its own fact test; `persistSyncSummary` unchanged (grep `Record|Log` in `sync_projection.go` = 0) |
| (b) EventID anchoring | `make check-anchored-event-ids` green (no new `EventID: idgen.NewID()`, no `uuid.NewSHA1` outside idgen+projection_replication, no zero-part `Anchor()`); `TestFileService_CreateAuditFactCommitted` (EventID == file ID), `TestMessageService_SendAuditFactCommitted` (EventID == message ID), `TestPreferenceService_SetAnchoredEventIDStableAcrossRetries`, `TestReplicateOnceAuditEventIDDeterministic` |
| (c) fail-closed, nil-audit convention | `assertAuditIngestFailed` on every `*AuditFailureFailsClosed` test (code `audit.ingest_failed`, HTTP 503); region promote → `region.promotion_failed` with rollback; nil-audit unit constructions still succeed (no failure) |
| (d) manifest + markers + gates | `make check-audit-manifest && make check-audit-swallow && make check-anchored-event-ids` green after 11 rows + markers |
| (e) fields/constructors/wiring | `make check-composition-root && make build` green; `assertAuditWiring` covers preference/region_fencing/projection_replay (+ conditional projection_replication) |
| (f) unit tests per op | targeted run: `go test ./internal/service/ -count=1 -run 'TestFileService_CreateAudit\|TestFileService_DeleteAudit\|TestFileService_UpdateMetadataAudit\|TestFilePresign_CompleteUploadAudit\|TestMessageService_(Send\|Edit\|ReactToMessage\|UnreactMessage)Audit\|TestMessageService_SendSystemNoAuditFact\|TestPreferenceService_SetAudit\|TestRegionFencing_PromoteAudit\|TestReplicateOnceAudit\|TestReplayProjectionAudit'` |
| (g) AGENTS.md rewritten | `bash scripts/check-audit-swallow.sh` green after the rewrite (FM-16: no present-tense `AuditLogger` references); diff of AGENTS.md reviewed |
| (h) no proto/migration/API changes; done-gate green | `git diff --stat` shows no `api/proto`, `migrations/`, `configs/`, `internal/handler/{http,grpc}` route/method changes; full gate below |

Full done-gate (Docker-free):

```bash
make test && make lint && make lint-strict && make check-file-sizes && \
make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && \
make check-load-scenarios && make check-composition-root && make check-proto-sync && \
make check-integration-compile && make check-architecture && make build
```

Plus, because repository transaction boundaries changed (region promote):

```bash
make test-integration
```

---

## 10. Out of scope / residuals / follow-ups

- **Out of scope by design**: `SendSystem` (tenant-less, no actor, zero production callers) and
  `persistSyncSummary` (disposable projection writes; lifecycle audited at the job layer) — both
  documented in AGENTS.md; `PresignUpload` (no row written); all read paths.
- **Documented residuals of the standalone pattern**: non-idempotent creates/sends after a 503
  (second entity on retry — op semantics unchanged by direction); file delete fact loss on a 503
  retry (needs `FileRepository.GetIncludingDeleted` — follow-up, out of scope); multi-occurrence ops
  append duplicate rows on retry (no dedup contract — never a silently dropped later fact).
- **Explicitly not changed**: migrations, protobuf, config, HTTP/gRPC handlers and routes, op
  semantics beyond the fail-closed audit return.
