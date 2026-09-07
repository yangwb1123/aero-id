#!/usr/bin/env bash
# Render the regional-cell template for one release. The template deliberately
# contains no usable application image reference or stable config checksum;
# applying it without this renderer is an operator error.
set -euo pipefail

TEMPLATE=${1:-deployments/k8s/regional-cell.yaml}
OUTPUT=${2:--}

: "${REGION:?set REGION to the exact cell region}"
: "${AERO_ID_IMAGE_DIGEST:?set AERO_ID_IMAGE_DIGEST to sha256:<64 lowercase hex characters>}"

if [[ ! "$REGION" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]]; then
  echo "render-regional-cell: REGION is not a valid DNS label" >&2
  exit 2
fi
case "$REGION" in
  us-east-1|eu-west-1|ap-southeast-1) ;;
  *)
    echo "render-regional-cell: REGION is not listed in account.regions" >&2
    exit 2
    ;;
esac
if [[ ! "$AERO_ID_IMAGE_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "render-regional-cell: AERO_ID_IMAGE_DIGEST must be sha256:<64 lowercase hex characters>" >&2
  exit 2
fi
if [[ "${AERO_ID_PAUSE_WORKLOADS:-0}" != 0 && "${AERO_ID_PAUSE_WORKLOADS:-0}" != 1 ]]; then
  echo "render-regional-cell: AERO_ID_PAUSE_WORKLOADS must be 0 or 1" >&2
  exit 2
fi

if [[ ! -f "$TEMPLATE" ]]; then
  echo "render-regional-cell: template is unavailable" >&2
  exit 2
fi

tmp=$(mktemp)
trap 'rm -f "$tmp" "$tmp.rendered"' EXIT

# REGION is restricted above, so it is safe to use as a sed replacement. The
# digest is hexadecimal and therefore safe as well. Do this before hashing:
# the rendered config, rather than the template, is the checksum contract.
sed \
  -e "s/REGION_PLACEHOLDER/${REGION}/g" \
  -e "s#ghcr.io/aero/aero-id@sha256:REPLACE_WITH_RELEASE_DIGEST#ghcr.io/aero/aero-id@${AERO_ID_IMAGE_DIGEST}#g" \
  "$TEMPLATE" >"$tmp"

config_hash=$(awk '
  $0 == "  config.yaml: |" { in_config=1; next }
  in_config && $0 != "" && $0 !~ /^    / { exit }
  in_config { sub(/^    /, ""); print }
' "$tmp" | sha256sum | awk '{print $1}')

if [[ ! "$config_hash" =~ ^[0-9a-f]{64}$ ]]; then
  echo "render-regional-cell: could not calculate config checksum" >&2
  exit 2
fi

sed "s/REPLACE_WITH_CONFIG_SHA256/${config_hash}/g" "$tmp" >"$tmp.rendered"

# A paused render installs the new ConfigMap and pod template without allowing
# an application or worker to start before an explicit migration step. This is
# used for schema-changing rollouts. Omit the HPA as well as setting both
# application Deployments to zero, because an HPA can otherwise recreate API
# pods before the migration completes.
if [[ "${AERO_ID_PAUSE_WORKLOADS:-0}" == 1 ]]; then
  awk '
    /^---$/ { kind=""; object=""; skip=0 }
    $0 == "kind: HorizontalPodAutoscaler" { skip=1; next }
    skip { next }
    $0 == "kind: Deployment" { kind="Deployment" }
    kind == "Deployment" && $0 == "  name: aero-id-api" { object="aero-id-api" }
    kind == "Deployment" && $0 == "  name: aero-id-workers" { object="aero-id-workers" }
    kind == "Deployment" && object == "aero-id-api" && $0 == "  replicas: 3" { sub(/3$/, "0") }
    kind == "Deployment" && object == "aero-id-workers" && $0 == "  replicas: 2" { sub(/2$/, "0") }
    { print }
  ' "$tmp.rendered" >"$tmp.paused"
  mv "$tmp.paused" "$tmp.rendered"
fi

# A rendered artifact must be self-contained. An unresolved marker is safer as
# a failed render than as a pod that starts with a different image or region.
if grep -Eq 'REPLACE_WITH_|REGION_PLACEHOLDER' "$tmp.rendered"; then
  echo "render-regional-cell: release placeholders remain after rendering" >&2
  exit 2
fi
if [[ "${AERO_ID_PAUSE_WORKLOADS:-0}" == 1 ]] && grep -q '^kind: HorizontalPodAutoscaler$' "$tmp.rendered"; then
  echo "render-regional-cell: paused release must not contain an HPA" >&2
  exit 2
fi

if [[ "$OUTPUT" == "-" ]]; then
  cat "$tmp.rendered"
else
  install -m 0644 "$tmp.rendered" "$OUTPUT"
fi
