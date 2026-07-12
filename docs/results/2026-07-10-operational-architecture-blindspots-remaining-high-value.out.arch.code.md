Now I have a comprehensive picture of the current implementation status. Let me summarize:

---

# Current Implementation Status Report

## Direction 1: Group Messaging (P1) — ❌ NOT STARTED
| Task | Status |
|------|--------|
| TASK-001: Group domain entities | ❌ No `domain/group.go`, no `MessageKindGroup` |
| TASK-002: Group migrations | ❌ Latest migration is 000027 — no group tables |
| TASK-003: Group repository | ❌ |
| TASK-004: Group message repository | ❌ |
| TASK-005: Group service (members) | ❌ |
| TASK-006: Group message service | ❌ |
| TASK-007: Group notification dispatch | ❌ |
| TASK-008: Group HTTP + gRPC handlers | ❌ |
| TASK-009: Group search filter | ❌ |
| TASK-010: Group integration tests | ❌ |

## Direction 2: Redis Shared State (P0.5) — ✅ PARTIALLY DONE
| Task | Status |
|------|--------|
| TASK-011: Redis client config | ✅ `internal/pkg/redis/redis.go` |
| TASK-012: Redis idempotency store | ✅ `internal/idempotency/store_redis.go` |
| TASK-013: Idempotency fallback routing | ❌ Not wired in `app.go` |
| TASK-014: Outbox `FOR UPDATE SKIP LOCKED` | ❌ |
| TASK-015: Leader election | ❌ |
| TASK-016: SSO L2 cache (Redis) | ❌ |
| TASK-017: Distributed rate limiter | ❌ |
| TASK-018: Redis health check | ❌ |

## Direction 3: File Pipeline (P1) — ❌ NOT STARTED
| Task | Status |
|------|--------|
| TASK-019: File status field | ❌ No `FileStatus` in `domain/file.go` |
| TASK-020: File upload triggers pipeline event | ❌ |
| TASK-021: Pipeline worker framework | ❌ No `internal/pipeline/` |
| TASK-022: Thumbnail generation | ❌ |
| TASK-023: Video/audio transcode stubs | ❌ |
| TASK-024: Pipeline completion callback | ❌ |
| TASK-025: GC interaction with pipeline | ❌ |

## Direction 4: Conversation Aggregation (P1.5) — ❌ NOT STARTED
| Task | Status |
|------|--------|
| TASK-026: Conversation repository query | ❌ |
| TASK-027: Conversation indexes migration | ❌ |
| TASK-028: Conversation service | ❌ |
| TASK-029: HTTP + gRPC endpoints | ❌ |
| TASK-030: Integration tests | ❌ |

## Direction 5: Webhook Platform (P2) — ❌ MOSTLY NOT STARTED
| Task | Status |
|------|--------|
| TASK-031: Webhook subscription domain + migration | ❌ |
| TASK-032: Webhook subscription repository | ❌ |
| TASK-033: Webhook management API | ❌ |
| TASK-034: Multi-subscription delivery | ❌ Single-endpoint handler exists in `outbox/webhook.go` |
| TASK-035: Delivery history | ❌ |
| TASK-036: Integration tests | ❌ |

---

**Please tell me which direction / tasks you'd like me to implement first.** I have the full architecture document and implementation plan ready to follow. I'd recommend starting with:

1. **Direction 1 (Group Messaging)** — the highest-P1 feature, completely untouched
2. **Direction 2 remaining tasks** — completing Redis shared state (SKIP LOCKED, leader election, SSO cache)
3. **Direction 4 (Conversation Aggregation)** — P1.5, pure SQL, no new dependencies

Which would you like me to begin?
