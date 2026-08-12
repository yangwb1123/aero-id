#!/usr/bin/env bash
# check-audit-swallow.sh — ban swallowed and bare-call audit writes.
#
# P1 (assign-swallow): any statement that assigns the result of
#   .Record / .Log / .Publish / .FinishPublication / .ClaimPublication /
#   .RequeuePublication / .RequeueDead / .MarkDelivery to _ or _, _
#   — the caller discards the error. This is the historical swallow shape
#   (the converted `_ = s.audit.Record(` and `_ = s.audit.Log(` sites, the inline
#   `_ = publisher.Publish` loop, `_ = s.publications.FinishPublication(`).
#   The B2-5 replay surface is included: RequeuePublication/RequeueDead take a
#   *domain.AuditFact and their error covers the in-tx audit append, so a
#   discarded result would silently drop both the requeue and its fact.
#   MarkDelivery (B2-5 drain settlement) is included for the same reason: its
#   error covers attempt+1 and lease-clear, so a discarded result silently
#   drops the settlement AND the gate (a swallowed MarkDelivery is how the
#   drain-path swallow went unobserved — callback_worker.go:121 pre-2026-08-08
#   hardening). The new positive fixture line is matched by the self-test.
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
#   like the historical swallow it replaces.
#
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
#   - *_test.go files are excluded from the ROOT scan (R-M scan-scope
#     amendment): the P1/P2 patterns are statement-start anchored, so a bare
#     `t.Log(` at statement start in a test would be a false positive; test-
#     file audit correctness is enforced by the audit-coverage integration
#     tests, which assert ledger/publication row counts.
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
# only green at 0.
#
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

set -u

ROOTS=("$@")
if [ "${#ROOTS[@]}" -eq 0 ]; then
  ROOTS=(internal/service)
fi
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES="$SELF_DIR/testdata/audit-swallow"

P1_PATTERN='^[[:space:]]*(_ = |_, _ = )[A-Za-z_][A-Za-z0-9_.]*\.(Record|Log|Publish|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead|MarkDelivery)\('
P2_PATTERN='^[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*\.(Record|Log|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead|MarkDelivery)\('

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
  return 0
}

assert_dead_tree_absent "internal/file" "$SELF_DIR/../internal/file"
assert_dead_tree_absent "internal/message" "$SELF_DIR/../internal/message"
assert_dead_tree_absent "internal/notification" "$SELF_DIR/../internal/notification"
assert_dead_tree_absent "internal/audit" "$SELF_DIR/../internal/audit"

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
# ── to the historical blind spot). The membership assertion applies to ──────
# ── explicit-root invocations — the Makefile gate always passes explicit ────
# ── roots — while the no-arg default (internal/service only) is the ─────────
# ── documented single-service shortcut and is exempt. ───────────────────────
comment_hits="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/comment_positive" --include='*.go' 2>/dev/null | wc -l)"
if [ "$comment_hits" -lt 1 ]; then
  echo "SELF-TEST FAIL: comment fixture under $FIXTURES/comment_positive matched nothing"
  fail=1
fi
if [ "$#" -gt 0 ]; then
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

# ── self-test: each new guard invocation must stay in the script (deleting
# ── one silently weakens the quarantine to the P1/P2 scan alone).
for pkg in file message notification audit; do
  if ! grep -q "assert_dead_tree_absent \"internal/$pkg\" \"\$SELF_DIR/../internal/$pkg\"" "$0"; then
    echo "SELF-TEST FAIL: FM-12 guard invocation for internal/$pkg missing from $0"
    fail=1
  fi
done

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

# ── self-test: the gate must accept its own negative fixtures ────────────
neg_hits="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/negative" --include='*.go' 2>/dev/null | wc -l)"
if [ "$neg_hits" -ne 0 ]; then
  echo "SELF-TEST FAIL: negative fixture matched $neg_hits line(s):"
  grep -rnE "$P1_PATTERN|$P2_PATTERN" "$FIXTURES/negative" --include='*.go' 2>/dev/null
  fail=1
fi

# ── scan the live tree ────────────────────────────────────────────────────
matches=""
for root in "${ROOTS[@]}"; do
  found="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$root" --include='*.go' --exclude='*_test.go' 2>/dev/null)"
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
