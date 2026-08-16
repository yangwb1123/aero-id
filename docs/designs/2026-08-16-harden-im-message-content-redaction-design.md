# Design — Harden IM Message-Content Redaction Allow-List at the Connector Boundary

- **Date:** 2026-08-16
- **Module:** `internal/connector` (shared redaction), consumed by `internal/connector/aeroim`
- **Spec:** REQ-aeroim-redact-message-content (verified against `1e0abb2`)
- **Status:** Proposed — implementation and acceptance are deliberately NOT included in this repository change set; this document is the design deliverable.

---

## 1. Verification verdict

All claims in the requirements specification were checked against the live tree at
commit `1e0abb2` (plus a transient in-package probe that was removed afterwards; the tree is byte-identical).

| Claim | Verification result |
|---|---|
| `sensitiveSummaryParts` at `redact.go:28-32`, contains `message_body` only | ✅ Confirmed (`grep -n`), no `message_text`/`latest_message`/`recent_message`/`message_preview`/`chat_history`/`conversation` |
| `canonicalSummaryKey` at `redact.go:79-81` strips `- _ . space`, lowercases | ✅ Confirmed |
| `sensitiveSummaryKey` at `redact.go:83-90`: substring match on canonical parts + exact `body/content/raw/messages/message` | ✅ Confirmed |
| `redactSummaryMap` recursion, depth cap 6, 64 KiB truncation envelope | ✅ Confirmed |
| Empirical leak: all six keys survive `RedactSummaryData`; counters retained | ✅ Confirmed by probe run against production code (`CONFIRMED LEAK` for all six; `message_count=5, comment_count=3, unread_count=2` retained) |
| Persistence chain `http_summary_request.appendDatasetSnapshots` → `aggregation.persistSummary` → `UpsertSnapshot` (writes `"data"` JSONB) → replication/reconcile | ✅ Confirmed (`http_summary_request.go`, `aggregation.go:151`, `account_snapshot.go` `DoUpdates` includes `"data"`, `projection_replication.go` publishes `snapshot.Data` verbatim, `sync_reconcile.go:49` digests `Data`) |
| No live consumer of the six keys | ✅ Confirmed: repo-wide grep only hits the legacy `/v1/conversations` HTTP routes and deprecation middleware, unrelated to projections |
| Existing tests unaffected by the proposed block shapes | ✅ Confirmed: `redact_test.go` keys (`keyboard/monkey/keynote/display_name`, credential shapes, `message/messages/body/content/raw`) and `http_summary_test.go` payloads contain no canonical collision with the six new shapes |
| `sourceMigrationCounters` counter contract (`file_count/object_count/message_count/comment_count`, consumed as `aero-im.message_count` / `aero-im.comment_count`) | ✅ Confirmed `legacy_migration.go:124-147,172-175`; none of the counter canonical forms (`filecount/objectcount/messagecount/commentcount/unreadcount`) contains any new shape |

**Verdict: verified.** The leak is real, the durability chain is real, and the
proposed fix is a minimal, backward-compatible extension of the existing redaction
machinery. Two issues not called out in the spec are documented in §6 and §8
(`conversation` substring breadth; already-persisted rows are not scrubbed).

---

## 2. Problem statement

`RedactSummaryData` (`internal/connector/redact.go:13`) is the anti-corruption
boundary that converts source summary payloads into durable aero-id projections.
AGENTS.md requires that the boundary emit only "redacted account/workspace/resource-summary
facts" and that aero-id "must not persist … unrestricted IM message history."

Today, scalar values under keys `message_text`, `latest_message`, `recent_message`,
`message_preview`, `chat_history`, and `conversations` pass redaction verbatim
(empirically confirmed, §1) and are persisted into the durable `account_snapshots.data`
JSONB column via `aggregation.persistSummary` → `repository.UpsertSnapshot`, then
propagate to replicated projections and reconciliation digests. The root cause is
that the block shapes are substring/allow-list based and none of the six canonical
forms (`messagetext`, `latestmessage`, `recentmessage`, `messagepreview`, `chathistory`,
`conversations`) contains the existing `messagebody` shape or equals `message`/`messages`.

---

## 3. Design

### 3.1 Change summary

One internal constant change plus tests. No public API change, no transport change,
no SQL/proto change, no audit-site change.

- **Primary change:** extend `sensitiveSummaryParts` (`internal/connector/redact.go:28-32`)
  with the six message-content shapes. The existing canonicalization + substring
  machinery (`canonicalSummaryKey`, `sensitiveSummaryKey`, `redactSummaryMap`)
  then blocks every case-insensitive separator variant at all nesting depths
  (maps, `[]any` elements) up to the existing depth cap of 6, with zero new
  recursion logic.
- **Rationale for this location:** `sensitiveSummaryParts` is the single,
  already-tested extension point; `canonicalSummaryParts()` canonicalizes it once
  into `sensitiveSummaryCanonicalParts`, so the per-key hot path is unchanged.
- **Deliberately NOT changed:** the exact-match list in `sensitiveSummaryKey`
  (`body`, `content`, `raw`, `messages`, `message`) stays as-is (REQ-4).

### 3.2 Exact edit spec (implementation guidance, not applied here)

```go
// sensitiveSummaryParts lists credential and message-content shapes in
// human-readable snake_case. Every part is canonicalized before matching, so
// camelCase and separator-free spellings resolve to the same shape. Message
// content must never become a durable projection (AGENTS.md: no unrestricted
// IM message history); counter keys (message_count etc.) are deliberately not
// substrings of these shapes.
var sensitiveSummaryParts = []string{
    "password", "token", "secret", "cookie", "mfa", "recovery", "credential", "private_key",
    "access_key", "refresh", "authorization", "object_bytes", "blob", "message_body",
    "api_key", "signing_key",
    // message-content shapes (REQ-1):
    "message_text", "latest_message", "recent_message", "message_preview", "chat_history",
    "conversation",
}
```

Net diff: +7 lines in a 91-line file (well under the 500-line file cap; no function
gains lines, so the 50-line/function and cyclomatic gates are untouched).

### 3.3 Behavior contract after the change

Blocked (dropped at every depth, all separator/case variants via canonicalization):

- The six message-content shapes and their plural/derived forms that contain them:
  `message_text`, `latest_message`, `recent_message`, `message_preview`,
  `chat_history`, `conversations` (canonical `conversations` ⊇ `conversation`),
  plus variants `Message_Text`, `message-text`, `latestMessage`, `recent-message`,
  `chat.history`, `Conversations`, `user_message_text`, `latest_message_preview`, etc.
- All existing credential shapes and exact keys (`token`, `password`, `secret`,
  `access_key`/`accessKey`, `api_key`/`apiKey`, `signing_key`, `private_key`, `mfa`,
  `recovery`, `cookie`, `refresh`, `authorization`, `object_bytes`, `blob`,
  `message_body`, and exact `message`, `messages`, `body`, `content`, `raw`) — unchanged.

Retained (values intact, at all depths):

- `message_count`, `comment_count`, `unread_count`, `file_count`, `object_count`
  (canonical forms contain no new shape — §1 collision table; preserves
  `sourceMigrationCounters` and the `aero-im.message_count` / `aero-im.comment_count`
  drift comparison).
- Everything else that does not match a shape (e.g. `display_name`).

### 3.4 Alternatives considered and rejected

| Alternative | Why rejected |
|---|---|
| Exact-match-only blocking for the new shapes | `conversations` canonicalizes to `conversations` ≠ `conversation`; exact match would NOT block the empirically confirmed plural leak. REQ-1 explicitly requires substring semantics. |
| Bare `message` or `chat` substring part | Would block `message_count` (canonical `messagecount` ⊇ `message`), breaking REQ-3 and the migration-counter contract. |
| Value-type-aware redaction (drop string values) | Redaction is key-based by design; stringified counters (`"5"`) would be dropped and become a footgun. Rejected per REQ-1 guidance. |
| A separate `messageContentParts` slice merged at canonicalization time | More moving parts than needed; a comment group inside the single slice gives the same readability with a smaller diff. (Acceptable variant if reviewers prefer two slices — behavior identical.) |
| Scrub existing rows as part of this change | Data remediation is a separate operational concern (§8); this change is forward-only by design. |

---

## 4. API changes

- **Public API:** none. `RedactSummaryData(map[string]any) map[string]any` signature,
  behavior envelope (64 KiB truncation), and truncation payload shape are unchanged.
- **Internal API:** `sensitiveSummaryParts` gains six entries. All other symbols
  unchanged. No protobuf, no OpenAPI, no new RPC, no config keys.
- **Cross-source effect:** the function is shared by `aeroim`, `aerovault`, `snaplink`,
  and `auditgovernance` adapters, so Vault/Snaplink summary boundaries inherit the
  hardening identically. Verified: no existing test payload in any adapter uses a
  key that would newly match.

---

## 5. Compatibility constraints

1. **Counter contract is load-bearing:** `sourceMigrationCounters`
   (`legacy_migration.go:124-147`) reads `file_count`, `object_count`,
   `message_count`, `comment_count` from `snapshot.Data`; `compareMigrationCounters`
   consumes `aero-im.message_count`/`aero-im.comment_count` for drift comparison.
   The new shapes are disjoint from these canonical forms (verified, §1), so the
   legacy-migration compare keeps working. A regression here is caught by AC-1's
   counter-retention assertion before any integration surface notices.
2. **No consumer depends on the leaky keys:** repo-wide grep finds no live reader of
   the six keys; only the legacy `/v1/conversations` HTTP routes (unrelated to
   projections) mention `conversations`. Blocking them breaks nothing.
3. **Existing connector tests must pass unchanged:** `http_summary_test.go` and
   `redact_test.go` payloads contain no canonical collision (§1). In particular the
   `keyboard`/`monkey`/`keynote` generic-key guard still holds (none contains a new
   shape).
4. **Gates:** no migration-set impact (no SQL), no proto-sync impact, no audit sites
   added (`check-audit-swallow` scans `internal/connector` and stays green), file-size
   and function-length limits untouched (§3.2).
5. **Rollout compatibility:** redaction happens on inbound summary ingestion, so old
   and new instances can run side by side; there is no schema change and no
   read-path coupling to the removed keys.

---

## 6. Failure modes and mitigations

| # | Failure mode | Likelihood | Mitigation |
|---|---|---|---|
| FM-1 | **Over-blocking a future legitimate summary key.** `conversation` as a substring also drops hypothetical `conversation_count` / `conversation_id` / `conversation_name` scalars from a source summary. | Low today (no such consumer/contract in tree) but real | Documented tradeoff of REQ-1's substring semantics. If a non-message `conversation_*` counter becomes a contract, it needs an explicit allow-list carve-out (e.g. top-level exact-key pass-through before shape matching) — a separate, deliberately reviewed change. Guard now: AC-1 counter-retention assertions pin the full current counter set. |
| FM-2 | **Under-blocking a new variant** (e.g. `msg_text`, `last_message`, `messageTextPreview`). The allow-list is shape-based; unlisted shapes pass. | Medium — future source naming | The shape list is the single extension point; adding `last_message`/`msg_text` is a one-line change when discovered. Boundary redaction is defense-in-depth, not the primary control: the source summary contract itself must not include message content (AGENTS.md boundary rule). Tests cross-check with `containsSensitiveSummaryKey` (production matcher) at depth. |
| FM-3 | **Regression of the migration-counter contract** (a future shape colliding with `message_count`). | Low | AC-1 asserts `message_count/comment_count/unread_count` retention; `TestJSONSummaryConnectorRedactsSensitiveFields` stays green (AC-2). |
| FM-4 | **Truncation-envelope interaction.** Deeply nested payloads hitting the depth/64 KiB caps. | Unchanged behavior | No new recursion; existing caps and truncation shape untouched. |
| FM-5 | **Already-persisted leaked rows are not scrubbed.** Rows written before rollout remain in `account_snapshots.data` and replicate via existing snapshots. | Certain for pre-existing data | Forward-only fix. Residual risk must be accepted or remediated separately (§8). The projection is disposable/replayable: a re-fetch + `persistSummary` refresh via reconciliation overwrites `data` with redacted values *if* the source no longer returns content; otherwise targeted backfill scrubbing is required. |

---

## 7. Migration steps

No data migration is required (no schema change). Steps are code + test + release:

1. **Edit** `internal/connector/redact.go` — extend `sensitiveSummaryParts` per §3.2
   (7 added lines, comment group).
2. **Add tests** to `internal/connector/redact_test.go` per AC-1 (table-driven
   subtest + counter-retention + nested/array + exact-key regression), each subtest
   ≤ 50 lines, cyclomatic ≤ 10. Optionally cross-check nested payloads with the
   existing `containsSensitiveSummaryKey` helper.
3. **Unit gate:** `go test ./internal/connector/...` (AC-3) — must include
   `redact_test.go`, `http_summary_test.go`, `http_summary_request_test.go`,
   `contract_test.go`, `fault_injection_test.go`.
4. **Done gate (unchanged):**
   `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build`.
   No `make proto` / `make migrate-up` / migration-set gates are affected.
5. **Deploy:** normal rollout; redaction applies on ingestion, so no
   blue/green sequencing constraint. No config or secret changes.
6. **Post-deploy audit (optional, recommended):** before rollout, size pre-existing
   exposure with a read-only query over `account_snapshots.data` for the six keys
   (per region) to decide whether FM-5 remediation (re-fetch + reconcile, or
   targeted scrub) is warranted. This is a separate task; do not couple it to this
   change.

---

## 8. Out of scope / residual risk

- **Pre-existing durable rows containing the six keys are not scrubbed by this
  change.** Owners must decide remediation separately (§6 FM-5).
- Per-source scope-type allow-listing for memberships/sources (separate direction).
- `internal/connector/aeroim` wrapper tests and composition-root wiring (separate).
- Any change to the Aero IM source contract or source-side summary generation.
- No SQL migration, protobuf, or audit-site changes — by design.

---

## 9. Testable acceptance mapping

| Acceptance (spec) | Concrete test / gate | Pass condition |
|---|---|---|
| AC-1 key variants | `TestRedactBlocksMessageContentKeyVariants` (new, `redact_test.go`) — table over `message_text`, `latest_message`, `recent_message`, `message_preview`, `chat_history`, `conversations`; input `RedactSummaryData(map[string]any{key:"SECRET-IM-CONTENT", "display_name":"Ada"})` | `got[key] == nil` **and** `got["display_name"] == "Ada"` for every row |
| AC-1 counter retention | Same test file: `RedactSummaryData(map[string]any{"message_count":int64(5),"comment_count":int64(3),"unread_count":int64(2)})` | All three retained with values 5/3/2 (pins `legacy_migration` contract, guards FM-1) |
| AC-1 nested/array | Same test file: `latest_message` at depth 2; `chat_history` inside a `[]any` element; cross-check with `containsSensitiveSummaryKey` | Neither key present in output at any depth |
| AC-1 exact-key regression | Same test file: keys `message`, `messages`, `body`, `content`, `raw` | All dropped (pins REQ-4) |
| AC-2 | `TestJSONSummaryConnectorRedactsSensitiveFields` (`http_summary_test.go:12`) unchanged | Passes: `token` dropped, `display_name` retained |
| AC-3 | `go test ./internal/connector/...` | Full connector suite green |
| AC-4 | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make build` | All green |

---

## 10. References

- AGENTS.md — "Event, outbox, and synchronization invariants"; "Data ownership and
  minimization"; engineering limits table.
- `internal/connector/redact.go` (block shapes/matcher), `internal/connector/http_summary_request.go`
  (boundary ingestion), `internal/service/aggregation.go` (`persistSummary`),
  `internal/repository/account_snapshot.go` (`UpsertSnapshot`), `internal/service/legacy_migration.go`
  (counter contract), `internal/service/projection_replication.go` / `sync_reconcile.go`
  (projection propagation).
- Spec REQ-aeroim-redact-message-content (evidence, verified).
