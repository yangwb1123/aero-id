# Design — Quarantine `internal/{file,message,notification,audit}` with FM-12 total tripwires and regression-pin the landed check-architecture realignment (cmd/migrate)

Date: 2026-08-10 · Module: `cmd/migrate` (batch grouping label; the change set is the dead-tree quarantine + gate realignment per the selected direction)
Source analysis: `docs/auto/analyses/cmd-migrate-c1b3771d.json` (direction 1) · Requirements: `docs/auto/runs/quarantine-the-unwired-parallel-tree-internal-fi-93837fdb/artifacts/requirements-10762e10/requirements.md`
Status: **Every requirements claim re-verified against HEAD `1001002` (2026-08-10). All hold; two drift corrections and two sweep extensions recorded in §0.** Baseline done-gate fully green before this change (recorded §7 Step 0).

## 0. Evidence audit (the requirements doc is untrusted input — each claim re-verified)

| # | Claim | Verification result |
|---|---|---|
| C1 | Dead trees exist: 34 total `.go` files (20 non-test + 14 test): file 8+4, message 5+4, notification 6+5, audit 1+1 | ✅ exact (`ls`/`find` counts match; integration-tagged `repository_integration_test.go` present in `file` and `notification`) |
| C2 | Zero non-test importers repo-wide; the only external references are `scripts/testdata/architecture/positive/dead_tree_import/fixture.go:3` (gate fixture, imports `internal/file`) and the comment at `internal/domain/audit.go:18` | ✅ exact (repo-wide grep; no live code imports the four packages) |
| C3 | Three swallow sites: `internal/file/service_share.go:45,75`, `internal/message/service.go:289`; `internal/notification/dispatcher.go:82` is best-effort SSE `d.pusher.Publish` | ✅ exact (all three `_ = s.audit.Log(` sites confirmed; dispatcher is `Publish`, deliberately not an audit shape) |
| C4 | `check-audit-swallow.sh` exclusion comment documents the four dirs as the excluded blind spot at :33-41; `:51-52` dead-tree `internal/audit` type sentence | ✅ exact (lines 33–41; the "No `Log(` exclusion exists … dead-tree internal/audit type" sentence at 50–52) |
| C5 | FM-12 guards for block/comment/search at :86-127; Makefile tripwire roots at `Makefile:78`; comment root-membership self-test at :133-151 | ✅ (guards at 86–97 / 99–113 / 115–125; membership check is at **157–168**, minor line drift from the cited 133–151 — the `comment_positive` pattern self-test now sits at 143–156; non-material) |
| C6 | check-architecture realignment **already landed** in commit `1001002`; no `notification:block`/`ALLOWED_SHARED`/`FEATURES=` tokens; per-rule fixtures exist; real-tree scan over `internal/` + `cmd/` only | ✅ exact (commit `1001002` diff: AGENTS.md, HARNESS.md, check-architecture.sh, 12 fixture files; `grep -nE 'notification:block|ALLOWED_SHARED|FEATURES='` → exit 1; all six per-rule self-test lines print green on the baseline tree) |
| C7 | AGENTS.md exemption list at :270-275 still names `comment create/edit/report`; gotcha bullet at :334 | ✅ exact (`comment create/edit/report` present; gotcha bullet "`internal/{file,message,notification}` and `internal/audit` are an unwired parallel tree" at :334) |
| C8 | `auditMessageDelete` at `internal/service/message.go:369-376`, call site :356; manifest row at `scripts/check-audit-manifest.sh:42` | ✅ exact |
| C9 | Retained exemptions emit zero `Record`/`Log` sites in live `internal/service` (file.go, file_presign.go, file_share.go, preference.go, region_fencing.go, projection_replication.go, projection_replay.go, sync_projection.go all 0); file **share** audited in-tx via `buildLegacyAuditFact` (file_share.go:116,152) | ✅ exact |
| C10 | Design doc §6: compound target = repo-wide-zero; FM-12 M-only ruling ("guard is red while the package legitimately exists — deletion and guards are one atomic change") | ✅ exact |
| C11 | `domain.AuditLogger` used only by `internal/file/file.go` and `internal/message/message.go`; `AuditEvent`/constants are live | ✅ exact (`grep -rln 'domain\.AuditLogger'` → exactly those two files; `AuditEvent`/`Audit*` constants used by live `internal/service` and `internal/repository`) |
| R5.4 | No other living citations in Makefile / check-architecture.sh / check-audit-manifest.sh | ✅ exact (grep returns zero hits) |

**Drift corrections (vs. the requirements text):**
- The requirements header says "Verification date: 2026-08-11" — the system date is **2026-08-10** (HEAD `1001002`, 2026-08-10). Cosmetic; this design uses the real date.
- C5's membership self-test line citation (:133-151) is today :157-168 (pattern self-test moved); nothing else drifted.

**Sweep extensions (D-4 class, found by this design's independent sweep — add to REQ-5's change set):**
- **X-1** `scripts/check-architecture.sh:57-59` EX-3 justification reads "…for the `AuditEvent.ID` field type of **the quarantined-tree audit types**". After REQ-1.3, `AuditEvent` is live and the tree is gone — the justification becomes false. Doc-comment-only reword (§3.8). Rule logic untouched.
- **X-2** `AGENTS.md:267` "The dead-tree `AuditLogger` carve-out does not apply to the live `service.AuditLogger` adapter" references a type deleted by REQ-1.3. Reword to historical (§3.6c).
- **X-3** `scripts/check-audit-swallow.sh:62-67` usage header documents the Makefile gate roots without the four new roots; leaving it stale would contradict the extended Makefile line (the sibling block-quarantine design's DA-1 lesson). Extend (§3.2a).
- **X-4** `internal/domain/audit.go`'s fail-closed doc comment currently lives on the deleted interface; REQ-1.3 requires it to survive on the constants block (§3.5).

## 1. Problem statement (confirmed)

Four unwired packages (34 `.go` files, zero non-test importers) still exist after the 2026-08-08 block/comment/search quarantine. They are provably unreachable, yet they (1) keep the audit gate's only excluded blind spot alive — `check-audit-swallow.sh:33-41` documents them as unscanned, and they still hold three `_ = s.audit.Log(` swallow sites (C3); (2) keep AGENTS.md's "unwired parallel tree" gotcha and the stale `comment create/edit/report` exemption (C7); (3) leave a HARNESS.md feature-package rules section describing the deleted architecture (HARNESS.md:24-32, incl. `internal/audit/` GORM rule at :30); and (4) carry the last users of `domain.AuditLogger` (C11). The check-architecture realignment (rules a–e over the live layered runtime) has already landed (C6) — this change deletes the trees, makes the tripwires total (FM-12 must-not-exist guards + positive fixtures + Makefile roots), reconciles every living citation, and regression-pins the landed realignment. No live code changes; no public API changes.

## 2. Design decisions

| # | Decision | Rationale |
|---|---|---|
| D-1 | Delete the four trees with `git rm -r`, **no shims** (no forwarding packages, no empty dirs) | Zero importers (C2); a shim would itself trip the new guards. Precedent: 2026-08-08 quarantine |
| D-2 | One reusable guard function `assert_dead_tree_absent <label> <dir>` + **four literal invocation lines**; the existing block/comment/search inline guards stay **byte-identical** | REQ-2.1 mandates the shared function so REQ-3's fixture-mode self-test exercises identical logic. Literal per-dir invocations preserve per-dir tripwire greps (the sibling B3-1 design's C6 lesson: a parameterized loop alone makes invocation-pinning self-tests unsatisfiable) |
| D-3 | Guard self-tests: (i) fixture-mode totality proof (REQ-3.2/3.3), (ii) invocation pin (FM-3), (iii) root-membership tripwire for the four new roots (REQ-2.4), mirroring the `internal/comment` precedent (:157-168) | Totality must be empirically pinned, not asserted; a deleted guard invocation or Makefile root silently weakens the quarantine to the P1/P2 scan alone |
| D-4 | Fixtures at `scripts/testdata/audit-swallow/dead_tree_positive/internal/<pkg>/fixture.go` — benign reintroductions (compilable, **propagated** audit error, zero P1/P2 shapes) | `testdata` is ignored by go tooling (build-safe) and never a scan ROOT; `check-architecture` scans only `internal/` + `cmd/`. `dead_tree_negative` is an intentionally absent path (no files) |
| D-5 | Extend `Makefile:78` with `internal/file internal/message internal/notification internal/audit` | Absent-path roots scan to 0 matches (N-4 safe by construction; the scan loop already suppresses "No such file" via `2>/dev/null`); dropping a root becomes a gate violation (REQ-2.3/2.4) |
| D-6 | REQ-1.3: delete `domain.AuditLogger` **and** its QUARANTINED note; move the fail-closed doc to the constants block; explicitly mark `AuditEvent` live in a comment | Zero users after deletion (C11). The note enumerates deleted files and would be a false citation (D-4). The "AuditEvent is live" marker prevents a future wrongful prune (the sibling design's C1/FM-11 correction) |
| D-7 | Citation sweep per REQ-5 **plus** X-1…X-4: check-audit-swallow exclusion comment + dead-type sentence + usage header, AGENTS.md gotcha + carve-out sentence, HARNESS.md feature rules, check-architecture EX-3 justification, domain/audit.go | D-4 ruling: living, undated files whose claims become false must be updated in the same commit; dated records (`docs/designs/`, `docs/auto/**`, analysis JSONs) stay as historical evidence |
| D-8 | `check-architecture.sh`: **zero logic changes** (REQ-4 — the realignment landed in `1001002`); regression-pin via acceptance only (X-1 doc reword is the sole byte change) | The header is the canonical rule definition; rule (a)'s own-tree exclusion is already documented as "inert once the dead trees are deleted" — verified inert by code reading |
| D-9 | Wire both gates into `.github/workflows/ci.yml` as one pure-bash `check-gates` job (`make check-audit-swallow` + `make check-architecture`), mirroring the sibling B3-1 design's D8 | Today **no workflow invokes either gate** (verified: zero hits in ci.yml/docker.yml/release.yml). A tripwire enforced only by the local done-gate does not turn a PR red on merge. The Makefile invocation is load-bearing: CI runs the exact done-gate surface including the four quarantine roots. If the sibling campaign lands the identical job first, this step becomes a presence-verification no-op |
| D-10 | One atomic commit: deletion + guards + fixtures + roots + citations + CI + AGENTS.md together | FM-12 M-only ruling (C10): a must-not-exist guard is red while the tree exists; guards without deletion = permanently red, deletion without guards = quarantine gap. Rollback = `git revert` of the single commit |

## 3. Concrete change set

### 3.1 `git rm -r internal/file internal/message internal/notification internal/audit` (34 files)

Post-condition: `grep -rl 'aero-id/internal/\(file\|message\|notification\|audit\)' --include='*.go' .` matches only `scripts/testdata/architecture/positive/dead_tree_import/fixture.go` (intentional gate fixture). `go list ./internal/file/...` → "no Go files".

### 3.2 `scripts/check-audit-swallow.sh`

**(a) Usage header (:62-67)** — extend the documented Makefile gate roots:

```bash
# Usage: scripts/check-audit-swallow.sh [ROOT...]   (default: internal/service)
# Multiple roots are scanned; the Makefile gate scans the whole live layer:
#   internal/service internal/repository internal/app internal/storage
#   internal/block internal/comment cmd internal/file internal/message
#   internal/notification internal/audit
# (internal/block, internal/comment, internal/search and
# internal/{file,message,notification,audit} are absent quarantine tripwires —
# the directories were deleted; the FM-12 guards below are the total
# tripwires.) Never add internal/sso without excluding its circuit-breaker
# calls: sso's bare `c.breaker.Record(...)` is a breaker observation, not an
# audit write, and would be a false positive for P2.
```

**(b) Exclusion block (:33-41)** — the four dirs are no longer an excluded surface; they are guarded absent-path tripwire roots:

```bash
# Exclusions (documented):
#   - only the live layer is scanned (default root internal/service; the
#     gate adds internal/repository, internal/app, internal/storage,
#     internal/block, internal/comment, cmd and the four absent quarantine
#     tripwire roots internal/{file,message,notification,audit}).
#     internal/block, internal/comment and internal/search were deleted in
#     the 2026-08-08 dead-tree quarantine; internal/{file,message,
#     notification,audit} in the 2026-08-10 quarantine. All seven are
#     absent-path tripwire roots under the FM-12 guards below, so even a
#     benign reintroduction (compilable package, propagated audit error)
#     turns the gate red.
```

**(c) Dead-type sentence (:50-52)** — the referenced type is deleted by REQ-1.3; reword to history:

```bash
# No `Log(` exclusion exists: Log is a first-class gate citizen (P1 + P2).
# AGENTS.md's historical "AuditLogger ... legacy diagnostics only" carve-out
# referred to the dead-tree internal/audit type, deleted in the 2026-08-10
# quarantine along with domain.AuditLogger — it never applied to the live
# service.AuditLogger adapter. Likewise the legacy middleware fail-open ...
```

**(d) FM-12 guards** — insert after the search guard (:125), before the positive-fixture self-test (:127). The function body is the single source of the guard semantics; `set -u` only (no `set -e`) means a non-zero return in the main path is expected and sets `fail=1` without aborting:

```bash
# ── quarantine guards (FM-12): internal/{file,message,notification,audit} ──
# must never exist. The four unwired trees were deleted in the 2026-08-10
# dead-tree quarantine; they carried the last excluded swallow sites (three
# `_ = s.audit.Log(` at internal/file/service_share.go:45,75 and
# internal/message/service.go:289 — historical). The guard is total — even a
# benign reintroduction (compilable package, propagated audit error, no
# swallow shape) is a gate violation. Deliberately lifting the quarantine =
# deleting this guard block AND its fixture-mode/invocation self-tests AND
# the Makefile tripwire roots AND the audit.go note AND the exclusion
# comment + AGENTS.md, in one commit.
assert_dead_tree_absent() {
  local label="$1" dir="$2"
  if [ -d "$dir" ]; then
    echo "QUARANTINE VIOLATION: $label must not exist (dead-tree quarantine, FM-12)"
    fail=1
    return 1
  fi
  echo "✓ quarantine guard: $label absent (FM-12)"
  return 0
}

assert_dead_tree_absent "internal/file" "$SELF_DIR/../internal/file"
assert_dead_tree_absent "internal/message" "$SELF_DIR/../internal/message"
assert_dead_tree_absent "internal/notification" "$SELF_DIR/../internal/notification"
assert_dead_tree_absent "internal/audit" "$SELF_DIR/../internal/audit"
```

**(e) Fixture-mode self-test (REQ-3) + invocation pin (FM-3)** — insert after the comment root-membership check (:168), before the negative-fixture self-test. Runs on every invocation (default and explicit-root):

```bash
# ── self-test (REQ-3): fixture-mode totality proof — each dead_tree_positive
# ── fixture is a benign reintroduction (compilable package, PROPAGATED audit
# ── error — deliberately not a P1/P2 swallow shape) whose mere existence
# ── must redden the FM-12 guard. A missing fixture dir fails the gate: the
# ── fixtures are part of the gate, not decoration. Runs on every invocation.
for pkg in file message notification audit; do
  if ( assert_dead_tree_absent "internal/$pkg" "$FIXTURES/dead_tree_positive/internal/$pkg" >/dev/null ); then
    echo "SELF-TEST FAIL: dead_tree_positive fixture for internal/$pkg missing or did not trip the FM-12 guard"
    fail=1
  else
    echo "✓ fixture-mode self-test: internal/$pkg reintroduction reddens the FM-12 guard"
  fi
done
if ( assert_dead_tree_absent "dead_tree_negative" "$FIXTURES/dead_tree_negative" >/dev/null ); then
  echo "✓ fixture-mode self-test: absent path passes the FM-12 guard"
else
  echo "SELF-TEST FAIL: dead_tree_negative must not exist and must pass the FM-12 guard"
  fail=1
fi

# ── self-test: each new guard invocation must stay in the script (deleting
# ── one silently weakens the quarantine to the P1/P2 scan alone).
for pkg in file message notification audit; do
  if ! grep -q "assert_dead_tree_absent \"internal/$pkg\" \"\$SELF_DIR/../internal/$pkg\"" "$0"; then
    echo "SELF-TEST FAIL: FM-12 guard invocation for internal/$pkg missing from $0"
    fail=1
  fi
done
```

**(f) Root-membership tripwire (REQ-2.4)** — insert beside (e), same `$# -gt 0` semantics as the `internal/comment` membership check (no-arg default stays exempt):

```bash
# ── self-test (REQ-2.4): the four quarantine roots must stay in the ROOTS
# ── list (root-removal tripwire: dropping one from the Makefile gate would
# ── silently shrink the scan back toward the historical blind spot). Same
# ── semantics as the internal/comment membership assertion above.
if [ "$#" -gt 0 ]; then
  for pkg in file message notification audit; do
    root_has=false
    for root in "${ROOTS[@]}"; do
      if [ "$root" = "internal/$pkg" ]; then
        root_has=true
      fi
    done
    if [ "$root_has" = false ]; then
      echo "SELF-TEST FAIL: internal/$pkg missing from scan ROOTS (tripwire root removed)"
      fail=1
    fi
  done
fi
```

### 3.3 Fixtures — `scripts/testdata/audit-swallow/dead_tree_positive/internal/<pkg>/fixture.go` (4 files)

Same content per package, only the `package` clause and the comment subject change (`file` / `message` / `notification` / `audit`). The fixture is deliberately swallow-free: no line starts with `_ = ` or a bare `ident.Log(`/`ident.Record(` (verified against P1/P2, which are statement-start-anchored):

```go
// Package file is a deliberate, benign reintroduction of a quarantined dead
// tree, used by check-audit-swallow.sh's fixture-mode self-test to prove the
// FM-12 guard's totality: this package compiles and its audit call PROPAGATES
// its error (the fail-closed form the live layer requires — deliberately NOT
// a P1/P2 swallow shape), yet its mere existence must turn the gate red.
package file

// Writer is the minimal propagated-error audit surface.
type Writer interface {
	Log(e *Event) error
}

// Event is a minimal audit event shape.
type Event struct {
	Actor string
	Kind  string
}

// Audit writes one event, propagating the error (fail-closed).
func Audit(w Writer, e *Event) error {
	if err := w.Log(e); err != nil {
		return err
	}
	return nil
}
```

`scripts/testdata/audit-swallow/dead_tree_negative/` must **not** exist (the self-test asserts the guard passes against it).

### 3.4 `Makefile:78`

```make
check-audit-swallow:
	@bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd internal/file internal/message internal/notification internal/audit
```

### 3.5 `internal/domain/audit.go`

Delete the `AuditLogger` interface and its QUARANTINED-DEAD-TREE note (:4-19); move the fail-closed statement onto the constants block; mark `AuditEvent` live. `AuditEvent` and all ten `Audit*` constants are unchanged (live layer: `internal/service/message.go` `auditMessageDelete`, `internal/service/file_share.go` `buildLegacyAuditFact` grant/revoke, `internal/service/comment.go` `ResolveReportWithAudit`, `internal/service/block.go`, `internal/service/admin.go`, `internal/repository/audit.go`, `internal/repository/db.go`).

```go
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Audit action constants for sensitive operations. A failed audit write is a
// failed durable audit write: callers must propagate the error (fail-closed).
// The earlier "a failed log must not fail the primary operation" fail-open
// wording is revoked — it predates the live-layer fail-closed conversion and
// must never be reinstated. The dead-tree AuditLogger interface (which
// carried the last excluded `_ = s.audit.Log(` swallow sites in the unwired
// internal/{file,message,notification,audit} tree) was deleted in the
// 2026-08-10 quarantine; the live layer uses service.AuditLogger, policed by
// make check-audit-swallow.
const (
	AuditFileShareGrant  = "file.share.grant"
	AuditFileShareRevoke = "file.share.revoke"
	AuditReportResolve   = "comment.report.resolve"
	AuditUserBlock       = "user.block"
	AuditUserUnblock     = "user.unblock"
	AuditMessageDelete   = "message.delete"
	AuditAdminGrantRole  = "admin.role.grant"
	AuditAdminRevokeRole = "admin.role.revoke"
	AuditUserBan         = "user.ban"
	AuditUserUnban       = "user.unban"
)

// AuditEvent records who did what to which resource and when. Live: consumed
// by the repository/ledger and admin read paths — do not prune.
type AuditEvent struct {
	ID          uuid.UUID
	ActorUID    string
	Action      string
	SubjectType string
	SubjectID   string
	TargetUID   string
	Meta        map[string]any
	CreatedAt   time.Time
}
```

### 3.6 `AGENTS.md`

**(a) Gotcha bullet (:334)** — replace the "unwired parallel tree" sentence with the quarantine statement, mirroring the block/comment/search sentence in the same bullet:

```markdown
- The dead trees `internal/{file,message,notification,audit}` were quarantined (deleted) in the
  2026-08-10 quarantine with `check-audit-swallow` FM-12 must-not-exist guards and Makefile tripwire
  roots, like `internal/block`, `internal/comment`, and `internal/search` before them (2026-08-08 —
  `internal/comment` carried the last swallowed fail-open out-of-transaction audit write;
  `internal/block`'s `BlockChecker` role is served by the live `service.BlockChecker`).
  The running application uses `internal/service` + `internal/repository`. Do not implement target
  account/audit work in an unwired tree.
```

**(b) Exemption list (:270-275, REQ-6)** — remove `comment create/edit/report`; keep the five retained exemptions with per-op evidence:

```markdown
- The audit-coverage invariant covers every operation that emits an audit fact, not every mutation.
  These legacy compatibility surfaces currently emit zero audit facts (before and after the
  fail-closed conversion) and are exempt only until migration — verified per op: file
  create/delete/update (`internal/service/file.go`, `file_presign.go`, `file_share.go` — zero
  `Record`/`Log` sites; file *share* is audited in-tx via `buildLegacyAuditFact` and is not exempt),
  message send/edit/react (`internal/service/message.go` — audit sites exist only on the delete
  path, `auditMessageDelete`), preferences (`internal/service/preference.go` — zero sites), region
  promotion (`internal/service/region_fencing.go` — zero sites), projection replication
  (`internal/service/projection_replication.go`, `projection_replay.go`, `sync_projection.go` —
  zero sites). The comment tree was quarantined on 2026-08-08 and live comment report is audited
  in-tx (`ResolveReportWithAudit`, `internal/service/comment.go`), so comment ops are no longer
  exempt. `make check-audit-manifest` enumerates every bare site; `make check-audit-swallow` bans
  swallowed/bare writes.
```

**(c) Carve-out sentence (:267, X-2)** — the referenced type no longer exists:

```markdown
- Live-layer audit writes (`Record` and `Log`) are durable; swallowing either is a gate violation
  (`make check-audit-swallow`). The dead-tree `AuditLogger` carve-out is historical (the interface
  was deleted in the 2026-08-10 quarantine) and never applied to the live `service.AuditLogger`
  adapter; the legacy fail-open carve-out for Ban and other middleware
  must never be re-applied to audit writes: the fail-closed `audit.ingest_failed`/503 contract
  stands for every live-layer audit site.
```

### 3.7 `HARNESS.md` (:15-33, REQ-5.3)

Replace the feature-package architecture section (feature list `file, message, comment, block, notification, search` and the `internal/audit/` GORM rule at :30) with the live-layer description:

````markdown
## Architecture — strict one-way dependency

```
handler  →  service  →  repository  →  Postgres (GORM)
                              ├→  storage (local | s3)
                              └→  sso (gRPC client + caches)
```

Rules (canonical definitions live in `scripts/check-architecture.sh`):
- `internal/domain/` must import nothing from `internal/` (only stdlib + `internal/pkg/*`; documented `google/uuid` exception)
- `internal/repository/` must not import `internal/service`
- `internal/{connector,event,outbox,region,sso}/` must not import service or repository (`internal/hub` is the documented EX-1 exception)
- `internal/handler/` may import `internal/repository` only for the two documented enum casts (EX-2)
- The dead trees `internal/{file,message,notification,audit,block,comment,search}` are quarantined: deletion + `check-audit-swallow` FM-12 must-not-exist guards + Makefile tripwire roots
- `internal/app/` is the composition root; nothing imports it.
- `internal/pkg/` packages must not import from feature packages.
````

### 3.8 `scripts/check-architecture.sh` (X-1 — doc-comment only; no rule logic)

Reword the EX-3 justification (:57-59): "…for the `AuditEvent.ID` field type of the quarantined-tree audit types" → "…for the live `AuditEvent.ID` field type (the dead-tree `AuditLogger` interface was deleted in the 2026-08-10 quarantine; `AuditEvent` itself is live)". The exact-string exception and the TODO stay.

### 3.9 `.github/workflows/ci.yml` (D-9)

Append one job (existing jobs byte-identical; no `setup-go`, no network, no Docker — pure bash + grep, sub-second):

```yaml
  # Quarantine + live-layer gate tripwires (B3): the FM-12 must-not-exist
  # guards, fixture-mode self-tests, and Makefile tripwire roots are enforced
  # on every PR/push. The Makefile invocation is load-bearing: it carries the
  # four quarantine roots and the membership tripwires. No paths filter: a
  # filter would recreate the blind spot this gate closes. Pure bash + grep —
  # no Go toolchain, no network, no Docker.
  check-gates:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Audit-swallow gate (incl. quarantine tripwires)
        run: make check-audit-swallow
      - name: Architecture gate (live-layer boundaries)
        run: make check-architecture
```

## 4. API changes

| Surface | Change |
|---|---|
| Public HTTP / gRPC / protobuf / OpenAPI | **None** — no handler, service, repository, or composition-root byte changes; no `.proto` edits |
| Config (`configs/config.yaml`, `AERO_ID_*`), migrations, storage, outbox, hub, sso, event, connector | **None** |
| `domain.AuditLogger` (Go interface, `internal/` package) | **Removed** — zero users after the deletion (C11); no compile impact |
| `domain.AuditEvent`, `Audit*` action constants | **Unchanged** — live (repository/ledger, admin, message delete, file share, comment report, block, ban) |
| `scripts/check-audit-swallow.sh` | CLI shape unchanged (`[ROOT...]`, no-arg default = `internal/service`); accepts the four new tripwire roots (already generic); output gains 4 `✓ quarantine guard` lines, 5 fixture-mode self-test lines, and the invocation-pin self-tests |
| `scripts/check-architecture.sh` | **No behavioral change** — one doc-comment reword (X-1) |
| `Makefile` | `check-audit-swallow` target invocation extended with four roots (line 78) |
| `.github/workflows/ci.yml` | Adds job `check-gates` (D-9); existing jobs byte-identical |
| Deleted packages | `internal/{file,message,notification,audit}` (34 files incl. 2 integration-tagged tests) — no consumers |

## 5. Compatibility constraints

1. **Existing guards byte-identical**: the block/comment/search FM-12 guard blocks (:86-125) are untouched — their comment texts and behavior are pinned by prior acceptance; the new function is additive.
2. **P1/P2 patterns, T-5 assertions, and existing self-tests** (positive/negative/cmd_positive/comment_positive) unchanged; baseline stays 0 P1/P2 matches.
3. **`set -u` only, never `set -e`** in check-audit-swallow.sh: the guard function returns 1 on violation in the main path; under `set -u` that is expected (sets `fail=1`), under `set -e` it would abort — red either way, but the fixture-mode subshell usage requires the `if ( … )` condition form. Do not add `|| true` to the main invocations.
4. **bash ≥ 3.2** compatibility: function + `local` + arrays + subshell — all long-standing script idioms; the `${arr[@]}` usage in the membership check mirrors the existing comment check.
5. **Root-membership semantics match the comment precedent**: tripwire active for explicit-root invocations (`$# -gt 0`); the no-arg default (`internal/service` only) stays exempt — the Makefile gate always passes explicit roots.
6. **Fixtures are gate parts but never scanned**: `scripts/testdata/` is ignored by go tooling (build-safe) and outside every ROOTS loop and outside `internal/`+`cmd/` (check-architecture rule (a) never sees them); they must contain zero P1/P2 shapes (statement-anchored patterns verified against the §3.3 content).
7. **Absent roots are safe**: the four new Makefile roots do not exist on the green tree; the scan loop's `2>/dev/null` makes absent paths N-4-safe (block/comment precedent).
8. **check-audit-manifest.sh unchanged**: the `auditMessageDelete|standalone-fact|-` row at :42 stays; no live audit site moves.
9. **EX-3 stays**: `AuditEvent.ID` remains `uuid.UUID`, so the exact-string `github.com/google/uuid` exception remains load-bearing; only its justification text changes.
10. **Deletion cannot break `go build`/`go vet`/`check-integration-compile`**: zero importers (C2) and zero live refs to the two removed integration-tagged tests — verified by construction and by the baseline gate.
11. **No `make test-integration` requirement**: no SQL, migrations, storage, connectors, event ingestion, or callback-delivery changes (live surfaces untouched).
12. **Historical records untouched**: `docs/designs/**`, `docs/auto/**`, analysis JSONs, and the campaign YAML keep their pre-change citations as evidence.

## 6. Failure modes and tripwire matrix

| # | Failure | Detection | Consequence |
|---|---|---|---|
| FM-1 | Reintroduce `internal/<pkg>` benignly (compilable, propagated audit errors, no swallow shape — the FM-12 state that defeats import bans and P1/P2 scans) | Dir guard `[ -d ]` → `QUARANTINE VIOLATION` | Red (exit 1), locally and in CI (D-9) |
| FM-2 | Reintroduce with a P1/P2 swallow shape | P1/P2 scan (roots now include the path) **and** dir guard | Red (two independent signals) |
| FM-3 | Delete one guard invocation (weakens quarantine to the P1/P2 scan alone) | Invocation pin greps `$0` for the exact literal invocation | Red: `SELF-TEST FAIL: FM-12 guard invocation … missing` |
| FM-4 | Delete a fixture dir | Fixture-mode self-test: guard passes against the (now absent) fixture path | Red: `SELF-TEST FAIL: dead_tree_positive fixture … missing` |
| FM-5 | Remove one of the four roots from `Makefile:78` | Root-membership tripwire (explicit-root invocation); CI job runs the same Makefile line | Red: `SELF-TEST FAIL: internal/<pkg> missing from scan ROOTS` |
| FM-6 | Reword the guard's violation/✓ messages | Not self-pinned (message text is informational; `fail=1` + `return 1` carry the semantics); REQ-2 acceptance greps the ✓ lines at done-gate | Documented residual — same class as the sibling's A1; rewording cannot weaken the gate |
| FM-7 | Delete the membership tripwire / invocation pins themselves | Not self-pinned | Documented residual — identical gap class as the `internal/comment` membership tripwire (accepted precedent) |
| FM-8 | check-architecture stale tokens resurrected (`FEATURES=`/`notification:block`/`ALLOWED_SHARED`) | One-time acceptance grep (REQ-4), not a standing check; standing backstop = rule (a) import ban + the new dir guards | Documented residual |
| FM-9 | `domain.AuditLogger` wrongly kept or reintroduced | `grep -rn 'domain\.AuditLogger'` acceptance → 0; `go vet` | Red at done-gate |
| FM-10 | `domain.AuditEvent` wrongly pruned later ("the interface was dead, so the struct must be too") | Rewritten audit.go comment marks it live; `go build` fails (dozens of live refs) | Build-red by construction |
| FM-11 | Deletion breaks build/vet/integration-compile | Zero importers (C2); full done-gate runs post-change | Impossible by construction; gate verifies |
| FM-12 | Guard function returns 1 in the main path (under `set -u`) | Expected: script has no `set -e`; `fail=1` propagates in the current shell | Red as designed; no abort; fixture-mode uses subshell `if` form |
| FM-13 | `check-gates` job removed from ci.yml (D-9) | Workflow files are not self-pinned | Documented residual (FM-7 class); workflow diffs are human-reviewed |
| FM-14 | Non-atomic landing: guards before deletion | Guards are red on the pre-deletion tree by design (C10 M-only ruling) | Forces the one-commit sequence (intended) |
| FM-15 | Deletion without guards | Quarantine gap returns (FM-1 unguarded) | Prevented by D-10 + review; CI job would not exist yet |

## 7. Migration steps (one atomic commit)

**Step 0 — baseline record (executed 2026-08-10, pre-change, all green):** `make test`, `make lint`, `make lint-strict`, `make check-file-sizes`, `make check-audit-swallow` (explicit Makefile roots), `make check-anchored-event-ids`, `make check-audit-manifest`, `make check-composition-root`, `make check-proto-sync`, `make check-integration-compile`, `make build`, plus `bash scripts/check-architecture.sh` — all exit 0.

**Step 1 — deletion:** `git rm -r internal/file internal/message internal/notification internal/audit`.

**Step 2 — `scripts/check-audit-swallow.sh`:** apply §3.2 (a)–(f). Keep the block/comment/search blocks byte-identical.

**Step 3 — fixtures:** write the four `dead_tree_positive/internal/<pkg>/fixture.go` files (§3.3); `dead_tree_negative` stays absent.

**Step 4 — `Makefile:78`:** append the four roots (§3.4).

**Step 5 — `internal/domain/audit.go`:** apply §3.5 (interface + note deleted; fail-closed doc moved).

**Step 6 — `AGENTS.md`:** apply §3.6 (a) gotcha bullet, (b) exemption list, (c) carve-out sentence.

**Step 7 — `HARNESS.md`:** apply §3.7.

**Step 8 — `scripts/check-architecture.sh`:** apply §3.8 (EX-3 doc reword only).

**Step 9 — `.github/workflows/ci.yml`:** append the `check-gates` job (§3.9); if the sibling campaign already landed it, verify presence and skip.

**Step 10 — verification in a throwaway worktree (never in the working tree):** full done-gate (§8 REQ-7) + every mutation probe (§8 REQ-2/REQ-3/REQ-4) + `bash scripts/check-audit-swallow.sh` with no args + explicit Makefile roots. Red-proof probes run in the scratch copy and are restored afterward.

**Step 11 — one commit** narrating the quarantine + tripwire landing (block/comment precedent), containing exactly the files from Steps 1–9.

**Rollback:** `git revert` of the single commit is total — deletion, guards, fixtures, roots, citations, and the CI job revert together; no half-quarantined state is reachable.

## 8. Testable acceptance mapping

| Requirement | Testable acceptance (exact commands) |
|---|---|
| REQ-1 deletion | `test ! -d internal/file && test ! -d internal/message && test ! -d internal/notification && test ! -d internal/audit`; `grep -rn 'internal/\(file\|message\|notification\|audit\)\(/\|"\)' --include='*.go' internal cmd` → no output; `grep -rn 'domain\.AuditLogger' --include='*.go' .` → no output; `go build ./...` and `go vet ./...` exit 0; `go list ./internal/{file,message,notification,audit}/...` → no packages |
| REQ-2 guards + roots | `bash scripts/check-audit-swallow.sh internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd internal/file internal/message internal/notification internal/audit` exits 0 and prints exactly four `✓ quarantine guard: internal/<pkg> absent (FM-12)` lines. **Mutation probe** (throwaway worktree, restore after): `mkdir -p internal/file && printf 'package file\n' > internal/file/x.go` → exit 1 + `QUARANTINE VIOLATION: internal/file must not exist (dead-tree quarantine, FM-12)` (no swallow shape present); `rm -rf internal/file` → exit 0. `grep -n 'internal/file' Makefile` shows all four roots in the `check-audit-swallow` target |
| REQ-3 fixtures + self-tests | `bash scripts/check-audit-swallow.sh` (no args) and the Makefile target both exit 0 and print the four `✓ fixture-mode self-test: internal/<pkg> reintroduction reddens the FM-12 guard` lines + the absent-path PASS line. **Mutation probes**: delete one fixture dir → exit 1 + `SELF-TEST FAIL: dead_tree_positive fixture …`; delete one guard invocation line → exit 1 + `SELF-TEST FAIL: FM-12 guard invocation …`; `grep -rnE '^[[:space:]]*(_ = |_, _ = )|^[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*\.(Record|Log|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead|MarkDelivery)\(' scripts/testdata/audit-swallow/dead_tree_positive` → no output |
| REQ-4 check-architecture regression pin | `make check-architecture` exits 0 and prints `✓ Architecture check passed (rules a-e: …)` plus all six `✓ self-test rule (…)` lines; `grep -nE 'notification:block|ALLOWED_SHARED|FEATURES=' scripts/check-architecture.sh` → no output; the `dead_tree_import` positive fixture still fires rule (a) in fixture mode (no `SELF-TEST FAIL: rule (a) positive fixture matched nothing`); `git diff scripts/check-architecture.sh` shows only the EX-3 comment reword |
| REQ-5 citation sweep | `grep -rn 'unwired parallel tree internal/{file,message,notification,audit}' scripts/check-audit-swallow.sh` → no output; `grep -n 'internal/audit' HARNESS.md` → no output; `grep -n 'unwired parallel tree' AGENTS.md` → no output; `grep -n 'internal/file/service_share.go\|internal/message/service.go:289' internal/domain/audit.go` → no output; `grep -n 'quarantined-tree audit types' scripts/check-architecture.sh` → no output (X-1); the usage header lists the four roots (X-3) |
| REQ-6 exemption reconciliation | `grep -n 'comment create/edit/report' AGENTS.md` → no output; `sed -n '268,280p' AGENTS.md` lists exactly `file create/delete/update, message send/edit/react, preferences, region promotion, projection replication` with per-op file evidence; `make check-audit-swallow` and `make check-audit-manifest` exit 0 (manifest row `internal/service/message.go|auditMessageDelete|standalone-fact|-` unchanged) |
| REQ-7 full gate | `make test && make lint && make lint-strict && make check-file-sizes && make check-audit-swallow && make check-anchored-event-ids && make check-audit-manifest && make check-composition-root && make check-proto-sync && make check-integration-compile && make build` — every command exits 0 on the post-change tree; `make check-audit-swallow` prints the four new guard lines + five fixture-mode PASS lines + the existing block/comment/search lines; CI `check-gates` job's two commands exit 0 (D-9) |
| D-9 CI job | `.github/workflows/ci.yml` contains job `check-gates` whose run block is exactly `make check-audit-swallow` + `make check-architecture`; `git diff .github/workflows/ci.yml` shows 0 removed lines; both commands exit 0 in a throwaway worktree with no `setup-go`, no network, no Docker |

## 9. Out of scope and dependencies

**Out of scope** (per the selected direction): extending `check-audit-swallow` scan roots to `internal/{connector,outbox,event,hub,region,sso,idempotency,middleware,config}` (direction 2); `cmd/migrate` CLI dispatch tests and migration-consistency gate (direction 3); porting dead-tree functionality into the live layer (the live file/message/comment compatibility surface keeps its behavior and its REQ-6 exemptions); any change to `migrations/`, protobuf contracts, or `check-proto-sync` (B3-3 already landed); the TODO relocations named in check-architecture's header (LegacyCutoverStatus/AuditHoldScope → domain; AuditEvent.ID → `internal/pkg/idgen`).

**Dependencies / sequencing:**
- The sibling B3-1 design (`docs/designs/2026-08-10-internal-dead-tree-quarantine-b3-1-design.md`, module `cmd/audit-replay`) targets the same change set. Where they overlap (guard semantics, fixtures, Makefile roots, CI job), this design follows the cmd/migrate requirements' naming (`assert_dead_tree_absent`, `dead_tree_positive/`) and adds the invocation pin (D-3) that the sibling's A2/A3 pins cover differently. If the sibling lands first, the overlapping files must already be present — verify and skip, never re-apply divergent edits (the block/comment/search guard blocks must stay byte-identical either way).
- `make test-integration` is **not** required: no SQL, migrations, storage, connectors, event ingestion, or callback-delivery changes (live surfaces untouched).
- Post-change follow-ups (not this change): relocate `LegacyCutoverStatus`/`AuditHoldScope` and delete EX-2; switch `AuditEvent.ID` to `internal/pkg/idgen` and delete EX-3.
