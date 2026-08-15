#!/usr/bin/env bash
# check-architecture.sh — guard the live layered runtime (rules a-e).
#
# Replaces the vacuous cross-feature gate over the dead features list
# (`file message block notification`), whose every subject was either deleted
# (block) or quarantined (file, message, notification, audit) — it was green
# vacuously and proved nothing about the live layered runtime. The script
# header is the canonical rule definition; AGENTS.md references it.
#
# Rules:
#   (a) Dead-tree reintroduction guard. For each dead package
#       internal/{file,message,notification,audit}: no .go file anywhere
#       under internal/ or cmd/ may contain the import string
#       "github.com/aero/aero-id/internal/<pkg>". Scans ALL .go files
#       including *_test.go ("no package anywhere"). Files located inside
#       the dead package's own directory are excluded (each tree's own
#       tests self-import; the exclusion is inert once the dead trees are
#       deleted).
#   (b) Domain purity. Non-test files in internal/domain/ may import only
#       stdlib (first path segment contains no ".") and internal/pkg/*.
#       ALL import paths are extracted from import blocks (aliased, grouped,
#       bare, dot, blank forms) — not only internal/ paths — so a third-
#       party import such as gorm.io/gorm trips the rule.
#   (c) Repository must not import service. Non-test files under
#       internal/repository/ may not import
#       "github.com/aero/aero-id/internal/service".
#   (d) Infrastructure layers must not import service or repository.
#       Non-test files under internal/{connector,event,outbox,region,sso}/
#       (including subpackages such as internal/connector/* and
#       internal/sso/*) may not import
#       "github.com/aero/aero-id/internal/service" or
#       "github.com/aero/aero-id/internal/repository". internal/hub is
#       deliberately NOT in this list (EX-1).
#   (e) Handler <-> repository ban. Non-test files under internal/handler/
#       may not import "github.com/aero/aero-id/internal/repository" at all.
#       (Was: allow-listed enum casts from http/account.go +
#       http/audit_ledger.go; deleted when LegacyCutoverStatus and
#       AuditHoldScope moved to internal/domain.)
#
# Exceptions (each with justification; review periodically):
#   EX-1 hub -> service: internal/hub/adapter.go imports internal/service
#        solely for the compile-time assertion
#        `var _ service.NotificationPusher = (*Hub)(nil)`; the in-file
#        comment states the invariant "hub may import service; service must
#        NOT import hub". hub is excluded from rule (d) by design.
#
# Test asymmetry: rules (b)-(e) exclude *_test.go (required for
# internal/repository/account_backfill_integration_test.go and
# internal/event/nats_integration_test.go); rule (a) includes tests ("no
# package anywhere").
#
# Self-tests: per-rule positive/negative fixture sweeps over
# scripts/testdata/architecture/ run on every invocation. A positive fixture
# that stops matching is a SELF-TEST FAIL (the gate went vacuous); a negative
# fixture that starts matching is a SELF-TEST FAIL (the rule went over-broad).
# The fixtures are part of the gate, not decoration: a missing fixture tree
# fails the gate.
#
# Usage: scripts/check-architecture.sh   (no args; Makefile target)
set -euo pipefail

INTERNAL="github.com/aero/aero-id/internal"
DEAD_TREES=(file message notification audit)
INFRA_LAYERS=(connector event outbox region sso)
FIXTURES="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/testdata/architecture"

violations=0
fail=0

# ── rule (a): dead-tree reintroduction guard ────────────────────────────────
scan_dead_tree_imports() {
  local root f pkg
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      for pkg in "${DEAD_TREES[@]}"; do
        # own-tree exclusion: files inside the dead package's own directory
        # (its tests self-import); inert once the tree is deleted. The leading
        # "/" normalizes root-relative paths ("internal/...") so the suffix
        # pattern also matches fixture paths ("*/internal/...").
        case "/$f" in
          */internal/$pkg/*) continue ;;
        esac
        if grep -q "\"$INTERNAL/$pkg\"" "$f"; then
          echo "QUARANTINE VIOLATION: $f imports internal/$pkg (dead-tree reintroduction)"
          violations=$((violations + 1))
        fi
      done
    done < <(find "$root" -name '*.go' 2>/dev/null | sort)
  done
}

# ── rule (b): domain purity ─────────────────────────────────────────────────
# Import-block-scoped extraction (aliased, grouped, bare, dot, blank forms):
# string literals outside import blocks (e.g. MIME maps in
# internal/domain/file.go) are never treated as imports.
extract_import_paths() {
  awk '
    function clean(s) { sub(/\/\/.*$/, "", s); sub(/\/\*.*\*\//, "", s); return s }
    /^import[[:space:]]*\(/ { inblock = 1; next }
    inblock && /^[[:space:]]*\)/ { inblock = 0; next }
    inblock {
      line = clean($0)
      if (match(line, /"[^"]+"/)) print substr(line, RSTART + 1, RLENGTH - 2)
      next
    }
    /^import/ {
      line = clean($0)
      if (match(line, /^import([[:space:]]+[^[:space:]]+)?[[:space:]]+"[^"]+"/)) {
        p = substr(line, RSTART, RLENGTH)
        sub(/^[^"]*"/, "", p)
        sub(/"[^"]*$/, "", p)
        print p
      }
    }
  '
}

# classification order matters (DD-3): internal/pkg/* first (its first
# segment is github.com), then the dot-segment heuristic.
classify_domain_import() {
  local path="$1"
  case "$path" in
    "$INTERNAL/pkg/"*) return 0 ;;
  esac
  case "${path%%/*}" in
    *.*) return 1 ;; # third-party
    *) return 0 ;;   # stdlib
  esac
}

scan_domain_purity() {
  local root f path
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      case "/$f" in
        */internal/domain/*) ;;
        *) continue ;;
      esac
      while IFS= read -r path; do
        if ! classify_domain_import "$path"; then
          echo "VIOLATION: $f imports $path (domain must be stdlib + internal/pkg/* only)"
          violations=$((violations + 1))
        fi
      done < <(extract_import_paths < "$f")
    done < <(find "$root" -name '*.go' ! -name '*_test.go' 2>/dev/null | sort)
  done
}

# ── rule (c): repository must not import service ────────────────────────────
scan_repository_service() {
  local root f
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      case "/$f" in
        */internal/repository/*) ;;
        *) continue ;;
      esac
      if grep -q "\"$INTERNAL/service\"" "$f"; then
        echo "VIOLATION: $f imports internal/service (repository must not import service)"
        violations=$((violations + 1))
      fi
    done < <(find "$root" -name '*.go' ! -name '*_test.go' 2>/dev/null | sort)
  done
}

# ── rule (d): infra layers must not import service or repository ────────────
scan_infra_service_repo() {
  local root f layer
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      for layer in "${INFRA_LAYERS[@]}"; do
        case "/$f" in
          */internal/$layer/*) ;;
          *) continue ;;
        esac
        if grep -q "\"$INTERNAL/service\"" "$f"; then
          echo "VIOLATION: $f imports internal/service (infra layer must not import service/repository)"
          violations=$((violations + 1))
        fi
        if grep -q "\"$INTERNAL/repository\"" "$f"; then
          echo "VIOLATION: $f imports internal/repository (infra layer must not import service/repository)"
          violations=$((violations + 1))
        fi
      done
    done < <(find "$root" -name '*.go' ! -name '*_test.go' 2>/dev/null | sort)
  done
}

# ── rule (e): handler must not import repository ───────────────────────────
# Absolute ban: any non-test file under internal/handler/ that imports
# "github.com/aero/aero-id/internal/repository" is a violation. (Was: two
# allow-listed files + two enum idents; deleted when
# LegacyCutoverStatus and AuditHoldScope moved to internal/domain.)
scan_handler_repo_import() {
  local root f
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      case "/$f" in
        */internal/handler/*) ;;
        *) continue ;;
      esac
      if grep -q "\"$INTERNAL/repository\"" "$f"; then
        echo "VIOLATION: $f imports internal/repository (handler may not import internal/repository)"
        violations=$((violations + 1))
      fi
    done < <(find "$root" -name '*.go' ! -name '*_test.go' 2>/dev/null | sort)
  done
}

# ── self-test sweep: fixtures are part of the gate ──────────────────────────
# Each scan function is run in the current shell (redirect, not command
# substitution) so the global counter is observable per rule.
selftest_rule() {
  local label="$1" fn="$2" count out
  local before after

  before=$violations
  out="$(mktemp)"
  "$fn" "$FIXTURES/positive" >"$out" || true
  after=$violations
  count=$((after - before))
  if [ "$count" -lt 1 ]; then
    echo "SELF-TEST FAIL: rule ($label) positive fixture matched nothing — gate is vacuous"
    fail=1
  else
    echo "✓ self-test rule ($label): positive fires ($count violation(s))"
  fi
  rm -f "$out"

  before=$violations
  out="$(mktemp)"
  "$fn" "$FIXTURES/negative" >"$out" || true
  after=$violations
  count=$((after - before))
  if [ "$count" -ne 0 ]; then
    echo "SELF-TEST FAIL: rule ($label) negative fixture matched $count violation(s):"
    sed 's/^/    /' "$out"
    fail=1
  else
    echo "✓ self-test rule ($label): negative clean"
  fi
  rm -f "$out"
}

if [ ! -d "$FIXTURES" ]; then
  echo "SELF-TEST FAIL: $FIXTURES is missing — the gate fixtures are part of the gate, not decoration"
  fail=1
else
  selftest_rule "(a) dead-tree ban"            scan_dead_tree_imports
  selftest_rule "(b) domain purity"            scan_domain_purity
  selftest_rule "(c) repository/service"       scan_repository_service
  selftest_rule "(d) infra layers"             scan_infra_service_repo
  selftest_rule "(e1) handler repo import"     scan_handler_repo_import
fi

# ── real-tree scan: internal/ and cmd/ only (fixtures are never scanned) ────
violations=0
scan_dead_tree_imports internal cmd
scan_domain_purity internal cmd
scan_repository_service internal cmd
scan_infra_service_repo internal cmd
scan_handler_repo_import internal cmd

if [ "$fail" -ne 0 ] || [ "$violations" -gt 0 ]; then
  echo ""
  [ "$violations" -gt 0 ] && echo "$violations architecture violation(s) detected."
  [ "$fail" -ne 0 ] && echo "check-architecture SELF-TEST FAILED — the gate fixtures or guards are broken."
  exit 1
fi

echo "✓ Architecture check passed (rules a-e: dead-tree ban, domain purity, repository/service, infra layers, handler/repository)"
