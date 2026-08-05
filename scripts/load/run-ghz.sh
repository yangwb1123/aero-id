#!/usr/bin/env bash
# ghz load scenarios (P6-08). Usage: run-ghz.sh <bearer-token> [grpc-addr]
set -euo pipefail

TOKEN="${1:?usage: run-ghz.sh <bearer-token> [grpc-addr]}"
ADDR="${2:-localhost:9090}"
PROTO=api/proto/aero_id/v1/aero_id.proto
OUT=scripts/load/results
mkdir -p "$OUT"

# name|service|rpc|data
SCENARIOS=(
  "account-getme|aeropb.AccountService|GetMe|{}"
  "aggregation-overview|aeropb.AggregationService|GetMeOverview|{}"
  "account-list-sources|aeropb.AccountService|ListSources|{\"page\":{\"limit\":50}}"
  "legacy-file-list|aeropb.FileService|ListFiles|{\"page\":{\"limit\":20}}"
  "audit-list-events|aeropb.AuditLedgerService|ListAuditFacts|{\"page\":{\"limit\":20}}"
)

for spec in "${SCENARIOS[@]}"; do
  IFS='|' read -r name service rpc data <<< "$spec"
  echo "==> ghz: $service/$rpc"
  ghz --insecure --proto "$PROTO" \
      --call "$service.$rpc" \
      -d "$data" --metadata "authorization=Bearer $TOKEN" \
      --concurrency 50 --duration 2m \
      --out "$OUT/ghz-$name.json" --format json \
      "$ADDR"
done
echo "done; results in $OUT"
