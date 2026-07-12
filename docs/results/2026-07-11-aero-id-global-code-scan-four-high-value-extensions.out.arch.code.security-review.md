I've now completed a thorough examination of the codebase. Here is the comprehensive security review.

---

# Security Review: aero-id Service

## Overall Security Posture: **Needs Improvement**

The codebase shows strong security awareness overall — proper ACL checks at the service layer, consistent AppError translation, protected internal endpoints, and SSO-based authentication. However, there are several production-critical findings, notably the complete absence of TLS at the transport layer, secrets committed to config files, and a path traversal risk in the storage layer.

---

## Detailed Findings

### Finding #1: No TLS on HTTP or gRPC Server — All Traffic in Cleartext

| Field | Value |
|---|---|
| **Category** | Cryptography / Data Protection |
| **Severity** | **Critical** |
| **Title** | Transport encryption not configured for either server |
| **Location** | `/home/u1/aero-id/configs/config.yaml` (no TLS config block for server); `/home/u1/aero-id/internal/app/new.go` lines ~257-280 (HTTP server creation), ~281-305 (gRPC server creation) |
| **Description** | The HTTP server (`http.Server`) is created with zero TLS configuration — the `ListenAndServe()` call uses plaintext. Likewise, the gRPC server uses `grpc.NewServer()` without `grpc.Creds()`. The SSO *client* has a rich TLS/mTLS configuration with pinning, but the *servers themselves* are unencrypted. All API traffic (including Bearer tokens passed in `Authorization` headers) travels in cleartext. |
| **Attack Scenario** | An attacker on the same network (e.g., cloud VPC, compromised sidecar, or ISP-level) can passively sniff all traffic, extracting Bearer tokens, file content, messages, and user identity information. With a Bearer token, the attacker can impersonate any user. Active MITM can modify requests/responses. |
| **Impact** | Complete loss of confidentiality and integrity for all API traffic. Token theft leads to full account compromise. |
| **Recommendation** | Add TLS configuration blocks for both HTTP and gRPC servers in config, then configure them during server creation. Example HTTP fix: |
| | **Config addition:**
| | ```yaml
| | server:
| |   http:
| |     tls_enabled: true
| |     tls_cert_file: /etc/certs/server.crt
| |     tls_key_file: /etc/certs/server.key
| |   grpc:
| |     tls_cert_file: /etc/certs/grpc.crt
| |     tls_key_file: /etc/certs/grpc.key
| | ```
| | **Code change in `app.go`:**
| | ```go
| | if cfg.Server.HTTP.TLS.Enabled {
| |     httpServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
| |     go func() {
| |         if err := httpServer.ListenAndServeTLS(cfg.Server.HTTP.TLS.CertFile, cfg.Server.HTTP.TLS.KeyFile); err != nil {
| |             errCh <- fmt.Errorf("https serve: %w", err)
| |         }
| |     }()
| | }
| | ```
| **Effort** | M (1-3 days) — config changes + conditional TLS setup in app.go |

### Finding #2: Secrets Committed in Config File

| Field | Value |
|---|---|
| **Category** | Authentication / Data Protection |
| **Severity** | **Critical** |
| **Title** | Production credentials and secrets in plaintext in committed config file |
| **Location** | `/home/u1/aero-id/configs/config.yaml` — contains `signing_key`, `database.password`, `sso` connection details |
| **Description** | The `configs/config.yaml` file, which is committed to the repository, contains a placeholder signing key (`change-me-please-very-long-random`), database password (`aero`), and SSO connection in dev mode with `insecure: true`. While these are dev defaults, the AGENTS.md explicitly states *"Secrets via env only, never committed"* and lists `AERO_ID_DATABASE_PASSWORD`, `AERO_ID_SIGNING_KEY`, `AERO_ID_S3_ACCESS_KEY`/`_SECRET_KEY` as env-only. Despite this, the YAML file contains both the signing key and the DB password under `database.password` (which viper will override from env in production, but the committed values are still a leak vector). |
| **Attack Scenario** | A contributor or CI system with access to the repo can extract the signing key. With the signing key, an attacker can forge HMAC-presigned download URLs for any file in the local storage backend. |
| **Impact** | Presigned download URL forgery, leading to unauthorized file access for any stored file. |
| **Recommendation** | Remove all secrets from `configs/config.yaml`. Use `"CHANGEME"` placeholders that would cause startup validation to fail if not overridden. The config validation already checks `signing_key` is non-empty — augment it to reject known placeholder values: |
| | ```go
| | if c.SigningKey == "" || c.SigningKey == "change-me-please-very-long-random" || strings.HasPrefix(c.SigningKey, "CHANGE") {
| |     return fmt.Errorf("signing_key must be set to a unique, random value via AERO_ID_SIGNING_KEY env")
| | }
| | ```
| **Effort** | S (< 1 day) |

### Finding #3: Path Traversal in Local Storage via OwnerUID

| Field | Value |
|---|---|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | Storage key constructed from ownerUID without sanitization permits path traversal |
| **Location** | `/home/u1/aero-id/internal/storage/local.go` lines 50-54 (`NewKey`), 60 (`keyToPath`); `/home/u1/aero-id/internal/storage/s3.go` lines 49-52 (`buildKey`) |
| **Description** | Both storage backends construct storage keys by embedding the `ownerUID` into a path structure: `filepath.Join(ownerUID, year, month, uuid)`. The `ownerUID` is obtained from the SSO token and is never sanitized or validated before being used in file paths. If an attacker can control their SSO UID (e.g., through a compromised SSO or a UID containing `../`), they can write files outside the intended root. For the local backend, `os.Create(path)` would create files at arbitrary locations on the filesystem. |
| **Attack Scenario** | If the SSO returns a UID like `../../../etc/cron.d/malicious`, the local storage backend would create a file at `/data/files/../../../etc/cron.d/malicious` = `/etc/cron.d/malicious`, enabling remote code execution. For S3, the risk is lower but still includes namespace pollution. |
| **Impact** | Arbitrary file write on disk (local backend) → potential RCE. S3 object key manipulation. |
| **Recommendation** | Validate that `ownerUID` contains no path traversal sequences. Sanitize the UID before embedding in storage keys: |
| ```go
| func sanitizeStorageUID(uid string) string {
|     // Remove any path traversal sequences
|     uid = strings.ReplaceAll(uid, "../", "")
|     uid = strings.ReplaceAll(uid, "..\\", "")
|     // Allow only safe characters
|     sanitized := make([]byte, 0, len(uid))
|     for _, b := range []byte(uid) {
|         if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.' {
|             sanitized = append(sanitized, b)
|         }
|     }
|     return string(sanitized)
| }
| ```
| Apply this in `NewKey` before constructing the path. Also reject empty/suspicious UIDs at the service boundary. |
| **Effort** | S (< 1 day) |

### Finding #4: GORM SQL Injection via Unsafe Column Interpolation in UnreadCounter

| Field | Value |
|---|---|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | SQL injection vector in unread counter via `fmt.Sprintf` with unvalidated column name |
| **Location** | `/home/u1/aero-id/internal/repository/unread.go` lines 46, 62-64 |
| **Description** | The `increment` method uses `fmt.Sprintf` to construct a column reference: `expr := fmt.Sprintf("%s = COALESCE(unread_counts.%s, 0) + ?", column, column)`. The `column` parameter is currently always passed from trusted internal callers (`"msg_count"` or `"notif_count"`), but the method signature accepts an arbitrary string. A single refactoring mistake or future change that passes user-controlled input would create a SQL injection vulnerability. Additionally, the `readCounter` method uses a hardcoded column check at lines 79-85, but the SQL expression itself could still be exploited if `column` were tainted. |
| **Attack Scenario** | Currently not exploitable because `column` is hardcoded. However, if a future developer adds a code path where the column name is derived from user input (e.g., a generic counter endpoint with a query parameter), an attacker could inject SQL via the column name. |
| **Impact** | Potential data exfiltration, modification, or deletion from PostgreSQL. |
| **Recommendation** | Use a whitelist of allowed column names instead of direct interpolation: |
| ```go
| var allowedColumns = map[string]bool{"msg_count": true, "notif_count": true}
| func (c *UnreadCounter) increment(ctx context.Context, tx *gorm.DB, uid, column string, delta int64) error {
|     if !allowedColumns[column] {
|         return fmt.Errorf("invalid column: %s", column)
|     }
|     ...
| }
| ```
| **Effort** | S (< 1 day) |

### Finding #5: S3 Server-Side Encryption Not Configured

| Field | Value |
|---|---|
| **Category** | Cryptography / Data Protection |
| **Severity** | **High** |
| **Title** | S3 backend stores objects without server-side encryption |
| **Location** | `/home/u1/aero-id/internal/storage/s3.go` — all PutObject/PresignPut calls |
| **Description** | The S3 storage client uploads objects without specifying any server-side encryption (SSE-S3, SSE-KMS, or SSE-C). Neither `configs/config.yaml` nor `S3Config` struct have encryption-related fields. For data-at-rest compliance (SOC2, PCI, HIPAA), S3 buckets should have encryption enabled. While bucket-level default encryption can enforce this, writing objects without explicit SSE headers means the system relies entirely on bucket policy, which is a defense-in-depth gap. |
| **Attack Scenario** | If the S3 bucket's default encryption is misconfigured or removed (e.g., during cloud provider migration), all existing and new objects become readable at rest from the storage layer. |
| **Impact** | Exposure of file content if the storage layer is compromised. |
| **Recommendation** | Add SSE configuration support: |
| ```yaml
| storage:
|   s3:
|     sse_type: "aws:kms"  # empty/none | AES256 | aws:kms
|     sse_kms_key_id: ""   # KMS key ARN (required for aws:kms)
| ```
| Then pass it in PutObject calls: |
| ```go
| input := &s3.PutObjectInput{
|     Bucket: aws.String(s.cfg.Bucket),
|     Key:    aws.String(key),
|     Body:   cr,
| }
| if s.cfg.SSEType != "" {
|     input.ServerSideEncryption = types.ServerSideEncryption(s.cfg.SSEType)
| }
| if s.cfg.SSEKMSKeyID != "" {
|     input.SSEKMSKeyId = aws.String(s.cfg.SSEKMSKeyID)
| }
| ```
| **Effort** | S (< 1 day) |

### Finding #6: Ban Middleware Fails Open on Error

| Field | Value |
|---|---|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | Ban check failure silently allows banned users through |
| **Location** | `/home/u1/aero-id/internal/middleware/ban.go` lines 38-44 |
| **Description** | When the ban checker (`IsBanned`) returns an error (e.g., DB connection failure), the middleware explicitly assumes the user is NOT banned and proceeds: `c.Next()`. The comment acknowledges this is a "fail-open" design. During a database outage or high-load period, banned users gain unrestricted access to all authenticated endpoints, including messaging, file sharing, and commenting. |
| **Attack Scenario** | An attacker who knows they are banned waits for a time of DB degradation/replication lag and executes their banned activity (e.g., spam, harassment). The ban middleware fails open and allows them through. |
| **Impact** | Bypass of all ban enforcement during database issues. This undermines the entire moderation system. |
| **Recommendation** | Change to fail-closed: treat a ban check error as if the user IS banned, and log the error for alerting. The user can appeal to support if wrongfully blocked by an infrastructure issue. |
| ```go
| if err != nil {
|     // Fail-closed: assume banned when the check itself fails.
|     httpx.WriteError(c, apperr.Internal("ban.check_failed", "cannot verify account status"))
|     c.Abort()
|     return
| }
| ```
| Alternatively, add a configurable flag `ban_fail_open` that defaults to `false` (fail-closed) so operators can opt-in to the degraded behavior. |
| **Effort** | S (< 1 day) |

### Finding #7: SSO Token Cache Poisoning via Singleflight + Persistent Error Caching

| Field | Value |
|---|---|
| **Category** | Authentication |
| **Severity** | **High** |
| **Title** | Transient SSO errors can propagate to all concurrent token validations via singleflight |
| **Location** | `/home/u1/aero-id/internal/sso/client.go` lines 120-136 |
| **Description** | The `ValidateToken` method uses `singleflight.DoChan` to deduplicate concurrent validations of the same token. If the singleflight leader's `validateUpstream` call encounters a transient network error (timeout, connection reset), the error is returned to **all** concurrent callers waiting on the same singleflight channel. This effectively amplifies transient SSO failures: a 200ms blip could cause hundreds of concurrent requests to fail authentication simultaneously. The stale cache fallback only applies inside `validateUpstream`, not to external callers who receive the singleflight error directly. |
| **Attack Scenario** | A brief SSO network partition could cascade into a mass authentication failure, causing a denial-of-service as all active users are logged out simultaneously. An attacker could also intentionally cause SSO connection pool exhaustion. |
| **Impact** | Widespread authentication failures during transient SSO issues, potentially causing cascading failures and user distrust. |
| **Recommendation** | Inside the singleflight function, catch transient errors and attempt stale cache fallback before propagating the error: |
| ```go
| ch := c.sf.DoChan(token, func() (interface{}, error) {
|     if uid, ok := c.tokenCache.Get(token); ok {
|         return uid, nil
|     }
|     uid, err := c.validateUpstream(token)
|     if err != nil {
|         // Fall back to stale cache before propagating to all waiters
|         if uid, ok := c.tokenCache.GetStale(token); ok {
|             return uid, nil
|         }
|     }
|     return uid, err
| })
| ```
| **Effort** | S (< 1 day) |

### Finding #8: Access Log Leaks Original Request URI

| Field | Value |
|---|---|
| **Category** | Data Protection / Compliance |
| **Severity** | **Medium** |
| **Title** | Access log records raw request URI path containing sensitive query parameters |
| **Location** | `/home/u1/aero-id/internal/middleware/access_log.go` lines 20-34 |
| **Description** | The access log middleware records both `c.FullPath()` (the route pattern) and `c.Request.URL.Path` (the raw path). The `URI` field in particular logs the raw request path, which could contain sensitive identifiers. Additionally, the `actor` field records the user's UID. Combined, these create a detailed user behavior trace in the logs. While UIDs are opaque, the query parameters and paths may leak file IDs, message IDs, or other resource identifiers. |
| **Attack Scenario** | An attacker who gains access to log files (e.g., via compromised logging infrastructure, SIEM, or log aggregation service) can reconstruct user activity patterns, including which files were accessed, which conversations were read, and at what times. |
| **Impact** | User privacy violation, potential GDPR compliance issue (behavioral tracking without consent), information disclosure. |
| **Recommendation** | Log only the route pattern (`c.FullPath()`) and remove `uri` from structured logging. If the raw path is needed for debugging, sanitize it to remove resource identifiers or make it debug-only: |
| ```go
| log.Info("http_request",
|     zap.String("method", c.Request.Method),
|     zap.String("route", c.FullPath()),   // pattern only, no resource IDs
|     zap.Int("status", c.Writer.Status()),
|     zap.Duration("latency", time.Since(start)),
|     zap.String("request_id", c.GetString("request_id")),
|     // actor is okay since it's the caller's own identity
|     zap.String("actor", c.GetString("actor_uid")),
| )
| ```
| **Effort** | S (< 1 day) |

### Finding #9: Health Probe Errors Leak Infrastructure Details

| Field | Value |
|---|---|
| **Category** | Information Disclosure |
| **Severity** | **Medium** |
| **Title** | Readiness endpoint exposes raw error messages from dependency probes |
| **Location** | `/home/u1/aero-id/internal/handler/http/health.go` lines 44-53 |
| **Description** | When a readiness check fails, the error message is embedded directly into the JSON response (e.g., `"database": "unavailable: dial tcp 127.0.0.1:5432: connect: connection refused"`). This leaks internal network topology, database connection details, and error messages that could aid an attacker in reconnaissance. The health endpoints (`/healthz`, `/readyz`) are public and do not require authentication. |
| **Attack Scenario** | An attacker probing the system can call `/readyz` and learn the database host, port, and connection state. Combined with other leaked info, this reduces the effort needed for a targeted attack on the database layer. |
| **Impact** | Information disclosure aiding further attacks. |
| **Recommendation** | Sanitize probe errors in the health response. Return a generic message while logging the full error internally: |
| ```go
| if err := chk.Probe(ctx); err != nil {
|     log.Error("health probe failed", zap.String("check", chk.Name), zap.Error(err))
|     results[chk.Name] = "unavailable"
|     ...
| }
| ```
| **Effort** | S (< 1 day) |

### Finding #10: Webhook Endpoint SSRF Risk

| Field | Value |
|---|---|
| **Category** | Threat Model |
| **Severity** | **Medium** |
| **Title** | Webhook URL config accepts arbitrary destinations, enabling SSRF |
| **Location** | `/home/u1/aero-id/internal/outbox/webhook.go` lines 58-93; config at `configs/config.yaml` under `outbox.webhook.url` |
| **Description** | The webhook handler makes HTTP POST requests to a configurable URL with no validation or restriction on the destination. An operator (or attacker with `system.config` permission) could configure the webhook URL to point to internal services (e.g., `http://localhost:5432/`, `http://169.254.169.254/latest/meta-data/`). The webhook delivers structured JSON payloads containing event data, potentially leaking sensitive information to unintended recipients or triggering internal side effects. |
| **Attack Scenario** | An admin with `system.config` permission could set the webhook URL to `http://169.254.169.254/latest/meta-data/iam/security-credentials/` to exfiltrate cloud provider instance credentials via the `X-Event-Signature` header and JSON body of POST requests. |
| **Impact** | SSRF leading to cloud metadata exposure, internal service scanning, or data exfiltration. |
| **Recommendation** | Add URL validation with a blocklist of private IP ranges and cloud metadata IPs. Restrict webhook URLs to HTTPS only (unless overridden for testing). Consider adding an allow-list for webhook destinations: |
| ```go
| func validateWebhookURL(rawURL string) error {
|     u, err := url.Parse(rawURL)
|     if err != nil {
|         return err
|     }
|     if u.Scheme != "https" {
|         return fmt.Errorf("webhook URL must use HTTPS")
|     }
|     host := u.Hostname()
|     if isPrivateIP(host) || isLoopback(host) || isCloudMetadata(host) {
|         return fmt.Errorf("webhook URL must not point to private/internal hosts")
|     }
|     return nil
| }
| ```
| **Effort** | S (< 1 day) |

### Finding #11: CORS Configured as Wildcard (All Origins Allowed) by Default

| Field | Value |
|---|---|
| **Category** | Authentication / Compliance |
| **Severity** | **Medium** |
| **Title** | CORS middleware sets `Access-Control-Allow-Origin: *` when no origins configured |
| **Location** | `/home/u1/aero-id/internal/middleware/cors.go` lines 14-42 |
| **Description** | When the `origins` list is empty, the CORS middleware sets `Access-Control-Allow-Origin: *` on all requests. This allows any website to make authenticated cross-origin requests to the API. While Bearer tokens in headers work with `*` (since credentials are not cookies), this still exposes the API to any website. For production, specific origins should be allow-listed. Note: the middleware *does* return 403 for OPTIONS preflight from unlisted origins when an allow-list is configured. |
| **Attack Scenario** | A malicious website can make authenticated cross-origin requests to aero-id if the victim's browser has cached/stored the Bearer token (e.g., in localStorage or a Bearer token auto-injected by an extension). Without `SameSite` cookies this is limited, but it still increases the attack surface. |
| **Impact** | CSRF-like attacks if tokens are stored in browser-accessible storage. |
| **Recommendation** | In production config, set explicit CORS origins. Add a startup warning when no origins are configured: |
| ```go
| if len(origins) == 0 {
|     log.Warn("CORS: no origins configured — allowing all origins (*). Set cors_origins for production.")
| }
| ```
| Also set `Vary: Origin` even for wildcard mode: |
| ```go
| case allowAll:
|     c.Header("Access-Control-Allow-Origin", "*")
|     c.Header("Vary", "Origin")
| ```
| **Effort** | S (< 1 day) |

### Finding #12: Notification Rate Limiter is Per-Instance (Not Distributed)

| Field | Value |
|---|---|
| **Category** | Threat Model / Denial of Service |
| **Severity** | **Medium** |
| **Title** | Notification dispatcher rate limiter is in-memory per-instance only |
| **Location** | `/home/u1/aero-id/internal/service/notif_rate_limiter.go` (entire file); wired in `app/new.go` via `WithDispatcherRateLimiter(10)` |
| **Description** | The notification rate limiter (`notifRateLimiter`) is an in-memory sliding window counter. When multiple aero-id instances run behind a load balancer (as is standard for HA), each instance maintains its own independent rate limit counter. An attacker could send 10 notifications per minute to the same recipient *through each instance*, achieving up to N × 10 notifications per minute (where N is the instance count). |
| **Attack Scenario** | With 5 instances behind a load balancer, a spammer could send 50 notifications/minute to the same target, overwhelming the notification table and degrading the recipient's experience. |
| **Impact** | Rate limit bypass under multi-instance deployment, enabling notification spam. |
| **Recommendation** | For production multi-instance deployments, replace the in-memory limiter with a Redis-backed distributed rate limiter (using the existing Redis client connection): |
| ```go
| type redisNotifRateLimiter struct {
|     client rueidis.Client
|     rate   int
| }
| func (r *redisNotifRateLimiter) Allow(recipientUID, notifType string) bool {
|     // Use INCR + EXPIRE on a key like "notif_rate:{recipient}:{type}:{minute_window}"
| }
| ```
| Fail back to in-memory when Redis is unavailable. |
| **Effort** | M (1-3 days) |

### Finding #13: Graceful Degradation Probe Exposes Internal Timing Information

| Field | Value |
|---|---|
| **Category** | Information Disclosure |
| **Severity** | **Low** |
| **Title** | Degradation probing interval and thresholds are predictable |
| **Location** | `/home/u1/aero-id/internal/middleware/degradation.go` lines 115-157; wired in `app/new.go` with 30s interval, 3-failure threshold |
| **Description** | The degradation state manager uses fixed, publicly-documented intervals (30s probe, 3 consecutive failures before marking degraded). An attacker who understands this can time their attacks: 90 seconds of sustained DoS on SSO before the circuit breaker kicks in and the degradation state reflects the issue. |
| **Attack Scenario** | An attacker targets SSO with a burst of invalid token requests. For 90 seconds (3 probes × 30s), the system continues to call the degraded SSO, waiting for full timeout (5s) each time before failing individual requests. This wastes server resources and amplifies the DoS. |
| **Impact** | Resource exhaustion amplification during coordinated attacks. |
| **Recommendation** | Add jitter to probe intervals and make thresholds configurable with tighter defaults for critical deps. Consider adaptive thresholds based on recent error rates rather than hard counts. |
| **Effort** | M (1-3 days) |

### Finding #14: Idempotency GORM Store Table Can Grow Unbounded

| Field | Value |
|---|---|
| **Category** | Threat Model / Denial of Service |
| **Severity** | **Medium** |
| **Title** | GORM-backed idempotency store accumulates rows until 1-hour cleaner runs |
| **Location** | `/home/u1/aero-id/internal/idempotency/store_redis.go` (Redis store has TTL), `/home/u1/aero-id/internal/app/new.go` lines 173-184 (cleaner runs every 1 hour) |
| **Description** | When Redis is unavailable, the idempotency store falls back to GORM (PostgreSQL). Each idempotent request creates a row with a 24-hour TTL. The cleaner runs only every 1 hour. Under high write volume, the `idempotency_keys` table can grow significantly between cleanup cycles, potentially causing query degradation and storage bloat. |
| **Attack Scenario** | An attacker sends 10,000 unique idempotent POST requests, creating 10,000 rows in the idempotency table. If repeated, the table grows large enough to slow down GORM queries and consume excessive storage. |
| **Impact** | Storage bloat, potential query performance degradation. |
| **Recommendation** | Reduce cleaner interval in GORM mode or add row count alerting. Consider a per-key TTL column that the cleaner can use more aggressively: |
| ```diff
| - stopIdemClean = gormStore.RunCleaner(time.Hour)
| + stopIdemClean = gormStore.RunCleaner(5 * time.Minute)
| ```
| **Effort** | S (< 1 day) |

### Finding #15: LIKE-Based Search Fallback — Parameterized (Safe) But Worth Noting

| Field | Value |
|---|---|
| **Category** | Input Validation |
| **Severity** | **Low** (Informational) |
| **Title** | LIKE search uses `ILIKE` with parameterized pattern — properly safe, but pattern construction could be optimized |
| **Location** | `/home/u1/aero-id/internal/repository/message.go` lines 407-417; `/home/u1/aero-id/internal/repository/comment.go` lines 447-457; `/home/u1/aero-id/internal/repository/file.go` lines 243-253 |
| **Description** | The `Search*Like` functions construct a LIKE pattern as `"%" + query + "%"` and pass it as a `?` parameter to GORM. This is properly parameterized and not vulnerable to SQL injection. However, the pattern includes no escaping of special LIKE characters (`%`, `_`). A search for `100%` would match all content starting with `100`, not just the literal `100%`. This is a functional issue, not a security vulnerability, but noted for completeness. |
| **Attack Scenario** | N/A — the query remains parameterized and safe. However, an attacker could use `%` in search queries to match more results than intended, potentially causing a wider-than-expected result set and performance impact. |
| **Impact** | Query result manipulation (non-security). |
| **Recommendation** | Escape LIKE metacharacters when constructing patterns: |
| ```go
| func escapeLike(s string) string {
|     s = strings.ReplaceAll(s, `\`, `\\`)
|     s = strings.ReplaceAll(s, `%`, `\%`)
|     s = strings.ReplaceAll(s, `_`, `\_`)
|     return s
| }
| pattern := "%" + escapeLike(query) + "%"
| ```
| **Effort** | S (< 1 day) |

---

## STRIDE Threat Model Summary

| Category | Risk Level | Key Findings |
|---|---|---|
| **Spoofing** | **High** | No TLS on servers → token theft → identity spoofing. SSO breaker fallback could serve stale identities. |
| **Tampering** | **Medium** | No TLS on servers → MITM. Outbox events not integrity-protected at rest. Presigned URL signatures (properly implemented). |
| **Repudiation** | **Low** | Audit logging present for all sensitive operations (admin, bans, shares). Good. |
| **Information Disclosure** | **High** | Health probes leak DB connection errors. Access log records full UIDs and paths. Config file committed with secrets. |
| **Denial of Service** | **Medium** | Idempotency GORM store unbounded growth. SSO singleflight amplifies transient failures. Notification rate limiter not distributed. |
| **Elevation of Privilege** | **Low** | Service-layer ACL checks appear correct. Admin role granting properly restricts self-grant. Ban fails open (degraded security). |

---

## Final Summary

### Overall Security Posture: **Needs Improvement**

The system has strong architectural security properties:
- ✅ Service-layer authorization on every operation (ownership checks, role-based)
- ✅ Consistent error handling that never leaks raw DB errors to clients
- ✅ SSO-based authentication with SSO client-side TLS/mTLS support
- ✅ Audit logging for all sensitive operations
- ✅ Proper HMAC presigned URL verification
- ✅ UUIDv7 IDs (no enumerable sequential IDs)
- ✅ Rate limiting architecture (even if not distributed)
- ✅ Graceful degradation awareness

However, the critical transport-layer encryption gap and config-secrets-in-repo issues must be addressed before production deployment.

### Top 3 Critical Issues (Must Fix Before Production)

1. **No TLS on HTTP/gRPC servers** — All traffic and Bearer tokens in cleartext on the wire
2. **Secrets committed in config file** — Signing key and DB password in version control
3. **Path traversal via ownerUID in storage** — Arbitrary file write on local backend

### Top 3 Quick Wins (High Impact, Low Effort)

1. **Ban middleware fail-closed** — Change single condition, prevents banned user bypass during outages (`internal/middleware/ban.go` line 42: replace `c.Next()` with 403 abort)
2. **Sanitize health probe errors** — Generic error messages instead of raw DB connection details (`internal/handler/http/health.go`)
3. **Remove raw URI from access log** — Prevent sensitive identifier leakage (`internal/middleware/access_log.go` line 24: remove `zap.String("uri", ...)`)

### Security Debt

The following issues should be addressed in subsequent sprints:

| Issue | Priority | Effort | Notes |
|---|---|---|---|
| S3 server-side encryption (SSE) | Medium | S | Compliance requirement for SOC2/HIPAA |
| Distributed notification rate limiter | Medium | M | Required for multi-instance deployments |
| Idempotency GORM cleaner interval reduction | Low | S | Prevents storage bloat under write-heavy loads |
| LIKE search escape metacharacters | Low | S | Prevents unintended query expansion |
| CORS origin validation with warning | Low | S | Defense-in-depth for browser-based access |
| SSO cert pinning rotation strategy | Low | S | Operational documentation needed |
| Webhook URL SSRF protection | Medium | S | Defense-in-depth for admin endpoints |
| SQL injection hardening for unread counter | Medium | S | Future-proofing via column allow-list |
