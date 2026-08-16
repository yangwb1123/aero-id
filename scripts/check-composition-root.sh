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

scan_new_go() { # <path> — C-1, C-2, C-3
  local file="$1" violations=""
  if [ ! -f "$file" ]; then echo "new.go missing (composition root moved?) — re-anchor check-composition-root"; return 1; fi
  local n1 n2 l1 l2
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

# ── self-tests: positive fixtures must be flagged; negative must pass ─────
scan_new_go "$FIXTURES/positive/new.go" >/dev/null 2>&1; r1=$?
scan_audit_go "$FIXTURES/positive/audit.go" >/dev/null 2>&1; r2=$?
scan_main_go "$FIXTURES/positive/main.go" >/dev/null 2>&1; r3=$?
scan_migrate_main_go "$FIXTURES/positive/migrate_main.go" >/dev/null 2>&1; r5=$?
[ "$r1" = "0" ] && { echo "SELF-TEST FAIL: positive/new.go matched nothing"; fail=1; }
[ "$r2" = "0" ] && { echo "SELF-TEST FAIL: positive/audit.go matched nothing"; fail=1; }
[ "$r3" = "0" ] && { echo "SELF-TEST FAIL: positive/main.go matched nothing"; fail=1; }
[ "$r5" = "0" ] && { echo "SELF-TEST FAIL: positive/migrate_main.go matched nothing (config.Load bypass must redden C-5)"; fail=1; }
scan_new_go "$FIXTURES/negative/new.go" >/dev/null 2>&1; rn=$?
scan_audit_go "$FIXTURES/negative/audit.go" >/dev/null 2>&1; ra=$?
scan_main_go "$FIXTURES/negative/main.go" >/dev/null 2>&1; rm=$?
scan_migrate_main_go "$FIXTURES/negative/migrate_main.go" >/dev/null 2>&1; rn5=$?
{ [ "$rn" != "0" ] || [ "$ra" != "0" ] || [ "$rm" != "0" ]; } && { echo "SELF-TEST FAIL: a negative fixture matched"; fail=1; }
[ "$rn5" != "0" ] && { echo "SELF-TEST FAIL: negative/migrate_main.go matched (C-5) — must pass with one seam of each"; fail=1; }
scan_new_go "$FIXTURES/positive/missing.go" >/dev/null 2>&1; rmiss=$?
[ "$rmiss" = "0" ] && { echo "SELF-TEST FAIL: missing-file fixture did not fail loud"; fail=1; }
scan_migrate_main_go "$FIXTURES/positive/missing.go" >/dev/null 2>&1; rmiss5=$?
[ "$rmiss5" = "0" ] && { echo "SELF-TEST FAIL: missing-file fixture did not fail loud (C-5)"; fail=1; }

# ── live-tree scan ────────────────────────────────────────────────────────
ROOT="${1:-$(cd "$SELF_DIR/.." && pwd)}"
scan_new_go "$ROOT/internal/app/new.go" || fail=1
scan_audit_go "$ROOT/internal/app/audit.go" || fail=1
scan_main_go "$ROOT/cmd/server/main.go" || fail=1
scan_migrate_main_go "$ROOT/cmd/migrate/main.go" || fail=1

if [ "$fail" -ne 0 ]; then
  echo ""
  echo "check-composition-root FAILED — audit stack call and drain Start must stay in the server composition root, and cmd/migrate must keep the shared config.Load/migrate.New seams."
  exit 1
fi
echo "✓ check-composition-root: buildAuditStack + drain Start wired in the server root, config.Load/migrate.New seams pinned in cmd/migrate (self-test ok)"
