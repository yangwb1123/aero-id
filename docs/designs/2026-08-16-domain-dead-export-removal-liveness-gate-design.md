# Design — `internal/domain` dead exported-symbol removal + exported-symbol liveness gate (rule f)

Status: **Proposed** (implementation-ready)
Date: 2026-08-16
Module: `internal/domain` (R1/R3/R6), `internal/repository/webhook.go` (R2), `scripts/check-architecture.sh` + fixtures (R4/R5)
Scope: All removals are `internal/`-only. No public API, no migration files, no change to `internal/app/new.go`, `cmd/*` wiring, or the outbox webhook handler.

---

## 1. Verification verdict (untrusted evidence → tree)

Every claim in the requirements spec (evidence) was re-verified against tree HEAD `5ad5343` on 2026-08-16. **The evidence is accurate in substance: all cited symbols, files, line anchors, and the headline counts (201 exported symbols, 46 rule-(f)-flagged) reproduce exactly with an independent enumeration.** Two gaps in the evidence's own tables were found (D1 material, D3 cosmetic) and are folded into the design.

| # | Claim (evidence) | Verdict | Tree evidence |
|---|---|---|---|
| E1 | `CanReadFile`/`CanReadMessage` in `internal/domain/access.go`; sole references are `access_test.go` (5 tests incl. `TestErrNotFound`/`TestErrDuplicate`) | **Confirmed** | `access.go:3,7`; `access_test.go` has exactly `TestCanReadFile_public/private`, `TestCanReadMessage`, `TestErrNotFound`, `TestErrDuplicate`. Zero refs outside `internal/domain` (grep over `internal cmd`). |
| E2 | Service duplicate `canReadFile`/`canReadMessage` at `service.go:329,334`; call sites `file.go:202`, `message.go:179,441,476`, `comment.go:58,70` | **Confirmed** | Definitions at `service.go:329/334`; all six call sites present verbatim; `service_test.go:37,51` test the service (unexported) versions, not the domain ones. |
| E3 | `PermContentModerate` zero refs; all other `Perm*` live | **Confirmed** (line drift) | Symbol at `admin.go:7` inside the `const (` block that opens at `:5`; zero refs. **D2:** evidence table says `admin.go:5` — the const-block line, not the symbol line. |
| E4 | `GenerateSigningKey`/`WebhookSubscription`/`WebhookDelivery` (`domain/webhook.go`) sole consumer is unwired `internal/repository/webhook.go` (312 lines) | **Confirmed** | `internal/repository/webhook.go` is exactly 312 lines and the only non-domain file naming the three symbols; zero `Webhook*` tokens in `internal/service/mocks/mocks.go`; `internal/app/outbox.go:58` calls `outbox.NewWebhookHandler` (different, live, self-contained type). |
| E5 | `ExportJob`/`ExportJobStatus`/`ValidStatusTransition`/`ExportJob*` consts zero refs (tests included) | **Confirmed** | Zero hits for any `ExportJob*` token outside `internal/domain/export.go`. |
| E6 | `VisibilityShared` does **not** exist (stale claim from the analysis) | **Confirmed** | `grep -rn VisibilityShared internal cmd` → empty; `shared.go` defines only `VisibilityPrivate`/`VisibilityPublic`. Nothing to delete. |
| E7 | `FileStatus*` (4) and `Category*` (6) consts zero external refs; types live via row casts; `ClassifyFile` live; `IsTerminal()` zero external callers | **Confirmed** | `file.go:13-16` (`FileStatus*`), `:30-35` (`Category*`); external refs only `db.go:49-50` and `httpx.go:58` (type casts) and `service/file.go:145`, `file_presign.go:101` (`ClassifyFile`). `IsTerminal` external callers: none. |
| E8 | idgen import-retention hack at `comment.go:80-81` (analysis's `:66` stale); `comment_test.go` uses `idgen.NewID()` at 17,26,27,35,45,47 | **Confirmed** | Comment at `comment.go:80`, `var _ = idgen.NewID` at `:81`; `comment.go:7` imports idgen solely for the hack (`shared.go:6` alias `ID = idgen.ID` keeps its own import); test lines match exactly. |
| E9 | `scripts/check-architecture.sh` header documents rules a-e (line 2), `selftest_rule` harness at 218-250, real-tree scan scope `internal cmd` with `! -name '*_test.go'` for (b)-(e), final echo at line 276 | **Confirmed** | Script is 276 lines; header line 2 `guard the live layered runtime (rules a-e)`; `selftest_rule()` at `:218-250`; rules (a)-(e1) swept + real-tree calls; final echo `:276` says `rules a-e`. Current run green. |
| E10 | `.golangci.yml` enables `unused` + `staticcheck`; U1000 does not flag the dead exports (lint-strict green) | **Confirmed** | `.golangci.yml` linters block enables both; `golangci-lint` behavior consistent with claim (U1000 = unexported identifiers only). |
| E11 | Headline counts: 201 exported top-level symbols; **46** flagged with zero qualified non-test refs; 21 of the 46 in the deletion list, 25 remaining | **Confirmed (reproduced)** | Independent python enumeration (type/var/func + const-block + single-line const over non-test `internal/domain/*.go`) → 201; qualified `domain\.<Sym>\b` scan over non-test files under `internal/` + `cmd/` excluding `internal/domain/` → 46. Symbol set matches the evidence §6 list exactly. R1 covers 2+1+4+6+1+7 = 21; R6 covers 1+1+3+3+1+2+2+1+10+1 = 25; 21+25 = 46. |
| E12 | `WebhookSubscription`/`WebhookDelivery` are **not** among the 46 (caught only by deleting the repository) | **Confirmed** | They have a qualified consumer (`internal/repository/webhook.go`); the 46-set excludes them. |
| E13 | `WithActor`/`ActorFrom`/`MustActor` zero refs; service re-implementation at `service.go:32,37`; middleware uses service versions | **Confirmed** | `domain.WithActor` etc. → zero hits; `service.go:32` `WithActor`, `:37` `ActorFrom` (private `actorKey`); `middleware/auth.go:46`, `ratelimit.go:103`, handlers use `service.WithActor`/`service.ActorFrom`. |
| E14 | `ErrNotFound`/`ErrDuplicate` (`errors.go`) and `ErrNestingTooDeep` (`comment.go:43`) zero external refs; consumers check only `err != nil` | **Confirmed** | `service/comment.go:104-106` (`if err := domain.ValidateNewComment(p); err != nil`); `comment_test.go` checks nil only. **D1:** `internal/domain/errors_test.go` (in-package) references `ErrNotFound`/`ErrDuplicate` in `TestErrNotFoundIsError`/`TestErrDuplicateIsError` — **missing from evidence §7**; must be dispositioned with the sentinels. |
| E15 | `SnapshotUnknown` (`aggregation.go:12`) value `"unknown"` persisted/validated; `MessageKindFile/Image` values 2/3 persisted and proto-mapped; `LegacyCutoverStatusShadow/RolledBack` machine values; `NotificationType` type zero refs with `NotifType*` live via `string()` conversions; `DatasetDescriptor` + 9 dead `Dataset*` consts; `DatasetAeroIMActivitySummary` live | **Confirmed** | `envelope.go:162-169` `snapshotStatus` validates via `domain.SnapshotStatus(value).Valid()`; `aggregation.go:16` `Valid()` accepts `SnapshotUnknown`; `aggregation.go:105` `Normalize()` sets it. `handler/grpc/validate.go:43-47` `messageKindFromProto` bounds-checks before cast; `repository/db.go:108` `domain.MessageKind(r.Kind)`. `service/notification.go:316,334,352,367-369,392,410,428` use `string(domain.NotifTypeX)`. `handler/http/aggregation.go:50` uses `domain.DatasetAeroIMActivitySummary`. `domain.NotificationType` → zero hits. |
| E16 | `LimitGrant` (`entitlement.go:12`) zero qualified non-test refs but structurally live: `EntitlementProjection.Limit()` (`:78`) returns it, `service/billing.go:100-101` consumes the returned grant; JSON shape via field tags | **Confirmed** | `entitlement.go:78` `func (projection *EntitlementProjection) Limit(...) (LimitGrant, bool)`; `service/billing.go` `grant, ok := projection.Limit(...)` then `grant.Unlimited`/`grant.Hard`. External textual refs exist only in tests (`client_test.go:21`, `tenant_billing_integration_test.go:92`, `billing_test.go:56,79,92,107,123,127`). |
| E17 | Registry functions are the public allow-list surface; all external callers read fields without naming `DatasetDescriptor` | **Confirmed** | Callers (`handler/http/aggregation.go:61`, `connector/http_summary_request.go:157`, `event/envelope.go:139`, `service/aggregation.go:178,189,203,272`, `sync_job.go:379`, `operation_runner.go:199,206`) use only field access (`descriptor.Name/Source/PII`), legal for unexported return types. |
| E18 | Affected-test table §7 (every row) | **Confirmed** | `service/message_test.go:59,70,101,105` `domain.MessageKindFile`; `legacy_cutover_test.go` `domain.LegacyCutoverStatusShadow/RolledBack` (also uses live `Legacy`/`CutOver`); connector tests `domain.DatasetSnaplinkIdentity/AeroIMProfile/AeroVaultTenants/Buckets/Usage`; `event/envelope_test.go:15,29,38`; `handler/http/event_ingest_test.go:110`, `handler/grpc/event_ingest_test.go:33`; `service/sync_projection_test.go:68-70,138`; integration-tagged `legacy_cutover_integration_test.go:48,73,110` and `tenant_billing_integration_test.go:92` (both `//go:build integration`). |
| E19 | Migrations untouched; `webhook_subscriptions`/`webhook_deliveries` tables remain | **Confirmed** | `migrations/000030_create_webhook_subscriptions.{up,down}.sql` exist; no migration depends on `internal/repository/webhook.go`. |
| E20 | Fixture doctrine and layout; rule-(f) positive sweep will also flag `type Fixture` in `positive/domain_imports_uuid` | **Confirmed** | `positive/domain_imports_uuid/internal/domain/fixture.go:9` `type Fixture` (exported, unreferenced) — adds 1 violation to the (f) positive sweep; `positive/domain_imports_gorm/internal/domain/fixture.go` declares no exported symbols; negative fixtures (`chain`, `hub_assertion`, `service_connector`) contain no `internal/domain` dirs. |

**Evidence deltas found:**

- **D1 (material — must be fixed in implementation):** evidence §7 omits `internal/domain/errors_test.go`, which references `ErrNotFound`/`ErrDuplicate` in-package (`TestErrNotFoundIsError`, `TestErrDuplicateIsError`). If the sentinels are deleted per R6 (recommended), `errors_test.go` must be deleted in the same step, or `make test` breaks. The design adds it to the affected-test matrix (§3.5).
- **D2 (cosmetic):** `PermContentModerate` is at `admin.go:7`; the `const (` block opens at `:5`. Evidence's `admin.go:5` anchors the block, not the symbol.
- **D3 (cosmetic):** evidence §6 numbers rows 1-48 for 46 flagged symbols (rows 22-23 are the non-flagged `WebhookSubscription`/`WebhookDelivery` pair, explicitly excluded from the 46). Row labels `38-47`/`48` are table artifacts; the arithmetic (21 in R1 + 25 in R6 = 46) is consistent.

---

## 2. Problem statement

`internal/domain` exports legacy symbols with zero production consumers, and no gate detects exported-symbol death:

1. **No static-analysis gate flags them.** `golangci-lint` U1000 covers only unexported identifiers, so `make lint-strict` is green with the dead exports present.
2. **`scripts/check-architecture.sh` rule (b)** checks only what `internal/domain` *imports*, never whether its exports are consumed.
3. **`scripts/check-composition-root.sh`** pins only `internal/app/new.go`/`cmd/*` seams — not repository wiring — so the unwired `internal/repository/webhook.go` (312 lines, sole consumer of `domain.WebhookSubscription`/`domain.WebhookDelivery`) passes every gate.
4. **Divergent duplicated ACL:** `domain.CanReadFile`/`CanReadMessage` (dead) vs. the service's unexported `canReadFile`/`canReadMessage` (live, six call sites). The dead pair is pure drift risk: two copies of the file/message read ACL that can disagree.

An independent enumeration reproduces the evidence's baseline exactly: **201 exported top-level symbols** in non-test `internal/domain/*.go`, of which **46** have zero qualified non-test references (`domain.<Symbol>` under `internal/` + `cmd/`, excluding `internal/domain/` and `_test.go`). The direction's deletion list covers 21 of the 46; the remaining 25 require disposition (delete / unexport / keep-with-consumer) for a green gate.

---

## 3. Design

### 3.1 API changes

**Public API: none.** Every removal and unexport is `internal/`-only; no proto, no HTTP/gRPC contract, no config key, no migration changes.

Internal-surface changes (R1-R3):

| Change | File | Detail |
|---|---|---|
| Delete | `internal/domain/access.go` + `internal/domain/access_test.go` | `CanReadFile`, `CanReadMessage`; tests die with them (incl. `TestErrNotFound`/`TestErrDuplicate` — R1-e). |
| Delete symbol | `internal/domain/admin.go:7` | `PermContentModerate` (const-block member at `:5-…`; delete just the line). |
| Delete file | `internal/domain/webhook.go` | `GenerateSigningKey`, `WebhookSubscription`, `WebhookDelivery` (48 lines). |
| Delete file | `internal/domain/export.go` | `ExportJob`, `ExportJobStatus`, `ValidStatusTransition`, `ExportJobPending/Processing/Completed/Failed` (48 lines). |
| Delete file | `internal/repository/webhook.go` | R2 — 312-line unwired repository; tables stay in migrations. |
| Remove exported consts | `internal/domain/file.go:13-16,30-35` | `FileStatus*` → private `fileStatusReady`/`fileStatusFailed` (only `IsTerminal()` needs values; keep `Pending/Processing` as literals or private consts); `Category*` → private `categoryImage…categoryOther` for `mimeCategoryMap`/`ClassifyFile`. `FileStatus`/`FileCategory` **types stay exported** (row casts at `db.go:49-50`, `httpx.go:58`); `ClassifyFile`, `mimeCategoryMap`, `IsTerminal` unchanged in behavior. |
| Remove import-retention hack | `internal/domain/comment.go:80-81` | Delete `var _ = idgen.NewID` + its comment; drop the `internal/pkg/idgen` import from `comment.go`. `shared.go` keeps its own idgen import (`ID` alias); `comment_test.go` imports idgen directly — unchanged. |

### 3.2 Baseline dispositions for the 46 flagged symbols (R6)

All behavior-preserving. `make test` + `make check-integration-compile` prove no caller changed semantics.

**Delete (zero references, tests included):**
- `AccountIdentity` + its `Valid()` method (`account.go:32,100`) — zero callers.
- `CommentStatusDeleted` (`shared.go:45`, value 2) — no writer/validator exists; `CommentStatus` row casts at `db.go:164` unaffected (nothing produces value 2).
- `internal/domain/actor.go` + `actor_test.go` — `WithActor`, `ActorFrom`, `MustActor`; handlers/middleware use `service.WithActor`/`service.ActorFrom` (verified E13).
- Error sentinels `ErrNotFound`, `ErrDuplicate` (`errors.go`), `ErrNestingTooDeep` (`comment.go:43`): delete; `ValidateNewComment` returns an inline `errors.New("comment nesting must not exceed two levels")` (or a private sentinel) and its doc comment drops the exported-name reference. **Delete `internal/domain/errors_test.go` in the same step (D1).** Consumers check only `err != nil` — no behavior change.

**Unexport (identical values; in-package uses preserved):**
- `SnapshotUnknown` → `snapshotUnknown` (`aggregation.go:12`); `Valid()` (`:16`) and `Normalize()` (`:105`) keep accepting `"unknown"`, so `envelope.go:162` validation and the persisted rows are unchanged.
- `MessageKindFile`/`MessageKindImage` → private consts (`shared.go:21-22`, values 2/3); `IsValid()` keeps them valid; `db.go:108` casts and `messageKindFromProto` (`validate.go:43-47`) bounds-checks are unchanged; in-package `shared_test.go:6` keeps compiling.
- `LegacyCutoverStatusShadow`/`LegacyCutoverStatusRolledBack` → private consts (`legacy_cutover.go:8,10`, values `"shadow"`/`"rolled_back"`); `LegacyCutoverStatus` type and `Legacy`/`CutOver` consts stay exported; `ValidCutoverTransition` unchanged.
- `NotificationType` → `notificationType` (`notification.go:8`); all 8 `NotifType*` consts stay exported and live via `string(domain.NotifTypeX)` at `service/notification.go:316-428`. (`Notification.Type` and `NotificationPreference.NotificationType` are plain `string` fields — unaffected.)
- `DatasetDescriptor` + 9 dead `Dataset*` consts → private (`aggregation.go:34-43,46`); `AccountDatasetRegistry()`/`DatasetDescriptorOf()` signatures become `[]datasetDescriptor` / `(datasetDescriptor, bool)` — legal for all verified callers (E17, field access only) and behavior-identical; `DatasetAeroIMActivitySummary` stays exported (live at `handler/http/aggregation.go:50`); `AccountDataset` type stays exported.

**Keep exported + add a genuine consumer:**
- `LimitGrant` (`entitlement.go:12`). Recommended disposition: extract the existing quota-exhaustion test in `service/billing.go:101` (`!grant.Unlimited && grant.Hard == 0`) into a typed helper in `service/billing.go`, e.g. `func grantExhausted(g domain.LimitGrant) bool`, and call it at the existing site. This is a **behavior-neutral refactor of live code** that names the type for the first time (a genuine consumer, not decoration), keeps the JSON shape (field tags) and the `Limit()` return contract, and avoids churning the 8 test-construction sites. **Not acceptable:** decorative `var _ = domain.LimitGrant{}` — rule (f) counts any qualified occurrence as a reference, so the gate must not be gamed; the AGENTS.md liveness doctrine forbids it. Alternative (behavior-adding, fail-closed): validate each received grant in `connector/billing/client.go` `CurrentEntitlement` (`:126-139`) — note that `envelope.Entitlement.Validate(tenantID)` already loops `projection.Limits` and calls `grant.Validate()`, so a second explicit loop would be redundant; the `grantExhausted` refactor is preferred.

### 3.3 Rule (f) — domain-liveness gate (R4)

Extend `scripts/check-architecture.sh` (the canonical rule definition per AGENTS.md) with a new rule.

**Semantics:** every exported top-level identifier declared in non-test files under `internal/domain/` — `type X`, `var X`, `func X` (methods are covered via their receiver type, never individually), every member of `const ( … )` blocks, and single-line `const X` declarations — must have ≥1 occurrence of `domain.<Symbol>` (word-boundary, qualified form) in a non-test `*.go` file under the scanned roots outside any `internal/domain/` directory. One violation per unreferenced symbol.

**Enumeration requirements (correctness-critical):**
- Const-block members must be extracted (the tree carries 60+ exported const-block members; a scan missing them is vacuous for enum constants). `internal/domain` has no `type (…)`/`var (…)` blocks today (verified), so the block walker handles `const ( … )`; it is written to accept `type (`/`var (` block keywords identically as cheap future-proofing.
- Matching is strictly qualified `domain.<Symbol>` so struct-field/label names (`NotificationType:` in `internal/service/preference.go`, `internal/repository/preference.go`, `internal/handler/grpc/preference.go`) never count (verified necessary — those files carry the exact label).
- `_test.go` files are excluded from **both** enumeration and the reference search, matching rules (b)-(e) doctrine.
- Type aliases (`type ID = idgen.ID`) are covered by the `type X` pattern.
- In-package references never count (the reference corpus excludes `internal/domain/` paths).

**Implementation shape** (bash + find + sed + awk; CI-safe, no Go toolchain, no network — matching the existing doctrine):

```bash
# ── rule (f): domain exported-symbol liveness ──────────────────────────────
# Enumerate exported top-level identifiers from non-test internal/domain/*
# files, then require a qualified domain.<Symbol> reference in the corpus.
scan_domain_liveness() {
  local root f sym corpus
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      # type/var/func single-line + const/type/var block members + single-line const
      awk '
        function clean(s) { sub(/\/\/.*$/, "", s); return s }
        /^(const|type|var)[[:space:]]*\(/ { inblock = 1; next }
        inblock && /^[[:space:]]*\)/ { inblock = 0; next }
        inblock {
          line = clean($0)
          if (match(line, /^[[:space:]]*[A-Z][A-Za-z0-9_]*/))
            print substr(line, RSTART, RLENGTH)
          next
        }
        {
          line = clean($0)
          if (match(line, /^(type|var|func)[[:space:]]+([A-Z][A-Za-z0-9_]*)/, m))
            print m[2]
          else if (match(line, /^const[[:space:]]+([A-Z][A-Za-z0-9_]*)[[:space:]]*=/, m))
            print m[1]
        }
      ' "$f" | sort -u
    done < <(find "$root" -path '*/internal/domain/*.go' ! -name '*_test.go' 2>/dev/null | sort)
  done | sort -u > "$TMP_EXPORTS"
  # corpus: non-test go files under the roots, excluding internal/domain paths
  for root in "$@"; do
    [ -d "$root" ] || continue
    find "$root" -name '*.go' ! -name '*_test.go' 2>/dev/null
  done | sort -u | grep -v '/internal/domain/' > "$TMP_CORPUS"
  while IFS= read -r sym; do
    if ! grep -qE "domain\.${sym}\b" $(cat "$TMP_CORPUS"); then
      echo "DOMAIN ORPHAN: exported $sym has no qualified non-test consumer"
      violations=$((violations + 1))
    fi
  done < "$TMP_EXPORTS"
}
```

(The snippet is the intended shape; the fixtures, not the doc, are the executable proof. Implementation notes: `grep -qE "domain\.${sym}\b"` guarantees the qualified form — `domain.File` cannot match `domain.FileStatus` because `\b` fails between `File` and `S`; `Trap{NotificationType: …}` cannot match `domain.NotificationType` because the label is preceded by `Trap{`, not `domain.`. Comments/string literals containing `domain.X` count as references — conservative and documented, see §5.)

**Wiring (all required):**
- Header line 2: `rules a-e` → `rules a-f`.
- Header rule list: add rule (f) documentation — purpose, scan scope (`internal/domain` non-test exports vs. qualified refs in non-test `*.go` under `internal/` + `cmd/` outside `internal/domain/`), test asymmetry (`_test.go` excluded from both sides), fixture doctrine.
- Fixture sweep: `selftest_rule "(f) domain liveness" scan_domain_liveness` after rule (e1).
- Real-tree: `scan_domain_liveness internal cmd`.
- Summary echo (line 276): `✓ Architecture check passed (rules a-f: …)` and the selftest labels `✓ self-test rule ((f) domain liveness): positive fires (N violation(s))` / `negative clean`.

### 3.4 Self-test fixtures (R5)

- **Positive** `scripts/testdata/architecture/positive/domain_orphan_export/internal/domain/orphan.go`:
  ```go
  package domain

  // Rule (f) positive fixture: exported symbols with no qualified non-test
  // consumer must be flagged. Imports nothing (rule (b) stays clean).
  func OrphanedExport() {}

  const (
      OrphanBlockConst = "orphan"
  )
  ```
  Both symbols are flagged (no file outside the fixture's `internal/domain` references them). The const-block member pins block extraction (a scan handling only type/var/func would still fire on `OrphanedExport`, halving but not zeroing the count — the block member makes the sweep robust). The pre-existing `type Fixture` in `positive/domain_imports_uuid` adds a third flagged symbol — acceptable (positive requires ≥1); do not modify that rule-(b) fixture.
- **Negative** `scripts/testdata/architecture/negative/domain_live/`:
  - `internal/domain/live.go` — `type Account struct{}`, `type SnapshotStatus string`, `type Trap struct{ NotificationType string }`, const-block member `SnapshotFresh SnapshotStatus = "fresh"`; imports nothing.
  - `internal/service/live.go` — references `domain.Account{}`, `domain.SnapshotStatus`, `domain.SnapshotFresh`, and constructs `domain.Trap{NotificationType: "x"}`. The `NotificationType:` label proves bare-word matches do not count.
  - Must stay clean under rules (a)-(f): no imports in `internal/domain` (rule (b)), no repository/handler/infra dirs (rules (c)-(e)).

### 3.5 Affected test files (R1/R6, incl. the D1 delta)

| File | Tag | Action |
|---|---|---|
| `internal/domain/access_test.go` | unit | delete (R1/R6) |
| `internal/domain/actor_test.go` | unit | delete (R6) |
| `internal/domain/errors_test.go` | unit | **delete (D1)** — tests the deleted sentinels |
| `internal/domain/shared_test.go` | unit | no change (unexport keeps in-package visibility) |
| `internal/domain/comment_test.go` | unit | no change (checks `err == nil` only) |
| `internal/service/message_test.go` | unit | `domain.MessageKindFile` (:59,70,101,105) → literal `domain.MessageKind(3)` or `3` |
| `internal/service/legacy_cutover_test.go` | unit | `Shadow`/`RolledBack` → `domain.LegacyCutoverStatus("shadow"/"rolled_back")`; `Legacy`/`CutOver` stay as-is |
| `internal/repository/legacy_cutover_integration_test.go` | **integration** | `LegacyCutoverStatusShadow` (:48,73,110) → literal — compiled by `check-integration-compile` |
| `internal/connector/http_summary_test.go`, `http_summary_skip_test.go`, `fault_injection_test.go` | unit | `DatasetSnaplinkIdentity`, `DatasetAeroIMProfile`, `DatasetAeroVaultTenants/Buckets/Usage` → `domain.AccountDataset("…")` literals |
| `internal/event/envelope_test.go` | unit | `DatasetAeroIMProfile` (:15,38), `DatasetAeroVaultUsage` (:29) → literals |
| `internal/handler/http/event_ingest_test.go` (:110), `internal/handler/grpc/event_ingest_test.go` (:33) | unit | `DatasetAeroIMProfile` → literal |
| `internal/service/sync_projection_test.go` | unit | `DatasetAeroVaultTenants/Buckets/Usage` (:68-70,138) → literals |
| `internal/connector/billing/client_test.go` (:21), `internal/service/billing_test.go` (:56,79,92,107,123,127) | unit | per R6 disposition — unchanged if `LimitGrant` stays exported (recommended) |
| `internal/repository/tenant_billing_integration_test.go` (:92) | **integration** | unchanged if `LimitGrant` stays exported; per disposition otherwise |

---

## 4. Compatibility constraints

- **No public API change.** All removals are `internal/`-only (A3/A4 greps prove no external reference). Live compatibility surfaces stay: `File`/`Message`/`Comment`/`Search` types and their live constants, `ClassifyFile`, `FileStatus`/`FileCategory`/`Visibility`/`MessageKind`/`CommentStatus`/`ReportStatus`/`SnapshotStatus` types and row casts (`db.go:49-50,108,164`, `httpx.go:58`), `AccountDatasetRegistry`/`DatasetDescriptorOf` allow-list functions, `LegacyCutoverStatus` type + `Legacy`/`CutOver` consts, `EntitlementProjection` contract, `NotifType*` consts, all live `Perm*`/`Audit*`/`Operation*`/`Step*`/`SyncJob*` consts.
- **Enum-value semantics on unexport.** `"unknown"`, 2/3, `"shadow"`/`"rolled_back"`, `"snaplink.identity"…"aero-vault.usage"`, and the `NotifType*` values are persisted in PostgreSQL rows, carried in proto/JSON mappings, or validated at boundaries (`envelope.go:162`, `AccountSnapshot.Normalize`, `IsValid()`, `messageKindFromProto`). Conversion to private consts/types uses **identical string/numeric literals**; `make test` + `check-integration-compile` + the A3/A4 greps prove no caller changed behavior.
- **`LimitGrant` contract:** JSON shape (field tags) and `EntitlementProjection.Limit() (LimitGrant, bool)` return contract are preserved; `service/billing.go` consumes the returned grant unchanged; the `grantExhausted` helper is behavior-neutral.
- **Repository deletion is inert:** no migration, `internal/app`, `cmd`, or mocks reference `internal/repository/webhook.go`; `webhook_subscriptions`/`webhook_deliveries` tables and `outbox.NewWebhookHandler` stay.
- **Rule (b) domain purity preserved:** all new fixtures' `internal/domain` files import nothing; the script grows only by one scan function + fixtures (no shell line-budget gate exists).
- **Qualified-matching precondition:** no non-test file aliases the domain import (verified — every import line is the bare path), so `domain.<Symbol>` matching is complete. The rule header documents this precondition.

---

## 5. Failure modes

| Failure mode | Detection / mitigation |
|---|---|
| **Gate vacuity** — rule (f) stops flagging (e.g., regex regression). | `selftest_rule` positive sweep prints `SELF-TEST FAIL: rule ((f) domain liveness) positive fixture matched nothing`; gate exits 1. `OrphanBlockConst` pins const-block extraction. |
| **Over-broad matching** — bare identifiers counted (struct-field labels). | Qualified `domain\.<Sym>\b` matching; pinned by the `Trap{NotificationType: "x"}` element of the negative fixture (a bare-word scan would flag it → negative SELF-TEST FAIL). |
| **Const-block extraction regression** — enum consts invisible to the scan. | Positive fixture's `OrphanBlockConst`; the 60+ real const-block members would otherwise silently escape the gate. |
| **False positives from comments/strings mentioning `domain.X`.** | Conservative by design: a comment reference keeps a dead symbol un-flagged (a gate false negative, not a build break). Documented in the rule header; fixtures are comment-free. |
| **Decorative references** (`var _ = domain.X`) gaming the gate. | Rule (f) counts any qualified occurrence as a reference, so this is a review doctrine, not grep-enforceable; R6 explicitly forbids it for `LimitGrant`. |
| **Aliased domain imports breaking qualified matching.** | None exist today (verified); the rule header names it a precondition; if introduced, the gate silently loses those symbols — caught by periodic `go doc` cross-checks in review, not by the gate. |
| **Compile breakage from unexported enum refs in tests.** | §3.5 matrix + `make test`; integration-tagged files are covered by `make check-integration-compile` (both `legacy_cutover_integration_test.go` and `tenant_billing_integration_test.go`). **Missing `errors_test.go` (D1) would fail `make test` if not deleted with the sentinels.** |
| **Enum-value drift on unexport** (wrong literal). | Private consts carry the identical literals; persisted/proto values verified by `make test` (e.g., `IsValid()`, envelope validation) and the A3/A4 greps. |
| **Positive sweep count drift** (e.g., future exported-but-unreferenced fixture symbols). | Self-test only requires ≥1 violation; the `domain_imports_uuid` `Fixture` + 2 orphan symbols give margin. |
| **Fixtures tripping other rules.** | New `internal/domain` fixture files import nothing (rule (b)); fixtures contain no repository/handler/infra dirs (rules (c)-(e)). Verified feasible against the existing fixture layout. |
| **Real-tree scan noise during the transition** — rule (f) lands before R6 dispositions. | Land rule (f) and the 25 dispositions in the same commit (ordered migration below); `make check-architecture` is only green at the end of the sequence. |

---

## 6. Migration steps (ordered)

Run each step with its gate; the tree is only green at step 7.

1. **R1 deletions** — delete `access.go`/`access_test.go`, `PermContentModerate` line, `webhook.go`, `export.go`; convert `file.go` `FileStatus*`/`Category*` to private consts (`IsTerminal()`, `mimeCategoryMap`, `ClassifyFile` updated in-file).
2. **R2** — delete `internal/repository/webhook.go` (312 lines).
3. **R3** — remove the idgen hack + import from `comment.go`.
4. **R6 dispositions** — delete `AccountIdentity`(+`Valid`), `CommentStatusDeleted`, `actor.go`/`actor_test.go`, error sentinels + **`errors_test.go` (D1)**; unexport `SnapshotUnknown`, `MessageKindFile/Image`, `LegacyCutoverStatusShadow/RolledBack`, `NotificationType`, `DatasetDescriptor` + 9 `Dataset*`; add `grantExhausted` to `service/billing.go` (or the alternative disposition) for `LimitGrant`.
5. **§3.5 test updates** — apply the matrix (literals/casts), incl. the two integration-tagged files.
6. **R4/R5** — add rule (f) to `scripts/check-architecture.sh` (header, scan function, selftest wiring, real-tree call, summary echo) + the two fixtures.
7. **Done gate** (from repo root):
   ```bash
   make test && make lint && make lint-strict && make check-file-sizes &&
   make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest &&
   make check-load-scenarios && make check-migration-set && make check-composition-root &&
   make check-proto-sync && make check-integration-compile && make build &&
   bash scripts/check-architecture.sh
   ```

---

## 7. Testable acceptance mapping

| Acceptance (evidence) | Testable probe |
|---|---|
| **A1 — rule (f) implemented and green** | `bash scripts/check-architecture.sh` exits 0 and prints `✓ self-test rule ((f) domain liveness): positive fires (N violation(s))` (N ≥ 1), `✓ self-test rule ((f) domain liveness): negative clean`, and `✓ Architecture check passed (rules a-f: …)`. Header comment (line 2 + rule list) documents rule (f) with scope and exclusions. Negative probe: temporarily remove the `domain.Account{}` reference from `negative/domain_live/internal/service/live.go` → rerun prints `SELF-TEST FAIL: rule ((f) domain liveness) negative fixture matched …` and exits 1. |
| **A2 — gate is not vacuous (positive fixture proof)** | `OrphanedExport` and `OrphanBlockConst` in `positive/domain_orphan_export` are flagged (positive sweep ≥1; expected count 3 with `domain_imports_uuid`'s `Fixture`). Delete either from the fixture → rerun prints `SELF-TEST FAIL: rule ((f) domain liveness) positive fixture matched nothing`. The const-block member specifically proves block extraction (a type/var/func-only scan would still fire on `OrphanedExport`, so this member pins the harder requirement). `negative/domain_live` stays clean: `domain.Account`, `domain.SnapshotStatus`, `domain.SnapshotFresh` referenced from `internal/service/live.go`, and the `NotificationType` field label does not count. |
| **A3 — dead symbols and unwired repository deleted; tests green** | Greps return nothing: `grep -rn "CanReadFile\|CanReadMessage\|PermContentModerate\|GenerateSigningKey\|WebhookSubscription\|WebhookDelivery\|ExportJob\|ValidStatusTransition\|ExportJobPending\|ExportJobProcessing\|ExportJobCompleted\|ExportJobFailed" --include='*.go' internal cmd`; `internal/domain/{access,export,webhook,actor}.go`, `internal/domain/{access,actor,errors}_test.go`, `internal/repository/webhook.go` absent; `grep -rn "var _ = idgen.NewID" internal/domain/` empty. `make test`, `make lint-strict`, `make build`, `make check-integration-compile`, `make check-file-sizes` all green (the D1 `errors_test.go` deletion is what makes `make test` green for the sentinel disposition). |
| **A4 — liveness doctrine preserved** | `internal/domain/file.go` still exports `File`, `FileStatus`, `FileCategory`, `Visibility` (type usage) and `ClassifyFile` (live at `service/file.go:145`, `file_presign.go:101`); casts at `repository/db.go:49-50` and `httpx.go:58` unchanged; comment/search/message types and all live constants untouched; `make check-file-sizes` green (no non-test Go file grows; the only new file is the shell-script change). |

---

## 8. Out of scope (preserved)

- Enum validation at the persistence boundary; new unit tests for untested domain invariants (analysis directions #2/#3).
- The `outbox` webhook handler (`internal/outbox/webhook.go`) and `webhook_subscriptions`/`webhook_deliveries` migrations.
- Any change to `internal/app/new.go`, `cmd/*` wiring, `internal/service/mocks/mocks.go`, or the composition-root pins.
- Public API / proto / config changes (all removals are `internal/`-only).
