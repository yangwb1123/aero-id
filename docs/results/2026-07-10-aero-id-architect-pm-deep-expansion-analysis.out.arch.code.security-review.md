Now I have a thorough understanding of the codebase. Here is my comprehensive security review.

---

# Security Review: aero-id

**Date**: 2026-07-12
**Reviewer**: Principal Security Engineer
**Scope**: Full production readiness assessment of the aero-id service

---

## 1. Authentication & Authorization

**Strengths**:
- All `/v1/*` routes require Bearer token validation via SSO, with no fallback paths
- gRPC infrastructure methods (health, reflection) correctly bypass auth
- Circuit breaker in SSO client prevents cascading failures; stale-cache fallback maintains availability during SSO outages
- Ownership checks enforced at the service layer for all resources (files, messages, comments)
- Storage key prefix check (`actor+"/"`) prevents finalizing another user's presigned upload
- Ban middleware runs before rate limiting, preventing banned users from consuming rate-limit slots

| Finding | Severity | Details |
|---------|----------|---------|
| Token cache bound to stale TTL | Info | `stale_ttl: 300s` means a revoked token is still accepted for up to 5 minutes after SSO invalidates it. Acceptable trade-off for availability |

---

## 2. Findings

### FINDING-01: Rate Limiting is OPT-IN and Disabled by Default

| Field | Value |
|-------|-------|
| **Category** | Denial of Service / Compliance |
| **Severity** | **Critical** |
| **Title** | Rate limiter disabled by default in production configuration |
| **Location** | `configs/config.yaml` line 73: `rate_limit: { enabled: false }` |
| **Description** | The per-actor rate limiter is OPT-IN: `enabled` defaults to `false`. If an operator deploys the default config to production, there is zero request throttling. A single compromised credential or bad actor can exhaust server resources (CPU, DB connections, storage) with no per-account backpressure. |
| **Attack Scenario** | An attacker obtains or brute-forces a valid SSO token (e.g., via an app-level credential stuffing attack). Without rate limiting, they can call `POST /v1/files` with multi-MB uploads thousands of times per second, consuming all storage space, DB connections, and bandwidth — a classic resource-exhaustion DoS. With `rps: 20`, the same attacker could only upload 20 files/second. |
| **Impact** | Total service degradation for legitimate users. AWS S3 storage costs could skyrocket. DB connection pool exhaustion. |
| **Recommendation** | **Change the default to enabled.** The config `rate_limit.enabled: false` should be `true` with safe defaults. Since it's per-instance and idempotent, even a conservative `rps: 50, burst: 100` is far better than none. If operators need to disable for load testing, they can explicitly set it to false. |
| **Effort** | S (change one line in config.yaml + regenerate) |

---

### FINDING-02: File Upload Multipart Parsing Exposes DoS Vector

| Field | Value |
|-------|-------|
| **Category** | Input Validation / Denial of Service |
| **Severity** | **High** |
| **Title** | `c.FormFile()` buffers entire uploaded file into memory before size validation |
| **Location** | `internal/file/handler_http.go` line 51-56: `fh, err := c.FormFile("file")` |
| **Description** | Gin's `c.FormFile()` calls `ParseMultipartForm()` which buffers the entire multipart body. The default `MaxMemory` is 32 MB — files larger than this spill to temp files on disk, but the full payload is still fully parsed before the service layer checks `max_upload_bytes` (50 MB in config). A 50 MB upload creates a 50 MB buffer in memory or a 50 MB temp file before any rejection can occur. An attacker sending many concurrent 50 MB uploads can exhaust server RAM or fill disk with temp files. |
| **Attack Scenario** | Attacker opens 200 concurrent connections, each uploading a 49 MB file. The `max_upload_bytes: 52428800` allows it. Each connection's multipart form is parsed into memory (49 MB × 200 ≈ 9.8 GB). Server OOM-kills the process or starts swapping. |
| **Impact** | Denial of service — memory exhaustion, process termination, or disk space exhaustion from temp files. |
| **Recommendation** | Set a restrictive gin `MaxMultipartMemory` in the router (e.g., `router.MaxMultipartMemory = 1 << 20` = 1 MB) so the multipart parser rejects oversized bodies before processing. Additionally, wrap the upload handler with a `http.MaxBytesReader` on the request body to enforce a hard cap at the HTTP level, before any parsing begins. |
| **Effort** | S (add router config and body wrapping in the handler) |

---

### FINDING-03: gRPC Server Lacks TLS

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **High** |
| **Title** | gRPC server uses plaintext TCP (no TLS) |
| **Location** | `internal/app/new.go` line ~168-175: `grpcLis, err := net.Listen("tcp", grpcAddr)` — no TLS options passed to `grpc.NewServer` |
| **Description** | The gRPC server listens on plain TCP with no TLS credentials configured. All RPC traffic — including Bearer tokens in `authorization` metadata — transits in cleartext. The SSO client dials the downstream SSO with configurable TLS (including mTLS and certificate pinning), but the aero-id gRPC server itself has no TLS configuration at all. |
| **Attack Scenario** | An attacker with network access between the client and aero-id gRPC (or on the same L2 segment) performs ARP spoofing or packet sniffing, capturing every gRPC request. Bearer tokens, file metadata, message contents, and user UIDs are all visible in plaintext. |
| **Impact** | Complete loss of confidentiality for all gRPC traffic. Token theft enables impersonation. |
| **Recommendation** | Add a `ServerTLSConfig` to the app config with cert/key file paths. In `New()`, load the TLS config and pass it as a gRPC `ServerOption` via `grpc.Creds(credentials.NewTLS(tlsConfig))`. Default to TLS 1.3 with a minimum of TLS 1.2. Even behind a service mesh, the gRPC server should not trust the network. |
| **Effort** | M (add config fields, TLS loading logic, wire into server options) |

---

### FINDING-04: HTTP Server Lacks TLS

| Field | Value |
|-------|-------|
| **Category** | Cryptography / Data Protection |
| **Severity** | **High** |
| **Title** | HTTP server uses plaintext (no HTTPS) |
| **Location** | `internal/app/new.go` line ~153-158: `httpServer := &http.Server{Addr: ...}` — no TLS configured |
| **Description** | The HTTP server serves all endpoints (including authenticated file downloads that may contain sensitive data) over plain HTTP. Bearer tokens, file contents, and message bodies are transmitted in cleartext. |
| **Attack Scenario** | Same as FINDING-03 for HTTP traffic: network-level attacker captures tokens and data. Additionally, without HTTPS there is no integrity protection — an attacker could inject malicious responses (e.g., a script tag with XSS). |
| **Impact** | Complete loss of confidentiality and integrity for HTTP traffic. |
| **Recommendation** | Add TLS configuration for the HTTP server (cert/key from config). If a reverse proxy (nginx, Envoy, ALB) terminates TLS, document this as a deployment requirement. Add a security note in README.md that TLS termination must be provided by the infrastructure. |
| **Effort** | S–M (add config fields, load cert/key, wire into http.Server) |

---

### FINDING-05: Presigned Upload Size Checked AFTER Storage

| Field | Value |
|-------|-------|
| **Category** | Input Validation / Denial of Service |
| **Severity** | **High** |
| **Title** | Presigned upload size limit is enforced after the object is already stored |
| **Location** | `internal/file/service_upload.go` line 62-74: `info, err := s.store.Stat(ctx, in.StorageKey)` then `if s.maxBytes > 0 && info.Size > s.maxBytes` |
| **Description** | In the presigned upload flow, the client PUTs the file directly to S3 (or to local storage via PUT). The `CompleteUpload` call then checks `Stat()` to get the object size and rejects if it exceeds `maxUploadBytes`. However, the object is already stored and consumed storage space. The service does delete it on rejection, but the bandwidth and PUT request to S3 already happened. An attacker can repeatedly presign-upload oversized files and trigger `CompleteUpload`, causing the service to repeatedly store and delete large objects — incurring S3 PUT costs, bandwidth costs, and potential rate-limit throttling from AWS. |
| **Attack Scenario** | Attacker calls `POST /v1/files/upload-url` to get a presigned upload URL, then PUTs a 1 GB file to S3 via that URL. Calls `POST /v1/files/complete` with the same key. Service sees 1 GB > 50 MB limit and deletes it. Repeats 1000 times: 1 TB of S3 PUT+DELETE traffic billed to the account. |
| **Impact** | Financial cost (S3 API charges, bandwidth), potential S3 rate-limit throttling. |
| **Recommendation** | Enforce the upload size limit at the S3 presigned URL itself by setting `ContentLength` on the `PresignPutObjectInput` to `maxUploadBytes`. For the local backend, implement a size-checking wrapper reader in the PUT handler. Additionally, the presign URL TTL should be short (minutes, not hours) to bound the window. |
| **Effort** | M (S3 content-length constraint on presign; local backend enforcement) |

---

### FINDING-06: CORS Defaults to Wildcard `*`

| Field | Value |
|-------|-------|
| **Category** | Compliance / Information Disclosure |
| **Severity** | **Medium** |
| **Title** | CORS configuration allows all origins when origins list is empty |
| **Location** | `configs/config.yaml` (no `cors_origins` field exists); `internal/middleware/cors.go` line 14: `allowAll := len(origins) == 0` |
| **Description** | The `CORS` middleware sets `Access-Control-Allow-Origin: *` when the origins list is empty (the default — the config has no `cors_origins` field, and `RouterConfig.CORSOrigins` is nil). This means any website can make authenticated requests to the API when the victim is logged in. While Bearer tokens are not cookies, if a client-side SPA stores the token in `localStorage`, an XSS on any origin could make cross-origin requests. |
| **Attack Scenario** | User visits malicious.com while logged into the aero-id SPA (with token in localStorage). If the SPA has any XSS vulnerability, the attacker's script reads the token from localStorage and exfiltrates it. Without CORS restrictions, the attacker can also make direct fetch() calls from their origin (though the token is scoped to the SPA's origin, so the browser won't automatically attach it). |
| **Impact** | Low-to-Medium (requires XSS to exploit fully). But violates the principle of least privilege. |
| **Recommendation** | Add a `cors_origins` config field (list of strings). In production, set it to the known list of allowed origins (e.g., the SPA domain). Fail closed when origins are configured but an unknown origin requests. Default to `[]` (which currently means `*`) — document that `*` is for dev only. |
| **Effort** | S (add config field, update router config) |

---

### FINDING-07: No Security Headers on HTTP Responses

| Field | Value |
|-------|-------|
| **Category** | Compliance / Data Protection |
| **Severity** | **Medium** |
| **Title** | Missing security-related HTTP response headers |
| **Location** | `internal/handler/http/router.go` — no security header middleware configured |
| **Description** | The HTTP server does not set standard security headers: `Strict-Transport-Security` (HSTS), `X-Content-Type-Options`, `X-Frame-Options`, `Content-Security-Policy`, or `Referrer-Policy`. An XSS vulnerability would be far more exploitable without CSP. MIME-type sniffing attacks are possible without `X-Content-Type-Options: nosniff`. Clickjacking is possible without `X-Frame-Options: DENY`. |
| **Attack Scenario** | Attacker uploads a file with `Content-Type: text/html` and `Content-Disposition` not set. A victim visits the presigned download URL. The browser renders it as HTML because it sniffs the content type. The attacker's JavaScript executes in the context of the aero-id domain. |
| **Impact** | XSS, clickjacking, MIME-sniffing attacks. |
| **Recommendation** | Add a `SecurityHeaders` middleware to the Gin engine chain that sets: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: strict-origin-when-cross-origin`, `Permissions-Policy: geolocation=(), microphone=(), camera=()`. Add HSTS only when TLS is configured (for dev, skip it). Consider CSP if the web app uses inline scripts. |
| **Effort** | S (write middleware, add to router chain) |

---

### FINDING-08: Presigned Download URL Bypasses Database Authorization

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **Medium** |
| **Title** | Public download endpoint does not verify file exists or is owned by the URL grantee |
| **Location** | `internal/handler/http/file.go` line 148-157: `h.Download` handler |
| **Description** | The `GET /files/dl` endpoint verifies only the HMAC signature of the presigned URL and then streams the object from storage by key. It does NOT check the database to verify: (a) the file exists, (b) it hasn't been soft-deleted, (c) the downloader is the owner or has been shared the file. While the presigned URL is only generated by `DownloadURL` which checks ownership, once generated the URL can be forwarded to anyone. The 5-minute TTL limits the window, but within that window, the URL authorizes unconditional access to the storage blob. |
| **Attack Scenario** | Alice generates a download URL for her private file and shares it with Bob. Bob forwards the URL to Charlie. Charlie accesses the file, and the service happily streams it — no check that Charlie is an authorized recipient. |
| **Impact** | Unauthorized file access within the TTL window (5 min by default, configurable). |
| **Recommendation** | Option A (best): Check the database in the download handler — verify the file key exists and is not deleted. This adds latency but ensures authorization. Option B (pragmatic): Accept this as a design trade-off (standard presigned URL behavior). Document in the URL that it must not be forwarded, and keep TTLs short. |
| **Effort** | M for Option A, S for Option B |

---

### FINDING-09: Database Password in DSN Could Leak

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Low** |
| **Title** | Database password embedded in DSN string; no DSN logging guard |
| **Location** | `internal/config/config.go` line 82-85: `DSN()` method — returns full DSN with password |
| **Description** | The `DSN()` method returns a string containing the database password. While the current codebase does not log the DSN, any future debug logging that calls `DSN()` would leak the password. The DSN is easily visible in crash logs, debug output, or test output. |
| **Attack Scenario** | An operator enables debug logging during an incident. The DSN is logged, and the database password is exposed in log aggregation systems (Splunk, Datadog, etc.). |
| **Impact** | Database credential exposure. |
| **Recommendation** | Add a `DSNSanitized()` method that replaces the password with `****` for any logging or error display. Use `DSN()` only for the actual `sql.Open()` call. |
| **Effort** | S (add method, use it in logging contexts) |

---

### FINDING-10: No CSRF Protection on Authenticated Endpoints

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | No Cross-Site Request Forgery protection |
| **Location** | Entire HTTP handler layer |
| **Description** | The API uses Bearer tokens in the `Authorization` header, which are NOT automatically attached by browsers on cross-origin requests. This inherently mitigates CSRF. However, the idempotency middleware and state-changing operations (POST/PUT/PATCH/DELETE on `/v1/*`) lack any CSRF token mechanism for cases where a client-side app might store the token in a cookie or where CORS misconfiguration could allow credentialed requests. |
| **Attack Scenario** | Scenario requires CORS misconfiguration or a cookie-based auth path. Currently mitigated by Bearer token pattern. |
| **Impact** | Low (mitigated by Bearer token design). |
| **Recommendation** | Document that Bearer tokens are the CSRF protection strategy. Ensure no cookie-based auth paths are ever added without corresponding CSRF tokens. Add a linter rule preventing cookie-based auth. |
| **Effort** | S (documentation + linter rule) |

---

### FINDING-11: Weak Default Signing Key in Config

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | Default signing key in config is a placeholder |
| **Location** | `configs/config.yaml` line 114: `signing_key: change-me-please-very-long-random` |
| **Description** | The committed config file has a non-random, placeholder signing key. The AGENTS.md correctly states "Secrets via env only, never committed" and the key is overridable via `AERO_ID_SIGNING_KEY`. However, the committed default is obviously non-random and would compromise HMAC presigned URL security if used in production. |
| **Attack Scenario** | An operator deploys without setting `AERO_ID_SIGNING_KEY`. The HMAC key for presigned URLs is now "change-me-please-very-long-random". An attacker can forge presigned download URLs for any storage key at any expiration time, gaining access to all files. |
| **Impact** | Complete bypass of file download authorization. All stored files are accessible to anyone who knows the secret (which is in a public git repo). |
| **Recommendation** | In `New()` or `config.validate()`, add a check that the signing key does not equal known placeholder values. Refuse to start with a weak key. Add a startup log warning if the key looks non-random. The `validate()` method already checks `SigningKey == ""` — extend it to also reject common placeholder patterns. |
| **Effort** | S (add validation check) |

---

### FINDING-12: Default Credentials in Docker Compose

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Low** |
| **Title** | Docker Compose uses default / weak credentials for all services |
| **Location** | `deployments/docker-compose.yml` — Postgres password `aero`, MinIO `minioadmin/minioadmin`, signing key `docker-dev-signing-key-change-me` |
| **Description** | The Docker Compose file sets weak/default credentials for Postgres, MinIO, and the signing key. While this is clearly a development environment, the file is committed and could be used as a template or might accidentally be deployed to production. |
| **Attack Scenario** | Developer copies docker-compose.yml to a staging server with a public IP. Postgres and MinIO are exposed on known ports with well-known passwords. Attacker scans for open Postgres/MinIO ports and compromises the database. |
| **Impact** | Data breach in non-production environments. |
| **Recommendation** | Add a clear comment at the top of the compose file: `# DEVELOPMENT ONLY — do not use in production.` Add a `WARNING` in `make run` or in the README. Consider using Docker secrets or env file overrides instead of hardcoded values. |
| **Effort** | S (comments + README note) |

---

### FINDING-13: Potential Race Condition in Soft-Delete File Access

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Tampering) |
| **Severity** | **Low** |
| **Title** | Gap between soft-delete row removal and storage blob deletion |
| **Location** | `internal/file/service_crud.go` (file delete flow) |
| **Description** | When a file is deleted, the service soft-deletes the DB row first, and the blob is only cleaned up by the GC sweep (which is OPT-IN and runs on a 1h interval by default). During this window, the storage blob still exists. If someone has a valid presigned URL generated before deletion, they can still access the blob until the URL expires. The DB-level check in `GET /v1/files/:id` correctly returns 404 for soft-deleted files, but the public download endpoint (see FINDING-08) does not check the DB. |
| **Attack Scenario** | Alice deletes a sensitive file. Bob has a presigned URL with 3 minutes remaining. Bob successfully downloads the file's content after Alice believed it was deleted. |
| **Impact** | Incomplete deletion — data persists in storage longer than users expect. |
| **Recommendation** | Accept this as a design trade-off (short TTLs mitigate the window). Add a comment in the file delete service explaining the blob deletion lag. Document that users should consider time-to-live on presigned URLs when handling sensitive data. |
| **Effort** | S (documentation) |

---

### FINDING-14: Token Leak in Access Logs via `actor_uid`

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Info** |
| **Title** | actor_uid logged on every request could include PII if SSO returns identifying UIDs |
| **Location** | `internal/middleware/access_log.go` line 30: `zap.String("actor", c.GetString("actor_uid")),` |
| **Description** | Every authenticated request logs the actor's UID. If the SSO system uses a PII-containing UID (e.g., email address, government ID number), the access logs would contain PII, creating GDPR/privacy compliance requirements for log retention. |
| **Impact** | Potential GDPR compliance issue if UIDs are PII. |
| **Recommendation** | Verify that SSO UIDs are opaque, non-PII identifiers (UUIDs, random strings). If they are PII, add a config option to hash/sanitize the UID in logs, or log only a truncated hash. |
| **Effort** | S (verification + optional config flag) |

---

### FINDING-15: No Audit Trail for Token Validation Failures

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Repudiation) |
| **Severity** | **Low** |
| **Title** | Failed authentication attempts not audited |
| **Location** | `internal/middleware/auth.go` — invalid tokens result in 401 but are not logged to the audit system |
| **Description** | Repeated failed authentication attempts (invalid/expired tokens) are only recorded in access logs, not in the audit log. Without dedicated monitoring, a credential-stuffing or brute-force attack against the auth endpoint could go undetected. The access log sampling (when `sample_ratio < 1`) could even skip logging failed attempts. |
| **Attack Scenario** | Attacker brute-forces tokens against `POST /v1/files` with different Bearer tokens. Each attempt returns 401. The access log records the failures, but with sampling at 0.1%, only 1 in 1000 failures is logged. The attack goes unnoticed. |
| **Impact** | Undetected brute-force attack against authentication. |
| **Recommendation** | Add a dedicated metrics counter for auth failures (`auth.token_invalid`). Ensure access log sampling never applies to 4xx errors (it currently says "Status >= 400 are always logged regardless of sampling" — verify this is correct). Optionally add IP-based monitoring for excessive auth failures. |
| **Effort** | S (metrics counter + verify sampling logic) |

---

### FINDING-16: Soft-Delete Messages Still Visible Through HMAC Download Link

| Field | Value |
|-------|-------|
| **Category** | Threat Model (Tampering) |
| **Severity** | **Medium** |
| **Title** | Soft-deleted files remain downloadable via pre-generated presigned URLs |
| **Location** | `internal/service/file.go` — Delete performs GORM soft delete; `internal/handler/http/file.go` — Download handler bypasses DB |
| **Description** | This is a specific instance of the general presigned-URL-revocation problem. When a file is soft-deleted, the row gets `deleted_at` set. The service correctly returns 404 for `GET /files/:id`. But the `GET /files/dl` (download) endpoint does not check the DB. Any presigned URL generated before deletion continues to work until it expires. |
| **Impact** | Deleted files remain accessible for up to the URL TTL (default 5 minutes). |
| **Recommendation** | In the `Download` handler, after verifying the HMAC signature, perform a DB lookup on the storage key to check if a live file references it. Return 404 if none exists. This adds latency but guarantees deletion is effective immediately. |
| **Effort** | M (add DB query to download handler) |

---

## 3. STRIDE Summary

| Threat | Status | Key Mitigations |
|--------|--------|-----------------|
| **Spoofing** | ✅ Good | SSO token validation; HMAC-signed presigned URLs; circuit breaker with stale cache |
| **Tampering** | ✅ Good | GORM parameterized queries; HMAC signing; audit log for sensitive mutations |
| **Repudiation** | ⚠️ Medium | Access logs for all requests; audit events for admin/block/share actions; missing: no auth-failure audit |
| **Information Disclosure** | ✅ Good | Error messages sanitized; no tokens in logs; file/message ownership enforced |
| **Denial of Service** | ❌ Needs Improvement | Rate limiter disabled by default (CRITICAL); no upload size enforcement at HTTP level; S3 presigned upload can waste storage |
| **Elevation of Privilege** | ✅ Good | Service-layer ownership checks; permission middleware for admin ops; storage key prefix validation |

---

## 4. OWASP Top 10 (2021) Coverage

| OWASP Category | Status | Notes |
|----------------|--------|-------|
| A01: Broken Access Control | ✅ Good | Ownership and permission checks at service layer |
| A02: Cryptographic Failures | ⚠️ Needs Work | No TLS on gRPC server; no TLS on HTTP at app level |
| A03: Injection | ✅ Good | Parameterized queries everywhere; no eval/exec |
| A04: Insecure Design | ⚠️ Needs Work | Rate limiting opt-in; upload size not enforced at boundary |
| A05: Security Misconfiguration | ⚠️ Needs Work | CORS defaults to `*`; no security headers |
| A06: Vulnerable Components | ✅ Good | CI runs `go vet`, `gosec`, `staticcheck`; Go 1.26 |
| A07: Authentication Failures | ✅ Good | Bearer token via SSO; token caching with TTL |
| A08: Software Integrity Failures | ✅ Good | Go module checksums; no third-party CDN |
| A09: Security Logging Failures | ⚠️ Needs Work | Auth failures not monitored; access log sampling can hide 4xx |
| A10: SSRF | ✅ Good | No request forwarding based on user input |

---

## 5. Final Summary

### Overall Security Posture: **Needs Improvement**

The codebase has strong fundamentals — service-layer authorization, parameterized queries, structured error handling, and HMAC-signed URLs are all well-implemented. However, three critical/high-severity issues in operational configuration and input validation undermine these strengths.

### Top 3 Critical Issues

1. **Rate limiter disabled by default** (FINDING-01) — The most impactful finding. Without explicit configuration, the service has **no per-actor request throttling** in production. Mitigation: change the default to `enabled: true` with conservative defaults.

2. **No TLS on either transport** (FINDING-03, FINDING-04) — Both HTTP and gRPC servers operate on cleartext TCP. All traffic — including Bearer tokens — is visible to network observers. Requires either adding TLS at the application level or making infrastructure-level TLS termination a documented hard requirement.

3. **File upload DoS via multipart parsing** (FINDING-02) — `c.FormFile()` buffers the entire upload in memory before any size validation. An attacker can exhaust server memory with concurrent oversize uploads.

### Top 3 Quick Wins

1. **Set `rate_limit.enabled: true` in the default config** (FINDING-01) — One line change, massive impact on production resilience.

2. **Add file upload size enforcement at the HTTP boundary** (FINDING-02) — Setting `MaxMultipartMemory` on the Gin router and wrapping upload handlers with `http.MaxBytesReader` prevents oversized payloads from reaching the application at all.

3. **Add security headers middleware** (FINDING-07) — ~20 lines of Go code sets `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy` on every response, blocking common attack vectors.

### Security Debt

- **Rate limiting infrastructure**: The current limiter is per-instance. A distributed Redis-backed limiter would be more robust for horizontally scaled deployments.
- **Presigned URL authorization**: The download endpoint should verify the file exists and is not deleted in the database, to ensure immediate revocation on delete.
- **Audit coverage**: Failed auth attempts and file download events are not audited. Adding these would strengthen repudiation defenses.
- **TLS automation**: Consider Let's Encrypt / ACME integration for automatic certificate management, or document reverse proxy TLS termination as a deployment prerequisite.

### Compliance Note

The codebase has no GDPR-specific mechanisms (data portability exports are partially present via `internal/domain/export.go` — not reviewed in depth). The data retention configuration for notifications and audit logs shows awareness of data lifecycle management. However, the presence of UID in access logs (FINDING-14) may require a Data Processing Impact Assessment depending on whether SSO UIDs are PII.
