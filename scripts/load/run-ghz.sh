#!/usr/bin/env bash
# ghz load scenarios (P6-08). Usage: run-ghz.sh <bearer-token> [grpc-addr]
#
# HOW SCENARIOS ARE DEFINED: scripts/load/ghz/scenarios.yaml is the single
# canonical source. This script is a pure executor — it parses that YAML
# (python3; no PyYAML dependency, the flat fixed schema is parsed directly),
# runs one ghz campaign per scenario with the per-scenario concurrency /
# duration / data / call from the YAML, and enforces the per-scenario p95 /
# p99 SLO targets fail-closed on the ghz JSON output. There are deliberately
# NO inline scenario definitions here; scripts/check-load-scenarios.sh (R2)
# reddens if a divergent inline array is ever reintroduced.
#
# SLO overrides (CI/regression runs against slower environments):
#   GZ_SLO_P95_MS=150 GZ_SLO_P99_MS=400 run-ghz.sh <token>   # ALL scenarios
# Both are integer milliseconds; they override the YAML p95/p99 targets.
#
# ghz JSON shape (pinned): the report's "latencyDistribution" array holds
# {percentage: <number>, latency: <nanoseconds>} entries (ghz emits
# 50/75/90/95/99/99.9/99.99/100). "percentile" is accepted as a documented
# alias for schema drift. Missing/unparsable JSON or missing percentile
# entries FAIL — never pass on missing data. The SLO checker runs after
# every campaign; a breach fails this script (and make load-test-grpc) with
# a non-zero exit after all scenarios have reported.
set -euo pipefail

TOKEN="${1:?usage: run-ghz.sh <bearer-token> [grpc-addr]}"
ADDR="${2:-localhost:9090}"
PROTO=api/proto/aero_id/v1/aero_id.proto
OUT=scripts/load/results
YAML=scripts/load/ghz/scenarios.yaml
mkdir -p "$OUT"

# Parse the canonical YAML into one TSV record per scenario:
#   name<TAB>call<TAB>data<TAB>concurrency<TAB>duration<TAB>p95_ms<TAB>p99_ms
# Malformed YAML (missing/unknown fields, bad formats) aborts the run BEFORE
# any ghz invocation — no half-campaigns. The `data:` field must be the
# canonical single-quoted YAML scalar '<json>': the parser decodes it and
# proves the payload parses (json.loads) — a quoted-but-invalid or unquoted
# payload aborts before any ghz campaign too (same fail-fast contract).
PLAN="$(python3 - "$YAML" <<'PYEOF'
import json
import re
import sys

path = sys.argv[1]
fields = ("name", "call", "data", "concurrency", "duration", "p95", "p99")
scen_re = re.compile(r"^  - name: (\S+)$")
field_re = re.compile(r"^    ([a-z0-9]+): (.*)$")


def fail(line, msg):
    sys.stderr.write("ERROR: %s line %d: %s\n" % (path, line, msg))
    sys.exit(1)


def to_ms(v):
    return int(v[:-2]) if v.endswith("ms") else int(v[:-1]) * 1000


def decode_data(value, name):
    """Decode the canonical single-quoted YAML `data:` scalar ('<json>')

    and prove the payload parses as JSON (json.loads). Any other form
    (unquoted, empty) or a JSON-parse failure (ValueError/TypeError) aborts
    the PLAN build fail-fast — BEFORE any ghz invocation — so a broken
    payload can never start a half-campaign. YAML '' -> ' escapes inside
    the quotes are unescaped per the YAML single-quoted-scalar rule.
    """
    if not (len(value) >= 2 and value.startswith("'") and value.endswith("'")):
        fail(-1, "scenario %s: data %r not a single-quoted payload"
             % (name, value))
    payload = value[1:-1].replace("''", "'")
    try:
        json.loads(payload)
    except (ValueError, TypeError) as e:
        fail(-1, "scenario %s: data payload is not valid JSON (%s)"
             % (name, e))
    return payload


scenarios = []
current = None
seen_top = False
with open(path, encoding="utf-8") as f:
    for ln, raw in enumerate(f, 1):
        line = raw.rstrip("\n")
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if not seen_top:
            if line != "scenarios:":
                fail(ln, "expected top-level 'scenarios:'")
            seen_top = True
            continue
        m = scen_re.match(line)
        if m:
            current = {"name": m.group(1)}
            scenarios.append(current)
            continue
        m = field_re.match(line)
        if m and current is not None:
            if m.group(1) in current:
                fail(ln, "duplicate field '%s'" % m.group(1))
            if m.group(1) not in fields:
                fail(ln, "unknown field '%s'" % m.group(1))
            current[m.group(1)] = m.group(2).strip()
            continue
        fail(ln, "unexpected line")

if not scenarios:
    sys.stderr.write("ERROR: %s: no scenarios defined\n" % path)
    sys.exit(1)

# Phase 1: validate EVERY scenario (including the decoded, JSON-checked
# data payload) BEFORE emitting any TSV row — a poisoned scenario anywhere
# in the file aborts with zero rows and zero ghz campaigns.
for i, s in enumerate(scenarios, 1):
    missing = [k for k in fields if k not in s or not s[k]]
    if missing:
        fail(-1, "scenario %d (%s) missing field(s): %s"
             % (i, s.get("name", "?"), ", ".join(missing)))
    if not re.fullmatch(r"[A-Za-z0-9-]+", s["name"]):
        fail(-1, "scenario %d: invalid name %r" % (i, s["name"]))
    call_parts = s["call"].split(".")
    if (len(call_parts) != 3 or call_parts[0] != "aeropb"
            or not re.fullmatch(r"[A-Za-z0-9_]+", call_parts[1])
            or not re.fullmatch(r"[A-Za-z0-9_]+", call_parts[2])):
        fail(-1, "scenario %s: invalid call %r (want aeropb.<Service>.<Method>)"
             % (s["name"], s["call"]))
    if not re.fullmatch(r"[0-9]+", s["concurrency"]):
        fail(-1, "scenario %s: concurrency %r not numeric"
             % (s["name"], s["concurrency"]))
    if not re.fullmatch(r"[0-9]+(ms|s|m|h)", s["duration"]):
        fail(-1, "scenario %s: duration %r not a Go duration string"
             % (s["name"], s["duration"]))

    for k in ("p95", "p99"):
        if not re.fullmatch(r"[0-9]+(ms|s)", s[k]):
            fail(-1, "scenario %s: %s %r not [0-9]+(ms|s)"
                 % (s["name"], k, s[k]))
    s["data"] = decode_data(s["data"], s["name"])

# Phase 2: emit the TSV rows — only reachable when every scenario validated.
for s in scenarios:
    print("\t".join([s["name"], s["call"], s["data"], s["concurrency"],
                     s["duration"], str(to_ms(s["p95"])), str(to_ms(s["p99"]))]))
PYEOF
)"

slo_fail=0
scenario_count=0
while IFS=$'\t' read -r name call data concurrency duration p95_ms p99_ms; do
  scenario_count=$((scenario_count + 1))
  P95_MS="${GZ_SLO_P95_MS:-$p95_ms}"
  P99_MS="${GZ_SLO_P99_MS:-$p99_ms}"
  service="${call#*.}"
  service="${service%.*}"
  rpc="${call##*.}"
  echo "==> ghz: $service/$rpc"
  ghz --insecure --proto "$PROTO" \
      --call "$call" \
      -d "$data" --metadata "authorization=Bearer $TOKEN" \
      --concurrency "$concurrency" --duration "$duration" \
      --out "$OUT/ghz-$name.json" --format json \
      "$ADDR"
  if ! python3 - "$OUT/ghz-$name.json" "$name" "$P95_MS" "$P99_MS" <<'PYEOF'
import json
import sys

path, name = sys.argv[1], sys.argv[2]
p95_ms, p99_ms = int(sys.argv[3]), int(sys.argv[4])

try:
    with open(path, encoding="utf-8") as f:
        report = json.load(f)
except Exception as e:
    print("SLO FAIL %s unreadable-result (%s)" % (name, e))
    sys.exit(1)

dist = report.get("latencyDistribution")
if not isinstance(dist, list) or not dist:
    print("SLO FAIL %s no latencyDistribution entries (zero completed requests?)" % name)
    sys.exit(1)


def find_ms(pct):
    for entry in dist:
        if not isinstance(entry, dict):
            continue
        p = entry.get("percentage", entry.get("percentile"))
        if isinstance(p, (int, float)) and abs(p - pct) < 0.1:
            lat = entry.get("latency")
            if isinstance(lat, (int, float)):
                return int(lat) // 1_000_000
    return None


a95, a99 = find_ms(95), find_ms(99)
if a95 is None or a99 is None:
    print("SLO FAIL %s latencyDistribution missing p95/p99 entries" % name)
    sys.exit(1)

failed = 0
if a95 > p95_ms:
    print("SLO FAIL %s p95=%sms > target=%sms" % (name, a95, p95_ms))
    failed = 1
if a99 > p99_ms:
    print("SLO FAIL %s p99=%sms > target=%sms" % (name, a99, p99_ms))
    failed = 1
if failed:
    sys.exit(1)
print("SLO OK %s p95=%sms p99=%sms" % (name, a95, a99))
PYEOF
  then
    slo_fail=$((slo_fail + 1))
  fi
done <<< "$PLAN"

echo "done; results in $OUT"
echo "SLO summary: $scenario_count scenarios, $slo_fail failed"
if [ "$slo_fail" -gt 0 ]; then exit 1; fi
exit 0
