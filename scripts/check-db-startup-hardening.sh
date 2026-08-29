#!/usr/bin/env bash
# check-db-startup-hardening.sh — guard the bounded DB startup contract.
#
# The script header is the canonical rule definition. It uses only bash and
# standard POSIX file/text tools, so it is safe for Docker-free CI gates and
# performs no network or compiled-toolchain work.
#
# Rules:
#   D1 — no contextless db.Ping dead-code path: non-test Go files must not
#        declare func Ping in internal/pkg/db or call db.Ping anywhere under
#        internal/ or cmd/. PingContext is intentionally allowed for the
#        request-time health/degradation probes.
#   D2 — bounded DSN wiring: internal/config/config.go must retain the
#        connect_timeout configuration/formatter seam.
#   D3 — Makefile wiring: the check-db-startup-hardening target must invoke
#        this script with the exact recipe used by the done gate.
#
# Self-tests run on every invocation. Positive fixtures deliberately trip D1
# or D2; negative fixtures contain PingContext and a wired connect_timeout and
# must remain clean. Missing fixture files are failures so the gate cannot
# become vacuous by shrinking its own test surface.
#
# Usage: scripts/check-db-startup-hardening.sh [repository-root]
set -euo pipefail

SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${1:-$(cd "$SELF_DIR/.." && pwd)}"
FIXTURES="$SELF_DIR/testdata/db-startup-hardening"

fail=0

scan_ping_dead_code() {
	local root="$1"
	local file found=0
	if [ ! -d "$root" ]; then
		echo "ERROR: D1 scan root missing: $root"
		return 1
	fi
	while IFS= read -r file; do
		case "/$file" in
		*/internal/pkg/db/*)
			if grep -nE '^[[:space:]]*func[[:space:]]+Ping[[:space:]]*\(' "$file"; then
				echo "D1 violation: contextless func Ping declaration in $file"
				found=1
			fi
			;;
		esac
		if grep -nE '(^|[^[:alnum:]_])db[[:space:]]*\.[[:space:]]*Ping[[:space:]]*\(' "$file"; then
			echo "D1 violation: contextless db.Ping caller in $file"
			found=1
		fi
	done < <(find "$root" -type f -name '*.go' ! -name '*_test.go' -print | sort)
	return "$found"
}

scan_dsn_wiring() {
	local file="$1"
	if [ ! -f "$file" ]; then
		echo "ERROR: D2 DSN file missing: $file"
		return 1
	fi
	if grep -qF 'connect_timeout' "$file"; then
		return 0
	fi
	echo "D2 violation: connect_timeout wiring is missing from $file"
	return 1
}

# ── self-tests: positive fixtures must trip and negative fixtures must pass ─
if [ ! -d "$FIXTURES/positive" ] || [ ! -d "$FIXTURES/negative" ]; then
	echo "SELF-TEST FAIL: $FIXTURES is missing"
	fail=1
else
	for fixture in \
		"$FIXTURES/positive/ping/internal/pkg/db/db.go" \
		"$FIXTURES/positive/caller/cmd/main.go" \
		"$FIXTURES/positive/missing-dsn/internal/config/config.go" \
		"$FIXTURES/negative/clean/internal/pkg/db/db.go" \
		"$FIXTURES/negative/clean/internal/app/health.go" \
		"$FIXTURES/negative/wired/internal/config/config.go"; do
		if [ ! -f "$fixture" ]; then
			echo "SELF-TEST FAIL: fixture missing: $fixture"
			fail=1
		fi
	done

	if scan_ping_dead_code "$FIXTURES/positive/ping" >/dev/null 2>&1; then
		echo "SELF-TEST FAIL: positive ping declaration fixture matched nothing"
		fail=1
	fi
	if scan_ping_dead_code "$FIXTURES/positive/caller" >/dev/null 2>&1; then
		echo "SELF-TEST FAIL: positive ping caller fixture matched nothing"
		fail=1
	fi
	if ! scan_ping_dead_code "$FIXTURES/negative/clean" >/dev/null 2>&1; then
		echo "SELF-TEST FAIL: negative clean fixture matched D1"
		fail=1
	fi
	if scan_dsn_wiring "$FIXTURES/positive/missing-dsn/internal/config/config.go" >/dev/null 2>&1; then
		echo "SELF-TEST FAIL: positive missing-DSN fixture matched nothing"
		fail=1
	fi
	if ! scan_dsn_wiring "$FIXTURES/negative/wired/internal/config/config.go" >/dev/null 2>&1; then
		echo "SELF-TEST FAIL: negative wired-DSN fixture failed D2"
		fail=1
	fi
fi

# ── Makefile recipe tripwire ───────────────────────────────────────────────
if [ ! -f "$ROOT/Makefile" ] || ! grep -qE '^[[:space:]]*@bash scripts/check-db-startup-hardening\.sh[[:space:]]*$' "$ROOT/Makefile"; then
	echo "SELF-TEST FAIL: Makefile check-db-startup-hardening recipe line missing"
	fail=1
fi

# ── live-tree scan ─────────────────────────────────────────────────────────
if ! scan_ping_dead_code "$ROOT/internal"; then
	fail=1
fi
if ! scan_ping_dead_code "$ROOT/cmd"; then
	fail=1
fi
if ! scan_dsn_wiring "$ROOT/internal/config/config.go"; then
	fail=1
fi

if [ "$fail" -ne 0 ]; then
	echo ""
	echo "check-db-startup-hardening FAILED — bounded DSN wiring and dead Ping removal must remain pinned."
	exit 1
fi

echo "✓ check-db-startup-hardening passed (bounded connect_timeout DSN; no contextless db.Ping)"
