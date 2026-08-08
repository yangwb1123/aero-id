# Correction verification — invalidate-on-401 design (FM-8, F-A, F-B, §4/§5/§6, Q2b)

| | |
|---|---|
| Subject | `docs/plans/2026-08-06-tokensource-invalidate-on-401-epoch-singleflight.md` (mtime 2026-08-06 08:02) |
| Review inputs | security assessment (08:06), concurrency review (08:12), test-design review (08:12) — `docs/auto/runs/tokensource-invalidate-on-401-with-epoch-single--713d6235/artifacts/adversarial_review-9c87f3a7/` |
| Method | Re-read the design doc end-to-end; re-verified every disputed claim against the live tree and the pinned `golang.org/x/sync v0.20.0` source |
| Date | 2026-08-06 |

## Verdict

**FAIL — the correction gate is not satisfied. The design doc is unrevised.** Its mtime (08:02) precedes all
three reviews (08:06–08:12); `git status` shows it as an untracked file with no subsequent edit. Every
flagged claim is still present verbatim: FM-8's false retention premise, FM-3's false metric identity,
the §5 runbook discriminator, the missing FM-2b/2d/FM-3b rows, the unqualified FM-10, all six missing §6
tests, and the absent Q2b resolution. Additionally, one reviewer claim (F-A) is itself contradicted by the
live tree — applying the reviewer's recommended "correction" verbatim would insert a **new** false claim
(see §2 below).

## 1. Required-correction checklist (each verified against the tree)

| Required correction | Status in design doc | Live-tree evidence |
|---|---|---|
| **FM-8**: x/sync v0.20.0 retains **no** results; `Forget` is dead code — drop it | **NOT corrected.** §4 FM-8 (doc line 265) still says "`singleflight.Group` retains the last result per key" and suggests "optional hygiene: `s.fetches.Forget("client-"+old)`" | `$GOMODCACHE/golang.org/x/sync@v0.20.0/singleflight/singleflight.go` (pinned in go.mod:25): `doCall`'s deferred cleanup does `if g.m[key] == c { delete(g.m, key) }` **before** delivering to channels — no retention for `Do` or `DoChan`. Epoch keys are never reused, so `Forget` is dead code |
| **F-A**: drain-liveness premise | **Premise is actually correct — the reviewer's F-A is FALSE on the live tree.** The doc's premise (E7, §2.6: drain wired and live) holds, but it is under-cited: no reference to the feed path. It must NOT be "corrected" to claim the drain is dead | `repository/audit_ledger.go:198-200`: `createAuditRows` inserts a `pending` `audit_publications` row in the same transaction as every ledger append (`appendInTx`:171 ← `Append`:113-119 and `AppendInTx`). `app/new.go:134/146` wires that repo into the ledger service; `:150` sets the publication repo on the callback service; `:179` starts the ticker. `service/callback.go:261-267` → `drainPublications` (callback_audit.go:47-71) → `publishAuditFact` (:97-112). **`publishAuditFact` runs in production; the REQ-5 emission point is reachable today** |
| **F-B**: metric identity — `aero_control_callback_deliveries_total` is a *callback*-delivery counter; audit publications have no metric | **NOT corrected.** FM-3 (doc line 260) still claims a dead-lettered audit publication is "observable via `aero_control_callback_deliveries_total{status="dead_letter"}`"; §5 step 9 (lines 290-292) repeats it | `internal/pkg/metrics/control_plane.go:80/:146`: `callbacks counterMap // status -> callback delivery outcomes`, fed only by `ObserveCallbackOutcome` from `markDelivery` (callback.go:314) — tenant webhook deliveries. `callback_audit.go` has zero metrics calls; `publicationOutcome` (:84-95) writes only DB rows |
| **§5 runbook preconditions** | **NOT corrected.** Step 9's discriminator ("dead-letter rise **without** scope-missing → token-endpoint health") is broken twice: (a) the cited counter never sees audit publications (F-B); (b) `publicationOutcome` maps every non-`errAuditTenantUnbound` error to the single code `audit.publication_failed`, so token-endpoint 401 is indistinguishable from transient 500s even in DB dead-letter state. No direction-#1 dependency statement, no M3b/M3c acceptance for the silent inline path | `service/callback_audit.go:84-95` verified: only `errAuditTenantUnbound` is distinct |
| **§4 FM-2b** (cache-hit caller whose `RLock` precedes the invalidate uses the old token once → 401 → converge; bounded by bulkhead ≤64) | **NOT present.** FM-2 (line 259) covers only the miss→epoch-read→`DoChan` windows | — |
| **§4 FM-2d** (overlapping wave of k stale holders → up to **k+1** fetches, not "exactly one") | **NOT present.** FM-2 and FM-10 (line 267) assert "at most one extra call" / "exactly one refetch" with no per-wave bound | — |
| **§4 FM-3b** (persistent governance 401 with a *valid* token → 1 fetch per audit fact on the live inline path; no attempt cap, no backoff, no metric) | **NOT present.** FM-9 (line 263) covers only "no scope metric fires there"; no unbounded-fetch statement, no M1 cooldown or A1 acceptance | `service/audit_ledger.go:79`: `_ = publisher.Publish(ctx, fact)` — swallow, no counter, no cooldown |
| **§4 FM-10** — qualify "exactly one": holds only in the settled-epoch steady state (T-4 step 3, each drain tick); and restate the mechanism (no retention ⇒ late arrivals start a new `fn`, short-circuited by the closure's `cachedToken()` re-check — the cache commits before entry deletion) | **NOT corrected.** Line 267 asserts "all join the post-invalidation epoch group → exactly one refetch" unqualified, with no mechanism statement | singleflight.go deletion-before-delivery (above); oauth.go:126 closure re-check verified |
| **§6 post-invalidate `AccessToken` interleaving** (the only test that can fail under the epoch-keying regression) | **NOT present.** Hardening row (line 315) starts only one `AccessToken` (pre-invalidate); under constant-`"client"`-key regression it joins the stale group, receives the revoked token from the shared channel, and every current assertion still passes | Reviewer's regression analysis confirmed: test as written cannot fail |
| **§6 T-4 part 1** — 401 body carries a scope code; assert `errors.Is(err, ErrTokenRejected) && !errors.Is(err, ErrTokenScopeMissing)` | **NOT present.** Line 304: "events → 401", body unspecified, only `ErrTokenRejected` asserted. With an empty body the precedence rule is untestable (a body-code-first regression still lands in the 401 branch) | §2.3's precedence rule is correct as written but unpinned by any test |
| **§6 token-endpoint-401 boundary test** (`/token` → 401 ⇒ `ErrTokenUnavailable`, never `ErrTokenRejected`, no invalidate) | **NOT present.** No named test pins the FM-3/REQ-3 boundary (a plausible implementer mistake: adding the 401 branch in `fetchToken`) | Deterministic per reviewer; `fetchToken` folds 401 into `ErrTokenUnavailable` today |
| **§6 403-no-invalidate** (cached token survives a 403; `/token` counter unchanged) | **NOT present.** Scope-missing 1 asserts the sentinel only; an invalidate-on-403 regression creates the refetch→403→invalidate loop the 401-only rule exists to avoid | — |
| **§6 non-JSON body** (`json.Unmarshal` failure branch of `classifyPublishError`) | **NOT present.** Scope-missing 1's "unrelated code" is valid JSON; the malformed-body branch is untested | — |
| **§6 Record-inline negative** (`Record` + fake repo + publisher returning `ErrTokenScopeMissing` + spy ⇒ spy not called) | **NOT present.** Pins REQ-5's "drain-path-only" boundary against the natural implementer mistake (emitting in the inline loop where errors are visible) | `fakeAuditLedgerRepo` exists in `sync_audit_export_test.go` per reviewer; `Record` at audit_ledger.go:68-80 |
| **§6 epoch-stays-0** (no-op `Invalidate` leaves `epoch == 0`; only a real invalidate bumps to 1) | **NOT present.** Line 314 asserts only `cachedToken()` untouched | `tokens.bindings[tenantID].epoch` is in-package accessible; also covers the invalid-tenant-ID branch (`TenantSourceID` error path) |
| **Q2b — explicit resolution for 403 + `{"error":"invalid_token"}`** (handled via M2, or documented-accepted-risk via A2) | **NOT present, no resolution of either kind.** `classifyPublishError` reads only `insufficient_scope`/`invalid_scope` on non-401; `invalid_token` → generic error, **no invalidation**, dead token reused until expiry — the exact up-to-24 h window the design exists to close. The doc never mentions `invalid_token` as a non-401 body code | Billing precedent (client.go:277-282) classifies `invalid_token` → `ErrUnauthorized`; governance authorities do express revocation as 403+`invalid_token` in real deployments |

## 2. Residual evidence-vs-claim mismatch (must be flagged)

1. **F-A as stated by the security reviewer is false on the live tree.** The assessment claims "nothing in
   the tree inserts `audit_publications` rows" (it grepped `AuditPublicationRepository` interface
   references and missed the row construction at repository/audit_ledger.go:198-200). The feed path is
   live: every `Append`/`AppendInTx` creates a `pending` publication row; the ticker started at
   new.go:179 drains it. Consequences: (a) the design doc's liveness premise is *correct* and must be
   kept — "correcting" it per the reviewer's direction would introduce a new false claim; (b) what the
   doc must instead add is the missing *citation* of the feed path (E7 currently cites only the drain
   half) and the security reviewer's genuinely valid Q3 residual: the publish wave observing the 401 is
   still **permanently dropped from governance delivery with zero signal** on the inline path, and
   REQ-5's metric counts only drain-path facts. That residual needs the M3b (inline publish-failure
   metric) or M3c (explicit acceptance) treatment the doc lacks.
2. **The requirements artifact's own verification** (requirements-10762e10) treats the drain as live
   ("Retry/drain path exists … ticker at callback.go:267") — consistent with the tree and with the
   design doc; only the security assessment diverges. The doc should state liveness explicitly with the
   feed-path citations so the divergence is resolved in favor of the tree.
3. **FM-10/T-4 step 3's determinism mechanism is unstated.** With no retained results, "exactly two
   `/token` hits" does not follow from group membership alone; it follows from closure `cachedToken()`
   re-check short-circuiting late arrivals (commit-before-delete ordering). The doc's FM-10 and the §6
   T-4 step 3 row should state this, and the test's `/token` counter and header captures must be
   `atomic.Int32`/mutex-guarded (handlers run on server goroutines; `go vet` will not catch a plain-int
   race), with the "401 once" events handler explicitly stateful.
4. **Minor line drift already known** (TokenSource at types.go:58-61, not :75-79; ticker call :266;
   `wireAuditServices` invoked at new.go:150, not :148) — immaterial but the doc's E7 should cite
   new.go:179 (ticker start) and new.go:150 for the liveness claim.
5. **§7 headroom estimates assume only one new counter.** If the F-B fix is implemented as a new
   audit-publication outcome counter (recommended), control_plane.go grows beyond the current
   "176 → ~188" estimate and §7 must be re-derived; likewise if M2/G2a add an error code or metric.

## 3. Propagation requirements once corrected (consistency obligations)

- **FM-8 fix** must propagate to: FM-10's mechanism sentence, §6 T-4 step 3's "Proves" column (epoch
  separation + closure re-check, not retention), and the §2.2 rationale ("singleflight keying is the dedup
  mechanism" stays; the retention rationale disappears).
- **F-B fix** must propagate to both FM-3 (line 260) and §5 step 9 (lines 290-292) — a counter change
  cannot be applied to one and missed in the other; G2a (distinct `audit.token_endpoint_rejected`
  outcome code via `errors.Is(err, ErrTokenUnavailable)` in `publicationOutcome`) is the companion fix
  that makes the runbook discriminator real.
- **Q2b resolution** must appear in three places consistently: §2.3 `classifyPublishError` (if M2:
  `invalid_token` on non-401 → `ErrTokenRejected` + invalidate, 401-outranks-body precedence unchanged;
  if A2: no code change but an explicit "documented-accepted-risk" FM row), §5 runbook (the 403-revocation
  deployment pattern), and §6 (M2: a 403+`invalid_token` test asserting `ErrTokenRejected` + invalidate +
  refetch; A2: a connector contract test pinning the authority's 401 contract).
- **FM-2b/2d/3b additions** must be consistent with FM-1/FM-2/FM-9: FM-2b inherits FM-1's discard guard
  and the bulkhead bound (P6-06, ≤64); FM-2d's k+1 bound supersedes the absolute "at most one extra
  call" wording in FM-2's correctness column; FM-3b must reference the M1/A1 decision and FM-9's
  drain-path-only scoping.
- **§6 additions** must be named in the REQ→test coverage table: the post-invalidate interleaving
  extension pins REQ-2's "never deduped against a post-invalidation fetch" clause (currently covered by
  no discriminating test); the token-endpoint-401 boundary test pins REQ-3's boundary; the Record-inline
  negative pins REQ-5; the epoch-stays-0 assertion pins REQ-1's no-op clause.

## 4. Required next action

Revise `docs/plans/2026-08-06-tokensource-invalidate-on-401-epoch-singleflight.md` to: (1) rewrite the
FM-8 row (no retention, drop `Forget`); (2) correct FM-3 and §5 step 9 metric identity (drop the
callback-deliveries citation; add an audit-publication outcome counter or state DB-only observability
with the G2a caveat); (3) add FM-2b, FM-2d, FM-3b rows and qualify FM-10's "exactly one" (settled-epoch
steady state) with the closure-recheck mechanism; (4) extend §6 with the post-invalidate `AccessToken`
interleaving, T-4 part 1 scope-code body + `!errors.Is(err, ErrTokenScopeMissing)`, token-endpoint-401
boundary, 403-no-invalidate, non-JSON body, Record-inline negative, and epoch-stays-0 tests; (5) add an
explicit Q2b resolution (M2 handling or A2 documented-accepted-risk) in §2.3/§4/§5/§6; (6) keep the
drain-liveness premise but cite the feed path (`createAuditRows`, new.go:150/:179) and add the Q3
residual-loss acceptance (M3b or M3c); (7) re-derive §7 if the F-B fix adds a counter.

VERDICT: FAIL — the design doc is unrevised; none of the seven required correction groups is present, the
Q2b resolution is absent, and the F-A "correction" as recommended by the security reviewer would itself
be false on the live tree (the durable drain is fed and `publishAuditFact` runs in production).
