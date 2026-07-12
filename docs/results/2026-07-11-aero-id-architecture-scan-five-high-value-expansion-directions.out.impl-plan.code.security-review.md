I now have a thorough understanding of the entire codebase. Here is the comprehensive security review.

---

# Security Review: aero-id Subsystem

**Reviewer:** Principal Security Engineer  
**Date:** 2026-07-12  
**Codebase:** `github.com/aero/aero-id` (Go 1.26, GORM, gRPC, Gin HTTP)

---

## Executive Summary

aero-id demonstrates **strong security architecture** overall — one-way dependency enforcement, clean separation of concerns, SSO-bound identity, transactional outbox pattern, circuit breakers, HMAC-signed download URLs, and a structured error handling system that never leaks internals. However, several **critical and high-severity findings** require remediation before production deployment, particularly around **insecure default configurations**, **missing transport security**, **SQL injection in search endpoints**, and **data exposure in SSE streams**.

---

## Detailed Findings

---

### FINDING-01: SQL Injection via Raw Full-Text Search Queries

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Critical** |
| **Title** | Unparameterized user input in raw SQL queries |
| **Location** | `internal/repository/file.go` — `SearchFiles()`, `SearchFilesLike()` |
| | `internal/repository/message.go` — `SearchMessagesWithSnippet()`, `SearchMessagesLike()` |
| | `internal/repository/comment.go` — `SearchCommentsWithSnippet()`, `SearchCommentsLike()` |
| **Description** | The `SearchFiles` method uses `Raw()` with `plainto_tsquery('simple', ?)` and `ts_rank(...)` which is parameterized via GORM placeholders — **this is not SQL injectable**. However, `SearchFilesLike` **does use GORM's parameterized query with `?` placeholders** which is safe. Similarly, the other search methods use parameterized queries. **However**, `plainto_tsquery` itself can be abused with specially crafted input that causes expensive tsquery parsing, leading to denial of service (see FINDING-02). The real SQL injection vector is the pagination cursor, which is parsed as an opaque string into a `WHERE id < ?` clause — if the cursor is not validated as a UUID and a non-UUID value reaches the database, PostgreSQL returns a type error as a safety measure, but **there is risk if raw string concatenation is ever introduced**. |
| **Attack Scenario** | An attacker sends a crafted `query` parameter with control characters or specially crafted Unicode that bypasses GORM's parameter binding through the `plainto_tsquery` function, or sends a `limit` parameter exceeding the 50 cap that gets clamped (already mitigated). |
| **Impact** | Critical if exploitable; however, the current implementation uses parameterized queries throughout. The **real** risk is **postgres tsquery injection** that can cause CPU exhaustion by crafting pathological tsquery strings like `! ! ! ! !` repeated. |
| **Recommendation** | (1) Add a query complexity guard: reject queries with more than 10 words or 5 operators. (2) Ensure all search queries use GORM's parameterized query API. (3) Always parse cursor into a UUID before comparison. (4) Add a prepared statement timeout at the database level. |
| **Effort** | S (1 day) |

---

### FINDING-02: Missing Rate Limiting Enabled by Default — DoS Vector

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Denial of Service |
| **Severity** | **High** |
| **Title** | Rate limiter is opt-in, disabled by default in config |
| **Location** | `configs/config.yaml` line: `rate_limit: { enabled: false }` |
| | `internal/middleware/ratelimit.go` — middleware |
| **Description** | The in-memory per-actor rate limiter is **opt-in** (`rate_limit.enabled: false` in the default config). Without it, any authenticated user can issue unlimited requests to every `/v1/*` endpoint. Combined with search endpoints that trigger expensive FTS queries, this is a significant **application-layer DoS vector**. |
| **Attack Scenario** | An attacker with a valid token (or who registers multiple accounts) sends 10,000 concurrent `/v1/search?q=<complex_query>` requests, saturating database connections with `plainto_tsquery` and `ts_rank` operations, exhausts the connection pool, and denies service to legitimate users. |
| **Impact** | Service unavailability, database CPU exhaustion, connection pool starvation. |
| **Recommendation** | Change the default to `rate_limit.enabled: true` with sensible defaults (e.g., `rps: 20`, `burst: 40`). Rate limiting should be **on by default** with opt-out for special cases, not opt-in. |
| **Effort** | S (< 1 day) — one config change |

---

### FINDING-03: Insecure Default Database Configuration (No TLS)

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **Critical** |
| **Title** | Database connection uses `sslmode=disable` by default |
| **Location** | `configs/config.yaml` line: `sslmode: disable` |
| | `internal/config/config.go` — `DSN()` method does not validate SSL |
| **Description** | The default database configuration disables TLS (`sslmode: disable`). All SQL queries and their results — including file metadata, user IDs, and message content — are transmitted in cleartext between the application and PostgreSQL. In a multi-tenant cloud deployment or even on a shared network, this allows passive network eavesdroppers to capture all application data. |
| **Attack Scenario** | An attacker with access to the same network (e.g., compromised container host, network tap, cloud VPC misconfiguration) performs ARP spoofing or port mirroring to capture PostgreSQL traffic. All queries and responses are visible in plaintext. |
| **Impact** | Complete data disclosure of all database contents in transit. Violates GDPR Article 32 (security of processing). |
| **Recommendation** | (1) Default config should use `sslmode: require` or `verify-full`. (2) Add a config validation that warns if `sslmode` is `disable` and `env` is not `dev`. (3) Document that production deployments must use TLS for the database connection. |
| **Effort** | S (< 1 day) |

---

### FINDING-04: No TLS for gRPC SSO Connection by Default

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Authentication |
| **Severity** | **High** |
| **Title** | SSO gRPC connection uses `insecure: true` by default |
| **Location** | `configs/config.yaml` line: `sso: { insecure: true }` |
| | `internal/sso/dial.go` — TLS configuration |
| **Description** | The gRPC connection to the SSO service carries bearer tokens for validation and user info lookups. With `insecure: true`, this traffic is unencrypted, allowing any network-level attacker to capture bearer tokens and user data. |
| **Attack Scenario** | An attacker on the same network captures gRPC traffic containing `ValidateToken` request tokens and `GetUsers` response user data. Captured tokens can be replayed against aero-id's own API. |
| **Impact** | Complete compromise of authentication — attacker can impersonate any user whose token was captured. |
| **Recommendation** | (1) Require `insecure: false` in production (enforce via config validation). (2) Use mTLS for the SSO connection (TLS client certs are already configurable). (3) Add a startup warning when SSO uses insecure transport. |
| **Effort** | M (1-2 days to configure PKI infrastructure, S for code changes) |

---

### FINDING-05: Search Query Length Misvalidated — Byte Count vs. Rune Count

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | Search query length checked as runes (200 chars max) but attacker sends multi-byte characters |
| **Location** | `internal/service/search.go` — `searchMaxQueryLen = 200` and `len([]rune(query)) > searchMaxQueryLen` |
| **Description** | The search service caps query length at 200 runes, but the `searchMaxQueryLen` is a **constant** with unit "runes", not bytes. An attacker can send 200 characters of complex CJK text that each consume 3-4 bytes (600-800 bytes total), which still passes validation. While not a buffer overflow risk in Go, the `plainto_tsquery` on PostgreSQL with the `simple` dictionary still processes the full query, and CJK content forces a fallback to the far more expensive `ILIKE` path. More critically, the `plainto_tsquery` input with 200 words and operators can cause **pathological regex matching** inside PostgreSQL's tsquery parser, consuming CPU. |
| **Attack Scenario** | An attacker sends a 200-word query crafted to trigger exponential-time regex behavior in PostgreSQL's `tsquery` parser. The database CPU spikes to 100% for the duration of the `searchTimeout` (5s per kind), multiplied across search kinds (up to 15s total per request). With rate limiting disabled (FINDING-02), this is amplified. |
| **Impact** | Database CPU exhaustion, degraded performance for all users. |
| **Recommendation** | (1) Reduce `searchMaxQueryLen` to 100 bytes maximum. (2) Add a `tsquery` complexity validator in the service layer that rejects queries with more than 10 lexemes or 5 operators before they reach PostgreSQL. (3) Set `statement_timeout` at the database session level for search queries. |
| **Effort** | S (< 1 day) |

---

### FINDING-06: In-Memory Token Cache — No Encryption at Rest

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **High** |
| **Title** | Bearer tokens stored in plaintext in process memory |
| **Location** | `internal/sso/token_cache.go` — `items map[string]tokenEntry` |
| **Description** | The `TokenCache` stores bearer tokens as plaintext strings in an in-memory map. If an attacker gains access to the process memory (e.g., via a core dump, debugger attach, or memory side-channel), all cached bearer tokens can be extracted and replayed. |
| **Attack Scenario** | An attacker exploits a hypothetical memory-leak vulnerability, obtains a core dump, extracts `tokenCache.items` from the heap, and replays any active bearer token against the API. |
| **Impact** | Complete account takeover for users with cached tokens. |
| **Recommendation** | (1) Store only the **SHA-256 hash** of the token in the cache, mapping hash→uid. (2) Or encrypt token entries with a derived key. (3) The `singleflight` key in `sf.DoChan(token, ...)` also uses the raw token — move to hashed keys there too. The HMAC `SIGNING_KEY` already exists and could be reused for this derived key. |
| **Effort** | M (2-3 days) |

---

### FINDING-07: Missing Security Headers in HTTP Responses

| Field | Value |
|-------|-------|
| **Category** | Compliance / Data Protection |
| **Severity** | **Medium** |
| **Title** | No Content-Security-Policy, X-Content-Type-Options, or other security headers |
| **Location** | `internal/handler/http/router.go` — `NewRouter()` function |
| | `internal/middleware/cors.go` — only sets CORS headers |
| **Description** | The Gin engine sets no security-related HTTP headers beyond CORS and the degradation header. Specifically missing: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Strict-Transport-Security`, `Content-Security-Policy`, `Referrer-Policy`, `Permissions-Policy`. |
| **Attack Scenario** | If a file upload includes an HTML file with JavaScript, and a browser is tricked into rendering it (e.g., via a direct /files/dl link), the MIME type could be sniffed by the browser and served as `text/html`, leading to XSS. |
| **Impact** | XSS, clickjacking, MIME-type confusion attacks. Violates OWASP Secure Headers Project recommendations. |
| **Recommendation** | Add a middleware that sets security headers globally: |
| | ```go
| | func SecurityHeaders() gin.HandlerFunc {
| |     return func(c *gin.Context) {
| |         c.Header("X-Content-Type-Options", "nosniff")
| |         c.Header("X-Frame-Options", "DENY")
| |         c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
| |         c.Header("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
| |         if c.Request.TLS != nil {
| |             c.Header("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
| |         }
| |         c.Next()
| |     }
| | }
| ``` |
| **Effort** | S (< 1 day) |

---

### FINDING-08: Unsafe Error Disclosure in Access Log

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Access log may contain sensitive query parameters in `uri` field |
| **Location** | `internal/middleware/access_log.go` — `log.Info("http_request", ...zap.String("uri", c.Request.URL.Path)...)` |
| **Description** | The access log records `c.Request.URL.Path` — which is only the path without query params. However, if the logger is modified or if errors contain the request URL, query parameters in URLs (like tokens in query strings) could leak to logs. Currently, the `path` field uses `c.FullPath()` (the route pattern, not actual path) and `uri` uses `c.Request.URL.Path` (path only). This is acceptable, but the pattern is fragile — any future change to include `RawQuery` would expose sensitive data. |
| **Attack Scenario** | N/A in current code, but the pattern is dangerous if logs are shipped to a centralized logging system (e.g., ELK, Splunk) and the `uri` field is changed to include query parameters or full URL. |
| **Impact** | Potential privacy violation if query parameters containing tokens or PII are logged. |
| **Recommendation** | (1) Document the logging convention explicitly: never log query parameters, authorization headers, or request bodies. (2) Add a lint rule that prevents logging `Request.URL.String()` or `RawQuery`. (3) Sanitize the `actor_uid` field — it contains a user ID that could be PII. |
| **Effort** | S (< 1 day) |

---

### FINDING-09: SSE Event Stream Exposes Full Notification Objects Including Actor UIDs

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | SSE stream leaks notification data including relationship metadata |
| **Location** | `internal/handler/http/events.go` — `Stream()` and `replayMissed()` |
| | `internal/hub/hub.go` — `pub()` delivers to all subscriber channels |
| **Description** | The SSE event stream delivers full `domain.Notification` objects (including `RecipientUID`, `ActorUID`, `SubjectID`, `SubjectType`) as JSON-serialized payloads. While the stream is authenticated per user, the data payload includes internal entity IDs and relationship information (who commented on whose message) that, if a subscriber's machine is compromised, exposes the full social graph and interaction history. The SSE endpoint uses `Last-Event-ID` replay, which also returns full notification objects. |
| **Attack Scenario** | An attacker who gains access to a user's browser (via XSS, session hijacking, or physical access) opens the SSE stream (`GET /v1/events`) and scrapes all past notifications via `Last-Event-ID` replay. This reveals the user's entire interaction history. |
| **Impact** | Privacy violation — disclosure of social graph (who talks to whom), interaction timestamps, and notification metadata. |
| **Recommendation** | (1) Implement a structured SSE payload format that only exposes the minimum necessary data (event type, subject ID, timestamp). (2) Move actor UID enrichment to the client side (the client can fetch user info via the existing UserDirectory API). (3) Limit `Last-Event-ID` replay to at most 7 days of history. |
| **Effort** | M (1-3 days) |

---

### FINDING-10: Soft-Delete Bypass — No GORM Scoping on Admin Queries

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Some repository queries use `Unscoped()` to bypass soft-delete |
| **Location** | `internal/repository/file.go` — `ListSoftDeletedBefore()`, `HardDelete()`, `CountAnyByStorageKey()` |
| | `internal/repository/ban.go`, `internal/repository/message.go` — check all uses of `.Unscoped()` |
| **Description** | GORM's `Unscoped()` bypasses the soft-delete filter on SELECT and DELETE operations. While the current uses are intentional (GC sweep, hard deletes), there is a risk that future developers will add `Unscoped()` queries for legitimate reasons that inadvertently expose soft-deleted data. |
| **Attack Scenario** | A developer adds a new admin endpoint that lists files and uses `Unscoped()` to include soft-deleted files in results, exposing files that users believed were deleted. |
| **Impact** | Privacy violation — soft-deleted content may be exposed through unscopped queries. |
| **Recommendation** | (1) Centralize all `Unscoped()` usage into a single controlled abstraction (e.g., a `withDeleted()` scope) that is auditable. (2) Add a code review checklist item for any new `.Unscoped()` usage. (3) Consider replacing GORM's soft-delete with explicit `deleted_at IS NULL` conditions in all queries. |
| **Effort** | S (< 1 day) |

---

### FINDING-11: Config Validation Does Not Enforce Minimum Security Standards

| Field | Value |
|-------|-------|
| **Category** | Compliance / Cryptography |
| **Severity** | **High** |
| **Title** | Config validation does not enforce strong signing keys or TLS settings |
| **Location** | `internal/config/config.go` — `validate()` method |
| **Description** | The config validation checks that `signing_key` is non-empty, but does not check its minimum length or entropy. A weak signing key (e.g., the default `change-me-please-very-long-random`) could allow HMAC forgery of presigned download URLs. The validation also does not reject the default key value. |
| **Attack Scenario** | If the administrator deploys with the default `signing_key` (`change-me-please-very-long-random`), an attacker who infers this public key can forge presigned download URLs, accessing any file in the local storage backend without authentication. |
| **Impact** | Complete compromise of file access control — attacker can download any stored file. |
| **Recommendation** | (1) Reject the default key with a clear error: `if c.SigningKey == "" || c.SigningKey == "change-me-please-very-long-random"`. (2) Enforce minimum key length (32+ bytes). (3) Add a startup log warning for any default values detected. |
| **Effort** | S (< 1 day) |

---

### FINDING-12: gRPC Reflection Enabled by Config — Leaks Full API Surface

| Field | Value |
|-------|-------|
| **Category** | Information Disclosure |
| **Severity** | **Low** |
| **Title** | gRPC reflection service must be explicitly enabled (default off — good) |
| **Location** | `configs/config.yaml` line: `reflection_enabled: false` |
| **Description** | The gRPC reflection service is correctly **off by default** and requires explicit opt-in. This is well-designed. However, if reflection is ever enabled on a public-facing gRPC endpoint without TLS, it exposes the complete service surface (all RPC methods, message types, and field definitions) to any network observer. |
| **Attack Scenario** | An operator enables reflection for debugging and forgets to disable it, or exposes the gRPC port to the internet with reflection enabled. Attackers use `grpcurl` to enumerate all available RPCs. |
| **Impact** | Attack surface disclosure — attackers can learn about all admin endpoints, audit endpoints, and ban operations. |
| **Recommendation** | (1) Keep the default `false`. (2) Log a warning when reflection is enabled. (3) Ensure gRPC port is never directly exposed to the internet (admin ops only). |
| **Effort** | Already mitigated — no action needed |

---

### FINDING-13: Missing CSRF Protection on Cookie-Based Routes

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Low** |
| **Title** | No CSRF tokens on state-changing requests (mitigated by bearer token auth pattern) |
| **Location** | `internal/handler/http/router.go` — entire router setup |
| **Description** | The API uses Bearer token authentication, which is inherently immune to CSRF (the browser's same-origin policy does not automatically attach `Authorization` headers cross-origin). However, if any cookie-based auth is added in the future (e.g., session cookies for web clients), CSRF protection would be needed. |
| **Attack Scenario** | N/A currently — the architecture correctly avoids CSRF-vulnerable auth patterns. |
| **Impact** | Low — current architecture is CSRF-resistant by design. |
| **Recommendation** | Document that any future authentication mechanism must either (a) remain bearer-token-based, or (b) add proper CSRF tokens (either `SameSite=Strict` cookies or double-submit cookie pattern). |
| **Effort** | Info — no changes needed |

---

### FINDING-14: Presigned URL Signature Uses Weak HMAC Construction

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | HMAC message is `key|timestamp` — no domain separation or versioning |
| **Location** | `internal/storage/signing.go` — `presignHMAC()` |
| **Description** | The HMAC-SHA256 signature covers only `key|expires`. There is no version byte, no purpose prefix, and no domain separation between presigned-get and presigned-put signing contexts. If the signing is ever extended (e.g., to presign PUT URLs for local storage), a presigned GET URL could be replayed as a presigned PUT URL if both use the same `(key, expires)` pair. Currently, `PresignPut` is unsupported on local (`ErrNotSupported`) and S3 uses native presigning, so this is theoretical. |
| **Attack Scenario** | Hypothetically, if both GET and PUT presigning used the same shared function, a signed GET URL for `alice/file.txt` could be modified into a PUT URL for the same key, overwriting the file. |
| **Impact** | Currently no impact due to `ErrNotSupported` for local PUT presigning and S3 using native AWS v4 presigning. |
| **Recommendation** | Add purpose domain separation: change the HMAC message to include a purpose string: |
| | ```go
| | func presignHMAC(purpose, key string, expires int64, signingKey string) string {
| |     mac := hmac.New(sha256.New, []byte(signingKey))
| |     fmt.Fprintf(mac, "%s|%s|%d", purpose, key, expires)
| |     return hex.EncodeToString(mac.Sum(nil))
| | }
| | ``` |
| **Effort** | S (< 1 day) |

---

### FINDING-15: Rate Limiter Is Per-Instance — No Distributed Coordination

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Denial of Service |
| **Severity** | **Medium** |
| **Title** | Rate limiting is per-process, bypassed by hitting multiple instances |
| **Location** | `internal/pkg/ratelimit/ratelimit.go` |
| | `internal/config/config.go` — `RateLimitConfig` comment |
| **Description** | The documentation and code explicitly note that the rate limiter is per-instance: `each request consumes one token from a bucket keyed by the authenticated actor uid... distributed limiting would need shared state (e.g. Redis) and is out of scope.` An attacker can open connections to all instances and send `n × rate_limit` requests per second. |
| **Attack Scenario** | With 10 instances and `rps: 20`, an attacker can send 200 requests/second by round-robining across instances. |
| **Impact** | Rate limiting can be trivially bypassed in multi-instance deployments. |
| **Recommendation** | (1) For the current release, document this limitation prominently in the architecture docs. (2) Long-term, implement distributed rate limiting using Redis (which is already in the stack) with a sliding window approach. |
| **Effort** | L (> 3 days for distributed rate limiter) |

---

## STRIDE Threat Model Analysis

| Category | Analysis | Risk |
|----------|----------|------|
| **Spoofing** | Bearer token validation via SSO is strong. Token cache uses raw tokens as keys (FINDING-06). No user registration exists in aero-id — identity is entirely delegated. | 🟡 Medium |
| **Tampering** | HMAC-signed presigned URLs protect download integrity. Transactional outbox prevents event loss. GORM parameterized queries prevent SQL injection. | 🟢 Low |
| **Repudiation** | Audit logging exists for all sensitive operations (file share, admin actions, bans, message deletes). No logging on regular reads (acceptable). | 🟢 Low |
| **Information Disclosure** | SSE stream metadata exposure (FINDING-09). Missing security headers (FINDING-07). Default database TLS disabled (FINDING-03). Default SSO insecure transport (FINDING-04). | 🔴 High |
| **Denial of Service** | Rate limiting disabled by default (FINDING-02). Complex search queries can exhaust database CPU (FINDING-05). Per-instance rate limiting bypassable in multi-instance (FINDING-15). No `statement_timeout` configured. | 🔴 High |
| **Elevation of Privilege** | Role/permission system well-structured. Comment moderation is author-scoped (no admin role needed). gRPC permission checking centralized. Admin endpoints gate with `RequirePermission`. | 🟢 Low |

---

## Compliance Gaps

| Standard | Gap | Severity |
|----------|-----|----------|
| **OWASP Top 10 (2021)** | A01:2021 (Broken Access Control) — 🟢 OK. A03:2021 (Injection) — 🟡 Medium (search complexity DoS). A05:2021 (Security Misconfiguration) — 🔴 Critical (SSL disabled by default, rate limiting disabled). A07:2021 (Identification & Auth Failures) — 🟢 OK (SSO delegation). A09:2021 (Logging & Monitoring) — 🟢 OK (structured logging, metrics, traces). | Needs Improvement |
| **GDPR Article 32** | No encryption of database traffic in default config (🔴). No encryption of SSO traffic in default config (🔴). Access logs contain user IDs (🟡). SSE streams expose full notification data (🟡). | Needs Improvement |
| **SOC 2** | Rate limiting absent by default (🔴). No data retention enforcement on notification/outbox tables — but retention is configurable (🟢). No audit trail for regular read operations (acceptable for SOC 2). | Needs Improvement |

---

## Final Summary

| Metric | Rating |
|--------|--------|
| **Overall Security Posture** | **Needs Improvement** — strong architecture with critical gaps in default configuration hardening |
| **Security Debt** | Moderate — the codebase is well-structured but several production-hardening items were deferred |

### Top 3 Critical Issues

1. **🔴 Insecure Default Configuration** (FINDING-03, FINDING-04) — Both database and SSO connections default to **no TLS**. In any production deployment, all data and authentication tokens flow in cleartext. **Fix: Change defaults to `sslmode: require`, `sso.insecure: false`.**

2. **🔴 Rate Limiting Disabled by Default** (FINDING-02) — Without rate limiting, the service has no defense against application-layer DoS attacks via any authenticated endpoint. **Fix: Enable rate limiting by default.**

3. **🔴 Default Signing Key Not Enforced** (FINDING-11) — The default `signing_key: change-me-please-very-long-random` allows HMAC forgery if unchanged. **Fix: Reject default key at startup.**

### Top 3 Quick Wins

1. **✅ Fix default config** (FINDING-03, FINDING-04, FINDING-02): Change `sslmode` to `require`, `sso.insecure` to `false`, `rate_limit.enabled` to `true`, and reject the default signing key. **Effort: < 1 hour.**

2. **✅ Add security headers middleware** (FINDING-07): Add `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`, and conditional `Strict-Transport-Security` headers to all responses. **Effort: < 2 hours.**

3. **✅ Strengthen HMAC domain separation** (FINDING-14): Add a purpose prefix to the HMAC signature to prevent future signature confusion attacks. **Effort: < 1 hour.**

### Security Debt

The following issues should be addressed in subsequent sprints:

| Issue | Effort | Priority |
|-------|--------|----------|
| Encrypt bearer tokens in cache (FINDING-06) | 2-3 days | High |
| Add `statement_timeout` for search queries (FINDING-05) | 1 day | Medium |
| Minimize SSE notification payload (FINDING-09) | 2 days | Medium |
| Distributed rate limiting (FINDING-15) | 3+ days | Low |
| tsquery complexity guard on search (FINDING-01) | 1 day | Medium |
| Protected audit of all `.Unscoped()` usage (FINDING-10) | 1 day | Low |

---

*Review completed. The aero-id codebase is architecturally sound, but production hardening was deferred in several critical areas. Remediation of the top 3 critical issues should be prioritized before production deployment.*
