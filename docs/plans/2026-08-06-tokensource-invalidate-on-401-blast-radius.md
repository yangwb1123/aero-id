# Blast-radius analysis — revised tokensource-invalidate-on-401 design (M3a/M3b, binary-only claim, Publish ownership)

| | |
|---|---|
| Analyzes | `docs/plans/2026-08-06-tokensource-invalidate-on-401-epoch-singleflight.md` as revised by the security review (M1/M2/M3a/M3b) |
| Method | Composition-root wiring (`internal/app/new.go`, `app/audit.go`), repository SQL (`internal/repository/audit_ledger.go`, `callback.go`), migrations (000036, 000045), ticker/drain (`internal/service/callback.go`, `callback_audit.go`), billing precedent (`internal/connector/billing/client.go`) — all re-read against the live tree on 2026-08-06 |
| Scope | (1) M3a "feed the durable drain from inline failures" blast radius, (2) binary-only claim re-validation, (3) Publish body-close ownership + 401-branch effects |

## 0. Headline: the shared premise of the prior reviews is factually wrong

Three verified facts change the entire blast-radius picture. **All prior reviews (design FM-9, security F-A/Q3,
concurrency FM-3b) analyzed a publish topology that does not exist in the production binary.**

**T-1 — The durable drain is live and is the *only* live publisher.** Every `Append` creates an
`audit_publications` row (`status='pending'`, `next_attempt_at=now`) inside the same transaction
(`createAuditRows`, internal/repository/audit_ledger.go:168-171). The claim/retry machinery was shipped with the
ledger (first appearance in `aafd7df`, never gated). The ticker is started unconditionally (new.go:179,
`worker_interval` 1 s), `SetAuditPublicationRepository` is applied unconditionally (app/audit.go:25-28), and
`drainPublications` early-returns only when `s.publications == nil` — never in production. **The security
review's F-A ("nothing inserts audit_publications rows; the drain is dead code") is false.**

**T-2 — The "legacy inline path" is dead in production.** `auditLedger := service.NewAuditLedgerService(auditLedgerRepo)`
(new.go:146) passes **no publishers**; the ledger has no publisher setter and `wireAuditServices` hands
`auditPublishers` only to the callback service (app/audit.go:32-34). `Record`'s `_ = publisher.Publish(ctx, fact)`
loop (audit_ledger.go:79) iterates an empty slice. The design's own E8 ("same publisher slice goes to **ledger and
callback service**") is wrong on the ledger half. With `audit_governance.enabled=true` the *drain* performs the
governance publish (callback_audit.go:108); with `enabled=false` the drain marks rows completed with zero
publishers (and still enqueues callback deliveries via `enqueueFact`).

**T-3 — The drain retry ladder is bounded and backoff-shaped, not the "no attempt cap" loop the reviewers feared.**
`publicationOutcome` caps at `MaxAttempts` (8; callback.go `MaxAttempts` default) with `retryDelay` 1 s → 1 h
exponential + jitter (callback.go:319-337); `dead_letter` rows are never re-claimed (claim predicate in
repository/audit_ledger.go:218-220 admits only `pending`+due or `delivering`+lease-expired). The
"1 refetch per audit fact, no backoff, no cap" FM-3b hazard exists only on the *unwired* inline path.

Consequence: the design's changes execute on the **drain path**, which already has backoff, a cap, and at-least-once
mechanics — so most of the feared feedback loops are structurally damped before the 401-branch is even added. The
analysis below re-derives each question under the corrected topology.

---

## 1. Q1 — M3a blast radius under the corrected topology

### 1.1 M3a is unnecessary; implementing it is a no-op with zero blast radius

M3a ("on inline publish failure, enqueue the fact into `audit_publications` so the drain becomes live") is
predicated on F-A, which is false:

- Rows already exist for **every** fact — including every inline-failed fact — created in the same transaction as
  the ledger append (T-1). The drain already retries whatever the inline path (were it wired) failed on.
- `UNIQUE (fact_id)` (migrations/000045) plus the existing row means any insert-on-failure must be
  `ON CONFLICT DO NOTHING` and would always conflict. Pure dead code.
- The one *live* sense of "feed": if a future direction #1 wires publishers into the ledger, the inline loop gains
  real behavior — but it will then be publishing facts the drain already publishes, i.e. duplicate deliveries that
  the sink dedups by `IdempotencyKey = fact.ID.String()` (publisher.go:185, unchanged by this design). No new
  machinery is needed either way.

**Blast-radius verdict for M3a: none (no-op).** It should be dropped from the revised scope; the security review's
direction-#1 dependency claim ("the design's recovery/observability story runs on an unfed drain") is inverted —
the drain is the recovery story and is already fed.

### 1.2 Feedback loop: drain publish → 401 → Invalidate → refetch during drain backoff

The loop the question asks about **does exist, but on the drain's own retry ladder**, and each leg is bounded:

| Leg | Behavior after the design lands | Bound |
|---|---|---|
| Drain retry `i` publishes with cached token | 401 → `Invalidate` (epoch++, cache cleared) → `ErrTokenRejected` → `publicationOutcome` → `pending` + `retryDelay(i)` → `FinishPublication` | Per-row ladder: 1 s, 2 s, 4 s, 8 s, 16 s, 32 s, 64 s, 60 m → `dead_letter` at attempt ≥ 8 (~62 min to terminal). ≤ 8 publishes + ≤ 8 refetches per fact |
| Next retry refetches | cache miss (invalidated) → singleflight epoch group → one fetch serves the wave | ≤ 1 token-endpoint call per tenant per tick (epoch-keyed dedup, `DoChan("client-%d")`) |
| ≤ 50 rows claimed per tick 401 in the same wave | N concurrent 401s → N idempotent `Invalidate` calls (one epoch bump) → next tick joins one group | 1 refetch per tick per tenant, not 50 |
| New facts arriving during the ladder | Each fact = its own row = its own ladder; singleflight dedups per tick | Token-endpoint load ≤ 8 × facts-per-hour worst case. At 50 facts/s: 400 fetches/h ≈ 0.11/s — negligible |

The one genuine unbounded term is **not** the drain: it is the *token cache being shared*, so an inline
invalidation (if the inline path ever becomes wired by direction #1) would force drain retries to refetch on every
backoff step. In the current binary that coupling is absent (T-2). **M1 (per-tenant refetch cooldown reusing
`refreshSkew`) remains good hardening** — it converts "drain retry always refetches during a revocation window"
into "refetch at most once per cooldown" — but it is **not load-bearing** under the corrected topology, and it must
be implemented so it also covers drain-triggered refetches (i.e. in `accessToken`/`fetchToken`, not in the inline
loop). Note the semantic conflation to document: `refreshSkew` is a refresh *lead-time*; reusing its value as a
*minimum refetch interval* is a constant, not a policy — do not add a new config key (see Q2).

### 1.3 Claim/Finish idempotency and at-least-once — verified sound, with one invisibility gap

- **Claim**: `FOR UPDATE SKIP LOCKED`, lease 5 min, `attempt+1` per claim (repository/audit_ledger.go:209-231).
  Two workers can never hold the same row (`lease_owner` set under row lock).
- **Finish**: `UPDATE ... WHERE id AND status='delivering' AND lease_owner=worker`; `RowsAffected != 1` →
  `ErrAuditPublicationLeaseLost`, which `finishPublicationAttempt` ignores (callback_audit.go:81). Idempotent in
  effect: a duplicate/late Finish is a harmless no-op error; the row re-enters the claim pool after lease expiry.
- **At-least-once**: crash between publish-success and Finish → row stays `delivering` until lease expiry →
  re-claimed → duplicate publish → sink dedups by `idempotency_key = fact.ID`. Consistent with the stated
  contract; the design's C-2 holds.
- **Retry re-runs `enqueueFact`** (callback_audit.go:100, before the publish loop): idempotent, because
  `EnqueueDelivery` uses `clause.OnConflict{DoNothing: true}` (repository/callback.go:160-161) against
  `UNIQUE (subscription_id, event_id)` (migrations/000036:31). No duplicate tenant callbacks on retry. No
  migration needed.
- **Gap (pre-existing, now visible)**: `dead_letter` is **terminal and metric-less**. The claim predicate never
  re-admits it, no replay exists for audit publications (only `ResetDelivery` for callbacks), and
  `finishPublicationAttempt` calls no metrics — `aero_control_callback_deliveries_total{status="dead_letter"}`
  is emitted only by `markDelivery` (callback.go:314). The design's FM-3/§5 runbook claim that dead-lettered
  publications are "observable via `aero_control_callback_deliveries_total{status=dead_letter}`" is wrong
  (security review F-B stands). **Required fix (code-only):** emit a drain-outcome metric from
  `finishPublicationAttempt` (e.g. `ObserveAuditPublicationOutcome(status)` following the
  `ObserveCallbackOutcome` pattern), or the runbook's "dead-letter ladder" discriminator — and the
  scope-missing-vs-token-endpoint split — has no signal at all.

### 1.4 Ticker load

- The ticker already does, in every deployment, per fact: 1 claim + 1 finish SQL (+ `enqueueFact`); with
  `enabled=true` also 1 HTTP publish. **The design adds no row-processing load**; it only changes the failure-mode
  behavior (refetch instead of reusing a poisoned token), at ≤ 1 extra token-endpoint call per tenant per tick
  during revocation windows (1.2).
- Throughput ceiling: 50 rows/tick at 1 s interval = 50 facts/s sustained; above that the backlog grows. Pre-existing.
- **Bulkhead contention (pre-existing, worth stating in the design)**: publications and callback deliveries share
  `WorkerConcurrency` (default 4, cap 64). A tick claiming 50 rows against a *slow* governance endpoint (5 s
  timeout each) occupies the bulkhead ≈ 50/4 × 5 s ≈ 62 s, and `drain` runs only after `drainPublications` in the
  same tick (callback.go:264-266) — callback deliveries can be delayed by minutes during transport-level
  outages. 401 waves themselves are fast (rejected responses), so the design does not worsen this; the worst case
  is unchanged from today's `enabled=true` behavior.
- `FinishPublication` errors are ignored by design (`_ =`), so a lease-lost row is retried — no stuck rows.

### 1.5 Interaction with the missing attempt cap (FM-3b) — re-derived

Under the corrected topology the FM-3b hazard ("1 refetch per audit fact, no cap, no backoff") does not exist in
the production binary (T-2, T-3). What remains, in `enabled=true` deployments, during a persistent governance 401
with a healthy token endpoint: each fact dead-letters after ≤ 8 publishes + ≤ 8 refetches over ~62 min, and the
facts keep flowing — that is a bounded, observable (once 1.3's metric lands) degradation, not a loop. **M3b (inline
publish-failure metric in `Record`'s loop) monitors unwired code in production** — it would never increment; it
should be dropped in favor of the drain-outcome metric, or kept only as a guard for future direction-#1 wiring.

---

## 2. Q2 — "Binary-only rollout, no config/proto/migration" under the revised scope

Revised scope = design §2 (types/oauth/publisher/metrics/emission) + M1 (cooldown reusing `refreshSkew`) + M2
(token-endpoint-vs-scope discrimination) + drain-outcome metric (1.3) + test fixes (post-invalidate second
`AccessToken` interleaving; 401-with-scope-body precedence assertion; FM-8 retention correction).

| Revised-scope item | Config | Proto | Migration | Behavior in `enabled=false` deployments |
|---|---|---|---|---|
| `Invalidate` + epoch + epoch-keyed singleflight (oauth.go) | none | none | none | **Unreachable** — publisher is never constructed when `audit_governance.enabled=false` (app/audit.go:44-46) |
| 401 branch + `classifyPublishError` + body-close move (publisher.go) | none | none | none | Unreachable (same gate) |
| Sentinels + interface method (types.go) | none | none | none | Compile-time only |
| New metric counters + seam (control_plane*.go) | none | none | none | Exist, never incremented |
| REQ-5 emission in `publishAuditFact` (callback_audit.go) | none | none | none | Loop over zero publishers → no emission |
| M1 cooldown | **none if it reuses `refreshSkew`** — must not add a key | none | none | Unreachable |
| M2 discrimination (error-code mapping in `publicationOutcome` if adopted) | none | none | none | Changes `error_code` text on rows only when `enabled=true` |
| Drain-outcome metric (1.3) | none | none | none | Counter never incremented |
| M3a feed-the-drain | none | none | none | **No-op by construction** (T-1, UNIQUE(fact_id)) — drop it |

Schema ground truth: `audit_publications` ships in migrations/000045 (already applied in any deployment that ran
`cmd/migrate`; `database.auto_migrate` is off by default, but the drain/ledger shipped together in the R11-era
commit `aafd7df` and the coverage query at app/audit.go:66-78 already reads the table at startup when enabled).
`callback_deliveries` idempotency constraint ships in 000036. `enqueueFact` idempotency needs no new index.
No `.proto` is touched.

**Verdict: the binary-only claim holds — with two corrections to its *argument*, not its conclusion:**

1. The claim must not rest on "the drain is dead code, so nothing changes at runtime." The truth is stronger: the
   drain is live, and the new code is fully gated behind the *existing* `audit_governance.enabled` flag; in
   `enabled=false` deployments every new symbol is unreachable; in `enabled=true` deployments the only behavior
   deltas are the intended ones (invalidate+refetch on 401, scope-missing metric, optional error-code/M1/M2
   refinements). Rolling the binary is safe in both modes without touching config.
2. The rollout gate remains the pre-existing config flip `audit_governance.enabled` — "binary-only" means *no new
   keys*, not *no config at all*. Also note (pre-existing, document in the runbook): in `enabled=false`
   deployments the drain consumes rows and marks them `completed` without publishing; flipping the flag later does
   **not** backfill those facts — governance coverage starts at flip time.

The `make test-integration` exemption also still holds: no repository SQL or migration changes in the revised scope
(all new logic is in-package unit-testable; M2's error-code mapping lives in `publicationOutcome`, which is
unit-tested without a database).

---

## 3. Q3 — Publish body-close ownership + 401-branch effects on callers and the billing precedent

### 3.1 Caller inventory and close-ownership change

`auditgovernance.Publisher.Publish` has exactly two call sites, both through the `service.AuditPublisher`
interface, both error-only consumers that never read the response body:

- `auditLedgerService.Record` (audit_ledger.go:79) — **unwired in production** (T-2).
- `callbackService.publishAuditFact` (callback_audit.go:108) — the live path.

The close-ownership move (`defer response.Body.Close()` from `validateReceipt` at publisher.go:221 to `Publish`,
per F7 and §2.3) is therefore **unobservable to every existing caller**. Verified additionally: no test calls
`validateReceipt`/`decodeReceipt` directly (grep of auditgovernance `*_test.go` = 0 hits), and no test asserts the
401 error string (F2). The 202 path is preserved exactly: `classifyPublishError` returns nil on 202 **without
reading the body**, so `decodeReceipt` still sees a fresh body; the non-202 branch of `validateReceipt` becomes
unreachable dead code (harmless — keep it as defense or delete it; the design should pick one to avoid lint drift).
The bounded read (`maxReceiptBytes+1` = 64 KiB + 1) prevents memory abuse on the new classification path, and the
non-drain of oversized bodies merely forfeits connection reuse for that request — identical to today's non-202
behavior.

One real, if small, blast-radius item: the **error string for 401 changes** from `"audit governance: HTTP 401"` to
`"audit governance: token rejected: HTTP 401"`. Nothing asserts it (F2), the inline loop swallows it, and the drain
persists the constant `error_code = "audit.publication_failed"` (callback_audit.go:84-94) — **DB rows are
unchanged**. If M2 is adopted, `publicationOutcome` should map `ErrTokenUnavailable` to a distinct code (e.g.
`audit.token_unavailable`) to make the runbook's token-endpoint-vs-scope discriminator real; that is the only place
where the new sentinels can legitimately change stored state.

### 3.2 Shared-cache coupling introduced by the 401 branch

`Invalidate` mutates the single per-tenant `tenantTokenSource` shared by every publisher in the slice (one
`OAuthTokenSource` per tenant binding; app/audit.go:50-58). Under the corrected topology the only consumer is the
drain, so the invalidation feedback is contained to the drain's own retry ladder (1.2). Two things the design must
state explicitly:

- If direction #1 later wires the same publisher into the ledger, a drain-observed 401 will force *inline*
  publishes to refetch, and vice versa — the two paths become coupled through the cache. That is the intended
  mechanism (shared invalidation), but it is new cross-caller coupling that M1 (cooldown) is what makes safe.
- Per-tenant isolation holds: `Invalidate` resolves the binding and no-ops for unbound tenants or mismatched
  `SourceSystem` (design §2.2, REQ-1), so a 401 for tenant A cannot trigger a refetch for tenant B. The drain
  claims rows for all tenants, and the epoch guard is per-tenant — no cross-tenant interference.

### 3.3 Divergences from the billing 401/403 precedent — to be documented, not harmonized

Billing precedent (client.go:270-282, verified): `decodeBillingError` maps body codes
`invalid_token` / `insufficient_scope` / `metering_source_unauthorized` → `ErrUnauthorized`, and *any* 401/403
status → `ErrUnauthorized` fallback; classification only, **no invalidation**; billing's own oauth.go has zero
`Invalidate` symbols. The revised design's `classifyPublishError` diverges in three deliberate ways, which the
design should record next to the precedent (and pin with tests):

1. **401 → `ErrTokenRejected` + `Invalidate`** (billing: 401 → `ErrUnauthorized`, nothing). Intentional and
   required by REQ-3; different authority semantics (governance 401 = the token itself is rejected; billing 401
   classification is for error mapping). The design already contains the "classification-only precedent" caveat —
   extend it to name this divergence so a future "harmonization" cannot silently delete the invalidation.
2. **Scope-code list differs**: design maps `insufficient_scope`/`invalid_scope` on non-401 statuses; billing
   maps `invalid_token`/`insufficient_scope`/`metering_source_unauthorized` regardless of status. **Open gap
   (security review Q2b, confirmed):** a 403 with `{"error":"invalid_token"}` falls through to the generic error —
   no sentinel, no invalidation. If the governance authority ever expresses revocation as 403-invalid_token, the
   dead-token window persists (up to 24 h of cache TTL, though the drain's 8-attempt ladder masks the first
   ~62 min). Given the billing precedent already classifies `invalid_token` as an auth failure, the consistent
   fix is to map `invalid_token` (on 403, and on any status) to `ErrTokenRejected` and invalidate — a 3-line,
   code-only, binary-only-preserving change with a trivial test. At minimum, the design must record an explicit
   rejection of that mapping.
3. **403 fallback asymmetry**: billing treats every 403 as `ErrUnauthorized`; the design keeps non-scope 403s
   generic. Deliberate (only scope codes are actionable for the `aero_control_audit_scope_missing_total`
   metric), but note the consequence: a 403 with an unrecognized body produces no signal at all — acceptable only
   because 401 (the revocation signal) is status-driven and precedes body parsing.

Also confirmed non-issues: the 401-outranks-body precedence rule (design §2.3) is consistent with the billing
fallback (status wins over body in both, in different directions), and the test-reviewer's fix (T-4 part 1 with a
`{"error":"insufficient_scope"}` body + `!errors.Is(err, ErrTokenScopeMissing)`) makes the precedence observable —
adopt it. Note the asymmetric alert consequence: a 401 whose body says `insufficient_scope` yields `ErrTokenRejected`
(no scope-missing metric, but invalidation) — the runbook's "scope-missing counter rises ⇒ grant drift" guidance
covers only the 403 spelling; say so.

---

## 4. Required corrections to the prior deliverables

**Design doc (`2026-08-06-tokensource-invalidate-on-401-epoch-singleflight.md`):**

1. E8: publishers go to the callback service only; the ledger gets none (new.go:146, app/audit.go:32-34). FM-9:
   the "legacy inline path" is unwired in production — the 401 invalidation therefore runs on the drain path, and
   the "no scope metric on the inline path" caveat is moot in the live binary.
2. FM-3/§5 runbook: dead-lettered audit publications are **not** observable via
   `aero_control_callback_deliveries_total{status="dead_letter"}` (that counter is callback-only, callback.go:314;
   `finishPublicationAttempt` emits nothing). Replace with the new drain-outcome metric (1.3) or drop the claim.
3. FM-8: correct the singleflight retention claim (x/sync v0.20.0 `doCall` deletes the map entry on completion;
   no retained results) — already flagged by the concurrency and test reviewers; drop the `Forget` suggestion.
4. §6: adopt the test-reviewer's three fixes (post-invalidate second `AccessToken` interleaving for the
   epoch-keying regression; 401-with-scope-body for the precedence assertion; FM-8 correction) plus the cheap
   additions (token-endpoint-401 boundary, 403-no-invalidate, non-JSON body, epoch-stays-0).
5. §5/§7: state the pre-existing bulkhead contention (publications vs. callback deliveries, default 4) and the
   50 facts/s drain ceiling as known-load context, and state that `enabled=false` deployments consume rows as
   `completed` without publishing (no backfill on later enablement).

**Security assessment (`...-security-assessment.md`):** F-A and Q3's "unfed drain / permanently dropped with zero
signal" premise are wrong (T-1/T-2): the drain is fed on every append and is the only live publisher; facts that
401 during a revocation window are retried with backoff and dead-letter (terminal, currently metric-less) rather
than dropped after one wave. M3a is unnecessary (no-op); M3b monitors unwired code; the real fixes are the
drain-outcome metric, M1 (optional hardening, not load-bearing), and the 403-`invalid_token` decision (3.3.2).
FM-3b (concurrency review) similarly applies only to an unwired path.

## 5. Bottom line

- **M3a**: no blast radius because it changes nothing — the drain is already fed and already live (T-1); drop it.
  The true blast radius of "making dead code live" does not exist; instead, the revised design runs on the drain's
  existing bounded ladder (≤ 8 attempts, 1 s → 1 h backoff, terminal dead-letter), which structurally prevents the
  feared 401 → Invalidate → refetch loop from becoming unbounded (≤ 1 refetch/tenant/tick via epoch-keyed
  singleflight; ≤ 8 refetches per fact). Claim/Finish and at-least-once semantics are sound; the one real gap is
  terminal dead-letter invisibility, fixed by a code-only drain-outcome metric.
- **Binary-only**: **holds** — no config keys, no proto, no migrations are needed for any revised-scope item
  (M1 reuses `refreshSkew`; M3a needs nothing; the 403-`invalid_token` fix is code-only); all new code is
  unreachable when `audit_governance.enabled=false`; the rollout gate is the pre-existing flag.
- **Publish ownership/401-branch**: unobservable to the two existing error-only callers and to tests; 202/receipt
  path preserved byte-for-byte; the real coupling is the shared token cache (contained today, needs M1 if
  direction #1 wires the inline path); the billing precedent diverges in three ways that must be documented, and
  the 403-`invalid_token` classification gap should be closed to match billing's own code set.

VERDICT: The revised design is safe to implement as specified once (a) M3a/M3b are dropped or re-scoped to the
drain-outcome metric, (b) the design's E8/FM-9/FM-3 runbook claims are corrected to the real topology
(drain-only live publisher, dead-letter invisible without the new metric), and (c) the 403-`invalid_token`
decision is made explicitly; all three are documentation/code-only and preserve the binary-only rollout.
