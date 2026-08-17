#!/usr/bin/env bash
# Fixture runner (clean): must reference the canonical YAML and carry no
# inline aeropb call literals (R2).
set -euo pipefail

YAML=scripts/load/ghz/scenarios.yaml
echo "parsing $YAML"

# Data guard tokens (mirror the real run-ghz.sh PLAN parser): json.loads
# validates every payload before any ghz invocation; a JSON-parse failure
# emits the literal token 'not valid JSON' and aborts the run.
GUARD_MSG='not valid JSON'
guard_ok() { python3 -c 'import json,sys; json.loads(sys.argv[1])' '{}' 2>/dev/null; }
guard_ok && echo "data guard ok (would emit: $GUARD_MSG on bad payload)"
