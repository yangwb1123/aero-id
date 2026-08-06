// Callback soak (P6-08): exercises the callback platform end to end against a
// local sink receiver:
//
//   - registers subscriptions pointing at the sink (endpoint verification with
//     the challenge handshake must succeed);
//   - triggers audited account operations so durable audit facts enqueue
//     callback publications;
//   - polls the sink and asserts deliveries arrive (at-least-once, signed with
//     X-Aero-Callback-Signature);
//   - cleans up subscriptions.
//
// Usage:
//   go run ./scripts/load/sink -addr :9091 &
//   k6 run -e BASE_URL=http://localhost:8080 -e TOKEN=<snaplink-token> \
//     -e RECEIVER_URL=http://127.0.0.1:9091 -e VUS=4 -e DURATION=5m \
//     -e PER_VU_DELIVERIES=20 scripts/load/k6/callback-soak.js
//
// The load account needs the `callback.manage` permission and a verified
// tenant. RECEIVER_URL must be reachable from the aero-id cell; when
// callback.allow_private_target is false the sink host must resolve publicly
// (use the cell's own DNS name, not 127.0.0.1).

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const TOKEN = __ENV.TOKEN || '';
const RECEIVER = __ENV.RECEIVER_URL || 'http://127.0.0.1:9091';
const VUS = Number(__ENV.VUS || 4);
const DURATION = __ENV.DURATION || '5m';
const PER_VU = Number(__ENV.PER_VU_DELIVERIES || 20);

export const options = {
  scenarios: {
    callback_soak: {
      executor: 'constant-vus',
      vus: VUS,
      duration: DURATION,
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<300', 'p(99)<1500'],
    'delivery_verified{scenario:callback_soak}': ['rate>0.99'],
    'signed_delivery{scenario:callback_soak}': ['rate>0.99'],
  },
};

const deliveryVerified = new Rate('delivery_verified');
const signedDelivery = new Rate('signed_delivery');
const registerLatency = new Trend('callback_register_ms');

const params = {
  headers: {
    Authorization: `Bearer ${TOKEN}`,
    'Content-Type': 'application/json',
    'X-Request-ID': `cb-${__VU}-${__ITER}`,
  },
};

function registerSubscription() {
  const t0 = Date.now();
  const res = http.post(
    `${BASE}/v1/admin/callback-subscriptions`,
    JSON.stringify({
      endpoint: RECEIVER,
      event_types: ['account.profile.updated'],
    }),
    params,
  );
  registerLatency.add(Date.now() - t0);
  if (res.status !== 201) {
    check(res, { 'register 201': (r) => r.status === 201 });
    return '';
  }
  return res.json('data.subscription.id') || '';
}

function deleteSubscription(id) {
  if (!id) {
    return;
  }
  const res = http.del(`${BASE}/v1/admin/callback-subscriptions/${id}`, null, params);
  check(res, { 'delete 200': (r) => r.status === 200 });
}

// triggerAuditFact mutates the caller's presentation profile so a durable
// `account.profile.updated` audit fact is committed (and enqueued for
// callback publication). Field-mask PATCH: only non-null fields are applied.
function triggerAuditFact() {
  const res = http.patch(
    `${BASE}/v1/me/profile`,
    JSON.stringify({ display_name: `load-${__VU}-${__ITER}` }),
    params,
  );
  check(res, { 'profile patch 200': (r) => r.status === 200 });
  return res.status === 200;
}

function sinkStats() {
  const res = http.get(`${RECEIVER}/stats`, { headers: { Accept: 'application/json' } });
  if (res.status !== 200) {
    return { verifications: -1, deliveries: -1, signed_deliveries: -1 };
  }
  return res.json();
}

export default function () {
  const subscriptionID = registerSubscription();
  if (!subscriptionID) {
    return;
  }
  const before = sinkStats();
  let triggered = 0;
  for (let i = 0; i < PER_VU; i++) {
    if (triggerAuditFact()) {
      triggered++;
    }
    sleep(0.1);
  }
  // Deliveries are asynchronous (worker interval + retry backoff): poll the
  // sink until every triggered fact arrived or the budget is exhausted.
  const deadline = Date.now() + 60_000;
  let delivered = 0;
  while (Date.now() < deadline) {
    const after = sinkStats();
    delivered = Math.max(0, after.deliveries - before.deliveries);
    if (delivered >= triggered && triggered > 0) {
      break;
    }
    sleep(1);
  }
  const after = sinkStats();
  const total = Math.max(0, after.deliveries - before.deliveries);
  const signedTotal = Math.max(0, after.signed_deliveries - before.signed_deliveries);
  deliveryVerified.add(triggered > 0 && total >= triggered, { scenario: 'callback_soak' });
  signedDelivery.add(triggered > 0 && signedTotal >= triggered, { scenario: 'callback_soak' });
  check({}, {
    'deliveries >= triggered facts': () => triggered > 0 && total >= triggered,
    'deliveries signed': () => triggered > 0 && signedTotal >= triggered,
  });
  deleteSubscription(subscriptionID);
  sleep(0.5);
}
