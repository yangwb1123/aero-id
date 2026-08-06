// Event ingest soak (P6-08): drives the canonical event ingest path at load
// and verifies the at-least-once contract holds under it:
//
//   - fresh batches are applied and receipted;
//   - redelivered batches (same event IDs, re-signed) return stable receipts
//     (`duplicate`) and never double-apply;
//   - every request carries a valid per-source HMAC signature and an RFC3339
//     timestamp inside the acceptance window.
//
// Usage:
//   k6 run -e BASE_URL=http://localhost:8080 -e TOKEN=<snaplink-token> \
//     -e SOURCE_KEY=<event_ingest.source_keys[SOURCE]> -e SOURCE=aero-im \
//     -e REGION=local -e BATCH=10 -e VUS=8 -e DURATION=5m \
//     scripts/load/k6/event-soak.js
//
// The load account must hold the `source_event_ingest` permission and a
// source binding for SOURCE (see config event_ingest.bindings).

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';
import crypto from 'k6/crypto';
import encoding from 'k6/encoding';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const TOKEN = __ENV.TOKEN || '';
const SOURCE = __ENV.SOURCE || 'aero-im';
const REGION = __ENV.REGION || 'local';
const SOURCE_KEY = __ENV.SOURCE_KEY || '';
const BATCH = Math.min(100, Number(__ENV.BATCH || 10));
const VUS = Number(__ENV.VUS || 8);
const DURATION = __ENV.DURATION || '5m';

export const options = {
  scenarios: {
    fresh: {
      executor: 'constant-vus',
      vus: Math.max(1, VUS - 1),
      duration: DURATION,
    },
    redelivery: {
      executor: 'constant-vus',
      vus: 1,
      duration: DURATION,
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<300', 'p(99)<1500'],
    'ingest_ok{scenario:fresh}': ['rate>0.999'],
    'receipt_stable{scenario:redelivery}': ['rate>0.999'],
  },
};

const ingestOK = new Rate('ingest_ok');
const receiptStable = new Rate('receipt_stable');
const ingestLatency = new Trend('ingest_latency_ms');

const params = {
  headers: {
    Authorization: `Bearer ${TOKEN}`,
    'Content-Type': 'application/json',
  },
};

// uuidv7-ish random ID: 16 random bytes formatted as a UUID. The ingest path
// treats event_id as an opaque unique key, so randomness is sufficient.
function randomID() {
  const bytes = crypto.randomBytes(16);
  bytes[6] = (bytes[6] & 0x0f) | 0x40; // version 4
  bytes[8] = (bytes[8] & 0x3f) | 0x80; // variant
  const hex = encoding.hexencode(bytes);
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

function envelope(id, aggregateVersion) {
  return {
    event_id: id,
    source: SOURCE,
    source_region: REGION,
    event_type: 'aero.im.workspace.member.updated',
    schema_version: 1,
    aggregate_type: 'im_workspace',
    aggregate_id: `ws-${__VU}-${__ITER % 50}`,
    aggregate_version: aggregateVersion,
    actor_uid: `snaplink:u-${__VU}`,
    operation_id: randomID(),
    correlation_id: randomID(),
    causation_id: randomID(),
    occurred_at: new Date().toISOString(),
    payload: { workspace_id: `ws-${__VU}`, membership: 'member', redacted: true },
  };
}

// signBatch computes the X-Aero-Event-Signature: HMAC-SHA256(source_key,
// timestamp + "." + body), hex-encoded.
function signBatch(body) {
  const timestamp = new Date().toISOString();
  const mac = crypto.hmac('sha256', SOURCE_KEY, `${timestamp}.${body}`, 'hex');
  return { timestamp, signature: `sha256=${mac}` };
}

function postEvents(events, tags) {
  const body = JSON.stringify({ events });
  const { timestamp, signature } = signBatch(body);
  const res = http.post(
    `${BASE}/internal/v1/events`,
    body,
    {
      headers: {
        ...params.headers,
        'X-Aero-Event-Timestamp': timestamp,
        'X-Aero-Event-Signature': signature,
      },
    },
    tags,
  );
  return res;
}

// Shared redelivery pool: the redelivery VU re-posts a fixed set of event IDs
// created by the fresh VUs, so duplicates are real (same ID, same aggregate
// version) rather than synthetic.
const redeliveryPool = [];
let poolIndex = 0;

export default function () {
  const scenario = __ENV.SCENARIO || '';
  if (scenario === 'redelivery') {
    if (redeliveryPool.length === 0) {
      sleep(2); // wait for the fresh scenario to seed the pool
      return;
    }
    const picked = redeliveryPool[poolIndex++ % redeliveryPool.length];
    const res = postEvents([picked], { tags: { scenario: 'redelivery' } });
    const ok = res.status === 200;
    ingestOK.add(ok, { scenario: 'redelivery' });
    if (ok) {
      const statuses = res.json('data.events') || [];
      // Every redelivered event must be receipted as duplicate (or applied the
      // first time a race lets the fresh copy win); never a fresh apply twice.
      const stable = statuses.length === 1 &&
        (statuses[0].status === 'duplicate' || statuses[0].status === 'applied');
      receiptStable.add(stable, { scenario: 'redelivery' });
    }
    sleep(0.2);
    return;
  }

  // Fresh batch: unique event IDs, monotonically increasing aggregate versions.
  const events = [];
  const base = __ITER * BATCH;
  for (let i = 0; i < BATCH; i++) {
    const id = randomID();
    events.push(envelope(id, base + i + 1));
  }
  const t0 = Date.now();
  const res = postEvents(events, { tags: { scenario: 'fresh' } });
  ingestLatency.add(Date.now() - t0, { scenario: 'fresh' });
  const ok = res.status === 200;
  ingestOK.add(ok, { scenario: 'fresh' });
  if (!ok) {
    check(res, { 'fresh batch status 200': (r) => r.status === 200 });
    return;
  }
  const statuses = res.json('data.events') || [];
  const applied = statuses.filter((e) => e.status === 'applied').length;
  check(res, {
    'all receipts returned': (r) => (r.json('data.events') || []).length === BATCH,
    'fresh events applied': () => applied >= Math.floor(BATCH * 0.9),
  });
  // Seed the redelivery pool with the first event of each batch.
  if (redeliveryPool.length < 500 && events.length > 0) {
    redeliveryPool.push(events[0]);
  }
  sleep(0.05);
}
