# Hardening — Audit-fact invariants for the in-tx audit migration: parity proof, dedup un-fireability, gate non-bypassability, fail-closed validation

| | |
|---|---|
| Extends | `docs/plans/2026-08-06-cmd-server-complete-in-tx-audit-writes-design.md` (design-81e5e5d9) |
| Requirements | `docs/requirements/2026-08-06-cmd-server-complete-in-tx-audit-writes.md` (REQ-1..REQ-7) |
| Head verified | `13e4676` + uncommitted working tree (39 modified files) — every symbol below re-read from the tree |
| Campaign | `aero-id-b2-in-tx-audit` (B2-1 in-tx audit, B2-2 single delivery; gate G3: T-4) |
| Date | 2026-08-06 |
| Status | Hardening — proofs verified with simulated gate runs on the live tree; 6 design corrections (C1–C6) that the design doc must absorb before implementation |

Verdict: all four requested invariants **hold** with the design as written, **provided** the six
corrections C1–C6 are absorbed. C1 (R-1 allowlist conflict) and C2 (new.go construction-order error) are
load-bearing: without them the done-gate breaks at migration step 3/4.

---

## 1. Invariant 1 — `buildLegacyAuditFact` preserves Log field parity + `target_uid`

### 1.1 The adapter contract (verified)

`internal/service/audit_ledger.go:135-141`:

```go
func (s *auditLedgerService) Log(ctx context.Context, legacy *domain.AuditEvent) error {
	if legacy == nil { return nil }
	return s.Record(ctx, AuditFactInput{ActorUID: legacy.ActorUID, Action: legacy.Action,
		ResourceType: legacy.SubjectType, ResourceID: legacy.SubjectID, Payload: legacy.Meta, OccurredAt: legacy.CreatedAt})
}
```

`Record` → `buildAuditFact` (`:84-101`) applies, in order: `bindAuditContext` (TenantID from ctx,
ActorUID fallback, TraceID from ctx) → `normalizeAuditInput` (Source→`"aero-id"`, SourceRegion→`"local"`,
PartitionKey derivation, OccurredAt→now when zero) → `redactAuditPayload`. **Field parity therefore
means: the converted fact must traverse exactly this pipeline.** The design's §3.3 helper must be a thin
wrapper over `buildAuditFact` — never a hand-rolled `domain.AuditFact` literal — or binding,
normalization, and redaction silently drift. Pinned implementation (adds to design §3.3):

```go
// buildLegacyAuditFact reproduces the Log-adapter mapping
// (audit_ledger.go:135-141) plus TargetUID→Payload["target_uid"] and a fresh
// EventID per call (REQ-3.2/3.3). It MUST route through buildAuditFact so
// context binding, source/region/partition normalization, OccurredAt
// defaulting, and payload redaction are byte-identical to the Log path.
func buildLegacyAuditFact(ctx context.Context, actor, action, subjectType, subjectID, targetUID string,
	meta map[string]any) *domain.AuditFact {
	if meta == nil {
		meta = map[string]any{}
	}
	payload := make(map[string]any, len(meta)+1)
	for k, v := range meta {
		payload[k] = v
	}
	if targetUID != "" {
		payload["target_uid"] = targetUID // explicit param wins on collision (none of the 10 sites collide)
	}
	return buildAuditFact(ctx, AuditFactInput{
		ActorUID: actor, Action: action, ResourceType: subjectType, ResourceID: subjectID,
		EventID: idgen.NewID(), Payload: payload,
	})
}
```

### 1.2 Per-site parity table (all ten sites read from the tree)

| Site | Action | SubjectType→ResourceType | SubjectID→ResourceID | TargetUID | Meta→Payload |
|---|---|---|---|---|---|
| admin.go:93 GrantRole | `AuditAdminGrantRole` | `user` | uid | uid | `{"role": roleCode}` |
| admin.go:120 RevokeRole | `AuditAdminRevokeRole` | `user` | uid | uid | `{"role": roleCode}` |
| ban.go:83 Ban | `AuditUserBan` | `user` | uid | uid | `{"reason", "expiresAt"}` |
| ban.go:119 Unban | `AuditUserUnban` | `user` | uid | uid | (none → `{}`) |
| block.go:54 Block | `AuditUserBlock` | `user` | uid | uid | (none → `{}`) |
| block.go:77 Unblock | `AuditUserUnblock` | `user` | uid | uid | (none → `{}`) |
| comment.go:415 ResolveReport | `AuditReportResolve` | `comment_report` | reportID | `c.OwnerUID` | `{"comment_id", "status"}` |
| message.go:313 DeleteMessage | `AuditMessageDelete` | `message` | id | `m.ToUID` | (none → `{}`) |
| file_share.go:114 ShareFile (per grantee) | `AuditFileShareGrant` | `file` | id | grantee g | (none → `{}`) |
| file_share.go:146 UnshareFile | `AuditFileShareRevoke` | `file` | id | granteeUID | (none → `{}`) |

`domain.AuditEvent` (domain/audit.go:31-40) confirms the adapter reads ActorUID/Action/SubjectType/
SubjectID/Meta/CreatedAt and drops TargetUID. All ten sites set a non-empty TargetUID and a zero
CreatedAt, so the converted facts differ from today's stored rows in **exactly two** fields:

1. `event_id`: NULL today (`Log` never sets it; `nullableID(zero)` stores NULL) → fresh UUIDv7 after
   (REQ-3.3, intentional).
2. `payload["target_uid"]`: absent today (adapter drops it) → present after (REQ-3.2, intentional).

Everything else is byte-identical: `source`, `source_region`, `partition_key` (tenant-prefixed
actor-scope, same ctx), `tenant_id`, `actor_uid`, `action`, `resource_type`, `resource_id`,
`occurred_at` (now, microsecond-truncated in `appendInTx`), `request_id`, `trace_id`, redacted payload
bytes (`encoding/json` sorts map keys, so `redactAuditPayload` output is deterministic; `target_uid`
passes `sensitiveAuditKey` — verified: contains no sensitive substring, is not `content`/`body`/`raw`).

### 1.3 The typed-receiver nuance (pins the "durable" premise)

In all six services the ten sites' receiver is **typed** `AuditLogger` (`internal/service/service.go:96-98`;
struct fields verified: admin.go:12-16 `audit AuditLogger` + separate `ledger AuditLedgerService`,
ban.go:26-30, block.go:18-21, comment.go:15-21, message.go:16-22, file.go:21-30), but the composition
root wires the **durable ledger service** into that field: `new.go:194-212,265` pass `auditLedger`
(the `AuditLedgerService`) to `WithFileAuditLogger`/`WithMessageAuditLogger`/`WithCommentAuditLogger`/
`WithBlockAuditLogger`/`WithAdminAuditLogger`/`WithBanAuditLogger`; `auditLedgerService` satisfies
`AuditLogger` via the `Log` adapter. So `_ = s.audit.Log(...)` today really is a swallowed
durable-ledger `Append` (evidence claim #3 stands), and the conversion must **keep the option wiring
unchanged** (design §3.3 does). adminService additionally has `ledger AuditLedgerService` used by the
`ListAudit` read path (`admin.go:56` via `WithAdminAuditLedger`, new.go:212) — the conversion must not
touch that field or its option.

---

## 2. Invariant 2 — fresh per-call EventIDs make the dedup key un-fireable; anchored hold/cutover IDs stay byte-identical

### 2.1 Dedup mechanics (verified)

- `checkAuditDuplicate` (repository/audit_ledger.go:147-159): no-op when `fact.EventID == zero`; else
  SELECT by `(tenant_id, source, event_id)`, `ErrAuditDuplicate` on a hit.
- Final guard: `uq_audit_ledger_tenant_event UNIQUE (tenant_id, source, event_id)` (migration
  000050:15-17); `appendInTx` translates the 23505 on that constraint to `ErrAuditDuplicate`
  (`:183-193`), leaving `uq_audit_publication_fact` violations raw (internal corruption).
- `appendInTx` runs the duplicate pre-check, then `pg_advisory_xact_lock(hashtext(partition))`, then the
  duplicate post-check — the post-lock check is what serializes same-partition races.

### 2.2 Un-fireability proof for the ten converted sites

1. `buildLegacyAuditFact` mints `idgen.NewID()` (uuidv7: 48-bit millisecond timestamp + 74 random
   bits) **per call**. No input to the helper is reused as a derivation part, so no retry, replay,
   crash-restart, or double-execution path can re-derive a previously stored `event_id`. The dedup key
   is un-fireable **by construction** — there is no code path in the design that produces a matching
   `(tenant_id, source, event_id)`.
2. The only theoretical trigger is a UUIDv7 collision (≈2⁻⁷⁴ for two calls in the same millisecond;
   ≈2⁻¹²² overall). That is the same risk class as the `id`/`fact_id` primary keys themselves; the
   Class-B in-tx swallow would then commit the domain write with one fact dropped — an accepted,
   cryptographically negligible residual, and worth one line in the design's failure-mode table
   (it is not currently stated).
3. Consequence the design already banks on: `ErrAuditDuplicate` in-tx (Class B) is a **defensive dead
   path** for these ten sites, so the D3 hazard (stable EventID reused across distinct intents rolling
   back a legitimate domain write) cannot recur.
4. At-least-once scoping: two distinct user intents (e.g., a client retry without an Idempotency-Key)
   produce two facts — exactly today's Log behavior and the documented at-least-once contract; this is
   not a dedup-key duplicate.

### 2.3 Byte-identity proof for anchored hold/cutover IDs

Chain of custody, all verified in the tree:

1. Anchor inputs are pinned by the current service code, which the WithAudit conversion keeps
   verbatim (design §3.3: "identical AuditFactInput it builds today"): `audit_hold.set` 6 parts
   `[audit_hold.set, tenant(ctx-bound), scopeType, scopeID(trimmed), reason,
   RenderAnchorTime(expiresAt)]` (audit_hold.go:73-79), `audit_hold.removed` 4 parts (audit_hold.go:99),
   `legacy.cutover.set` 4 parts `[legacy.cutover.set, tenant(ctx-bound), status, previous]`
   (legacy_cutover.go:104-107). The ctx-bound tenant resolution and trimmed scopeID are already
   documented in-tree as the post-normalization values.
2. `idgen.Anchor` is a pure function: `uuid.NewSHA1(AnchorNamespace, NUL-joined parts)` — identical
   parts ⇒ identical UUIDv5, byte-identical across processes/regions/retries (idgen.go:38-52; NUL
   separator prevents prefix collisions).
3. The fact pipeline never mutates `EventID`: `buildAuditFact` (`:84-101`), `normalizeAuditInput`
   (`:103-124`), `appendInTx` (`:130-146`, mutates only ChainSequence/PreviousHash/RecordHash/
   OccurredAt/CreatedAt), `auditLedgerFactToRow` → `nullableID` (`:277-283`). The repo appends the fact
   exactly as built (design §3.2: "the repo never re-derives anchors").
4. Payload byte-parity across the same call: `redactAuditPayload` copies deterministically and
   `encoding/json` sorts map keys ⇒ identical `payload_hash`; `appendInTx` truncates `occurred_at` to
   microseconds before hashing — the stored `event_id` is unaffected by any of this.
5. Crash-retry semantics: a retry re-derives the same anchor → post-lock `checkAuditDuplicate` (or the
   unique constraint, cross-partition) → `ErrAuditDuplicate` → in-tx swallow **after the domain write**
   → domain row commits, exactly one fact, one publication row. This is byte-for-byte today's
   `Record` + `ErrAuditDuplicate→nil` semantics minus the crash window (REQ-4.2).
6. Cross-partition race: same anchor ⇒ same tenant/scope, but `partition_key = tenant:actor`, so two
   concurrent same-anchor calls from different actors take different advisory locks; the pre-checks
   cannot see the uncommitted winner; the unique constraint catches the loser and translates to
   `ErrAuditDuplicate` → swallow → commit. Verified equivalent to today's out-of-tx behavior.

---

## 3. Invariant 3 — the P1/P2 gate regexes and wiring assertions cannot be bypassed

### 3.1 Simulated baseline on the live tree (proof the hole and the fix)

| Run | Result |
|---|---|
| Current gate (`scripts/check-audit-swallow.sh internal/service`) | GREEN — while 10 `_ = s.audit.Log` sites exist (old P1/P2 method lists `Record\|Publish\|FinishPublication\|ClaimPublication` exclude `Log`; the negative fixture documents the exclusion) |
| Proposed P1 receiver alternative `^[[:space:]]*(_ = )s\.audit\.` | exactly **10 hits** — the design's claimed baseline is exact |
| Proposed P2 receiver alternative `^[[:space:]]*s\.audit\.` | **0 hits** — no false positives on the residual checked sites (all five residual `Record` sites + `account_profile.go:62` start with `if err :=`/`return`, verified) |
| `_ = publisher.Publish` whole-tree | 0 (verified) |

### 3.2 Bypass vectors found and the hardened patterns that close them

| # | Bypass vector | Why it evades the design as written | Fix (must land in the script) |
|---|---|---|---|
| B1 | `_ = s.ledger.Log(...)` / `_ = r.audit.Log(...)` (renamed receiver) | receiver-based `s\.audit\.` misses any other receiver name; `Log` is not in the P1/P2 method lists | add `Log` to **both** method lists (`Record\|Log\|Publish\|FinishPublication\|ClaimPublication`); post-conversion there are zero `.Log(` calls in `internal/service`+`internal/repository` (verified: only the 10 swallow sites today; definitions and the `AuditLogger` interface lines start with `func`/`Log(` — no leading dot — so no false positives) |
| B2 | `_, _ = s.audit.Log(...)` double-assign | design text shows only `_ = s\.audit\.` | use `(_ = |_, _ = )s\.audit\.` (or the general form below) |
| B3 | `x, _ = s.audit.Record(...)` (named-var assign-swallow) | neither pattern | add `^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*[[:space:]]*, _ = s\.audit\.` (this one alternative also covers `_, _ = `) |
| B4 | `go s.audit.Record(...)` / `defer s.audit.Record(...)` | script comment documents "anchoring naturally excludes go/defer" — but both discard the error (deferred/goto-return values are dropped), i.e. they are swallows | add `go[[:space:]]+s\.audit\.` and `defer[[:space:]]+s\.audit\.` to P2. Verified zero legitimate uses in the tree |
| B5 | `_ = (*s).audit.Record(...)` parenthesized receiver | `s\.audit\.` and the `[A-Za-z_]…` chain regex both fail on `(*s)` | add `\(\*[A-Za-z_][A-Za-z0-9_]*\)\.audit\.` alternatives to P1 and P2 |
| B6 | `s.audit . Log(...)` (whitespace around the selector — legal Go) | `s\.audit\.` requires the dot immediately after `audit` | make receiver patterns whitespace-tolerant: `s[[:space:]]*\.audit[[:space:]]*\.` (still cannot match `s.audit != nil` because a `\.` is required after `audit`) |
| B7 | `_ = s.audit.` + newline + `Log(...)` (split call) | old P1 method-list needs `(` on the same line | already closed by the receiver-based alternative (no `(` required) — keep it `(`-free |
| B8 | `_ = r.audit.AppendInTx(tx, f)` in a future repo file | the gate scans only `internal/service` | add `AppendInTx` to both method lists **and** extend the scan to `internal/repository` (Makefile: `check-audit-swallow: bash scripts/check-audit-swallow.sh internal/service internal/repository`; script loops roots). Verified 0 hits of the hardened patterns in `internal/repository` today. **Do not scan all of `internal/`**: P2's method-list `Record` then false-positives on `c.breaker.Record(...)` in `internal/sso/client.go:335,342,346` (circuit-breaker, not audit) |

Remaining documented residuals (accepted; the gate is a source-text tripwire, the real enforcement is
the compile-time signatures + REQ-7.2 behavioral test): receiver **field renames** are caught for all
current method names (B1) but a brand-new method name on a renamed receiver would need a new gate line;
a token-split identifier (`s . audit`) is pathological; the legacy unwired tree
(`internal/{file,message,comment,block,notification,search,audit}`) stays unscanned (documented).

### 3.3 Fixture updates (self-tests must prove each new pattern)

- **positive/fixture.go** gains: `_ = s.audit.Log(ctx, event)`, bare `s.audit.Log(ctx, event)`,
  `go s.audit.Record(ctx, f)`, `defer s.audit.Log(ctx, e)`, `x, _ = s.audit.Record(ctx, f)`,
  `_ = s.audit.` + newline + `Log(ctx, event)`, `_ = (*s).audit.Record(ctx, f)`,
  `s.audit . Log(ctx, event)`, and repository-shaped `_ = r.audit.AppendInTx(tx, f)` /
  `_ = r.audit.Log(ctx, e)` (already contains `_ = publisher.Publish(ctx, fact)`).
- **negative/fixture.go** **drops** the line `_ = s.audit.Log(ctx, event) // legacy diagnostics
  surface: excluded` — it now matches and the self-test would fail. Keeps: `if err := s.audit.Record…`,
  `return s.audit.Record…`, `err := s.audit.Record…`, `err = s.audit.Record…`, comment lines, and bare
  `d.pusher.Publish(ctx, ev)` (Publish stays out of P2 — SSE hub).
- Fix the stale "Baseline on today's tree" header comment (it still names the removed
  `audit_ledger.go:79` loop): baseline is 10 `_ = s.audit.Log` (invisible to the old gate) + 0 others;
  target 0.

### 3.4 Wiring assertions — formulations that cannot be bypassed

| Assertion | Formulation (all self-verifying: a broken pattern yields count ≠ expected ⇒ red) |
|---|---|
| (a) single `drainPublications` call site | `grep -rn "drainPublications(" internal/service --include='*.go'` minus `_test.go` minus the definition line (`^internal/service/callback_audit\.go:.*func \(s \*callbackService\) drainPublications\(`); remaining count must be **1** and the line must be `internal/service/callback.go:…s.drainPublications(workerCtx)` (inside `Start`; verified tree state: definition callback_audit.go:47 + sole call callback.go:282). A rename or a second delivery path makes the count 0 or 2 ⇒ red. Semantic renames are caught by the REQ-7.2 behavioral test (fake `AuditPublicationRepository` recording `ClaimPublication`; only `Start` drives it) — the mechanical assertion and the behavioral test are a pair, not alternatives |
| (b) single-arg `NewAuditLedgerService` | non-test occurrences must be exactly 2: the definition (`internal/service/audit_ledger.go:56 func NewAuditLedgerService(repo repository.AuditLedgerRepository)`) and the single call (`internal/app/audit.go:51 service.NewAuditLedgerService(auditLedgerRepo)`); every non-definition line must contain `)` on the same line (kills the multi-line `NewAuditLedgerService(\n repo, pubs)` evasion) and no comma inside the parens; count ≠ 2 ⇒ red. The compile-time single-arg signature remains the real enforcement |
| (c) `_ = publisher.Publish` stays 0 | `grep -rn "_ = publisher.Publish" internal --include='*.go'` count == 0 (verified 0 today; also covered for `internal/service` by P1's `Publish` union) |

---

## 4. Invariant 4 — fail-closed policy: no mutation commits without its durable fact; no swallowed or duplicate facts

### 4.1 No mutation without its durable fact (three independent layers, all verified)

1. **Startup**: `requireAuditLedger` (repository/audit_wiring.go:19-28) panics on nil and on
   typed-nil (`reflect` check); the eight constructor changes (§3.1 of the design) route every new
   repo through it. A repo without a ledger cannot exist.
2. **Runtime**: every converted service uses the fail-closed head `repo, ok := s.blocks.(
   repository.BlockAuditRepository); if !ok { return apperr.Internal("block.audit_unavailable", …) }`
   — no silent plain-write fallback (matching the landed `finishStep`/`setOperationStatus` shape). In
   production the concrete repos always satisfy the assertion; mocks either set `s.audit == nil` or
   gain `WithAudit` fns (design F3).
3. **In-tx**: `XxxWithAudit` runs domain write → `AppendInTx` in one gorm tx. Any non-duplicate
   append failure (signing-key misconfig, DB error, publication-row insert failure) propagates and
   rolls back the domain row with the fact — proven by the existing trigger-injection test
   `TestAuditWiringPublicationInsertRollsBackLedgerRow` (audit_wiring_integration_test.go:268) and
   generalized by REQ-6's per-mutation matrix. The `s.audit == nil` plain path exists only in unit
   tests; production wiring passes the ledger into every option (`new.go:194-212,265`) and constructor
   arg (hold/cutover), so `s.audit` is never nil in the wired root.
4. **Residual out-of-tx set stays checked**: verified all six sites (`account.go:155,186`,
   `operation_state.go:39`, `replay.go:145,240`, `event_ingest.go:154`, plus the to-be-deleted
   `account_profile.go:62`) are `if err :=`/`return` shapes — never `_ =`; the gate (P2) also proves
   no bare statement exists.

### 4.2 No swallowed facts

Post-conversion: zero `_ = s.audit.` / `_ = publisher.Publish` in `internal/service` +
`internal/repository`, enforced by the hardened gate (target 0; today exactly 10, all `Log`-shaped,
proven invisible to the old gate in §3.1). The design's Class-B in-tx swallow is the **only**
error-discarding point left, and it is confined to `ErrAuditDuplicate` after a successful domain write
in the same tx — it can never swallow a *failed* append (any other error rolls back) and never masks a
failed domain write.

### 4.3 No duplicate facts

- **Anchored (hold/cutover)**: dedup key + unique constraint ⇒ exactly one fact per anchor, including
  crash-retry (§2.3.5) and cross-partition races (§2.3.6). One publication row per fact
  (`uq_audit_publication_fact UNIQUE (fact_id)`, migration 000045:12; ledger row + publication row
  created in the same tx, `createAuditRows` :172-200).
- **Fresh-ID (ten sites)**: dedup un-fireable by construction (§2.2); N-grantee file share yields N
  facts + N publication rows in one tx with one advisory lock (single actor partition).
- **Distinct intents produce distinct facts** (at-least-once, unchanged from today) — explicitly not
  a dedup violation.

### 4.4 Short-circuit parity (facts fire exactly where they fire today)

Verified against each plain repo method: 0-row **no-op successes** log today and must keep logging
(admin GrantRole/RevokeRole on absent role, ban Ban upsert/Unban on unbanned, block Block
upsert/Unblock on absent block, file Revoke on absent grantee — all plain methods return nil on 0
rows); **short-circuits before the Log site** must stay short-circuits (message delete of a missing
message — the service pre-loads and errors first, message.go:295-311; file share with zero grantees —
`return nil` before the loop, file_share.go:101-104; comment report missing — plain `ResolveReport`
returns `ErrNotFound` on RowsAffected==0, comment.go:370-385, service returns before the Log site).
The design's §3.2 phrase "resolve with no status change" is **wrong** (see C4) but harmless once
corrected.

---

## 5. Design corrections (C1–C6) — must be absorbed before implementation

| # | Finding (verified) | Correction |
|---|---|---|
| **C1** | **Cross-gate conflict, load-bearing**: `scripts/check-anchored-event-ids.sh` R-1 bans the literal `EventID: idgen.NewID()` in non-test Go except the C2 allowlist `internal/service/callback.go` (grep verified: the only current site is callback.go:261). The design's `buildLegacyAuditFact` mints `EventID: idgen.NewID()` ⇒ `make check-anchored-event-ids` (in the done-gate, Makefile:60-62) **fails the moment the helper lands** | Extend the allowlist to `internal/service/audit_fact.go` with the C2 rationale restated (fresh per-intent ID, no natural anchor — REQ-3.3), converting the single-entry `grep -v "^$C2_ALLOWLIST:"` to a `grep -vE` alternation over the allowlist entries, and update the R-1 header comment ("sole allow-list entry" → two documented intent sites). Do **not** evade via `id := idgen.NewID(); EventID: id` — that defeats the gate's intent; the allowlist is the honest fix and the `allow=0` self-test keeps it load-bearing. This goes into migration step 1 |
| **C2** | **Construction-order error in the design's §1 table**: `NewFileShareRepository(gdb)` is at `new.go:132`, **before** `buildAuditStack` at `:134` — `auditLedgerRepo` is not yet in scope, so the design's "fileShare :132" reads as if it were after the stack. (msg :141, comment :142, block :144, ban :145, admin :148 are after — correct) | Migration step 4 must **move the `fileShareRepo` construction to after `buildAuditStack`** (e.g., next to `msgRepo` at :141) or move `buildAuditStack` earlier (controlMetrics exists at :96; cfg/gdb in scope). The other seven call sites need no reorder |
| **C3** | `buildAuditComplianceServices` (app/audit.go:145-149) declares its second param as `ledgerRepo repository.AuditLedgerRepository` — that interface **does not include `AppendInTx`**, so `NewAuditHoldRepository(gdb, ledgerRepo)` (design §3.5) would **not compile** | Change the param type to `repository.FullAuditLedgerRepository` (the type `buildAuditStack` returns; new.go:366 already passes it) |
| **C4** | Design §3.2 "unconditional fact append … resolve with no status change" is wrong: plain `ResolveReport` unconditionally UPDATEs status/resolved_at/resolved_by (comment.go:370-377), which affects 1 row even when the status is unchanged (PostgreSQL counts matched rows); RowsAffected==0 occurs only for a **missing report** → `ErrNotFound` → service returns before the Log site. The unconditional-append claim holds for admin/ban/block/file only | Rephrase §3.2: facts fire on every successful (non-ErrNotFound) domain write; the missing-report/missing-message/zero-grantee short-circuits keep parity. Also pin: the conditional hide is an unconditional `UpdateColumn("status", …)` (comment.go:311-322) — a concurrent comment deletion is the only 0-row hide ⇒ in-tx it rolls back the resolve (the §4.5 documented change, now verified against the implementation) |
| **C5** | adminService has **two** audit fields: `audit AuditLogger` (the ten-site receiver; wired with the ledger adapter via `WithAdminAuditLogger`, new.go:211) and `ledger AuditLedgerService` (`WithAdminAuditLedger`, new.go:212; used by `ListAudit` at admin.go:56) | State explicitly in §3.3: convert only the two `s.audit.Log` sites in admin; leave `s.ledger` and `WithAdminAuditLedger` untouched (read path, checked, out of scope) |
| **C6** | Gate receiver-name dependency: all six converted services keep the field name `audit` (verified structs + option setters assign `s.audit = a` only). The gate's `s\.audit\.` patterns silently weaken if a future service renames the field | Document the constraint in the script header: field rename requires updating the gate; the method-list union (with `Log`, B1) still catches `_ =`/bare swallows of all current audit method names on any receiver, so the tripwire degrades gracefully |

---

## 6. Migration-step deltas (amendments to design §6)

1. **Step 1 (gate first)** additionally: (a) C1 — extend the R-1 allowlist in
   `check-anchored-event-ids.sh` (ERE alternation + header comment); (b) §3.2/3.3 pattern hardening
   (B1–B8: `Log` in both method lists, `_, _ =`/`x, _ =`, `go`/`defer`, `(*s).`, whitespace-tolerant
   receiver, `AppendInTx` in the lists); (c) fixture updates per §3.3; (d) scan scope
   `internal/service internal/repository` (Makefile passes both roots; script loops); (e) wiring
   assertions per §3.4. `make check-audit-swallow` must now fail with **exactly 10 hits** and
   `make check-anchored-event-ids` must still pass.
2. **Step 2 (repo layer)** unchanged, plus C3's parameter-type change in `buildAuditComplianceServices`
   (step 4 area, but compile-coupled to the constructor signature).
3. **Step 3 (services)**: pin `buildLegacyAuditFact` as the §1.1 wrapper over `buildAuditFact`; C4
   wording; C5 admin note.
4. **Step 4 (wiring)**: C2 — reorder `fileShareRepo` construction after `buildAuditStack`.
5. **Steps 5–6** unchanged; the T-4 matrix gains the file-share N-facts/N-rows assertion (already in
   the design) and the anchored byte-identity assertions (already in the design).

## 7. Acceptance mapping additions

| Acceptance | Artifact |
|---|---|
| Gate proves the hole: old gate green on 10 `_ = s.audit.Log`; hardened gate red with exactly 10; target 0 | §3.1 simulation (reproducible with the §3.2/3.3 patterns + fixtures) |
| Dedup un-fireability + anchored byte-identity | §2.2/§2.3 proofs; T-4 commit-side assertions (correct `event_id`) + duplicate-injection rollback test keep the constraint; `make check-anchored-event-ids` stays green with the extended allowlist |
| No-swallow + no-duplicate + fail-closed | §4.1–4.4; REQ-6 matrix; REQ-7.2 `Start`-only drain test; `TestAuditLedgerRecordEnqueuesOnlyIntegration` (app/audit_integration_test.go:49) stays green |
