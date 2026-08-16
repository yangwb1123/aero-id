#!/usr/bin/env bash
# Resilient driver: run the r4/r5-materialized campaign directions one at a
# time with retries. Skips directions that already have an acceptance
# artifact (landed). Logs to docs/campaigns/driver.out + per-direction logs.
set -u
cd /home/u1/aero-id || exit 1

# materialized after 2026-08-15 15:20 (epoch 1755264000) with no acceptance
# artifact = pending
mapfile -t RUNS < /tmp/pending-final.txt

echo "== $(date +%H:%M) driver start: ${#RUNS[@]} pending directions =="
echo "${RUNS[@]}" | tr ' ' '\n' | sed 's/^/  /'

for name in "${RUNS[@]}"; do
  pipe="docs/auto/runs/$name/pipeline.yaml"
  # landed while we were running? (acceptance artifact appears on success)
  acc="docs/auto/runs/$name/archive"
  if [ -d "$acc" ] && ls "$acc"/*/task-1-acceptance.md >/dev/null 2>&1; then
    echo "== $(date +%H:%M) SKIP (already landed): $name"
    continue
  fi
  ok=0
  for attempt in 1 2 3 4 5; do
    echo "== $(date +%H:%M) RUN $name attempt $attempt"
    python3 pi-batch.py --pipeline "$pipe" --reuse --reuse-legacy --retries 1 \
      --log-file "docs/campaigns/driver-${name:0:40}.log" >> docs/campaigns/driver.out 2>&1
    rc=$?
    if [ $rc -eq 0 ]; then
      echo "== $(date +%H:%M) LANDED $name"
      ok=1
      break
    fi
    echo "== $(date +%H:%M) FAILED $name attempt $attempt rc=$rc (waiting 120s)"
    sleep 120
  done
  if [ $ok -eq 0 ]; then
    echo "== $(date +%H:%M) GAVE UP $name after 5 attempts"
  fi
  # agent-health pause between directions
  sleep 30
done
echo "== $(date +%H:%M) driver complete =="
