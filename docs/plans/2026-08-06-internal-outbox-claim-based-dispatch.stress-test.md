# Stress-test audit — claim lease protocol (multi-instance correctness)

| | |
|---|---|
| Subject | `docs/plans/2026-08-06-internal-outbox-claim-based-dispatch.md` (design) |
| Scope | Per-row token minting, lease expiry vs conditional finalize races, crash recovery, DB-clock vs app-clock, partial-batch staleness, purge race, starvation/churn, mixed-version at-least-once, audit of F1–F11, distributed-transaction/split-brain confirmation |
| Method | Source re-verification + **empirical execution of the design's exact SQL against PostgreSQL 17.10** (scratch schema, 60-iteration race loop, concurrent claimers) |
| Date | 2026-08-06 |
| Resolution | H1 (lease sizing) → token-gated claim renewal (D12); H5 (scan amplification) → Go-side backoff exclusion with a bounded scan (D4); H4 (clock-skew text) → F6 corrected; H2/H3/H6 also folded into the design (§4, D4, §5/§5.1). See `2026-08-06-internal-outbox-claim-based-dispatch.md` §1.2/§1.3, D4/D12, §4, §5, F6 |

## 0. Verdict summary

**The protocol core is sound.** Every load-bearing mechanism was executed against a real
Postgres and behaved exactly as designed: claim exclusivity, fresh-token invalidation,
both finalize/re-claim orderings, the lease-boundary race (60/60 iterations: exactly one
winner, never both, never neither), concurrent claimer disjointness, partial-batch
finalize, purge-race no-op, relative retry writes, dead-letter boundary, and backoff
SQL/Go parity. No distributed transaction is introduced and no split-brain hazard exists
beyond generic single-PostgreSQL failure modes that are at-least-once-compatible.

**Six gaps found** — one high severity (lease sizing uses a per-event bound for a
batch-lifetime claim; the supported webhook chain can amplify duplicates ~30× and
defeat the backoff governor), one that invalidates a claimed benefit (D5's rationale is
right but the primary reason is stronger: PG refuses to create the REQ-7 index at all),
and four medium/low (corrupted `retry_count` fails the whole claim statement, F6 clock
directions swapped, backoff scan-amplification bound claim false, rollout-order hazard
stated backwards).

| # | Stress point | Verdict |
|---|---|---|
| 1 | Token minting + lease expiry vs finalize races | ✅ Correct; verified empirically incl. boundary race |
| 2 | Crash between claim and finalize | ✅ Correct; AC-3 path, token invalidation verified |
| 3 | Lease overrun, DB-clock `claimed_at` vs app clock | ✅ Claims are DB-clock-pure; ⚠️ F6 direction analysis wrong (H4); DB NTP step unenumerated (H9); lease *sizing* wrong for webhook chain (H1) |
| 4 | Partial-batch staleness | ✅ Correct; verified empirically |
| 5 | Purge race | ✅ Correct; verified empirically |
| 6 | Starvation / churn | ✅ At-least-once holds; ⚠️ backoff-row "same bound" claim false (H5); slow-handler consequence understated (H1) |
| 7 | Mixed-version at-least-once | ✅ Holds (worst = status quo); ⚠️ new-binary-before-migration = total stall, unstated (H6) |
| 8 | F1–F11 audit | 9 correct, 2 need correction (F6, F11), 1 needs amendment (F1) |
| 9 | Distributed transaction / split-brain | ✅ None introduced; residual risks are PG-level, at-least-once-compatible |

---

## 1. Empirical verification (PG 17.10, exact design SQL)

Scratch schema mirrored `outbox_events` + `idx_outbox_unpublished`; the design's
statements were executed verbatim (parameterized interval `'30s'`).

| # | Check | Result |
|---|---|---|
| E-1 | `UPDATE … WHERE id IN (SELECT … FOR UPDATE SKIP LOCKED … LIMIT ?) RETURNING` legality & behavior | ✅ Legal; claimed oldest-first, minted per-row UUID tokens, stamped DB `now()` |
| E-2 | Claim exclusivity while unexpired | ✅ Second claimer sees only unclaimed rows |
| E-3 | Re-claim after expiry mints fresh token; stale finalize with dead token | ✅ Stale `MarkPublished`/`MarkFailed` → 0 rows (no-op), row unchanged |
| E-4 | Finalize *within* lease, then re-claimer | ✅ Finalize applies; subsequent claim excludes published row |
| E-5 | **Boundary race** (finalize at exactly `claimed_at + lease` vs re-claim, 60 concurrent iterations) | ✅ 60/60 exactly-one-winner; **BOTH = 0, NEITHER = 0** — never double-applied, never dropped |
| E-6 | Two concurrent `ClaimBatch(7)` on 10 rows | ✅ Disjoint (7 + 3, zero overlap) |
| E-7 | Partial-batch `MarkPublished` (1 valid + 1 stale claim) | ✅ Only valid subset applied; stale row untouched |
| E-8 | Finalize after row deleted (purge race) | ✅ 0 rows, no error, no resurrection |
| E-9 | Relative `MarkFailed` + dead-letter boundary (`retry_count+1 >= maxRetries`) | ✅ 1→f, 2→f, 3→t with maxRetries=3 |
| E-10 | Backoff SQL/Go parity (`LEAST(5s·2^(n−1), 1h)`, n ∈ {1,2,3,5,10,11,12,20,21,25}) | ✅ 5s,10s,20s,80s,42:40,1h… matches Go `backoff()` |
| E-11 | **REQ-7-style partial-index predicate with `now()`** | ❌ **`CREATE INDEX` fails: "functions in index predicate must be marked IMMUTABLE"** — the predicate cannot be created at all (see H2) |
| E-12 | Corrupted `retry_count` (1000000) with `POWER(2, retry_count-1)` in claim predicate | ❌ **Entire ClaimBatch statement errors: "value out of range: overflow"** — one poisoned row stalls all claiming (see H3) |

---

## 2. Stress-point analysis

### 2.1 Token minting + lease expiry vs conditional finalize races — ✅ SOUND

The safety argument is the **conjunction** `claim_token = token AND claimed_at >= now() −
lease AND published_at IS NULL` plus row-lock serialization. All interleavings at the
lease boundary were executed (E-4/E-5):

- **Finalize commits first** (within lease): row published; the re-claimer's subquery
  re-checks under READ COMMITTED (EPQ after lock wait) → `published_at IS NULL` fails →
  claim never applies. Verified.
- **Re-claim commits first** (at/beyond boundary): fresh token + new `claimed_at`; the
  stale finalize fails on token mismatch even though its lease comparison against the
  *new* `claimed_at` would pass. This is why the token check is load-bearing and not the
  lease term. Verified.
- At exactly `claimed_at + lease`, the finalize is already stale (its statement `now()`
  is strictly later than the claim statement's), so the effective lease is marginally
  shorter than configured — a conservative, safe direction. Verified (E-5: 0 finalize
  wins at the exact boundary).
- The `published_at IS NULL` conjunct in `MarkPublished` is redundant for the same-claim
  flow (the relay never finalizes an event twice) but is the correct defense against an
  old-binary unconditional publish racing a new-binary claim during rollout (see §2.7).

Token collisions (F7): `(id, token)` pair matching makes cross-row interference
impossible even on collision; same-row collision requires a UUIDv4 collision with the
row's live token (~2⁻¹²²), not a practical threat.

### 2.2 Crash between claim and finalize — ✅ SOUND

Lease expiry is the recovery path: re-claim mints a fresh token that permanently
invalidates the dead holder's finalize (E-3). The one contract note the design already
makes bears repeating: the handler's side effect may have happened before the crash, so
recovery is a *duplicate* side effect — this is exactly AC-3's "twice total" and the
at-least-once contract. Verified logically and via E-3; AC-3 covers it. No permanent
stall is possible: an expired row is unconditionally re-claimable (`claimed_at <
now() − lease`).

### 2.3 Lease overrun; DB-clock `claimed_at` vs app clock — ⚠️ SOUND CORE, TWO CORRECTIONS

- **Claims are app-clock-free**: `claimed_at` is written by DB `now()` and compared to
  DB `now()` on both claim and finalize. Instance clock skew cannot extend or shorten a
  lease. Correct.
- **App clock enters only via `MarkFailed`'s `at` argument** (`last_retry_at`) and the
  relay's Go backstop. The design's F6 analysis of this is **swapped** (H4): the safety
  property survives only because the relay backstop always gates `Handle` — the design's
  stated mechanism ("DB behind → backstop+Release") is backwards.
- **Unenumerated axis**: DB server clock steps (NTP) are not instance skew. A forward
  step > lease prematurely expires every claim → re-claims → duplicates (legal, at-least-
  once); a backward step extends claims → delivery delay. Neither loses events. Mitigate
  with NTP discipline and lease ≫ poll interval (H9).
- **Lease *sizing* is the one real problem** (H1): the claim lifetime is the whole
  batch's sequential handler time, not one handler call.

### 2.4 Partial-batch staleness — ✅ SOUND (verified)

`MarkPublished` applies the valid subset and reports `ErrClaimLost` for the stale tail
(E-7). Events whose handler succeeded keep their `published_at`; stale ones are
re-claimed and re-delivered once. `ErrClaimLost`-vs-DB-error discrimination via
`errors.Is` is unambiguous. The design's return value on `ErrClaimLost`
(`len(publishedClaims)`) is correct — those events were handled.

### 2.5 Purge race — ✅ SOUND (verified)

`PurgeDeadLetters` only deletes dead-letter or published rows. A claimed row is neither,
so it can only be purged after a `MarkFailed` dead-lettered it while claimed; a
subsequent finalize from any holder affects 0 rows (E-8). No resurrection, no error
propagation. One hygiene note: a dead-lettered-then-purged row can carry a dangling
claim column — invisible to all queries (`NOT dead_letter` / `published_at IS NULL`
filters) — harmless (H7 covers the same hygiene point).

### 2.6 Starvation / churn — ⚠️ AT-LEAST-ONCE HOLDS; TWO GAPS

- F10 (slow instance loses claims): correct as far as it goes — someone else delivers;
  but the design understates the *steady-state duplicate amplification* when handler
  latency × batch > lease, which is reachable with the *supported* webhook chain (H1).
- F11 (no churn via SQL backoff predicate): the zero-write claim is correct, but it buys
  the churn away at the cost of O(backlog) index scans per poll; the design's D5/F11
  "same bound" statement for backoff rows is false (H5).
- Fairness: `ORDER BY created_at, id` + SKIP LOCKED preserves oldest-first among
  claimable rows; backoff rows are excluded by predicate, not by lock contention. No
  head-of-line starvation beyond the intended backoff.

### 2.7 Mixed-version rollout — ✅ AT-LEAST-ONCE HOLDS; ORDERING HAZARD UNSTATED

All interleavings of old (unconditional) and new (claim-based) binaries against the
post-migration schema were traced:

- Old fetches a row a new instance holds → both handle → duplicate (status quo level;
  multiplicity is old+new instance count, no worse than today).
- Old's unconditional `MarkPublished` lands mid-claim → new's finalize → `ErrClaimLost`
  warn (noise, correct); the event is published once by old, handled by both — at-least-
  once holds.
- Old's unconditional `MarkFailed` races new's relative write → transient
  retry_count/last_retry_at skew (absolute vs relative write), bounded, converges;
  dead-letter boundary may trigger one attempt early/late — transient, status quo level.
- **No loss path exists**: any finalize that fails leaves the row unpublished →
  re-claimable; any old publish only sets `published_at`. At-least-once is preserved in
  every interleaving. The design's §5 claim ("no worse than status quo") is correct.

The gap is the **other** direction (H6): §5 warns against old binaries on the new schema
(harmless — old code ignores the new columns), but the dangerous order is new binaries
on the *pre-migration* schema: every `ClaimBatch` errors ("column claim_token does not
exist") → **complete delivery stall** — strictly worse than status quo (no duplicates,
but *no delivery at all*). Fail-stop and loud (warn every poll), but it must be stated
as the binding constraint: **migration must land before any new binary**.

---

## 3. Audit of the 11 enumerated failure modes

| # | FM | Verdict | Notes |
|---|---|---|---|
| F1 | Handler exceeds lease | ⚠️ Behavior correct; **sizing analysis wrong** | Finalize → `ErrClaimLost`, re-claim → re-delivery: verified. But "lease must exceed handler p95" is the wrong bound — the claim covers the whole batch (§H1). |
| F2 | Crash between claim and finalize | ✅ | Lease recovery + token invalidation verified (E-3). Duplicate side effect is the documented contract. |
| F3 | DB outage at claim | ✅ | Drain aborts, returns 0; nothing claimed; resumes on recovery. No partial-claim leak (statement atomicity). |
| F4 | DB outage at finalize | ✅ | Claims held → lease expiry → re-delivery. Note: even a *committed-but-response-lost* `MarkPublished` is paid with a full re-delivery after lease expiry (no retry); a single idempotent retry would avoid it (H11). |
| F5 | Partial-batch stale | ✅ | Verified (E-7). Valid subset applied; stale tail re-delivered. |
| F6 | App/DB clock skew | ⚠️ **Directions swapped** (H4) | Lease checks are DB-clock-pure (correct). The backoff analysis is inverted; safety survives via the Go backstop, and F11's zero-churn claim only holds with clock agreement. |
| F7 | Token collision | ✅ | Safe by construction: `(id, token)` pairs; same-row collision at UUIDv4 odds. |
| F8 | Purge races claim | ✅ | Verified (E-8): 0 rows, harmless. |
| F9 | Lease misconfigured too short | ✅ | Spurious re-claims → legal duplicates + warn spam. Correct; test-side flakiness note in H10; sizing rule in H1. |
| F10 | Slow-instance starvation | ⚠️ Holds, **consequences understated** | Events still delivered by whoever finalizes — true. The steady-state N× duplicate loop for slow-but-succeeding handlers and the defeated backoff governor for slow-but-failing ones are H1. |
| F11 | Re-claim churn on drained batch | ⚠️ Zero-write claim correct; **bound claim false** (H5) | SQL predicate excludes backoff rows — no churn. But backoff rows are bounded by the backlog, not batch×instances; every poll scans them. |

---

## 4. Findings

### H1 — HIGH: lease sizing uses a per-event bound for a batch-lifetime claim

`ClaimBatch(limit=100)` claims the whole batch in one statement; the relay then handles
the batch **sequentially** (`relay.go` loop). The claim's lifetime is therefore
`Σ handler latency over the batch`, not one handler call. The design's sizing rule
("lease must exceed handler p95", F1; "default 30s ≫ … webhook chain ≤ 10s") applies the
per-event bound to a batch-lifetime claim — off by the batch factor (100×).

With the **supported, config-default webhook chain** (`configs/config.yaml`: webhook
timeout 5s; batch 100), a slow-but-successful endpoint (say 4.5s/event) yields:

- Drain duration ≈ 450s ≫ 30s lease. Only the first ~6 events finalize within their
  lease; the tail (~94) gets `ErrClaimLost` at `MarkPublished`, and the *next* poll
  re-claims the tail and **re-handles it** → every event is delivered ~batch/(lease/per-
  event) ≈ **~30×** (duplicate signed webhook POSTs — the exact defect this design exists
  to remove) before it drifts to the front of the batch and publishes. Convergence takes
  hours.
- Slow-but-failing endpoint: the tail's `MarkFailed` is lost (`ErrClaimLost`) →
  `retry_count` never advances for the tail → **the backoff governor and dead-letter
  ceiling are defeated exactly during handler storms**; the event retries once per drain
  at full handler cost until it reaches the front of the batch. At-least-once still
  holds; progress still converges — but ~30× slower and with ~30× the handler load.
- Verified bounds: lease window admits ~`lease/per-event` events per drain
  (30s/4.5s ≈ 6), tail = batch − that.

The design's lease-sizing rule must be, at minimum: **lease > batch × p99(per-event
handler latency) + finalize margin** — which with defaults (100 × 5s) means ~500–1000s,
i.e., crash-recovery latency of minutes. Better options, in order of preference:

1. **Claim in sub-batches** (e.g., claim `max(1, lease/p99)` events per statement, loop
   within drain) — bounds claim lifetime to a single handler window, keeps 30s recovery.
2. **Renew claims** during long drains (periodic `UPDATE … SET claimed_at = now() WHERE
   claim_token = … AND claimed_at >= now() − lease`, token-gated) — keeps batch claiming,
   adds a low-rate write.
3. At minimum, document that the webhook chain's worst case exceeds the default lease
   and set the lease accordingly per deployment.

Note the renewal must itself be token-gated and lease-checked, and renewal must never
extend a claim whose token was superseded — the same conjunction as finalize.

### H2 — MEDIUM-HIGH: D5 is not just preferable, it is the only viable option

Verified: `CREATE INDEX … WHERE published_at IS NULL AND NOT dead_letter AND (claimed_at
IS NULL OR claimed_at < now() − interval '30 seconds')` fails with **"functions in index
predicate must be marked IMMUTABLE"** (PG requires index predicates to be immutable;
`now()` is STABLE). REQ-7's suggested predicate cannot be created on any supported
Postgres. D5's stated rationale (write-time ejection, no re-admission at expiry) is
correct as a secondary concern, but the design should cite the IMMUTABLE requirement as
the primary, decisive reason. **Action**: strengthen §4/D5 wording; REQ-7's index
requirement is unsatisfiable as written and should be amended in the spec.

### H3 — MEDIUM: one corrupted `retry_count` fails the entire ClaimBatch statement

Verified: a row with `retry_count = 1000000` (manual corruption, future bug, or a
raised `max_retries`) makes `POWER(2, retry_count − 1)` overflow and the **whole
statement errors** — no events are claimed anywhere until the row is fixed. Go's
`backoff()` degrades gracefully (returns a degenerate duration → immediate retry); the
SQL fails hard. Unreachable through normal operation (dead-letter caps `retry_count` at
`maxRetries`), but the asymmetry is a one-line fix: clamp the exponent,
`POWER(2, LEAST(retry_count, 60) − 1)` (or a `CASE` guard), and optionally add the
overflow case to `TestBackoffSQLParity`.

### H4 — MEDIUM: F6 clock-skew directions are swapped

The design's F6 says "(DB ahead) → can only delay; (DB behind) → hit the Go
backstop+Release; never early delivery". Derivation with `last_retry_at` (app clock)
vs the SQL predicate (DB clock):

- **DB clock ahead of app**: `last_retry_at` appears old → the predicate admits the
  event up to the skew *early*. The Go backstop (pure app-clock, self-consistent) then
  rejects it → `Release` → **no early delivery, but bounded claim+release churn on
  near-due rows** — i.e., F11's "zero claim writes" holds only when clocks agree.
- **DB clock behind app**: `last_retry_at` appears young → the predicate excludes the
  event longer → **delay by the skew**; the backstop never fires because nothing is
  claimed.

The design has both directions inverted. The *safety* conclusion survives — delivery is
never early because the relay backstop always gates `Handle` — but the mechanism text
must be corrected, and the skew-induced churn should be acknowledged (bounded by skew ×
admission rate; not a correctness issue).

### H5 — MEDIUM: backoff-excluded rows are not bounded by batch × instances

D5/F11 claim backoff rows are excluded "with the same bound" as claimed rows. False:
claimed-but-unexpired rows are bounded by batch × instances (at most 100 × N), but
backoff-excluded rows are bounded by nothing — they are the backlog. Worst case (large
backlog that failed together, e.g. 1M events at `retry_count=19` → 1h backoff): every
poll's ClaimBatch scans the entire backoff backlog (index entries + heap fetches) to
find `limit` passing rows — ~1M row visits per poll per instance, vs today's fixed
100-row scan (today the backoff check happens in Go *after* a bounded fetch). The
trade-off chosen by D4 (SQL-first backoff) is: zero claim/release writes vs
O(backlog) scan per poll. For batch=100, the foregone alternative (claim + Go-skip +
Release) costs ≤200 row updates/poll even in the pathological case — cheaper than a
1M-row scan. Neither choice is universally right; the design should (a) state the real
bound, (b) quantify with a synthetic-backlog test (100k in-backoff rows; measure
ClaimBatch latency), and (c) expose a monitoring signal (statement scan rows/duration)
so a post-outage backlog cannot silently degrade the drain.

### H6 — LOW-MEDIUM: rollout-order hazard stated backwards

§5 warns "do not leave old binaries running against the new schema" — that direction is
harmless (old code ignores the new columns; duplicates at status-quo level; verified by
tracing, §2.7). The hazardous direction is unstated: **new binaries against the
pre-migration schema** → every ClaimBatch errors → complete delivery stall (no
duplicates, but no delivery at all — an availability regression, fail-stop and loud).
The binding constraint is: **apply 000058 before (or atomically with) any new binary**;
old binaries may safely overlap the new schema. Fix the §5 wording.

### H7 — LOW: MarkPublished leaves claims attached to published rows

No correctness impact (published rows are excluded everywhere by `published_at IS
NOT NULL`; stale finalizes on them are token-rejected), but published rows carry
`claim_token`/`claimed_at` until purge — ambiguous operational state and stale claims
survive on dead-lettered/purged rows too. Recommend clearing both columns in the same
`MarkPublished` statement (`claim_token = NULL, claimed_at = NULL` alongside
`published_at = now()`). Cheap, self-documenting, zero race impact (the token check runs
in the same statement).

### H8 — LOW: `pending` metric and drain-log semantics drift in multi-instance mode

`ObserveOutboxDrain(pending=len(claimed))` and the drain summary's `pending` now count
"claimable right now", not "unpublished". In-flight claims (up to 100 × instances) are
invisible, so backlog gauges understate during active multi-instance processing. Code
"unchanged" but signal meaning changed — document it, or add a claimed/in-flight count.

### H9 — LOW: DB clock NTP steps are an unenumerated axis

F6 covers instance↔DB skew; the DB clock itself can step (NTP). Forward step > lease →
premature expiry → re-claims → duplicate deliveries (legal, at-least-once); backward
step → claims outlive their lease → delivery delay. No loss in either direction.
Mitigations: NTP discipline; lease ≫ poll interval (30s vs 5s absorbs small steps).
State it in F6 for completeness.

### H10 — LOW: AC-3's 100ms lease risks negative-check flakiness

The negative assertion ("second ClaimBatch excludes E while unexpired") must complete
within one lease (100ms) of the claim — a few slow CI round-trips make it flaky.
Recommend a ≥1s injectable lease and age the row via SQL (`claimed_at = now() − 1s`,
already the design's sleep-free technique) to trigger expiry.

### H11 — LOW: transient MarkPublished error costs a full-batch re-delivery

`MarkPublished` DB error → relay returns 0 → claims held → lease expiry → re-claim →
re-handle → duplicate side effects, even when the original statement *committed* (lost
response). One safe retry would avoid most of this: a retry is idempotent because the
`published_at IS NULL` conjunct turns a committed-then-retried finalize into a 0-row
`ErrClaimLost` (harmless). Optional; at-least-once makes the current behavior legal.

---

## 5. Distributed transaction / split-brain confirmation

**No distributed transaction is introduced.** Every operation is a single-statement or
single-row conditional update on one PostgreSQL: claim = one `UPDATE … RETURNING`;
finalize/release = one conditional `UPDATE`; `Append` remains inside the caller's
transaction. No 2PC, no saga, no cross-service coordination, no multi-region writes.

**No split-brain hazard is introduced.** The protocol has exactly one authority — the
row itself — and a per-row fencing token minted in the same atomic statement as the
state change:

- Two instances cannot both hold a valid claim: claims are exclusive by predicate + row
  lock (verified E-6), and re-claims mint a fresh token that invalidates every prior
  holder's finalize (E-3). There is no interleaving in which two instances both
  successfully finalize the same row (E-5: 60/60 exactly-one-winner).
- The lease is time-based fencing; its revocation point is expiry. The classic
  time-based-lease caveats apply and are at-least-once-compatible: DB clock NTP step
  (H9) and PG failover with async replication (a committed claim/finalize can be lost
  in replication → duplicate after failover — legal). An unfenced old primary accepting
  writes after failback is generic PostgreSQL split-brain affecting all application
  state, not something this design introduces or can prevent; it remains out of scope
  per AGENTS.md (single authoritative DB assumed).
- The design adds no cross-region replication, no multi-writer promotion, no new
  authority boundary, and no credential-like state (claim tokens are internal row
  state, not secrets).

---

## 6. Recommendations (prioritized)

1. **H1**: Fix the lease-sizing rule to `batch × p99(per-event)` or adopt sub-batch
   claiming / token-gated claim renewal; add a test asserting `MarkFailed` with an
   expired claim leaves `retry_count` untouched (the backoff-governor interaction).
2. **H2**: Amend REQ-7/§4 — the proposed index predicate is uncreatable (IMMUTABLE);
   D5's deviation is mandatory, cite the primary reason.
3. **H3**: Clamp `POWER` exponent in the claim SQL; add overflow case to the parity test.
4. **H6**: State rollout order as "migration strictly before new binaries"; the current
   §5 warning targets the harmless direction.
5. **H4/H5**: Correct F6 direction analysis; replace the false "same bound" claim in
   D5/F11 with the real O(backlog) scan bound and a monitoring signal.
6. **H7**: Clear claims in `MarkPublished`. **H8**: document the `pending` semantics
   drift. **H9**: add NTP-step note to F6. **H10**: ≥1s injectable lease in AC-3.
7. **Test-gap additions** to the acceptance mapping:
   - `TestClaimLeaseBoundaryRace` — concurrent finalize vs re-claim at the boundary;
     assert exactly-one-winner and that a published row never carries a foreign claim
     (the crux invariant; verified empirically here, untested in the current ACs).
   - `TestMarkFailedExpiredClaimLeavesRetryCount` (H1b).
   - Synthetic-backlog scan-depth guard for ClaimBatch (H5).

## 7. Overall verdict

The design's core protocol is correct and was verified against a real PostgreSQL: claim
exclusivity, token fencing, boundary-race safety, crash recovery, partial-batch and
purge semantics all behave as specified, at-least-once holds through mixed-version
rollout, and no distributed-transaction or split-brain hazard is introduced. The
deliverable is **approvable with amendments**: H1 (lease sizing for the supported
webhook chain) must be resolved before implementation, H2/H3/H6 should be folded into
the design text, and H4/H5/H7–H11 are documentation and test hardening.
