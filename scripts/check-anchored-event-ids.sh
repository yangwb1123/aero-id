#!/usr/bin/env bash
# check-anchored-event-ids.sh — R-1/R-2 regression gate for the deterministic
# event-ID derivation contract (design: derive-stable-deterministic-event-id-
# from-an-anc, §9 R-1/R-2, §10). Every audit event_id must be either an
# already-deterministic v7 receipt (operation.requested, sync.job.requested,
# callback create/delete, account provision) or an idgen.Anchor-derived v5;
# random minting is a programming error except at the C2 replay site.
#
# R-1: `EventID: idgen.NewID()` is banned in non-test Go except the C2
#   allowlist: the historical replay sites (internal/service/callback.go
#   ReplayDelivery, internal/service/audit_fact.go buildLegacyAuditFact) plus
#   the dead-letter replay surface, where the repository mints a fresh v7
#   EventID per (run, leg, tenant) inside the requeue transaction
#   (internal/repository/audit_publication_replay.go,
#   internal/repository/callback_audit.go) — the eight unanchored
#   legacy-Log sites (admin/ban/block/file-share) mint a fresh v7 per call by
#   contract (B2-1 REQ-3.3: a stable reused ID across distinct intents would
#   let ErrAuditDuplicate roll back a legitimate domain write).
# R-2: `idgen.AnchorNamespace` is defined exactly once; `uuid.NewSHA1` /
#   `uuid.NewHash` and var-level UUID namespace constants are banned outside
#   internal/pkg/idgen + internal/service/projection_replication.go (the
#   projection precedent); raw digest-to-UUID casts and zero-part `Anchor()`
#   are banned (a v5 result must be built by the RFC 4122 encoding, and every
#   anchor site has fixed arity >= 4).
#
# Self-tests against scripts/testdata/anchored-event-ids/ before scanning;
# ROOT parameterized for the self-test fixture (precedent: check-audit-swallow).

set -u

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES="$SELF_DIR/testdata/anchored-event-ids"

# Files exempt from R-1 (C2 allowlist) and R-2 derivation-source bans.
# The C2 fresh-v7 mint sites: the two service replay sites (ReplayDelivery,
# buildLegacyAuditFact) and the dead-letter replay surface — the repository
# mints a fresh v7 EventID per (run, leg, tenant) inside the requeue
# transaction (audit_publication_replay.go / callback_audit.go), the same
# fresh-intent-per-replay class as the historical sites. Every other mint is
# policed.
C2_ALLOWLIST="internal/service/callback.go|internal/service/audit_fact.go|internal/repository/audit_publication_replay.go|internal/repository/callback_audit.go"
IDGEN_FILE="internal/pkg/idgen/idgen.go"
PROJECTION_FILE="internal/service/projection_replication.go"

# scan_root <root> <allow-callback>
# Returns 0 when the root is clean, 1 with a message otherwise.
scan_root() {
  local root="$1" allow="$2"
  local violations=""

  local r1
  r1="$(grep -rn "EventID: idgen.NewID()" "$root" --include='*.go' 2>/dev/null \
    | grep -v '_test.go' | grep -v '\.pb\.go')"
  if [ -n "$r1" ] && [ "$allow" = "1" ]; then
    r1="$(printf '%s\n' "$r1" | grep -vE "^($C2_ALLOWLIST):" || true)"
  fi
  [ -n "$r1" ] && violations="${violations}R-1 EventID: idgen.NewID() outside the C2 allowlist:
$r1
"

  local r2
  r2="$(grep -rn "uuid.NewSHA1(\|uuid.NewHash(" "$root" --include='*.go' 2>/dev/null \
    | grep -v '_test.go' | grep -v "^$IDGEN_FILE:" | grep -v "^$PROJECTION_FILE:" || true)"
  [ -n "$r2" ] && violations="${violations}R-2 uuid.NewSHA1/NewHash outside idgen + projection_replication.go:
$r2
"

  local r3
  r3="$(grep -rn "uuid.UUID(sha1\|uuid.UUID(sum\[\|uuid.UUID(digest" "$root" --include='*.go' 2>/dev/null \
    | grep -v '_test.go')"
  [ -n "$r3" ] && violations="${violations}R-2 raw SHA-1 digest cast — the v5 encoding must set version/variant bits:
$r3
"

  local r4
  r4="$(grep -rn "Anchor()" "$root" --include='*.go' 2>/dev/null \
    | grep -v '_test.go' | grep -v "^$IDGEN_FILE:")"
  [ -n "$r4" ] && violations="${violations}R-2 zero-part Anchor() call (every site has fixed arity >= 4):
$r4
"

  local ns_defs ns_count
  ns_defs="$(grep -rn "= uuid.MustParse(" "$root" --include='*.go' 2>/dev/null \
    | grep -v '_test.go' | grep -v "^$IDGEN_FILE:" | grep -v "^$PROJECTION_FILE:" || true)"
  [ -n "$ns_defs" ] && violations="${violations}R-2 UUID namespace constant outside idgen + projection_replication.go:
$ns_defs
"

  # AnchorNamespace lives in internal/pkg/idgen/idgen.go; the check pins the
  # real file regardless of which roots are scanned (a "cmd" root alone would
  # count 0 and false-fail).
  ns_count="$(grep -c "var AnchorNamespace" "$SELF_DIR/../internal/pkg/idgen/idgen.go" 2>/dev/null || true)"
  if [ "$ns_count" != "1" ]; then
    violations="${violations}R-2 AnchorNamespace definitions = ${ns_count}, want exactly 1
"
  fi

  if [ -n "$violations" ]; then
    printf '%s' "$violations"
    return 1
  fi
  return 0
}

fail=0

# ── self-test: the gate must flag its own positive fixtures ──────────────
if scan_root "$FIXTURES/positive" 0 >/dev/null 2>&1; then
  echo "SELF-TEST FAIL: positive fixture under $FIXTURES/positive matched nothing"
  fail=1
fi

# ── self-test (I6): a cmd-root with the C2 allow-list ENABLED must still be
# ── scanned and flagged — green must mean "scanned and allowed", never
# ── "never scanned". The alternation exempts only the exact allow-listed
# ── files, not every file under a cmd/ root. ────────────────────────────────
if scan_root "$FIXTURES/cmd_positive/cmd" 1 >/dev/null 2>&1; then
  echo "SELF-TEST FAIL: cmd fixture under $FIXTURES/cmd_positive/cmd matched nothing with allow=1 (cmd/ would be unscanned)"
  fail=1
fi

# ── self-test: the gate must accept its own negative fixtures ────────────
if ! scan_root "$FIXTURES/negative" 0 >/dev/null 2>&1; then
  echo "SELF-TEST FAIL: negative fixture matched:"
  scan_root "$FIXTURES/negative" 0 || true
  fail=1
fi

# ── scan the live tree (C2 allowlist applies). Every root argument is
# ── scanned (Makefile passes "internal cmd"); a root that is never scanned
# ── would make green mean "never scanned" instead of "scanned and allowed"
# ── (I6 doctrine). Defaults to internal when no root is given.
if [ "$#" -eq 0 ]; then
  set -- "internal"
fi
for root in "$@"; do
  if ! scan_root "$root" 1; then
    echo ""
    echo "check-anchored-event-ids FAILED — audit event_ids must be deterministic (or C2)."
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo ""
  echo "check-anchored-event-ids FAILED."
  exit 1
fi

echo "✓ check-anchored-event-ids: no random audit EventID minting (C2 allowlist ok, self-test ok)"
