# Security assessment — invalidate-on-401 posture for the audit governance TokenSource

| | |
|---|---|
| Subject | `docs/plans/2026-08-06-tokensource-invalidate-on-401-epoch-singleflight.md` (REQ-1..REQ-6 of `docs/requirements/2026-08-06-cmd-migrate-tokensource-invalidate-on-401.md`) |
| Questions | (Q1) does invalidating on **any** 401 permit attacker/misconfiguration-driven refetch storms? (Q2) is excluding token-endpoint 401s sufficient? (Q3) are audit facts published while the token is invalid silently dropped at `audit_ledger.go:79`, masking security-relevant events? |
| Method | Live-tree verification of the design's claims plus the full publish/drain/metrics wiring (`oauth.go`, `publisher.go`, `audit_ledger.go`, `callback_audit.go`, `callback.go`, `repository/audit_ledger.go`, `app/audit.go`, `app/new.go`, `pkg/metrics/control_plane.go`) |
| Date | 2026-08-06 |

## Verdicts

- **Q1 — invalidate-on-any-401: ACCEPT as designed**, with one required mitigation for the *inline* path
  (refetch cooldown) and one explicit acceptance for the residual 1:1 loop. No attacker amplification exists.
- **Q2 — excluding token-endpoint 401s: SUFFICIENT as an invalidation policy.** Two observability gaps remain
  (token-endpoint 401 is not distinct on the live path; `invalid_token` on non-401 statuses is unhandled).
- **Q3 — swallowed audit facts: REAL, and worse than the design assumes.** The design reduces the loss window
  from up-to-24 h to one publish wave, but the 401'd fact(s) are still permanently dropped from governance
  delivery with **zero signal**, and the design's own observability story (REQ-5 emission, FM-3 dead-letter
  signal) is **unreachable in the live tree**: the durable drain is never fed.

The two load-bearing findings not visible from the design document alone:

- **F-A — the durable drain is dead code.** Nothing in the tree inserts `audit_publications` rows.
  `AuditPublicationRepository` (repository/audit_ledger.go:42-46) exposes only `ClaimPublication` +
  `FinishPublication`; the only other references are the coverage query (app/audit.go:91) and the
  claim/finish implementations. `drainPublications` therefore claims nothing, `publishAuditFact` never runs,
  the REQ-5 scope-missing emission point is unreachable, and there is **no metric of any kind on the audit
  publication path** (`callback_audit.go` has zero metrics calls; the design would add the first).
- **F-B — the design's FM-3 observability claim is factually wrong.** FM-3 asserts a dead-lettered audit
  publication is "observable via `aero_control_callback_deliveries_total{status="dead_letter"}`". That counter
  is fed only by **callback** delivery outcomes (`s.metrics.ObserveCallbackOutcome(status)`, callback.go:314
  from the `s.drain` path). Audit publications never touch it.

Both follow from the same root cause: the durable-drain half of the pipeline (direction #1 of the source
analysis — "remove the inline `_ = publisher.Publish` loop / AppendInTx consolidation", explicitly still
unlanded per §5 of the spec) is unfinished. The design builds its recovery/alerting story on that unfinished
half while the **only live publish path is the inline swallow at audit_ledger.go:79**.

---

## Q1 — Invalidating on any 401: refetch-storm analysis

### Who can produce a 401 against aero-id's *valid* token?

| 401 source | Can an attacker exploit it to force refetches? | Marginal risk |
|---|---|---|
| Genuine revocation/rotation at the governance authority | Not attacker-controlled without owning the authority | This is the intended case; invalidation is the fix |
| On-path attacker corrupting the request (e.g., mangled `Authorization`) | Yes — but an on-path attacker can already drop/modify every request and forge 202 receipts. TLS/mTLS is assumed (publisher.go uses `secureEndpoint`) | Negligible: refetch forcing adds nothing to that adversary's capability |
| Request-content-driven 401 (attacker-influenced payload/action) | No. Authz at the events endpoint is keyed to the bearer token, not the body; malformed payloads produce 4xx validation errors, not 401 | None |
| Upstream misconfiguration (gateway/WAF false positive, IdP clock skew, mTLS-at-proxy failure surfacing as 401) | Not attacker-controlled, but **realistic** | This is the only credible spurious-401 driver — see the loop analysis below |

### Amplification math (the epoch design is sound)

- Refetch is **1:1, not N:1**: each observed 401 bumps the epoch; the next `AccessToken` wave joins one
  epoch-keyed singleflight group → exactly one refetch per wave. Waves per batch are bounded by the bulkhead
  (`WorkerConcurrency`); waves per tick are bounded by batch boundaries. The epoch guard (FM-1/FM-2 in the
  design) correctly prevents stale-fetch repopulation and wastes at most one fetch in the race window.
- A spurious invalidate is **self-healing**: the refetch returns the same (still-valid) client-credentials
  token; the cost is one extra `POST /token` per wave. No user-visible side effect, no lockout surface (this
  is not a user-token flow where a spurious invalidate would force interactive re-auth).
- The token endpoint is **the same authority** that returned the 401 — the refetch load lands on the party
  whose misbehavior (or legitimate rotation) caused the event. It cannot be pointed at a third party.
- **This asymmetry is decisive**: cost of a spurious invalidate = 1 token fetch; cost of a missed genuine
  revocation = up-to-24 h of silently dropped audit facts (the status quo the design exists to fix). For a
  client-credentials token, invalidate-on-any-401 is the correct risk posture. Scope-missing-only
  invalidation would not address the primary threat at all.

### The real residual risk: an unbounded 1:1 loop on the *live* inline path

The loop: events endpoint persistently 401s (misconfiguration) → `Record`'s inline `Publish` 401s →
invalidate → next `Record` refetches (success — the token is fine) → publishes → 401 → invalidate → … Each
cycle's refetch **succeeds**, so any failure-only cooldown would not stop it.

- On the **drain path** (once direction #1 feeds it), the loop is self-terminating: `publicationOutcome`
  retries with backoff (`RetryBase`, `MaxAttempts` default 8) and dead-letters. Bounded by design.
- On the **inline path — the only live path — there is no attempt counter, no backoff, no cap, and no
  metric**: every audit event during a persistent upstream 401 costs one extra token fetch, indefinitely,
  silently. Absolute volume is low (refetch rate = security-event rate) and lands on the same authority, so
  this is a hygiene/availability finding, not a DoS. But it is exactly the "upstream misconfiguration forces
  repeated token refetches" scenario the question asks about, and the design does not bound it.

**Mitigation M1 (recommended, small):** per-tenant refetch cooldown — a min interval between fetch attempts
(reuse `refreshSkew`, default 30 s; the fetch already only happens on cache miss). During the cooldown,
`AccessToken` returns `ErrTokenUnavailable` without hitting the endpoint. Bounds the loop at ≤ 1 fetch per
cooldown per tenant regardless of event rate, also fixes the pre-existing fetch-per-event pattern during a
token-endpoint outage, and adds ≤ one cooldown of latency to recovery after a genuine rotation (acceptable —
the drain retry backoff already tolerates this).

**Explicit acceptance A1 (if M1 is declined):** during a persistent upstream 401, the token endpoint receives
≤ 1 client-credentials fetch per audit event, no amplification beyond 1:1, no cross-tenant effect (per-tenant
token sources), no attacker exploitability without transport/authority compromise. The loop is invisible to
operators (no metric on the inline path — see Q3) — which is the stronger reason to take M1 or the Q3 fixes.

---

## Q2 — Excluding token-endpoint 401s: sufficient?

**Sufficient as an invalidation policy — yes, with a structural argument:**

- `fetchToken` runs only on a cache miss (`accessToken` checks `cachedToken()` first). At the moment a
  token-endpoint 401 is observed, **there is no cached token to invalidate**. `Invalidate` would be a no-op
  for the cache and a meaningless epoch bump.
- A refetch after a token-endpoint 401 would fail identically — the artifact being rejected is the
  **client credential/grant** (rotated or revoked client secret), not the access token. Invalidation cannot
  repair it.
- Bindings are per-tenant with dedicated client credentials; one tenant's credential rejection says nothing
  about another tenant's cached token. No cross-tenant propagation concern.

**Two gaps remain, both observability/classification, not invalidation:**

- **G2a — token-endpoint 401 is not distinctly observable on the live path.** `fetchToken` folds it into
  `ErrTokenUnavailable: HTTP 401`, which the inline loop swallows. On the drain path,
  `publicationOutcome` maps every non-`errAuditTenantUnbound` error to the single code
  `audit.publication_failed` — token-endpoint rejection and transient 500s are indistinguishable in
  dead-letter state. The design's runbook signal ("dead-letter rise **without** scope-missing → token
  endpoint health") therefore cannot actually discriminate. **Mitigation:** a distinct outcome code
  (`audit.token_endpoint_rejected`) via `errors.Is(err, ErrTokenUnavailable)` in `publicationOutcome`, plus
  the refetch-cooldown alert from M1. Small, no contract change.
- **G2b — `invalid_token` on non-401 statuses is unhandled.** The billing precedent
  (client.go:277-282) classifies `invalid_token` → `ErrUnauthorized`; the design's `classifyPublishError`
  only reads scope codes (`insufficient_scope`/`invalid_scope`) on non-401 statuses. If the governance
  authority expresses revocation as 403 + `{"error":"invalid_token"}` (a real deployment pattern), the
  design returns a generic error, does **not** invalidate, and the dead token is reused until expiry —
  the exact up-to-24-h window the design exists to close. **Mitigation M2 (optional, ~3 lines):** classify
  `invalid_token` on non-401 statuses as `ErrTokenRejected` and invalidate (status-driven 401 precedence
  rule unchanged; self-healing makes a false positive cost one fetch). **Explicit acceptance A2:** the
  governance authority's contract must guarantee 401 (not 403+`invalid_token`) for token rejection; this
  cannot be verified from aero-id's side (external service) — document it as an operational assumption and
  cover it in the connector contract test.

---

## Q3 — Audit facts published while the token is invalid: silently dropped?

**Yes — and the design reduces but does not eliminate the loss, and the residual loss is invisible.**

### What actually happens today (verified)

`auditLedgerService.Record` (audit_ledger.go:68-80): `repo.Append` durably persists the fact **first**, then
the inline loop `_ = publisher.Publish(ctx, fact)` runs **once, fire-and-forget**; every publish error —
401, 500, network, receipt mismatch — is swallowed and `Record` returns nil. This loop is the **only live
publish path** and it is fed by the security-relevant call sites: replay (replay.go:145/:240), sync jobs
(sync_job.go:163/:467), event ingestion (event_ingest.go:154), account lifecycle (account.go:155/:184,
account_profile.go:62), legacy cutover (legacy_cutover.go:88).

### What the design changes

- **Loss window:** from [rotation → token expiry] = up to 24 h of facts → [rotation → first 401 observed] =
  exactly the publish wave that observes the 401 (typically one fact, plus any concurrent in-flight
  publishes still holding the revoked token). Subsequent facts self-heal via the refetch. This is a major,
  correct improvement.
- **Residual loss:** the fact(s) that 401'd are **never delivered to the governance authority** — there is
  no retry on the inline path — and **no signal is emitted**: `ObserveAuditIngestFailure` fires only on
  `repo.Append` error (audit_ledger.go:73), and the design deliberately (REQ-5) does not add emission to the
  inline loop, betting on direction #1.

### Why it can mask security-relevant events

- aero-id's **local** evidence is intact: `Append` precedes the publish, the ledger is append-only,
  chain-verified (`VerifyChain`), and WORM-anchored elsewhere. No audit fact is lost from aero-id's own
  compliance view.
- The **governance authority's** view — the sink this connector exists to feed (`wait_for=ledgered` receipt
  contract, publisher.go:9) — is missing exactly the events published during the invalid window. If that
  sink is the authoritative compliance archive, a security-relevant event (admin action, replay, failed
  lifecycle operation) published in that window is absent from it, with zero operator signal. The design
  converts "up to 24 h of missing governance coverage, silently" into "one wave of missing governance
  coverage, silently". Better by ~3 orders of magnitude, but the silent part is unchanged.
- **The design's own alerting story cannot fire.** REQ-5's emission lives in `publishAuditFact` on the
  drain path, which is unreachable (F-A): `audit_publications` is never populated, `drainPublications`
  claims nothing, `publishAuditFact` never executes, `aero_control_audit_scope_missing_total` never
  increments, and no audit-publication dead-letter metric exists at all (F-B). Until direction #1 lands and
  feeds the drain, the deliverable's §5 runbook (scope-missing alert, dead-letter ladder) and §6 service
  test are testing dead code.

### Mitigations (in decreasing order of completeness)

- **M3a (the real fix — direction #1's job):** on inline publish failure, enqueue the fact into the durable
  drain (`audit_publications`) so the existing claim/retry/dead-letter machinery becomes live. This makes
  REQ-5's emission point reachable, gives the 401'd fact a retry with backoff, and restores FM-3's
  termination story. It is explicitly out of scope for this campaign — **the design must declare the
  dependency** rather than assume the machinery works.
- **M3b (minimum, ~3 lines, trivially removable when direction #1 lands):** emit an inline publish-failure
  metric in `Record`'s loop (new `aero_control_audit_publish_failures_total{source,code}` counterMap, or
  reuse the existing seam pattern). This removes the "silent" qualifier today, on the live path, without
  waiting for direction #1. It does not conflict with direction #1 (the counter is dropped with the loop).
- **M3c (acceptance):** if neither M3a nor M3b is taken, the design document must be corrected to state:
  (1) governance delivery is best-effort on the inline path; (2) exactly the publish wave observing a 401 is
  permanently dropped from governance delivery, durably retained only in the local ledger; (3) no metric
  signals this; (4) the §5 runbook and FM-3 observability claims are conditional on direction #1 feeding the
  drain. An *unconditional* acceptance is defensible only because local audit evidence is durable and
  chain-verified — the loss is replication coverage, not audit evidence.

### Required corrections to the design document (independent of the above choices)

1. **FM-3:** "observable via `aero_control_callback_deliveries_total{status="dead_letter"}`" is wrong —
   that counter tracks callback deliveries (callback.go:314); audit publications have no metric. Either add
   an audit-publication outcome counter or drop the claim.
2. **§5 runbook:** both alert signals (scope-missing counter, dead-letter ladder) presuppose the drain is
   fed; state the direction-#1 dependency explicitly and add a temporary inline-path signal (M3b) or accept
   its absence.
3. **§4 FM-10 / §6 T-4:** the "exactly one refetch" claims are correct for the epoch/singleflight mechanism
   but should note the per-wave bound (waves ≤ bulkhead) rather than implying a global 1, and that on the
   inline path the refetch stream is 1 per 401 with no backoff (M1).

---

## Recommendation summary

| # | Finding | Verdict | Mitigation / acceptance |
|---|---|---|---|
| Q1 | Invalidate on any 401 | **Accept** — no attacker amplification; 1:1 refetch, singleflight-deduped, self-healing; asymmetry favors invalidation for client-credentials tokens | M1 per-tenant refetch cooldown (recommended); A1 explicit acceptance otherwise |
| Q2 | Token-endpoint 401 excluded | **Sufficient** as invalidation policy (nothing cached during fetch; refetch would fail identically; per-tenant isolation) | G2a distinct token-endpoint outcome code + metric; G2b optional `invalid_token` classification (M2) or documented contract assumption (A2) |
| Q3 | Facts published during invalid window | **Real residual risk** — loss reduced 24 h → one wave, but still permanent, still silent, and the design's alerting is unreachable (F-A, F-B) | M3a feed the durable drain (direction #1 dependency); M3b inline publish-failure metric now; M3c corrected explicit acceptance |

**Bottom line:** the epoch-singleflight invalidate-on-401 mechanism itself is sound and correctly scoped;
the security posture of *this deliverable* is undermined not by the mechanism but by the fact that its
recovery/observability story runs on the unfed durable drain while the live inline path remains
swallow-and-silent. Fix the inline path's signal (M3b) or its durability (M3a), bound the refetch loop
(M1), and correct the two factual claims (FM-3 metric, §5 runbook preconditions) — then the design's
security claims hold as stated.
