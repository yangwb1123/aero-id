Now I've cross-referenced all five directions. Here are my findings after verifying the code-level evidence:

---

## Verification Results

### ✅ Direction 1: gRPC Interceptor Test Gap — **Confirmed & Validated**

**Evidence holds.** The `server.go` interceptors (`AuthInterceptor`, `RateLimitInterceptor`, `PermissionInterceptor`, `MetricsInterceptor`, `TracingInterceptor`, `RecoveryInterceptor` — lines 67-130) have zero independent test files. The `middleware/` directory has test files but they only cover HTTP middlewares. `parity_test.go` is a handler-level integration test that only checks success + known-error-code mapping, not interceptor-specific behavior like:
- Token missing → `codes.Unauthenticated` (not tested)
- Rate limit exceeded → `codes.ResourceExhausted` (not tested)
- panic recovery → `codes.Internal` (not tested)

**Additional finding:** `PermissionInterceptor` and `MetricsInterceptor` were also not mentioned in the analysis but are equally untested.

---

### ✅ Direction 2: File Download Audit — **Confirmed**

**Evidence holds.** The `audit.AuditLogger` field (`fileService.audit`, line 29) is wired via `WithFileAuditLogger` (line 58) but **never invoked** in any read path:
- `Get()` (line 103): returns file after ACL check — no `s.audit.Log(...)`
- `DownloadURL()` in `file_presign.go` (line 16): delegates to `Get()` + `PresignGet` — no audit call
- No `AuditFileDownload` constant exists in `domain/audit.go`

The `audit_events` table (migration 000014) is ready but dormant for file access.

---

### ⚠️ Direction 3: Search Quality — **Partially Confirmed**

**Evidence 2 is outdated.** The codebase already uses `ORDER BY ts_rank(search_vector, plainto_tsquery('simple', ?)) DESC` in both:
- `internal/repository/file.go:225` (`SearchFiles`)
- `internal/repository/message.go:322` (`SearchMessages`)

Migration 000016 already adds FTS indexes on files, messages, and comments.

**However, the rest of the analysis stands:**
- ✅ No trigram indexes (`pg_trgm` GIN) for the ILIKE fallback path (`SearchFilesLike`, `SearchMessagesLike`)
- ✅ No typo tolerance / prefix matching
- ✅ No search analytics — zero references to query logging in `internal/search/`
- ✅ No autocomplete/typeahead endpoints
- ✅ File FTS is on `name` only, while `SearchFiles` uses `plainto_tsquery` which doesn't support prefix/fuzzy

---

### ✅ Direction 4: Webhook Lifecycle — **Confirmed**

**Evidence holds across the board:**
- `Event` struct (`internal/outbox/outbox.go` line 19-32) has no version field — `Payload []byte` is bare JSON
- `WebhookConfig` (`internal/outbox/webhook.go` line 18-25) is pure static config (`mapstructure` tags), no runtime management API
- `WebhookHandler.Handle()` (line 55-80) only records `published_at` / `retry_count` — no delivery detail table, no `status_code`, `duration`, `response_body` tracking
- No event schema registry exists anywhere in the codebase
- No webhook CRUD or key rotation endpoints in the HTTP or gRPC handler trees

---

### ✅ Direction 5: Share-by-Link — **Confirmed**

**Evidence holds.** The system is strictly per-user sharing:
- `file_shares` table (`000006.up.sql`) has only `(file_id, grantee_uid, owner_uid, created_at)` — no `link_token`, `expires_at`, `access_limit`, `password_hash`, `access_count`
- `Service` interface (`internal/service/service.go:174-177`) exposes only `ShareFile`/`UnshareFile` — no `CreateShareLink`/`RevokeShareLink`
- Zero references to `share_link`/`ShareLink`/`share-link` exist in the entire `internal/` tree
- `canRead()` (`file.go` line 138-149) only checks owner + `file_shares` — has no concept of link token auth

---

## Corrections to the Analysis

| Claim in Doc | Actual Code | Impact |
|---|---|---|
| Direction 3, Ev2: `ORDER BY created_at DESC` | Already `ORDER BY ts_rank(...) DESC` | Strengthens the argument — the FTS infra is mature; remaining gaps are truly in typo tolerance + analytics |
| Direction 1: lists 5 interceptors | Actually 6 (`PermissionInterceptor` was added since) | Adds one more untested interceptor to the list |

## Summary

The analysis is **high quality and well-evidenced**. Four of five directions are fully corroborated; Direction 3 is partially correct (the rank-sort improvement is already done, which only makes the remaining gaps — trigram/typo/analytics — more clearly separable work items). The document's cross-reference methodology (100+ docs) and code-line-level citations are sound. This is a solid basis for the next engineering planning session.
