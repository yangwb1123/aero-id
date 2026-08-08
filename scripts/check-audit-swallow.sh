#!/usr/bin/env bash
# check-audit-swallow.sh — ban swallowed and bare-call audit writes.
#
# P1 (assign-swallow): any statement that assigns the result of
#   .Record / .Log / .Publish / .FinishPublication / .ClaimPublication /
#   .RequeuePublication / .RequeueDead to _ or _, _
#   — the caller discards the error. This is the historical swallow shape
#   (the converted `_ = s.audit.Record(` and `_ = s.audit.Log(` sites, the inline
#   `_ = publisher.Publish` loop, `_ = s.publications.FinishPublication(`).
#   The B2-5 replay surface is included: RequeuePublication/RequeueDead take a
#   *domain.AuditFact and their error covers the in-tx audit append, so a
#   discarded result would silently drop both the requeue and its fact.
# P2 (bare-call): a bare statement `s.audit.Record(ctx, f)` or `s.audit.Log(ctx, e)`
#   also discards the error and matches nothing today (errcheck is not enabled in
#   .golangci.yml) — P2 closes that false-negative hole. Anchoring at line
#   start naturally excludes `if err :=`, `return`, `err :=`, `defer`, `go`
#   and comment lines. `Publish` is deliberately NOT in P2:
#   notification.go:281,294 bare `d.pusher.Publish(...)` calls are the
#   best-effort SSE hub (AGENTS.md), not audit calls; a future bare audit
#   `Publish` swallow is still caught by P1's `_ =` form. Requeue* IS in P2:
#   the only callers are cmd/audit-replay, and a bare requeue call there would
#   discard the audit-fact append error.
#
# Exclusions (documented):
#   - the unwired parallel tree internal/{file,message,comment,
#     notification,search,audit} — only the live layer is scanned (default
#     root internal/service; the gate adds internal/repository, internal/app,
#     internal/storage, internal/block and cmd — internal/block was deleted in
#     the dead-tree quarantine, and the root is a regression tripwire: a
#     swallowed audit write reintroduced there turns the gate red).
#   - the self-test fixtures under scripts/testdata/audit-swallow/ live
#     outside the scanned tree and are never scanned.
#
# No `Log(` exclusion exists: Log is a first-class gate citizen (P1 + P2).
# AGENTS.md's "AuditLogger ... legacy diagnostics only" carve-out refers to
# the dead-tree internal/audit type, not the live service.AuditLogger adapter.
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
# Never add internal/sso without excluding its circuit-breaker calls: sso's
# bare `c.breaker.Record(...)` is a breaker observation, not an audit write,
# and would be a false positive for P2.

set -u

ROOTS=("$@")
if [ "${#ROOTS[@]}" -eq 0 ]; then
  ROOTS=(internal/service)
fi
SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES="$SELF_DIR/testdata/audit-swallow"

P1_PATTERN='^[[:space:]]*(_ = |_, _ = )[A-Za-z_][A-Za-z0-9_.]*\.(Record|Log|Publish|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead)\('
P2_PATTERN='^[[:space:]]*[A-Za-z_][A-Za-z0-9_.]*\.(Record|Log|FinishPublication|ClaimPublication|RequeuePublication|RequeueDead)\('

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
  found="$(grep -rnE "$P1_PATTERN|$P2_PATTERN" "$root" --include='*.go' 2>/dev/null)"
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
