Now I have a thorough understanding of the entire codebase. Let me compile the complete security review.

---

# Security Review: aero-id Subsystem

## Overall Security Posture: **Needs Improvement**

The subsystem demonstrates strong security awareness — HMAC signing with constant-time comparison, circuit breakers, gRPC transport hardening, unified error handling, and structured audit logging are all present. However, several **critical-by-default** configurations and **actively dangerous** defaults (rate limiting off, CORS wildcard, default signing key committed) mean the service is **not production-ready without explicit operator intervention**.

---

## Detailed Findings

### 🔴 Critical

| Category | Severity | Title |
|---|---|---|
| **Security Misconfiguration** | **Critical** | Default signing key committed to repository |
| **Denial of Service** | **Critical** | Rate limiting disabled by default |
| **Security Misconfiguration** | **Critical** | CORS permits all origins by default |
| **Logging & Monitoring** | **Critical** | Audit logging errors silently discarded everywhere |

---

#### Finding 1: Default signing key in config

| Field | Value |
|---|---|
| **Category** | Security Misconfiguration / Cryptography |
| **Severity** | **Critical** |
| **Title** | Default signing key committed to configuration |
| **Location** | `configs/config.yaml` line ~68: `signing_key: change-me-please-very-long-random` |
| **Description** | The HMAC-SHA256 key used to sign presigned download URLs and (potentially) other integrity-critical operations has a hardcoded, predictable default value. The signing key is the sole gate protecting anonymous file downloads; anyone who discovers this key can forge valid download URLs for any file stored on a `local` backend. |
| **Attack Scenario** | An attacker reads the public config (committed to git), computes `HMAC-SHA256("change-me-please-very-long-random", "key|expiry")` for any known storage key, and downloads any file anonymously. No token required. |
| **Impact** | Complete confidentiality failure for all stored files on any instance that did not override the default. Blast radius includes every object in local storage. |
| **Recommendation** | **Immediate:** Add a startup-time validation that rejects the default key in production mode. **Permanent:** Generate the key at deploy time via environment variable (`AERO_ID_SIGNING_KEY`) and validate that it is not the default at startup. Add a config validation function: |

```go
// in config/config.go or a validation module
var defaultSigningKey = "change-me-please-very-long-random"

func (c *Config) Validate() error {
    if c.SigningKey == "" || c.SigningKey == defaultSigningKey {
        return fmt.Errorf("signing_key must be overridden from the default in production")
    }
    // ... other validations
}
```

| **Effort** | S (< 1 day) |

---

#### Finding 2: Rate limiting disabled by default

| Field | Value |
|---|---|
| **Category** | Denial of Service |
| **Severity** | **Critical** |
| **Title** | Rate limiting opt-in and disabled by default |
| **Location** | `configs/config.yaml` line ~55: `rate_limit: { enabled: false, rps: 20, burst: 40 }`; `internal/app/new.go` ~line 60 |
| **Description** | The per-actor/IP edge rate limiter is opt-in (`enabled: false`). All authenticated and public routes are unprotected until an operator explicitly enables it. A single compromised token or a botnet targeting public endpoints (health probes excluded) can exhaust server resources (DB connections, storage bandwidth, gRPC streams). |
| **Attack Scenario** | An attacker obtains one valid Bearer token (or targets an anonymous route like `/files/dl`) and issues 10,000 requests/second. No rate limiter blocks them. The DB connection pool (max 50) saturates, S3 API rate limits are exceeded, and legitimate users experience timeouts. |
| **Impact** | Full denial of service for all users until the attacker's IP/token is manually blocked upstream. |
| **Recommendation** | **Change the default** to `enabled: true` with sensible defaults (e.g. 20 rps / 40 burst). The `idle_ttl` memory concern is bounded (10m default, per-key ~100 bytes). For distributed deployments, add a Redis-backed limiter option. |

| **Effort** | S (< 1 day) |

---

#### Finding 3: CORS allows all origins by default

| Field | Value |
|---|---|
| **Category** | Security Misconfiguration |
| **Severity** | **Critical** |
| **Title** | CORS middleware defaults to permissive wildcard when origins list is empty |
| **Location** | `internal/middleware/cors.go` lines 16-18: `allowAll := len(origins) == 0` |
| **Description** | When the `CORSOrigins` config list is empty (the default), the middleware serves `Access-Control-Allow-Origin: *` on every response. This allows any website to make authenticated cross-origin requests that include credentials (if cookies were used) or to read responses. While Bearer tokens must be explicitly sent (not cookie-automated), the wildcard still exposes API responses to any site that a logged-in user visits. |
| **Attack Scenario** | An attacker hosts a page at `evil.com` that uses `fetch('https://aero-id.example.com/v1/search?q=...', {headers: {Authorization: 'Bearer ...'}})` to exfiltrate data. If the victim's token is accessible to JS, the wildcard CORS allows reading the response. More realistically, any XSS in a same-origin app can exfiltrate to any domain. |
| **Impact** | Violates the principle of least privilege for cross-origin access. Enables CSRF-like attacks on any user whose token is accessible in the browser (e.g., stored in `localStorage` and leaked via XSS, or via an OAuth implicit flow). |
| **Recommendation** | **Require explicit origin allowlist in production.** Change the default behavior to deny all when empty, or log a loud warning: |

```go
func CORS(origins []string) gin.HandlerFunc {
    if len(origins) == 0 {
        // In production, log or panic: CORS origins must be configured
        log.Warn("CORS: no origins configured; requests from browsers will be blocked")
    }
    // ... only allow listed origins
}
```

| **Effort** | S (< 1 day) |

---

#### Finding 4: Audit logging silently discards errors

| Field | Value |
|---|---|
| **Category** | Logging & Monitoring / Repudiation |
| **Severity** | **Critical** |
| **Title** | All audit `Log()` calls discard errors with blank identifier |
| **Location** | Multiple files: `service/admin.go` (lines 62, 83, 89), `service/ban.go` (lines 75, 106), `service/block.go` (lines 45, 70), `service/comment.go` (~line 325), `service/comment_reactions.go`, `service/file_share.go`, `service/message.go` — every instance uses `_ = s.audit.Log(...)` |
| **Description** | Every audit event is logged via best-effort semantics — the return error is unconditionally discarded. If the database is under load, the audit connection pool is exhausted, or the audit table has a schema issue, **no one will know** that audit events are being silently dropped. This violates fundamental audit trail integrity requirements (NIST SP 800-53 AU-3, SOC 2 CC3.3). A malicious admin can perform sensitive actions (grant roles, ban users) and the audit trail will be incomplete with zero alarms. |
| **Attack Scenario** | A compromised admin floods the audit table with noise events (e.g., rapidly toggling a role grant/revoke) until writes start failing; the errors are silently swallowed. They then perform an unauthorized role grant; the audit event is lost. No forensic trace exists. |
| **Impact** | Complete loss of non-repudiation for sensitive operations. Regulatory non-compliance (GDPR Art. 5(2), SOC 2). |
| **Recommendation** | At minimum, **log the error** when audit write fails. Better: use a channel-based async writer that retries internally and exposes a health check / metric: |

```go
// Best-effort but at least observable
if err := s.audit.Log(ctx, event); err != nil {
    log.Error("audit log write failed", 
        zap.Error(err), 
        zap.String("action", event.Action),
        zap.String("actor", event.ActorUID))
}
```

Long-term: implement a durable audit write path (separate connection, retry queue, or use the outbox pattern for audit events too).

| **Effort** | S (log the error) / M (durable path) |

---

### 🟠 High

| Category | Severity | Title |
|---|---|---|
| **Authentication** | **High** | Token stale fallback window allows serving revoked tokens |
| **Input Validation** | **High** | Comment and message content length unrestricted |
| **Input Validation** | **High** | File name not sanitized for path traversal characters |
| **Authorization** | **High** | No validation that SSO uid format is expected |
| **Data Protection** | **High** | No encryption at rest for sensitive stored data |
| **Denial of Service** | **High** | SSE connections unbounded per user |
| **Injection** | **High** | Webhook URL config has no validation — potential SSRF vector |

---

#### Finding 5: Token stale fallback window

| Field | Value |
|---|---|
| **Category** | Authentication / Session Management |
| **Severity** | **High** |
| **Title** | Stale token cache can serve identities past SSO-declared expiry |
| **Location** | `internal/sso/client.go` lines 92-95 (stale fallback in breaker path), `internal/sso/token_cache.go` lines 64-75 (`GetStale`) |
| **Description** | When the SSO circuit breaker is open (SSO unreachable), `validateUpstream()` falls back to `tokenCache.GetStale()`, which serves cached identities up to `maxTTL + staleFor` past the original SSO validation. With defaults `token_cache_ttl: 60s` and `stale_ttl: 300s`, a logged-out user's token remains valid for up to **6 minutes** (60s cache + 300s stale window). If the token was explicitly revoked by SSO (e.g., password change), the stale cache will still authenticate the user. |
| **Attack Scenario** | A user's account is compromised; the admin revokes the user's SSO session. The attacker continues to use the cached aero-id token for up to 6 minutes (configurable). If `stale_ttl` is set to a high value (the config default is 300s), the window is significant. |
| **Impact** | Window of authentication bypass after token revocation. Depends on SSO outage timing and configured windows. |
| **Recommendation** | Make the stale window **zero by default** (opt-in only). Document the trade-off explicitly. Consider a Redis-based TokenCache with TTL-driven expiry rather than local in-memory, so tokens are invalidated faster across instances. |

| **Effort** | S (change default `stale_ttl: 0`) |

---

#### Finding 6: Comment/message content length unrestricted

| Field | Value |
|---|---|
| **Category** | Input Validation / Denial of Service |
| **Severity** | **High** |
| **Title** | No maximum length enforcement on comment or message content |
| **Location** | `internal/service/comment.go` line ~55: `if in.Content == ""` (only empty check); `internal/service/message.go` line ~65: `if in.Content == "" && in.FileID == nil` (only empty check) |
| **Description** | Comment and message content is checked only for emptiness (non-empty required), but there is **no maximum length** enforced at the service layer. The database column (`TEXT`/`VARCHAR`) provides an implicit cap, but this defers error detection to the DB and could produce opaque 500 errors. An attacker can store multi-megabyte comments/messages, causing storage bloat, slow queries, and rendering issues. |
| **Attack Scenario** | An attacker sends a message with 100MB of text via the gRPC API. The message passes the emptiness check, hits the DB, and either fails with a confusing error (if the column has a size limit) or succeeds, bloating the table and slowing down inbox/conversation queries. |
| **Impact** | Database resource exhaustion, degraded UX for legitimate users, potential out-of-memory conditions when rendering messages. |
| **Recommendation** | Define and enforce a `MaxContentLength` constant (e.g., 10KB for comments, 64KB for messages) at the service layer: |

```go
const maxCommentLength = 10000 // 10KB

func (s *commentService) Create(...) {
    if len(in.Content) > maxCommentLength {
        return nil, apperr.BadRequest("comment.too_long", "content exceeds maximum length")
    }
    // ...
}
```

| **Effort** | S (< 1 day) |

---

#### Finding 7: File name unsanitized

| Field | Value |
|---|---|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | File `name` field not sanitized — stored key is safe but name appears in responses |
| **Location** | `internal/service/file.go` line ~70: `if in.Name == ""` (only empty check) |
| **Description** | File names are stored as-is from user input without sanitization. While the storage key uses an ID-generated path (safe from path traversal), the `name` field is echoed back in API responses, listing views, and potentially HTML rendering in clients. Names containing `../`, null bytes, or script injection characters could cause issues in downstream systems. The `Content-Disposition` header for downloads uses the storage key, not the file name, but client-side rendering of file names could be an XSS vector. |
| **Attack Scenario** | An attacker uploads a file with name `<script>alert('xss')</script>.png`. When another user's client renders this name without escaping, the script executes. Alternatively, a name like `../../../etc/passwd` could confuse downstream tooling. |
| **Impact** | Potential stored XSS in client applications that consume the API. |
| **Recommendation** | Sanitize the name field or at minimum validate it follows expected patterns (no control characters, max length ~255, reject common injection patterns): |

```go
func sanitizeFileName(name string) string {
    // Remove path separators, control characters, and trim
    name = strings.Map(func(r rune) rune {
        if r == '/' || r == '\\' || r == '\x00' || !unicode.IsPrint(r) {
            return -1
        }
        return r
    }, name)
    return strings.TrimSpace(name[:min(len(name), 255)])
}
```

| **Effort** | S (< 1 day) |

---

#### Finding 8: No encryption at rest

| Field | Value |
|---|---|
| **Category** | Data Protection |
| **Severity** | **High** |
| **Title** | Sensitive data stored without encryption at rest |
| **Location** | Entire data layer — no mention of column-level encryption, TDE, or application-layer encryption in `configs/config.yaml`, `internal/repository/db.go`, or any repository |
| **Description** | The `Secret` field on `WebhookSubscription` (domain model) is documented as "plaintext stored, encrypted at rest" (`internal/domain/webhook.go` line ~20), but there is **no encryption implementation**. All data — including audit logs containing PII (UIDs), webhook signing keys, file metadata, message content — is stored as plaintext in PostgreSQL. If the database is compromised (backup leak, SQL injection, insider threat), all data is readable. The `sslmode: disable` default in config means data in transit is also unencrypted (configurable but the default is insecure). |
| **Attack Scenario** | An attacker gains read access to the production database via a compromised backup or a lateral movement from another compromised service, and reads all personal messages, audit logs, and webhook signing keys. |
| **Impact** | Complete confidentiality failure for all stored data. Regulatory non-compliance (GDPR Art. 32 — "appropriate technical measures"). |
| **Recommendation** | 1. **Enable TLS for Postgres** (`sslmode: verify-full` as the default). 2. Implement application-layer encryption for sensitive fields (webhook `Secret`, signing keys) using a deployment key. 3. Document 3rd-party TDE options for the database at rest. |

| **Effort** | L (> 3 days for full implementation) |

---

#### Finding 9: SSE connections unbounded per user

| Field | Value |
|---|---|
| **Category** | Denial of Service |
| **Severity** | **High** |
| **Title** | No limit on concurrent SSE connections per user |
| **Location** | `internal/hub/hub.go` (no connection count check), `internal/handler/http/events.go` (no rate limiting per user on SSE endpoint) |
| **Description** | The `/v1/events` SSE endpoint creates a goroutine and a buffered channel (cap 16) per connection. There is no limit on the number of concurrent connections per user. An attacker with a single valid token can open thousands of SSE connections, each consuming memory (channel buffer, goroutine stack, hub registry entry). |
| **Attack Scenario** | An attacker obtains a valid token and opens 10,000 SSE connections via JavaScript `EventSource` connections from multiple browser tabs. The server creates 10,000 goroutines + 10,000 × 16-event channel buffers (~20MB minimum, more with GC pressure). CPU usage spikes from `Flush()` on each heartbeat. |
| **Impact** | Partial or complete denial of service for all users due to memory exhaustion. |
| **Recommendation** | Enforce a per-user connection limit (e.g., max 10 concurrent SSE connections). Also add a rate limiter to the SSE endpoint: |

```go
// In hub.go or events handler
var (
    userConns sync.Map // uid → *int64 (atomic counter)
    maxSSEConnections = 10
)

func (h *EventsHandler) Stream(c *gin.Context) {
    uid, _ := service.ActorFrom(c.Request.Context())
    
    // Check limit
    count, _ := userConns.LoadOrStore(uid, new(int64))
    if atomic.AddInt64(count.(*int64), 1) > int64(maxSSEConnections) {
        atomic.AddInt64(count.(*int64), -1)
        c.AbortWithStatus(http.StatusTooManyRequests)
        return
    }
    defer atomic.AddInt64(count.(*int64), -1)
    // ...
}
```

| **Effort** | M (1-3 days) |

---

#### Finding 10: Webhook URL config — potential SSRF

| Field | Value |
|---|---|
| **Category** | Input Validation / SSRF |
| **Severity** | **High** |
| **Title** | Outbound webhook URL has no validation — potential SSRF vector |
| **Location** | `outbox/webhook.go` line ~80: `req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.URL, ...)` |
| **Description** | The webhook URL is read from config (env- or file-based) and used directly as the target of outgoing HTTP POST requests. There is no validation that the URL is an external HTTPS endpoint, no blocklist for internal IP ranges, and no timeout on DNS resolution. If an attacker gains control of the config (e.g., via a compromised environment variable), they could redirect webhook traffic to internal services (SSRF). Even without config compromise, a DNS rebinding attack on the configured hostname could redirect traffic. |
| **Attack Scenario** | A malicious operator (or an attacker who compromises the deployment pipeline) sets `outbox.webhook.url` to `http://metadata.google.internal/computeMetadata/v1/...` or `http://internal-admin-panel:8080/action?cmd=...`. The aero-id outbox relay sends sensitive event payloads (including user IDs and event data) to this internal endpoint. |
| **Impact** | SSRF to internal infrastructure, potential data exfiltration, or lateral movement. |
| **Recommendation** | Validate the webhook URL at startup: |

```go
func validateWebhookURL(rawURL string) error {
    u, err := url.Parse(rawURL)
    if err != nil { return err }
    if u.Scheme != "https" {
        return fmt.Errorf("webhook url must use https scheme")
    }
    ip := net.ParseIP(u.Hostname())
    if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
        return fmt.Errorf("webhook url must not target private/reserved IPs")
    }
    // Also validate no localhost domains
    return nil
}
```

| **Effort** | M (1-3 days) |

---

### 🟡 Medium

| Category | Severity | Title |
|---|---|---|
| **Session Management** | **Medium** | Bearer token accepted case-insensitively |
| **Input Validation** | **Medium** | `meta` field on files/messages has no size or schema validation |
| **Denial of Service** | **Medium** | Search ILIKE fallback could be exploited for slow queries |
| **Authentication** | **Medium** | No minimum password/credential strength (delegated to SSO but no validation of SSO response) |
| **Data Protection** | **Medium** | Access logs may contain sensitive query parameters |
| **Cryptography** | **Medium** | HMAC presigned URL key is shared across all users/tenants |

---

#### Finding 11: Bearer token case-insensitive comparison

| Field | Value |
|---|---|
| **Category** | Authentication / Session |
| **Severity** | **Medium** |
| **Title** | Bearer token prefix compared case-insensitively |
| **Location** | `internal/middleware/auth.go` line 33: `strings.EqualFold(header[:len(prefix)], prefix)`; `internal/handler/grpc/server.go` line ~237: same pattern |
| **Description** | The `Authorization` header prefix is matched with `strings.EqualFold`, meaning `bearer`, `BEARER`, `Bearer`, and `BeArEr` are all accepted per RFC 7235 §2.1 (`Bearer` is case-sensitive). While this does not directly enable an attack (the token value is still case-sensitive), it deviates from the HTTP standard and could enable header smuggling in certain proxy configurations. |
| **Recommendation** | Use a case-sensitive comparison: `strings.HasPrefix(header, "Bearer ")`. |

| **Effort** | S (< 1 hour) |

---

#### Finding 12: Free-form `meta` field without size limits

| Field | Value |
|---|---|
| **Category** | Input Validation / Data Protection |
| **Severity** | **Medium** |
| **Title** | `meta` JSON map on files and messages has no size or schema validation |
| **Location** | `internal/service/file.go` — `Meta map[string]any` in CreateFileInput; `internal/handler/http/file.go` — `Meta map[string]any` in UpdateMetadata and PresignUpload |
| **Description** | The `meta` field accepts arbitrary JSON objects up to the database JSONB column size limit (~1GB per PostgreSQL). There is no validation on the total size of the meta map, the number of keys, or the types of values. An attacker can upload files with enormous metadata maps, consuming storage and bandwidth. Since `meta` is returned in every file/detail response, large maps also degrade API performance. |
| **Attack Scenario** | An attacker calls `PATCH /files/:id` with `meta` containing a deeply nested JSON object with thousands of keys. The response size balloons, pagination becomes slow, and storage grows. |
| **Impact** | Storage bloat, degraded API performance. |
| **Recommendation** | Enforce a `MaxMetaSize` (e.g., 4KB serialized) and `MaxMetaKeys` (e.g., 50): |

```go
const (
    maxMetaBytes = 4096
    maxMetaKeys  = 50
)

func validateMeta(meta map[string]any) *AppError {
    if len(meta) > maxMetaKeys { ... }
    data, _ := json.Marshal(meta)
    if len(data) > maxMetaBytes { ... }
    return nil
}
```

| **Effort** | S (< 1 day) |

---

#### Finding 13: Bearer token accepted in both HTTP and gRPC headers but no token rotation mechanism

| Field | Value |
|---|---|
| **Category** | Session Management |
| **Severity** | **Medium** |
| **Title** | No support for token rotation, refresh, or explicit revocation from aero-id |
| **Location** | `internal/sso/client.go` — `ValidateToken` only validates but cannot explicitly invalidate a cached token |
| **Description** | Once a token is cached in `TokenCache`, there is no API to forcibly invalidate it. The only way to expire a token is to wait for its TTL + stale window. If a security incident requires immediate invalidation of all sessions for a user, there is no mechanism to achieve it without restarting all aero-id instances (clearing in-memory caches). |
| **Attack Scenario** | A user reports account compromise. The admin revokes the user's SSO session, but the aero-id token cache continues to authenticate the attacker for up to 6 minutes (with default settings). |
| **Impact** | Window of unauthorized access after incident response. |
| **Recommendation** | Add a `RevokeToken(ctx, uid)` or `RevokeTokenByPrefix(ctx, tokenPrefix)` endpoint that clears the token cache for the affected user (or use Redis-backed cache for distributed invalidation). |

| **Effort** | M (1-3 days) |

---

### 🔵 Low / Info

| Category | Severity | Title |
|---|---|---|
| **Data Protection** | **Low** | No `Content-Security-Policy` or security headers on HTTP responses |
| **Cryptography** | **Low** | Webhook `Secret` field unused in implementation |
| **Info** | **Info** | gRPC reflection disabled by default — good |
| **Info** | **Info** | Read header timeout prevents Slowloris — good |
| **Info** | **Info** | Circuit breaker for SSO dependency — good |
| **Info** | **Info** | Data retention policies for audit, notifications, outbox — good |

---

## STRIDE Threat Model Summary

| Threat | Assessment |
|---|---|
| **Spoofing** | **Good.** Bearer token validation via SSO with caching. HMAC-signed download URLs prevent URL forgery. Self-grant prevention on roles. |
| **Tampering** | **Fair.** HMAC-signed download URLs and webhooks (constant-time comparison). But: no content integrity checks on stored files, no encryption at rest, no audit trail integrity. |
| **Repudiation** | **Needs Improvement.** Audit logs exist for sensitive operations but errors are silently discarded. No digital signatures on audit events. |
| **Information Disclosure** | **Fair.** Good error message sanitization (`*AppError` codes). But: no encryption at rest, default `sslmode: disable`, access logs may leak sensitive info, search returns message/comment content. |
| **Denial of Service** | **Needs Improvement.** Rate limiting disabled by default. SSE connections unbounded. Message/comment content unlimited. But: circuit breaker on SSO, capped file uploads, gRPC size limits, search timeouts. |
| **Elevation of Privilege** | **Good.** Ownership checks at service layer, permission checks at transport layer, role-based admin access control. |

---

## OWASP Top 10 Alignment

| # | Category | Status |
|---|---|---|
| A01:2021 | Broken Access Control | ✅ Good (ownership checks, RBAC, transport+service enforcement) |
| A02:2021 | Cryptographic Failures | ❌ **Significant** (no encryption at rest, default signing key, dev SSL mode) |
| A03:2021 | Injection | ✅ Good (GORM parameterized queries, no raw SQL construction found) |
| A04:2021 | Insecure Design | ❌ **Significant** (rate limiting off by default, CORS wildcard, no SSE limits) |
| A05:2021 | Security Misconfiguration | ❌ **Critical** (default signing key committed, CORS wildcard, SSL disabled default) |
| A06:2021 | Vulnerable Components | ⚠️ Fair (dependency management via go.mod, but no SCA/SAST scanning evident) |
| A07:2021 | ID and Auth Failure | ⚠️ Fair (delegated to SSO, but stale token serving, no token revocation API) |
| A08:2021 | Software & Data Integrity | ⚠️ Fair (webhook HMAC signing, but no integrity for stored data) |
| A09:2021 | Security Logging & Failures | ❌ **Significant** (audit errors silently discarded, no alert on audit failure) |
| A10:2021 | SSRF | ⚠️ Fair (webhook URL not validated for internal targets) |

---

## Top 3 Critical Issues

1. **Default signing key in config** (`signing_key: change-me-please-very-long-random`) — complete bypass of file access control for anonymous downloads. **Must be caught at startup.**

2. **Rate limiting disabled by default** — all authenticated and public routes are vulnerable to DoS. **Should be enabled by default.**

3. **CORS default `*`** — any website can read API responses. **Should require explicit origin configuration.**

## Top 3 Quick Wins

1. **Add startup validation** that rejects `signing_key` default, requires `rate_limit.enabled`, and warns on empty CORS origins. Effort: < 1 day.

2. **Log audit write failures** instead of silently discarding them. Change `_ = audit.Log(...)` to log the error. Effort: < 1 hour.

3. **Add content length limits** on message/comment content and file `meta` size. Effort: < 1 day.

## Security Debt

| Item | Effort | Priority |
|---|---|---|
| Encryption at rest for PII and secrets | L | High |
| Per-user SSE connection limits | M | High |
| Webhook URL validation against SSRF | M | Medium |
| Token revocation API for incident response | M | Medium |
| Granular rate limiting (per-endpoint, per-IP, per-user) | L | Medium |
| Content Security Policy / security headers on HTTP | S | Low |
| File integrity verification on download (checksum validation) | M | Low |
| Regular dependency scanning (govulncheck, Trivy) | S | Low |
| Penetration testing of presigned URL scheme | M | Low |

---

## Summary

aero-id is a well-architected system with strong separation of concerns and thoughtful patterns (HMAC signing, circuit breakers, outbox pattern, ownership-based authorization). However, its **default configurations are production-insecure**:

1. The **default signing key** is a catastrophic risk for local-storage deployments.
2. **Rate limiting** and **CORS restrictions** are opt-in rather than opt-out.
3. **Audit logging fails silently**, undermining the entire non-repudiation strategy.

These three issues alone elevate the risk profile significantly. The remaining findings — encryption at rest, SSE limits, content length validation, SSRF hardening — represent a substantial but manageable security debt.

**Recommendation:** Fix the 3 critical issues and the 3 quick wins before any production deployment. Then address the security debt items in priority order over subsequent sprints.
