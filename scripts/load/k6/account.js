// Shared k6 harness: authenticates once per VU against the load-test SSO
// account, then exercises control-plane + legacy surfaces.
//
// Usage:
//   k6 run -e BASE_URL=http://localhost:8080 -e TOKEN=<snaplink-token> \
//     -e VUS=50 -e DURATION=5m scripts/load/k6/account.js
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const TOKEN = __ENV.TOKEN || '';
const VUS = Number(__ENV.VUS || 20);
const DURATION = __ENV.DURATION || '3m';

export const options = {
  scenarios: {
    control_plane: {
      executor: 'constant-vus',
      vus: Math.ceil(VUS * 0.6),
      duration: DURATION,
    },
    legacy_compat: {
      executor: 'constant-vus',
      vus: Math.floor(VUS * 0.4),
      duration: DURATION,
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<250', 'p(99)<1000'],
    'http_req_duration{scenario:control_plane}': ['p(95)<200'],
  },
};

const errRate = new Rate('failed_requests');
const aggLatency = new Trend('aggregation_latency_ms');

const params = {
  headers: {
    Authorization: `Bearer ${TOKEN}`,
    'Content-Type': 'application/json',
    'X-Request-ID': `k6-${__VU}-${__ITER}`,
  },
};

function get(path, tags) {
  const res = http.get(`${BASE}${path}`, params, tags);
  const ok = res.status === 200;
  errRate.add(!ok, tags);
  if (!ok) {
    // Partial aggregate responses are 200 with partial:true; anything else is a real error.
    check(res, { [`${path} status 200`]: (r) => r.status === 200 });
  }
  return res;
}

export default function () {
  const scenario = __ENV.SCENARIO || '';
  if (scenario === 'legacy' || __ITER % 5 === 4) {
    // Legacy compat surface (ADR-011): kept live, expect Deprecation header.
    const r = get('/v1/files?limit=20', { tags: { scenario: 'legacy' } });
    check(r, {
      'legacy deprecation header': (res) => res.headers.Deprecation === 'true',
    });
    get('/v1/messages?limit=20', { tags: { scenario: 'legacy' } });
    get('/v1/comments?target_type=file&target_id=demo', { tags: { scenario: 'legacy' } });
    sleep(1);
    return;
  }

  // Control plane: account identity + local projections (no sync fan-out).
  get('/v1/me', { tags: { scenario: 'control_plane' } });
  const t0 = Date.now();
  const overview = get('/v1/me/overview', { tags: { scenario: 'control_plane' } });
  aggLatency.add(Date.now() - t0, { scenario: 'control_plane' });
  check(overview, {
    'overview data envelope': (res) => res.json('data') !== null,
  });
  get('/v1/me/sources?limit=50', { tags: { scenario: 'control_plane' } });
  get('/v1/me/memberships?limit=50', { tags: { scenario: 'control_plane' } });
  get('/v1/account-datasets', { tags: { scenario: 'control_plane' } });
  get('/v1/me/data?dataset=aero-im.workspaces&consistency=eventual', { tags: { scenario: 'control_plane' } });
  sleep(0.5);
}
