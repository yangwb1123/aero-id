#!/usr/bin/env bash
# check-audit-swallow.sh — ban swallowed and bare-call audit writes.
#
# P1 (assign-swallow): any statement that assigns the result of
#   .Record / .Log / .Publish / .FinishPublication / .ClaimPublication /
#   .RequeuePublication / .RequeueDead / .MarkDelivery / .QuarantineEvent to
#   _ or _, _ — the caller discards the error. This is the historical swallow
#   shape (the converted `_ = s.audit.Record(` and `_ = s.audit.Log(` sites,
#   the inline `_ = publisher.Publish` loop, `_ = s.publications.FinishPublication(`).
#   The B2-5 replay surface is included: RequeuePublication/RequeueDead take a
#   *domain.AuditFact and their error covers the in-tx audit append, so a
#   discarded result would silently drop both the requeue and its fact.
#   MarkDelivery (B2-5 drain settlement) is included for the same reason: its
#   error covers attempt+1 and lease-clear, so a discarded result silently
#   drops the settlement AND the gate (a swallowed MarkDelivery is how the
#   drain-path swallow went unobserved — callback_worker.go:121 pre-2026-08-08
#   hardening). QuarantineEvent (event-ingest quarantine, 2026-08-16) is
#   included because its error covers the durable quarantine receipt write
#   (and the audit coupling): a discarded result silently drops the quarantine
#   state while the caller still returns a deterministic 4xx. The new positive
#   fixture lines are matched by the self-test.
# P2 (bare-call): a bare statement `s.audit.Record(ctx, f)` or `s.audit.Log(ctx, e)`
#   also discards the error and matches nothing today (errcheck is not enabled in
#   .golangci.yml) — P2 closes that false-negative hole. Anchoring at line
#   start naturally excludes `if err :=`, `return`, `err :=`, `defer`, `go`
#   and comment lines. `Publish` is deliberately NOT in P2:
#   notification.go:281,294 bare `d.pusher.Publish(...)` calls are the
#   best-effort SSE hub (AGENTS.md), not audit calls; a future bare audit
#   `Publish` swallow is still caught by P1's `_ =` form. Requeue* IS in P2:
#   the only callers are cmd/audit-replay, and a bare requeue call there would
#   discard the audit-fact append error. MarkDelivery IS in P2: a bare
#   settlement call (e.g. a future regression to `s.repo.MarkDelivery(...)`
#   without the error branch) discards the attempt+1/lease-clear error exactly
#   like the historical swallow it replaces. QuarantineEvent IS in P2: a bare
#   `s.receipts.QuarantineEvent(...)` call (e.g. a future regression before
#   the quarantineEvent helper) discards the receipt-write error exactly like
#   the two historical `_ =` sites it replaces.
#
# Exclusions (documented):
#   - only the live layer is scanned (default: the full gate surface —
#     the 21-root list: internal/service internal/repository internal/app
#     internal/storage internal/block internal/comment cmd, the four absent
#     quarantine tripwire roots internal/{file,message,notification,audit},
#     and the ten extended live control-plane roots internal/{connector,
#     outbox,event,hub,region,sso,idempotency,middleware,config,handler}
#     (G2, 2026-08-14 — the B2-2 blind spot); ADR-012
#     Option B — the no-arg default equals the Makefile gate line, so a
#     tampered CI no-arg invocation cannot shrink the scan).
#     internal/block, internal/comment and internal/search were deleted in
#     the 2026-08-08 dead-tree quarantine; internal/{file,message,
#     notification,audit} in the 2026-08-10 quarantine. All seven are
#     absent-path tripwire roots under the FM-12 guards below, so even a
#     benign reintroduction (compilable package, propagated audit error)
#     turns the gate red.
#   - *_test.go files are excluded from the ROOT scan (R-M scan-scope
#     amendment): the P1/P2 patterns are statement-start anchored, so a bare
#     `t.Log(` at statement start in a test would be a false positive; test-
#     file audit correctness is enforced by the audit-coverage integration
#     tests, which assert ledger/publication row counts.
#   - *.pb.go files are excluded from the ROOT scan (G2 mechanical
#     exclusion): committed generated protobuf stubs under
#     internal/sso/{auditpb,authzpb,ssopb} and internal/handler/grpc/aeropb
#     are never hand-edited and drift-pinned by check-proto-sync. The dot
#     glob is load-bearing: the underscore form *_pb.go matches nothing in
#     this repo (a silent vacuous no-op that would re-expose the stubs —
#     FM-5, pinned by the pb_exclude liveness self-test).
#   - a narrow breaker-observation exemption (G2): bare statement-start
#     `c.breaker.Record(...)` lines (internal/sso/client.go:274,277,281;
#     authorize/get_users paths at 370-381/423-430 since B3-2) are
#     circuit-breaker outcome observations (breaker.Record is void), not
#     audit writes. BREAKER_EXEMPT_PATTERN is a statement-anchored,
#     receiver-exact post-filter (grep -vE) — d.breaker, c.breaker2,
#     s.audit.*, and the P1 `_ = c.breaker.Record(` form stay red. Do not
#     loosen it to a substring filter.
#   - the self-test fixtures under scripts/testdata/audit-swallow/ live
#     outside the scanned tree and are never scanned.
#
# No `Log(` exclusion exists: Log is a first-class gate citizen (P1 + P2).
# AGENTS.md's historical "AuditLogger ... legacy diagnostics only" carve-out
# referred to the dead-tree internal/audit type, deleted in the 2026-08-10
# quarantine along with domain.AuditLogger — it never applied to the live
# service.AuditLogger adapter.
# Likewise the legacy middleware fail-open carve-out (Ban and legacy checks,
# AGENTS.md) must never be re-applied to audit writes: a swallowed or bare-call
# Log/Record is a gate violation regardless of any middleware fail-open
# convention.
#
# Baseline on today's tree: 0 matches — every live-layer audit write
# (`Record` or `Log`) propagates its error via `if err :=` and the gate is
# only green at 0. Post-G2 extended baseline (21 roots): 0 matches — the
# three c.breaker.Record( observations are exempted, the ten committed
# *.pb.go stubs are excluded, nothing else matches.
#
# Usage: scripts/check-audit-swallow.sh [ROOT...]   (no-arg default: the full
# gate surface — internal/service internal/repository internal/app
# internal/storage internal/block internal/comment cmd internal/file
# internal/message internal/notification internal/audit internal/connector
# internal/outbox internal/event internal/hub internal/region internal/sso
# internal/idempotency internal/middleware internal/config internal/handler;
# ADR-012 Option B: a no-arg invocation must scan exactly what the done-gate
# scans, so a tampered CI body cannot shrink the scan)
# Multiple roots are scanned; the Makefile gate scans the whole live layer:
#   internal/service internal/repository internal/app internal/storage
#   internal/block internal/comment cmd internal/file internal/message
#   internal/notification internal/audit internal/connector internal/outbox
#   internal/event internal/hub internal/region internal/sso
#   internal/idempotency internal/middleware internal/config internal/handler
# (internal/block, internal/comment, internal/search and
# internal/{file,message,notification,audit} are absent quarantine tripwires —
# the directories were deleted; the FM-12 guards below are the total
# tripwires.) G2 (2026-08-14) implemented the sso breaker exclusion as the
# BREAKER_EXEMPT_PATTERN post-filter: sso's bare `c.breaker.Record(...)` is a
# breaker observation, not an audit write, and would be a P2 false positive.
# The filter is statement-anchored and receiver-exact — only the bare
# statement-start `c.breaker.Record(` line is exempted; the P1 `_ =` form,
# other receivers (d.breaker, c.breaker2), and every canonical audit
# receiver (s.audit, s.publications, s.repo, publisher) stay fully scanned.
# The *.pb.go mechanical exclusion keeps committed generated protobuf stubs
# (internal/sso/*/ and internal/handler/grpc/aeropb/) out of the scan; the
# dot glob is load-bearing (the underscore form matches nothing here).

set -u

# ADR-012 Option B (security_engineer Ruling 2): the no-arg default is the
# FULL live-layer gate surface (identical to the Makefile invocation), not
# internal/service alone — a no-arg invocation (e.g. a tampered CI job body
# `bash scripts/check-audit-swallow.sh`) must scan exactly what the done-gate
# scans and must trip the membership tripwires. The membership tripwires
# below are therefore UNCONDITIONAL (no `$# -gt 0` gate): dropping a
# quarantine root from the default OR from an explicit-root invocation is
# red either way.
ROOTS=("$@")
if [ "${#ROOTS[@]}" -eq 0 ]; then
  # G2 (2026-08-14): extended to the ten unscanned live control-plane dirs
  # (connector/outbox/event/hub/region/sso/idempotency/middleware/config/
  # handler), appended after the historical 11 in the Makefile order.
  # ADR-012 Option B byte-equivalence with the Makefile recipe line is
  # machine-checked below (REQ-G2-3).
  ROOTS=(internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd internal/file internal/message internal/notification internal/audit internal/connector internal/outbox internal/event internal/hub internal/region internal/sso internal/idempotency internal/middleware internal/config internal/handler)
fi
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES="$SELF_DIR/testdata/audit-swallow"

P1_PATTERN='^[[:space:]]*(_ = |_, _ = )[A-Za-z_][A-Za-z0-9_.]*\.(Record|Log|Publish|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead|MarkDelivery|QuarantineEvent)\('
P2_PATTERN='^[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*\.(Record|Log|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead|MarkDelivery|QuarantineEvent)\('
# G2 breaker-observation exemption (2026-08-14, refreshed B3-2):
# internal/sso/client.go calls `c.breaker.Record(...)` bare (validate lines
# 274,277,281; authorize 370-381; get_users 423-430) as circuit-breaker
# outcome observations; breaker.Record is VOID (internal/sso/breaker.go), so
# the call discards nothing and the P1 `_ =` form cannot even compile. The
# exemption is a POST-FILTER on grep output (grep -vE), not a change to
# P1/P2. Mandated properties:
#   - statement-anchored: `c.breaker.Record(` only at statement start after
#     optional whitespace within the grep output line;
#   - receiver-exact: only the identifier chain c.breaker — d.breaker,
#     c.breaker2, and any other receiver stay red;
#   - P1 form NOT exempted: `_ = c.breaker.Record(` stays red;
#   - tolerates both grep output formats: path:line:content (directory
#     scans) and line:content (single-file fixture greps).
# Do NOT loosen this to a substring filter (FM-3): p1_anchor.go pins the
# statement anchoring and near_miss.go pins receiver exactness.
BREAKER_EXEMPT_PATTERN='^([^:]*:)?[0-9]*:[[:space:]]*c\.breaker\.Record\('

fail=0

# ── quarantine guard (FM-12): internal/block must never exist ──────────────
# The tripwire root scans the (absent) path for swallow shapes; this assertion
# makes the tripwire total — even a benign reintroduction (compilable package,
# working import, propagated audit error) is a gate violation. Deliberately
# lifting the quarantine = deleting this block AND updating the audit.go note
# + the exclusion comment, in one commit.
if [ -d "$SELF_DIR/../internal/block" ]; then
  echo "QUARANTINE VIOLATION: internal/block must not exist (dead-tree quarantine, FM-12)"
  fail=1
else
  echo "✓ quarantine guard: internal/block absent (FM-12)"
fi

# ── quarantine guard (FM-12): internal/comment must never exist ────────────
# internal/comment carried the last swallowed, fail-open, post-commit audit
# write (service.go:431 `_ = s.audit.Log(`) outside any transaction; its only
# consumer internal/search was equally unwired. The guard is total — even a
# benign reintroduction (compilable package, propagated audit error) is a gate
# violation. Deliberately lifting the quarantine = deleting this guard AND the
# internal/search guard AND the Makefile tripwire root + comment_positive
# self-test + the audit.go note + the exclusion comment + AGENTS.md, in one
# commit.
if [ -d "$SELF_DIR/../internal/comment" ]; then
  echo "QUARANTINE VIOLATION: internal/comment must not exist (dead-tree quarantine, FM-12)"
  fail=1
else
  echo "✓ quarantine guard: internal/comment absent (FM-12)"
fi

# ── quarantine guard (FM-12): internal/search must never exist ─────────────
# internal/search was the only consumer of internal/comment (search queries
# comments via the feature repo) and is itself unwired with zero importers;
# without this guard it would be an unscanned path in the same class as the
# deleted comment tree.
if [ -d "$SELF_DIR/../internal/search" ]; then
  echo "QUARANTINE VIOLATION: internal/search must not exist (dead-tree quarantine, FM-12)"
  fail=1
else
  echo "✓ quarantine guard: internal/search absent (FM-12)"
fi

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
  guard_pass=$((guard_pass + 1))
  return 0
}

guard_pass=0
assert_dead_tree_absent "internal/file" "$SELF_DIR/../internal/file"
assert_dead_tree_absent "internal/message" "$SELF_DIR/../internal/message"
assert_dead_tree_absent "internal/notification" "$SELF_DIR/../internal/notification"
assert_dead_tree_absent "internal/audit" "$SELF_DIR/../internal/audit"

# ── self-test (ADR-012, guard-PASS counter): the four main FM-12 guards must
# ── actually EXECUTE — a text-preserving neutralization (commenting out an
# ── invocation, wrapping it in `if false; then … fi`) previously satisfied
# ── the substring invocation pin while the guard never ran (BYPASS 1,
# ── verified 2026-08-12: benign internal/file reintroduction landed exit 0).
# ── The counter is incremented only by the function's PASS branch, so fewer
# ── than four PASSes means an invocation was removed from execution.
if [ "$guard_pass" -ne 4 ]; then
  echo "SELF-TEST FAIL: FM-12 main guard produced $guard_pass/4 PASS lines (guard invocation neutralized?)"
  fail=1
fi

# ── self-test: the gate must flag its own positive fixtures ──────────────
pos_hits="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/positive" --include='*.go' 2>/dev/null | wc -l)"
if [ "$pos_hits" -lt 1 ]; then
  echo "SELF-TEST FAIL: positive fixture under $FIXTURES/positive matched nothing"
  fail=1
fi

# ── self-test (I6): the Requeue* patterns must fire under a root named cmd/
# ── (the Makefile gate scans cmd) — green must mean "scanned", never
# ── "never scanned". ────────────────────────────────────────────────────────
cmd_hits="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/cmd_positive/cmd" --include='*.go' 2>/dev/null | wc -l)"
if [ "$cmd_hits" -lt 1 ]; then
  echo "SELF-TEST FAIL: cmd fixture under $FIXTURES/cmd_positive/cmd matched nothing (cmd/ would be unscanned)"
  fail=1
fi

# ── self-test (REQ-2): the comment_positive fixture pins the extended ───────
# ── scope — the exact P1/P2 shapes that made internal/comment the last ──────
# ── excluded swallow site must match (pattern-regression tripwire), and ─────
# ── internal/comment must stay in the ROOTS list (root-removal tripwire: ────
# ── dropping it from the Makefile gate would silently shrink the scan back ──
# ── to the historical blind spot). UNCONDITIONAL since ADR-012 Option B: ────
# ── the no-arg default equals the Makefile gate line (includes ──────────────
# ── internal/comment), so a tampered no-arg CI invocation cannot skip the ───
# ── membership assertion either. ────────────────────────────────────────────
comment_hits="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/comment_positive" --include='*.go' 2>/dev/null | wc -l)"
if [ "$comment_hits" -lt 1 ]; then
  echo "SELF-TEST FAIL: comment fixture under $FIXTURES/comment_positive matched nothing"
  fail=1
fi
root_has_comment=false
for root in "${ROOTS[@]}"; do
  if [ "$root" = "internal/comment" ]; then
    root_has_comment=true
  fi
done
if [ "$root_has_comment" = false ]; then
  echo "SELF-TEST FAIL: internal/comment missing from scan ROOTS (tripwire root removed)"
  fail=1
fi

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

# ── self-test: each new guard invocation must stay in the script, ANCHORED
# ── at line start (ADR-012): a commented-out or dead-code invocation no
# ── longer satisfies the pin (BYPASS 1). The guard-PASS counter above is the
# ── execution proof; this pin is the text-presence proof.
for pkg in file message notification audit; do
  if ! grep -qE "^[[:space:]]*assert_dead_tree_absent \"internal/$pkg\" \"\\\$SELF_DIR/../internal/$pkg\"[[:space:]]*$" "$0"; then
    echo "SELF-TEST FAIL: FM-12 guard invocation for internal/$pkg missing or not line-anchored in $0"
    fail=1
  fi
done

# ── self-test (REQ-2.4): the four quarantine roots must stay in the ROOTS
# ── list (root-removal tripwire: dropping one from the Makefile gate would
# ── silently shrink the scan back toward the historical blind spot).
# ── UNCONDITIONAL since ADR-012 Option B (security_engineer Ruling 2): the
# ── no-arg default equals the Makefile gate line and includes all four
# ── roots, so a tampered no-arg CI invocation cannot skip this assertion;
# ── an explicit invocation missing a root is red too.
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

# ── adjacent-path guard (ADR-012, 2026-08-12): case-insensitive exact-name
# ── basename match over internal/ + cmd/ at any depth (find -L: symlinked
# ── variants included). Reintroducing the dead-tree harm class at a case or
# ── wrapper variant (internal/FILE, internal/legacy/file) previously landed
# ── green with zero script edits (BYPASS 2, verified 2026-08-12). Near-var-
# ── iant names (internal/filex) remain a documented deliberate-evasion
# ── residual: prefix matching would false-positive on live
# ── internal/connector/auditgovernance, internal/sso/auditpb, cmd/audit-replay.
for base in file message notification audit; do
  adjacent="$(find -L "$SELF_DIR/../internal" "$SELF_DIR/../cmd" -type d -iname "$base" 2>/dev/null)"
  if [ -n "$adjacent" ]; then
    echo "QUARANTINE VIOLATION: adjacent-path reintroduction of dead-tree name '$base' (ADR-012):"
    echo "$adjacent"
    fail=1
  fi
done

# ── self-test (ADR-012): the adjacent-path guard must flag its own positive
# ── fixtures — case-variant dirs under scripts/testdata/audit-swallow/
# ── adjacent_positive/ (outside internal/ + cmd/, so build-safe and never
# ── scanned). A missing fixture dir fails the gate: fixtures are gate parts.
for base in file message notification audit; do
  if ( find -L "$FIXTURES/adjacent_positive" -type d -iname "$base" 2>/dev/null | grep -q . ); then
    echo "✓ fixture-mode self-test: adjacent-path guard flags $base variant (ADR-012)"
  else
    echo "SELF-TEST FAIL: adjacent_positive fixture for '$base' missing or did not trip the ADR-012 guard"
    fail=1
  fi
done

# ── self-test (FM-16, X-5/F1): no present-tense dead-type reference. The
# ── dead-tree AuditLogger interface was deleted in the 2026-08-10
# ── quarantine; AGENTS.md must never describe it in the present tense or
# ── mislabel the live service.AuditLogger adapter as best-effort/legacy.
# ── Corrected form [`]?AuditLogger[`]? matches an optional literal backtick
# ── (grep cannot escape a backtick with a backslash — the historical
# ── `\`AuditLogger` pin was vacuous and matched nothing, verified 2026-08-12).
# ── The banned carve-out phrasings are spelled hyphenated here so this
# ── comment itself cannot satisfy the pins below.
if grep -nE '[`]?AuditLogger[`]? (is|are|remains|carries|holds|provides|writes|records|emits|stores|serves|has|supports|maintains|logs)' "$SELF_DIR/../AGENTS.md" >/dev/null 2>&1; then
  echo "SELF-TEST FAIL (FM-16): AGENTS.md describes AuditLogger in the present tense (dead type) or mislabels the live adapter"
  fail=1
fi
if grep -nF 'carve-out'" does not apply" "$SELF_DIR/../AGENTS.md" >/dev/null 2>&1; then
  echo "SELF-TEST FAIL (FM-16): AGENTS.md present-tense carve-out dead-type reference"
  fail=1
fi
if grep -nF 'carve-out'" refers to" "$0" >/dev/null 2>&1; then
  echo "SELF-TEST FAIL (FM-16): check-audit-swallow.sh present-tense carve-out dead-type reference"
  fail=1
fi

# ── self-test: the gate must accept its own negative fixtures ────────────
neg_hits="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/negative" --include='*.go' 2>/dev/null | wc -l)"
if [ "$neg_hits" -ne 0 ]; then
  echo "SELF-TEST FAIL: negative fixture matched $neg_hits line(s):"
  grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/negative" --include='*.go' 2>/dev/null
  fail=1
fi

# ── self-test (REQ-G2-2): the ten extended live roots must stay in the ROOTS
# ── list (root-removal tripwire, unconditional — ADR-012 Option B: the
# ── no-arg default equals the Makefile gate line and includes all ten, so a
# ── tampered no-arg CI invocation cannot skip this; explicit invocations
# ── missing a root are red too).
for pkg in connector outbox event hub region sso idempotency middleware config handler; do
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

# ── self-test (REQ-G2-3): the Makefile check-audit-swallow recipe line must
# ── carry the FULL 21-root list. Anchored on the recipe line (the
# ── `@bash scripts/check-audit-swallow.sh …` line), NOT the target-name
# ── line — a shrunk Makefile line is red even when the script default is
# ── still correct.
if ! grep -qE '^[[:space:]]*@bash scripts/check-audit-swallow\.sh internal/service internal/repository internal/app internal/storage internal/block internal/comment cmd internal/file internal/message internal/notification internal/audit internal/connector internal/outbox internal/event internal/hub internal/region internal/sso internal/idempotency internal/middleware internal/config internal/handler[[:space:]]*$' "$SELF_DIR/../Makefile"; then
  echo "SELF-TEST FAIL: Makefile check-audit-swallow recipe line missing or does not carry the full extended root list"
  fail=1
fi

# ── self-test (REQ-G2-4): the breaker exemption must be LIVE and NARROW ──
# ── breaker_ok.go (bare c.breaker.Record(...) lines) must match the raw ────
# ── patterns yet yield ZERO residuals after the BREAKER_EXEMPT_PATTERN ─────
# ── post-filter. ───────────────────────────────────────────────────────────
breaker_raw="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/breaker_exempt/breaker_ok.go" 2>/dev/null)"
if [ -z "$breaker_raw" ]; then
  echo "SELF-TEST FAIL: breaker_ok.go fixture matched nothing (exemption not exercised)"
  fail=1
elif [ "$(printf '%s\n' "$breaker_raw" | grep -cvE "$BREAKER_EXEMPT_PATTERN" || true)" -ne 0 ]; then
  echo "SELF-TEST FAIL: breaker exemption left residual matches in breaker_ok.go:"
  printf '%s\n' "$breaker_raw" | grep -vE "$BREAKER_EXEMPT_PATTERN"
  fail=1
fi

# ── self-test (REQ-G2-5): the exemption must NEVER hide a real audit ───────
# ── swallow — the mandatory no-hiding regression. audit_red.go (s.audit ────
# ── bare calls), near_miss.go (d.breaker / c.breaker2) and p1_anchor.go ─────
# ── (`_ = c.breaker.Record(`) must each stay ≥1 match after the exemption ──
# ── filter; a missing fixture file fails the gate (fixture-mode totality). ──
for file in audit_red near_miss p1_anchor; do
  if [ ! -f "$FIXTURES/breaker_exempt/$file.go" ]; then
    echo "SELF-TEST FAIL: fixture $FIXTURES/breaker_exempt/$file.go missing (fixtures are gate parts)"
    fail=1
    continue
  fi
  red_hits="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/breaker_exempt/$file.go" 2>/dev/null | grep -vE "$BREAKER_EXEMPT_PATTERN" | wc -l)"
  if [ "$red_hits" -lt 1 ]; then
    echo "SELF-TEST FAIL: $file.go must stay red under the breaker exemption (exemption too broad?)"
    fail=1
  fi
done

# ── self-test (REQ-G2-6): the *.pb.go exclusion must be LIVE — the ─────────
# ── pb_exclude fixture (a P2-shaped line in a file actually named *.pb.go) ──
# ── must match raw yet be invisible to the scan-loop grep shape; a ──────────
# ── regression to the vacuous *_pb.go glob reddens the gate. ────────────────
pb_raw="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/pb_exclude" --include='*.go' 2>/dev/null)"
if [ -z "$pb_raw" ]; then
  echo "SELF-TEST FAIL: pb_exclude fixture matched nothing (exclusion not exercised)"
  fail=1
fi
pb_scanned="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/pb_exclude" --include='*.go' --exclude='*.pb.go' 2>/dev/null)"
if [ -n "$pb_scanned" ]; then
  echo "SELF-TEST FAIL: --exclude='*.pb.go' did not exclude the pb_exclude fixture:"
  echo "$pb_scanned"
  fail=1
fi

# ── self-test (REQ-G2-7): text-presence pins (line-anchored, ADR-012 ───────
# ── style) — the exemption definition, its application in the scan loop, ───
# ── the *.pb.go exclusion, and the extended membership loop header must ────
# ── all stay in this script. ───────────────────────────────────────────────
if ! grep -qE '^BREAKER_EXEMPT_PATTERN=' "$0"; then
  echo "SELF-TEST FAIL: BREAKER_EXEMPT_PATTERN definition missing from $0"
  fail=1
fi
if ! grep -qF -- "--exclude='*.pb.go'" "$0"; then
  echo "SELF-TEST FAIL: --exclude='*.pb.go' missing from $0"
  fail=1
fi
if ! grep -qF 'grep -vE "$BREAKER_EXEMPT_PATTERN"' "$0"; then
  echo "SELF-TEST FAIL: BREAKER_EXEMPT_PATTERN post-filter application missing from $0"
  fail=1
fi
if ! grep -qE '^# ── self-test \(REQ-G2-2\): the ten extended live roots must stay in the ROOTS' "$0"; then
  echo "SELF-TEST FAIL: extended membership loop header missing from $0"
  fail=1
fi

# ── scan the live tree ────────────────────────────────────────────────────
matches=""
for root in "${ROOTS[@]}"; do
  found="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$root" --include='*.go' --exclude='*_test.go' --exclude='*.pb.go' 2>/dev/null | grep -vE "$BREAKER_EXEMPT_PATTERN")"
  if [ -n "$found" ]; then
    matches+="$found"$'\n'
  fi
done
if [ -n "$matches" ]; then
  echo "AUDIT SWALLOW VIOLATIONS (must be 0):"
  echo "$matches"
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo ""
  echo "check-audit-swallow FAILED — audit write errors must never be discarded."
  exit 1
fi

# ── T-5 mechanical wiring assertions (single delivery path) ───────────────
# (a) drainPublications( must have exactly ONE call site outside its
#     definition and outside _test.go files, and that site must lie inside
#     callbackService.Start (today internal/service/callback_worker.go).
#     This pins the "drain worker is the only audit delivery path" invariant.
drain_sites="$(grep -rn "drainPublications(" internal/service --include='*.go' 2>/dev/null \
  | grep -v '_test.go' | grep -v 'func (s \*callbackService) drainPublications' || true)"
drain_count="$(printf '%s\n' "$drain_sites" | grep -c 'drainPublications(' || true)"
if [ "$drain_count" -ne 1 ]; then
  echo "T-5 VIOLATION: drainPublications( has $drain_count non-test call site(s), want exactly 1:"
  echo "$drain_sites"
  fail=1
elif ! printf '%s\n' "$drain_sites" | grep -q 'callback_worker.go:'; then
  echo "T-5 VIOLATION: the sole drainPublications( call site must live in callback_worker.go (inside Start):"
  echo "$drain_sites"
  fail=1
fi

# (b) NewAuditLedgerService( must never be called with a second argument in
#     non-test Go (the ledger has no publisher surface — Record only appends).
two_arg="$(grep -rn "NewAuditLedgerService(.*,.*)" internal --include='*.go' 2>/dev/null \
  | grep -v '_test.go' | grep -v 'func NewAuditLedgerService' || true)"
if [ -n "$two_arg" ]; then
  echo "T-5 VIOLATION: NewAuditLedgerService called with a second argument:"
  echo "$two_arg"
  fail=1
fi

# (c) `_ = publisher.Publish` must remain 0 (the inline publisher loop is dead).
publish_swallow="$(grep -rn '_ = .*\.Publish(' internal/service internal/app --include='*.go' 2>/dev/null || true)"
if [ -n "$publish_swallow" ]; then
  echo "T-5 VIOLATION: swallowed Publish call:"
  echo "$publish_swallow"
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo ""
  echo "check-audit-swallow FAILED — see violations above."
  exit 1
fi

echo "✓ check-audit-swallow: no swallowed or bare-call audit writes under ${ROOTS[*]} (self-test ok)"
