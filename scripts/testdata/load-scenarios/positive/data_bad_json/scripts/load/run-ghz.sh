#!/usr/bin/env bash
# Fixture runner (clean): must reference the canonical YAML, carry no
# inline aeropb call literals (R2), and carry the json.loads data-guard
# tokens (R2(e)) so the fixture sweep stays single-rule-isolated.
set -euo pipefail

YAML=scripts/load/ghz/scenarios.yaml
echo "parsing $YAML"

# Data guard tokens (mirror the real run-ghz.sh PLAN parser): json.loads
# validates every payload before any ghz invocation; a JSON-parse failure
# emits the literal token 'not valid JSON' and aborts the run.
GUARD_MSG='not valid JSON'
guard_ok() { python3 -c 'import json,sys; json.loads(sys.argv[1])' '{}' 2>/dev/null; }
guard_ok && echo "data guard ok (would emit: $GUARD_MSG on bad payload)"