#!/usr/bin/env bash
# Fixture runner (clean): must reference the canonical YAML and carry no
# inline aeropb call literals (R2).
set -euo pipefail

YAML=scripts/load/ghz/scenarios.yaml
echo "parsing $YAML"

echo aeropb.AggregationService.GetMeOverview
