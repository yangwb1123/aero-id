Now I have a thorough understanding of the entire subsystem. Let me compile the security review.

---

# Security Review: Redis Idempotency Subsystem (TASK-011 & TASK-012)

## Overall Security Posture: Needs Improvement

The implementation is clean and well-structured, but has **one critical**, **two high**, and **several medium** security findings that must be addressed before production deployment.

---

## Findings

### Finding 1 — No TLS for Redis (Plaintext Secrets on Wire)

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **Critical** |
| **Title** | Redis connection lacks TLS, exposing password and response data |
| **Location** | `internal/pkg/redis/redis.go:28-41` |
| **Description** | The `rueidis.ClientOption` struct has a `TLSConfig *tls.Config` field that is never populated. The Redis password (loaded from `AERO_ID_REDIS_PASSWORD` env) and all idempotency response bodies (which may contain PII, message content, file metadata) travel in cleartext over the network. Every other external connection in aero-id (SSO gRPC, tracing OTLP) supports TLS/mTLS, but Redis is a gap. |
| **Attack Scenario** | An attacker with access to the network segment between aero-id and Redis (e.g., compromised container host, cloud VPC misconfiguration, shared network in colo) can: (1) sniff the Redis AUTH password via passive TCP capture, (2) read all idempotency response bodies containing user data, (3) replay the stolen password to connect directly to Redis and exfiltrate or tamper with all keys. |
| **Impact** | Full disclosure of Redis password and all idempotency records. Potential lateral movement to Redis data used by other subsystems (SSO cache, leader election, rate limiter in future tasks). |
| **Recommendation** | Add TLS configuration to `RedisConfig` mirroring the existing SSO TLS pattern (`tls_ca_file`, `tls_cert_file`, `tls_key_file`, `tls_server_name`, `tls_cert_pins`), and pass the `*tls.Config` to `rueidis.ClientOption.TLSConfig`. For self-signed/development setups, add an `insecure_skip_verify` bool (default false). |

**Code Changes Required:**

1. **`internal/config/config.go`** — Add TLS fields to `RedisConfig`:
```go
type RedisConfig struct {
    // ... existing fields ...
    
    // TLS transport security (empty fields = plaintext TCP).
    TLSCAFile     string   `mapstructure:"tls_ca_file"`
    TLSCertFile   string   `mapstructure:"tls_cert_file"`
    TLSKeyFile    string   `mapstructure:"tls_key_file"`
    TLSServerName string   `mapstructure:"tls_server_name"`
    TLSCertPins   []string `mapstructure:"tls_cert_pins"`
    TLSInsecure   bool     `mapstructure:"tls_insecure_skip_verify"`
}
```

2. **`internal/pkg/redis/redis.go`** — Build `*tls.Config` and wire it into `rueidis.ClientOption`.

3. **`configs/config.yaml`** — Add commented-out TLS section under `redis:`.

| **Effort** | L (3 days: config + TLS loading + cert pinning + testing with TLS Redis) |
|---|---|

---

### Finding 2 — No Input Validation on `Idempotency-Key`

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | Idempotency-Key header lacks length and character validation |
| **Location** | `internal/middleware/idempotency.go:27-28`, `internal/idempotency/store_redis.go:57` |
| **Description** | The `Idempotency-Key` header value is used directly in the Redis key construction at `redisKey(uid, endpoint, key)` → `"idempot:" + uid + ":" + endpoint + ":" + key`. There is no validation of the key's length, character set, or format. An attacker could: (1) send a 1MB key → huge Redis key name wasting memory and bandwidth; (2) embed null bytes, newlines, or binary data into the key; (3) craft keys with colons to create collisions with other uid/endpoint combinations. |
| **Attack Scenario** | **Memory exhaustion**: Send POST requests with 100KB `Idempotency-Key` values at high rate. Each creates a Redis key that persists for up to 24h. At 1000 RPS × 100KB keys, Redis fills memory in minutes, triggering eviction or OOM kill. **Key collision**: If uid happens to contain a colon (unlikely from SSO but possible), or the key contains `"u1:/v1/files:"` prefix, it could impersonate a different user's key. |
| **Impact** | Redis out-of-memory condition leading to data loss (other subsystems' keys evicted), degraded SSO cache, failed idempotency checks, or complete Redis DoS. |
| **Recommendation** | Add input validation in the idempotency middleware before calling `Reserve`: |

```go
// In internal/middleware/idempotency.go, after getting key:
if key == "" {
    c.Next()
    return
}
// --- ADD ---
if len(key) > 128 {
    c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{
        "code":    "invalid_idempotency_key",
        "message": "Idempotency-Key must be 128 characters or fewer",
    }})
    return
}
if !isValidIdempotencyKey(key) {
    c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{
        "code":    "invalid_idempotency_key",
        "message": "Idempotency-Key must contain only printable ASCII characters",
    }})
    return
}
```

Also, add a **per-actor key rate limit** in the store to prevent mass creation:
- Track `set` rate of `idempot:{uid}:*` key creations per minute
- Reject if a single uid creates > N distinct keys in T seconds

| **Effort** | S (validation logic + tests: < 1 day) |
|---|---|

---

### Finding 3 — No Memory/Eviction Guardrails for Idempotency Keys

| Field | Value |
|-------|-------|
| **Category** | Denial of Service / Threat Model |
| **Severity** | **High** |
| **Title** | No max-key limit or memory quota on idempotency Redis keys |
| **Location** | `internal/idempotency/store_redis.go:51-83` |
| **Description** | The `Reserve` method blindly creates a new Redis key for every unique `(uid, endpoint, key)` tuple with a 2-minute TTL. An attacker (either an external user sending many unique `Idempotency-Key` values, or a compromised/malicious actor) can generate an unbounded number of keys. Redis memory is finite; once exhausted, Redis evicts keys via `maxmemory-policy` (if configured) or starts returning OOM errors. This affects not just the idempotency subsystem but **all Redis consumers** (SSO cache, leader election, future rate limiter). |
| **Attack Scenario** | A single authenticated user sends `POST /v1/messages` with `Idempotency-Key: attack-000001` through `attack-999999`. Each creates a key `idempot:{uid}:POST /v1/messages:attack-NNNNNN` with a 2-minute TTL. At 1000 keys/second, the user stores 120K keys in 2 minutes. If each key and its JSON payload averages ~200 bytes, that's ~24MB — but sustained at high rate, it grows further. Redis maxmemory default is typically 0 (no limit) or a fraction of system RAM, so this can easily OOM a small Redis instance. |
| **Impact** | Redis OOM → all cached data lost (SSO tokens, user caches, outbox leader lease, rate limiter state). Cascading failure: SSO calls spike to upstream, idempotency fails open (which is correct per design), but Redis restart also causes a burst of reconnections. |
| **Recommendation** | Implement **one or more** of: |

1. **Per-actor key quota**: Redis sorted set `idempot:quota:{uid}` with TTL, tracking key creation rate. Reject if > N keys/min.
2. **Redis maxmemory-policy enforcement**: Document that `maxmemory-policy volatile-ttl` or `allkeys-lru` should be configured on the Redis server. Add startup warning if `INFO maxmemory` returns 0.
3. **Application-level circuit breaker**: Track Redis memory usage via `INFO memory` periodically and log a critical warning if `used_memory / maxmemory > 0.8`.
4. **Key size budget**: Enforce that `len(payload) < MaxValueSize` (e.g., 64KB) in the Redis store.

| **Effort** | M (1-2 days: quota tracking + tests + dashboards) |
|---|---|

---

### Finding 4 — Password Discoverable via Timing Side-Channel in Connection Ping

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Authentication |
| **Severity** | Medium |
| **Title** | Redis password validated on every startup via plaintext PING; no AUTH error differentiation |
| **Location** | `internal/pkg/redis/redis.go:43-47` |
| **Description** | The startup ping (`client.Do(ctx, client.B().Ping().Build())`) succeeds only after the AUTH command is sent internally by rueidis. If the password is wrong, rueidis returns an authentication error. However, the error message from `Ping()` is not differentiated from a network error. While this doesn't directly leak the password, the error string `"redis ping: AUTH failed"` vs `"redis ping: connection refused"` provides **different leak channels**: timing (AUTH failure is deterministic once TCP is established) and error message content. This helps an attacker distinguish "wrong password" from "network down." |
| **Attack Scenario** | An attacker with partial network access can brute-force the Redis password without needing to see the AUTH response directly — they just measure whether the error includes "AUTH" or not. In practice this requires the attacker to be on the same network segment as aero-id or Redis. |
| **Impact** | Low in isolation, but combined with Finding 1 (no TLS) the password can be brute-forced more easily. |
| **Recommendation** | Wrap the ping error to strip authentication details: |

```go
if err := client.Do(ctx, client.B().Ping().Build()).Error(); err != nil {
    client.Close()
    return nil, fmt.Errorf("redis ping: %w", err)  // Use %w not %v to hide details
}
```

Furthermore, use `errors.Is` to detect authentication errors and return a generic "Redis authentication failed" message rather than surfacing the underlying detail.

| **Effort** | S (trivial code change) |
|---|---|

---

### Finding 5 — In-Progress Conflict Key Is Not Authenticated Per-Caller

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | Medium |
| **Title** | Idempotency conflict does not verify caller identity matches existing reservation owner |
| **Location** | `internal/middleware/idempotency.go:36-50`, `internal/idempotency/store_redis.go:51-83` |
| **Description** | When a second caller hits an in-progress idempotency key, they receive a 409 Conflict. The `uid` passed to `Reserve` comes from the JWT (set as `actor_uid` in the auth middleware). The key includes the uid as a prefix component, so `idempot:u1:...` and `idempot:u2:...` are different keys. **However**, if the uid itself can be spoofed or if an endpoint accepts requests without authentication (e.g., public endpoints that might be added later), the uid could be empty or attacker-controlled. More critically: the current implementation assumes the `uid` is correctly authenticated and bound to the request, but there is **no validity check on the uid** at the store level. The `reserveKey` function blindly trusts all three inputs. |
| **Attack Scenario** | If a public endpoint is ever added to the idempotency middleware chain (currently all idempotent endpoints are authenticated via the `auth` middleware, so uid is set), an unauthenticated request would pass `uid=""` to the store. All unauthenticated callers would collide on the same key `idempot::POST /v1/something:key`, causing 409 conflicts for unrelated users. More insidiously: if an SSO bug returns an empty or attacker-controlled uid, the attacker could reserve keys under that uid and block legitimate requests. |
| **Impact** | Medium — requires another bug (SSO bypass, empty uid acceptance) to exploit. But the system should be resilient to this. |
| **Recommendation** | Add an explicit check in the idempotency middleware that validates `uid` is non-empty before calling `Reserve`: |

```go
uid := c.GetString("actor_uid")
if uid == "" {
    c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{
        "code":    "internal",
        "message": "missing actor identity",
    }})
    return
}
```

This is already stated in the invariants ("A validated-but-empty uid is rejected on both transports"), but the idempotency middleware should enforce it defensively rather than relying on earlier middleware alone.

| **Effort** | S |
|---|---|

---

### Finding 6 — Redis Key Format Vulnerable to Colon Injection Confusion

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | Low |
| **Title** | Colon-delimited key format can cause uid/endpoint mismatch if colons present in values |
| **Location** | `internal/idempotency/store_redis.go:57` |
| **Description** | The key format `"idempot:" + uid + ":" + endpoint + ":" + key` uses colons as delimiters. If any of these three components contains a colon, the key becomes ambiguous. Example: uid = `"user:1"`, endpoint = `"POST /v1/files"`, key = `"mykey"` → key `idempot:user:1:POST /v1/files:mykey`. A subsequent caller with uid = `"user"`, endpoint = `"1:POST /v1/files"`, key = `"mykey"` would get the same key. While SSO uids are UUIDs (no colons) and endpoint is server-constructed (`Method + " " + Path`), the `Idempotency-Key` header value is completely uncontrolled. |
| **Attack Scenario** | If an attacker can include a colon in their Idempotency-Key, they could craft a key that appears under a different user's namespace. Example: Idempotency-Key = `u2:POST /v1/messages:evil` with uid `u1` would produce key `idempot:u1:POST /v1/messages:u2:POST /v1/messages:evil`. This would NOT collide with u2's key `idempot:u2:POST /v1/messages:evil`, but it creates confusing key patterns. More dangerous: if the endpoint string somehow contains colons (e.g., a future route parameter). |
| **Impact** | Low — no practical exploit currently because uid and endpoint are controlled and predictable. But a future code change or SSO integration could introduce colons. |
| **Recommendation** | Use a fixed-width or length-prefixed key format, or validate that idempotency keys do not contain colons. Alternatively, switch to a hash-based key: `SHA256(uid + "|" + endpoint + "|" + key)` to eliminate delimiter ambiguity entirely. Or simply use a Redis hash/JSON document keyed by a composite struct, but that's more complex. The simplest fix: validate `key` does not contain `:` and length < 128. |

| **Effort** | S (validation < 1 day) |
|---|---|

---

### Finding 7 — `Release` Silently Drops Errors — Hides Tampering

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Repudiation |
| **Severity** | Low |
| **Title** | Release best-effort error swallowing hides malicious DEL failures |
| **Location** | `internal/idempotency/store_redis.go:139-143` |
| **Description** | `Release` returns `nil` regardless of whether the `DEL` command actually succeeded. The comment says "Errors are silently dropped because a best-effort delete is safe." This is true for normal operation (TTL will eventually clean up). However, it also hides: (1) attacks that delete keys via separate channels (making `Release` silently succeed when the key is already gone), (2) Redis connectivity issues that should be observable via metrics, (3) key expiration races that mask an attacker reserving a key after the real owner's release appears to succeed. |
| **Attack Scenario** | An attacker with Redis access (via stolen credentials, Finding 1) can `DEL` idempotency keys ahead of legitimate requests. The `Release` would still return `nil` on the victim, making the attack invisible in application logs. The legitimate caller would get a 409 Conflict because the key was re-reserved by the attacker. |
| **Impact** | Low — `Release` is only called on 5xx errors, so it's a rare path anyway. But the silent error swallow makes debugging harder. |
| **Recommendation** | Log the error at debug level and increment a metric counter: |

```go
func (s *RedisStore) Release(ctx context.Context, uid, endpoint, key string) error {
    k := redisKey(uid, endpoint, key)
    if err := s.client.Do(ctx, s.client.B().Del().Key(k).Build()).Error(); err != nil {
        // Best-effort: TTL will clean up anyway.
        log.Debug("idempotency release failed", zap.Error(err), zap.String("key", k))
        metrics.Inc("idempotency_release_errors")
    }
    return nil
}
```

| **Effort** | S |
|---|---|

---

### Finding 8 — No Observability for Security Events

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Repudiation |
| **Severity** | Medium |
| **Title** | No audit logging for idempotency conflicts or replay events |
| **Location** | `internal/middleware/idempotency.go:23-62` |
| **Description** | The idempotency middleware handles three interesting security events: (1) conflict (409) — a duplicate request detected, (2) replay — a completed response was replayed, (3) store failure — the store returned an error and the request was allowed through (fail-open). None of these are logged or emitted as metrics. In a production security incident, knowing that a user received a replayed response (possible indicator of a race condition or attempted replay attack) or that the store is failing (fail-open could be exploited) is valuable for forensic analysis. |
| **Attack Scenario** | An attacker exploits a race condition by sending multiple duplicate requests simultaneously, hoping one bypasses the idempotency check due to a store error. If the store fails, the middleware fails open (passes through). Without logging/metrics, this attack vector is invisible to the operations team. |
| **Impact** | Medium — fail-open is the correct design choice (idempotency should never block writes), but without observability, silent store degradation is indistinguishable from normal operation, masking potential abuse. |
| **Recommendation** | Add structured logging at key decision points: |

```go
// In Idempotency middleware:
case err != nil:
    log.Warn("idempotency store error, failing open",
        zap.String("uid", uid),
        zap.String("endpoint", endpoint),
        zap.String("idem_key", key),
        zap.Error(err))
    c.Next()
case reserved:
    // ... existing logic ...
case rec.Completed:
    log.Debug("idempotent replay",
        zap.String("uid", uid),
        zap.String("endpoint", endpoint),
        zap.String("idem_key", key))
    replayIdempotent(c, rec)
default:
    log.Info("idempotency conflict",
        zap.String("uid", uid),
        zap.String("endpoint", endpoint),
        zap.String("idem_key", key))
    c.AbortWithStatusJSON(http.StatusConflict, ...)
```

Also add Prometheus counters: `idempotency_store_errors_total`, `idempotency_replays_total`, `idempotency_conflicts_total`.

| **Effort** | S (logging + metrics) |
|---|---|

---

### Finding 9 — Response Bodies May Contain PII, Stored Unencrypted in Redis

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Compliance |
| **Severity** | Medium |
| **Title** | Idempotency response bodies stored in Redis without encryption at rest or transit |
| **Location** | `internal/idempotency/store_redis.go:107-124` |
| **Description** | The `Complete` method stores the full HTTP response body (including JSON payloads with user-generated content, message bodies, file metadata) in Redis as a plaintext JSON value with TTL up to 24 hours. If the response body contains PII (message content, user profiles, file names with personal data), it is stored unencrypted. Redis has no encryption-at-rest. Combined with Finding 1 (no TLS), data is exposed both in-transit and at-rest. This could violate GDPR Article 32 (security of processing), SOC2 (CC6.1 encryption), or internal data protection policies. |
| **Attack Scenario** | An attacker who gains access to the Redis process (e.g., via Redis CLI, compromised container, or `docker exec` on the Redis container) can read `GET idempot:u1:*` to dump all recent response bodies for a user, potentially containing private messages, file names, or other sensitive content. |
| **Impact** | Data leakage of user-generated content. Regulatory risk (GDPR fines up to 4% of global revenue for inadequate technical measures). |
| **Recommendation** | **Option A (lightweight)**: Document in the architecture that idempotency responses may contain PII and ensure Redis is on an isolated network with TLS and strong AUTH. Add a configurable TTL cap (already 24h max). **Option B (defense in depth)**: Encrypt response bodies at the application layer before storing in Redis using an envelope encryption scheme with `AERO_ID_SIGNING_KEY` or a dedicated data encryption key. **Option C (GDPR-focused)**: Consider whether idempotency should store full response bodies at all — instead, store only status code and a reference, and rely on the client retrying with the real handler. |

The simplest and most impactful improvement: at minimum, ensure TLS is configured (Finding 1) and document the PII storage concern in the data retention section of the design doc.

| **Effort** | L (for Option B — envelope encryption) |
|---|---|

---

### Finding 10 — `rueidis` v1.0.76 May Have CVEs

| Field | Value |
|-------|-------|
| **Category** | Compliance / Supply Chain |
| **Severity** | Low |
| **Title** | Dependency version pinned but not audited for known vulnerabilities |
| **Location** | `go.mod:15` |
| **Description** | `github.com/redis/rueidis v1.0.76` is pinned. While rueidis is actively maintained, it's important to check that v1.0.76 has no known CVEs. The `go.mod` also pins `github.com/alicebob/miniredis/v2 v2.38.0` for testing only (test file excluded from production build, so this is low risk). |
| **Attack Scenario** | If a CVE is discovered in rueidis (e.g., RESP3 parsing vulnerability, memory exhaustion via malformed server response), the vulnerability would affect aero-id in production. |
| **Impact** | Varies depending on the CVE — could range from DoS to RCE in the worst case. |
| **Recommendation** | Add `go mod verify` and `govulncheck` to the CI pipeline (`make audit`). Run `govulncheck ./...` to detect known vulnerabilities. Set up Dependabot or Renovate for automated dependency updates. |

| **Effort** | S (tooling integration) |
|---|---|

---

## STRIDE Analysis Summary

| Threat | Finding | Severity |
|--------|---------|----------|
| **S**poofing | Identity via empty uid (Finding 5); No TLS for Redis password (Finding 1) | Medium / Critical |
| **T**ampering | No TLS means data in transit can be modified (Finding 1); Complete doesn't verify caller ownership (Finding 5) | Critical / Medium |
| **R**epudiation | Release errors silently dropped (Finding 7); No audit logging of conflicts/replays (Finding 8) | Low / Medium |
| **I**nformation Disclosure | Response bodies with PII unencrypted (Finding 9); No TLS (Finding 1) | Medium / Critical |
| **D**enial of Service | Unbounded key creation (Finding 3); No key size validation (Finding 2) | High / High |
| **E**levation of Privilege | Colon injection in keys (Finding 6); Empty uid bypass (Finding 5) | Low / Medium |

---

## Top 3 Critical Issues

1. **No TLS for Redis connections** — All Redis traffic (including password and user data) travels in cleartext. This is the highest priority fix before any production deployment that crosses network boundaries.

2. **No input validation on Idempotency-Key** — An attacker can create arbitrarily large keys and an unbounded number of them, leading to Redis memory exhaustion DoS that cascades to all subsystems sharing the Redis instance.

3. **No per-actor key quota** — A single authenticated user can flood Redis with millions of unique idempotency keys, causing eviction of SSO cache entries and potentially taking down the entire Redis-dependent subsystem.

---

## Top 3 Quick Wins

1. **Add Idempotency-Key validation** (S, < 1 day) — Reject keys > 128 chars or containing colons in the middleware before they reach the store. This alone prevents the most trivial DoS vector.

2. **Add audit logging and metrics to idempotency middleware** (S, < 1 day) — Log conflicts, replays, and store errors. Add Prometheus counters. This transforms the idempotency subsystem from a black box into an observable security boundary.

3. **Add empty-uid guard in idempotency middleware** (S, < 1 day) — Reject requests with empty `actor_uid` to harden against authentication bypass bugs in the SSO layer or future public endpoints.

---

## Security Debt

| Item | Effort | Priority |
|------|--------|----------|
| Redis TLS/mTLS support | L | Critical |
| Redis maxmemory-policy documentation & startup warning | S | High |
| Per-actor key creation quota | M | High |
| Dependency vulnerability scanning (`govulncheck`) | S | Medium |
| Response body encryption / PII documentation | M | Medium |
| Rueidis retry/fail behavior review (fail-open correctness under Redis degradation) | L | Medium |
| Penetration test of idempotency key collision and injection scenarios set up against miniredis | M | Low |

## References

- OWASP Top 10:2021 — **A01:2021 (Broken Access Control)**, **A03:2021 (Injection)**, **A04:2021 (Insecure Design)**, **A08:2021 (Software and Data Integrity Failures)**
- OWASP ASVS — V5 (Validation, Sanitization and Encoding), V2 (Authentication), V9 (Communications)
- NIST SP 800-53 Rev. 5 — SC-8 (Transmission Confidentiality and Integrity), SC-13 (Cryptographic Protection), AU-6 (Audit Review, Analysis, and Reporting)
- GDPR Article 32 — Security of processing (appropriate technical measures, pseudonymisation, encryption)
- SOC 2 CC6.1 — Logical and physical access controls; CC6.7 — Transmission encryption
