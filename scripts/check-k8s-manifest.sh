#!/usr/bin/env bash
# Static/rendered checks for the regional-cell release template. This gate does
# not contact a cluster or use kubectl/kubeconfig. It catches mutable images,
# non-optional feature secrets and missing preflight, then validates ALL steady,
# paused and up/down/version renders against vendored SHA-256-pinned strict
# Kubernetes schemas. Unknown GVKs/fields, duplicate keys, missing/changed schemas,
# empty/partial bundles and invalid types fail closed; no missing-schema skips.
# The existing deployments/k8s Go test package is the validator and self-test seam.
# Go dependencies may need normal go.mod/go.sum-verified provisioning on a cold
# cache; schema validation has no network or arbitrary file-reference resolver.
# Requires Go, bash, sha256sum and timeout, not Docker or a Kubernetes client.
# Server-side dry-run/admission/rollout remain separate production release gates.
set -euo pipefail

manifest=${1:-deployments/k8s/regional-cell.yaml}
migration_manifest=${K8S_MIGRATION_MANIFEST:-deployments/k8s/migration-job.yaml}
fail() { echo "check-k8s-manifest: $*" >&2; exit 1; }

[[ -f "$manifest" ]] || fail "manifest is unavailable"
[[ -f "$migration_manifest" ]] || fail "migration Job template is unavailable"

# A release renderer must replace both placeholders. Keeping them in the source
# template makes an unrendered apply visibly fail at image pull/config rollout.
[[ $(grep -c 'ghcr.io/aero/aero-id@sha256:REPLACE_WITH_RELEASE_DIGEST' "$manifest") -eq 4 ]] ||
  fail "API/worker application and preflight images are not all digest placeholders"
[[ $(grep -c 'checksum/config: "REPLACE_WITH_CONFIG_SHA256"' "$manifest") -eq 2 ]] ||
  fail "API and worker config checksum annotations are not both present"
[[ $(grep -c '^      initContainers:' "$manifest") -eq 2 ]] ||
  fail "API and worker startup preflight init containers are not both present"
[[ $(grep -c '^        - name: preflight$' "$manifest") -eq 2 ]] ||
  fail "API and worker preflight init containers are not both present"
[[ $(grep -c 'command: \["/app/preflight"\]' "$manifest") -eq 2 ]] ||
  fail "preflight init containers do not override the image entrypoint"
[[ $(grep -c "fieldPath: metadata.annotations\['checksum/config'\]" "$manifest") -eq 4 ]] ||
  fail "preflight and application containers are not bound to the pod config checksum"

# The base is intentionally source-disabled. Secure overlays must supply the
# endpoint and complete credentials; they must not make the base unbootable.
awk '
  /^      snaplink:$/ { source="snaplink" }
  /^      aero_im:$/ { source="aero_im" }
  /^      aero_vault:$/ { source="aero_vault" }
  source != "" && /^        endpoint: / {
    if ($0 != "        endpoint: \"\"") exit 1
    source=""
  }
' "$manifest" || fail "a source endpoint is enabled in the base manifest"

# Every source credential is optional at the Kubernetes layer. If an enabled
# overlay omits one, config.Load/preflight fails closed instead of leaving a pod
# Pending merely because an unrelated source is disabled.
for key in \
  snaplink_bearer_token snaplink_oauth_client_secret \
  aero_im_bearer_token aero_im_oauth_client_secret aero_im_target_assertion_private_key \
  aero_vault_bearer_token aero_vault_oauth_client_secret; do
  grep -q "key: ${key}, optional: true" "$manifest" || fail "source secret ${key} is not optional"
done

if grep -Eq 'image:[[:space:]]+[^[:space:]]*:latest([[:space:]]|$)' "$manifest" "$migration_manifest"; then
  fail "mutable :latest image found"
fi
while IFS= read -r image_line; do
  [[ "$image_line" == *'@sha256:'* ]] || fail "workload image is not digest-pinned: $image_line"
done < <(grep -E '^[[:space:]]+image:' "$manifest" "$migration_manifest")

# Both steady-state workloads and the migration Job must use the same image
# digest. The Job is separate so applying the normal bundle is idempotent.
[[ $(grep -c 'ghcr.io/aero/aero-id@sha256:REPLACE_WITH_RELEASE_DIGEST' "$migration_manifest") -eq 1 ]] ||
  fail "migration Job image is not a digest placeholder"
grep -q 'command: \["/app/migrate"\]' "$migration_manifest" || fail "migration Job does not run /app/migrate"
grep -q 'args: MIGRATION_ARGS_PLACEHOLDER' "$migration_manifest" || fail "migration Job args are not renderer-controlled"

# Render every release form with deterministic inputs; retain up AND version
# instead of overwriting up before validation. Cleanup only our private tree.
workdir=$(mktemp -d)
rendered="$workdir/steady.yaml"
paused="$workdir/paused.yaml"
migration_rendered="$workdir/migration-up.yaml"
migration_down="$workdir/migration-down.yaml"
migration_version="$workdir/migration-version.yaml"
trap 'rm -rf "$workdir"' EXIT
REGION=us-east-1 \
AERO_ID_IMAGE_DIGEST=sha256:$(printf 'a%.0s' {1..64}) AERO_ID_PAUSE_WORKLOADS=0 \
  scripts/render-regional-cell.sh "$manifest" "$rendered"
grep -q 'checksum/config: "[0-9a-f]\{64\}"' "$rendered" || fail "rendered checksum is missing"
! grep -q 'REPLACE_WITH_' "$rendered" || fail "release placeholders remain after rendering"
config_hash=$(awk '
  $0 == "  config.yaml: |" { in_config=1; next }
  in_config && $0 != "" && $0 !~ /^    / { exit }
  in_config { sub(/^    /, ""); print }
' "$rendered" | sha256sum | awk '{print $1}')
checksum_count=$(grep -Eo 'checksum/config: "[0-9a-f]{64}"' "$rendered" | wc -l)
[[ "$checksum_count" -eq 2 ]] || fail "rendered checksum is not present on both workloads"
[[ $(grep -Eo 'checksum/config: "[0-9a-f]{64}"' "$rendered" | sed -E 's/.*"([0-9a-f]+)"/\1/' | sort -u) == "$config_hash" ]] ||
  fail "rendered checksum does not match ConfigMap bytes"

REGION=us-east-1 \
AERO_ID_IMAGE_DIGEST=sha256:$(printf 'a%.0s' {1..64}) \
AERO_ID_PAUSE_WORKLOADS=1 scripts/render-regional-cell.sh "$manifest" "$paused"
! grep -q '^kind: HorizontalPodAutoscaler$' "$paused" || fail "paused render retains the API HPA"
! grep -q '  replicas: [23]$' "$paused" || fail "paused render starts an application workload"

RELEASE=check-release \
AERO_ID_IMAGE_DIGEST=sha256:$(printf 'a%.0s' {1..64}) MIGRATION_ACTION=up \
  scripts/render-migration-job.sh "$migration_manifest" "$migration_rendered"
grep -q '"up"' "$migration_rendered" || fail "migration up command is missing"
! grep -Eq 'REPLACE_WITH_|PLACEHOLDER' "$migration_rendered" || fail "migration release placeholders remain"
RELEASE=check-release \
AERO_ID_IMAGE_DIGEST=sha256:$(printf 'a%.0s' {1..64}) \
MIGRATION_ACTION=down MIGRATION_COUNT=2 \
  scripts/render-migration-job.sh "$migration_manifest" "$migration_down"
grep -q '"down", "2"' "$migration_down" || fail "migration down command is missing"
RELEASE=check-release \
AERO_ID_IMAGE_DIGEST=sha256:$(printf 'a%.0s' {1..64}) \
MIGRATION_ACTION=version \
  scripts/render-migration-job.sh "$migration_manifest" "$migration_version"
grep -q '"version"' "$migration_version" || fail "migration version command is missing"

# Never an optional validator or an ambient PATH binary. Tests consume the exact
# artifacts above (30 resources total) and prove rejection with derived red cases.
AERO_ID_K8S_RENDERED_DIR="$workdir" GOTOOLCHAIN=local GOWORK=off GOFLAGS= \
  timeout --foreground --kill-after=5s 2m go test -mod=readonly ./deployments/k8s \
  -run '^TestKubernetes(RenderedSchemaGate|StrictSchemaRejectsUnsafeDocuments|SchemaIntegrityAndNoExternalResolution|SchemaManifestBudgets)$' \
  -count=1 -timeout=60s || fail "pinned offline schema validation or self-test failed"

echo "check-k8s-manifest: ok (30 resources, pinned offline schemas + rejection self-tests)"
