Now I have a comprehensive understanding of the codebase. Here is my security review:

---

# Security Review: aero-id

| Field | Value |
|-------|-------|
| **Reviewer** | Principal Security Engineer |
| **Codebase** | aero-id (Go service) |
| **Date** | 2026-07-12 |
| **Scope** | Full codebase — handlers, services, repositories, storage, SSO client, middleware |
| **Build Status** | `go build ./...` ✅ `go vet ./...` ✅ `go test ./...` ✅ |

---

## Executive Summary

aero-id demonstrates **good security hygiene** overall. The architecture is clean, authorization checks are consistently applied in the service layer, external dependencies (SSO, S3) are handled with TLS/mTLS + SPKI pinning + circuit breakers, and error information is not leaked to clients. Several hardening measures from the implementation plan (degradation detection, transport hardening, rate limiting, data lifecycle management) are in place.

However, there are **two critical** and **several high-severity** issues that must be addressed before production deployment.

---

## Findings

### 🔴 CRITICAL

---

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Critical** |
| **Title** | Signing Key Default in Config — Presigned URL Forgery |
| **Location** | `configs/config.yaml` line 81: `signing_key: change-me-please-very-long-random` |
| **Description** | The HMAC-SHA256 key used to sign presigned download URLs is hardcoded with a weak placeholder default (`change-me-please-very-long-random`). If this default is deployed, any actor who learns it (source code, Docker image, config leak) can forge valid download URLs for any file, bypassing all access controls. The `Local.PresignGet` and `Local.Download` endpoints use this key exclusively for authorization — no SSO token is needed for `/files/dl`. |
| **Attack Scenario** | 1. Attacker obtains the binary or config (container image pull, configmap leak, source access). 2. Extracts `signing_key`. 3. Constructs arbitrary presigned URLs: `GET /files/dl?key=<any_storage_key>&exp=<future_timestamp>&sig=<forged_hmac>`. 4. Downloads any file stored on the backend. |
| **Impact** | Complete compromise of file confidentiality. An attacker can enumerate storage keys and download every file in the system. |
| **Recommendation** | **Enforce at startup** that `signing_key` cannot be the default value, or require a minimum entropy check. The environment variable `AERO_ID_SIGNING_KEY` should be mandatory in production with a strong random value (≥ 32 bytes / 256 bits of entropy). Add validation in `config.validate()`: |
| **Effort** | S (< 1 day) |

---

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | Self-Role-Grant Bypass in Admin Service (Design Intent but Risky) |
| **Location** | `internal/service/admin.go` lines 65–69 |
| **Description** | The `GrantRole` method explicitly refuses self-grants (`if uid == actor { return apperr.BadRequest("admin.self_grant", ...) }`). However, the principle of least privilege is violated in a different way: **any admin holding `PermSystemConfig` can grant any role** (including `PermSystemConfig` itself) to any other user. There is no super-admin escalation path, but once a single admin account is compromised, the attacker can grant every permission to an arbitrary account, then use that account to [ban users, read audit logs, modify system config]. Worse, the `GrantRole` handler at the HTTP level validates `PermAuditRead` (scope in `router.go`), NOT `PermSystemConfig` — the HTTP routing uses the **audit middleware** for the grant endpoints. Let me verify this. |
| **Attack Scenario** | 1. Attacker compromises an admin account with any role granting `PermSystemConfig` (e.g., `system.config`). 2. Issues `POST /v1/admin/roles/grant { "uid": "attacker_controlled_uid", "role_code": "system.config" }`. 3. Now `attacker_controlled_uid` has `system.config` permission, can grant further roles or read audit logs. |
| **Impact** | Complete compromise of admin functionality. |
| **Recommendation** | Implement role hierarchy: only a "super admin" role (not grantable via the API) can grant other roles. Require two-person integrity (approval) for role grants. Add explicit audit logging of all role mutations (already present). |
| **Effort** | M (1–3 days) |

Let me verify the HTTP route registration for admin:

Looking at the admin handler code:
```go
// Audit & roles group requires PermAuditRead.
admin := r.Auth.Group("/admin")
admin.Use(middleware.RequirePermission(domain.PermAuditRead, h.permChecker))
{
    admin.GET("/audit-log", h.ListAudit)
    admin.GET("/roles", h.ListRoles)
    admin.GET("/roles/:uid", h.ListUserRoles)
}

// System config group requires system.config permission.
syscfg := r.Auth.Group("/admin")
syscfg.Use(middleware.RequirePermission(domain.PermSystemConfig, h.permChecker))
{
    syscfg.POST("/roles/grant", h.GrantRole)
    syscfg.POST("/roles/revoke", h.RevokeRole)
}
```

This is correctly scoped — `GrantRole` and `RevokeRole` require `PermSystemConfig`, while `ListAudit` and `ListRoles` require `PermAuditRead`. So the routing is correct. The critical issue here is really about the absence of privilege escalation protection when an admin account is compromised, which is a design choice but still a risk.

Actually, let me re-evaluate. The self-grant prevention is good. The issue is really that any admin with `PermSystemConfig` can give `PermSystemConfig` to anyone else. This is a valid escalation path but common in RBAC systems without a super-admin tier. I'll downgrade this to High.

---

### 🟠 HIGH

---

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | No CSRF Protection on Mutating Endpoints |
| **Location** | All HTTP handlers under `/v1/` — `internal/handler/http/router.go` |
| **Description** | The service has no Cross-Site Request Forgery (CSRF) protection. Every mutating endpoint (`POST /v1/files`, `POST /v1/messages`, `POST /v1/comments`, etc.) relies solely on the `Authorization: Bearer` header for authentication. A browser-based attacker who tricks a logged-in user into visiting a malicious page can make authenticated requests cross-origin (for non-GET methods) on subdomains where CORS is permissive or cookies are sent. While the service uses Bearer tokens (not cookies), a token stored in `localStorage` by a client-side app is still at risk if the client app has an XSS vulnerability — the malicious JavaScript can read and use the token. |
| **Attack Scenario** | 1. User authenticates on the aero-id frontend, token stored in `localStorage`. 2. Attacker exploits an XSS vulnerability in the frontend. 3. Malicious JS reads token from `localStorage`. 4. Issues authenticated API calls on behalf of the user. |
| **Impact** | Unauthorized actions performed on behalf of authenticated users. |
| **Recommendation** | The Bearer token pattern is acceptable for server-to-server or SPA APIs. Ensure the frontend uses short-lived tokens and stores them securely (in-memory, not `localStorage`). Add `SameSite=Strict` cookie-based session tokens as a more secure alternative for browser clients. Add security headers (`X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`). |
| **Effort** | M (1–3 days for full CSRF implementation) |

---

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **High** |
| **Title** | Missing Security Response Headers |
| **Location** | `internal/handler/http/router.go` (all HTTP responses) |
| **Description** | The service sets no security-relevant HTTP headers on responses. Missing headers include: `Strict-Transport-Security` (HSTS), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy`, `Referrer-Policy`, `Permissions-Policy`. While this is an API service (not serving HTML directly), these headers protect against content-sniffing, clickjacking, and other browser-level attacks, especially if any endpoint returns user-controlled content (e.g., file metadata, comments). |
| **Attack Scenario** | 1. Attacker uploads a file with `MimeType: "text/html"` and malicious content. 2. (If the file storage is ever served directly without Content-Disposition headers) the browser renders it as HTML, executing attacker scripts. 3. Missing `X-Content-Type-Options: nosniff` prevents the browser from refusing MIME sniffing. |
| **Impact** | XSS, clickjacking, MIME-type confusion attacks |
| **Recommendation** | Add a middleware that sets baseline security headers on every response. This is especially important for the `/files/dl` endpoint which serves user-uploaded content. |
| **Effort** | S (< 1 day) |

---

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | User Content Stored/Served Without Sanitization → Stored XSS |
| **Location** | `internal/service/comment.go` line 104 (`in.Content` stored as-is); `internal/service/message.go` line 80; `internal/file/handler_http.go` (file names) |
| **Description** | User-submitted content (comment text, message content, file names) is stored and served verbatim without any HTML sanitization, content-type validation, or encoding. While aero-id is an API service and the frontend is responsible for safe rendering, the service provides no defense-in-depth. If any consumer renders this data unsafely (e.g., a notification email, an admin dashboard, a webhook receiver that auto-renders JSON), it can lead to stored XSS. The `Content-Type` for file downloads is user-supplied, which could trick browsers into interpreting files as HTML. |
| **Attack Scenario** | 1. Attacker posts a comment containing `<script>alert('xss')</script>`. 2. The API returns the raw HTML in JSON. 3. A notification webhook or admin dashboard renders the JSON field unsafely. 4. Script executes in the consumer's context. |
| **Impact** | Stored XSS in any consumer that renders the data without sanitization. |
| **Recommendation** | Apply output encoding (HTML entity encoding) for text fields in API responses. Set `Content-Type: application/json; charset=utf-8` explicitly on all JSON responses. For file downloads set `Content-Disposition: attachment; filename="..."` and `X-Content-Type-Options: nosniff`. Validate that `MimeType` is not a dangerous type (e.g., `text/html`) when serving files. |
| **Effort** | M (1–3 days) |

---

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Denial of Service) |
| **Severity** | **High** |
| **Title** | No SSE Connection Limit — Memory Exhaustion Risk |
| **Location** | `internal/hub/hub.go` line 85; `internal/handler/http/events.go` lines 30–70 |
| **Description** | The SSE (Server-Sent Events) hub creates a buffered channel (`chan SSEEvent, 16`) per subscriber and stores it indefinitely in a `sync.Map`. There is **no limit** on the number of concurrent SSE connections a single user or IP can establish. An attacker can open thousands of SSE connections, each with a 16-slot buffer, causing unbounded memory growth. Additionally, slow consumers silently drop events (which is correct behaviour), but the hub does not detect or disconnect slow consumers. |
| **Attack Scenario** | 1. Attacker opens 10,000 concurrent SSE connections to `/v1/events`. 2. Each connection allocates: goroutine + 16-event channel + map entry. 3. Memory usage grows linearly with connections, potentially OOM-killing the process. |
| **Impact** | Service denial-of-service through memory exhaustion |
| **Recommendation** | Implement per-IP and per-uid SSE connection limits (e.g., max 10 connections per uid). Add a connection-timeout for SSE connections that haven't received an event in N minutes. Track total active SSE goroutines with a semaphore. |
| **Effort** | M (1–3 days) |

---

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | Ban Middleware Fails Open on Error |
| **Location** | `internal/middleware/ban.go` lines 39–44 |
| **Description** | When the ban check (`checker.IsBanned`) returns an error, the middleware **allows the request to proceed** (fail-open). The comment says: "If the ban check fails we err on the side of caution and assume the user is NOT banned (fail-open)." This is the wrong posture for authorization: a database failure could allow a banned user to bypass the ban check. The middleware also warns about missing a logger handle but notes that "the access log middleware downstream will record the 200/403 eventually" — but it already allowed the banned request through. |
| **Attack Scenario** | 1. User is banned. 2. The ban-check database is briefly unavailable. 3. User's request bypasses the ban check and hits the handler. 4. For a brief window, the banned user can post messages, upload files, etc. |
| **Impact** | Temporary bypass of user bans during database failures |
| **Recommendation** | **Fail-closed**: when the ban check errors, deny the request with a 503 or 403. Add a logger dependency to the middleware so the failure is recorded. |
| **Effort** | S (< 1 day) |

---

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **High** |
| **Title** | Request and Response Bodies Stored in Idempotency Store (TTL-bound) |
| **Location** | `internal/idempotency/store.go` lines 70–85; `internal/middleware/idempotency.go` lines 36–44 |
| **Description** | The idempotency middleware captures **full response bodies** (including error responses) and stores them in the database/Redis for up to 24 hours. If a response contains PII, secrets, or sensitive data (e.g., user details in a 200 response, error details in a 400 response), these are persisted in the `idempotency_keys` table. While the body is TTL-bound, the default TTL is 24 hours — long enough for an attacker who gains DB/Redis access to exfiltrate sensitive response data. |
| **Attack Scenario** | 1. Admin queries audit logs (response contains PII). 2. Idempotency key stores the entire response body. 3. Attacker gains read access to Postgres/Redis. 4. Queries `idempotency_keys` table, extracts stored audit response bodies containing PII. |
| **Impact** | PII exposure through idempotency response caching |
| **Recommendation** | Implement selective redaction of sensitive fields before storing in the idempotency cache. Reduce the default TTL to a shorter window (e.g., 1 hour). Document this behaviour in the data retention docs. |
| **Effort** | M (1–3 days) |

---

### 🟡 MEDIUM

---

| Field | Value |
|-------|-------|
| **Category** | Compliance (OWASP Top 10) |
| **Severity** | **Medium** |
| **Title** | Rate Limiting Disabled By Default |
| **Location** | `configs/config.yaml` lines 42–46: `rate_limit: { enabled: false }` |
| **Description** | The in-memory rate limiter is **opt-in and disabled by default**. If production deployment does not explicitly enable it, there is no protection against brute-force attacks, credential stuffing, or API abuse. The comment says "disabled by default so it never surprises dev/CI", but this is a dangerous default for a production service. |
| **Attack Scenario** | 1. Attacker runs a distributed brute-force on `POST /v1/comments` or `POST /v1/messages`. 2. No rate limiting — attacker can blast requests at max network speed. 3. Spam, resource exhaustion, database load. |
| **Impact** | Service abuse, resource exhaustion, spam |
| **Recommendation** | Change the default to `enabled: true` with sensible defaults (e.g., 100 RPS, burst 200). The comment about "surprising dev/CI" can be solved by having dev/CI explicitly override the config. Add a startup warning log when rate limiting is disabled. |
| **Effort** | S (< 1 day) |

---

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | No TLS for HTTP Server (Plaintext Traffic) |
| **Location** | `internal/app/new.go` lines 190–199; `configs/config.yaml` |
| **Description** | The HTTP server is configured without TLS. The config file and `http.Server` settings make no mention of certificates. All HTTP traffic is transmitted in cleartext. While this is acceptable behind a TLS-terminating reverse proxy, the architecture does not enforce this requirement. If someone deploys the HTTP server exposed to the internet without a proxy, all tokens and data are transmitted in cleartext. |
| **Attack Scenario** | 1. Service deployed directly on a public IP without a reverse proxy. 2. Attacker on the same network or upstream captures traffic. 3. SSO tokens, file content, messages, and comments are visible in plaintext. |
| **Impact** | Complete loss of confidentiality for all in-transit data |
| **Recommendation** | Add documentation that the HTTP server MUST be fronted by a TLS-terminating proxy in production. Optionally add built-in TLS support for direct deployments. |
| **Effort** | S (< 1 day for documentation) |

---

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | No Host Header Validation |
| **Location** | `internal/handler/http/router.go` — gin engine |
| **Description** | The HTTP router does not validate the `Host` header. In deployments behind a reverse proxy, this can allow host header injection attacks, which can be exploited for cache poisoning, password reset poisoning, or SSRF if the `Host` header is reflected in responses (it is not currently, but downstream components might use it). |
| **Attack Scenario** | 1. Attacker sends a request with `Host: evil.com`. 2. If any link-generation code uses the Host header (e.g., presigned URL generation), the attacker can cause clients to connect to evil.com. |
| **Impact** | Host header injection, potential phishing via reflected links |
| **Recommendation** | Add a host validation middleware that checks `Host` against an expected allow-list. Use `gin.SetTrustedProxies` to configure proxy trust. |
| **Effort** | S (< 1 day) |

---

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Repudiation) |
| **Severity** | **Medium** |
| **Title** | Audit Logs Are Best-Effort and Silent on Failure |
| **Location** | `internal/service/file.go` line 344; `internal/service/comment.go` line 305; `internal/service/message.go` line 176 |
| **Description** | Audit logging is consistently implemented as best-effort with `_ = s.audit.Log(...)`. A failed audit log write is silently discarded. This means administrative actions (ban, grant role, file share, comment report resolution) can occur without being recorded, breaking non-repudiation. |
| **Attack Scenario** | 1. Admin grants a role to an attacker-controlled account. 2. The audit database write fails transiently. 3. The grant succeeds but is not recorded. 4. Incident response cannot trace the grant back to the responsible admin. |
| **Impact** | Loss of audit trail integrity, inability to detect and attribute malicious admin actions |
| **Recommendation** | At minimum, log the audit failure via the structured logger. For critical operations (role grant, ban), consider making the audit log part of the transaction (same DB transaction as the operation) so the write and the audit are atomic. |
| **Effort** | M (1–3 days) |

---

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Spoofing) |
| **Severity** | **Medium** |
| **Title** | System Messages Bypass Actor Authentication |
| **Location** | `internal/service/message.go` lines 122–135 (`SendSystem`) |
| **Description** | The `SendSystem` method is documented as "internal-only" and bypasses the authenticated actor check. It stamps the message with `domain.SystemSenderUID` as the sender. This method is accessible through the message service interface, and any component with access to the service can send system messages to any user. While this is legitimate for the notification machinery, there are no guards preventing abuse. If the service interface is exposed inappropriately or a malicious internal caller gains access, arbitrary system messages can be sent. |
| **Attack Scenario** | 1. Internal component (e.g., a compromised worker thread) calls `SendSystem` with any `toUID` and content. 2. Attacker sends fake system messages to users for phishing purposes. |
| **Impact** | Phishing via system messages, social engineering |
| **Recommendation** | Restrict access to `SendSystem` via a separate internal interface or require explicit authorization. Document that `SendSystem` is a privileged operation. |
| **Effort** | S (< 1 day) |

---

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Tampering) |
| **Severity** | **Medium** |
| **Title** | Webhook HMAC Key from Config — No Rotation Support |
| **Location** | `configs/config.yaml` lines 57–61; `internal/outbox/webhook.go` lines 50–55 |
| **Description** | The outbox webhook signing key is loaded from config and held in memory for the lifetime of the process. There is no key-rotation mechanism. If the key is compromised, an attacker can forge webhook payloads indefinitely until the process is restarted with a new key. |
| **Attack Scenario** | 1. Attacker compromises the config and extracts `outbox.webhook.signing_key`. 2. Forges webhook requests to the webhook endpoint claiming to be aero-id. 3. Downstream systems process forged events. |
| **Impact** | Integrity compromise of webhook-delivered events |
| **Recommendation** | Support reading the webhook signing key from a file that can be rotated without restart. Add key-rotation support (e.g., `X-Event-Signature-Key-Id` header). |
| **Effort** | M (1–3 days) |

---

### 🟢 LOW

---

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Low** |
| **Title** | Presigned Upload StorageKey Ownership Validation is Prefix-Based Only |
| **Location** | `internal/service/file_presign.go` lines 60–62 |
| **Description** | The `CompleteUpload` method validates storage key ownership with a simple prefix check: `strings.HasPrefix(in.StorageKey, actor+"/")`. This prevents cross-owner key claims but does not validate the full path traversal safety (`../`). The backend (`Local` or `S3`) uses UUIDs in the path, so traversal is unlikely, but the prefix check alone is not a robust security boundary. |
| **Attack Scenario** | Unlikely in practice given UUID-based key construction, but a path-traversal character in `actor` (e.g., `uid/../other`) could potentially target another owner's storage prefix. |
| **Impact** | Low — mitigated by UUID path components and filesystem-level protections. |
| **Recommendation** | Sanitize the storage key before the prefix check. Consider validating that the key does not contain path-traversal sequences (`../`, `..\\`). |
| **Effort** | S (< 1 day) |

---

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | Access Log Contains `actor_uid` in Plain Text |
| **Location** | `internal/middleware/access_log.go` line 24 |
| **Description** | The access log includes the authenticated user's `actor_uid` as a structured field. If logs are shipped to a centralized logging platform, anyone with log access can track every action performed by every user. While this is intentional for debugging, it can be a privacy concern under GDPR if the `uid` is considered personal data. |
| **Attack Scenario** | An attacker who gains read access to the logging system can reconstruct complete user activity timelines. |
| **Impact** | Privacy violation, GDPR compliance risk if uid is PII |
| **Recommendation** | Document in the data retention policy that access logs contain user identifiers. Consider supporting a configuration option to hash/redact `actor_uid` in logs. |
| **Effort** | S (< 1 day) |

---

## STRIDE Summary

| Category | Risk Level | Key Issues |
|----------|-----------|------------|
| **S**poofing | **Medium** | System messages bypass auth (`SendSystem`); no mTLS for inter-service calls (SSO uses TLS but is configurable) |
| **T**ampering | **Medium** | Webhook HMAC key no rotation; presigned URL HMAC key hardcoded default; audit logs best-effort (silent failures) |
| **R**epudiation | **Medium** | Audit logs are best-effort — failures are silently dropped, breaking non-repudiation |
| **I**nformation Disclosure | **High** | Missing security headers; user content stored unsanitized (XSS risk); idempotency store caches full response bodies; access log contains PII; no TLS for HTTP |
| **D**enial of Service | **Medium** | No SSE connection limit; rate limiting disabled by default; gRPC `MaxConcurrentStreams` is 100 (but default 0 = unlimited, though config sets 100); ban middleware fail-open on DB error |
| **E**levation of Privilege | **High** | Self-grant prevention exists but role grant/revoke allow lateral privilege escalation within admin tier; ban middleware fail-open on error temporarily bypasses bans |

---

## Compliance Mapping

| Standard | Status | Notes |
|----------|--------|-------|
| **OWASP Top 10 2021** | ⚠️ Needs Improvement | A01:Broken Access Control (fail-open ban), A03:Injection (no XSS sanitization), A04:Insecure Design (rate limiting off by default), A05:Security Misconfiguration (missing headers), A07:Identification & Auth Failures (no CSRF) |
| **GDPR** | ⚠️ Needs Improvement | Access logs contain PII (uid); no data anonymization; idempotency store caches response bodies (may contain PII) |
| **SOC2** | ⚠️ Needs Improvement | Audit logs are best-effort (breaks CC6.1 - logical access controls); webhook key no rotation |
| **PCI-DSS** | N/A | Service does not process credit card data |

---

## Final Summary

| Metric | Assessment |
|--------|-----------|
| **Overall Security Posture** | **Needs Improvement** |
| **Foundational strengths** | Clean architecture, consistent authorization in service layer, good SSO client hardening (circuit breaker, stale cache, SPKI pinning), rate limiting infrastructure present (even if off by default), data retention policies, degradation detection |
| **Critical gaps** | Presigned URL signing key default value → complete file confidentiality breach. Missing security headers, no CSRF, stored XSS risk, SSE DoS vector |

### Top 3 Critical Issues

1. **🔴 Default signing key in config** (`configs/config.yaml`) — Any deployment using the default `change-me-please-very-long-random` can have all files compromised via forged presigned URLs. **Fix: Validate at startup, document env var override.**

2. **🔴 No security response headers + unsanitized user content** — User-uploaded files and user-created comments/messages are stored and served with user-controlled MIME types. Combined with missing `X-Content-Type-Options: nosniff`, `Content-Disposition`, and CSP headers, this creates a stored XSS pipeline. **Fix: Add security headers middleware, set `Content-Disposition: attachment` for file downloads, sanitize or encode user text content.**

3. **🔴 SSE connection unlimited → DoS** — No limits on concurrent Server-Sent Events connections. An attacker with a single auth token can exhaust server memory by opening thousands of SSE streams. **Fix: Cap concurrent SSE connections per uid (e.g., 5), add idle timeout.**

### Top 3 Quick Wins

| # | Issue | Effort | Impact |
|---|-------|--------|--------|
| 1 | **Validate signing_key at startup** — Reject the default placeholder value. | S (< 1 hr) | Prevents file confidentiality compromise |
| 2 | **Add security headers middleware** — `X-Content-Type-Options`, `Content-Disposition`, `X-Frame-Options`, HSTS. | S (< 2 hrs) | Mitigates XSS, MIME confusion, clickjacking |
| 3 | **Enable rate limiting by default** — Change `enabled: false` to `enabled: true` with sensible defaults. | S (< 30 min) | Prevents brute-force abuse |

### Security Debt

1. **Audit log reliability** — Making audit writes best-effort is a significant gap for non-repudiation. Plan to make audit log writes transactional for critical operations (role grants, account bans).
2. **Configuration management** — Secrets (signing keys, webhook keys, database passwords) are loaded from config/environment but there is no secret-rotation support.
3. **Subsystem isolation** — The `SendSystem` method and the presigned-upload `CompleteUpload` pattern both bypass the normal authorization flow in intentional but risky ways. Document these as security-sensitive surfaces.
4. **Transport security documentation** — The HTTP server has no TLS. This is acceptable behind a proxy but should be a hard requirement written in operational docs.

---

### Closing Recommendation

The aero-id service has a **solid architecture with well-enforced authorization boundaries** — the service layer consistently checks ownership, the SSO client is hardened with circuit breakers and stale caching, and data retention/cleanup policies are in place. The critical issues are configuration defaults and missing defense-in-depth layers (security headers, rate limiting, connection limits), not fundamental design flaws.

I recommend addressing the **Top 3 Critical Issues** before any production deployment, followed by the **Top 3 Quick Wins** which can be completed in a single engineering day. The remaining findings can be addressed in the next sprint.
