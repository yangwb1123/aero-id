Now I have a thorough understanding of the codebase. Let me compile the comprehensive security review.

---

# Security Review: aero-id Subsystem

## Overall Security Posture: **Needs Improvement** — several high-severity findings exist, primarily around authorization parity between transports, credential defaults, and fail-open safety checks.

---

## Detailed Findings

### Finding 1: Authorization Gap — gRPC `ListBanned` Lacks Permission Check

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Critical** |
| **Title** | `ListBanned` RPC accessible to any authenticated user over gRPC |
| **Location** | `internal/handler/grpc/server.go:297-309` (`requiredPermForMethod`), `internal/service/ban.go:97-103` (`ListBanned`) |
| **Description** | The gRPC permission interceptor only checks `ListAudit`, `GrantRole`, and `RevokeRole`. The `Ban`, `Unban`, and `ListBans` RPCs are NOT registered in `requiredPermForMethod`, so they bypass interceptor-level RBAC. While `Ban()` and `Unban()` have service-layer permission checks, `ListBanned()` does NOT — it only checks authentication. By contrast, the HTTP handler enforces `RequirePermission(domain.PermUserBan)` on the ban routes group. |
| **Attack Scenario** | An authenticated user calls `AdminService.ListBans` via gRPC and enumerates all currently banned users, gaining information about account enforcement actions and potentially identifying users targeted by bans. |
| **Impact** | Information disclosure — any authenticated user can list all bans, which may violate privacy expectations and reveal enforcement actions. |
| **Recommendation** | Add `ListBanned` to `requiredPermForMethod` in `server.go` and/or add a permission check in `banService.ListBanned()`. |
| **Effort** | S |

### Finding 2: Default Credentials in Configuration

| Field | Value |
|-------|-------|
| **Category** | Authentication / Secure Configuration |
| **Severity** | **High** |
| **Title** | Default database password and signing key in config.yaml |
| **Location** | `configs/config.yaml` lines 17 (`password: aero`), 81 (`signing_key: change-me-please-very-long-random`) |
| **Description** | The configuration file ships with a well-known database password (`aero`) and HMAC signing key (`change-me-please-very-long-random`). Production deployments that accidentally ship with these defaults are trivially compromised. The docker-compose file overrides the signing key but uses another well-known value (`docker-dev-signing-key-change-me`). |
| **Attack Scenario** | A deployment that doesn't override `AERO_ID_DATABASE_PASSWORD` or `AERO_ID_SIGNING_KEY` via environment variables allows an attacker to either (a) directly connect to Postgres, or (b) forge valid HMAC signatures for the presigned local download endpoint. |
| **Impact** | Database compromise (if Postgres is exposed) or arbitrary file downloads via forged presigned URLs (local backend). |
| **Recommendation** | (1) Replace defaults with empty strings and make config validation fail when they're empty. (2) Add a hard `!= "change-me-..."` validation or emit a startup warning. (3) Add `config validate` CI check that rejects known-default patterns. |
| **Effort** | S |

### Finding 3: SSO gRPC Communication in Plaintext by Default

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **High** |
| **Title** | Inter-service SSO token validation over unencrypted gRPC |
| **Location** | `configs/config.yaml` line 28 (`insecure: true`); `internal/sso/client.go` `NewClient()` uses `dialOptions(tlsCfg...)` |
| **Description** | The `sso.insecure: true` default disables TLS for the gRPC connection to the SSO service. Token validation requests and user resolution responses (including user profile data) are transmitted in cleartext. If an attacker gains access to the internal network, they can read bearer tokens and user data. |
| **Attack Scenario** | An attacker with network access to the SSO endpoint (e.g. via compromised container, ARP spoofing, or cloud VPC lateral movement) can sniff `ValidateToken` requests containing bearer tokens, then replay those tokens against aero-id or the SSO directly. |
| **Impact** | Complete authentication bypass — attacker captures bearer tokens and can impersonate any user whose token they observe. |
| **Recommendation** | (1) Default `insecure` to `false`. (2) In docker-compose/dev, use `insecure: true` but add a loud startup warning. (3) Document the TLS/mTLS setup steps in the README deployment section. (4) Consider adding a config validation rule that rejects `insecure: true` when `app.env == "production"`. |
| **Effort** | M (requires TLS cert generation for dev and prod) |

### Finding 4: Database Connection Without TLS

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **High** |
| **Title** | `sslmode: disable` for all database connections by default |
| **Location** | `configs/config.yaml` line 19 (`sslmode: disable`); `internal/config/config.go:159` (`DSN()`) |
| **Description** | The Postgres DSN is built with `sslmode=disable` hardcoded. All database traffic — including user messages, file metadata, and notification content — travels in cleartext between aero-id and Postgres. |
| **Attack Scenario** | An attacker with network access to the database subnet can sniff SQL queries and result sets, exfiltrating all message content, file metadata, and user activity data. |
| **Impact** | Complete data compromise — all application data exposed in transit. |
| **Recommendation** | (1) Make `sslmode` configurable with a default of `require` or `verify-full`. (2) In dev/docker-compose, explicitly set `sslmode=disable`. (3) Add a CI check that rejects `sslmode=disable` in production configs. |
| **Effort** | S |

### Finding 5: Ban Check Fail-Open on Error

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | Ban middleware assumes user is NOT banned on lookup error |
| **Location** | `internal/middleware/ban.go:47-51` |
| **Description** | When `IsBanned()` returns an error (e.g. database timeout), the middleware falls through to `c.Next()`, allowing the request to proceed. The comment says "fail-open" and "err on the side of caution" — but for a ban check, "failing open" means a banned user bypasses the ban during a transient DB issue. |
| **Attack Scenario** | A banned user who knows their ban is active can time their requests during a database incident and execute API calls before the ban check recovers. |
| **Impact** | Ban enforcement bypass during database degradation. A banned user can send messages, create comments, and perform other actions during the window. |
| **Recommendation** | Change to fail-closed: if the ban check errors, deny the request with 503 or 403. If the DB is degraded, the degradation middleware already handles signaling. Alternatively, cache the ban status to handle transient failures. |
| **Effort** | S |

### Finding 6: Presigned Upload Size Limit Bypass

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | S3 presigned uploads can bypass `max_upload_bytes` during upload |
| **Location** | `internal/service/file_presign.go:60-88` (`CompleteUpload`) |
| **Description** | When using S3 presigned uploads (`PresignUpload` → client PUTs directly to S3 → `CompleteUpload`), the size cap is enforced by a `Stat` call AFTER the upload is complete. The server correctly rejects the oversized object, but the storage cost of the oversized blob has already been incurred. More critically, the `cappedReader` defense-in-depth mechanism is completely bypassed for direct uploads. A client could upload terabytes of data with a single presigned URL before calling `CompleteUpload`. |
| **Attack Scenario** | A malicious client requests a presigned upload URL, then PUTs a 10 GB file to S3. The `CompleteUpload` call rejects it as oversized, but the 10 GB object remains in S3 (the `CompleteUpload` only deletes it on error), incurring storage cost. |
| **Impact** | Storage cost attack (fractional cent per large upload, but can accumulate) plus temporary orphaned objects. |
| **Recommendation** | (1) Set `ContentLength` on the presigned PUT URL to enforce the limit at S3 side. (2) Use S3 bucket lifecycle policies with tag-based or prefix-based auto-cleanup for orphaned objects. (3) Add a server-side cleanup goroutine for stale presigned objects. |
| **Effort** | M |

### Finding 7: No MIME Magic Number Validation

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | Uploaded file MIME type validated only by client-provided Content-Type header |
| **Location** | `internal/service/file.go:267-273` (`mimeAllowed`); `configs/config.yaml:73` (`allowed_mime_types: []`) |
| **Description** | The `mimeAllowed` check parses the Content-Type header that the client sends. When `allowed_mime_types` is empty (default), ALL MIME types are accepted. Even when configured, the check trusts the client's Content-Type without verifying the actual bytes (no `http.DetectContentType` magic-number check). An attacker can upload an executable or script while claiming it's an image. |
| **Attack Scenario** | A user uploads a JavaScript file or PE executable with `Content-Type: image/png`. If a downstream service (e.g., a thumbnail pipeline or file listing endpoint) serves the file with the declared content type, a browser could execute it as a script. |
| **Impact** | Cross-site scripting (if served with trusting Content-Type), malware hosting, or policy violation. |
| **Recommendation** | (1) Add server-side content-type detection via `http.DetectContentType` on the first 512 bytes. (2) Reject when the declared and detected types don't match. (3) Configure `allowed_mime_types` in production to restrict to known-safe types. |
| **Effort** | M |

### Finding 8: File Download Lacks Content-Disposition Header

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Downloaded files served without `Content-Disposition: attachment` |
| **Location** | `internal/handler/http/file.go:176-182` (`Download`) |
| **Description** | The `/files/dl` handler serves the raw file content with `c.Status(http.StatusOK)` followed by `io.Copy`. It does not set `Content-Type` or `Content-Disposition` headers. If a user uploads an HTML file (or one with a MIME type of `text/html`), the browser may render it inline, leading to potential XSS in the download domain context. |
| **Attack Scenario** | Alice uploads a file named "note.html" with content `<script>...</script>`. When Bob clicks the download link, the browser renders it as HTML and the script executes in the context of the aero-id domain. |
| **Impact** | Stored XSS in the download domain context. |
| **Recommendation** | Set `Content-Type: application/octet-stream` and `Content-Disposition: attachment; filename="..."` in the download handler. Also add `X-Content-Type-Options: nosniff`. |
| **Effort** | S |

### Finding 9: CORS Allows All Origins by Default

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | CORS configured with `Access-Control-Allow-Origin: *` when origins list is empty |
| **Location** | `internal/middleware/cors.go:16` (`allowAll := len(origins) == 0`); `internal/handler/http/router.go:37` (`RouterConfig.CORSOrigins` not set in `app.go`) |
| **Description** | When `CORSOrigins` is nil/empty (which is the case in `app.go`'s `RouterConfig` — the field is never populated), the CORS middleware returns `Access-Control-Allow-Origin: *`. While Bearer-token-based APIs aren't vulnerable to credential-leakage attacks via CORS (unlike cookie-based auth), this still allows any page to make authenticated requests if the token is stored in a way the browser can access (e.g., `Authorization` header set by JavaScript, not a cookie). |
| **Attack Scenario** | If an SPA stores the bearer token in `localStorage` and JavaScript on any page can access it, a malicious site could send `fetch()` requests to the API using that token and read the responses. |
| **Impact** | Cross-origin data access if tokens are stored in browser-accessible storage and JavaScript-based requests are made. |
| **Recommendation** | Set `CORSOrigins` to an explicit allow-list in production. Document that it defaults to `*` for development and must be configured. |
| **Effort** | S |

### Finding 10: Overly Permissive Outbox Event Types Filter Default

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Empty webhook event_types delivers ALL domain events to the external endpoint |
| **Location** | `internal/outbox/webhook.go:86-92` (`matches()`); `configs/config.yaml:61` (`event_types: []`) |
| **Description** | When `outbox.webhook.event_types` is empty (the default), ALL domain events (message.sent, comment.replied, file.commented, message.read, message.deleted, message.reacted, etc.) are forwarded to the configured webhook URL. This includes potentially sensitive data like `message.read` receipts (which reveal when a user read a message). An operator who configures a webhook URL without explicitly restricting event types may unintentionally leak user activity data. |
| **Attack Scenario** | A third-party webhook endpoint receives detailed user interaction data (read receipts, message content changes) that was not explicitly intended for export. |
| **Impact** | Unintended data sharing with external systems, potential GDPR/privacy violation if the webhook endpoint is not properly contracted. |
| **Recommendation** | (1) Default to delivering NO events (empty event_types should mean "deliver none"). (2) Add a startup warning when a webhook URL is configured but event_types is empty. (3) Document the event types in detail. |
| **Effort** | S |

### Finding 11: Token Stale Cache Can Serve Revoked Tokens for Up to 5 Minutes

| Field | Value |
|-------|-------|
| **Category** | Authentication / Session Management |
| **Severity** | **Medium** |
| **Title** | SSO token cache stale_ttl allows revoked tokens to be honored for 5 minutes |
| **Location** | `configs/config.yaml:30-31` (`token_cache_ttl: 60s`, `stale_ttl: 300s`); `internal/sso/client.go` (`validateUpstream`, `GetStale`) |
| **Description** | When SSO is reachable, the token cache lives 60 seconds. When SSO is unreachable, the `stale_ttl` of 300 seconds allows serving tokens up to 5 minutes past their natural expiry from the cache. If a user's token is revoked at the SSO (e.g., admin revokes session, password change), aero-id could continue honoring it for up to 5 minutes during any SSO connectivity disruption. |
| **Attack Scenario** | An admin revokes a compromised user's token at SSO. Due to an SSO network blip, aero-id's stale cache serves the revoked token for the next 5 minutes, allowing the attacker continued access. |
| **Impact** | Delayed enforcement of token revocation during SSO degradation. |
| **Recommendation** | (1) Reduce `stale_ttl` to 60s (matching `token_cache_ttl`). (2) Add a background token re-validation goroutine that proactively re-checks cache entries before they expire. (3) Document the trade-off in security considerations. |
| **Effort** | S |

### Finding 12: Path Traversal Risk in Local Storage Download

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | Local storage `keyToPath` joins user-influenced key without traversal check |
| **Location** | `internal/storage/local.go:31-33` (`keyToPath`); `internal/storage/local.go:129-131` (`VerifyPresignedURL`, `Get`) |
| **Description** | The `key` parameter in presigned URLs is passed directly to `filepath.Join(l.cfg.Root, key)`. While the `key` is generated server-side via `NewKey()` (which produces `owner/YYYY/MM/UUID`), the download handler does not validate that the resolved path is within the configured root directory. If an attacker could manipulate the `key` parameter (e.g., via a forged presigned URL), a path traversal like `../../etc/passwd` could read arbitrary files. The HMAC signature prevents forgery, so this is defense-in-depth. |
| **Attack Scenario** | An attacker would need to forge an HMAC signature to exploit this. However, if the signing key is ever leaked (see Finding 2), the attacker can craft a URL with `key=../../etc/passwd` and read the server's password file. |
| **Impact** | Arbitrary file read if signing key is compromised. |
| **Recommendation** | Add a `filepath.Clean` / path prefix check in `keyToPath` or `Get` to ensure the resolved path starts with the root directory: `if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(l.cfg.Root)) { return error }`. |
| **Effort** | S |

### Finding 13: Outbox Webhook Signing Key Stored in Plaintext Config

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Secrets Management |
| **Severity** | **Medium** |
| **Title** | Webhook HMAC signing key loaded from config file/environment as plaintext |
| **Location** | `internal/config/config.go:50` (`SigningKey string`); `internal/outbox/webhook.go:118` (`[]byte(h.cfg.SigningKey)`) |
| **Description** | The webhook signing key is read from the config.yaml file or environment variable, and held in process memory as a plain Go string. It is never masked in logs or rendered. If an attacker gains read access to the process memory (e.g., via a debug endpoint), they can steal the signing key and forge webhook payloads to the external endpoint. |
| **Attack Scenario** | An attacker with `ptrace` capability or memory read access can extract the signing key from the Go heap, then craft false event notifications to the webhook endpoint. |
| **Impact** | Webhook injection — attacker can send fake events to external systems, potentially triggering false downstream actions. |
| **Recommendation** | (1) Consider reading from a file path or secret manager integration. (2) At minimum, implement a `String()` redaction for the config struct so logs don't reveal the key. (3) Document that the key should come from a secure environment variable, not the config file. |
| **Effort** | S |

### Finding 14: Rate Limiter Key Uses IP:Port Format for gRPC

| Field | Value |
|-------|-------|
| **Category** | Authorization / Input Validation |
| **Severity** | **Low** |
| **Title** | gRPC rate limiter uses IP:port as fallback key, causing per-request uniqueness |
| **Location** | `internal/handler/grpc/server.go:316-321` (`rateLimitKey`) |
| **Description** | When the actor uid is unavailable (e.g., public routes), the gRPC rate limiter keys on `ip:<addr>` where `addr` is the full `peer.Addr.String()`. For TCP connections, this includes the ephemeral port (e.g., `ip:10.0.0.1:54321`). Each new TCP connection from the same client gets a different ephemeral port, meaning each connection creates a new rate limit bucket. This effectively bypasses per-IP rate limiting for gRPC. |
| **Attack Scenario** | An attacker opens many short-lived gRPC connections from the same IP. Each connection creates a fresh token bucket, so the per-IP rate limit is effectively infinite. |
| **Impact** | Per-IP rate limiting for unauthenticated gRPC is ineffective. |
| **Recommendation** | Use `peer.Addr` only for the IP portion (strip port). For example: `strings.Split(addr.String(), ":")[0]` for IPv4, or use `net.SplitHostPort`. |
| **Effort** | S |

### Finding 15: Admin `ListRoles` Requires No Permission

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Any authenticated user can list all roles and their permissions via gRPC |
| **Location** | `internal/handler/grpc/server.go:297-309` (`requiredPermForMethod`); `internal/handler/grpc/admin.go:50-56` (`ListRoles`) |
| **Description** | `ListRoles` is not registered in `requiredPermForMethod`, and the service-layer `adminService.ListRoles()` only checks authentication (`mustActor`). The HTTP handler uses `RequirePermission(domain.PermAuditRead)` on the admin group. Over gRPC, any authenticated user can enumerate all defined roles and their associated permissions. |
| **Attack Scenario** | An authenticated user calls `AdminService.ListRoles` to discover the role hierarchy and permissions structure, then targets the most powerful role or exploits a misconfiguration. |
| **Impact** | Information disclosure — role/permission enumeration aids privilege escalation planning. |
| **Recommendation** | Add `ListRoles` and `ListUserRoles` to `requiredPermForMethod` with `PermAuditRead`. Add service-layer checks matching the HTTP side. |
| **Effort** | S |

### Finding 16: gRPC Admin `GrantRole`/`RevokeRole` Service-Level Self-Grant Check No Longer Effective

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Low** |
| **Title** | Self-grant check in `GrantRole` prevents the first admin from bootstrapping |
| **Location** | `internal/service/admin.go:56` (`if uid == actor`) |
| **Description** | `adminService.GrantRole` rejects granting a role to yourself (`if uid == actor`). This is intended to prevent privilege escalation. However, it also prevents the first admin account from being bootstrapped through the API. The system requires an out-of-band seeding mechanism (direct SQL or a seed script), which must be documented. |
| **Attack Scenario** | This is not a vulnerability per se, but a usability/ops friction point. An attacker who compromises an admin account still cannot self-grant higher roles, which is good. |
| **Impact** | No direct security impact; the guard prevents self-escalation as intended. |
| **Recommendation** | Ensure the bootstrapping process for the first admin is documented in the deployment guide. |
| **Effort** | Info only |

---

## STRIDE Analysis

| Threat | Status | Key Finding |
|--------|--------|-------------|
| **Spoofing** | ✅ Good | SSO token validation with circuit breaker, caching, singleflight |
| **Tampering** | ⚠️ Needs Work | Presigned URLs HMAC-protected (good), but webhook signature can be retrieved from config (Finding 13) |
| **Repudiation** | ✅ Good | Full audit logging for all admin actions, ban operations, file sharing |
| **Information Disclosure** | ⚠️ Needs Work | gRPC ListBanned/ListRoles lack permission checks (Findings 1, 15); SSO in plaintext (Finding 3); DB in plaintext (Finding 4); XSS via file download (Finding 8) |
| **Denial of Service** | ⚠️ Needs Work | gRPC rate limiter ineffective for unauthenticated users (Finding 14); presigned upload bypass (Finding 6); CORS `*` (Finding 9) |
| **Elevation of Privilege** | ⚠️ Needs Work | Ban middleware fail-open (Finding 5); stale token cache (Finding 11); self-grant guard only at service layer (Finding 16) |

---

## OWASP Top 10 Mapping

| OWASP Category | Affected? | Finding |
|----------------|-----------|---------|
| A01: Broken Access Control | ✅ Yes | Findings 1, 5, 11, 15 |
| A02: Cryptographic Failures | ✅ Yes | Findings 3, 4, 13 |
| A03: Injection | ❌ No | All SQL uses parameterized queries |
| A04: Insecure Design | ✅ Yes | Findings 5, 10, 12 |
| A05: Security Misconfiguration | ✅ Yes | Findings 2, 7, 9 |
| A06: Vulnerable Components | ❌ Not reviewed | Dependency versions not checked |
| A07: Identification/Auth Failures | ✅ Yes | Finding 11 (stale cache) |
| A08: Software/Data Integrity | ❌ No | Not applicable |
| A09: Security Logging/Monitoring | ❌ No | Good audit logging |
| A10: SSRF | ✅ Yes | Finding 13 (webhook URL from config) |

---

## Final Summary

| Category | Result |
|----------|--------|
| **Overall Security Posture** | **Needs Improvement** |
| **File Size Constraints** | Good — all files under 500 lines, functions under 50 lines |
| **Architecture Adherence** | Good — clean one-way dependency, no business logic in handlers |
| **SQL Injection Protection** | ✅ Excellent — GORM with parameterized queries throughout; no concatenated SQL |
| **Auth Token Handling** | ⚠️ Adequate — Bearer token validation, cache, circuit breaker; stale TTL too long |
| **Authorization** | ⚠️ Inconsistent — HTTP and gRPC have different permission enforcement levels |
| **Secrets Management** | ❌ Poor — defaults in config, no TLS for DB/SSO |

### Top 3 Critical Issues

1. **Authorization gap between HTTP and gRPC transports** — `ListBanned` and `ListRoles` have no permission checks over gRPC, while HTTP enforces them. This requires immediate correction.
2. **Default credentials in config** — well-known database password and signing key in config.yaml will inevitably be deployed somewhere.
3. **Plaintext inter-service communication** — SSO token validation (bearer tokens) and database traffic (all user data) travel in cleartext by default.

### Top 3 Quick Wins

| Issue | Effort | Impact |
|-------|--------|--------|
| Add `ListBanned`, `ListRoles`, `ListUserRoles` to gRPC permission interceptor map | < 1 day | Eliminates auth gap |
| Set `Content-Disposition: attachment` on file downloads | < 1 day | Prevents stored XSS in download domain |
| Fix gRPC rate limiter key to strip port from IP address | < 1 hour | Makes per-IP rate limiting effective |

### Security Debt

The most pressing security debt is the **transport authorization inconsistency**. The architecture relies on service-layer checks for some operations and interceptor-layer checks for others, but there is no single source of truth mapping gRPC methods to required permissions. The HTTP side uses explicit middleware-per-group, which is fragile and easy to miss when adding a new endpoint. A centralized permission registry (similar to `requiredPermForMethod` but covering ALL methods including ban/search/preference endpoints) would prevent this class of bug from recurring.
