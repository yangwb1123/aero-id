#!/usr/bin/env bash
# check-proto-sync.sh — deterministic proto-stub sync gate.
#
# Regenerates every committed *.pb.go from api/proto/**/*.proto with a PINNED
# toolchain and fails (exit 1) if the committed stubs are not byte-identical.
# The repository is never mutated: all writes go to mktemp trees removed by an
# EXIT trap.
#
# Pins (never ambient PATH — ~/go/bin/protoc-gen-go has already drifted
# v1.34.1 -> v1.36.11 between analysis and implementation):
#   - protoc-gen-go      : derived from the repository's go.mod
#                          (google.golang.org/protobuf vX.Y.Z) — single source
#                          of truth
#   - protoc-gen-go-grpc : v1.5.1, hardcoded constant + header cross-check
#                          (no go.mod anchor exists for the plugin tool)
#   - protoc             : 3.21.12, asserted via `protoc --version`
#                          (host prerequisite; Debian: protobuf-compiler)
#   - go toolchain       : GOTOOLCHAIN=local on both `go install` calls — a
#                          PR-controllable go.mod `go`/`toolchain` directive
#                          can never trigger an alternate toolchain download
#
# Fail-guard discipline (deliberate `set -u` WITHOUT `set -e`): every command
# whose failure must fail the gate is followed by `|| fail ...`; pipelines
# either exit with the semantically meaningful member's status or are guarded
# at the consuming side; `cp -r`/`sed -i` in the red self-test are guarded AND
# their effect is re-verified (a silently failing cp/sed would otherwise make
# the red fixture pass vacuously = false green).
#
# Self-tests (precedent: scripts/check-audit-swallow.sh): every invocation
# proves sensitivity (green fixture -> 0) and permissiveness (derived red
# fixture -> 1). The red fixture is created in a temp dir; the repository is
# never mutated. Self-tests run after the real check passes (fast-fail).
#
# Exit-code/stdout contract (CI-consumable):
#   exit 0  success; stdout carries exactly ONE verdict line.
#   exit 1  any drift/toolchain/self-test failure; all diagnostics on stderr.
#   No other exit codes are produced.
#
# Usage: scripts/check-proto-sync.sh [ROOT]
#   ROOT defaults to the repository root. Proto/stub paths resolve under ROOT;
#   the version pins resolve from the repository's go.mod.

set -u

# All temp-dir variables are pre-initialized so the EXIT trap never sees an
# unbound variable: under `set -u` bash aborts the trap's command line before
# rm runs, leaking the directories.
GOBIN_DIR=""
OUT_DIR=""
TMP_DIR=""

PROTOC_GO_GRPC_PIN="v1.5.1"   # == every committed *_grpc.pb.go header
PROTOC_PIN="3.21.12"          # == every committed *.pb.go header

# MUST mirror the Makefile `proto:` target enumeration (Makefile lines
# 113-117). The Makefile-parity guard below enforces this mechanically.
PROTOS=(
  sso/v1/sso.proto
  aero_id/v1/aero_id.proto
  snaplink/authz/v1/authz.proto
  snaplink/audit/v1/audit.proto
  aero_id/v1/event.proto
)
STUB_DIRS=(
  internal/sso/ssopb
  internal/sso/authzpb
  internal/sso/auditpb
  internal/handler/grpc/aeropb
)

fail() { echo "ERROR: $*" >&2; exit 1; }

# Repository root, canonicalized to the physical path (`pwd -P`) so every
# path below is CWD-independent. An unresolvable script location must fail
# the gate, not yield an empty root.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)" \
  || fail "cannot resolve repository root from ${BASH_SOURCE[0]}"

# ROOT is canonicalized the same way: a relative, trailing-slash, or symlinked
# ROOT must not defeat the `[ "$root" = "$REPO_ROOT" ]` Makefile-parity guard
# through string inequality.
ROOT="${1:-$REPO_ROOT}"
ROOT="$(cd -P -- "$ROOT" && pwd -P)" || fail "cannot resolve ROOT: ${1:-<default>}"

# protoc-gen-go pin from the repository go.mod (tab-indented require block).
# The pipeline's exit status is head's, so a sed failure alone would be
# masked; the go.mod existence guard plus the non-empty result check below
# make the derivation fail-closed regardless.
derive_go_pin() {
  sed -n 's/^[[:space:]]*google\.golang\.org\/protobuf v\([0-9][0-9.]*\).*$/\1/p' \
    "$REPO_ROOT/go.mod" | head -n 1
}

# Actionable pre-flight: committed stub headers must name the pins we will
# install. Turns a wrong-version regeneration into a clear error instead of a
# confusing diff. Every grep is guarded: a missing stub dir yields grep exit
# 2 -> fail, never a silent pass.
header_cross_check() {
  local go_pin="$1" root="$2" d
  for d in "${STUB_DIRS[@]}"; do
    grep -rqF "protoc-gen-go v$go_pin" "$root/$d" \
      || fail "stubs under $root/$d not generated with protoc-gen-go v$go_pin (go.mod pin); align go.mod + committed stubs, or the gate is pinned wrong"
    grep -rqF "protoc-gen-go-grpc $PROTOC_GO_GRPC_PIN" "$root/$d" \
      || fail "stubs under $root/$d not generated with protoc-gen-go-grpc $PROTOC_GO_GRPC_PIN (script pin); regenerate with make proto using that plugin, or update the pin"
  done
}

# One full pipeline run against a ROOT. Exit 0 = byte-identical, 1 = drift.
run_check() {
  local root="$1" go_pin rc diff_out diff_rc d p generated rel f
  [ -f "$REPO_ROOT/go.mod" ] || fail "no go.mod at repository root: $REPO_ROOT"
  [ -d "$root/api/proto" ] || fail "no api/proto under $root"

  go_pin="$(derive_go_pin)"
  [ -n "$go_pin" ] || fail "cannot derive protoc-gen-go pin: no 'google.golang.org/protobuf vX.Y.Z' require in $REPO_ROOT/go.mod"

  header_cross_check "$go_pin" "$root"

  # Makefile-parity guard (real repo only; fixture has no Makefile). A
  # missing Makefile at the real root is itself a gate failure, not a skip.
  if [ "$root" = "$REPO_ROOT" ]; then
    [ -f "$REPO_ROOT/Makefile" ] || fail "Makefile missing at $REPO_ROOT (parity guard needs it)"
    for p in "${PROTOS[@]}"; do
      grep -qF "api/proto/$p" "$REPO_ROOT/Makefile" \
        || fail "proto $p missing from the Makefile proto target — keep PROTOS and Makefile in lockstep"
    done
  fi

  # Install pinned plugins into a temp GOBIN exactly once per invocation;
  # ambient plugin binaries are never consulted (PATH prefix below).
  if [ -z "$GOBIN_DIR" ]; then
    GOBIN_DIR="$(mktemp -d)" || fail "mktemp failed for GOBIN"
    trap 'rm -rf "${GOBIN_DIR:-}" "${OUT_DIR:-}" "${TMP_DIR:-}"' EXIT
    echo "installing protoc-gen-go@v$go_pin + protoc-gen-go-grpc@$PROTOC_GO_GRPC_PIN (temp GOBIN)" >&2
    # GOTOOLCHAIN=local on BOTH installs: go.mod `go`/`toolchain` directives are
    # PR-controllable, and without it the go command downloads and executes a
    # PR-selected alternate toolchain (supply-chain finding F1). `local` pins
    # every go invocation to the toolchain that runs the gate — an alternate
    # toolchain download is never triggered.
    GOFLAGS= GOWORK=off GOTOOLCHAIN=local GOBIN="$GOBIN_DIR" \
      go install "google.golang.org/protobuf/cmd/protoc-gen-go@v$go_pin" \
      || fail "go install protoc-gen-go@v$go_pin failed (network or nonexistent version?)"
    GOFLAGS= GOWORK=off GOTOOLCHAIN=local GOBIN="$GOBIN_DIR" \
      go install "google.golang.org/grpc/cmd/protoc-gen-go-grpc@$PROTOC_GO_GRPC_PIN" \
      || fail "go install protoc-gen-go-grpc@$PROTOC_GO_GRPC_PIN failed (network or nonexistent version?)"
    # The PATH prefix below only protects against ambient binaries if the
    # pinned binaries actually landed in GOBIN_DIR.
    [ -x "$GOBIN_DIR/protoc-gen-go" ] || fail "protoc-gen-go missing from $GOBIN_DIR after go install"
    [ -x "$GOBIN_DIR/protoc-gen-go-grpc" ] || fail "protoc-gen-go-grpc missing from $GOBIN_DIR after go install"
  fi

  # protoc is a host tool (not installable via go); pin it by assertion.
  # Exact-string comparison after command substitution strips the trailing
  # newline; `grep -qE "...$"` would be equivalent but a substring match like
  # "libprotoc 3.21.123" must never pass.
  command -v protoc >/dev/null 2>&1 \
    || fail "protoc not found on PATH — install protoc $PROTOC_PIN (Debian: protobuf-compiler)"
  protoc_version="$(protoc --version 2>&1)" || fail "cannot determine protoc version (protoc --version failed)"
  [ "$protoc_version" = "libprotoc $PROTOC_PIN" ] \
    || fail "protoc version mismatch: got '$protoc_version', want 'libprotoc $PROTOC_PIN'; every committed stub header says $PROTOC_PIN — install that version (a different protoc produces false diffs)"

  # Regenerate all 5 protos into a temp tree with the exact Makefile flags.
  # OUT_DIR is created once per invocation and reused across the 3 pipeline
  # runs; its contents are reset before every regen so stale output from an
  # earlier run can never be diffed (defense-in-depth: every run currently
  # regenerates the identical full set, but a future subset-regen must not
  # inherit stale files).
  if [ -z "$OUT_DIR" ]; then
    OUT_DIR="$(mktemp -d)" || fail "mktemp failed for output"
  fi
  rm -rf "$OUT_DIR" && mkdir -p "$OUT_DIR" || fail "cannot reset output dir $OUT_DIR"

  # protoc args are passed via an array: an unquoted $(for ...) substitution
  # would word-split paths containing spaces and glob-expand metacharacters.
  local proto_args=()
  for p in "${PROTOS[@]}"; do
    proto_args+=("$root/api/proto/$p")
  done
  PATH="$GOBIN_DIR:$PATH" protoc \
    --proto_path="$root/api/proto" \
    --go_out="$OUT_DIR" --go_opt=module=github.com/aero/aero-id \
    --go-grpc_out="$OUT_DIR" --go-grpc_opt=module=github.com/aero/aero-id \
    "${proto_args[@]}" \
    || fail "protoc regeneration failed for $root"

  # Diff every stub dir. Any difference — including files present on only one
  # side (e.g. a new proto committed without stubs) — is drift. `diff -rq`
  # exits 0 = identical, 1 = differences, 2 = error (e.g. missing dir); both
  # non-zero codes fail the gate, labeled so the failure is actionable.
  rc=0
  for d in "${STUB_DIRS[@]}"; do
    diff_out="$(diff -rq "$OUT_DIR/$d" "$root/$d" 2>&1)"
    diff_rc=$?
    if [ "$diff_rc" -ne 0 ]; then
      if [ "$diff_rc" -eq 2 ]; then
        echo "PROTO SYNC CHECK ERROR (diff exit 2) in $root/$d:" >&2
      else
        echo "PROTO SYNC DRIFT in $root/$d:" >&2
      fi
      echo "$diff_out" >&2
      rc=1
    fi
  done

  # Total-coverage sweep: every file the regeneration produced must exist and
  # byte-match under $root — even when a new proto's go_package targets a
  # brand-new stub dir outside STUB_DIRS (a new proto committed without stubs
  # would otherwise be invisible to the per-dir diffs above). find's failure
  # is guarded: an empty/incomplete enumeration must fail, never pass.
  generated="$(find "$OUT_DIR" -type f 2>/dev/null)" \
    || { echo "PROTO SYNC CHECK ERROR: cannot enumerate $OUT_DIR (find failed)" >&2; rc=1; }
  if [ -n "$generated" ]; then
    while IFS= read -r f; do
      rel="${f#"$OUT_DIR"/}"
      diff_out="$(diff -q "$f" "$root/$rel" 2>&1)"
      diff_rc=$?
      if [ "$diff_rc" -ne 0 ]; then
        echo "PROTO SYNC DRIFT: generated $rel != committed (new proto without regenerated stubs?):" >&2
        echo "$diff_out" >&2
        rc=1
      fi
    done <<< "$generated"
  fi
  if [ "$rc" -eq 0 ]; then
    echo "OK: stubs under $root match pinned regeneration" >&2
    return 0
  fi
  return 1
}

# ── self-tests: sensitivity (green -> 0) and permissiveness (red -> 1) ──────
run_self_tests() {
  local fixture="$REPO_ROOT/scripts/testdata/proto-sync/green" red rc=0 selftest_out marker
  [ -d "$fixture" ] || { echo "SELF-TEST FAIL: fixture $fixture missing" >&2; return 1; }

  # Captured (not swallowed) so a failure is diagnosable in CI output.
  if ! selftest_out="$(run_check "$fixture" 2>&1)"; then
    echo "SELF-TEST FAIL: green fixture did not pass (fixture stale or gate broken):" >&2
    echo "$selftest_out" >&2
    rc=1
  fi

  # Red fixture: temp copy + one deterministic comment injection after the
  # header block. Byte-difference by construction. cp/sed failures here would
  # otherwise make the red fixture pass vacuously (false green), so both are
  # guarded AND the injection is re-verified below.
  TMP_DIR="$(mktemp -d)" || fail "mktemp failed for red fixture"
  red="$TMP_DIR/red"
  cp -r "$fixture" "$red" || fail "cannot copy fixture to $red (cp -r failed)"
  marker='// proto-sync self-test: hand-edit marker'
  # POSIX sed append form (`2a\` + newline + text) — accepted by GNU and BSD
  # sed. A sed that does not apply the script (BSD `-i` suffix handling, a
  # broken build, a missing file) leaves the file unmodified, often exiting
  # 0 — the grep re-verification below is the actual fail-closed guard: an
  # unmodified red fixture is a hard error, never a pass.
  sed -i $'2a\\\n'"$marker" "$red/internal/sso/ssopb/sso.pb.go" \
    || fail "cannot inject hand-edit marker into $red/internal/sso/ssopb/sso.pb.go"
  grep -qF "$marker" "$red/internal/sso/ssopb/sso.pb.go" \
    || fail "hand-edit marker injection not verified in red fixture — aborting (sed portability/behavior guard)"

  if selftest_out="$(run_check "$red" 2>&1)"; then
    echo "SELF-TEST FAIL: red fixture passed — gate lost sensitivity:" >&2
    echo "$selftest_out" >&2
    rc=1
  fi
  return $rc
}

run_check "$ROOT" || exit 1
run_self_tests || exit 1
echo "✓ check-proto-sync passed (real tree + green + red self-tests)"
