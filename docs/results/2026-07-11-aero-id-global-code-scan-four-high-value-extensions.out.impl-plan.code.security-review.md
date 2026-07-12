Now I have a thorough understanding of the codebase. Let me compile the full security review.

---

# 🛡️ Security Review: aero-id

**Review Date:** 2026-07-12  
**Reviewer:** Principal Security Engineer  
**Scope:** Full codebase audit — authentication, authorization, input validation, cryptography, session management, data protection, threat model  
**Codebase:** Go service — HTTP + gRPC transports, GORM/Postgres, local/S3 storage, SSO-gRPC identity dependency

---

## Finding Register

---

### FINDING-01: gRPC Ban RPCs Missing Permission Enforcement

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | gRPC `Ban` / `Unban` / `ListBans` RPCs bypass permission check |
| **Location** | `internal/handler/grpc/admin.go` — functions `Ban`, `Unban`, `ListBans` (lines ~78–130) |
| **Description** | The gRPC `PermissionInterceptor` only checks permissions for three methods (`ListAudit`, `GrantRole`, `RevokeRole`) in `requiredPermForMethod()`. The ban RPCs (`/aero_id.v1.AdminService/Ban`, `/Unban`, `/ListBans`) are **not listed** and therefore bypass ALL permission checks at the transport level. The handlers delegate directly to `s.bans.Ban()`, which does **not** check the caller's role/permissions — it only verifies the actor exists. |
| **Attack Scenario** | An attacker with a valid user token (any registered user) can call the gRPC endpoint to ban arbitrary users, including admins or competitors. While the HTTP admin handler requires `PermUserBan` via `RequirePermission` middleware, the gRPC transport has no equivalent check. |
| **Impact** | **Privilege escalation.** Any authenticated user can ban/unban any other user via gRPC, causing widespread denial of service against legitimate users, extortion, or competitive sabotage. |
| **Recommendation** | Add the missing entries to `requiredPermForMethod()` in `server.go` for all ban-related methods: |
```go
func requiredPermForMethod(fullMethod string) (string, bool) {
    m := map[string]string{
        // existing entries...
        "/aero_id.v1.AdminService/ListAudit":  domain.PermAuditRead,
        "/aero_id.v1.AdminService/Ban":        domain.PermUserBan,
        "/aero_id.v1.AdminService/Unban":      domain.PermUserBan,
        "/aero_id.v1.AdminService/ListBans":   domain.PermUserBan,
        // Also missing — ListRoles, ListUserRoles should require at minimum PermAuditRead:
        "/aero_id.v1.AdminService/ListRoles":     domain.PermAuditRead,
        "/aero_id.v1.AdminService/ListUserRoles": domain.PermAuditRead,
    }
    perm, ok := m[fullMethod]
    return perm, ok
}
```
| **Effort** | S (< 1 day) |

---

### FINDING-02: Ban Middleware Fails Open

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | Ban middleware fail-open lets banned users through when ban DB is degraded |
| **Location** | `internal/middleware/ban.go` lines 46–50 |
| **Description** | When `checker.IsBanned()` returns an error (e.g., DB connection failure), the middleware calls `c.Next()` instead of aborting. The comment explicitly states the design choice: *"err on the side of caution and assume the user is NOT banned (fail-open)."* |
| **Attack Scenario** | An attacker who is banned floods the ban repository with connections until it becomes degraded/read-only. Once the ban check errors, every banned request is allowed through. The attacker can continue uploading, commenting, and messaging despite being banned. |
| **Impact** | Complete bypass of the ban enforcement mechanism under degraded conditions. Defeats moderation actions. |
| **Recommendation** | Change to **fail-closed**: if the ban check errors, abort with 503 Service Unavailable. Log the error context (the middleware has access to gin context for request ID). The degraded path detection (see `DegradationState`) already handles DB health — failing closed is more appropriate for a security gate. |
```go
if err != nil {
    // Fail-closed: if we can't verify the ban status, don't allow the request.
    httpx.WriteError(c, apperr.Internal("ban.check_failed", "failed to verify account status"))
    c.Abort()
    return
}
```
| **Effort** | S (< 1 day) |

---

### FINDING-03: Secrets in Plaintext Config File

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **Critical** |
| **Title** | Database password and HMAC signing key committed in config file |
| **Location** | `configs/config.yaml` — `signing_key`, `database.password`, `redis.password` |
| **Description** | The default `config.yaml` contains plaintext secrets: `database.password: aero`, `signing_key: change-me-please-very-long-random`, and `redis.password: ""`. While these are documented as "change me" values, the config file is committed to version control. The AGENTS.md states "Secrets via env only, never committed" but the config file violates this principle. The signing key is used for file download HMAC authentication and webhook event signing — if it's weak or exposed, an attacker can forge presigned download URLs to access any file. |
| **Attack Scenario** | 1. An attacker obtains the config file (e.g., via misconfigured S3 bucket, CI leak, or insider). They now have the DB password and signing key. 2. With the signing key, the attacker can generate valid HMAC signatures for arbitrary storage keys, bypassing the presigned URL auth and downloading any file. 3. With the DB password, the attacker can directly connect to Postgres. |
| **Impact** | Complete compromise of file storage authorization and database access. |
| **Recommendation** | 1. Remove all secrets from the committed config file; use placeholder values that fail if not overridden by env. 2. Validate that `signing_key` and `database.password` are not placeholder values at startup. 3. Add a check that `AERO_ID_DATABASE_PASSWORD` and `AERO_ID_SIGNING_KEY` env vars are set (not empty) in production mode. |
```go
// In config validation:
if strings.Contains(cfg.SigningKey, "change-me") {
    return fmt.Errorf("signing_key must not be a placeholder value; set AERO_ID_SIGNING_KEY")
}
if cfg.Database.Password == "" || cfg.Database.Password == "aero" {
    return fmt.Errorf("database.password must be set; use AERO_ID_DATABASE_PASSWORD env var")
}
```
| **Effort** | S (< 1 day) |

---

### FINDING-04: Rate Limiting Disabled by Default

| Field | Value |
|-------|-------|
| **Category** | Threat Model — Denial of Service |
| **Severity** | **High** |
| **Title** | In-memory rate limiting is opt-in and disabled by default in production configuration |
| **Location** | `configs/config.yaml` — `rate_limit.enabled: false` |
| **Description** | The per-instance rate limiter is disabled by default. An attacker can send unlimited requests to any endpoint without throttling, enabling brute-force attacks, resource exhaustion, and service disruption. |
| **Attack Scenario** | An attacker sends 10,000 requests/second to the file upload endpoint, filling disk storage. Or floods the comment endpoint with spam. Or performs a brute-force search over file UUIDs via the GET endpoint. |
| **Impact** | Unbounded resource consumption, potential DoS, spam, and brute-force attacks. |
| **Recommendation** | Enable rate limiting by default with conservative values (20 rps, burst 40). Also add per-endpoint rate limit granularity (e.g., stricter limits on auth/comment/file-upload, looser on read-only). The limiter is in-memory per-instance — document that multi-instance deployments need a shared Redis-backed limiter. |
```yaml
rate_limit:
  enabled: true
  rps: 20
  burst: 40
  idle_ttl: 10m
  cleanup_every: 1m
```
| **Effort** | S (< 1 day) |

---

### FINDING-05: Webhook Signing Key Stored in Plaintext in DB

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **High** |
| **Title** | Webhook signing key stored as plaintext string in database |
| **Location** | `internal/domain/webhook.go` line 21 — `SigningKey string` |
| **Description** | The `WebhookSubscription.SigningKey` field is stored as a plaintext string in Postgres. While the code comment says "encrypted at rest" (referring to DB-level TDE), there is no application-level encryption. An attacker with DB read access (SQL injection, backup exposure, DBA compromise) can extract all webhook signing keys. With the signing key, the attacker can forge webhook event signatures (`X-Event-Signature` header) sent to webhook endpoints. |
| **Attack Scenario** | Attacker gets read access to the `webhook_subscriptions` table. Extracts all `signing_key` values. Forges fake events with valid HMAC-SHA256 signatures to subscriber endpoints, injecting false `message.sent` or `comment.replied` events. |
| **Impact** | Trusted channel compromise — webhook subscribers cannot distinguish real events from forged ones. |
| **Recommendation** | 1. Encrypt the signing key at the application layer using AEAD (e.g., AES-256-GCM) with a key-encryption-key managed via env var or KMS. 2. Only decrypt on read when constructing webhook delivery. 3. Implement key rotation support. 4. Store only the key fingerprint for lookup. |
| **Effort** | M (1–3 days) |

---

### FINDING-06: gRPC Permission Interceptor Coverage Gaps

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | `ListRoles` and `ListUserRoles` gRPC methods lack permission enforcement |
| **Location** | `internal/handler/grpc/server.go` — `requiredPermForMethod()` |
| **Description** | The `ListRoles` and `ListUserRoles` gRPC methods are not listed in `requiredPermForMethod()`, so they pass through the permission interceptor without any check. These methods expose the role hierarchy and which users have which roles — sensitive information that should require `PermAuditRead`. The HTTP admin handler correctly enforces `RequirePermission(domain.PermAuditRead, ...)` on these endpoints. |
| **Attack Scenario** | An authenticated user calls the gRPC `ListRoles` method to enumerate all available roles and their permissions, then `ListUserRoles` to identify administrative users. This reconnaissance aids privilege escalation targeting. |
| **Impact** | Information disclosure of RBAC configuration and user privilege mapping. |
| **Recommendation** | Add to `requiredPermForMethod()` (as shown in FINDING-01). Conduct an audit of ALL gRPC methods to ensure every method has a declared permission requirement. |
| **Effort** | S (< 1 day) |

---

### FINDING-07: Token Cache Serves Stale Identities After Revocation

| Field | Value |
|-------|-------|
| **Category** | Authentication / Session Management |
| **Severity** | **High** |
| **Title** | Token cache `GetStale` serves last-known uid past token expiry during SSO outage |
| **Location** | `internal/sso/token_cache.go` lines 36–45 (`GetStale` method), `internal/sso/client.go` lines 160–167 (stale fallback) |
| **Description** | When the SSO service is unreachable and the circuit breaker trips, `validateUpstream` falls back to `c.tokenCache.GetStale(token)`. This serves a uid for a token even after it has expired, up to the `staleTTL` boundary (default: 300s = 5 minutes). If a user's token is revoked (e.g., they log out, change password, or are suspended by SSO), the stale cache extends the window during which the revoked token remains valid for aero-id. The `GetStale` comment says "last-known identity" — but this includes tokens that were explicitly validated in the past, even if SSO would now reject them. |
| **Attack Scenario** | 1. User Alice authenticates, obtains token T. aero-id caches T→alice_uid. 2. Alice's account is suspended/deleted in SSO. SSO revokes T. 3. Attacker (who has intercepted T, or Alice herself) uses T against aero-id while SSO is down or the breaker is tripped. 4. aero-id serves the stale identity, allowing continued access for up to 5 minutes. |
| **Impact** | Extended window for compromised or revoked tokens to remain valid beyond SSO's revocation. Defeats immediate account suspension at the SSO layer. |
| **Recommendation** | 1. Reduce the default `stale_ttl` from 300s to 30s — enough to ride through a transient SSO blip, but small enough that revocation lag is tolerable. 2. Add a token revocation check: maintain a small in-memory set of recently-revoked token hashes (size-bounded, TTL-capped) that block stale serves. 3. At minimum, document this tradeoff prominently in the SSO configuration. |
```yaml
sso:
  stale_ttl: 30s  # REDUCED from 300s — limits post-revocation access window
```
| **Effort** | S (< 1 day) |

---

### FINDING-08: AppError.Error() Leaks Internal Error Causes

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Wrapped error causes exposed in `AppError.Error()` and potentially in logs |
| **Location** | `internal/pkg/errors/errors.go` lines 26–30 |
| **Description** | `AppError.Error()` includes the cause chain: `return e.Code + ": " + e.Message + " (cause: " + e.Cause.Error() + ")"`. While the HTTP/JSON response only returns `ae.Message` (safe), the full error string is used by `log.Error(err)` calls, the gRPC `span.SetStatus(otelcodes.Error, err.Error())`, and any panic/recovery logging. If a repository returns `sql.ErrConnDone` or a raw Postgres error, the wrapped cause leaks connection status, query structure, or data details into operational logs that may be exposed to operators or monitoring systems. |
| **Attack Scenario** | An operator exposes structured logs to a SIEM or log aggregation service with insufficient access control. A malicious operator (or external attacker who breaches the log system) extracts DB schema details, query patterns, or error states from log entries. |
| **Impact** | Slow information leak of system internals through error propagation to logs. |
| **Recommendation** | Provide a separate `Error()` for logging that includes the cause, and a separate `SafeError()` or `PublicMessage()` for external consumption. Sanitize or truncate cause chains in log output. Better: log the cause as a structured zap field rather than embedding it in the message string. |
```go
func (e *AppError) Error() string {
    return e.Code + ": " + e.Message
}
func (e *AppError) LogSafe() string {
    if e.Cause != nil {
        return e.Code + ": " + e.Message + " (cause: " + e.Cause.Error() + ")"
    }
    return e.Code + ": " + e.Message
}
```
| **Effort** | S (< 1 day) |

---

### FINDING-09: No Input Size Limits on Message/Comment Endpoints

| Field | Value |
|-------|-------|
| **Category** | Input Validation / Denial of Service |
| **Severity** | **Medium** |
| **Title** | Message and comment content size is unbounded at the service layer |
| **Location** | `internal/service/comment.go` (Create), `internal/service/message.go` (Send) |
| **Description** | The service layer only checks that content is non-empty (`if in.Content == ""`). There is no maximum content length for message text or comment text. An attacker can send multi-megabyte messages or comments, causing excessive database storage, memory allocation, and transport bandwidth. File uploads have `max_upload_bytes` (50MB), but text content has no cap. |
| **Attack Scenario** | An attacker sends a 100MB string as a comment. This is stored in Postgres, causing disk bloat, replication lag, and potential out-of-memory conditions in handlers that buffer the response. |
| **Impact** | Storage and memory DoS, potential DB performance degradation. |
| **Recommendation** | Add configurable maximum length limits for message content and comment content. Validate at the domain layer before persistence. |
```go
const (
    MaxMessageContentLength  = 5000  // 5KB for messages
    MaxCommentContentLength  = 10000 // 10KB for comments
)
```
| **Effort** | S (< 1 day) |

---

### FINDING-10: File Upload Content-Dedup Oracle

| Field | Value |
|-------|-------|
| **Category** | Information Disclosure |
| **Severity** | **Medium** |
| **Title** | File dedup by checksum (owner-scoped) creates a file-existence side channel |
| **Location** | `internal/file/service_crud.go` — `Create()` dedup via `FindByChecksum(ctx, actor, checksum)` |
| **Description** | The file service deduplicates uploads for the same owner by computing the SHA-256 hash of the content and checking `FindByChecksum(ctx, actor, checksum)`. While this is owner-scoped (only returns rows owned by the same actor), the dedup path includes timings that could reveal whether another owner has a file with the same SHA-256 hash (e.g., if the upload is fast — it skipped the write). More critically: if an attacker can upload a file and observe the dedup behavior (fast return = file existed), they can learn that the owner previously uploaded the same content. |
| **Attack Scenario** | Bob sends a file to Alice (via message share). Bob then tries to upload the same file. If the dedup skips the upload (fast), Bob learns Alice already had this file. This is a narrow but real privacy concern. |
| **Impact** | Weak information disclosure about file existence by checksum. |
| **Recommendation** | Remove the dedup path entirely, or make it timing-constant by always writing to storage and then deleting if dedup is found. The dedup saves disk space but adds complexity and an oracle. Alternatively, store the checksum but deduplicate only during GC (background), not during upload. |
| **Effort** | M (1–3 days) |

---

### FINDING-11: Audit Logging Is Best-Effort / Silently Drops Errors

| Field | Value |
|-------|-------|
| **Category** | Threat Model — Repudiation |
| **Severity** | **Medium** |
| **Title** | Audit log failures silently discarded in critical paths |
| **Location** | Multiple files: `service/file_share.go` line 96, `service/admin.go` line 82, `service/block.go` line 62, etc. |
| **Description** | Audit logging uses the pattern `if s.audit != nil { _ = s.audit.Log(ctx, ...) }`. The return value is discarded with `_ =`. If the audit log insert fails (DB error, schema change, transaction conflict), the failure is silently ignored and the primary operation (grant role, ban user, share file) succeeds without an audit trail. This violates non-repudiation — there is no record of who performed what action. |
| **Attack Scenario** | An admin grants themselves additional privileges. The audit log insert fails (silently). There is no record of the grant. The admin can deny ever performing the action. |
| **Impact** | Complete failure of audit trail integrity under DB load or partial failures. Violates compliance requirements (SOC2, SOX) that mandate tamper-proof audit logs. |
| **Recommendation** | 1. Audit logging should be transactional with the primary operation. Use the outbox pattern (same DB transaction) so the audit event and the business action succeed or fail together. 2. If async audit is intentional, log the failure to the application logger with enough context to reconstruct the event. |
```go
// Better: write audit event in the same transaction as the primary operation
// using the transactional outbox pattern already present in the codebase.
```
| **Effort** | M (1–3 days) |

---

### FINDING-12: Content-Type Not Validated on File Downloads

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | File downloads serve user-supplied Content-Type without validation |
| **Location** | `internal/handler/http/file.go` — `Download` serves raw file bytes. `fileResponse` returns `MimeType` from DB. |
| **Description** | When files are uploaded, the MIME type comes from `fh.Header.Get("Content-Type")` (user-supplied). This value is stored in the database and later returned in API responses. The file download endpoint (`GET /v1/files/:id/url` returns a presigned URL; `GET /files/dl` streams raw bytes). If a browser directly accesses the download URL, the Content-Type in the response is not enforced server-side — it's determined by the S3 object metadata or not set at all for local storage. This could allow a user to upload a `.html` file with `Content-Type: text/html` and serve it from the storage domain, enabling XSS if the storage domain shares origin with the main app. |
| **Attack Scenario** | Attacker uploads a file named `evil.html` with `Content-Type: text/html` containing JavaScript. The download URL is accessed directly in a browser. The browser renders the HTML and executes the script in the context of the storage domain. If the storage domain shares cookies with the main app, this is a Stored XSS. |
| **Impact** | Stored Cross-Site Scripting (XSS) if CORS config or domain sharing allows it. |
| **Recommendation** | 1. Serve file downloads with `Content-Disposition: attachment` by default. 2. Only allow rendering-safe Content-Types (image/*, video/*, audio/*, application/pdf) to be served inline. 3. Validate Content-Type server-side by sniffing the file magic bytes rather than trusting the client. 4. Ensure the file storage domain (or the download endpoint path) is not same-origin with the main application. |
```go
// In the Download handler:
c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
c.Header("Content-Type", "application/octet-stream")  // Force binary download
```
| **Effort** | S (< 1 day) |

---

### FINDING-13: CORS Configuration Allows `*` When Empty

| Field | Value |
|-------|-------|
| **Category** | Compliance / Data Protection |
| **Severity** | **Low** |
| **Title** | CORS reflects `*` when no origins are configured |
| **Location** | `internal/middleware/cors.go` lines 17–38 |
| **Description** | When the `origins` list is empty (the default in config), the CORS middleware sets `Access-Control-Allow-Origin: *` for every request. This means any website can make authenticated (cookie-based or credentialed) requests to the API. While the API uses Bearer tokens (not cookies), this permissive CORS weakens the security posture if a client-side app stores tokens in `localStorage` and makes fetch requests — any origin can read the response. |
| **Attack Scenario** | A malicious site at `evil.com` tricks a logged-in aero-id user into visiting. The site makes `fetch()` calls to the aero-id API. Because CORS allows `*`, the browser permits reading the response. If the user's token is accessible (e.g., stored in `localStorage` rather than httpOnly cookie), the attacker exfiltrates it and gains persistent access. |
| **Impact** | Weak cross-origin protection for browser-based clients. |
| **Recommendation** | Require explicit CORS origin configuration in production. Validate that the origin list is non-empty in production mode. Use specific origins rather than wildcard. |
| **Effort** | S (< 1 day) |

---

### FINDING-14: HMAC Signing Key Rotation Not Supported

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | No support for signing key rotation — keys are static for process lifetime |
| **Location** | `internal/storage/signing.go` — `presignHMAC` uses a single key; `internal/storage/local.go` — `sign()` uses a single key |
| **Description** | The signing key is loaded once at startup and used for the lifetime of the process. There is no mechanism to rotate the key without a full deployment restart. If the key is compromised, all existing presigned URLs become forgeable until the process restarts. Both the local storage HMAC and webhook HMAC use the same key material. |
| **Attack Scenario** | The signing key leaks (see FINDING-03). An attacker generates valid presigned URLs for any file. Until the deployment is restarted with a new key, the attacker has unlimited access. |
| **Impact** | Compromised signing key has unlimited blast radius and no rotation capability. |
| **Recommendation** | Implement key rotation: support multiple active keys identified by a key ID prefix in the signature. The primary key signs new URLs; all keys can verify. Add a /reload endpoint or SIGHUP handler to reload keys without restart. |
| **Effort** | L (> 3 days) |

---

### FINDING-15: No HSTS or Security Headers

| Field | Value |
|-------|-------|
| **Category** | Compliance |
| **Severity** | **Low** |
| **Title** | HTTP server does not set security headers (HSTS, X-Frame-Options, X-Content-Type-Options) |
| **Location** | `internal/middleware/` — no security header middleware exists |
| **Description** | The HTTP response does not include standard security headers: `Strict-Transport-Security`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`. A single `X-Aero-Degraded` operational header is set by the degradation middleware. |
| **Attack Scenario** | A MiTM attacker on a non-HSTS connection strips TLS. The browser has no HSTS preload instruction. MIME-sniffing is permitted (no `X-Content-Type-Options: nosniff`), increasing XSS risk. |
| **Impact** | Lowered security posture; fails OWASP A5 (Security Misconfiguration). |
| **Recommendation** | Add a middleware that sets security headers globally: |
```go
func SecurityHeaders() gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
        c.Header("X-Content-Type-Options", "nosniff")
        c.Header("X-Frame-Options", "DENY")
        c.Next()
    }
}
```
| **Effort** | S (< 1 day) |

---

## STRIDE Threat Model Summary

| Category | Applicable Findings | Posture |
|----------|-------------------|---------|
| **S**poofing | FINDING-07 (stale cache serves revoked tokens) | Moderate risk — stale cache window extends token validity |
| **T**ampering | FINDING-05 (webhook keys plaintext), FINDING-14 (no key rotation) | Moderate risk — signing key compromise allows forgery |
| **R**epudiation | FINDING-11 (audit errors silently dropped) | **Weak** — audit integrity not guaranteed |
| **I**nformation Disclosure | FINDING-06 (RBAC info leak via gRPC), FINDING-10 (dedup oracle), FINDING-12 (Content-Type XSS) | Moderate risk — multiple small leaks |
| **D**enial of Service | FINDING-04 (rate limiting off), FINDING-09 (no size limits on text) | **High risk** — trivial to DoS text endpoints |
| **E**levation of Privilege | FINDING-01 (gRPC ban bypass), FINDING-02 (ban fail-open), FINDING-06 (RBAC gap) | **Critical risk** — any authenticated user can ban via gRPC |

---

## OWASP Top 10 (2021) Coverage

| OWASP | Relevant Findings |
|-------|-------------------|
| A01 — Broken Access Control | FINDING-01, FINDING-02, FINDING-06 |
| A02 — Cryptographic Failures | FINDING-03, FINDING-05, FINDING-14 |
| A03 — Injection | Not directly found (GORM parameterizes queries) |
| A04 — Insecure Design | FINDING-11 (non-transactional audit) |
| A05 — Security Misconfiguration | FINDING-04, FINDING-15 |
| A06 — Vulnerable Components | No supply-chain review performed |
| A07 — Auth Failures | FINDING-07 (stale token cache) |
| A08 — Data Integrity Failures | FINDING-14 (no key rotation) |
| A09 — Logging Failures | FINDING-11, FINDING-08 |
| A10 — SSRF | No SSRF findings observed |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

The codebase demonstrates a strong foundation: consistent use of `*AppError` for safe error boundaries, HMAC-signed URLs, RBAC patterns, SSO-driven auth, circuit breakers, telemetry, and a well-layered architecture. However, several **critical** and **high** severity issues undermine the security posture, particularly around the gRPC permission enforcement and the fail-open ban middleware.

### Top 3 Critical Issues

1. **🔴 gRPC Ban RPCs bypass permission checks** — Any authenticated user can ban/unban arbitrary users via gRPC. No permission verification exists for these methods at the transport layer.
2. **🔴 Ban middleware fails open** — If the ban repository is degraded, banned users are transparently allowed through. Fail-closed (503) is the correct posture for a security gate.
3. **🔴 Secrets committed in config file** — Database password and HMAC signing key in plaintext in the YAML config. The signing key on the local backend is the only thing protecting file downloads from unauthorized access.

### Top 3 Quick Wins

1. **Add missing gRPC permissions** to `requiredPermForMethod()` — covers Ban, Unban, ListBans, ListRoles, ListUserRoles. **(S, < 1 day)**
2. **Switch ban middleware to fail-closed** — abort with 503 when `IsBanned()` errors. Remove the fail-open comment. **(S, < 1 day)**
3. **Enable rate limiting by default** with 20 rps / burst 40 — immediate DoS protection. **(S, < 1 day)**

### Security Debt

| Item | Category | Effort |
|------|----------|--------|
| Encrypt webhook signing keys at application level | Cryptography | M (1-3 days) |
| Implement signing key rotation | Cryptography | L (> 3 days) |
| Make audit logging transactional with primary operation | Repudiation | M (1-3 days) |
| Add content-length limits for messages/comments | Input Validation | S (< 1 day) |
| Add HSTS and security headers | Compliance | S (< 1 day) |
| Reduce stale_ttl from 300s to 30s | Auth | S (< 1 day) |
| File download Content-Disposition enforcement | XSS prevention | S (< 1 day) |
| Fix AppError cause leakage in logs | Data Protection | S (< 1 day) |
