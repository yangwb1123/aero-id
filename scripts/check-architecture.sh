#!/usr/bin/env bash
# check-architecture.sh — guard the live layered runtime (rules a-g).
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
#   (f) Domain exported-symbol liveness. Every exported top-level identifier
#       declared in non-test files under internal/domain/ — type X, var X,
#       func X (methods are covered via their receiver type, not individually),
#       every member of const ( … ) blocks, and single-line const X forms —
#       must have at least one reference matching `domain.<Symbol>`
#       (word-boundary, qualified form only) in a non-test *.go file outside
#       internal/domain, within the scanned roots. One violation per
#       unreferenced symbol. Qualified matching is required so struct-field
#       and label names (e.g. `NotificationType:` in repository/handler
#       preference structs) never count as references. Enumeration covers
#       const-block members (internal/domain carries 60+ exported const-block
#       members) and type aliases (`type ID = idgen.ID`).
#   (g) Single gRPC error mapper. Files under internal/handler/grpc/ may not
#       define a local grpcError function; use internal/pkg/errors.GRPCError.
#
# Exceptions (each with justification; review periodically):
#   EX-1 hub -> service: internal/hub/adapter.go imports internal/service
#        solely for the compile-time assertion
#        `var _ service.NotificationPusher = (*Hub)(nil)`; the in-file
#        comment states the invariant "hub may import service; service must
#        NOT import hub". hub is excluded from rule (d) by design.
#
# Test asymmetry: rules (b)-(f) exclude *_test.go (required for
# internal/repository/account_backfill_integration_test.go and
# internal/event/nats_integration_test.go); rule (a) includes tests ("no
# package anywhere"). Rule (f) additionally requires its consumers to be
# non-test files: a symbol referenced only by tests is dead in production.
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

# ── rule (g): one gRPC error mapper ─────────────────────────────────────────
scan_grpc_error_mapper() {
  local root f
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r f; do
      case "/$f" in
        */internal/handler/grpc/*) ;;
        *) continue ;;
      esac
      if grep -q "func grpcError" "$f"; then
        echo "VIOLATION: $f defines a local grpcError (use internal/pkg/errors.GRPCError)"
        violations=$((violations + 1))
      fi
    done < <(find "$root" -name '*.go' 2>/dev/null | sort)
  done
}

# ── rule (f): domain exported-symbol liveness ─────────────────────────────
# Enumerate exported top-level identifiers declared in one Go source file
# (read from stdin): type X / var X / func X / single-line const X plus
# members of const ( … ) blocks. Methods are skipped (covered via their
# receiver type); unexported identifiers are skipped. POSIX awk only (mawk
# has no gawk-style match(s, re, arr)).
extract_domain_exports() {
  awk '
    function clean(line) {
      sub(/\/\/.*$/, "", line)
      sub(/\/\*.*\*\//, "", line)
      return line
    }
    {
      line = clean($0)
      if (inblock) {
        if (line ~ /^[[:space:]]*\)/) { inblock = 0; next }
        if (line ~ /^[[:space:]]*$/) next
        if (line ~ /^[[:space:]]*\//) next
        if (match(line, /[A-Za-z_][A-Za-z0-9_]*/)) {
          name = substr(line, RSTART, RLENGTH)
          if (substr(name, 1, 1) ~ /[A-Z]/) print name
        }
        next
      }
      if (line ~ /^(const|type|var)[[:space:]]*\(/) { inblock = 1; next }
      if (match(line, /^(const|type|var|func)[[:space:]]+[A-Za-z_][A-Za-z0-9_]*/)) {
        name = substr(line, RSTART, RLENGTH)
        sub(/^[^[:space:]]+[[:space:]]+/, "", name)
        if (substr(name, 1, 1) ~ /[A-Z]/) print name
      }
    }
  '
}

scan_domain_liveness() {
  local root domain_dir sym corpus symbols
  for root in "$@"; do
    [ -d "$root" ] || continue
    while IFS= read -r domain_dir; do
      # Reference corpus: all non-test .go files under the root except the
      # domain package's own files (in-package use is unqualified and never
      # matches the qualified scan anyway; the exclusion keeps the scan fast).
      corpus="$(mktemp)"
      find "$root" -name '*.go' ! -name '*_test.go' ! -path '*/internal/domain/*' \
        -exec cat {} + 2>/dev/null > "$corpus" || true
      symbols="$(mktemp)"
      while IFS= read -r f; do
        extract_domain_exports < "$f" >> "$symbols"
      done < <(find "$domain_dir" -name '*.go' ! -name '*_test.go' 2>/dev/null | sort)
      while IFS= read -r sym; do
        [ -n "$sym" ] || continue
        if ! grep -q "domain\.$sym\b" "$corpus"; then
          echo "VIOLATION: $domain_dir declares exported $sym with no qualified non-test consumer (rule f)"
          violations=$((violations + 1))
        fi
      done < "$symbols"
      rm -f "$corpus" "$symbols"
    done < <(find "$root" -type d -path '*/internal/domain' 2>/dev/null | sort)
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
  selftest_rule "(f) domain liveness"          scan_domain_liveness
  selftest_rule "(g) single grpc error mapper" scan_grpc_error_mapper
fi

# ── real-tree scan: internal/ and cmd/ only (fixtures are never scanned) ────
violations=0
scan_dead_tree_imports internal cmd
scan_domain_purity internal cmd
scan_repository_service internal cmd
scan_infra_service_repo internal cmd
scan_handler_repo_import internal cmd
scan_domain_liveness internal cmd
scan_grpc_error_mapper internal cmd

if [ "$fail" -ne 0 ] || [ "$violations" -gt 0 ]; then
  echo ""
  [ "$violations" -gt 0 ] && echo "$violations architecture violation(s) detected."
  [ "$fail" -ne 0 ] && echo "check-architecture SELF-TEST FAILED — the gate fixtures or guards are broken."
  exit 1
fi

echo "✓ Architecture check passed (rules a-g: dead-tree ban, domain purity, repository/service, infra layers, handler/repository, domain liveness, single grpc error mapper)"
