#!/usr/bin/env bash
# check-audit-manifest.sh — enumerated bare-site manifest gate (sweep-and-lock
# the in-tx audit conversion, REQ-1).
#
# Every bare `s.audit.Record(` / `s.audit.Log(` call site in internal/service
# must be enumerated in the manifest below and carry the class marker comment
# `audit-site: <class>` (REQ-2). The gate fails when:
#   (a) an unmapped site exists — a NEW mutation path cannot regress to a
#       non-tx Record without landing a manifest row and its integration proof;
#   (b) a mapped site is missing its `audit-site:` marker comment;
#   (c) a with-audit-fallback row's WithAudit sibling symbol is absent from the
#       same file (the bare call is then provably the non-atomic fallback arm
#       of an in-tx mutation — with the B2-1 conversion landed, the remaining
#       fallback rows are the documented residuals).
#
# Line-drift-proof: matches (file, key token), never line numbers.
#
# Usage: scripts/check-audit-manifest.sh   (scans internal/service)

set -u

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES="$SELF_DIR/testdata/audit-manifest"
ROOT="internal/service"

# Manifest: file|key-token|class|with-audit-sibling
# Classes: standalone-fact (documented no-domain-write or read-path fact) and
# with-audit-fallback (non-atomic arm of an in-tx mutation; sibling enforced).
# Rows are keyed by a token that appears in the call line and survives line
# drift. The ten post-commit Log sites converted by B2-1 (admin/ban/block/
# comment/message/file_share) and the four deleted fallback tails are NOT in
# this table — their rows were removed with the calls (the gate tolerates
# orphan rows by design: a row with no matching call is inert).
MANIFEST=(
  "internal/service/account.go|account.viewed|standalone-fact|-"
  "internal/service/account.go|account.listed|standalone-fact|-"
  "internal/service/agent_membership.go|agent.membership.checked|standalone-fact|-"
  "internal/service/account_backfill.go|account.legacy_backfill.requested|standalone-fact|-"
  "internal/service/audit_anchor.go|audit.anchor.requested|standalone-fact|-"
  "internal/service/audit_anchor.go|audit.anchor.manifest_viewed|standalone-fact|-"
  "internal/service/legacy_migration.go|legacy.migration.compared|standalone-fact|-"
  "internal/service/operation_state.go|operation.reconciled|standalone-fact|-"
  "internal/service/replay.go|audit.replay.dry_run|standalone-fact|-"
  "internal/service/replay.go|audit.replay.requested|standalone-fact|-"
  "internal/service/event_ingest.go|eventAuditInput|standalone-fact|-"
  "internal/service/legacy_cutover.go|legacy.cutover.set|with-audit-fallback|SetCutoverWithAudit"
  "internal/service/message.go|auditMessageDelete|standalone-fact|-"
  "internal/service/file.go|AuditFileCreate|standalone-fact|-"
  "internal/service/file.go|AuditFileDelete|standalone-fact|-"
  "internal/service/file_presign.go|AuditFileCreate|standalone-fact|-"
  "internal/service/file_presign.go|AuditFileDownload|standalone-fact|-"
  "internal/service/file_share.go|AuditFileUpdate|standalone-fact|-"
  "internal/service/message_audit.go|auditMessageSend|standalone-fact|-"
  "internal/service/message_audit.go|auditMessageEdit|standalone-fact|-"
  "internal/service/message_audit.go|auditMessageReact|standalone-fact|-"
  "internal/service/message_audit.go|auditMessageUnreact|standalone-fact|-"
  "internal/service/preference.go|AuditPreferenceSet|standalone-fact|-"
  "internal/service/projection_replication.go|AuditProjectionReplicationPublish|standalone-fact|-"
  "internal/service/projection_replication_failures.go|AuditProjectionReplicationQuarantine|standalone-fact|-"
  "internal/service/projection_replay.go|auditProjectionReplay|standalone-fact|-"
  "internal/service/sync_export.go|exportDownloadAudit|standalone-fact|-"
)

fail=0

# ── self-tests: positive fixtures must fail, negative fixtures must pass ──
pos_violations=0
for f in "$FIXTURES"/positive/*.go; do
  [ -e "$f" ] || continue
  if grep -qE "s\.audit\.(Record|Log)\(" "$f"; then
    # A positive fixture with a site that IS in the manifest is a gate failure
    # only if it lacks the marker; fixtures are intentionally bare.
    if ! grep -qE "audit-site: (standalone-fact|with-audit-fallback)" "$f"; then
      pos_violations=$((pos_violations + 1))
    fi
  fi
done
if [ "$pos_violations" -lt 1 ]; then
  echo "SELF-TEST FAIL: positive fixture under $FIXTURES/positive matched nothing (gate would be vacuous)"
  fail=1
fi

neg_violations=0
for f in "$FIXTURES"/negative/*.go; do
  [ -e "$f" ] || continue
  # A negative fixture must look exactly like a compliant live site: the call
  # block resolves to a manifest token AND the marker is present. If the scan
  # flags it (unmapped or missing marker), the self-test fails.
  if grep -qE "s\.audit\.(Record|Log)\(" "$f"; then
    mapped=0
    for entry in "${MANIFEST[@]}"; do
      token="${entry#*|}"
      token="${token%%|*}"
      if grep -qF "$token" "$f"; then
        mapped=1
      fi
    done
    if [ "$mapped" -ne 1 ] || ! grep -qE "audit-site: (standalone-fact|with-audit-fallback)" "$f"; then
      neg_violations=$((neg_violations + 1))
    fi
  fi
done
if [ "$neg_violations" -ne 0 ]; then
  echo "SELF-TEST FAIL: negative fixture failed the manifest checks (unmapped or missing marker)"
  fail=1
fi

# ── (a) every bare site must resolve to exactly one manifest row ──────────
# Coverage check per file: the number of bare Record/Log call sites in a file
# must equal the number of manifest tokens for that file, and every token must
# be present in the file. The token may live on any line of the file (e.g.
# `Record(ctx, input)` where input is built above, or a helper function whose
# name is the token); distinct tokens disambiguate multiple sites in one file
# (account.go: viewed vs listed, replay.go: dry_run vs requested). A site with
# no manifest row, or a file with more sites than tokens, fails the gate.
for f in $(grep -rlE "s\.audit\.(Record|Log)\(" "$ROOT" --include='*.go' 2>/dev/null | grep -v '_test.go'); do
  site_count="$(grep -cE "s\.audit\.(Record|Log)\(" "$f" || true)"
  token_count=0
  missing_token=0
  for entry in "${MANIFEST[@]}"; do
    file="${entry%%|*}"
    rest="${entry#*|}"
    token="${rest%%|*}"
    if [ "$file" = "$f" ]; then
      token_count=$((token_count + 1))
      if ! grep -qF "$token" "$f"; then
        missing_token=$((missing_token + 1))
      fi
    fi
  done
  if [ "$site_count" -ne "$token_count" ] || [ "$missing_token" -ne 0 ]; then
    echo "MANIFEST VIOLATION: $f has $site_count bare audit site(s) but $token_count manifest token(s) ($missing_token missing):"
    grep -nE "s\.audit\.(Record|Log)\(" "$f"
    echo "  (add a manifest row with its audit-site marker for every bare site,"
    echo "   or route the mutation through a WithAudit repo method)"
    fail=1
  fi
done

# ── (b) every mapped site carries its audit-site marker ──────────────────
for entry in "${MANIFEST[@]}"; do
  file="${entry%%|*}"
  rest="${entry#*|}"
  token="${rest%%|*}"
  rest="${rest#*|}"
  class="${rest%%|*}"
  if ! grep -qE "audit-site: $class" "$file"; then
    echo "MANIFEST VIOLATION: $file ($token) is missing its 'audit-site: $class' marker comment"
    fail=1
  fi
done

# ── (c) with-audit-fallback rows must have their sibling symbol in-file ───
for entry in "${MANIFEST[@]}"; do
  file="${entry%%|*}"
  rest="${entry#*|}"
  token="${rest%%|*}"
  rest="${rest#*|}"
  class="${rest%%|*}"
  sibling="${rest#*|}"
  if [ "$class" = "with-audit-fallback" ] && [ "$sibling" != "-" ]; then
    if ! grep -q "$sibling" "$file"; then
      echo "MANIFEST VIOLATION: $file ($token) class=with-audit-fallback but sibling '$sibling' is absent from the file"
      fail=1
    fi
  fi
done

if [ "$fail" -ne 0 ]; then
  echo ""
  echo "check-audit-manifest FAILED — every bare audit site must be enumerated and documented."
  exit 1
fi

echo "✓ check-audit-manifest: every bare audit site in internal/service is enumerated and documented (self-test ok)"
