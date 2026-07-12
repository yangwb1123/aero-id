Now I have a thorough understanding of the codebase. Here is the complete security review:

---

# Security Review: aero-id

| Field | Value |
|---|---|
| **Reviewer** | Principal Security Engineer |
| **Codebase** | aero-id — Go service for data owned by accounts |
| **Review Date** | 2026-07-12 |
| **Codebase Age** | ~30 migrations deep, mature codebase |
| **Go Version** | 1.26 |

---

## 1. Authentication & Authorization

### Finding AUTH-1: Missing Authorization Check at Service Layer for Admin GrantRole/RevokeRole
| | |
|---|---|
| **Category** | Authorization |
| **Severity** | High |
| **Title** | Service-layer permission bypass on GrantRole/RevokeRole |
| **Location** | `internal/service/admin.go` lines 62-92 (GrantRole), 98-118 (RevokeRole) |
| **Description** | The `adminService.GrantRole` and `RevokeRole` methods rely entirely on the HTTP/gRPC middleware for permission enforcement (`RequirePermission(PermSystemConfig, ...)`). Neither method re-verifies the caller's permission at the service layer before executing the role mutation. In contrast, `banService.Ban` DOES call `s.admin.HasPermission(...)` at the service layer as defense-in-depth. This inconsistency means any new transport, CLI command, or internal code path that directly calls `adminService.GrantRole` without first running the middleware check will have no authorization gate. |
| **Attack Scenario** | A future internal batch job or a gRPC-streaming handler calls `adminService.GrantRole(ctx, "attacker-controlled-uid", "admin")` without the middleware permission check. The service layer blindly grants the role. |
| **Impact** | Privilege escalation — any code path reaching GrantRole/RevokeRole without middleware can assign arbitrary roles. |
| **Recommendation** | Add `s.HasPermission(ctx, actor, domain.PermSystemConfig)` at the top of both `GrantRole` and `RevokeRole` in the service layer, matching the pattern used by `banService.Ban`. |
| **Effort** | S (< 1 day) |

### Finding AUTH-2: Stale Token Serving During SSO Outage
| | |
|---|---|
| **Category** | Authorization |
| **Severity** | High |
| **Title** | Token cache stale TTL extends revocation window |
| **Location** | `internal/sso/client.go` (validateUpstream), `internal/sso/token_cache.go` |
| **Description** | When SSO is unreachable and the circuit breaker is open, `validateUpstream` falls back to `tokenCache.GetStale(token)` which serves tokens that have already expired — for up to `StaleTTL` (default config: 300s) past the SSO-provided expiry. The base token cache TTL is 60s. This means a deactivated/revoked user account at SSO continues to be authenticated by aero-id for up to 6 minutes (60s cache + 300s stale window) during an SSO outage. By design but risk boundary is wide. |
| **Attack Scenario** | SSO suffers a 2-minute outage. An attacker who stole a valid token 4 minutes ago (token now revoked at SSO) still has access because the stale cache serves it. |
| **Impact** | Authentication bypass for recently-revoked users during SSO degradation periods. |
| **Recommendation** | Reduce default `stale_ttl` from 300s to 60s or lower. Consider logging a security event when stale cache is used so operations can audit for abuse patterns. |
| **Effort** | S (< 1 day) |

### Finding AUTH-3: Ban Check Fails Open on DB Error
| | |
|---|---|
| **Category** | Authorization |
| **Severity** | High |
| **Title** | Ban middleware fails open — DB errors bypass ban enforcement |
| **Location** | `internal/middleware/ban.go` lines 37-41 |
| **Description** | The `Ban` middleware catches errors from `checker.IsBanned(...)` and silently proceeds with `c.Next()` — i.e., assuming NOT banned when the check fails. The comment says "err on the side of caution and assume the user is NOT banned (fail-open)." This is the wrong failure mode for security: a banned user experiencing a transitory DB blip can bypass the ban. |
| **Attack Scenario** | A banned user floods the DB connection pool with requests until a query times out. During the transient DB failure, all ban checks return error → middleware allows the request → banned user performs prohibited actions (sending messages, uploading files). |
| **Impact** | Banned users can defeat the ban by inducing or coinciding with DB errors. |
| **Recommendation** | Change to fail-closed: on error from `IsBanned`, abort with `503 Service Unavailable` and log a security warning. The service is degraded anyway if it cannot enforce bans. Alternatively, cache the ban status in-memory with a short TTL so the check degrades to the last-known state instead of open. |
| **Effort** | M (1-3 days) — requires in-memory ban cache design or a decision to 503 on error |

---

## 2. Input Validation

### Finding INP-1: Path Traversal in Local Storage via `storage_key`
| | |
|---|---|
| **Category** | Input Validation |
| **Severity** | High |
| **Title** | Path traversal in CompleteUpload — insufficient storage_key validation |
| **Location** | `internal/service/file_presign.go` lines 67-68 (`strings.HasPrefix` check) and `internal/storage/local.go` lines 116-128 for Stat, similar pattern in Get/Delete |
| **Description** | The `CompleteUpload` service validates the user-supplied `StorageKey` with `strings.HasPrefix(in.StorageKey, actor+"/")` — only checking the prefix. An attacker can craft `actorUID/../../../etc/passwd` as the storage key. While `filepath.Join` normalizes the path, the prefix check passes, and the resulting path escapes the storage root. The `Local.Stat()` method called by `CompleteUpload` issues `os.Stat()` on the resolved path, enabling filesystem metadata probing. The same traversal risk exists in any code path that uses user-influenced keys with `keyToPath()`. |
| **Attack Scenario** | An attacker calls `CompleteUpload` with `storage_key: "victim-uid/../../../../etc/shadow"`. `Local.Stat()` resolves to `/storage/root/etc/shadow`. The attacker cannot read the file (Stat only returns metadata) but can probe file existence, size, and modification times — a reconnaissance tool. If combined with other paths that call `Get` or `Delete` with a user-supplied key, full file read/delete is possible. |
| **Impact** | Filesystem enumeration via `os.Stat` at minimum. Potential path traversal in other callers of `Get`/`Delete` if user-supplied keys reach them. |
| **Recommendation** | In `CompleteUpload`, after the prefix check, also call `filepath.Clean()` on the key relative to the storage root and verify it still starts with the storage root. Better: never accept user-supplied storage keys; require that keys are system-generated (via `NewKey` which produces `ownerUID/year/month/id`). The PresignUpload pattern already generates keys server-side — make `CompleteUpload` also require a key that was previously generated by the server. |
| **Effort** | S (< 1 day) — add a `strings.Contains(in.StorageKey, "..")` rejection alongside the prefix check, or verify the cleaned path is still within the expected directory |

### Finding INP-2: No Schema Validation on Custom Metadata (`Meta`)
| | |
|---|---|
| **Category** | Input Validation |
| **Severity** | Medium |
| **Title** | Arbitrary JSON metadata accepted without schema validation |
| **Location** | `internal/service/file.go` (Create), `internal/service/file_share.go` (UpdateMetadata), all handlers accepting `meta map[string]any` |
| **Description** | Files accept an arbitrary `Meta` field of type `map[string]any` which is stored directly as JSONB in PostgreSQL. There is no schema validation, no deep nesting limit, no key naming convention, and no type constraints. An attacker can store deeply nested JSON objects that could cause DB index bloat, decompression bombs in JSONB, or cross-service data corruption if other systems consume this `Meta` field. Additionally, the `UpdateMetadata` handler merges user `Meta` with existing `Meta` — an attacker can overwrite existing operational keys (e.g., those set by pipeline workers) if they guess or discover key names. |
| **Attack Scenario** | An attacker uploads a file with `meta: {"a": {"b": {"c": ... 1000 nested levels}}}` causing a massive JSON document stored in the Meta column. When listed or returned, this payload is serialized into API responses, potentially causing serialization failures, response delays, or OOM on the client. |
| **Impact** | Data quality degradation, potential performance impact on serialization, potential overwriting of system-internal metadata keys. |
| **Recommendation** | Implement a `MetaValidator` that enforces: max depth (e.g., 5 levels), max keys (e.g., 50), max value length per key (e.g., 1KB), and disallows certain key patterns reserved for system use. Validate in the service layer before persisting. |
| **Effort** | M (1-3 days) — design meta schema, add validation, update tests |

---

## 3. Cryptography

### Finding CRYPTO-1: Plaintext Token Storage in In-Memory Cache
| | |
|---|---|
| **Category** | Cryptography |
| **Severity** | High |
| **Title** | Bearer tokens cached in plaintext in process memory |
| **Location** | `internal/sso/token_cache.go` — entire file; token stored as `map[string]tokenEntry` |
| **Description** | The `TokenCache` stores raw bearer tokens as map keys in plaintext in the process memory space. A process memory dump (via SIGQUIT, OOM killer coredump, `/proc/pid/mem` with sufficient privileges, or a compromised sidecar) reveals all cached tokens. Tokens are the primary authentication credential for the SSO. |
| **Attack Scenario** | An attacker with `ptrace` or `/proc/<pid>/mem` access (e.g., via a shared container or debug sidecar) reads the process heap and extracts all cached bearer tokens. They can then impersonate any user whose token was cached. |
| **Impact** | Complete account takeover for any user whose token was in the cache. |
| **Recommendation** | Store a SHA-256 hash of the token as the map key instead of the raw token, and clear the original token string from memory after hashing (zero the byte slice). This way the cache maps `sha256(token) → uid` and a memory dump cannot recover the token. |
| **Effort** | M (1-3 days) — minimal change to token_cache.go, add hashing at cache insertion point |

### Finding CRYPTO-2: No File Content Integrity Verification on Download
| | |
|---|---|
| **Category** | Cryptography |
| **Severity** | Medium |
| **Title** | SHA-256 checksum computed on upload but never verified on download |
| **Location** | `internal/service/file.go` (Create — computes checksum), `internal/handler/http/file.go` (Download — no checksum verification), `internal/service/file_presign.go` (DownloadURL — returns presigned URL with no hash) |
| **Description** | During upload (`Create`), the service computes a SHA-256 checksum of the file content and stores it in the `checksum` column. However, no code path verifies this checksum when serving the file to a client, and the checksum is not included in the API response (the `fileResponse` DTO omits it). The presigned URL returned by `DownloadURL` has no `Content-MD5` or any integrity hash. An attacker (or compromised storage backend) could silently modify stored files without detection. |
| **Attack Scenario** | A storage bucket is compromised or an insider modifies a file. The user downloads the modified file. Neither the server nor the client has any mechanism to detect the tampering. |
| **Impact** | Silent data corruption / tampering of stored files goes undetected by both server and client. |
| **Recommendation** | 1. Include the `checksum` in the `fileResponse` DTO so clients can verify after download. 2. Add an optional `X-Content-Checksum` response header when serving files through the local download handler. 3. For S3 presigned URLs, consider using `ContentMD5` in the `PresignGetObject` input to enforce integrity at the S3 level. |
| **Effort** | S (< 1 day) for exposing checksum in DTO; L (> 3 days) for full end-to-end verification pipeline |

### Finding CRYPTO-3: Dev Config Hardcodes Secrets
| | |
|---|---|
| **Category** | Cryptography |
| **Severity** | Medium |
| **Title** | Database password and signing key hardcoded in committed config file |
| **Location** | `configs/config.yaml` — `database.password: aero`, `signing_key: change-me-please-very-long-random` |
| **Description** | The default configuration file contains a database password (`aero`) and a signing key placeholder that, if not overridden, provides no cryptographic protection. While AGENTS.md states "Secrets via env only, never committed," the config file is committed with placeholder values. If a deployment uses the default config without overriding `AERO_ID_DATABASE_PASSWORD` and `AERO_ID_SIGNING_KEY`, the HMAC-based presigned URL signing key would be `change-me-please-very-long-random` — trivially guessable, allowing anyone to forge presigned download URLs. |
| **Attack Scenario** | A developer deploys with the default config. An external attacker can forge presigned download URLs for any local-storage file using the known signing key. |
| **Impact** | Unauthenticated arbitrary file download from local storage. |
| **Recommendation** | Replace the default values with a clear comment pointing to the env-var override mechanism (already documented). Add a startup validation that warns if the signing key is the placeholder value. In dev config, use a `{{ env "AERO_ID_SIGNING_KEY" }}` template or `""` empty string that forces env-var configuration. |
| **Effort** | S (< 1 day) |

---

## 4. Session Management

### Finding SESS-1: No Session-Level Token Revocation
| | |
|---|---|
| **Category** | Session Management |
| **Severity** | Info |
| **Title** | No local session invalidation on logout (by design) |
| **Location** | Entire codebase |
| **Description** | aero-id has no logout endpoint. Token revocation is SSO's responsibility. Once a token is validated and cached by aero-id, it remains accepted until the cache TTL + stale window expire, even if the user "logs out" at SSO. This is by design (stateless architecture), but operators should be aware. |
| **Attack Scenario** | A user logs out at SSO believing their session is terminated, but their token is still cached in aero-id's memory for up to 360s (60s cache + 300s stale). Any in-flight requests during this window are still authorized. |
| **Impact** | Limited — the token TTL bounds the exposure window. Acceptable risk. |
| **Recommendation** | Document the logout→invalidation delay for operators. Consider adding a webhook from SSO to proactively invalidate cached tokens on revocation. No code change needed. |
| **Effort** | N/A |

---

## 5. Data Protection

### Finding DATA-1: Actor UID Logged in Plaintext in Access Logs
| | |
|---|---|
| **Category** | Data Protection |
| **Severity** | Low |
| **Title** | `actor_uid` logged in plaintext for every authenticated request |
| **Location** | `internal/middleware/access_log.go` line 31 — `zap.String("actor", c.GetString("actor_uid"))` |
| **Description** | Every authenticated request logs `actor_uid` in the access log. While UIDs are not PII per se (they are opaque identifiers), they can be used to link requests to specific user activity. If access logs are shipped to a third-party analytics or SIEM system without adequate controls, a user's activity pattern across the system is exposed. |
| **Attack Scenario** | An attacker gains read access to the log aggregation system (e.g., Grafana Loki, Elasticsearch, or syslog). They can reconstruct which user performed which actions based on the `actor_uid` field. |
| **Impact** | Privacy exposure — user activity correlation. Low severity since UIDs are already exposed in API responses. |
| **Recommendation** | Make `actor_uid` logging configurable with a `log_actor_uid` boolean flag. For high-privacy deployments, it should default to off, logging only the `request_id` for correlation. |
| **Effort** | S (< 1 day) |

### Finding DATA-2: Idempotency Store Caches Full Response Bodies
| | |
|---|---|
| **Category** | Data Protection |
| **Severity** | Medium |
| **Title** | Idempotency records include full response bodies in database/Redis |
| **Location** | `internal/idempotency/store.go` (GormStore), `internal/idempotency/store_redis.go` (RedisStore) |
| **Description** | The idempotency middleware stores complete HTTP response bodies (headers + body) in the database (GORM) or Redis keyed by `idempot:{uid}:{endpoint}:{key}`. Sensitive data returned in API responses (e.g., file download URLs containing storage keys, user information in enriched lists, etc.) is persisted in plaintext with a TTL of up to 24 hours. This creates an additional data persistence layer that operators may not be aware of for GDPR data deletion requests or security audits. |
| **Attack Scenario** | An attacker with database read access (e.g., a SQL injection in another part of the stack, a backup leak, or a compromised DBA account) can read idempotency records and reconstruct API responses containing potentially sensitive data. |
| **Impact** | Sensitive API response data stored for up to 24h in secondary storage, complicating data deletion compliance. |
| **Recommendation** | 1. Document the idempotency store as a data retention surface in the privacy architecture. 2. Consider scrubbing sensitive fields from stored responses (or store only the status code). 3. Ensure the cleanup/purge mechanism runs frequently and is tested. |
| **Effort** | M (1-3 days) — design scrubbing logic, add tests |

---

## 6. Threat Model — STRIDE Analysis

| Threat | Rating | Finding |
|---|---|---|
| **Spoofing** | 🟡 Medium | Token cache stores raw tokens (CRYPTO-1). Stale cache serves revoked tokens (AUTH-2). Token→UID binding is trusted from SSO but delayed revocation has a window. |
| **Tampering** | 🟡 Medium | No integrity verification on file download (CRYPTO-2). Content dedup reuses storage keys but checksums are never verified. |
| **Repudiation** | 🟢 Good | Audit logging implemented for ban, admin, file share/unshare, message delete operations. Audit log has its own table and TTL-cleaner. |
| **Information Disclosure** | 🟡 Medium | Path traversal allows filesystem probing (INP-1). CORS defaults to permissive (CORS-*) when origins not configured. `actor_uid` logged in plaintext. Presigned URLs contain storage keys in query parameters. |
| **Denial of Service** | 🟡 Medium | Rate limiting OPT-IN and disabled by default (DOS-1). No distributed rate limiting — per-instance limiter can be bypassed by spraying across instances. No body size limit on gRPC beyond max recv msg size. |
| **Elevation of Privilege** | 🟡 Medium | Service layer missing permission re-check on GrantRole/RevokeRole (AUTH-1). Admin endpoints have multiple middleware guard layers (good) but service layer gap weakens defense-in-depth. Ban fails open (AUTH-3). |

### Finding STRIDE-1: Rate Limiting is OPT-IN and Disabled by Default
| | |
|---|---|
| **Category** | Denial of Service |
| **Severity** | High |
| **Title** | Application-layer rate limiting requires explicit enabling |
| **Location** | `configs/config.yaml` — `rate_limit.enabled: false`; `internal/app/new.go` lines 141-144 — `if cfg.RateLimit.Enabled { ... }` |
| **Description** | The rate limiter is explicitly OPT-IN and disabled in the default config. A production deployment that uses the default config without explicitly setting `rate_limit.enabled: true` has NO application-layer DoS protection. The rate limiter is also per-instance, not distributed — an attacker sending requests across multiple instances can bypass the per-instance token bucket. |
| **Attack Scenario** | An attacker launches a credential-stuffing attack against the login proxy (SSO, not aero-id directly) or a resource-exhaustion attack against aero-id's file-upload endpoint. Without rate limiting, each instance accepts unlimited requests, overwhelming the SSO client, database connections, and storage backends. |
| **Impact** | Service degradation or outage under attack, resource exhaustion across downstream dependencies. |
| **Recommendation** | 1. Enable rate limiting by default in production config templates. 2. Add a startup warning if rate limiting is disabled. 3. For multi-instance deployments, consider a Redis-backed distributed rate limiter (the codebase already has Redis infrastructure). |
| **Effort** | S (< 1 day) for config change; L (> 3 days) for distributed rate limiter |

### Finding STRIDE-2: gRPC Reflection Can Expose Full RPC Surface
| | |
|---|---|
| **Category** | Information Disclosure |
| **Severity** | Medium |
| **Title** | gRPC reflection service enumerates all RPC methods when enabled |
| **Location** | `internal/handler/grpc/server.go` — `infraMethod` bypasses auth for `/grpc.reflection.*` |
| **Description** | When `server.grpc.reflection_enabled` is true, the gRPC reflection service is registered. Reflection methods bypass the auth interceptor (classified as `infraMethod` returning true). While the config defaults to `false`, if an operator enables reflection for debugging, any client that can reach the gRPC port can enumerate all available RPC methods without authentication. This exposes internal method names, input/output message schemas, and provides the full attack surface for gRPC brute-force or fuzzing. |
| **Attack Scenario** | An attacker connects to the gRPC port (9090) with `grpcurl -plaintext` and enumerates all services: `grpcurl localhost:9090 list`. They can see `aero_id.v1.AdminService`, `aero_id.v1.FileService`, etc., revealing the full API surface for targeted attacks. |
| **Impact** | Attack surface enumeration — internal RPC method names and proto schemas become known to attackers. |
| **Recommendation** | 1. Strongly recommend that reflection remains off in production. 2. If reflection is needed for debugging, restrict gRPC listener to localhost or require mTLS client certificates. 3. Add a security-audit log entry when the gRPC server starts with reflection enabled. |
| **Effort** | S (< 1 day) — config validation and logging |

### Finding STRIDE-3: No Security Headers on HTTP Responses
| | |
|---|---|
| **Category** | Information Disclosure |
| **Severity** | Low |
| **Title** | Missing security-related HTTP response headers |
| **Location** | `internal/handler/http/router.go` — middleware chain, no security headers middleware |
| **Description** | The HTTP server does not set `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy`, `Strict-Transport-Security`, or `Referrer-Policy` headers. While aero-id serves a JSON API (not HTML), these headers provide defense-in-depth against content-type sniffing, clickjacking (if any UI embeds the API), and protocol downgrade attacks. |
| **Attack Scenario** | A browser makes a direct request to an API endpoint. Without `X-Content-Type-Options: nosniff`, older browsers might sniff the response body and render it as HTML, enabling XSS if the attacker can control part of the JSON response (e.g., the file name field). Without HSTS, a MiTM attacker could downgrade HTTPS to HTTP. |
| **Impact** | Defense-in-depth reduction. Low likelihood for a JSON API but no reason not to set them. |
| **Recommendation** | Add a `SecurityHeaders` middleware that sets standard headers: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `X-XSS-Protection: 0` (deprecated but defensive), `Strict-Transport-Security: max-age=31536000; includeSubDomains` (if TLS), `Referrer-Policy: no-referrer`. |
| **Effort** | S (< 1 day) |

---

## 7. Compliance Considerations

| Standard | Status | Notes |
|---|---|---|
| **OWASP Top 10 2021** | 🟡 Needs Work | A01:BAC (broken access control) — AUTH-1, AUTH-3. A03:Injection — INP-1 (path traversal). A04:Insecure Design — CRYPTO-1, CRYPTO-2. A05:Security Misconfiguration — DOS-1 (rate limiting). A07:Identification & Auth Failures — AUTH-2 (stale cache). A09:Logging & Monitoring — Acceptable with structured logging and tracing. |
| **OWASP ASVS** | 🟡 Level 2 partial | Missing V2 (session management) verification for token revocation. Missing V3 (input validation) for meta field. Missing V8 (data protection) for at-rest encryption. |
| **GDPR** | 🟡 Needs attention | User data in audit logs, idempotency records, access logs, and notification tables has TTL-based cleanup. But no data export API for users to retrieve their files (the DataExportJobs table exists via migration 000025 but not yet wired). No documented data deletion workflow beyond soft-delete. |
| **SOC2** | 🟡 Needs improvement | Audit logging exists for admin operations (ban, grant role, file share) but not for regular resource access (file reads, message reads). No session-level access logging. Ban fails open (AUTH-3) would be a SOC2 TSC (CC6.1) finding. |

---

## 8. Summary of All Findings

| # | Category | Severity | Title | Effort |
|---|----------|----------|-------|--------|
| AUTH-1 | Authorization | High | Missing service-layer permission check on GrantRole/RevokeRole | S |
| AUTH-2 | Authorization | High | Stale token cache extends revocation window to 5+ min | S |
| AUTH-3 | Authorization | High | Ban check fails open on DB error | M |
| INP-1 | Input Validation | High | Path traversal via storage_key in CompleteUpload | S |
| INP-2 | Input Validation | Medium | No schema validation on custom metadata (Meta) | M |
| CRYPTO-1 | Cryptography | High | Bearer tokens cached in plaintext in process memory | M |
| CRYPTO-2 | Cryptography | Medium | No file content integrity verification on download | S-L |
| CRYPTO-3 | Cryptography | Medium | Dev config hardcodes secrets (password, signing key) | S |
| SESS-1 | Session Mgmt | Info | No local session logout / token revocation (by design) | N/A |
| DATA-1 | Data Protection | Low | Actor UID logged in plaintext in access logs | S |
| DATA-2 | Data Protection | Medium | Idempotency store caches full response bodies with sensitive data | M |
| STRIDE-1 | DoS | High | Rate limiting is OPT-IN and disabled by default | S |
| STRIDE-2 | Info Disclosure | Medium | gRPC reflection exposes full RPC surface when enabled | S |
| STRIDE-3 | Compliance | Low | Missing security HTTP response headers | S |

---

## 9. Final Assessment

| Metric | Rating |
|---|---|
| **Overall Security Posture** | **Needs Improvement** |
| **Top 3 Critical Issues** | 1. **AUTH-3**: Ban fails open — banned users bypass enforcement on DB errors<br>2. **CRYPTO-1**: Plaintext token cache — memory dump reveals all cached tokens<br>3. **STRIDE-1**: Rate limiting OPT-IN and OFF by default — no DoS protection |
| **Top 3 Quick Wins** | 1. **INP-1** (S): Add `../` detection to `CompleteUpload` storage_key validation — 3 lines of code<br>2. **AUTH-1** (S): Add `HasPermission` check to `GrantRole`/`RevokeRole` in service layer — ~5 lines each<br>3. **STRIDE-3** (S): Add security headers middleware — 30 lines, one-time effort |
| **Security Debt** | - Plaintext token cache (needs memory scrubbing design)<br>- No content integrity verification for files (requires end-to-end fix across storage backends)<br>- No distributed rate limiting (requires Redis-backed limiter design)<br>- Inconsistent authorization checking between service and transport layers |

---

### Key Strengths of the Codebase

1. **Consistent error handling** — `WriteError` and `GRPCError` never leak internal details. Raw DB/network errors are wrapped and converted to generic messages before reaching clients.
2. **Clean architecture enforcement** — Handler (I/O) → Service (business logic + authz) → Repository (persistence) with clear separation. Authorization checks are concentrated in the service layer where possible.
3. **HMAC timing-safe comparison** — `hmac.Equal` used for presigned URL verification. Good.
4. **SSO circuit breaker** — The three-state breaker (closed/open/half-open) prevents cascading failures and provides stale fallback with clear semantic boundaries. The `isAvailabilityError` distinction (business rejection vs. network failure) is correctly implemented.
5. **Transactional outbox** — Domain events are emitted in the same DB transaction as the data mutation, preventing dual-write consistency issues.
6. **No raw SQL injection vectors** — GORM parameterized queries + raw SQL uses `?` placeholders throughout.
7. **gRPC keepalive enforcement** — Minimum ping interval configurable (default 10s) prevents abusive clients.
8. **Audit trail** — Selected security-relevant operations (bans, admin role changes, file sharing) are logged to an audit table.

### Most Critical Action Items

The **ban fail-open** (AUTH-3) is the single most dangerous issue — it turns a security control into a paper tiger under error conditions. Fixing it should be the top priority.

The **plaintext token cache** (CRYPTO-1) is the most impactful improvement — a simple SHA-256 hashing of the cache key eliminates the risk of mass token exposure from memory dumps.

The **OPT-IN rate limiting** (STRIDE-1) represents an operational risk: without it, a production deployment has no application-layer DoS protection out of the box.
