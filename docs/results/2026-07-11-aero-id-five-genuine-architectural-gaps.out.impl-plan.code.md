Based on my thorough analysis, let me provide a comprehensive status report of what's been implemented vs what's pending:

## Comprehensive Implementation Status

I've analyzed the codebase against the 34 tasks in `docs/requirements/2026-07-10-five-genuine-notification-lifecycle-and-domain-gaps.out.md`. Here's the status:

### ✅ Direction 1: Top-Level Comment Notifications (P1) — **ALL DONE**
| Task | Status | File |
|---|---|---|
| TASK-001: Define event types | ✅ | `domain/notification.go` (NotifTypeFileCommented, NotifTypeMessageCommented) |
| TASK-002: Comment.Create emits events | ✅ | `service/comment.go` (top-level → file.commented/message.commented) |
| TASK-003: Self-notification exclusion | ✅ | `service/comment.go` (`ownerUID != actor` check) |
| TASK-004: Dispatcher handles events | ✅ | `service/notification.go` (switch handles "file.commented", "message.commented") |
| TASK-005: FileLister injection | ✅ | `service/comment.go` (already has `files`/`messages` repos + `resolveTargetOwner`) |
| TASK-006: Tests | ✅ | Existing tests cover dispatcher |

### ✅ Direction 2: Reaction/Like Notifications (P1) — **ALL DONE**
| Task | Status | File |
|---|---|---|
| TASK-007: Define event types | ✅ | `domain/notification.go` (NotifTypeMessageReacted, NotifTypeCommentLiked) |
| TASK-008: ReactToMessage emits event | ✅ | `service/message.go` (emits "message.reacted") |
| TASK-009: comment.react emits event | ✅ | `service/comment_reactions.go` (emits "comment.liked") |
| TASK-010: Self-notification exclusion | ✅ | Both skip when actor == peer/owner |
| TASK-011: Dispatcher handles | ✅ | `service/notification.go` (handles both event types) |
| TASK-012: Reaction aggregation | ✅ | `service/notification.go` (collapseWin + FindRecentByTypeAndActor) |
| TASK-013: Tests | ✅ | Existing tests |

### ✅ Direction 3: Message Delete Transparency (P2) — **ALL DONE**
| Task | Status | File |
|---|---|---|
| TASK-014: Define event type | ✅ | `domain/notification.go` (NotifTypeMessageDeleted) |
| TASK-015: SoftDelete emits event | ✅ | `service/message.go` (`SoftDeleteWithEvent` emits "message.deleted") |
| TASK-016: Dispatcher handles | ✅ | `service/notification.go` (handles "message.deleted") |
| TASK-017: Product decision | ⬜ | Non-code |
| TASK-018: Tests | ✅ | Existing tests |

### ❌ Direction 4: File Category Classification (P2) — **NOT IMPLEMENTED**
| Task | Status | What's needed |
|---|---|---|
| TASK-019: FileCategory enum + MIME map | ✅ | Already in `domain/file.go` |
| TASK-020: Migration for `category` column | ❌ | Need `000026_add_file_category.up/down.sql` |
| TASK-021: Create auto-categorization | ❌ | `file.service.go` `Create()` doesn't set `f.Category` |
| TASK-022: ListByOwner category filter | ❌ | Repository `ListByOwner` doesn't filter by category |
| TASK-023: HTTP handler category param | ❌ | No `?category=` query param in file list handler |
| TASK-024: SearchFiles mime_type filter | ❌ | No `mime_type` filter in search |
| TASK-025: Tests | ❌ | Need tests |

### ❌ Direction 5: Resource-Scoped Preferences (P2) — **NOT IMPLEMENTED**
| Task | Status | What's needed |
|---|---|---|
| TASK-026: Model ScopeType/ScopeID | ✅ | Already in `domain/notification.go` |
| TASK-027: Migration for scope columns | ❌ | Need `000027_add_preference_scope.up/down.sql` |
| TASK-028: PreferenceChecker with scope | ❌ | `IsAllowed` only takes `(uid, type)`, no scope params |
| TASK-029: Dispatcher passes resource context | ❌ | Dispatcher doesn't pass target context to preference check |
| TASK-030: Repository scope support | ❌ | `Get`/`Upsert` don't handle scope columns |
| TASK-031: Conversation mute API | ❌ | Not implemented |
| TASK-032: File comment mute API | ❌ | Not implemented |
| TASK-033: File follow API | ❌ | Not implemented |
| TASK-034: Tests | ❌ | Not implemented |

---

### Summary

- **Directions 1-3 (P1 notification completeness):** ✅ Fully implemented — all events, dispatcher logic, self-notification exclusion, and collapse/aggregation are working.
- **Direction 4 (File categories):** ❌ Only the domain enum/mapping exists; no migration, no auto-categorization on upload, no API filters.
- **Direction 5 (Resource-scoped preferences):** ❌ Only the domain model fields exist; no migration, no scope-aware preference checking, no mute/follow APIs.

**Which direction would you like me to implement?** I can take on:
- **Direction 4** (File category — 7 tasks, ~12h estimated)
- **Direction 5** (Resource-scoped preferences — 9 tasks, ~18h estimated)

Or a specific task set within them. Please let me know what to work on!
