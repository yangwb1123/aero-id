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
#   (e) Handler <-> repository ban. Two assertions on non-test files under
#       internal/handler/:
#       (e1) a file may import "github.com/aero/aero-id/internal/repository"
#            only if it is exactly internal/handler/http/account.go or
#            internal/handler/http/audit_ledger.go;
#       (e2) every repository.<Ident> usage must be one of the two enum
#            idents LegacyCutoverStatus or AuditHoldScope (type-casts only —
#            no constructors, methods, or other symbols). Implemented as an
#            ident allow-list (not line-number pinning, to survive drift).
#
# Exceptions (each with justification; review periodically):
#   EX-1 hub -> service: internal/hub/adapter.go imports internal/service
#        solely for the compile-time assertion
#        `var _ service.NotificationPusher = (*Hub)(nil)`; the in-file
#        comment states the invariant "hub may import service; service must
#        NOT import hub". hub is excluded from rule (d) by design.
#   EX-2 handler -> repository enum casts: internal/handler/http/account.go
#        and internal/handler/http/audit_ledger.go import internal/repository
#        only for LegacyCutoverStatus/AuditHoldScope type-casts.
#        TODO: relocate LegacyCutoverStatus (internal/repository/
#        legacy_cutover.go) and AuditHoldScope (internal/repository/
#        audit_hold.go) to internal/domain, then delete this exception and
#        rule (e)'s allow-lists.
#   EX-3 domain -> github.com/google/uuid: internal/domain/audit.go imports
#        github.com/google/uuid v1.6.0 for the AuditEvent.ID field type of
#        the quarantined-tree audit types. The exception is exact-string
#        (not a prefix), so gorm.io/gorm and any other third-party import
#        still trips rule (b).
#        TODO: switch AuditEvent.ID to internal/pkg/idgen (the pattern used
#        by comment.go/export.go/shared.go) and delete this exception.
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
ALLOWED_HANDLER_FILES=(
  "*/internal/handler/http/account.go"
  "*/internal/handler/http/audit_ledger.go"
)
ALLOWED_REPO_IDENTS=(LegacyCutoverStatus AuditHoldScope)
UUID_EXCEPTION="github.com/google/uuid"
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
# segment is github.com), then the exact-string EX-3 exception, then the
# dot-segment heuristic.
classify_domain_import() {
  local path="$1"
  case "$path" in
    "$INTERNAL/pkg/"*) return 0 ;;
    "$UUID_EXCEPTION") return 0 ;; # EX-3
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
          echo "VIOLATION: $f imports $path (domain must be stdlib + internal/pkg/* only; see EX-3)"
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

# ── rule (e1): handler may import repository only in the two files ──────────
scan_handler_repo_import() {
  local root f pat allowed
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      case "/$f" in
        */internal/handler/*) ;;
        *) continue ;;
      esac
      if ! grep -q "\"$INTERNAL/repository\"" "$f"; then
        continue
      fi
      allowed=false
      for pat in "${ALLOWED_HANDLER_FILES[@]}"; do
        case "/$f" in
          $pat) allowed=true ;;
        esac
      done
      if [ "$allowed" = false ]; then
        echo "VIOLATION: $f imports internal/repository (handler may import repository only in http/account.go or http/audit_ledger.go; see EX-2)"
        violations=$((violations + 1))
      fi
    done < <(find "$root" -name '*.go' ! -name '*_test.go' 2>/dev/null | sort)
  done
}

# ── rule (e2): handler repository access limited to the two enum casts ──────
scan_handler_repo_idents() {
  local root f usage ident a allowed
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      case "/$f" in
        */internal/handler/*) ;;
        *) continue ;;
      esac
      while IFS= read -r usage; do
        # anchored so field access (s.repository.Put() and myrepository.X
        # cannot false-positive
        ident="$(sed 's/.*repository\.//' <<< "$usage")"
        allowed=false
        for a in "${ALLOWED_REPO_IDENTS[@]}"; do
          if [ "$ident" = "$a" ]; then
            allowed=true
          fi
        done
        if [ "$allowed" = false ]; then
          echo "VIOLATION: $f uses repository.$ident (handler repository access limited to LegacyCutoverStatus/AuditHoldScope casts; see EX-2)"
          violations=$((violations + 1))
        fi
      done < <(grep -oE '(^|[^A-Za-z0-9_.])repository\.[A-Za-z_][A-Za-z0-9_]*' "$f" || true)
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
  selftest_rule "(e2) handler repo idents"     scan_handler_repo_idents
fi

# ── real-tree scan: internal/ and cmd/ only (fixtures are never scanned) ────
violations=0
scan_dead_tree_imports internal cmd
scan_domain_purity internal cmd
scan_repository_service internal cmd
scan_infra_service_repo internal cmd
scan_handler_repo_import internal cmd
scan_handler_repo_idents internal cmd

if [ "$fail" -ne 0 ] || [ "$violations" -gt 0 ]; then
  echo ""
  [ "$violations" -gt 0 ] && echo "$violations architecture violation(s) detected."
  [ "$fail" -ne 0 ] && echo "check-architecture SELF-TEST FAILED — the gate fixtures or guards are broken."
  exit 1
fi

echo "✓ Architecture check passed (rules a-e: dead-tree ban, domain purity, repository/service, infra layers, handler/repository)"
