#!/usr/bin/env bash
# check-composition-root.sh — server composition-root seam gate (Mutation-A pin).
#
# The audit-governance activation gate is only meaningful if the server boot
# path actually reaches it. This gate pins the seam statically, Docker-free,
# in the done-gate:
#
#   C-1  internal/app/new.go must contain exactly one
#        `callbackSvc, auditLedger, auditLedgerRepo, err := buildAuditStack(cfg, gdb, controlMetrics)`
#        (deleting the call → count 0; wrapping/renaming → count 0; a second
#        copy anywhere → count 2).
#   C-2  internal/app/new.go must contain exactly one
#        `stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)`
#        (the drain start; also pins that the tick interval stays
#        config-driven, not a literal).
#   C-2b internal/app/audit.go must contain zero `callbackSvc.Start(` — the
#        drain must not move into buildAuditStack (drain-before-Start class).
#   C-3  the C-1 line must precede `repository.NewFileRepository(` in new.go
#        (F-1 dependency order: the audit stack is constructed first).
#   C-4  cmd/server/main.go must contain exactly one `app.New(ctx, cfg, log)`
#        and zero `AuditGovernance` tokens — no Load-bypass or cfg-mutation
#        vector between config.Load and app.New.
#   C-5  cmd/migrate/main.go must contain exactly one `config.Load(` — the
#        shared validation seam that fires the audit-governance activation
#        gate and the F3 anchor-scheme boot coverage for the production
#        migration binary — and exactly one `migrate.New(` dispatch call.
#        No load-bypass vector for migrate (e.g. "migrate before governance").
#   C-6  internal/app/new.go must contain exactly one
#        `repository.RunAuditCleanerWithHolds(` and zero bare
#        `repository.RunAuditCleaner(` — the audit TTL cleaner must run with
#        the legal-hold registry so held actors' rows are never purged (P4-07;
#        a bare call delegates with a nil hold registry and full-purges). Note
#        the fixed string `repository.RunAuditCleaner(` does NOT match the
#        WithHolds form (the character after RunAuditCleaner is `W`, not `(`) —
#        proven by the positive/negative cleaner.go fixtures.
#   C-7  internal/app/new.go must stay <= MAX_COMPOSITION_ROOT_LINES lines
#        (default 400, read from the environment; the Makefile exports the
#        overridable ?= variable). Same wc -l counting and strict -gt
#        semantics as check-file-sizes so both gates agree on the boundary
#        (400 passes, 401 trips) — proven by the positive/negative size.go
#        fixtures (401 vs 400 lines, otherwise C-1/C-2/C-3/C-6-satisfying).
#   C-8  production Vault construction must use internal/connector/aerovault:
#        no non-test production file outside that package may directly call
#        NewJSONSummaryConnector with SourceAeroVault, and the app connector
#        builder must contain exactly one read-only aerovault.New call plus
#        exactly one capability-aware aerovault.NewWithCapabilities call.
#        This prevents a future composition-root path from bypassing the
#        capability wrapper.
#   C-9  production Snaplink construction must use the capability-aware
#        internal/connector/snaplink adapter. Direct generic construction is
#        banned outside that adapter/shared implementation, and the app
#        builder must call snaplink.NewWithCapabilities exactly once without
#        calling the legacy snaplink.New seam.
#
# The semantic floor (refusal byte-exactness, kill switch, delivery) lives in
# the integration boot e2e (internal/app/composition_root_integration_test.go);
# this gate is the Docker-free companion pin. Missing files are fail-loud:
# green never means "file not scanned" (precedent: check-audit-swallow /
# check-anchored-event-ids self-test doctrine). Self-tests run against
# scripts/testdata/composition-root/ before the live-tree scan.
#
# Usage: scripts/check-composition-root.sh [ROOT]   (default: repo root)

set -u

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FIXTURES="$SELF_DIR/testdata/composition-root"

C1='callbackSvc, auditLedger, auditLedgerRepo, err := buildAuditStack(cfg, gdb, controlMetrics)'
C2='stopCallbacks := callbackSvc.Start(context.Background(), cfg.Callback.WorkerInterval)'
C4='app.New(ctx, cfg, log)'

fail=0

scan_new_go() { # <path> — C-1, C-2, C-3, C-6, C-7
  local file="$1" violations=""
  if [ ! -f "$file" ]; then echo "new.go missing (composition root moved?) — re-anchor check-composition-root"; return 1; fi
  local n1 n2 l1 l2 n6 n6b nlines budget
  n1="$(grep -cF -- "$C1" "$file")"
  [ "$n1" = "1" ] || violations="${violations}C-1 buildAuditStack call: found $n1, want exactly 1
"
  n2="$(grep -cF -- "$C2" "$file")"
  [ "$n2" = "1" ] || violations="${violations}C-2 drain Start: found $n2, want exactly 1
"
  l1="$(grep -nF -- "$C1" "$file" | head -1 | cut -d: -f1)"
  l2="$(grep -nF 'repository.NewFileRepository(' "$file" | head -1 | cut -d: -f1)"
  if [ -n "$l1" ] && [ -n "$l2" ] && [ "$l1" -ge "$l2" ]; then
    violations="${violations}C-3 buildAuditStack (line $l1) must precede NewFileRepository (line $l2)
"
  fi
  n6="$(grep -cF -- 'repository.RunAuditCleanerWithHolds(' "$file")"
  [ "$n6" = "1" ] || violations="${violations}C-6 hold-aware audit cleaner: found $n6, want exactly 1
"
  n6b="$(grep -cF -- 'repository.RunAuditCleaner(' "$file")"
  [ "$n6b" = "0" ] || violations="${violations}C-6 plain audit cleaner (nil hold registry): found $n6b, want 0 — held actors would be purged
"
  budget="${MAX_COMPOSITION_ROOT_LINES:-400}"
  nlines="$(wc -l < "$file")"
  [ "$nlines" -le "$budget" ] || violations="${violations}C-7 composition root size: $nlines lines > $budget (MAX_COMPOSITION_ROOT_LINES) — split into sibling builders
"
  if [ -n "$violations" ]; then printf '%s' "$violations"; return 1; fi
  return 0
}

scan_audit_go() { # <path> — C-2b
  local file="$1" violations=""
  if [ ! -f "$file" ]; then echo "audit.go missing — re-anchor check-composition-root"; return 1; fi
  local n
  n="$(grep -cF 'callbackSvc.Start(' "$file")"
  if [ "$n" != "0" ]; then printf 'C-2b drain Start inside buildAuditStack: found %s (drain-before-Start class)\n' "$n"; return 1; fi
  return 0
}

scan_main_go() { # <path> — C-4
  local file="$1" violations=""
  if [ ! -f "$file" ]; then echo "cmd/server/main.go missing — re-anchor check-composition-root"; return 1; fi
  local n
  n="$(grep -cF -- "$C4" "$file")"
  [ "$n" = "1" ] || violations="${violations}C-4 app.New on the Loaded config: found $n, want exactly 1
"
  n="$(grep -c 'AuditGovernance' "$file")"
  [ "$n" = "0" ] || violations="${violations}C-4 AuditGovernance touched in main.go (Load-bypass vector): found $n
"
  if [ -n "$violations" ]; then printf '%s' "$violations"; return 1; fi
  return 0
}

scan_migrate_main_go() { # <path> — C-5
  local file="$1" violations=""
  if [ ! -f "$file" ]; then echo "cmd/migrate/main.go missing — re-anchor check-composition-root"; return 1; fi
  local n
  n="$(grep -cF -- 'config.Load(' "$file")"
  [ "$n" = "1" ] || violations="${violations}C-5 config.Load seam: found $n, want exactly 1 (migrate must keep the shared config validation gate)
"
  n="$(grep -cF -- 'migrate.New(' "$file")"
  [ "$n" = "1" ] || violations="${violations}C-5 migrate.New dispatch seam: found $n, want exactly 1
"
  if [ -n "$violations" ]; then printf '%s' "$violations"; return 1; fi
  return 0
}

scan_vault_construction() { # <root> — C-8 direct generic construction ban
  local root="$1" f violations=""
  if [ ! -d "$root" ]; then
    echo "C-8 scan root missing — re-anchor Vault construction check"
    return 1
  fi
  while IFS= read -r f; do
    case "/$f" in
      */internal/connector/aerovault/*) continue ;;
      # http_summary.go is the shared transport implementation delegated to by
      # aerovault.New; its constructor necessarily mentions both the generic
      # constructor and SourceAeroVault for source-specific policy. It is not
      # a composition root or a second Vault construction path.
      */internal/connector/http_summary.go) continue ;;
    esac
    # The two names must be absent from the same production file, not merely
    # on one line: a multiline call must not evade the composition check.
    if grep -qF 'NewJSONSummaryConnector' "$f" && grep -qF 'SourceAeroVault' "$f"; then
      violations="${violations}C-8 $f directly constructs the generic Vault connector; use internal/connector/aerovault
"
    fi
  done < <(find "$root" -name '*.go' ! -name '*_test.go' 2>/dev/null | sort)
  if [ -n "$violations" ]; then printf '%s' "$violations"; return 1; fi
  return 0
}

scan_vault_wiring() { # <root> — C-8 capability-aware composition seam
  local file="$1/internal/app/connectors.go" violations=""
  if [ ! -f "$file" ]; then
    echo "internal/app/connectors.go missing — re-anchor C-8 Vault wiring check"
    return 1
  fi
  local n
  n="$(grep -cF -- 'aerovault.New(' "$file")"
  [ "$n" = "1" ] || violations="${violations}C-8 read-only aerovault.New seam: found $n, want exactly 1
"
  n="$(grep -cF -- 'aerovault.NewWithCapabilities(' "$file")"
  [ "$n" = "1" ] || violations="${violations}C-8 capability-aware aerovault.NewWithCapabilities seam: found $n, want exactly 1
"
  if [ -n "$violations" ]; then printf '%s' "$violations"; return 1; fi
  return 0
}

scan_snaplink_construction() { # <root> — C-9 direct generic construction ban
  local root="$1" f violations=""
  if [ ! -d "$root" ]; then
    echo "C-9 scan root missing — re-anchor Snaplink construction check"
    return 1
  fi
  while IFS= read -r f; do
    case "/$f" in
      */internal/connector/snaplink/*) continue ;;
      */internal/connector/http_summary.go) continue ;;
    esac
    if grep -qF 'NewJSONSummaryConnector' "$f" && grep -qF 'SourceSnaplink' "$f"; then
      violations="${violations}C-9 $f directly constructs the generic Snaplink connector; use internal/connector/snaplink
"
    fi
  done < <(find "$root" -name '*.go' ! -name '*_test.go' 2>/dev/null | sort)
  if [ -n "$violations" ]; then printf '%s' "$violations"; return 1; fi
  return 0
}

scan_snaplink_wiring() { # <root> — C-9 capability-aware composition seam
  local file="$1/internal/app/connectors.go" violations=""
  if [ ! -f "$file" ]; then
    echo "internal/app/connectors.go missing — re-anchor C-9 Snaplink wiring check"
    return 1
  fi
  local n
  n="$(grep -cF -- 'snaplink.NewWithCapabilities(' "$file")"
  [ "$n" = "1" ] || violations="${violations}C-9 snaplink.NewWithCapabilities seam: found $n, want exactly 1
"
  n="$(grep -cF -- 'snaplink.New(' "$file")"
  [ "$n" = "0" ] || violations="${violations}C-9 legacy snaplink.New seam: found $n, want 0
"
  if [ -n "$violations" ]; then printf '%s' "$violations"; return 1; fi
  return 0
}

# ── self-tests: positive fixtures must be flagged; negative must pass ─────
scan_new_go "$FIXTURES/positive/new.go" >/dev/null 2>&1; r1=$?
scan_new_go "$FIXTURES/positive/cleaner.go" >/dev/null 2>&1; r6=$?
scan_audit_go "$FIXTURES/positive/audit.go" >/dev/null 2>&1; r2=$?
scan_main_go "$FIXTURES/positive/main.go" >/dev/null 2>&1; r3=$?
scan_migrate_main_go "$FIXTURES/positive/migrate_main.go" >/dev/null 2>&1; r5=$?
[ "$r1" = "0" ] && { echo "SELF-TEST FAIL: positive/new.go matched nothing"; fail=1; }
[ "$r6" = "0" ] && { echo "SELF-TEST FAIL: positive/cleaner.go matched nothing (bare RunAuditCleaner must redden C-6)"; fail=1; }
[ "$r2" = "0" ] && { echo "SELF-TEST FAIL: positive/audit.go matched nothing"; fail=1; }
[ "$r3" = "0" ] && { echo "SELF-TEST FAIL: positive/main.go matched nothing"; fail=1; }
[ "$r5" = "0" ] && { echo "SELF-TEST FAIL: positive/migrate_main.go matched nothing (config.Load bypass must redden C-5)"; fail=1; }
scan_new_go "$FIXTURES/negative/new.go" >/dev/null 2>&1; rn=$?
scan_new_go "$FIXTURES/negative/cleaner.go" >/dev/null 2>&1; rn6=$?
scan_audit_go "$FIXTURES/negative/audit.go" >/dev/null 2>&1; ra=$?
scan_main_go "$FIXTURES/negative/main.go" >/dev/null 2>&1; rm=$?
scan_migrate_main_go "$FIXTURES/negative/migrate_main.go" >/dev/null 2>&1; rn5=$?
{ [ "$rn" != "0" ] || [ "$ra" != "0" ] || [ "$rm" != "0" ]; } && { echo "SELF-TEST FAIL: a negative fixture matched"; fail=1; }
[ "$rn6" != "0" ] && { echo "SELF-TEST FAIL: negative/cleaner.go matched (C-6) — must pass with exactly one WithHolds form and zero bare forms"; fail=1; }
[ "$rn5" != "0" ] && { echo "SELF-TEST FAIL: negative/migrate_main.go matched (C-5) — must pass with one seam of each"; fail=1; }
scan_new_go "$FIXTURES/positive/missing.go" >/dev/null 2>&1; rmiss=$?
[ "$rmiss" = "0" ] && { echo "SELF-TEST FAIL: missing-file fixture did not fail loud"; fail=1; }
scan_migrate_main_go "$FIXTURES/positive/missing.go" >/dev/null 2>&1; rmiss5=$?
[ "$rmiss5" = "0" ] && { echo "SELF-TEST FAIL: missing-file fixture did not fail loud (C-5)"; fail=1; }
scan_new_go "$FIXTURES/positive/size.go" >/dev/null 2>&1; rsize=$?
[ "$rsize" = "0" ] && { echo "SELF-TEST FAIL: positive/size.go (401 lines) passed — C-7 size pin not reddening"; fail=1; }
scan_new_go "$FIXTURES/negative/size.go" >/dev/null 2>&1; rnsize=$?
[ "$rnsize" != "0" ] && { echo "SELF-TEST FAIL: negative/size.go (400 lines) reddened — C-7 boundary off by one"; fail=1; }
scan_vault_construction "$FIXTURES/positive" >/dev/null 2>&1; rvault=$?
[ "$rvault" = "0" ] && { echo "SELF-TEST FAIL: positive Vault constructor fixture passed — C-8 direct-construction ban is vacuous"; fail=1; }
scan_vault_construction "$FIXTURES/negative" >/dev/null 2>&1; rnvault=$?
[ "$rnvault" != "0" ] && { echo "SELF-TEST FAIL: negative Vault constructor fixture matched"; fail=1; }
scan_vault_wiring "$FIXTURES/positive" >/dev/null 2>&1; rvaultwire=$?
[ "$rvaultwire" = "0" ] && { echo "SELF-TEST FAIL: positive Vault wiring fixture passed — C-8 seam count is vacuous"; fail=1; }
scan_vault_wiring "$FIXTURES/negative" >/dev/null 2>&1; rnvaultwire=$?
[ "$rnvaultwire" != "0" ] && { echo "SELF-TEST FAIL: negative Vault wiring fixture matched"; fail=1; }
scan_snaplink_construction "$FIXTURES/positive" >/dev/null 2>&1; rsnap=$?
[ "$rsnap" = "0" ] && { echo "SELF-TEST FAIL: positive Snaplink constructor fixture passed — C-9 direct-construction ban is vacuous"; fail=1; }
scan_snaplink_construction "$FIXTURES/negative" >/dev/null 2>&1; rnsnap=$?
[ "$rnsnap" != "0" ] && { echo "SELF-TEST FAIL: negative Snaplink constructor fixture matched"; fail=1; }
scan_snaplink_wiring "$FIXTURES/positive" >/dev/null 2>&1; rsnapwire=$?
[ "$rsnapwire" = "0" ] && { echo "SELF-TEST FAIL: positive Snaplink wiring fixture passed — C-9 seam count is vacuous"; fail=1; }
scan_snaplink_wiring "$FIXTURES/negative" >/dev/null 2>&1; rnsnapwire=$?
[ "$rnsnapwire" != "0" ] && { echo "SELF-TEST FAIL: negative Snaplink wiring fixture matched"; fail=1; }

# ── live-tree scan ────────────────────────────────────────────────────────
ROOT="${1:-$(cd "$SELF_DIR/.." && pwd)}"
scan_new_go "$ROOT/internal/app/new.go" || fail=1
scan_audit_go "$ROOT/internal/app/audit.go" || fail=1
scan_main_go "$ROOT/cmd/server/main.go" || fail=1
scan_migrate_main_go "$ROOT/cmd/migrate/main.go" || fail=1
scan_vault_construction "$ROOT/internal" || fail=1
scan_vault_construction "$ROOT/cmd" || fail=1
scan_vault_wiring "$ROOT" || fail=1
scan_snaplink_construction "$ROOT/internal" || fail=1
scan_snaplink_construction "$ROOT/cmd" || fail=1
scan_snaplink_wiring "$ROOT" || fail=1

if [ "$fail" -ne 0 ]; then
  echo ""
  echo "check-composition-root FAILED — audit stack call and drain Start must stay in the server composition root, cmd/migrate must keep the shared config.Load/migrate.New seams, and source construction must remain capability-isolated."
  exit 1
fi
echo "✓ check-composition-root: buildAuditStack + drain Start wired in the server root, hold-aware audit cleaner pinned (C-6), new.go within ${MAX_COMPOSITION_ROOT_LINES:-400} lines (C-7), config.Load/migrate.New seams pinned in cmd/migrate, Vault and Snaplink construction isolated (C-8/C-9; self-test ok)"
