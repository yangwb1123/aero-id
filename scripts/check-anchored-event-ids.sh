#!/usr/bin/env bash
# check-anchored-event-ids.sh — R-1/R-2/R-3 regression gate for the
# deterministic event-ID derivation contract (design:
# derive-stable-deterministic-event-id-from-an-anc, §9 R-1/R-2, §10; golden
# coverage close: R-3/R-5). Every audit event_id must be either an
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
# R-3: shape coverage — every live idgen.Anchor call whose leading arguments
#   are string literals must resolve to a golden pin in TestAnchor_
#   GoldenConstants (the ANCHOR_MANIFEST below mirrors the golden case set).
#   The leading literal run is joined with '.' ("audit" + "region.promote"
#   -> audit.region.promote); first-argument-only extraction would yield the
#   vacuous single "audit" for exactly the shapes this gate exists to cover.
#   Dynamic-first-arg sites (operation_audit.go passes its action at runtime;
#   G1-G7) are not extracted.
# R-5: the namespace byte literal must survive in internal/pkg/idgen/idgen.go
#   exactly once — a reassignment of AnchorNamespace to a hex-literal
#   uuid.UUID{0x38, 0x24, ...} form would dodge both the MustParse grep and
#   the var-count check, silently invalidating every historical derived
#   event_id across a rollout.
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

# R-3 golden manifest: the joined leading-literal discriminators that every
# live idgen.Anchor call must satisfy, mirroring the golden case set in
# internal/pkg/idgen/idgen_test.go:
#   audit.region.promote  (G14/G14b/G14c)  audit.preference.set (G15)
#   audit_hold.set        (G9/G9b)         audit_hold.removed  (G10)
#   legacy.cutover.set    (G11-G13)        sync.job.finished   (G8)
# The operation.* family (G1-G7) is documented only: the live operation site
# passes its action dynamically, so those discriminators are never extracted.
ANCHOR_MANIFEST="audit.region.promote audit.preference.set audit_hold.set audit_hold.removed legacy.cutover.set sync.job.finished projection.replication.quarantine.v1"

# extract_anchors <root> — emit one joined leading-literal discriminator per
# line for every idgen.Anchor("lit", ...) call in the root, joined from the
# leading run of string-literal arguments (stopping at the first non-literal
# argument). Excludes _test.go, *.pb.go, and the definition file (R-2 file
# exclusion precedent). Dynamic-first-arg calls are not matched by the regex.
extract_anchors() {
  local root="$1"
  grep -rno 'idgen\.Anchor("[^"]*"\(, *"[^"]*"\)*' "$root" --include='*.go' 2>/dev/null \
    | grep -v '_test.go' | grep -v '\.pb\.go' | grep -v "^$IDGEN_FILE:" \
    | sed -E 's/^[^:]+:[0-9]+://' \
    | sed 's/^idgen\.Anchor(//' \
    | while IFS= read -r call; do
        printf '%s' "$call" | grep -o '"[^"]*"' | tr -d '"' | paste -sd. -
      done
}

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

  # R-5: the namespace byte literal must occur exactly once in the real file
  # (occurrence count, not line count). A hex-literal uuid.UUID{0x38, 0x24,
  # ...} reassignment deletes the literal, drops the count to 0, and reddens
  # the gate — closing the evasion path where neither the MustParse grep nor
  # the var-count check fires.
  ns_lit="$(grep -o "38248e3a-32f0-4929-9b56-b2dbd75aa849" "$SELF_DIR/../internal/pkg/idgen/idgen.go" 2>/dev/null | wc -l | tr -d ' ')"
  if [ "$ns_lit" != "1" ]; then
    violations="${violations}R-2 AnchorNamespace byte literal occurrences = ${ns_lit}, want exactly 1 (a hex-literal reassignment would dodge the MustParse grep)
"
  fi

  # R-3: shape coverage — every extracted discriminator must have a golden
  # pin. A shape with no pin means a new (or drifted) derivation contract
  # that the golden set does not lock byte-for-byte.
  local d r3_missing=""
  while IFS= read -r d; do
    [ -z "$d" ] && continue
    case " $ANCHOR_MANIFEST " in
      *" $d "*) ;;
      *) r3_missing="${r3_missing}  $d
" ;;
    esac
  done < <(extract_anchors "$root")
  if [ -n "$r3_missing" ]; then
    violations="${violations}R-3 no golden pin for shape(s) (add a case to TestAnchor_GoldenConstants in internal/pkg/idgen/idgen_test.go):
$r3_missing"
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

# ── self-test (R-3): an anchor shape with no golden pin must redden, and the
# ── reddening must be attributable to R-3. The r3_positive fixture contains
# ── ONLY the unpinned anchor, so no R-1/R-2 pattern can mask the failure.
if scan_root "$FIXTURES/r3_positive" 0 >/dev/null 2>&1; then
  echo "SELF-TEST FAIL: r3_positive fixture under $FIXTURES/r3_positive matched nothing"
  fail=1
fi
if ! scan_root "$FIXTURES/r3_positive" 0 2>&1 | grep -q '^R-3 '; then
  echo "SELF-TEST FAIL: r3_positive fixture reddened without an R-3 message"
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

echo "✓ check-anchored-event-ids: no random audit EventID minting; all anchor shapes golden-pinned (C2 allowlist ok, self-test ok)"
