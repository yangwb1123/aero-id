#!/usr/bin/env bash
# Render the one-shot migration Job for an immutable application release.
# The Job is deliberately separate from the steady-state regional-cell bundle.
set -euo pipefail

template=${1:-deployments/k8s/migration-job.yaml}
output=${2:--}

: "${RELEASE:?set RELEASE to a unique Kubernetes-safe release identifier}"
: "${AERO_ID_IMAGE_DIGEST:?set AERO_ID_IMAGE_DIGEST to sha256:<64 lowercase hex characters>}"

if [[ ! "$RELEASE" =~ ^[a-z0-9]([a-z0-9-]{0,53}[a-z0-9])?$ ]]; then
  echo "render-migration-job: RELEASE must be a Kubernetes-safe DNS label of at most 55 characters" >&2
  exit 2
fi
if [[ ! "$AERO_ID_IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "render-migration-job: AERO_ID_IMAGE_DIGEST must be sha256:<64 lowercase hex characters>" >&2
  exit 2
fi
if [[ ! -f "$template" ]]; then
  echo "render-migration-job: template is unavailable" >&2
  exit 2
fi

action=${MIGRATION_ACTION:-up}
count=${MIGRATION_COUNT:-1}
case "$action" in
  up|version)
    args="[\"-config\", \"/etc/aero-id/config.yaml\", \"-migrations\", \"/app/migrations\", \"${action}\"]"
    job_name="${RELEASE}-${action}"
    ;;
  down)
    if [[ ! "$count" =~ ^[1-9][0-9]*$ ]]; then
      echo "render-migration-job: MIGRATION_COUNT must be a positive integer for down" >&2
      exit 2
    fi
    args="[\"-config\", \"/etc/aero-id/config.yaml\", \"-migrations\", \"/app/migrations\", \"down\", \"${count}\"]"
    job_name="${RELEASE}-down-${count}"
    ;;
  *)
    echo "render-migration-job: MIGRATION_ACTION must be up, down, or version" >&2
    exit 2
    ;;
esac

if [[ ${#job_name} -gt 63 ]]; then
  echo "render-migration-job: rendered Job name is too long" >&2
  exit 2
fi

tmp=$(mktemp)
trap 'rm -f "$tmp" "$tmp.rendered"' EXIT
sed \
  -e "s#ghcr.io/aero/aero-id@sha256:REPLACE_WITH_RELEASE_DIGEST#ghcr.io/aero/aero-id@${AERO_ID_IMAGE_DIGEST}#g" \
  -e "s#RELEASE_PLACEHOLDER#${job_name}#g" \
  -e "s#MIGRATION_ARGS_PLACEHOLDER#${args}#g" \
  "$template" >"$tmp"

if grep -Eq 'REPLACE_WITH_|RELEASE_PLACEHOLDER|MIGRATION_ARGS_PLACEHOLDER' "$tmp"; then
  echo "render-migration-job: release placeholders remain after rendering" >&2
  exit 2
fi

if [[ "$output" == "-" ]]; then
  cat "$tmp"
else
  install -m 0644 "$tmp" "$output"
fi
