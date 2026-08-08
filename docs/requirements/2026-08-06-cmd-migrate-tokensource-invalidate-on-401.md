# Requirements Specification — audit governance OAuth client: TokenSource invalidate-on-401 with epoch single-flight backfill and scope-missing alerting

> Target code is `internal/connector/auditgovernance` (+ the service-layer metrics seam); the direction was filed under
> campaign module label `cmd/migrate` (analysis `cmd-migrate-c1b3771d.json`), same labeling situation as
> `2026-08-06-cmd-migrate-check-audit-swallow-gate.md`.

| | |
|---|---|
| Module | `cmd/migrate` (campaign label) → target: `internal/connector/auditgovernance`, `internal/pkg/metrics`, `internal/service` |
| Direction | "TokenSource Invalidate-on-401 with epoch single-flight backfill and scope-missing alerting for the audit governance OAuth client" |
| Source analysis | `docs/auto/analyses/cmd-migrate-c1b3771d.json` (direction #3) |
| Date | 2026-08-06 |
| Scope | Invalidation seam + epoch-guarded singleflight, 401 classification in `Publisher.Publish`, scope-missing sentinel + distinct alert metric, and the supplied acceptance checks — nothing else |

## 1. Problem statement

`tenantTokenSource` caches a client-credentials token until `refreshAt` (up to 24 h: `cacheToken` caps the lifetime at
`min(expires_in, 86400s)` minus `refreshSkew`). There is no invalidation path. When the governance endpoint rejects a
publish with HTTP 401 (token rotation/revocation), `Publisher.Publish` surfaces a generic `audit governance: HTTP 401`
error and every subsequent attempt reuses the same poisoned cached token until expiry, so the durable drain
(`callbackService.drainPublications`) retries with a dead token and eventually dead-letters the fact — audit facts can
be lost for up to a day with no operator signal.

Three concrete gaps, all verified against the repository (Section 2):

1. **No `Invalidate`/epoch mechanism.** `tenantTokenSource` (oauth.go) exposes only `cachedToken`/`fetchToken`/
   `cacheToken`; the `singleflight.Group` at `s.fetches` (oauth.go:126) dedupes fetches but cannot force a backfill
   after invalidation, and a stale in-flight fetch could repopulate the cache after an invalidation.
2. **401 is indistinguishable from any other failure.** `validateReceipt` (publisher.go:220-237) accepts only 202;
   every other status — including 401 and scope-missing 403 — becomes `fmt.Errorf("audit governance: HTTP %d", ...)`
   (publisher.go:224) with the response body discarded.
3. **No scope-missing alert.** The existing `ObserveAuditIngestFailure` metrics hook (control_plane.go:97) fires only
   on repository append failure (audit_ledger.go:73); publish failures are silent. Billing precedent
   (client.go:270-282) shows the repo already classifies `insufficient_scope`/`invalid_token` and 401/403 into a
   distinct `ErrUnauthorized`, but the audit governance connector does not.

## 2. Evidence verification (all citations re-checked against the repository on 2026-08-06)

| # | Cited evidence | Verification result |
|---|---|---|
| E1 | `internal/connector/auditgovernance/oauth.go` — `tenantTokenSource.cachedToken`, `fetchToken`, `s.fetches.DoChan` singleflight, `cacheToken`; **no `Invalidate` symbol** | **Verified.** Struct at oauth.go:34-42 (`token string`, `refreshAt time.Time`, `mu sync.RWMutex`, `fetches singleflight.Group`). `accessToken` at :122-145 calls `s.fetches.DoChan("client", ...)` (:126) whose closure only re-checks `cachedToken` — dedup, no backfill trigger. `cachedToken` at :147-151 returns `(s.token, s.token != "" && s.now().Before(s.refreshAt))`. `fetchToken` at :153-173 maps any non-200 to `ErrTokenUnavailable: HTTP %d`. `cacheToken` at :210-217 caps lifetime at `min(expires_in, 86400)` seconds minus skew. Grep for `Invalidate` across `internal/connector/` → **zero matches**. No `epoch` field exists. |
| E2 | `internal/connector/auditgovernance/publisher.go` — `Publish` → `validateReceipt` only accepts 202; 401 becomes generic `HTTP 401` error | **Verified.** `Publish` at :83-101: `authorization` (:103-120) → `newRequest` → `client.Do` → `validateReceipt`. `validateReceipt` at :220-237: `if response.StatusCode != http.StatusAccepted` → `fmt.Errorf("audit governance: HTTP %d", response.StatusCode)` (:224) after discarding the body (`io.Copy(io.Discard, ...)`, :223). No status classification, no invalidation call, no retry loop. `IdempotencyKey` = `fact.ID.String()` (:185) — stable across retries (precondition for T-10.1). `Publisher.tokens` is the `TokenSource` interface (types.go:75-79: only `AccessToken`, `HasBinding`). |
| E3 | `internal/connector/billing/client.go:281` — in-repo 401/403 precedent | **Verified (line 281-282).** `decodeBillingError` maps body codes `invalid_token`, `insufficient_scope`, `metering_source_unauthorized` → `ErrUnauthorized` (:277-279) and `status == 401 \|\| status == 403` → `ErrUnauthorized` (:281-282). Precise scope of the precedent: **error classification** of 401/403 and scope-missing responses. Billing's own token source (billing/oauth.go) has no cache invalidation either — the precedent is classification, not epoch invalidation; the spec below does not overclaim it. |
| E4 | `internal/pkg/metrics/control_plane.go:97` — `ObserveAuditIngestFailure` hook | **Verified.** `Registry.ObserveAuditIngestFailure(source string)` at :97-99 increments `control.auditIngestFail`; rendered at :141 as `aero_control_audit_ingest_failures_total{source=%q}`. Existing counters all follow `aero_control_*_total` naming (e.g. `aero_control_events_quarantined_total`, `aero_control_callback_deliveries_total`). |
| E5 | `internal/service/control_plane_metrics.go:55` — service seam | **Verified.** `ControlPlaneMetrics` interface at :15 (includes `ObserveAuditIngestFailure(source string)`); `noopControlPlaneMetrics` at :29; `RegistryControlPlaneMetrics.ObserveAuditIngestFailure` at :55-56. |
| E6 | `internal/service/audit_ledger.go` — hook fires only on append failure; publish failures swallowed | **Verified.** `Record` at :68-80: `ObserveAuditIngestFailure` only on `repo.Append` error (:73); the inline `_ = publisher.Publish(ctx, fact)` loop at :79 swallows publish errors (that inline loop is direction #1 of the same analysis — out of scope here, see §5). |
| E7 | Durable drain path (the path that retries a failed publish) | `internal/service/callback_audit.go`: `drainPublications` at :47-89 claims `audit_publications` rows; `publishAuditFact` at :99-112 calls `publisher.Publish` (:108); `publicationOutcome` at :87-97 maps failures to `pending`/`dead_letter` — retried on the next tick from `callback.go:267` (ticker → `drainPublications`). `callbackService` already carries the metrics seam: `metrics ControlPlaneMetrics` (callback.go:66), `SetControlPlaneMetrics` (:99-103), wired from the composition root (`internal/app/new.go:148`). No retry loop inside the connector is needed or wanted: retries are the caller's durable mechanism. |
| E8 | Composition root | `internal/app/audit.go:38-67` `buildAuditPublishers`: constructs `auditgovernance.NewOAuthTokenSource` (:50) and `auditgovernance.NewPublisher` (:58); the same `[]service.AuditPublisher` is handed to both the ledger service and the callback service via `SetAuditPublishers` (app/audit.go:26-35). No metrics are injected into the connector today. |
| E9 | Test precedent | `publisher_test.go` uses `httptest` servers with `/token` + `/api/v1/events` handlers (`TestPublisherUsesDerivedSourceAndMatchingLedgerReceipt`, :18-73) and `auditBinding` helper (:164-169). Tests are in-package (`package auditgovernance`), so `cachedToken()` is directly assertable. |

## 3. Requirements

### REQ-1 — Invalidation seam on `TokenSource`

Add `Invalidate(SourceBinding)` to the `TokenSource` interface in `internal/connector/auditgovernance/types.go`
(:75-79) and implement it on `OAuthTokenSource`/`tenantTokenSource` (the only implementer — E1).

- `tenantTokenSource` gains an `epoch uint64` field.
- `Invalidate(binding)` resolves the per-tenant source and, under `mu.Lock`: clears `s.token = ""`, resets
  `s.refreshAt` to the zero time, and increments `s.epoch`.
- An invalidate for an unbound tenant is a no-op (the publisher only invalidates the binding it just used).
- After `Invalidate`, `cachedToken()` MUST return `("", false)` — the very next `AccessToken` performs a fresh fetch.

### REQ-2 — Epoch-guarded single-flight backfill (one refetch under N concurrent publishes)

- `accessToken` (oauth.go:122-145) reads `s.epoch` under `RLock` and keys the singleflight group by
  `"client-" + epoch` instead of the constant `"client"`. A post-invalidation refetch therefore joins a **new**
  singleflight group: all N concurrent callers after invalidation share one key and trigger exactly one fetch; a
  pre-invalidation in-flight fetch is never deduped against a post-invalidation one.
- The singleflight closure captures the epoch at start and passes it to `fetchToken`; `cacheToken(wire, epoch)`
  (oauth.go:210-217) discards the result under `Lock` when `epoch != s.epoch`, so a stale pre-invalidation fetch can
  never repopulate the cache after `Invalidate` (no poisoning re-entry).
- Token cache TTL policy (24 h cap, `refreshSkew`) is unchanged; the epoch is an invalidation guard, not a new
  expiry mechanism.

### REQ-3 — Publisher classifies 401 and invalidates before returning

In `Publisher.Publish` (publisher.go:83-101), when `response.StatusCode == http.StatusUnauthorized`:

- call `p.tokens.Invalidate(SourceBinding{TenantID: fact.TenantID, SourceSystem: source})` (the binding already
  computed by `authorization`), and
- return a distinct sentinel error — new `ErrTokenRejected` in types.go — wrapped as
  `fmt.Errorf("%w: HTTP %d", ErrTokenRejected, response.StatusCode)`, replacing the generic
  `audit governance: HTTP 401` (publisher.go:224).

The invalidation is **status-driven** (any 401), independent of whether the response body parses. No retry loop is
added inside `Publish`: the retry that benefits from the refetch is the caller's durable mechanism (drain tick /
caller loop — E7), which now succeeds because `cachedToken()` is empty.

### REQ-4 — Scope-missing responses produce a distinct sentinel error

On non-202 statuses, instead of discarding the body, read it with the existing bounded limit (`maxReceiptBytes`,
publisher.go:20) and best-effort parse an error code following the billing precedent (E3: `{"error": "..."}` envelope,
client.go:271-277). If the code is a scope-missing code (`insufficient_scope`, `invalid_scope`), return a new sentinel
`ErrTokenScopeMissing` (types.go), wrapped with the HTTP status. Body-parse failure stays non-fatal: the generic
status error (or `ErrTokenRejected` for 401) is returned as today, and the scope-missing check never blocks the
invalidation of REQ-3.

### REQ-5 — Distinct scope-missing alert metric

- `internal/pkg/metrics/control_plane.go`: add a `counterMap` (label `source`) and `ObserveTokenScopeMissing(source
  string)` on `Registry`; render it in `writeControlPlaneText` following the existing `aero_control_*_total`
  convention — chosen name **`aero_control_audit_scope_missing_total{source=%q}`** (the direction's placeholder name
  `auditgovernance_token_scope_missing` is the semantic intent; the binding requirement is a metric **distinct** from
  `aero_control_audit_ingest_failures_total`, test-verifiable per §4).
- `internal/service/control_plane_metrics.go`: add `ObserveTokenScopeMissing(source string)` to the interface (:15),
  the noop impl (:29), and the `RegistryControlPlaneMetrics` adapter (:55).
- **Emission point:** `callbackService.publishAuditFact` (callback_audit.go:99-112) — when
  `publisher.Publish` returns an error with `errors.Is(err, auditgovernance.ErrTokenScopeMissing)`, call
  `s.metrics.ObserveTokenScopeMissing(strings.TrimSpace(fact.Source))` before returning the error. This is the
  durable path; `callbackService` already has the metrics seam (E7) and the composition root already attaches the
  registry adapter to all setters (app/new.go:148). Do **not** add emission to `auditLedgerService.Record`'s inline
  loop — that loop is direction #1's removal target (§5).
- The 401-invalidation itself also removes the silence root cause on the legacy inline path: `Record`'s `_ =
  publisher.Publish(...)` (audit_ledger.go:79) calls the same `Publish`, so a 401 there now invalidates the token
  even before the drain's next tick refetches.

### REQ-6 — No contract changes to the 202/receipt path

`validateReceipt`'s 202-only acceptance, receipt envelope validation (`receiptMatches`), and `IdempotencyKey =
fact.ID.String()` (:185) are unchanged. Retried publishes carry the identical idempotency key; deduplication of the
governance event remains the governance side's job (T-10.1 asserts aero-id does not perturb the key).

## 4. Acceptance checks (supplied checks preserved; made testable)

All tests are in-package unit tests (`package auditgovernance` / `package service` / `package metrics`) using
`httptest` servers per E9, with a `/token` hit counter on the test server. No real network, no Docker.

**T-4 (part 1) — 401 invalidates immediately; N concurrent publishes trigger exactly one refetch (epoch + singleflight).**
Unit test: prime the cache with one successful publish (server: `/token` returns a valid token, hit counter = 1;
`/api/v1/events` returns 202 with a matching receipt). Then make the events endpoint return 401 once. Assert:

1. `Publish` fails with `errors.Is(err, ErrTokenRejected)`.
2. Immediately after, `tokens.bindings[tenantID].cachedToken()` returns `("", false)` (in-package access, E9).
3. Run N ≥ 8 concurrent `Publish` calls (same fact) against an events endpoint that now accepts (202); assert
   `/token` hit counter == **2** total (one refetch for all N) and all N publishes succeed.
4. Assert every one of the N governance requests carries `Authorization: Bearer <refetched-token>` — i.e. the
   refetched token, not the revoked one.

**T-4 (part 2) — after invalidation the refetched token is used on the retry and the publication completes.**
Same fixture: first `Publish` → 401, second `Publish` (same fact) → 202; assert the second attempt's
`Authorization` header equals the token served by `/token` after the invalidation, and `Publish` returns nil.

**T-10.1 — a 401 followed by refetch-success does not duplicate the governance event (idempotency_key unchanged).**
The test server captures the full JSON body of the 401'd request and of the successful retry; assert
`body["idempotency_key"]` and `body["event_id"]` are identical across both (both equal `fact.ID.String()`) and that
the invalidation/refetch path never generates a new idempotency key. (Governance-side dedup on that key is the
authority's contract; aero-id must not perturb it.)

**Scope-missing alert (unnumbered in the supplied direction) — distinct metric, verified by test.**
1. Connector test: events endpoint returns 403 with body `{"error":"insufficient_scope"}`; assert
   `errors.Is(err, ErrTokenScopeMissing)`. Repeat for `invalid_scope`.
2. Service test: `callbackService.publishAuditFact` with a fake `AuditPublisher` returning `ErrTokenScopeMissing`
   and a metrics spy → `ObserveTokenScopeMissing(source)` called exactly once with the fact's source.
3. Registry test: after `ObserveTokenScopeMissing("aero-id")`, the rendered registry text contains
   `aero_control_audit_scope_missing_total{source="aero-id"}` and the counter is distinct from
   `aero_control_audit_ingest_failures_total` (zero interaction between the two counters).
4. Negative: a non-scope 500 response does not increment the scope-missing counter.

## 5. Out of scope (explicit)

- **Direction #1 of the same analysis** (remove the inline `_ = publisher.Publish` loop in `auditLedgerService.Record`,
  AppendInTx consolidation): still live at audit_ledger.go:79; not touched here.
- **Direction #2** (`make check-audit-swallow` gate, ~25 `_ = s.audit.Record/Log` call sites): no Makefile target
  exists yet; not touched here.
- No retry loop inside `Publisher.Publish`; no changes to the 202/receipt contract, token TTL policy, or
  `validateReceipt` semantics beyond status/body classification.
- No new configuration keys, no migrations (no schema change), no proto changes, no connector metrics dependency
  (the connector stays dependency-free; the metric is emitted at the service seam, REQ-5).

## 6. Engineering constraints and done gate

- File limits (non-test): oauth.go is 217 lines, publisher.go 258, control_plane.go 176, control_plane_metrics.go 81,
  callback_audit.go 113 — all have headroom, but REQ-1/REQ-2 additions must keep oauth.go ≤ 500 and REQ-3/REQ-4 must
  keep publisher.go ≤ 500; split into sibling files (e.g. `publisher_errors.go`) at ~400 lines per AGENTS.md.
- Function limits: `Publish` and `validateReceipt` must stay ≤ 50 lines and cyclomatic ≤ 10; new status/body
  classification belongs in a small helper (≤ 50 lines, cyclomatic ≤ 10); package average cyclomatic ≤ 5.0.
- Repository errors stay out of the connector; new sentinels live in types.go alongside the existing `Err*` set.
- Done gate: `make test && make lint && make lint-strict && make check-file-sizes && make build`. No
  `make test-integration` run is required (no SQL/storage/connector-network contract change), but the changed
  connector tests must pass under `make test`.
