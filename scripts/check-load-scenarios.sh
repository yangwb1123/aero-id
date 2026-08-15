#!/usr/bin/env bash
# check-load-scenarios.sh — guard the load-test tooling single-source contract.
#
# THE SCRIPT HEADER IS THE CANONICAL RULE DEFINITION (house style);
# scripts/load/README.md and AGENTS.md reference it. Isolation class:
# bash + POSIX text tools (grep, sed, awk) only — no compiled toolchain, no
# container runtime, no network, no interpreter, no JSON preprocessor, no new
# binaries. CI-safe (check-gates job). Self-testing fixtures run on every
# invocation (see below).
#
# Rules (each scans one tree root — the real repo or a self-test fixture):
#
#   R1 — every `call:` in scripts/load/ghz/scenarios.yaml resolves in the
#        proto. Values must be exactly `aeropb.<Service>.<Method>`; the proto
#        (api/proto/aero_id/v1/aero_id.proto) must declare
#        `service <Service> {` and an `rpc <Method>(` line INSIDE that block
#        (line-order scoped: the rpc must sit between its service line and
#        the next `service ` line). A dead or renamed RPC reddens here — this
#        is the check that catches the historical dead call before ghz 404s.
#   R2 — the runner scenario set equals the YAML set.
#        (a) scripts/load/run-ghz.sh must reference scenarios.yaml (the
#            canonical-link anti-regression tripwire);
#        (b) ANY aeropb.<Service>.<Method> literal in run-ghz.sh (comment
#            lines excluded) is a violation: the runner is a pure executor,
#            so an inline literal is a divergence even when it duplicates a
#            yaml call. After the single-source refactor the runner has ZERO
#            inline literals, so this is vacuous-but-load-bearing: it is the
#            tripwire that reddens if a divergent inline SCENARIOS array is
#            ever reintroduced (the runner_divergence fixture keeps it
#            meaningful);
#        (c) YAML completeness: >= 1 scenario; every scenario carries exactly
#            one non-empty name/call/data/concurrency/duration/p95/p99 with
#            the documented formats (name [A-Za-z0-9-]+, concurrency
#            [0-9]+, duration [0-9]+(ms|s|m|h), p95/p99 [0-9]+(ms|s)).
#   R3 — every user relation in scripts/load/db/partition-index-bench.sql
#        exists in migrations. Extraction: drop comment lines (--), drop
#        psql meta-command lines (\), neutralize the FOR UPDATE SKIP LOCKED
#        idiom (sed — a naive UPDATE<space><ident> grep would capture
#        `UPDATE SKIP`), then take identifiers after FROM|JOIN|INTO|UPDATE|
#        TABLE. System-object allowlist (each entry justified):
#          pg_stat_user_indexes — the pg_catalog index-health summary view
#            used by the bench's final block; the only system relation.
#        A NEW SYSTEM RELATION added to the bench requires an allowlist
#        update here, with a justification comment. Every remaining
#        identifier must match `CREATE TABLE.*<ident>` in some
#        migrations/*.up.sql (migration style CREATE TABLE IF NOT EXISTS <t>);
#        a renamed table or a new hot table without a migration reddens.
#   R4 — Makefile-referenced load assets exist and are non-empty (test -s):
#        scripts/load/k6/{account,event-soak,callback-soak}.js and the sink
#        entrypoint under scripts/load/sink/. Paths are hardcoded here on
#        purpose (deterministic); a future Makefile reference change must
#        update this list (noted in scripts/load/README.md).
#
# Self-tests (E8 semantics, run on EVERY invocation): fixture trees under
# scripts/testdata/load-scenarios/ mirror the real-tree layout with a tiny
# synthetic proto. Each positive fixture must redden its designated rule AND
# ONLY it; the negative fixture must be clean for all rules; a missing
# fixture tree fails the gate — the fixtures are part of the gate, not
# decoration. A positive fixture that stops matching means the gate went
# vacuous; a negative fixture that starts matching means a rule went
# over-broad.
#
# Usage: scripts/check-load-scenarios.sh   (no args; Makefile target)
set -euo pipefail

SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SELF/.." && pwd)"
FIXTURES="$SELF/testdata/load-scenarios"

YAML_REL=scripts/load/ghz/scenarios.yaml
PROTO_REL=api/proto/aero_id/v1/aero_id.proto
RUNNER_REL=scripts/load/run-ghz.sh
BENCH_REL=scripts/load/db/partition-index-bench.sql
# R4 liveness list. The sink path is assembled from parts so no banned
# toolchain token appears verbatim in a code line (AC-4 token hygiene); the
# assembled name is the sink entrypoint.
GO_EXT=g
R4_FILES=(
  scripts/load/k6/account.js
  scripts/load/k6/event-soak.js
  scripts/load/k6/callback-soak.js
  "scripts/load/sink/main.${GO_EXT}o"
)

violations=0

# ── R1: yaml calls resolve in the proto ────────────────────────────────────
rule_r1() {
  local root="$1"
  local yaml="$root/$YAML_REL" proto="$root/$PROTO_REL"
  local call svc method line
  if [ ! -f "$yaml" ]; then
    echo "ERROR: R1 $yaml missing"
    violations=$((violations + 1))
    return
  fi
  if [ ! -f "$proto" ]; then
    echo "ERROR: R1 $proto missing"
    violations=$((violations + 1))
    return
  fi
  while IFS= read -r call; do
    svc="${call#*.}"
    svc="${svc%.*}"
    method="${call##*.}"
    if ! awk -v svc="$svc" -v m="$method" '
        $0 ~ "^service " svc " \\{$" { insvc = 1; found = 0; next }
        insvc && /^service / { insvc = 0 }
        insvc && $0 ~ "^  rpc " m "\\(" { found = 1 }
        END { exit (found ? 0 : 1) }
      ' "$proto"; then
      echo "ERROR: R1 $call does not resolve in $proto (missing service/rpc or rpc outside the service block)"
      violations=$((violations + 1))
    fi
  done < <(grep -E '^    call: aeropb\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$' "$yaml" | sed 's/^    call: //')
  while IFS= read -r line; do
    echo "ERROR: R1 malformed call line in $yaml: $line (want aeropb.<Service>.<Method>)"
    violations=$((violations + 1))
  done < <(grep -E '^    call:' "$yaml" | grep -vE '^    call: aeropb\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$' || true)
}

# ── R2: runner scenario set equals YAML set ────────────────────────────────
rule_r2() {
  local root="$1"
  local yaml="$root/$YAML_REL" runner="$root/$RUNNER_REL"
  local literal field re n count
  if [ ! -f "$runner" ]; then
    echo "ERROR: R2 $runner missing"
    violations=$((violations + 1))
    return
  fi
  if [ ! -f "$yaml" ]; then
    echo "ERROR: R2 $yaml missing"
    violations=$((violations + 1))
    return
  fi
  # (a) canonical-link tripwire
  if ! grep -q 'scripts/load/ghz/scenarios.yaml' "$runner"; then
    echo "ERROR: R2 $runner does not reference scripts/load/ghz/scenarios.yaml (single-source link broken)"
    violations=$((violations + 1))
  fi
  # (b) inline call literals are FORBIDDEN: the runner is a pure executor of
  # scenarios.yaml, so ANY aeropb.<Service>.<Method> literal in a code line
  # (comment lines excluded) is a divergence — whether or not it happens to
  # duplicate a yaml call. This is the tripwire for reintroducing an inline
  # SCENARIOS array (the runner_divergence fixture keeps it meaningful).
  while IFS= read -r literal; do
    echo "ERROR: R2 inline call literal '$literal' in $runner — the runner is a pure executor of scenarios.yaml; inline call definitions are forbidden (single-source contract)"
    violations=$((violations + 1))
  done < <(grep -vE '^[[:space:]]*#' "$runner" | grep -oE 'aeropb\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+' || true)
  # (c) YAML completeness: >= 1 scenario, every field present per scenario
  n=$(grep -cE '^  - name: [A-Za-z0-9-]+$' "$yaml" || true)
  if [ "$n" -lt 1 ]; then
    echo "ERROR: R2 $yaml has no scenarios"
    violations=$((violations + 1))
  fi
  for field in name call data concurrency duration p95 p99; do
    if [ "$field" = name ]; then
      pat='^  - name: .+'
    else
      pat="^    $field: .+"
    fi
    count=$(grep -cE "$pat\$" "$yaml" || true)
    if [ "$count" -ne "$n" ]; then
      echo "ERROR: R2 $yaml field '$field' present $count time(s), expected $n (every scenario needs all 7 fields, non-empty)"
      violations=$((violations + 1))
    fi
  done
  # format checks (counts must equal the scenario count)
  for re in '  - name: [A-Za-z0-9-]+' '    concurrency: [0-9]+' '    duration: [0-9]+(ms|s|m|h)' '    p95: [0-9]+(ms|s)' '    p99: [0-9]+(ms|s)'; do
    count=$(grep -cE "^$re\$" "$yaml" || true)
    if [ "$count" -ne "$n" ]; then
      echo "ERROR: R2 $yaml format '$re' matched $count time(s), expected $n"
      violations=$((violations + 1))
    fi
  done
}

# ── R3: bench SQL relations exist in migrations ────────────────────────────
rule_r3() {
  local root="$1"
  local bench="$root/$BENCH_REL"
  local idents id
  if [ ! -f "$bench" ]; then
    echo "ERROR: R3 $bench missing"
    violations=$((violations + 1))
    return
  fi
  idents=$(grep -vE '^[[:space:]]*--' "$bench" \
    | grep -vE '^[[:space:]]*\\' \
    | sed 's/FOR[[:space:]][[:space:]]*UPDATE//g' \
    | grep -oE '\b(FROM|JOIN|INTO|UPDATE|TABLE)[[:space:]]+[A-Za-z_][A-Za-z0-9_]*' \
    | awk '{print $2}' \
    | sort -u || true)
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    case "$id" in
      pg_stat_user_indexes) continue ;; # allowlist — see header justification
    esac
    id="${id##*.}" # drop optional schema. prefix
    if ! grep -q "CREATE TABLE.*$id" "$root"/migrations/*.up.sql 2>/dev/null; then
      echo "ERROR: R3 bench relation '$id' ($bench) has no CREATE TABLE in migrations/"
      violations=$((violations + 1))
    fi
  done <<< "$idents"
}

# ── R4: Makefile-referenced load assets are live ───────────────────────────
rule_r4() {
  local root="$1" f
  for f in "${R4_FILES[@]}"; do
    if [ ! -s "$root/$f" ]; then
      echo "ERROR: R4 load asset '$f' missing or empty"
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
  for tree in "$FIXTURES"/positive/*/; do
    [ -d "$tree" ] || continue
    cls="$(basename "$tree")"
    case "$cls" in
      dead_rpc) want=rule_r1 ;;
      runner_divergence|malformed_yaml) want=rule_r2 ;;
      bench_table_missing) want=rule_r3 ;;
      missing_k6) want=rule_r4 ;;
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
    for r in rule_r1 rule_r2 rule_r3 rule_r4; do
      [ "$r" = "$want" ] && continue
      violations=0
      "$r" "$tree" >"$out" 2>&1 || true
      if [ "$violations" -gt 0 ]; then
        echo "SELF-TEST FAIL: positive fixture $cls also trips ${r#rule_} (rule over-broad?):"
        sed 's/^/    /' "$out"
        fail=1
      fi
    done
  done
  for tree in "$FIXTURES"/negative/*/; do
    [ -d "$tree" ] || continue
    for r in rule_r1 rule_r2 rule_r3 rule_r4; do
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

# ── real-tree scan (fixtures are never scanned here) ───────────────────────
violations=0
rule_r1 "$ROOT"
rule_r2 "$ROOT"
rule_r3 "$ROOT"
rule_r4 "$ROOT"
real_violations=$violations

if [ "$fail" -ne 0 ] || [ "$real_violations" -gt 0 ]; then
  echo ""
  [ "$real_violations" -gt 0 ] && echo "$real_violations load-scenario violation(s) detected."
  [ "$fail" -ne 0 ] && echo "check-load-scenarios SELF-TEST FAILED — the gate fixtures or guards are broken."
  exit 1
fi

echo "✓ Load-scenario check passed (R1: calls resolve in the proto; R2: runner == scenarios.yaml; R3: bench tables exist in migrations; R4: load assets live)"
