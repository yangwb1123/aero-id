#!/usr/bin/env bash
# check-migration-set.sh — guard migration-set integrity.
#
# THE SCRIPT HEADER IS THE CANONICAL RULE DEFINITION (house style);
# AGENTS.md references it. Isolation class: bash + POSIX text tools (grep,
# sed, awk, sort, uniq, comm, tr) only — no compiled toolchain, no container
# runtime, no network, no interpreter, no new binaries. CI-safe (check-gates
# job). Self-testing fixtures run on every invocation (see below).
#
# Canonical validity contract (shared with internal/pkg/migrate Discover):
# a migration filename matches  ^[0-9]{6}_.+\.(up|down)\.sql$ . A file that is
# migration-like (ends with .up.sql/.down.sql case-insensitively, or carries a
# [0-9]{6}_ prefix and a .sql suffix case-insensitively) but does NOT match
# the canonical form is malformed. Anything else is a non-migration file and
# is ignored (README.md, notes.txt, migrations_test.go, dotfiles, dirs).
#
# Rules (each scans one root's migrations/ tree — the real repo or a
# self-test fixture):
#
#   G1 — up/down pair invariant: the set of 6-digit versions present as
#        *.up.sql must equal the set present as *.down.sql (and vice versa).
#        Extraction: grep -oE '^[0-9]{6}' over the file list, sort -u, then
#        comm -3 to list up-only / down-only versions. A version with files
#        on only one side reddens. This is the pin for the AGENTS.md
#        invariant ("Every zero-padded NNNNNN_name.up.sql has a matching
#        .down.sql") that used to be enforced only by the Docker-gated
#        migrations/migrations_test.go.
#   G2 — no duplicate versions: any version appearing in more than one up
#        file, or more than one down file (sort | uniq -d), reddens. This is
#        the pin for the silent map-merge that used to let the second file
#        win in Discover().
#   G3 — no malformed migration-like filenames: every migration-like file
#        that fails the canonical regexp reddens; non-migration files are
#        ignored. The bash predicate mirrors the Go predicate (isMigrationLike
#        in internal/pkg/migrate) exactly, so the two can never diverge.
#
# Self-tests (E8 semantics, run on EVERY invocation): fixture trees under
# scripts/testdata/migration-set/ mirror the real-tree layout. Each positive
# fixture must redden its designated rule AND ONLY it; the negative fixture
# must be clean for all rules; a missing fixture tree fails the gate — the
# fixtures are part of the gate, not decoration. A positive fixture that
# stops matching means the gate went vacuous; a negative fixture that starts
# matching means a rule went over-broad.
#
# Makefile-recipe tripwire: the exact recipe line
#   @bash scripts/check-migration-set.sh
# must exist (line-anchored) in the Makefile; removing the target reddens the
# gate.
#
# Usage: scripts/check-migration-set.sh   (no args; Makefile target)
set -euo pipefail

SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SELF/.." && pwd)"
FIXTURES="$SELF/testdata/migration-set"

CANONICAL_RE='^[0-9]{6}_.+\.(up|down)\.sql$'

violations=0

# ── G1: up/down pair invariant ─────────────────────────────────────────────
rule_g1() {
  local root="$1"
  local dir="$root/migrations"
  local ups downs up_only down_only
  if [ ! -d "$dir" ]; then
    echo "ERROR: G1 $dir missing"
    violations=$((violations + 1))
    return
  fi
  ups="$(ls "$dir" 2>/dev/null | grep -E '\.up\.sql$' | grep -oE '^[0-9]{6}' | sort -u || true)"
  downs="$(ls "$dir" 2>/dev/null | grep -E '\.down\.sql$' | grep -oE '^[0-9]{6}' | sort -u || true)"
  if [ "$ups" != "$downs" ]; then
    up_only="$(comm -23 <(printf '%s\n' "$ups") <(printf '%s\n' "$downs") | tr '\n' ' ')"
    down_only="$(comm -13 <(printf '%s\n' "$ups") <(printf '%s\n' "$downs") | tr '\n' ' ')"
    echo "ERROR: G1 unpaired migration(s) in $dir (up-only: ${up_only:-none}; down-only: ${down_only:-none})"
    violations=$((violations + 1))
  fi
}

# ── G2: no duplicate versions ──────────────────────────────────────────────
rule_g2() {
  local root="$1"
  local dir="$root/migrations"
  local dup
  if [ ! -d "$dir" ]; then
    echo "ERROR: G2 $dir missing"
    violations=$((violations + 1))
    return
  fi
  dup="$(ls "$dir" 2>/dev/null | grep -E '\.up\.sql$' | grep -oE '^[0-9]{6}' | sort | uniq -d || true)"
  if [ -n "$dup" ]; then
    echo "ERROR: G2 duplicate up version(s) in $dir: $(echo "$dup" | tr '\n' ' ')"
    violations=$((violations + 1))
  fi
  dup="$(ls "$dir" 2>/dev/null | grep -E '\.down\.sql$' | grep -oE '^[0-9]{6}' | sort | uniq -d || true)"
  if [ -n "$dup" ]; then
    echo "ERROR: G2 duplicate down version(s) in $dir: $(echo "$dup" | tr '\n' ' ')"
    violations=$((violations + 1))
  fi
}

# ── G3: no malformed migration-like filenames ──────────────────────────────
rule_g3() {
  local root="$1"
  local dir="$root/migrations"
  local f base lower
  if [ ! -d "$dir" ]; then
    echo "ERROR: G3 $dir missing"
    violations=$((violations + 1))
    return
  fi
  for f in "$dir"/*; do
    [ -f "$f" ] || continue
    base="$(basename "$f")"
    [[ "$base" =~ $CANONICAL_RE ]] && continue
    lower="${base,,}"
    if [[ "$lower" == *.up.sql || "$lower" == *.down.sql ]] || { [[ "$base" =~ ^[0-9]{6}_ ]] && [[ "$lower" == *.sql ]]; }; then
      echo "ERROR: G3 malformed migration filename '$base' in $dir (want NNNNNN_name.up.sql / NNNNNN_name.down.sql)"
      violations=$((violations + 1))
    fi
  done
}

# ── self-test sweep: fixtures are part of the gate ─────────────────────────
fail=0
out="$(mktemp)"
if [ ! -d "$FIXTURES/positive" ] || [ ! -d "$FIXTURES/negative" ]; then
  echo "SELF-TEST FAIL: $FIXTURES is missing — the gate fixtures are part of the gate, not decoration"
  fail=1
else
  # Fixture liveness: each designated fixture tree must exist; deleting one
  # reddens the gate (it would otherwise silently shrink the sweep).
  for cls in unpaired duplicate malformed; do
    if [ ! -d "$FIXTURES/positive/$cls" ]; then
      echo "SELF-TEST FAIL: positive fixture $cls missing — the gate fixtures are part of the gate, not decoration"
      fail=1
    fi
  done
  if [ ! -d "$FIXTURES/negative/clean" ]; then
    echo "SELF-TEST FAIL: negative fixture clean missing — the gate fixtures are part of the gate, not decoration"
    fail=1
  fi
  for tree in "$FIXTURES"/positive/*/; do
    [ -d "$tree" ] || continue
    cls="$(basename "$tree")"
    case "$cls" in
      unpaired) want=rule_g1 ;;
      duplicate) want=rule_g2 ;;
      malformed) want=rule_g3 ;;
      *)
        echo "SELF-TEST FAIL: unknown positive fixture $cls"
        fail=1
        continue ;;
    esac
    # designated rule must fire (anti-vacuity)
    violations=0
    "$want" "$tree" >"$out" 2>&1 || true
    if [ "$violations" -lt 1 ]; then
      echo "SELF-TEST FAIL: positive fixture $cls matched nothing for ${want#rule_} — gate is vacuous"
      fail=1
    fi
    # other rules must be silent (fixture isolates its violation class)
    for r in rule_g1 rule_g2 rule_g3; do
      [ "$r" = "$want" ] && continue
      violations=0
      "$r" "$tree" >"$out" 2>&1 || true
      if [ "$violations" -gt 0 ]; then
        echo "SELF-TEST FAIL: positive fixture $cls also trips ${r#rule_} (rule over-breadth?):"
        sed 's/^/    /' "$out"
        fail=1
      fi
    done
  done
  for tree in "$FIXTURES"/negative/*/; do
    [ -d "$tree" ] || continue
    for r in rule_g1 rule_g2 rule_g3; do
      violations=0
      "$r" "$tree" >"$out" 2>&1 || true
      if [ "$violations" -gt 0 ]; then
        echo "SELF-TEST FAIL: negative fixture $(basename "$tree") trips ${r#rule_}:"
        sed 's/^/    /' "$out"
        fail=1
      fi
    done
  done
fi
rm -f "$out"

# ── Makefile-recipe tripwire (mirrors check-audit-swallow.sh) ──────────────
if ! grep -qE '^[[:space:]]*@bash scripts/check-migration-set\.sh[[:space:]]*$' "$SELF/../Makefile"; then
  echo "SELF-TEST FAIL: Makefile check-migration-set recipe line missing (removing the target reddens the gate)"
  fail=1
fi

# ── real-tree scan (fixtures are never scanned here) ───────────────────────
violations=0
rule_g1 "$ROOT"
rule_g2 "$ROOT"
rule_g3 "$ROOT"
real_violations=$violations

if [ "$fail" -ne 0 ] || [ "$real_violations" -gt 0 ]; then
  echo ""
  [ "$real_violations" -gt 0 ] && echo "$real_violations migration-set violation(s) detected."
  [ "$fail" -ne 0 ] && echo "check-migration-set SELF-TEST FAILED — the gate fixtures or guards are broken."
  exit 1
fi

echo "✓ Migration-set check passed (G1: up/down pairs; G2: no duplicate versions; G3: no malformed migration files)"
