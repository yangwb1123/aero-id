I now have a comprehensive understanding of the entire codebase. Here is the full security review.

---

# 🛡️ Security Review: aero-id

## Overview

**Review Date:** 2026-07-12
**Codebase:** aero-id — Go micro-service for data owned by accounts (files, messages, comments)
**Architecture:** Handler (HTTP/gRPC) → Service → Repository (GORM) | Storage (local/S3) | SSO (gRPC)
**Lines of Code:** ~12,000 Go (non-test, non-generated)

---

## Summary

| Dimension | Rating |
|-----------|--------|
| Authentication | Good |
| Authorization | Good |
| Input Validation | Needs Improvement |
| Cryptography | Good |
| Session Management | Good |
| Data Protection | Needs Improvement |
| STRIDE Coverage | Good |
| Compliance Readiness | Good |

**Overall Security Posture:** Good — well-architected with strong separation of concerns, consistent authorization checks, and proper error handling. Several moderate issues exist but none are easily exploitable in the current threat model.

---

## Findings

### 🚨 Finding F1: Ban Middleware Fails Open on Error

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | Ban check errors allow banned users through |
| **Location** | `internal/middleware/ban.go:52-55` |
| **Description** | When `Ban()` middleware encounters an error from `IsBanned()` (e.g., database connection failure), it calls `c.Next()` — allowing the request to proceed. This is a security **fail-open** scenario: a banned user who can trigger a DB error (or coincident with a DB outage) bypasses the ban entirely. |
| **Attack Scenario** | An attacker whose account is banned initiates a DoS against the database connection pool (e.g., by sending many expensive requests). The ban checker starts erroring, and the attacker's subsequent requests bypass the ban middleware while the DB is saturated. |
| **Impact** | Banned users can continue accessing the service during a database outage or when the ban check errors. For indefinite/permabans, this defeats the ban system entirely. |
| **Recommendation** | Change to **fail-closed**: if the ban checker errors, reject the request with 503. The ban state is an access-control decision — uncertainty should deny, not permit. |
| **Effort** | S (< 1 day) |

```go
// Current (fail-open):
if err != nil {
    c.Next() // ❌
    return
}

// Recommended (fail-closed):
if err != nil {
    httpx.WriteError(c, apperr.Internal("ban.check_failed", "failed to verify account status"))
    c.Abort()
    return
}
```

---

### 🚨 Finding F2: Missing Input Length and Content-Type Validation on User-Generated Content

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | No max-length enforcement on comment, message, and file-name content |
| **Location** | All `Create`/`Send`/`Edit` services: `internal/comment/service.go`, `internal/message/service.go`, `internal/file/service_crud.go` |
| **Description** | Message content, comment content, and file names are only validated for non-empty. No maximum length is enforced anywhere in the service layer. This allows storing arbitrarily large text payloads, enabling: (a) database row bloat, (b) `ts_headline`/`ILIKE` performance degradation on search, (c) amplification in outbox/webhook payloads, and (d) SSE event size amplification to streaming clients. The database columns (TEXT/JSON) accept unbounded input. |
| **Attack Scenario** | An attacker repeatedly sends messages with 100KB+ content. Each message creates a DB row, an outbox event, a notification row, and an SSE push. The database and memory footprint grow linearly. The `ts_headline` function on search also processes this unbounded text. |
| **Impact** | Denial of service through database and memory exhaustion. Potential cost amplification on S3/outbound webhook delivery. |
| **Recommendation** | Add domain-level validation constants and enforce them at the service layer boundary: |
| **Effort** | S (< 1 day) |

```go
// In domain/comment.go or domain/message.go:
const MaxCommentContentLen = 5000
const MaxMessageContentLen = 50000
const MaxFileNameLen = 255

// In service Create:
if utf8.RuneCountInString(in.Content) > MaxMessageContentLen {
    return nil, apperr.BadRequest("message.too_long", "content exceeds maximum length")
}
```

---

### 🚨 Finding F3: Token Cache Lacks Revocation Support — Stale Tokens Persist Beyond SSO Revocation

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | Token cache with TTL provides a window for revoked tokens to remain valid |
| **Location** | `internal/sso/token_cache.go`, `internal/sso/client.go:126-144` |
| **Description** | The `TokenCache` uses a TTL-based expiry (configurable via `token_cache_ttl`). Once a token is cached, it remains valid until the TTL expires, even if SSO revokes the token. The `singleflight` pattern also means a burst of requests for a revoked-but-cached token all get the stale cached uid. There is no notification mechanism from SSO to purge the cache. |
| **Attack Scenario** | An attacker's session token is revoked by an admin after a compromise. The attacker continues to use the token for up to `token_cache_ttl` (potentially minutes) because the cache never re-validates with SSO. |
| **Impact** | Window of vulnerability between token revocation and cache expiry. |
| **Recommendation** | Reduce `token_cache_ttl` to the minimum acceptable value (e.g., 30s). For higher security, optionally skip the cache for sensitive operations (token validation is cheap). Document the revocation window in config. |
| **Effort** | S (< 1 day) |

---

### 🔶 Finding F4: Search ILIKE Fallback Enables Expensive Pattern Matching

| Field | Value |
|-------|-------|
| **Category** | Denial of Service |
| **Severity** | **Medium** |
| **Title** | ILIKE search fallback with leading/trailing wildcards bypasses indexes |
| **Location** | `internal/repository/comment.go:203-218`, `internal/repository/message.go:223-238`, `internal/service/search.go` |
| **Description** | The CJK fallback path builds `pattern := "%" + query + "%"` and uses `content ILIKE ?`. PostgreSQL cannot use standard B-tree indexes for `ILIKE '%pattern%'`. A long or pathological query (e.g., `%foobar%` repeated) forces a full sequential scan of the `content` column on large tables. While scoped to the caller's own data (`owner_uid = ?`), a user with many messages/comments can still trigger a costly scan. |
| **Attack Scenario** | A user with thousands of messages sends search requests with long, complex query strings. Each request triggers a sequential scan of their message content, consuming DB CPU and I/O. |
| **Impact** | Database CPU exhaustion, increased latency for other requests. |
| **Recommendation** | Add a maximum query length (`searchMaxQueryLen` already exists, good) AND a maximum execution timeout per search kind. Consider adding a trigram index (`pg_trgm`) for ILIKE support. Also limit the number of concurrent search operations. |
| **Effort** | M (1-3 days) |

---

### 🔶 Finding F5: Inconsistent Access Control — Comment Reactions Bypass Target Access Check in Race Window

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | `ReactToMessage` accepts `actorUID` as parameter, not from context |
| **Location** | `internal/message/service.go:219-237` |
| **Description** | The `ReactToMessage` and `UnreactMessage` service methods accept an `actorUID` string parameter rather than extracting it from the request context via `mustActor`. If ever called from a handler that passes a user-controlled `actorUID`, this would allow actor impersonation. Currently the HTTP/gRPC handlers pass the context-derived actor, but the method signature itself is a code smell that bypasses the standard authorization pattern used everywhere else. |
| **Attack Scenario** | A future code change in a handler could pass a different `actorUID` value (e.g., from a JSON field). Since the method signature accepts it, there's no compilation guard against misuse. |
| **Impact** | Potential privilege escalation if misused. |
| **Recommendation** | Change `ReactToMessage` and `UnreactMessage` to extract the actor from context like all other service methods. |
| **Effort** | S (< 1 day) |

---

### 🔶 Finding F6: SSE Event Stream Lacks Output Encoding — XSS Risk for Web Clients

| Field | Value |
|-------|-------|
| **Category** | Data Protection / Input Validation |
| **Severity** | **Medium** |
| **Title** | SSE event data streamed without content sanitization |
| **Location** | `internal/handler/http/events.go:65-74` |
| **Description** | The SSE handler streams notification data as raw JSON in the `data:` field. If a frontend renders this data into the DOM without proper escaping (e.g., assigning `event.data` to `innerHTML`), stored content like message text or comment content could execute JavaScript. While the API returns JSON (safe from direct HTML injection), the SSE `data:` field is streamed as-is, and the `event:` type field comes from the domain — both could contain attacker-controlled content. |
| **Attack Scenario** | An attacker posts a comment containing `</script><img src=x onerror=alert(1)>`. When another user views the file, the notification is delivered via SSE. A web client that processes the SSE data without HTML-escaping executes the injected script. |
| **Impact** | Stored XSS against users viewing notifications via SSE. |
| **Recommendation** | Ensure SSE data is stringified JSON (it is), but document that frontends must render it safely. Consider prefixing `event:` types with a fixed enum (not arbitrary strings). |
| **Effort** | S (< 1 day — documentation + validation) |

---

### 🔶 Finding F7: Missing Auditing for Regular Operations

| Field | Value |
|-------|-------|
| **Category** | Threat Model / Repudiation |
| **Severity** | **Medium** |
| **Title** | No audit trail for read operations and regular write operations |
| **Location** | Cross-cutting |
| **Description** | Audit logging is present for privileged operations (role grants, bans, file shares, blocks, message deletes) but absent for: file uploads, file reads, comment creation, comment reads, message sending, message reads. This means security incidents involving data access (e.g., an attacker exfiltrating files via shared URLs) leave no audit trail. |
| **Attack Scenario** | An attacker with a valid token enumerates file IDs via the List endpoint and downloads them. No audit event is logged for any of these reads, so the exfiltration goes undetected. |
| **Impact** | Inability to detect or investigate unauthorized access to data. |
| **Recommendation** | Add opt-in audit logging for `Get` operations on files (especially shared files) and `Send` operations for messages. Consider configuring alerts for anomalous access patterns. |
| **Effort** | M (1-3 days) |

---

### 🔶 Finding F8: Potential Path Traversal via SSO UID in Local Storage Backend

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | SSO uid used in filesystem path without sanitization |
| **Location** | `internal/storage/local.go:41`, `internal/file/service_presign.go:53` |
| **Description** | Storage keys are built as `filepath.ToSlash(filepath.Join(ownerUID, year, month, id))`. The `ownerUID` is returned by SSO and is never validated for path-traversal characters (`../`, `/`). If SSO ever returns a uid like `../../../etc`, the resulting path could escape the configured root directory. While SSO is a trusted subsystem (gRPC with mTLS support), this is a defense-in-depth gap: a compromised or misconfigured SSO could lead to arbitrary file read/write on local storage. |
| **Attack Scenario** | SSO is compromised or misconfigured and returns uid `../../var/lib`. The attacker uploads a file; the storage path becomes `<root>/../../var/lib/<filename>`, overwriting system files or reading sensitive data via presigned URLs. |
| **Impact** | Arbitrary filesystem access in local storage mode. |
| **Recommendation** | Validate the uid format at the application boundary (auth middleware) to reject uids containing `/`, `\`, `..`, or null bytes. |
| **Effort** | S (< 1 day) |

```go
// In auth middleware, after SSO validates token:
if uid, err := v.ValidateToken(...); err == nil {
    if strings.ContainsAny(uid, "/\\\x00") || strings.Contains(uid, "..") {
        httpx.WriteError(c, apperr.Unauthorized("auth.invalid_uid", "invalid user identifier"))
        c.Abort()
        return
    }
}
```

---

### 🔸 Finding F9: Webhook Delivery Lacks Request Signing Verification on Receive Side

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | Webhook sender signs payloads but receiver verification is out of scope |
| **Location** | `internal/outbox/webhook.go` |
| **Description** | The webhook deliverer signs payloads with HMAC-SHA256 (`X-Event-Signature` header). However, there is no documented requirement or guidance for the receiver to verify this signature. The signing provides sender authenticity only if the receiver validates it. |
| **Attack Scenario** | If the webhook URL is HTTP (not HTTPS), an attacker on the network path can send spoofed events to the webhook receiver, which has no way to verify them without checking the HMAC. |
| **Impact** | Spoofed webhook events delivered to the external endpoint if HTTPS is not enforced. |
| **Recommendation** | Document in config that webhook receivers MUST verify the HMAC signature. Consider enforcing HTTPS-only for webhook URLs in config validation. |
| **Effort** | S (< 1 day) |

---

### 🔸 Finding F10: Presigned URL TTL Is Global and Not Bounded Per-Operation

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | Presigned download URL TTL is a global config value with no per-request override |
| **Location** | `internal/app/new.go` (presignTTL), `internal/file/service_presign.go` |
| **Description** | The TTL for presigned download URLs is configured globally and applied uniformly to all downloads. Sensitive files and public files share the same expiry window. If the TTL is set too long (e.g., 1 hour), a compromised presigned URL provides an extended access window. |
| **Attack Scenario** | A file owner shares a presigned URL (intended for a 5-minute window). The global TTL is 1 hour. The URL is intercepted or leaked (e.g., via referrer headers, browser history, or network logs). The attacker has 1 hour to download the file. |
| **Impact** | Extended window of exposure for leaked presigned URLs. |
| **Recommendation** | Allow callers to specify a per-request TTL (capped by a configurable maximum). For the global default, use a conservative value (e.g., 5 minutes). |
| **Effort** | S (< 1 day) |

---

### 🔸 Finding F11: No Rate Limiting on Notification Dispatch to Individual Users

| Field | Value |
|-------|-------|
| **Category** | Denial of Service |
| **Severity** | **Low** |
| **Title** | Notification rate limiter exists but only caps per-type, not total volume |
| **Location** | `internal/service/notif_rate_limiter.go`, `internal/notification/dispatcher.go` |
| **Description** | The dispatcher uses a per-(recipient, type) rate limiter. An attacker can still flood a victim with notifications by triggering events across many types (message.received, comment.reply, file.commented, comment.liked). Each type gets its own bucket. |
| **Attack Scenario** | Attacker sends many messages and comments across different files to a victim. Each triggers a different notification type. The victim receives 10+ notifications per minute despite the rate limiter. |
| **Impact** | Notification spam to targeted users. Potential mobile push notification cost escalation. |
| **Recommendation** | Add a global per-recipient notification rate cap in addition to the per-type cap. |
| **Effort** | S (< 1 day) |

---

### ℹ️ Finding F12: gRPC Reflection Not Audited

| Field | Value |
|-------|-------|
| **Category** | Information Disclosure |
| **Severity** | **Info** |
| **Title** | gRPC reflection service is configurable, off by default (good) |
| **Location** | `internal/config/config.go:GRPCConfig.ReflectionEnabled` |
| **Description** | gRPC reflection is opt-in (`reflection_enabled: false` by default). This is the correct security posture: reflection exposes the full RPC surface including method names and message types, aiding reconnaissance. It is correctly off by default. |
| **Recommendation** | None — keep as-is. When enabled in staging for debugging, ensure it is disabled in production configs. |
| **Effort** | S (config management) |

---

## STRIDE Summary

| Category | Risk Level | Mitigations |
|----------|-----------|-------------|
| **Spoofing** | Low | Bearer token validation via SSO gRPC, token cache with stale fallback on SSO outage |
| **Tampering** | Low | HMAC-signed presigned URLs, parameterized SQL queries, HMAC-signed webhook payloads |
| **Repudiation** | Medium | Audit logging for admin ops is good; missing audit for regular data access operations |
| **Information Disclosure** | Low-Medium | Access checks on file/message read, presigned URL time limits; SSE data not sanitized (F6) |
| **Denial of Service** | Medium | Rate limiting (per-instance), file size caps, request timeouts, read header timeout; ILIKE search (F4), no connection concurrency limits, notification spam potential (F11) |
| **Elevation of Privilege** | Low | Consistent ownership checks, admin permission model, self-grant prevention; `ReactToMessage` actorUID parameter (F5) |

---

## OWASP Top 10 (2021) Coverage

| # | Category | Status |
|---|----------|--------|
| A01 | Broken Access Control | **Good** — Consistent ownership checks, admin RBAC, ban system |
| A02 | Cryptographic Failures | **Good** — HMAC-SHA256, configurable TLS |
| A03 | Injection | **Good** — Parameterized GORM queries |
| A04 | Insecure Design | **Good** — One-way architecture, service layer enforces authz |
| A05 | Security Misconfiguration | **Needs Improvement** — gRPC reflection (correctly off by default), CORS (permissive when empty) |
| A06 | Vulnerable Components | Not reviewed — `go.mod` dependency scan |
| A07 | Identification & Auth Failures | **Good** — SSO integration, token cache, stale fallback |
| A08 | Software & Data Integrity Failures | **Needs Improvement** — Webhook payload signing without receiver verification (F9) |
| A09 | Security Logging & Monitoring Fail | **Needs Improvement** — Missing audit on regular reads/writes (F7) |
| A10 | Server-Side Request Forgery | **Low risk** — Webhook URL from config only |

---

## Final Summary

### Overall Security Posture: **Good**

The aero-id codebase demonstrates strong security awareness in its architecture:
- Clean one-way dependency prevents authorization bypass
- Consistent `mustActor()` pattern at every service entry point
- HMAC-signed URLs and payloads
- Configurable rate limiting, timeouts, and ban enforcement
- Privacy-respecting error messages (no internal leakage)
- Circuit breaker + stale fallback for SSO resilience
- Soft deletes + configurable data retention

### Top 3 Critical Issues

| # | Issue | Priority |
|---|-------|----------|
| 1 | **Ban middleware fails open** (F1) — database outage allows banned users through | Fix immediately |
| 2 | **No input length limits on content** (F2) — unbounded text fields enable DoS amplification | Fix before production scale |
| 3 | **Token cache revocation window** (F3) — compromised tokens remain valid after SSO revocation | Fix with urgency |

### Top 3 Quick Wins

| # | Issue | Effort | Impact |
|---|-------|--------|--------|
| 1 | **Validate SSO uid format** at auth boundary to prevent path traversal (F8) | < 1 day | Closes supply-chain defense gap |
| 2 | **Add max-length constants** for content fields (F2) | < 1 day | Prevents DB/memory DoS |
| 3 | **Fix ReactToMessage signature** to use context actor (F5) | < 1 day | Prevents future authorization misuse |

### Security Debt

| Area | Debt | Priority |
|------|------|----------|
| Audit coverage | Missing audit for regular read/write operations | Medium |
| Distributed rate limiting | Per-instance only, no shared state | Low (architectural) |
| Notification spam | Global per-recipient cap missing | Low |
| Presigned URL TTL | Global, no per-request override | Low |
| Output encoding | SSE data not sanitized, XSS vector for web clients | Low-Medium |

---

### Closing Recommendations

1. **Immediate**: Fix the ban middleware fail-open (F1) — this is the most urgent finding as it undermines the entire ban system.
2. **Before production scale**: Add input length limits (F2) and uid validation (F8) as defense-in-depth.
3. **Security roadmap**: Expand audit coverage, add distributed rate limiting for multi-instance deployments, and implement output sanitization for SSE events.
4. **Configuration hardening**: Ensure `reflection_enabled: false` in production, `read_header_timeout >= 1s`, and `token_cache_ttl` as low as operationally feasible.
