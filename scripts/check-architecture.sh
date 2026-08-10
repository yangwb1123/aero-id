#!/usr/bin/env bash
# check-architecture.sh — Enforce one-way dependency rules (HARNESS.md).
#
# Allowed imports from feature packages:
#   internal/domain, internal/pkg/*, internal/repository, internal/sso,
#   internal/storage, internal/outbox, internal/hub, internal/handler/*, internal/audit
#
# Forbidden: feature → feature (cross-feature imports).
# Known exceptions are listed below with justification.
set -euo pipefail

# comment and search were deleted in the 2026-08-08 dead-tree quarantine
# (internal/comment carried the last swallowed fail-open audit write;
# internal/search was its only, equally unwired, consumer).
FEATURES=(file message block notification)
INTERNAL="github.com/aero/aero-id/internal"

# Packages that any feature may import (shared infrastructure).
ALLOWED_SHARED=(
  "$INTERNAL/domain"
  "$INTERNAL/pkg/"
  "$INTERNAL/repository"
  "$INTERNAL/sso"
  "$INTERNAL/storage"
  "$INTERNAL/outbox"
  "$INTERNAL/hub"
  "$INTERNAL/handler/"
  "$INTERNAL/audit"
  "$INTERNAL/app"
)

# Known cross-feature exceptions (format: "importer_pkg:imported_pkg:reason").
# Each exception must have a comment explaining why. Review periodically.
KNOWN_EXCEPTIONS=(
  # notification needs block.BlockChecker to suppress notifications for blocked users.
  # TODO: move BlockChecker interface to internal/domain/ to eliminate this dependency.
  "notification:block:notification dispatcher checks block status before pushing"
)

violations=0

for feat in "${FEATURES[@]}"; do
  dir="internal/$feat"
  [ -d "$dir" ] || continue

  # Collect all internal imports from non-test .go files in this feature package.
  imports=$(grep -rh "\"$INTERNAL/" "$dir"/*.go 2>/dev/null \
    | grep -v '_test.go' \
    | sed 's|.*"\('"$INTERNAL"'[^"]*\)".*|\1|' \
    | sort -u)

  for imp in $imports; do
    # Skip self-imports.
    [[ "$imp" == "$INTERNAL/$feat" ]] && continue
    # Skip imports of own sub-packages.
    [[ "$imp" == "$INTERNAL/$feat/"* ]] && continue

    # Check if it's an allowed shared package.
    allowed=false
    for shared in "${ALLOWED_SHARED[@]}"; do
      if [[ "$imp" == "$shared" || "$imp" == "$shared"* ]]; then
        allowed=true
        break
      fi
    done
    $allowed && continue

    # Check if it's a known exception.
    excepted=false
    for exc in "${KNOWN_EXCEPTIONS[@]}"; do
      IFS=: read -r exc_importer exc_imported _ <<< "$exc"
      if [[ "$feat" == "$exc_importer" && "$imp" == "$INTERNAL/$exc_imported" ]]; then
        excepted=true
        break
      fi
    done
    $excepted && continue

    echo "VIOLATION: internal/$feat imports $imp (cross-feature)"
    violations=$((violations + 1))
  done
done

# Rule: internal/domain/ must not import from internal/ (except internal/pkg/).
domain_imports=$(grep -rh "\"$INTERNAL/" internal/domain/*.go 2>/dev/null \
  | grep -v '_test.go' \
  | sed 's|.*"\('"$INTERNAL"'[^"]*\)".*|\1|' \
  | grep -v "$INTERNAL/pkg/" \
  | sort -u) || true

for imp in $domain_imports; do
  echo "VIOLATION: internal/domain imports $imp (domain must be self-contained)"
  violations=$((violations + 1))
done

if [ "$violations" -gt 0 ]; then
  echo ""
  echo "$violations architecture violation(s) detected. Fix before continuing."
  exit 1
fi

echo "✓ Architecture check passed (no cross-feature imports)"
